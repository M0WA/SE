// Package dockersandbox runs a short-lived snippet of untrusted code
// (Python or Go) inside a locked-down, ephemeral Docker container -- the
// backend for cmd/mcp-sandbox's "run_python"/"run_go" MCP tools. Each call
// gets a fresh container with a resource cap (see Limits), no capabilities,
// a read-only root (writable tmpfs /tmp), and no network unless opted in.
// The model only supplies the code that runs inside; Limits is fixed by
// whoever constructs the Runner (admin startup flags, never the model or
// per-call), so sandboxed code can never influence its own confinement.
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
	"strings"
	"sync"
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

// DefaultMemory/CPUs/PidsLimit are Limits' zero-value fallbacks. 512m, not
// tighter, because the Go compiler itself needs headroom even for a
// trivial "go run" with a cold cache -- 256m reliably OOM-killed it before
// the program ever ran.
const (
	DefaultMemory    = "512m"
	DefaultCPUs      = "1"
	DefaultPidsLimit = "128"
)

// maxOutputBytes caps how much of stdout/stderr is kept -- a runaway print
// loop must never exhaust our own memory. Not part of Limits: this bounds
// our own output capture, not the sandboxed container, so it's not
// admin-tunable.
const maxOutputBytes = 64 * 1024

// Limits is the resource ceiling applied to every sandboxed container a
// Runner creates -- admin-configured once at process startup, never
// per-call; the model supplies only the code, never the limits. A zero
// Limits{} is valid and resolves every field to its Default* constant.
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
	// DNS is zero or more Docker --dns nameserver IPs, applied to every
	// container -- only meaningful when RunOptions.Network is true. Empty
	// leaves Docker's own embedded DNS (127.0.0.11) in place -- see
	// cmd/mcp-sandbox's -dns/-host-dns flags and DetectHostDNS.
	DNS []string
	// HostNetwork runs with Docker's --network host instead of bridge --
	// only meaningful when RunOptions.Network is true. DNS then "just
	// works" via the host's real resolv.conf (a bridge container can't
	// reach the host's systemd-resolved stub at 127.0.0.53, see DNS above).
	// A meaningfully bigger elevation than a DNS convenience -- the
	// container can bind the host's own network interfaces/ports directly
	// -- so an admin opts in explicitly (-host-network flag), never the
	// default even when Network is true.
	HostNetwork bool
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
	// installArgv builds the argv for a run that also installs packages --
	// only called when Network and Packages are both non-empty (see Run).
	// Packages become trailing args to a fixed `sh -c '<script>' sh`,
	// referenced only via "$@", never string-concatenated, so a
	// model-supplied package name can never break out of its argument.
	installArgv func(path string, packages []string) []string
	// systemInstallScript is the shell script that installs OS-level packages named in the
	// SE_SANDBOX_SYSTEM_PACKAGES env var (newline-separated, read via IFS splitting -- never
	// string-concatenated -- then handed to the real package manager as real argv elements,
	// same injection-proofing reasoning as installArgv). Run as a standalone container's entire
	// command by provisionImage, not chained with the code-running one -- see that function's
	// own doc comment for why the two never share a container.
	systemInstallScript string
	env                 []string
}

