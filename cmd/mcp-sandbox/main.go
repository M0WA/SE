// Command mcp-sandbox is a first-party MCP server exposing "run_python"
// and "run_go", executing code inside a fresh, locked-down Docker
// container (see internal/adapters/dockersandbox: resource limits,
// dropped capabilities, read-only root, no network unless -network).
// Spawned as a stdio subprocess by internal/adapters/mcpclient, same
// model as cmd/mcp-web/cmd/mcp-datetime -- not a systemd service.
//
// Every flag here is admin-configured via the MCPServer row's Args --
// never the model, and never per-call: the model only supplies the code
// that runs inside an already-built container.
//
// run_python/run_go additionally take an optional file_ids argument (a
// user's own uploaded/generated file ids, from list_files) -- fetched via
// the same authenticated, per-turn SE_FILES_API_TOKEN mcp-files already
// uses (mcpclient passes it to every spawned server this turn, not just
// mcp-files), then written into the sandbox's own working directory
// (dockersandbox.RunOptions.Files) under their real filenames. This is a
// filesystem mount, independent of -network: it lets a generated script
// read any file type (a PDF via pdfplumber, a .docx via python-docx, an
// image via Pillow, whatever the model reaches for) by writing ordinary
// code against a real file on disk, rather than this server hand-building
// a bespoke extractor per format.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"searchengine/internal/adapters/dockersandbox"
	"searchengine/internal/bootstrap"
)

// maxSandboxInputFileBytes bounds one fetched input file -- an uploaded
// file can never exceed maxUploadedFileBytes (5MB, account_files.go)
// server-side, so this is just that same ceiling restated here, not a new
// policy of its own.
const maxSandboxInputFileBytes = 5 * 1024 * 1024

func main() {
	baseURL := flag.String("base-url", "http://127.0.0.1:8080", "search-server's own base URL, for fetching file_ids' content")
	network := flag.Bool("network", false, "give sandboxed containers real outbound network access (default: none)")
	memory := flag.String("memory", dockersandbox.DefaultMemory, "Docker --memory value for each sandboxed container, e.g. 512m or 1g")
	cpus := flag.String("cpus", dockersandbox.DefaultCPUs, "Docker --cpus value for each sandboxed container, e.g. 1 or 0.5")
	pidsLimit := flag.String("pids-limit", dockersandbox.DefaultPidsLimit, "Docker --pids-limit value for each sandboxed container")
	timeout := flag.Duration("timeout", dockersandbox.DefaultTimeout, "wall-clock time limit for a single run_python/run_go call")
	dns := flag.String("dns", "", "comma-separated DNS server IP(s) for a network-enabled sandbox (Docker --dns); only meaningful with -network")
	hostDNS := flag.Bool("host-dns", false, "use this host's own real upstream DNS servers inside a network-enabled sandbox, instead of Docker's default embedded DNS -- merged with -dns if both are set; only meaningful with -network")
	hostNetwork := flag.Bool("host-network", false, "run network-enabled sandboxes with Docker's --network host instead of the default bridge network -- shares the host's own network namespace outright, so DNS resolution just works with no -dns/-host-dns needed, at the cost of a bigger privilege elevation (the container can see/bind the host's own network interfaces directly); only meaningful with -network")
	flag.Parse()

	dnsServers := splitNonEmpty(*dns)
	if *hostDNS {
		detected, err := dockersandbox.DetectHostDNS()
		if err != nil {
			log.Fatalf("-host-dns: %v", err)
		}
		dnsServers = append(dnsServers, detected...)
	}

	runner := dockersandbox.New(dockersandbox.Limits{
		Memory:      *memory,
		CPUs:        *cpus,
		PidsLimit:   *pidsLimit,
		Timeout:     *timeout,
		DNS:         dnsServers,
		HostNetwork: *hostNetwork,
	})
	// SE_FILES_API_TOKEN: set by mcpclient.Provider on every spawned stdio server for a
	// signed-in role=user turn (not mcp-files-specific -- see package doc comment above). Empty
	// means no user is signed in, so a file_ids request fails with a clear message rather than a
	// confusing 401.
	files := &filesClient{
		baseURL: strings.TrimRight(*baseURL, "/"),
		token:   bootstrap.GetEnv("SE_FILES_API_TOKEN", ""),
		http:    &http.Client{Timeout: 20 * time.Second},
	}
	server := newServer(runner, *network, files)

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}

