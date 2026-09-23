// Command e2e-check is a manual, dev-host-only smoke test against a real
// deployed searchengine instance (default se.mo-sys.de) -- run by hand
// after a release, never wired into CI (nothing in .github/workflows or
// the Makefile's build/deb targets references this package). It drives
// the same public HTTPS endpoints a browser does, as a signed-in admin,
// to catch regressions a unit test can't: live model behavior, real MCP
// tool round-trips, and gateway/proxy timeout misconfiguration -- the
// class of bug a mocked test never exercises.
//
// Usage:
//
//	go run ./cmd/e2e-check -host se.mo-sys.de -creds cmd/e2e-check/credentials.json
//
// credentials.json (git-ignored -- see credentials.example.json for the
// shape) holds the target deployment's real admin username/password.
// Never commit a real one.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"time"
)

func main() {
	host := flag.String("host", "se.mo-sys.de", "target deployment's hostname (no scheme)")
	credsPath := flag.String("creds", "cmd/e2e-check/credentials.json", "path to a JSON {admin_user, admin_password} file for -host")
	includeSlow := flag.Bool("include-slow", false, "also run slow/known-heavy checks (e.g. a sandbox package install) that can take minutes")
	timeout := flag.Duration("chat-timeout", 90*time.Second, "per-request HTTP client timeout -- deliberately close to nginx's own proxy_read_timeout, so a check FAILS instead of hanging when that's misconfigured")
	flag.Parse()

	creds, err := loadCredentials(*credsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e-check: %v\n", err)
		os.Exit(1)
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e-check: building cookie jar: %v\n", err)
		os.Exit(1)
	}
	c := &client{
		base: "https://" + *host,
		http: &http.Client{Jar: jar, Timeout: *timeout},
	}

	checks := []check{
		{"healthz", c.checkHealthz},
		{"admin login", c.checkLogin(creds)},
		{"session role", c.checkSessionRole},
		{"plain chat (no tools)", c.checkPlainChat},
		{"web-search-gated chat", c.checkWebSearchChat},
		{"always-on MCP tool (datetime)", c.checkDatetimeTool},
		{"sandbox MCP tool (fast, no packages)", c.checkSandboxFast},
	}
	if *includeSlow {
		checks = append(checks, check{"sandbox MCP tool (package install)", c.checkSandboxSlow})
	}

	failed := 0
	for _, chk := range checks {
		start := time.Now()
		err := chk.run()
		elapsed := time.Since(start).Round(time.Millisecond)
		if err != nil {
			failed++
			fmt.Printf("FAIL  %-40s %8s  %v\n", chk.name, elapsed, err)
			continue
		}
		fmt.Printf("PASS  %-40s %8s\n", chk.name, elapsed)
	}

	fmt.Printf("\n%d/%d checks passed against %s\n", len(checks)-failed, len(checks), *host)
	if failed > 0 {
		os.Exit(1)
	}
}

type check struct {
	name string
	run  func() error
}

type credentials struct {
	AdminUser     string `json:"admin_user"`
	AdminPassword string `json:"admin_password"`
}

func loadCredentials(path string) (credentials, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return credentials{}, fmt.Errorf("reading credentials file %q (copy credentials.example.json and fill in real values): %w", path, err)
	}
	var c credentials
	if err := json.Unmarshal(data, &c); err != nil {
		return credentials{}, fmt.Errorf("parsing credentials file %q: %w", path, err)
	}
	if c.AdminUser == "" || c.AdminPassword == "" {
		return credentials{}, fmt.Errorf("credentials file %q is missing admin_user/admin_password", path)
	}
	return c, nil
}

// client is a thin wrapper sharing one cookie jar (and so one login
// session) across every check -- checks run in sequence, not in
// parallel, since they share this one session's state.
type client struct {
	base string
	http *http.Client
}

func (c *client) getJSON(path string, out any) (int, error) {
	resp, err := c.http.Get(c.base + path)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, err
	}
	if out != nil && len(body) > 0 {
		if err := json.Unmarshal(body, out); err != nil {
			return resp.StatusCode, fmt.Errorf("decoding response body %q: %w", truncate(body, 200), err)
		}
	}
	return resp.StatusCode, nil
}

func (c *client) postJSON(path string, in, out any) (int, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return 0, err
	}
	resp, err := c.http.Post(c.base+path, "application/json", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode >= 400 {
		return resp.StatusCode, fmt.Errorf("%s %s: %d: %s", http.MethodPost, path, resp.StatusCode, truncate(respBody, 300))
	}
	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return resp.StatusCode, fmt.Errorf("decoding response body %q: %w", truncate(respBody, 200), err)
		}
	}
	return resp.StatusCode, nil
}

func truncate(b []byte, n int) string {
	s := string(b)
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

func (c *client) checkHealthz() error {
	status, err := c.getJSON("/healthz", nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("expected 200, got %d", status)
	}
	return nil
}

func (c *client) checkLogin(creds credentials) func() error {
	return func() error {
		var out struct {
			Redirect string `json:"redirect"`
		}
		_, err := c.postJSON("/login", map[string]string{
			"username": creds.AdminUser,
			"password": creds.AdminPassword,
		}, &out)
		return err
	}
}

func (c *client) checkSessionRole() error {
	var out struct {
		Role string `json:"role"`
	}
	status, err := c.getJSON("/session", &out)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("expected 200, got %d", status)
	}
	if out.Role != "admin" {
		return fmt.Errorf("expected role=admin (logged-in session), got %q", out.Role)
	}
	return nil
}

