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
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"searchengine/internal/adapters/dockersandbox"
)

func main() {
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
	server := newServer(runner, *network)

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

type runArgs struct {
	Code string `json:"code" jsonschema:"the complete, runnable source code to execute"`
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
// a real stdio subprocess.
func newServer(runner *dockersandbox.Runner, network bool) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "mcp-sandbox", Version: "1"}, nil)

	networkNote := "This sandbox has NO network access -- any attempt to reach the network will fail."
	if network {
		networkNote = "This sandbox DOES have network access."
	}

	pythonDesc := "Execute a Python script in a fresh, isolated sandbox and return its exit code, stdout, " +
		"and stderr. Use this to actually run code -- to check that it works, to compute something " +
		"precisely, or to verify your own reasoning -- rather than simulating execution in your head. " +
		"Only the Python standard library is guaranteed available (no pip install). Nothing persists " +
		"between calls -- each call gets a brand new sandbox with no files or state from any previous " +
		"call. " + networkNote
	goDesc := "Execute a Go program in a fresh, isolated sandbox and return its exit code, stdout, " +
		"and stderr. Use this to actually run code -- to check that it works, to compute something " +
		"precisely, or to verify your own reasoning -- rather than simulating execution in your head. " +
		"The code must be a complete, runnable 'package main' file with a main() function. Only the " +
		"Go standard library is guaranteed available -- there is no go.mod, so an import beyond stdlib " +
		"will fail to resolve even if network access is enabled. Nothing persists between calls -- each " +
		"call gets a brand new sandbox with no files or state from any previous call. " + networkNote

	if network {
		pythonDesc += " Pass \"packages\" to install extra pip packages before the script runs, if the standard " +
			"library alone isn't enough."
		goDesc += " Pass \"packages\" (Go module import paths, optionally \"@version\") to \"go get\" them into a " +
			"throwaway module before running, if the standard library alone isn't enough."
		mcp.AddTool(server, &mcp.Tool{Name: "run_python", Description: pythonDesc},
			func(ctx context.Context, req *mcp.CallToolRequest, args runArgsWithPackages) (*mcp.CallToolResult, any, error) {
				return runInSandbox(ctx, runner, dockersandbox.Python, args.Code, args.Packages, network)
			})
		mcp.AddTool(server, &mcp.Tool{Name: "run_go", Description: goDesc},
			func(ctx context.Context, req *mcp.CallToolRequest, args runArgsWithPackages) (*mcp.CallToolResult, any, error) {
				return runInSandbox(ctx, runner, dockersandbox.Go, args.Code, args.Packages, network)
			})
		return server
	}

	mcp.AddTool(server, &mcp.Tool{Name: "run_python", Description: pythonDesc},
		func(ctx context.Context, req *mcp.CallToolRequest, args runArgs) (*mcp.CallToolResult, any, error) {
			return runInSandbox(ctx, runner, dockersandbox.Python, args.Code, nil, network)
		})
	mcp.AddTool(server, &mcp.Tool{Name: "run_go", Description: goDesc},
		func(ctx context.Context, req *mcp.CallToolRequest, args runArgs) (*mcp.CallToolResult, any, error) {
			return runInSandbox(ctx, runner, dockersandbox.Go, args.Code, nil, network)
		})

	return server
}

// runInSandbox is the shared body behind both tools -- only the language
// differs. IsError is reserved for a genuine infrastructure failure; the
// sandboxed code exiting non-zero or timing out is ordinary information
// the model should see, not a tool-call failure.
func runInSandbox(ctx context.Context, runner *dockersandbox.Runner, lang dockersandbox.Language, code string, packages []string, network bool) (*mcp.CallToolResult, any, error) {
	res, err := runner.Run(ctx, dockersandbox.RunOptions{Language: lang, Code: code, Packages: packages, Network: network})
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
