// Package dockersandbox runs a short-lived snippet of untrusted code
// (Python or Go) inside a locked-down, ephemeral Docker container --
// the execution backend for cmd/mcp-sandbox's "run_python"/"run_go" MCP
// tools. Every invocation gets its own fresh container (no state carries
// over between calls), a memory/CPU/process-count/wall-clock-time cap
// (see Limits), no capabilities, a read-only root filesystem (a writable
// tmpfs /tmp is provided for whatever a script/build genuinely needs to
// write), and -- unless the caller opts in -- no network access at all.
// The model only ever supplies the CODE that runs *inside* the container;
// Limits is set once, by whoever constructs the Runner (an admin, via
// cmd/mcp-sandbox's own startup flags -- never the model, and never
// per-call), so there is no way for the sandboxed code to influence its
// own confinement.
package dockersandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Language selects which sandboxed runtime a Run call uses.
type Language string

const (
	Python Language = "python"
	Go     Language = "go"
)

// DefaultTimeout bounds a single Run call's wall-clock time when the
// Runner wasn't given one of its own (see Limits.Timeout) and ctx itself
// carries no deadline either.
const DefaultTimeout = 15 * time.Second

// DefaultMemory, DefaultCPUs, and DefaultPidsLimit are Limits' own
// zero-value fallbacks -- see NewRunner. DefaultMemory is 512m, not a
// tighter number, because the Go compiler itself (not just the program it
// builds) needs real headroom even for a trivial one-file "go run" with a
// cold build cache -- 256m measured as reliably OOM-killing `go run`
// before it ever got to executing the compiled program.
const (
	DefaultMemory    = "512m"
	DefaultCPUs      = "1"
	DefaultPidsLimit = "128"
)

// maxOutputBytes caps how much of stdout/stderr each is kept -- a runaway
// print loop inside the sandbox must never be allowed to exhaust this
// process's own memory, or blow up the tool result handed back to the
// chat completion call. Not part of Limits: this bounds OUR OWN memory
// use capturing output, not anything about the sandboxed container, so
// there's no operational reason an admin would need to tune it.
const maxOutputBytes = 64 * 1024

// Limits is the resource ceiling applied to EVERY sandboxed container a
// Runner creates, regardless of language -- admin-configured once, at
// process startup (see cmd/mcp-sandbox's own flags), never per-call: the
// model supplies only the code that runs inside a container already built
// to these limits, never the limits themselves. A zero Limits (Limits{})
// is valid and resolves every field to its Default* constant -- see
// NewRunner.
type Limits struct {
	// Memory is a Docker --memory value, e.g. "512m" or "1g". Also applied
	// as --memory-swap (equal to Memory), so the container gets no swap
	// headroom beyond its own memory cap.
	Memory string
	// CPUs is a Docker --cpus value, e.g. "1" or "0.5".
	CPUs string
	// PidsLimit is a Docker --pids-limit value (a plain count, as a
	// string) -- bounds how many processes/threads the sandboxed code can
	// fork, the standard guard against a fork bomb.
	PidsLimit string
	// Timeout bounds a single Run call's wall-clock time when ctx itself
	// carries no deadline of its own. Zero means DefaultTimeout.
	Timeout time.Duration
}

// withDefaults returns l with every zero-valued field resolved to its
// Default* constant.
func (l Limits) withDefaults() Limits {
	if l.Memory == "" {
		l.Memory = DefaultMemory
	}
	if l.CPUs == "" {
		l.CPUs = DefaultCPUs
	}
	if l.PidsLimit == "" {
		l.PidsLimit = DefaultPidsLimit
	}
	if l.Timeout == 0 {
		l.Timeout = DefaultTimeout
	}
	return l
}

// languageConfig describes one supported runtime: which image to run it
// in, what to name the code file inside the sandbox, the argv that
// actually executes it, and any environment variables that runtime needs
// to behave under a read-only root filesystem.
type languageConfig struct {
	image    string
	filename string
	argv     func(path string) []string
	env      []string
}