var languageConfigs = map[Language]languageConfig{
	Python: {
		image:    "python:3-slim",
		filename: "script.py",
		argv:     func(path string) []string { return []string{"python3", path} },
		// --target puts installed packages under the writable /tmp tmpfs
		// (site-packages is read-only); PYTHONPATH points the interpreter
		// there. python:3-slim already ships pip.
		installArgv: func(path string, packages []string) []string {
			script := `set -e; mkdir -p /tmp/pip-packages; pip install --quiet --no-cache-dir --target=/tmp/pip-packages "$@"; ` +
				`PYTHONPATH=/tmp/pip-packages exec python3 ` + path
			return append([]string{"sh", "-c", script, "sh"}, packages...)
		},
		// python:3-slim is Debian-based -- apt-get. -qq/--no-install-recommends keep it
		// reasonably fast; DEBIAN_FRONTEND=noninteractive avoids a debconf prompt hanging the
		// call forever on a package that has one. -o APT::Sandbox::User=root skips apt's own
		// internal privilege-drop for its download step (normally to an unprivileged _apt user)
		// -- that drop needs CAP_CHOWN/CAP_FOWNER/CAP_SETUID/CAP_SETGID just to set up its own
		// sandboxed directories, capabilities not worth granting provisionImage's already
		// isolated, single-purpose, never-runs-untrusted-code container just to satisfy a
		// redundant second layer of sandboxing on top of Docker's own. sandboxSplitEnvVarIntoArgs
		// (below) rebuilds "$@" from SE_SANDBOX_SYSTEM_PACKAGES before this ever touches a
		// package name.
		systemInstallScript: `export DEBIAN_FRONTEND=noninteractive; apt-get -o APT::Sandbox::User=root update -qq; ` +
			sandboxSplitEnvVarIntoArgs + `apt-get -o APT::Sandbox::User=root install -y -qq --no-install-recommends "$@"`,
		// PYTHONDONTWRITEBYTECODE: skip .pyc writes under the read-only root.
		// PYTHONUNBUFFERED: flush stdout as written, so a timeout-killed
		// script still has its output captured.
		env: []string{"PYTHONDONTWRITEBYTECODE=1", "PYTHONUNBUFFERED=1"},
	},
	Go: {
		image:    "golang:1-alpine",
		filename: "main.go",
		// "go run <file>" needs no go.mod as long as it imports only
		// stdlib; a non-stdlib import fails unless Packages is used too
		// (see installArgv) -- a documented scope limit only Packages lifts.
		argv: func(path string) []string { return []string{"go", "run", path} },
		// /sandbox is read-only and can't hold a go.mod, so copy the code
		// into a subdirectory of writable /tmp and init a throwaway module
		// there (not at /tmp's own top level -- Go refuses "go mod
		// init"/"go get" directly in a bare system temp root: an observed
		// "ignoring go.mod in system temp root" warning). Then "go get"
		// each requested module and run from that subdirectory.
		installArgv: func(path string, packages []string) []string {
			script := `set -e; mkdir -p /tmp/sandbox-mod; cp ` + path + ` /tmp/sandbox-mod/main.go; cd /tmp/sandbox-mod; go mod init sandbox >/dev/null 2>&1; go get "$@"; exec go run main.go`
			return append([]string{"sh", "-c", script, "sh"}, packages...)
		},
		// golang:1-alpine is Alpine-based -- apk. --no-cache skips the local package index
		// cache (pointless in a throwaway container).
		systemInstallScript: sandboxSplitEnvVarIntoArgs + `apk add --no-cache "$@"`,
		// GOCACHE/GOPATH/GOMODCACHE/HOME must point at writable /tmp -- Go's
		// caches default to $HOME, which fails under --read-only otherwise.
		env: []string{"HOME=/tmp", "GOCACHE=/tmp/go-cache", "GOPATH=/tmp/go-path", "GOMODCACHE=/tmp/go-mod"},
	},
}

// sandboxSplitEnvVarIntoArgs rebuilds "$@" from SE_SANDBOX_SYSTEM_PACKAGES (newline-separated,
// set via docker run -e, never shell-interpolated) using IFS-splitting restricted to newlines
// (set -f additionally disables globbing) -- the standard POSIX-sh way to turn a safely-passed
// string back into a real positional-parameter list without eval or arrays, so a package name
// reaches the real package manager as a genuine argv element, the same injection-proofing
// installArgv already gives Packages above.
const sandboxSplitEnvVarIntoArgs = `IFS='
'; set -f; set -- $SE_SANDBOX_SYSTEM_PACKAGES; unset IFS; set +f; `