type chatResponse struct {
	Answer      string `json:"answer"`
	ToolResults []struct {
		ToolName string `json:"tool_name"`
		Err      string `json:"err"`
	} `json:"tool_results"`
}

// chatOnce POSTs a single-turn conversation to /chat -- every check below
// sends one fixed, deterministic-ish question rather than reusing
// history, so checks stay independent of each other.
func (c *client) chatOnce(question string, webSearch bool) (chatResponse, error) {
	var out chatResponse
	_, err := c.postJSON("/chat", map[string]any{
		"messages":   []map[string]string{{"role": "user", "content": question}},
		"web_search": webSearch,
	}, &out)
	return out, err
}

// checkPlainChat proves the chat endpoint answers at all, with no MCP
// tools in play -- the simplest possible real round trip through the
// live model. Catches a wholesale chat-endpoint misconfiguration (the
// exact class of bug an earlier incident this tool exists to prevent
// caused: a partial PATCH silently zeroing base_url/model/enabled).
func (c *client) checkPlainChat() error {
	out, err := c.chatOnce("Reply with exactly the single word: PONG", false)
	if err != nil {
		return err
	}
	if out.Answer == "" {
		return fmt.Errorf("got an empty answer")
	}
	return nil
}

// checkWebSearchChat proves the whole web_search/web_fetch MCP path
// works end-to-end AND that the model actually reaches a real final
// answer, not just tool calls with nothing usable -- the exact failure
// mode a live incident hit (search results capped to 2, both from one
// blocked domain, so every tool-call round retried the same dead end and
// the turn ended with no real answer).
func (c *client) checkWebSearchChat() error {
	out, err := c.chatOnce("What is the current year? Search the web to confirm.", true)
	if err != nil {
		return err
	}
	if out.Answer == "" {
		return fmt.Errorf("got an empty answer -- the model may have exhausted its tool-call budget without reaching one (see the web_search_result_count/mcp-web prompt incident this check guards against)")
	}
	if len(out.ToolResults) == 0 {
		return fmt.Errorf("expected at least one tool call (web_search/web_fetch) for a web-search-gated question, got none")
	}
	for _, tr := range out.ToolResults {
		if tr.Err != "" {
			return fmt.Errorf("tool %q returned an error: %s", tr.ToolName, tr.Err)
		}
	}
	return nil
}

// checkDatetimeTool proves a plain, always-on (not web-search-gated) MCP
// server actually gets invoked and answers correctly -- mcp-datetime is
// the cheapest possible real tool round trip (no network, no subprocess
// dependency install), so this isolates "is MCP wiring healthy at all"
// from "is web search healthy."
func (c *client) checkDatetimeTool() error {
	out, err := c.chatOnce("Use your datetime tool to tell me the current UTC year. Reply with just the 4-digit year.", false)
	if err != nil {
		return err
	}
	if out.Answer == "" {
		return fmt.Errorf("got an empty answer")
	}
	if len(out.ToolResults) == 0 {
		return fmt.Errorf("expected the datetime tool to be called, got no tool_results -- either it's not configured as always-on (gated_by_web_search=false, enabled=true) or MCP wiring is broken")
	}
	return nil
}

// checkSandboxFast exercises the sandbox MCP server's real Docker-container
// round trip (container spin-up, not just a network call) with a
// trivial, fast script -- no network/package install, so it stays quick
// and deterministic. This is the check most likely to surface a gateway/
// proxy timeout misconfiguration even for ordinary use, since spinning up
// a container is the slowest step in an otherwise-instant tool call.
func (c *client) checkSandboxFast() error {
	out, err := c.chatOnce("Use run_python to compute 6 * 7 and tell me only the resulting number.", false)
	if err != nil {
		return err
	}
	if out.Answer == "" {
		return fmt.Errorf("got an empty answer")
	}
	if len(out.ToolResults) == 0 {
		return fmt.Errorf("expected run_python to be called, got no tool_results -- sandbox MCP server may not be configured as always-on")
	}
	for _, tr := range out.ToolResults {
		if tr.Err != "" {
			return fmt.Errorf("tool %q returned an error: %s", tr.ToolName, tr.Err)
		}
	}
	return nil
}

// checkSandboxSlow deliberately reproduces the slow path a real incident
// hit: a sandbox call needing a real network package install (mirroring
// easyocr's own multi-minute, always-uncached download) -- this is the
// scenario mcpclient.callTimeout's hardcoded 60s ceiling (independent of
// whatever -timeout an admin sets on the sandbox MCPServer row itself)
// can silently break, surfacing as a raw gateway timeout instead of a
// graceful in-app tool error. Opt-in (-include-slow) since it can take
// several minutes and isn't needed for a routine post-release check.
func (c *client) checkSandboxSlow() error {
	out, err := c.chatOnce(
		"Use run_python with packages [\"requests\"] to import requests and print requests.__version__. "+
			"This may take a while the first time -- please wait for it rather than giving up.", false)
	if err != nil {
		return err
	}
	if out.Answer == "" {
		return fmt.Errorf("got an empty answer")
	}
	for _, tr := range out.ToolResults {
		if tr.Err != "" {
			return fmt.Errorf("tool %q returned an error: %s -- if this mentions a timeout, check mcpclient.callTimeout vs the sandbox MCPServer row's own -timeout flag", tr.ToolName, tr.Err)
		}
	}
	return nil
}