var languageConfigs = map[Language]languageConfig{
	Python: {
		image:    "python:3-slim",
		filename: "script.py",
		argv:     func(path string) []string { return []string{"python3", path} },
		// PYTHONDONTWRITEBYTECODE avoids Python even attempting a .pyc
		// write under the read-only root (harmless either way -- it just
		// silently skips caching -- but this makes the intent explicit).
		// PYTHONUNBUFFERED ensures stdout is flushed as written rather
		// than block-buffered, so a script killed by the timeout still
		// has whatever it printed up to that point captured.
		env: []string{"PYTHONDONTWRITEBYTECODE=1", "PYTHONUNBUFFERED=1"},
	},
	Go: {
		image:    "golang:1-alpine",
		filename: "main.go",
		// "go run <file>" (naming the file, not a package path) runs a
		// single standalone file with no go.mod required, as long as it
		// imports only the standard library -- there is no module
		// resolution step to need one for. An import beyond stdlib fails
		// the same way it would with no network at all: this is a
		// documented scope limit (see cmd/mcp-sandbox's own tool
		// description), not something Network=true alone fixes.
		argv: func(path string) []string { return []string{"go", "run", path} },
		// GOCACHE/GOPATH/GOMODCACHE/HOME all need to point at the
		// writable tmpfs /tmp -- go's own build/module caches try to
		// write under $HOME by default, which fails outright under
		// --read-only otherwise.
		env: []string{"HOME=/tmp", "GOCACHE=/tmp/go-cache", "GOPATH=/tmp/go-path", "GOMODCACHE=/tmp/go-mod"},
	},
}

// RunOptions is one sandboxed execution request.
type RunOptions struct {
	Language Language
	Code     string
	// Network, when true, gives the container real outbound network
	// access. False (the default across this whole package) runs with
	// --network none -- the sandboxed code can't reach anything, on the
	// host or the internet, at all.
	Network bool
}

// Result is one sandboxed execution's outcome. A non-zero ExitCode or a
// TimedOut run is NOT itself a Go error -- Run's error return is reserved
// for genuine infrastructure failures (docker missing, permission denied,
// couldn't create the sandbox workdir); the code under test simply
// failing, panicking, or running long is ordinary, useful information the
// caller (ultimately the model) should see and can act on.
type Result struct {
	ExitCode int
	TimedOut bool
	Stdout   string
	Stderr   string
}

// Runner executes RunOptions via the `docker` CLI, applying the same
// Limits to every container it creates. Otherwise stateless -- every Run
// call gets its own fresh container and temp workdir; nothing is pooled
// or reused across calls.
type Runner struct {
	limits Limits
}

// New returns a Runner enforcing limits (zero fields fall back to their
// Default* constants -- see Limits.withDefaults).
func New(limits Limits) *Runner { return &Runner{limits: limits.withDefaults()} }