// RunOptions is one sandboxed execution request.
type RunOptions struct {
	Language Language
	Code     string
	// Network, when true, gives the container real outbound network access.
	// False (the default) runs with --network none -- no reachability at
	// all, host or internet.
	Network bool
	// Packages is zero or more package/module names to install before
	// running Code (pip names, or Go import paths). Only takes effect when
	// Network is also true; otherwise silently ignored. Model-supplied,
	// passed to pip/go as real argv elements (see installArgv), never
	// through a shell string, so it can never inject a shell command.
	Packages []string
	// Files is zero or more input files (real filename -> raw bytes) to
	// make available to Code, independent of Network -- this is a
	// filesystem mount, not a network capability, so it works the same
	// whether or not the sandbox has outbound access. Written read-only
	// into the same bind-mounted working directory as Code, so the model's
	// own script just opens each by its ordinary filename. The caller
	// (cmd/mcp-sandbox) is responsible for sanitizing where each byte
	// slice actually came from; Run itself only guards against a filename
	// escaping the sandbox directory (see filepath.Base below).
	Files map[string][]byte
	// SystemPackages is zero or more OS-level package names (apt for
	// Python's Debian-based image, apk for Go's Alpine-based one) to
	// install before running Code. Only takes effect when Network is also
	// true; otherwise silently ignored -- same rule as Packages, since
	// installing anything needs to actually reach a package repository.
	// Unlike Packages/Files, this also makes the container's root
	// filesystem writable (see Run) -- a real, deliberate privilege step
	// beyond Packages' own /tmp-scoped installs, since apt/apk write
	// system-wide (/usr, /var, /etc). Model-supplied, passed to
	// apt-get/apk as real argv elements (see systemInstallScript's own
	// IFS-splitting), never through a shell string.
	SystemPackages []string
}

// Result is one sandboxed execution's outcome. A non-zero ExitCode or
// TimedOut is NOT a Go error -- Run's error return is reserved for
// infrastructure failures (docker missing, permission denied); the code
// under test failing/panicking/running long is ordinary, useful
// information the caller should see.
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

	dockerOnce sync.Once
	dockerBin  string
	dockerErr  error
}

// New returns a Runner enforcing limits (zero fields fall back to their
// Default* constants -- see Limits.withDefaults).
func New(limits Limits) *Runner { return &Runner{limits: limits.withDefaults()} }

