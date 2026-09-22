// Command mcp-sandbox is a first-party MCP (Model Context Protocol) server
// exposing "run_python" and "run_go" -- native tool-calling tools that
// execute a snippet of code inside a fresh, locked-down Docker container
// (see internal/adapters/dockersandbox for the confinement this applies:
// resource limits, dropped capabilities, a read-only root filesystem, and
// no network access unless this server was started with -network). Spawned
// as a stdio subprocess by internal/adapters/mcpclient (see
// domain.MCPServer's Transport="stdio" configuration) rather than run as a
// systemd service, same operational model as cmd/mcp-web/cmd/mcp-datetime.
//
// Every flag here is admin-configured, via the MCPServer row's own Args
// (e.g. Args: ["-network", "-memory=1g"]) -- never the model, and never
// per-call: the model only ever supplies the CODE that runs inside an
// already-built container, never anything about the container itself.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"searchengine/internal/adapters/dockersandbox"
)

func main() {
	network := flag.Bool("network", false, "give sandboxed containers real outbound network access (default: none)")
	memory := flag.String("memory", dockersandbox.DefaultMemory, "Docker --memory value for each sandboxed container, e.g. 512m or 1g")
	cpus := flag.String("cpus", dockersandbox.DefaultCPUs, "Docker --cpus value for each sandboxed container, e.g. 1 or 0.5")
	pidsLimit := flag.String("pids-limit", dockersandbox.DefaultPidsLimit, "Docker --pids-limit value for each sandboxed container")
	timeout := flag.Duration("timeout", dockersandbox.DefaultTimeout, "wall-clock time limit for a single run_python/run_go call")
	flag.Parse()

	runner := dockersandbox.New(dockersandbox.Limits{
		Memory:    *memory,
		CPUs:      *cpus,
		PidsLimit: *pidsLimit,
		Timeout:   *timeout,
	})
	server := newServer(runner, *network)

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}

type runArgs struct {
	Code string `json:"code" jsonschema:"the complete, runnable source code to execute"`
}

// runResult is the tool's own wire shape -- the same "small,
// self-describing JSON object" convention cmd/mcp-datetime's get_datetime
// uses, so the model can read exit_code/timed_out programmatically rather
// than having to parse a formatted string.
type runResult struct {
	ExitCode int    `json:"exit_code"`
	TimedOut bool   `json:"timed_out"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

// newServer builds the mcp.Server exposing "run_python"/"run_go", factored
// out of main so a test can connect to it directly over an in-memory
// transport (mcp.NewInMemoryTransports) instead of exercising it only via
// a real stdio subprocess -- same pattern cmd/mcp-web/cmd/mcp-datetime
// already use.
func newServer(runner *dockersandbox.Runner, network bool) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "mcp-sandbox", Version: "1"}, nil)

	networkNote := "This sandbox has NO network access -- any attempt to reach the network will fail."
	if network {
		networkNote = "This sandbox DOES have network access, enabled by the admin who configured this server."
	}

	mcp.AddTool(server, &mcp.Tool{
		Name: "run_python",
		Description: "Execute a Python script in a fresh, isolated sandbox and return its exit code, stdout, " +
			"and stderr. Use this to actually run code -- to check that it works, to compute something " +
			"precisely, or to verify your own reasoning -- rather than simulating execution in your head. " +
			"Only the Python standard library is guaranteed available (no pip install). Nothing persists " +
			"between calls -- each call gets a brand new sandbox with no files or state from any previous " +
			"call. " + networkNote,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args runArgs) (*mcp.CallToolResult, any, error) {
		return runInSandbox(ctx, runner, dockersandbox.Python, args.Code, network)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "run_go",
		Description: "Execute a Go program in a fresh, isolated sandbox and return its exit code, stdout, " +
			"and stderr. Use this to actually run code -- to check that it works, to compute something " +
			"precisely, or to verify your own reasoning -- rather than simulating execution in your head. " +
			"The code must be a complete, runnable 'package main' file with a main() function. Only the " +
			"Go standard library is guaranteed available -- there is no go.mod, so an import beyond stdlib " +
			"will fail to resolve even if network access is enabled. Nothing persists between calls -- each " +
			"call gets a brand new sandbox with no files or state from any previous call. " + networkNote,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args runArgs) (*mcp.CallToolResult, any, error) {
		return runInSandbox(ctx, runner, dockersandbox.Go, args.Code, network)
	})

	return server
}

// runInSandbox is the shared body behind both tools -- only the language
// differs. IsError is reserved for a genuine infrastructure failure
// (docker missing, permission denied, couldn't prepare the sandbox
// workdir): the sandboxed code itself exiting non-zero, panicking, or
// timing out is ordinary, useful information the model should see and can
// act on, not a tool-call failure.
func runInSandbox(ctx context.Context, runner *dockersandbox.Runner, lang dockersandbox.Language, code string, network bool) (*mcp.CallToolResult, any, error) {
	res, err := runner.Run(ctx, dockersandbox.RunOptions{Language: lang, Code: code, Network: network})
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
		// json.Marshal on this plain, all-string/int/bool struct cannot
		// actually fail -- this exists only so the (never-reached) error
		// path is handled rather than silently swallowed.
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: "encoding sandbox result: " + err.Error()}},
		}, nil, nil
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(out)}}}, nil, nil
}