// Run executes opts.Code in a fresh, locked-down container and returns
// its outcome. See the package doc comment for the confinement this
// applies unconditionally (the Runner's own Limits, dropped capabilities,
// read-only root, no network unless opts.Network) and Limits.Timeout for
// the wall-clock bound when ctx has no deadline of its own.
func (r *Runner) Run(ctx context.Context, opts RunOptions) (Result, error) {
	cfg, ok := languageConfigs[opts.Language]
	if !ok {
		return Result{}, fmt.Errorf("dockersandbox: unsupported language %q", opts.Language)
	}

	dir, err := os.MkdirTemp("", "se-sandbox-*")
	if err != nil {
		return Result{}, fmt.Errorf("creating sandbox workdir: %w", err)
	}
	defer os.RemoveAll(dir)
	// os.MkdirTemp's default mode (0700, owner-only) is unreachable from
	// inside the container: --cap-drop ALL below strips CAP_DAC_OVERRIDE,
	// so even the container's own root user (which otherwise maps
	// 1-for-1 onto host root -- this host runs no user-namespace
	// remapping) is subject to normal permission checks like anyone else,
	// and host root != this process's own UID. 0o755 makes the directory
	// itself traversable/listable by any UID; the code file's own 0o444
	// below is what actually keeps it read-only.
	if err := os.Chmod(dir, 0o755); err != nil {
		return Result{}, fmt.Errorf("preparing sandbox workdir: %w", err)
	}

	codePath := filepath.Join(dir, cfg.filename)
	// 0o444 (read-only, no write bit for anyone): the code file is only
	// ever read by the container (mounted :ro below anyway, but this is
	// defense in depth on the host side too), never modified after this
	// process writes it.
	if err := os.WriteFile(codePath, []byte(opts.Code), 0o444); err != nil {
		return Result{}, fmt.Errorf("writing sandbox code: %w", err)
	}

	runCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, r.limits.Timeout)
		defer cancel()
	}

	name := "se-sandbox-" + randomHex(8)
	args := []string{
		"run", "--name", name, "--rm",
		"--memory", r.limits.Memory, "--memory-swap", r.limits.Memory,
		"--cpus", r.limits.CPUs,
		"--pids-limit", r.limits.PidsLimit,
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges:true",
		"--read-only",
		// exec is NOT docker's --tmpfs default (noexec is) -- Go's own
		// build writes its compiled binary under $GOCACHE/$GOTMPDIR
		// (pointed at /tmp below) and then runs it directly from there;
		// Python never needs this, but the same mount serves both
		// languages, so the option is always present rather than
		// language-conditional.
		"--tmpfs", "/tmp:rw,exec,size=64m,mode=1777",
		"-v", dir + ":/sandbox:ro",
		"-w", "/sandbox",
	}
	if !opts.Network {
		args = append(args, "--network", "none")
	}
	for _, e := range cfg.env {
		args = append(args, "-e", e)
	}
	args = append(args, cfg.image)
	args = append(args, cfg.argv("/sandbox/"+cfg.filename)...)

	cmd := exec.CommandContext(runCtx, "docker", args...)
	var stdout, stderr limitedBuffer
	stdout.limit, stderr.limit = maxOutputBytes, maxOutputBytes
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	// Best-effort cleanup, unconditionally: exec.CommandContext SIGKILLs
	// the `docker run` CLI process on context expiry, but that does NOT
	// reliably stop or remove the CONTAINER it launched -- the client and
	// the container are independent from the daemon's point of view, and
	// killing the former doesn't signal the latter. Using a fresh
	// (never-canceled) context here is required: runCtx is already Done()
	// by the time a timeout is what brought us here.
	killCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = exec.CommandContext(killCtx, "docker", "rm", "-f", name).Run()
	cancel()

	result := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if runCtx.Err() != nil && errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		result.TimedOut = true
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	if runErr != nil {
		return result, fmt.Errorf("running sandbox: %w", runErr)
	}
	return result, nil
}

// randomHex returns n random bytes hex-encoded, for a unique-enough
// per-run container name (so a concurrent call's cleanup `docker rm -f`
// can never target another call's still-running container).
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand.Read practically never fails on any platform this
		// runs on; falling back to a timestamp keeps names unique enough
		// even in that vanishingly unlikely case, rather than panicking a
		// whole chat turn over a naming collision risk.
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// limitedBuffer is an io.Writer that keeps at most limit bytes, silently
// discarding (and noting) anything past that rather than growing without
// bound -- shared shape for both stdout and stderr capture in Run.
type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (w *limitedBuffer) Write(p []byte) (int, error) {
	remaining := w.limit - w.buf.Len()
	if remaining <= 0 {
		w.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		w.buf.Write(p[:remaining])
		w.truncated = true
		return len(p), nil
	}
	w.buf.Write(p)
	return len(p), nil
}

func (w *limitedBuffer) String() string {
	if w.truncated {
		return w.buf.String() + "\n... (truncated)"
	}
	return w.buf.String()
}