// splitNonEmpty splits a comma-separated flag value into its trimmed,
// non-empty parts -- unlike a bare strings.Split, "" produces no elements.
func splitNonEmpty(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// FileIDs is offered on both runArgs variants below, unconditionally --
// file access is a filesystem mount independent of -network, unlike
// Packages, which only works with real network access to fetch from.
type runArgs struct {
	Code    string   `json:"code" jsonschema:"the complete, runnable source code to execute"`
	FileIDs []string `json:"file_ids,omitempty" jsonschema:"optional ids (from list_files) of the current user's own files to make available to the code, written into its working directory under their real filenames"`
}

// runArgsWithPackages is runArgs plus an optional Packages field, used
// only when started with -network, so the model discovers the parameter
// exclusively when it's actually usable, never exposed-but-ignored.
type runArgsWithPackages struct {
	Code string `json:"code" jsonschema:"the complete, runnable source code to execute"`
	// Packages is model-supplied, like Code -- passed to pip/go as real
	// argv elements (see dockersandbox.RunOptions.Packages), never
	// through a shell string.
	Packages []string `json:"packages,omitempty" jsonschema:"optional package names to install before running the code -- pip package names for run_python, Go module import paths (optionally with an @version) for run_go"`
	FileIDs  []string `json:"file_ids,omitempty" jsonschema:"optional ids (from list_files) of the current user's own files to make available to the code, written into its working directory under their real filenames"`
}

// runResult is the tool's wire shape -- a small, self-describing JSON
// object so the model can read exit_code/timed_out programmatically.
type runResult struct {
	ExitCode int    `json:"exit_code"`
	TimedOut bool   `json:"timed_out"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

// newServer builds the mcp.Server exposing "run_python"/"run_go", factored
// out of main so a test can connect via an in-memory transport instead of
// a real stdio subprocess. files is nil-safe -- see resolveFiles.
func newServer(runner *dockersandbox.Runner, network bool, files *filesClient) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "mcp-sandbox", Version: "1"}, nil)

	networkNote := "This sandbox has NO network access -- any attempt to reach the network will fail."
	if network {
		networkNote = "This sandbox DOES have network access."
	}

	fileIDsNote := " Pass \"file_ids\" (ids from list_files) to make any of the current user's own files " +
		"available in the working directory under their real filenames -- this works for any file type " +
		"(a PDF, a .docx, an image, whatever), independent of network access: read it like an ordinary " +
		"local file, using whatever library the task calls for."

	pythonDesc := "Execute a Python script in a fresh, isolated sandbox and return its exit code, stdout, " +
		"and stderr. Use this to actually run code -- to check that it works, to compute something " +
		"precisely, or to verify your own reasoning -- rather than simulating execution in your head. " +
		"Only the Python standard library is guaranteed available (no pip install). Nothing persists " +
		"between calls -- each call gets a brand new sandbox with no files or state from any previous " +
		"call. " + networkNote + fileIDsNote
	goDesc := "Execute a Go program in a fresh, isolated sandbox and return its exit code, stdout, " +
		"and stderr. Use this to actually run code -- to check that it works, to compute something " +
		"precisely, or to verify your own reasoning -- rather than simulating execution in your head. " +
		"The code must be a complete, runnable 'package main' file with a main() function. Only the " +
		"Go standard library is guaranteed available -- there is no go.mod, so an import beyond stdlib " +
		"will fail to resolve even if network access is enabled. Nothing persists between calls -- each " +
		"call gets a brand new sandbox with no files or state from any previous call. " + networkNote + fileIDsNote

	if network {
		pythonDesc += " Pass \"packages\" to install extra pip packages before the script runs, if the standard " +
			"library alone isn't enough."
		goDesc += " Pass \"packages\" (Go module import paths, optionally \"@version\") to \"go get\" them into a " +
			"throwaway module before running, if the standard library alone isn't enough."
		mcp.AddTool(server, &mcp.Tool{Name: "run_python", Description: pythonDesc},
			func(ctx context.Context, req *mcp.CallToolRequest, args runArgsWithPackages) (*mcp.CallToolResult, any, error) {
				return runInSandbox(ctx, runner, files, dockersandbox.Python, args.Code, args.Packages, args.FileIDs, network)
			})
		mcp.AddTool(server, &mcp.Tool{Name: "run_go", Description: goDesc},
			func(ctx context.Context, req *mcp.CallToolRequest, args runArgsWithPackages) (*mcp.CallToolResult, any, error) {
				return runInSandbox(ctx, runner, files, dockersandbox.Go, args.Code, args.Packages, args.FileIDs, network)
			})
		return server
	}

	mcp.AddTool(server, &mcp.Tool{Name: "run_python", Description: pythonDesc},
		func(ctx context.Context, req *mcp.CallToolRequest, args runArgs) (*mcp.CallToolResult, any, error) {
			return runInSandbox(ctx, runner, files, dockersandbox.Python, args.Code, nil, args.FileIDs, network)
		})
	mcp.AddTool(server, &mcp.Tool{Name: "run_go", Description: goDesc},
		func(ctx context.Context, req *mcp.CallToolRequest, args runArgs) (*mcp.CallToolResult, any, error) {
			return runInSandbox(ctx, runner, files, dockersandbox.Go, args.Code, nil, args.FileIDs, network)
		})

	return server
}

// runInSandbox is the shared body behind both tools -- only the language
// differs. IsError is reserved for a genuine infrastructure failure; the
// sandboxed code exiting non-zero or timing out is ordinary information
// the model should see, not a tool-call failure.
func runInSandbox(ctx context.Context, runner *dockersandbox.Runner, files *filesClient, lang dockersandbox.Language, code string, packages []string, fileIDs []string, network bool) (*mcp.CallToolResult, any, error) {
	inputFiles, err := resolveFiles(ctx, files, fileIDs)
	if err != nil {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil, nil
	}
	res, err := runner.Run(ctx, dockersandbox.RunOptions{Language: lang, Code: code, Packages: packages, Network: network, Files: inputFiles})
	if err != nil {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: "sandbox infrastructure error: " + err.Error()}},
		}, nil, nil
	}
	out, err := json.Marshal(runResult{
		ExitCode: res.ExitCode, TimedOut: res.TimedOut,
		Stdout: res.Stdout, Stderr: res.Stderr,
	})
	if err != nil {
		// json.Marshal on this plain struct cannot actually fail -- handled
		// anyway rather than silently swallowed.
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: "encoding sandbox result: " + err.Error()}},
		}, nil, nil
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(out)}}}, nil, nil
}

// resolveFiles fetches each requested file id and returns them keyed by
// their real filename, ready for dockersandbox.RunOptions.Files. Returns
// nil, nil for an empty fileIDs (the common case) without touching files
// at all -- files itself may also be nil (only main() constructs a real
// one), matching every other nil-safe optional dependency in this repo.
func resolveFiles(ctx context.Context, files *filesClient, fileIDs []string) (map[string][]byte, error) {
	if len(fileIDs) == 0 {
		return nil, nil
	}
	if files == nil || files.token == "" {
		return nil, errNoToken
	}
	out := make(map[string][]byte, len(fileIDs))
	for _, id := range fileIDs {
		name, data, err := files.fetch(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("fetching file_id %q: %w", id, err)
		}
		out[name] = data
	}
	return out, nil
}

// errNoToken mirrors cmd/mcp-files' own -- no signed-in role=user session
// this turn means file_ids can't be resolved, a clear message rather than
// a confusing 401 from the HTTP call.
var errNoToken = fmt.Errorf("no signed-in user for this chat turn -- file_ids is only available when a regular user account is signed in")

// filesClient is a thin, read-only HTTP client for search-server's GET
// /account/api/files/{id} -- the same authenticated call cmd/mcp-files'
// read_file/read_file_base64 make (see this package's own doc comment for
// why this isn't a shared package with that one: a small, independently
// readable duplicate of a ~15-line helper, not worth the coupling).
type filesClient struct {
	baseURL string
	token   string
	http    *http.Client
}

// fetch returns one file's real filename (from its Content-Disposition
// header) and raw bytes, capped at maxSandboxInputFileBytes.
func (c *filesClient) fetch(ctx context.Context, fileID string) (filename string, data []byte, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/account/api/files/"+fileID, nil)
	if err != nil {
		return "", nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("calling search-server: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSandboxInputFileBytes+1))
	if err != nil {
		return "", nil, fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("server returned %d: %s", resp.StatusCode, body)
	}
	if len(body) > maxSandboxInputFileBytes {
		return "", nil, fmt.Errorf("file too large (over %d bytes)", maxSandboxInputFileBytes)
	}
	name := fileID
	if _, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); err == nil && params["filename"] != "" {
		name = params["filename"]
	}
	return name, body, nil
}
