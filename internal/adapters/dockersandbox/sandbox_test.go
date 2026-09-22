package dockersandbox

import (
	"context"
	"os"
	"os/exec"
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