// dockerPath resolves the "docker" CLI's absolute path once per Runner (via exec.LookPath),
// cached for that Runner's lifetime and used everywhere this package invokes docker for it -- a
// fixed, resolved path rather than a bare command name repeated at every call site (a bare name
// is technically PATH-order-dependent; go:S4036 flags exactly this). Falls back to the literal
// "docker" if LookPath itself fails, so a misconfigured PATH still surfaces as the same
// "executable file not found" error a bare exec.Command("docker", ...) would already give, not a
// new failure mode. Cached per-Runner rather than process-wide so a test constructing a fresh
// Runner after changing PATH (simulating docker missing) gets a fresh lookup, not a stale one
// from an earlier Runner in the same test binary.
func (r *Runner) dockerPath() string {
	r.dockerOnce.Do(func() {
		r.dockerBin, r.dockerErr = exec.LookPath("docker")
	})
	if r.dockerErr != nil {
		return "docker"
	}
	return r.dockerBin
}

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

	if err := r.ensureImage(ctx, cfg.image); err != nil {
		return Result{}, err
	}

	dir, err := os.MkdirTemp("", "se-sandbox-*")
	if err != nil {
		return Result{}, fmt.Errorf("creating sandbox workdir: %w", err)
	}
	defer os.RemoveAll(dir)
	// os.MkdirTemp's default 0700 is unreachable from inside the container:
	// --cap-drop ALL strips CAP_DAC_OVERRIDE, so even container root is
	// subject to normal permission checks, and host root != our own UID.
	// 0o755 makes the dir traversable by any UID; the code file's own
	// 0o444 below is what actually keeps it read-only.
	if err := os.Chmod(dir, 0o755); err != nil {
		return Result{}, fmt.Errorf("preparing sandbox workdir: %w", err)
	}

	codePath := filepath.Join(dir, cfg.filename)
	// 0o444: the code file is only ever read (mounted :ro below too, but
	// this is defense in depth on the host side), never modified after write.
	if err := os.WriteFile(codePath, []byte(opts.Code), 0o444); err != nil {
		return Result{}, fmt.Errorf("writing sandbox code: %w", err)
	}

	for name, data := range opts.Files {
		// filepath.Base strips any directory component (e.g. "../../etc/passwd" -> "passwd"),
		// so an input filename can never write outside dir -- the same defense-in-depth
		// reasoning as codePath's own 0o444 above.
		safeName := filepath.Base(name)
		if safeName == "." || safeName == string(filepath.Separator) {
			return Result{}, fmt.Errorf("dockersandbox: invalid input filename %q", name)
		}
		if err := os.WriteFile(filepath.Join(dir, safeName), data, 0o444); err != nil {
			return Result{}, fmt.Errorf("writing sandbox input file %q: %w", safeName, err)
		}
	}

	runCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, r.limits.Timeout)
		defer cancel()
	}

	// image is what the actual code-execution container below runs from. When SystemPackages is
	// requested, provisionImage builds a throwaway image with them already installed as plain
	// files -- via a SEPARATE, short-lived container that gets the elevated privileges apt/apk
	// themselves need (see provisionImage's own doc comment), one that NEVER runs opts.Code. The
	// execution container below then runs from that image under the sandbox's normal,
	// unconditional lockdown (--cap-drop ALL, --read-only, no elevated caps at all) -- installing
	// packages and running untrusted code never happen in the same container.
	image := cfg.image
	if opts.Network && len(opts.SystemPackages) > 0 {
		provisioned, failResult, err := r.provisionImage(runCtx, cfg, opts.SystemPackages)
		if err != nil {
			return Result{}, err
		}
		if failResult != nil {
			// The install script itself failed (e.g. an unknown package name) -- ordinary
			// information for the caller, same as opts.Code itself exiting non-zero; opts.Code
			// never even runs.
			return *failResult, nil
		}
		defer func() {
			rmCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_ = exec.CommandContext(rmCtx, r.dockerPath(), "rmi", "-f", provisioned).Run()
			cancel()
		}()
		image = provisioned
	}

	// tmpfsSize is bumped past the plain default (64m) for any Go run, and when installing
	// packages -- a real "no space left on device" failure was observed compiling stdlib for a
	// Go run pulling in zero external dependencies at all (confirmed live: `go build` always
	// writes to $GOCACHE under /tmp, and a stdlib-heavy program -- archive/zip, encoding/xml,
	// image, crypto/* -- can exceed 64m on its own, package installs or not), and again
	// compiling against packages installed via SystemPackages. 1024m is generous headroom for
	// any of these cases (a plain go build, a pip/go-get pull, or a go build against newly
	// apt/apk-installed system libraries), not a tight fit.
	tmpfsSize := "64m"
	if opts.Language == Go || (opts.Network && len(opts.Packages) > 0) || len(opts.SystemPackages) > 0 {
		tmpfsSize = "1024m"
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
		// exec (not docker's noexec default): Go's build runs its compiled
		// binary straight from $GOCACHE/$GOTMPDIR (/tmp). Python doesn't
		// need it, but the same mount serves both languages.
		"--tmpfs", "/tmp:rw,exec,size=" + tmpfsSize + ",mode=1777",
		"-v", dir + ":/sandbox:ro",
		"-w", "/sandbox",
	}
	switch {
	case !opts.Network:
		args = append(args, "--network", "none")
	case r.limits.HostNetwork:
		args = append(args, "--network", "host")
	}
	// else: no --network flag at all, Docker's own default bridge network.
	for _, d := range r.limits.DNS {
		args = append(args, "--dns", d)
	}
	for _, e := range cfg.env {
		args = append(args, "-e", e)
	}
	args = append(args, image)
	codeInContainer := "/sandbox/" + cfg.filename
	if opts.Network && len(opts.Packages) > 0 {
		args = append(args, cfg.installArgv(codeInContainer, opts.Packages)...)
	} else {
		args = append(args, cfg.argv(codeInContainer)...)
	}

	cmd := exec.CommandContext(runCtx, r.dockerPath(), args...)
	var stdout, stderr limitedBuffer
	stdout.limit, stderr.limit = maxOutputBytes, maxOutputBytes
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	// Best-effort cleanup, unconditionally: killing the `docker run` CLI on
	// context expiry does NOT reliably stop/remove the container it
	// launched (client and container are independent to the daemon). A
	// fresh context is required since runCtx may already be Done().
	killCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = exec.CommandContext(killCtx, r.dockerPath(), "rm", "-f", name).Run()
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

