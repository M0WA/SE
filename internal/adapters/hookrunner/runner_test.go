package hookrunner

import (
	"context"
	"strings"
	"testing"
	"time"
)

const testdataDir = "testdata"

func TestNew(t *testing.T) {
	r := New(testdataDir)
	if r.Dir != testdataDir {
		t.Fatalf("Dir = %q, want %q", r.Dir, testdataDir)
	}
}

func TestRunHookScript_HappyPath(t *testing.T) {
	r := New(testdataDir)
	out, err := r.RunHookScript(context.Background(), "echo_args.sh", []string{"hello", "world; rm -rf /"}, nil)
	if err != nil {
		t.Fatalf("RunHookScript: %v", err)
	}
	want := "hello world; rm -rf /\n"
	if out != want {
		t.Fatalf("output = %q, want %q", out, want)
	}
}

func TestRunHookScript_ArgsPassedVerbatimNeverShellInterpreted(t *testing.T) {
	// A capture group containing shell metacharacters must reach the
	// script as a single opaque argv element, never be reinterpreted as
	// shell syntax (e.g. command substitution, redirection, chaining).
	r := New(testdataDir)
	dangerous := "$(echo pwned) && echo pwned2 | cat /etc/passwd `id`"
	out, err := r.RunHookScript(context.Background(), "echo_args.sh", []string{dangerous}, nil)
	if err != nil {
		t.Fatalf("RunHookScript: %v", err)
	}
	if out != dangerous+"\n" {
		t.Fatalf("output = %q, want the arg echoed back verbatim (%q)", out, dangerous+"\n")
	}
}

func TestRunHookScript_EmptyScriptName(t *testing.T) {
	r := New(testdataDir)
	if _, err := r.RunHookScript(context.Background(), "", nil, nil); err == nil {
		t.Fatal("expected error for empty script name, got nil")
	}
}

func TestRunHookScript_PathTraversalRejected(t *testing.T) {
	r := New(testdataDir)
	cases := []string{
		"../runner.go",
		"..",
		".",
		"sub/echo_args.sh",
		`sub\echo_args.sh`,
		"/etc/passwd",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := r.RunHookScript(context.Background(), name, nil, nil); err == nil {
				t.Fatalf("expected error for script name %q, got nil", name)
			}
		})
	}
}

func TestRunHookScript_NonexistentScript(t *testing.T) {
	r := New(testdataDir)
	if _, err := r.RunHookScript(context.Background(), "does_not_exist.sh", nil, nil); err == nil {
		t.Fatal("expected error for nonexistent script, got nil")
	}
}

func TestRunHookScript_NonZeroExit(t *testing.T) {
	r := New(testdataDir)
	_, err := r.RunHookScript(context.Background(), "fail.sh", nil, nil)
	if err == nil {
		t.Fatal("expected error for non-zero exit, got nil")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error %q does not contain stderr text %q", err.Error(), "boom")
	}
}

func TestRunHookScript_NonZeroExitLongStderrTruncated(t *testing.T) {
	r := New(testdataDir)
	_, err := r.RunHookScript(context.Background(), "fail_long_stderr.sh", nil, nil)
	if err == nil {
		t.Fatal("expected error for non-zero exit, got nil")
	}
	if strings.Count(err.Error(), "e") >= 500 {
		t.Fatalf("error should truncate stderr well below the script's 500 bytes: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "...") {
		t.Fatalf("error %q should carry a truncation marker", err.Error())
	}
}

func TestRunHookScript_Timeout(t *testing.T) {
	r := &Runner{Dir: testdataDir, Timeout: 50 * time.Millisecond}
	start := time.Now()
	_, err := r.RunHookScript(context.Background(), "sleep.sh", nil, nil)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error %q does not mention timeout", err.Error())
	}
	if elapsed > 4*time.Second {
		t.Fatalf("RunHookScript took %s, expected it to be killed well before the script's own 5s sleep", elapsed)
	}
}

func TestRunHookScript_CallerDeadlineShorterThanRunnerTimeout(t *testing.T) {
	// If the caller's ctx already has an earlier deadline than r.Timeout,
	// that shorter deadline must still govern.
	r := &Runner{Dir: testdataDir, Timeout: 5 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := r.RunHookScript(ctx, "sleep.sh", nil, nil)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if elapsed > 4*time.Second {
		t.Fatalf("RunHookScript took %s, expected the caller's shorter deadline to govern", elapsed)
	}
}

func TestRunHookScript_StdoutTruncation(t *testing.T) {
	r := New(testdataDir)
	out, err := r.RunHookScript(context.Background(), "big_output.sh", nil, nil)
	if err != nil {
		t.Fatalf("RunHookScript: %v", err)
	}
	if len(out) <= maxStdout {
		t.Fatalf("output length = %d, want > maxStdout (%d) since it should carry a truncation note", len(out), maxStdout)
	}
	if !strings.Contains(out, "truncated") {
		t.Fatalf("output does not mention truncation: %q", out[len(out)-60:])
	}
	if !strings.HasPrefix(out, strings.Repeat("a", maxStdout)) {
		t.Fatal("output's first maxStdout bytes should be untouched original content")
	}
}

// TestRunHookScript_EnvReachesChildProcess proves a passed env entry
// actually reaches the child process (not just accepted and ignored).
func TestRunHookScript_EnvReachesChildProcess(t *testing.T) {
	r := New(testdataDir)
	out, err := r.RunHookScript(context.Background(), "echo_env.sh", []string{"WEB_SEARCH_BASE_URL"}, map[string]string{"WEB_SEARCH_BASE_URL": "http://searxng.example:8888"})
	if err != nil {
		t.Fatalf("RunHookScript: %v", err)
	}
	want := "http://searxng.example:8888\n"
	if out != want {
		t.Fatalf("output = %q, want %q", out, want)
	}
}

// TestRunHookScript_NilEnv_BehavesExactlyAsBefore proves passing a nil env
// map is equivalent to omitting env entirely -- the variable named is
// simply absent from the child's environment, and nothing else about
// execution changes.
func TestRunHookScript_NilEnv_BehavesExactlyAsBefore(t *testing.T) {
	r := New(testdataDir)
	out, err := r.RunHookScript(context.Background(), "echo_env.sh", []string{"WEB_SEARCH_BASE_URL"}, nil)
	if err != nil {
		t.Fatalf("RunHookScript: %v", err)
	}
	if out != "\n" {
		t.Fatalf("output = %q, want just a newline (variable unset)", out)
	}
}

// TestRunHookScript_EmptyEnv_BehavesExactlyAsBefore proves an empty
// (non-nil) env map behaves the same as a nil one.
func TestRunHookScript_EmptyEnv_BehavesExactlyAsBefore(t *testing.T) {
	r := New(testdataDir)
	out, err := r.RunHookScript(context.Background(), "echo_args.sh", []string{"hi"}, map[string]string{})
	if err != nil {
		t.Fatalf("RunHookScript: %v", err)
	}
	if out != "hi\n" {
		t.Fatalf("output = %q, want %q", out, "hi\n")
	}
}
