package dockersandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeResolvConf(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing test resolv.conf: %v", err)
	}
	return path
}

func TestDetectHostDNSFromPaths_ParsesNameserverLines(t *testing.T) {
	dir := t.TempDir()
	path := writeResolvConf(t, dir, "resolv.conf", "nameserver 8.8.8.8\nnameserver 1.1.1.1\nsearch example.com\n")
	got, err := detectHostDNSFromPaths([]string{path})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 || got[0] != "8.8.8.8" || got[1] != "1.1.1.1" {
		t.Errorf("unexpected servers: %v", got)
	}
}

// TestDetectHostDNSFromPaths_PrefersFirstPathThatHasServers proves the
// ordering/fallback mechanism (real DNS detection puts systemd-resolved's
// path first so real upstream servers win over the 127.0.0.53 stub), using
// two temp files standing in for the two real paths.
func TestDetectHostDNSFromPaths_PrefersFirstPathThatHasServers(t *testing.T) {
	dir := t.TempDir()
	first := writeResolvConf(t, dir, "first.conf", "nameserver 192.0.2.1\n")
	second := writeResolvConf(t, dir, "second.conf", "nameserver 127.0.0.53\n")
	got, err := detectHostDNSFromPaths([]string{first, second})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0] != "192.0.2.1" {
		t.Errorf("expected the first path's server to win, got %v", got)
	}
}

func TestDetectHostDNSFromPaths_FallsBackWhenFirstPathMissing(t *testing.T) {
	dir := t.TempDir()
	second := writeResolvConf(t, dir, "second.conf", "nameserver 9.9.9.9\n")
	got, err := detectHostDNSFromPaths([]string{filepath.Join(dir, "does-not-exist.conf"), second})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0] != "9.9.9.9" {
		t.Errorf("expected fallback to the second path's server, got %v", got)
	}
}

// TestDetectHostDNSFromPaths_FallsBackWhenFirstPathHasNoServers covers a
// present-but-empty first file -- a different branch than the missing-file
// case above (os.IsNotExist vs. a zero-length parse result).
func TestDetectHostDNSFromPaths_FallsBackWhenFirstPathHasNoServers(t *testing.T) {
	dir := t.TempDir()
	first := writeResolvConf(t, dir, "first.conf", "search example.com\n")
	second := writeResolvConf(t, dir, "second.conf", "nameserver 9.9.9.9\n")
	got, err := detectHostDNSFromPaths([]string{first, second})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0] != "9.9.9.9" {
		t.Errorf("expected fallback to the second path's server, got %v", got)
	}
}

func TestDetectHostDNSFromPaths_ErrorsWhenNoPathHasServers(t *testing.T) {
	dir := t.TempDir()
	_, err := detectHostDNSFromPaths([]string{filepath.Join(dir, "missing.conf")})
	if err == nil {
		t.Fatal("expected an error when no path yields any nameserver")
	}
}

// TestDetectHostDNSFromPaths_PropagatesRealReadError covers a path that
// exists but can't be read -- unlike "doesn't exist", which is skipped.
func TestDetectHostDNSFromPaths_PropagatesRealReadError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping: running as root, which ignores file permissions")
	}
	dir := t.TempDir()
	path := writeResolvConf(t, dir, "unreadable.conf", "nameserver 8.8.8.8\n")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer os.Chmod(path, 0o644)
	_, err := detectHostDNSFromPaths([]string{path})
	if err == nil {
		t.Fatal("expected a read error to propagate")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("expected the error to name the unreadable path, got %v", err)
	}
}

func TestDetectHostDNS_UsesRealPaths(t *testing.T) {
	// Smoke test: the exported entry point delegates to
	// detectHostDNSFromPaths with real system paths, which should always
	// resolve in any CI/dev environment.
	got, err := DetectHostDNS()
	if err != nil {
		t.Fatalf("unexpected error detecting this host's own DNS servers: %v", err)
	}
	if len(got) == 0 {
		t.Error("expected at least one detected nameserver")
	}
}