// provisionImage installs packages into a throwaway image via a separate, short-lived
// container that never runs opts.Code -- only the fixed, admin-controlled install script (see
// languageConfig.systemInstallScript). Unlike the real execution container in Run, this one
// keeps Docker's own ordinary default capability set (no --cap-drop at all -- the same posture
// virtually any everyday container runs under, not host-root-equivalent) and a writable root
// filesystem: apt/apk both write system-wide (/usr, /var, /etc) and, on Debian, chmod/chown
// files the base image already owns as a different uid (_apt) -- operations that need
// CAP_FOWNER/CAP_DAC_OVERRIDE/CAP_CHOWN even for uid 0 once any capability is dropped, so trying
// to hand back just enough individual capabilities turned into chasing apt's exact internal
// needs one failure at a time. This container's actual attack surface is narrow regardless
// (a fixed, non-model-supplied command, never opts.Code), so its own isolation comes from being
// short-lived, single-purpose, and separate from code execution -- not from capability-dropping
// on top of that. Once packages are installed, they're just ordinary files -- the actual
// code-execution container in Run runs from the committed image under the sandbox's completely
// normal lockdown (--cap-drop ALL, --read-only), no elevated privileges or writable root at all,
// so installing packages and running untrusted code never happen in the same container. Returns
// (image, nil, nil) on success -- caller must docker rmi it once done -- (_, non-nil Result,
// nil) if the install script itself failed (ordinary information, same as opts.Code exiting
// non-zero), or (_, nil, error) only for a genuine infrastructure failure.
func (r *Runner) provisionImage(ctx context.Context, cfg languageConfig, packages []string) (string, *Result, error) {
	if err := r.ensureImage(ctx, cfg.image); err != nil {
		return "", nil, err
	}
	name := "se-sandbox-provision-" + randomHex(8)
	image := "se-sandbox-provisioned:" + randomHex(8)
	args := []string{
		"run", "--name", name,
		"--memory", r.limits.Memory, "--memory-swap", r.limits.Memory,
		"--cpus", r.limits.CPUs,
		"--pids-limit", r.limits.PidsLimit,
		"--security-opt", "no-new-privileges:true",
		"-e", "SE_SANDBOX_SYSTEM_PACKAGES=" + strings.Join(packages, "\n"),
	}
	if r.limits.HostNetwork {
		args = append(args, "--network", "host")
	}
	for _, d := range r.limits.DNS {
		args = append(args, "--dns", d)
	}
	args = append(args, cfg.image, "sh", "-c", cfg.systemInstallScript)
	cmd := exec.CommandContext(ctx, r.dockerPath(), args...)
	var stdout, stderr limitedBuffer
	stdout.limit, stderr.limit = maxOutputBytes, maxOutputBytes
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	// The stopped container must still exist for "docker commit" below -- no --rm here.
	// Cleanup happens unconditionally once we're done with it, success or failure.
	defer func() {
		rmCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_ = exec.CommandContext(rmCtx, r.dockerPath(), "rm", "-f", name).Run()
		cancel()
	}()

	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return "", &Result{ExitCode: exitErr.ExitCode(), Stdout: stdout.String(), Stderr: stderr.String()}, nil
	}
	if runErr != nil {
		return "", nil, fmt.Errorf("dockersandbox: installing system packages: %w", runErr)
	}
	commitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(commitCtx, r.dockerPath(), "commit", name, image).CombinedOutput(); err != nil {
		return "", nil, fmt.Errorf("dockersandbox: committing provisioned image: %w (%s)", err, out)
	}
	return image, nil, nil
}

// imagePullTimeout bounds a cold "docker pull" of a sandbox image --
// generous, since a first pull on a fresh host can take a while, and
// deliberately separate from Limits.Timeout (which bounds the sandboxed
// CODE's own wall-clock time, not one-time image setup).
const imagePullTimeout = 5 * time.Minute

// ensureImage makes sure image is present before a timed `docker run`
// touches it. Without this, an auto-pull's progress log would leak into
// the container's captured stdout/stderr, and its time would count against
// Limits.Timeout. "docker image inspect" is a fast local check, so the
// common (cached) case costs nothing.
func (r *Runner) ensureImage(ctx context.Context, image string) error {
	if err := exec.CommandContext(ctx, r.dockerPath(), "image", "inspect", image).Run(); err == nil {
		return nil
	}
	pullCtx, cancel := context.WithTimeout(ctx, imagePullTimeout)
	defer cancel()
	out, err := exec.CommandContext(pullCtx, r.dockerPath(), "pull", image).CombinedOutput()
	if err != nil {
		return fmt.Errorf("pulling sandbox image %s: %w: %s", image, err, out)
	}
	return nil
}

// randomHex returns n random bytes hex-encoded, for a unique-enough
// per-run container name (so a concurrent call's cleanup `docker rm -f`
// can never target another call's still-running container).
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand.Read practically never fails; a timestamp fallback
		// keeps names unique enough rather than panicking over this.
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
