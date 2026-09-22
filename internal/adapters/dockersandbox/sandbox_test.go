package dockersandbox

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// requireDockerTests skips real-Docker-dependent tests unless
// SE_DOCKER_TESTS=1 is set -- same convention as
// internal/adapters/browserfetcher's requireBrowserTests: these tests
// spawn real containers (pulling python:3-slim/golang:1-alpine on first
// run) rather than a fast, hermetic unit test, and not every dev/CI
// environment has (or should be assumed to have) a usable Docker daemon.
func requireDockerTests(t *testing.T) {
	t.Helper()
	if os.Getenv("SE_DOCKER_TESTS") == "" {
		t.Skip("skipping: set SE_DOCKER_TESTS=1 to run real-Docker sandbox tests (pulls python:3-slim/golang:1-alpine on first run)")
	}
}

func TestRun_Python_CapturesStdoutAndStderr(t *testing.T) {
	requireDockerTests(t)
	r := New(Limits{})
	res, err := r.Run(context.Background(), RunOptions{
		Language: Python,
		Code:     "import sys\nprint('out line')\nprint('err line', file=sys.stderr)\n",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ExitCode != 0 || res.TimedOut {
		t.Fatalf("expected a clean exit, got %+v", res)
	}
	if strings.TrimSpace(res.Stdout) != "out line" {
		t.Errorf("expected stdout %q, got %q", "out line", res.Stdout)
	}
	if strings.TrimSpace(res.Stderr) != "err line" {
		t.Errorf("expected stderr %q, got %q", "err line", res.Stderr)
	}
}

func TestRun_Python_NonZeroExitCodeSurfaced(t *testing.T) {
	requireDockerTests(t)
	r := New(Limits{})
	res, err := r.Run(context.Background(), RunOptions{Language: Python, Code: "import sys\nsys.exit(7)\n"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ExitCode != 7 {
		t.Errorf("expected exit code 7, got %+v", res)
	}
}

func TestRun_Go_CapturesStdout(t *testing.T) {
	requireDockerTests(t)
	r := New(Limits{})
	code := "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello from go\")\n}\n"
	res, err := r.Run(context.Background(), RunOptions{Language: Go, Code: code})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ExitCode != 0 || res.TimedOut {
		t.Fatalf("expected a clean exit, got %+v", res)
	}
	if strings.TrimSpace(res.Stdout) != "hello from go" {
		t.Errorf("expected stdout %q, got %q", "hello from go", res.Stdout)
	}
}

// TestRun_MissingImageIsPulledWithoutContaminatingOutput is the direct
// regression test for a real bug found via CI on a cold runner: without
// ensureImage, `docker run` auto-pulls a missing image and its progress
// log lands in the container's own captured stderr, and the pull time
// eats into Limits.Timeout -- both reproduced here by force-removing the
// image first.
func TestRun_MissingImageIsPulledWithoutContaminatingOutput(t *testing.T) {
	requireDockerTests(t)
	const image = "python:3-slim"
	if err := exec.Command("docker", "rmi", "-f", image).Run(); err != nil {
		t.Logf("docker rmi %s (best-effort, ignoring result): %v", image, err)
	}
	r := New(Limits{Timeout: 30 * time.Second})
	res, err := r.Run(context.Background(), RunOptions{Language: Python, Code: "print('out line')"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.TimedOut {
		t.Fatalf("expected the pull to be covered by imagePullTimeout, not Limits.Timeout, got %+v", res)
	}
	if strings.TrimSpace(res.Stdout) != "out line" {
		t.Errorf("expected stdout %q, got %q", "out line", res.Stdout)
	}
	if res.Stderr != "" {
		t.Errorf("expected no pull-progress noise in stderr, got %q", res.Stderr)
	}
}

func TestRun_NetworkDisallowedByDefault(t *testing.T) {
	requireDockerTests(t)
	r := New(Limits{})
	res, err := r.Run(context.Background(), RunOptions{
		Language: Python,
		Code: "import urllib.request\ntry:\n" +
			"    urllib.request.urlopen('http://example.com', timeout=3)\n" +
			"    print('reached network')\n" +
			"except Exception:\n" +
			"    print('blocked')\n",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(res.Stdout) != "blocked" {
		t.Errorf("expected network access to be blocked by default, got %+v", res)
	}
}

func TestRun_NetworkAllowedWhenRequested(t *testing.T) {
	requireDockerTests(t)
	r := New(Limits{})
	res, err := r.Run(context.Background(), RunOptions{
		Language: Python, Network: true,
		Code: "import urllib.request\n" +
			"r = urllib.request.urlopen('http://example.com', timeout=5)\n" +
			"print('status', r.status)\n",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res.Stdout, "status 200") {
		t.Errorf("expected a successful network fetch with Network:true, got %+v", res)
	}
}

// TestRun_PythonPackagesInstalledWhenNetworkEnabled proves RunOptions.
// Packages actually reaches a real "pip install --target=..." before the
// script runs, and that the installed package is importable via the
// PYTHONPATH the install script sets -- "six" is a tiny, pure-Python,
// dependency-free package, chosen only so this test stays fast.
func TestRun_PythonPackagesInstalledWhenNetworkEnabled(t *testing.T) {
	requireDockerTests(t)
	r := New(Limits{Timeout: 30 * time.Second})
	res, err := r.Run(context.Background(), RunOptions{
		Language: Python, Network: true, Packages: []string{"six"},
		Code: "import six\nprint('six version', six.__version__)\n",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ExitCode != 0 || !strings.Contains(res.Stdout, "six version") {
		t.Errorf("expected six installed and importable, got %+v", res)
	}
}

// TestRun_PackagesIgnoredWithoutNetwork proves Packages has no effect at
// all (no install attempted, plain argv used) when Network is false --
// mirrors DNS/HostNetwork's own "meaningless without Network" convention.
// The package would need network to install, so if this silently tried
// anyway it would hang/fail confusingly rather than just running the code
// as if Packages were never set.
func TestRun_PackagesIgnoredWithoutNetwork(t *testing.T) {
	requireDockerTests(t)
	r := New(Limits{})
	res, err := r.Run(context.Background(), RunOptions{
		Language: Python, Packages: []string{"six"},
		Code: "import six\nprint('should not get here')\n",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ExitCode == 0 || !strings.Contains(res.Stderr, "ModuleNotFoundError") {
		t.Errorf("expected six NOT installed (no network), got %+v", res)
	}
}

// TestRun_GoModulesInstalledWhenNetworkEnabled proves RunOptions.Packages
// for Go actually reaches a real "go get" (against a throwaway go.mod in
// /tmp) before "go run" -- rsc.io/quote is the Go project's own canonical
// minimal test module, chosen only for how small and stable it is.
func TestRun_GoModulesInstalledWhenNetworkEnabled(t *testing.T) {
	requireDockerTests(t)
	r := New(Limits{Timeout: 60 * time.Second})
	res, err := r.Run(context.Background(), RunOptions{
		Language: Go, Network: true, Packages: []string{"rsc.io/quote"},
		Code: `package main

import (
	"fmt"
	"rsc.io/quote"
)

func main() { fmt.Println(quote.Hello()) }
`,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ExitCode != 0 || res.Stdout == "" {
		t.Errorf("expected rsc.io/quote fetched and its Hello() printed, got %+v", res)
	}
}

// TestRun_CustomDNSServerAppliedToContainer proves Limits.DNS actually
// reaches the container as a real --dns flag, not just plumbed-through-
// but-unused -- read back via /etc/resolv.conf from inside the sandbox
// itself, the same "prove it with real Docker behavior" bar
// TestRun_CustomMemoryLimitEnforced/TestRun_CustomPidsLimitEnforced use for
// their own Limits fields.
func TestRun_CustomDNSServerAppliedToContainer(t *testing.T) {
	requireDockerTests(t)
	r := New(Limits{DNS: []string{"8.8.8.8", "1.1.1.1"}})
	res, err := r.Run(context.Background(), RunOptions{
		Language: Python, Network: true,
		Code: "print(open('/etc/resolv.conf').read())",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res.Stdout, "8.8.8.8") || !strings.Contains(res.Stdout, "1.1.1.1") {
		t.Errorf("expected both configured DNS servers in /etc/resolv.conf, got %+v", res)
	}
}

// TestRun_HostNetworkSharesHostsNetworkNamespace proves Limits.HostNetwork
// actually reaches the container as a real --network host flag, not just
// plumbed-through-but-unused. Deliberately does NOT assert on
// /etc/resolv.conf content (e.g. "must/must not contain Docker's own
// 127.0.0.11 embedded DNS server") -- that turned out to be a Docker
// version/daemon-config-dependent emergent behavior, not a stable
// contract: on this environment's Docker, even the DEFAULT bridge network
// already forwards the host's own real upstream DNS servers directly,
// with no 127.0.0.11 indirection at all, contradicting what was assumed
// (and briefly asserted here) based on se.mo-sys.de's own older Docker
// version and generic Docker documentation. Instead this asserts the one
// thing --network host actually, definitionally means: the container
// shares the host's network namespace outright, so it sees every one of
// the host's own network interfaces (loopback plus every real interface,
// likely several) -- strictly more than a bridge-networked container's
// fixed two (loopback + one veth pair), regardless of Docker version or
// DNS daemon configuration.
func TestRun_HostNetworkSharesHostsNetworkNamespace(t *testing.T) {
	requireDockerTests(t)
	countInterfaces := "import socket\nprint(len(socket.if_nameindex()))"

	bridge := New(Limits{})
	bridgeRes, err := bridge.Run(context.Background(), RunOptions{Language: Python, Network: true, Code: countInterfaces})
	if err != nil {
		t.Fatalf("unexpected error (bridge): %v", err)
	}
	bridgeCount, err := strconv.Atoi(strings.TrimSpace(bridgeRes.Stdout))
	if err != nil {
		t.Fatalf("parsing bridge interface count from %+v: %v", bridgeRes, err)
	}

	host := New(Limits{HostNetwork: true})
	hostRes, err := host.Run(context.Background(), RunOptions{Language: Python, Network: true, Code: countInterfaces})
	if err != nil {
		t.Fatalf("unexpected error (host): %v", err)
	}
	hostCount, err := strconv.Atoi(strings.TrimSpace(hostRes.Stdout))
	if err != nil {
		t.Fatalf("parsing host interface count from %+v: %v", hostRes, err)
	}

	if hostCount <= bridgeCount {
		t.Errorf("expected --network host to see strictly more network interfaces than the isolated bridge network (loopback + this host's own real interfaces vs. just loopback + one veth pair), got host=%d bridge=%d", hostCount, bridgeCount)
	}
}

// TestRun_HostNetworkAllowsRealFetch proves a --network host container
// still genuinely reaches the internet, same bar
// TestRun_NetworkAllowedWhenRequested holds bridge networking to.
func TestRun_HostNetworkAllowsRealFetch(t *testing.T) {
	requireDockerTests(t)
	r := New(Limits{HostNetwork: true})
	res, err := r.Run(context.Background(), RunOptions{
		Language: Python, Network: true,
		Code: "import urllib.request\n" +
			"r = urllib.request.urlopen('http://example.com', timeout=5)\n" +
			"print('status', r.status)\n",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res.Stdout, "status 200") {
		t.Errorf("expected a successful network fetch with HostNetwork:true, got %+v", res)
	}
}

func TestRun_ReadOnlyRootFilesystem(t *testing.T) {
	requireDockerTests(t)
	r := New(Limits{})
	res, err := r.Run(context.Background(), RunOptions{
		Language: Python,
		Code:     "open('/etc/should-not-write', 'w').write('x')\n",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ExitCode == 0 {
		t.Fatalf("expected writing outside /tmp to fail under a read-only root, got %+v", res)
	}
	if !strings.Contains(res.Stderr, "Read-only file system") {
		t.Errorf("expected a read-only-filesystem error, got %+v", res)
	}
}

func TestRun_TmpIsWritable(t *testing.T) {
	requireDockerTests(t)
	r := New(Limits{})
	res, err := r.Run(context.Background(), RunOptions{
		Language: Python,
		Code:     "open('/tmp/scratch', 'w').write('ok')\nprint(open('/tmp/scratch').read())\n",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(res.Stdout) != "ok" {
		t.Errorf("expected /tmp to be writable, got %+v", res)
	}
}

func TestRun_TimesOutAndReportsTimedOut(t *testing.T) {
	requireDockerTests(t)
	r := New(Limits{Timeout: 2 * time.Second})
	start := time.Now()
	res, err := r.Run(context.Background(), RunOptions{Language: Python, Code: "import time\ntime.sleep(30)\n"})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.TimedOut {
		t.Fatalf("expected TimedOut, got %+v", res)
	}
	if elapsed > 10*time.Second {
		t.Errorf("expected the 2s Limits.Timeout to fire well before 10s, took %v", elapsed)
	}
}

func TestRun_TimeoutCleansUpTheContainer(t *testing.T) {
	requireDockerTests(t)
	r := New(Limits{Timeout: 1 * time.Second})
	res, err := r.Run(context.Background(), RunOptions{Language: Python, Code: "import time\ntime.sleep(30)\n"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.TimedOut {
		t.Fatalf("expected TimedOut, got %+v", res)
	}
	// The essential non-regression check for Run's own explicit "docker rm
	// -f" cleanup: a timed-out container must not linger, since
	// exec.CommandContext killing the `docker run` CLI process does NOT by
	// itself stop the container it launched (see Run's own comment).
	out, err := exec.Command("docker", "ps", "-a", "--filter", "name=se-sandbox-", "--format", "{{.Names}}").CombinedOutput()
	if err != nil {
		t.Fatalf("listing containers: %v: %s", err, out)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("expected no lingering se-sandbox-* containers after a timeout, found: %s", out)
	}
}

func TestRun_CustomMemoryLimitEnforced(t *testing.T) {
	requireDockerTests(t)
	r := New(Limits{Memory: "32m"})
	res, err := r.Run(context.Background(), RunOptions{
		Language: Python,
		Code:     "x = bytearray(200*1024*1024)\nprint('allocated')\n",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ExitCode == 0 {
		t.Errorf("expected a 200MB allocation to be OOM-killed under a 32m limit, got %+v", res)
	}
}

func TestRun_CustomPidsLimitEnforced(t *testing.T) {
	requireDockerTests(t)
	r := New(Limits{PidsLimit: "4"})
	res, err := r.Run(context.Background(), RunOptions{
		Language: Python,
		Code: "import subprocess\n" +
			"for i in range(50):\n" +
			"    subprocess.Popen(['sleep', '5'])\n" +
			"print('spawned all (bug: pids-limit not enforced!)')\n",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(res.Stdout, "spawned all") {
		t.Errorf("expected forking 50 processes to be blocked under a pids-limit of 4, got %+v", res)
	}
}

func TestRun_OutputTruncatedPastMaxOutputBytes(t *testing.T) {
	requireDockerTests(t)
	r := New(Limits{})
	res, err := r.Run(context.Background(), RunOptions{
		Language: Python,
		Code:     "print('x' * 200000)\n",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(res.Stdout, "... (truncated)") {
		t.Errorf("expected truncated output to end with the truncation marker, got last 50 chars: %q", lastN(res.Stdout, 50))
	}
	if len(res.Stdout) > maxOutputBytes+len("\n... (truncated)")+1 {
		t.Errorf("expected output capped near maxOutputBytes, got %d bytes", len(res.Stdout))
	}
}

func TestRun_UnsupportedLanguage(t *testing.T) {
	r := New(Limits{})
	_, err := r.Run(context.Background(), RunOptions{Language: "ruby", Code: "puts 1"})
	if err == nil {
		t.Fatal("expected an error for an unsupported language")
	}
}

func TestRun_DockerNotOnPATH(t *testing.T) {
	requireDockerTests(t)
	// Empties PATH for the duration of this test so exec.CommandContext's
	// "docker" lookup fails the same way it would on a host that never
	// installed Docker at all -- proves this surfaces as Run's ordinary
	// error return, not a panic or a hang.
	t.Setenv("PATH", t.TempDir())
	r := New(Limits{})
	_, err := r.Run(context.Background(), RunOptions{Language: Python, Code: "print(1)"})
	if err == nil {
		t.Fatal("expected an error when docker isn't on PATH")
	}
}

// TestLimitedBuffer_Write covers every branch directly and
// deterministically -- real subprocess output arrives in whatever chunks
// the OS pipe happens to deliver, which isn't a reliable way to exercise
// the "already full" branch specifically (see
// TestRun_OutputTruncatedAcrossMultipleWrites, which covers the
// through-Run integration but can't guarantee the exact Write call
// pattern).
func TestLimitedBuffer_Write(t *testing.T) {
	var w limitedBuffer
	w.limit = 10

	n, err := w.Write([]byte("12345"))
	if n != 5 || err != nil {
		t.Fatalf("first write: n=%d err=%v", n, err)
	}
	if w.String() != "12345" {
		t.Fatalf("expected %q, got %q", "12345", w.String())
	}

	// Crosses the limit mid-write (5 already buffered + 8 more > 10).
	n, err = w.Write([]byte("67890abc"))
	if n != 8 || err != nil {
		t.Fatalf("crossing write: n=%d err=%v", n, err)
	}
	if got := w.String(); got != "1234567890\n... (truncated)" {
		t.Fatalf("expected truncated buffer capped at the limit, got %q", got)
	}

	// Now already full -- exercises the "remaining <= 0" branch
	// specifically, which the crossing write above does not.
	n, err = w.Write([]byte("more"))
	if n != 4 || err != nil {
		t.Fatalf("already-full write: n=%d err=%v", n, err)
	}
	if got := w.String(); got != "1234567890\n... (truncated)" {
		t.Fatalf("expected no further growth once already full, got %q", got)
	}
}

func lastN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
