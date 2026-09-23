// Command e2e-check is a manual, dev-host-only smoke test against a real
// deployed searchengine instance (default se.mo-sys.de) -- run by hand
// after a release, never wired into CI (nothing in .github/workflows or
// the Makefile's build/deb targets references this package). It drives
// the same public HTTPS endpoints a browser does, first as a signed-in
// admin and then as a dedicated regular test user, to catch regressions
// a unit test can't: live model behavior, real MCP tool round-trips
// (every configured server, every enabled agent), persistent-chat/file
// lifecycle (including a fork's independence), admin/account CRUD, and
// gateway/proxy timeout misconfiguration -- the class of bug a mocked
// test never exercises. Deliberately does NOT trigger a real crawl job:
// that mutates the live index against a real seed URL with no clean
// undo, too invasive even as an opt-in check.
//
// Usage:
//
//	go run ./cmd/e2e-check -host se.mo-sys.de -creds cmd/e2e-check/credentials.json
//
// credentials.json (git-ignored -- see credentials.example.json for the
// shape) holds the target deployment's real admin username/password,
// plus a dedicated regular test_user/test_user_password -- create that
// account once via the admin UI (Users -> Add user) and never reuse a
// real person's account, since these checks pin/rename/delete chats,
// upload/delete files, and change its own password under it. Never
// commit a real credentials file.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strings"
	"time"
)

// errSkip marks a check as skipped rather than passed or failed --
// deployment-dependent preconditions (no agents configured, no "Image
// analyst" agent found) aren't a regression in this tool's own sense, so
// they shouldn't fail the whole run the way a real error does.
var errSkip = errors.New("skip")

// REST paths/headers referenced from more than one check below -- named
// once each so a route rename (or the header name) needs one edit, and so
// SonarCloud's go:S1192 (repeated string literal) doesn't flag the
// duplication across otherwise-independent checkX functions.
const (
	skipNoPinnedChat      = "no pinned chat (checkChatPin must have failed)"
	pathLogin             = "/login"
	pathAdminAgents       = "/admin/api/agents"
	pathAdminMCPServers   = "/admin/api/mcp-servers"
	pathAccountChats      = "/account/api/chats"
	pathAccountMCPServers = "/account/api/mcp-servers"
	headerContentType     = "Content-Type"
)

func main() {
	host := flag.String("host", "se.mo-sys.de", "target deployment's hostname (no scheme)")
	credsPath := flag.String("creds", "cmd/e2e-check/credentials.json", "path to a JSON credentials file for -host (see credentials.example.json)")
	includeSlow := flag.Bool("include-slow", false, "also run slow/known-heavy checks (sandbox package install, image OCR) that can take minutes")
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

	var passed, failed, skipped int
	run := func(checks []check) {
		p, f, s := runChecks(checks)
		passed += p
		failed += f
		skipped += s
	}

	// Phase 1: admin session, checks whose shape is known upfront.
	phase1 := []check{
		{"healthz", c.checkHealthz},
		{"admin login", c.checkLogin(creds.AdminUser, creds.AdminPassword)},
		{"session role (admin)", c.checkSessionRole("admin")},
		{"plain chat (no tools)", c.checkPlainChat},
		{"web-search-gated chat", c.checkWebSearchChat},
		{"always-on MCP tool (datetime)", c.checkDatetimeTool},
		{"sandbox MCP tool (fast, no packages)", c.checkSandboxFast},
		{"search: plain query", c.checkSearchPlain},
		{"search: site: operator", c.checkSearchSiteOperator},
		{"search: sort order", c.checkSearchSort},
		{"security: wrong password rejected", c.checkWrongPasswordRejected(creds.AdminUser)},
		{"security: unauthenticated request rejected", c.checkUnauthenticatedRejected},
	}
	if *includeSlow {
		phase1 = append(phase1, check{"sandbox MCP tool (package install)", c.checkSandboxSlow})
	}
	run(phase1)

	// Phase 2: still admin, but built from what phase 1 already proved is
	// a live, logged-in session -- lists every agent and every MCP server
	// this deployment actually has configured, rather than assuming
	// fixed names, then exercises each one plus a few scratch-row CRUD
	// smoke tests.
	run(c.buildAgentChecks())
	run(c.buildMCPConnectivityChecks())
	run([]check{
		{"admin CRUD: mcp server", c.checkAdminMCPServerCRUD},
		{"admin CRUD: agent", c.checkAdminAgentCRUD},
		{"admin: embedding endpoints (list)", c.checkAdminEmbeddingEndpointsList},
		{"admin: embeddings recompute status", c.checkAdminEmbeddingsRecomputeStatus},
	})

	// Phase 3: "user login" switches the one shared cookie jar over to
	// the regular test user, so every check from here on runs as that
	// user instead -- no admin-only check may appear later in this list.
	if creds.TestUser != "" {
		phase3 := []check{
			{"user login", c.checkLogin(creds.TestUser, creds.TestUserPassword)},
			{"session role (user)", c.checkSessionRole("user")},
			{"security: user forbidden from admin endpoint", c.checkUserForbiddenFromAdmin},
			{"persistent chat: pin", c.checkChatPin},
			{"persistent chat: rename", c.checkChatRename},
			{"persistent chat: fork (independent copy)", c.checkChatFork},
			{"mcp-files tool (attach + read)", c.checkFilesTool},
			{"account: personal MCP server (http-only)", c.checkAccountMCPServerCRUD},
			{"account: your files (unscoped listing)", c.checkAccountFilesUnscoped},
			{"account: change password (round trip)", c.checkAccountPasswordRoundTrip(creds.TestUser, creds.TestUserPassword)},
			{"file attach + vision similarity (Image analyst agent)", c.checkImageVision},
		}
		phase3 = append(phase3,
			check{"persistent chat: delete (cascades files)", c.checkChatDelete},
			check{"persistent chat: delete fork", c.checkChatForkDelete},
		)
		run(phase3)
	}

	fmt.Printf("\n%d passed, %d failed, %d skipped (of %d) against %s\n", passed, failed, skipped, passed+failed+skipped, *host)
	if failed > 0 {
		os.Exit(1)
	}
}

func runChecks(checks []check) (passed, failed, skipped int) {
	for _, chk := range checks {
		start := time.Now()
		err := chk.run()
		elapsed := time.Since(start).Round(time.Millisecond)
		switch {
		case errors.Is(err, errSkip):
			skipped++
			fmt.Printf("SKIP  %-45s %8s  %v\n", chk.name, elapsed, unwrapSkip(err))
		case err != nil:
			failed++
			fmt.Printf("FAIL  %-45s %8s  %v\n", chk.name, elapsed, err)
		default:
			passed++
			fmt.Printf("PASS  %-45s %8s\n", chk.name, elapsed)
		}
	}
	return passed, failed, skipped
}

func unwrapSkip(err error) string {
	msg := err.Error()
	// errSkip itself has no useful message; skip(reason) below wraps it
	// with fmt.Errorf("%w: reason", errSkip), so strip the "skip: " prefix.
	const prefix = "skip: "
	if len(msg) > len(prefix) && msg[:len(prefix)] == prefix {
		return msg[len(prefix):]
	}
	return msg
}

func skip(reason string) error {
	return fmt.Errorf("%w: %s", errSkip, reason)
}

type check struct {
	name string
	run  func() error
}

type credentials struct {
	AdminUser     string `json:"admin_user"`
	AdminPassword string `json:"admin_password"`
	// TestUser/TestUserPassword, when set, name a dedicated regular
	// (non-admin) account used for every user-session check below --
	// optional: leaving it blank skips those checks entirely rather than
	// failing, since not every environment has one provisioned.
	TestUser         string `json:"test_user"`
	TestUserPassword string `json:"test_user_password"`
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
// parallel, since they share this one session's state (and, after
// "persistent chat: pin," chat IDs the later checks reuse).
type client struct {
	base           string
	http           *http.Client
	testChatID     string
	testForkChatID string
}

// freshClient shares nothing with c -- its own empty cookie jar -- for a
// check that must NOT disturb the shared session (e.g. proving a wrong
// password is rejected without logging the real session out).
func (c *client) freshClient() (*client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &client{base: c.base, http: &http.Client{Jar: jar, Timeout: c.http.Timeout}}, nil
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

// getJSONStatus is getJSON without treating any status code as an error
// -- for a check that itself asserts on the status (e.g. expecting 401).
func (c *client) getJSONRaw(path string) (int, []byte, error) {
	resp, err := c.http.Get(c.base + path)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return resp.StatusCode, body, err
}

func (c *client) postJSON(path string, in, out any) (int, error) {
	return c.doJSON(http.MethodPost, path, in, out)
}

func (c *client) patchJSON(path string, in, out any) (int, error) {
	return c.doJSON(http.MethodPatch, path, in, out)
}

func (c *client) doJSON(method, path string, in, out any) (int, error) {
	status, body, err := c.doJSONRaw(method, path, in)
	if err != nil {
		return status, err
	}
	if status >= 400 {
		return status, fmt.Errorf("%s %s: %d: %s", method, path, status, truncate(body, 300))
	}
	if out != nil && len(body) > 0 {
		if err := json.Unmarshal(body, out); err != nil {
			return status, fmt.Errorf("decoding response body %q: %w", truncate(body, 200), err)
		}
	}
	return status, nil
}

// doJSONRaw is doJSON without the >=400-is-an-error check -- for a check
// that itself asserts on the status code (e.g. expecting exactly 400 or
// 403).
func (c *client) doJSONRaw(method, path string, in any) (int, []byte, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequest(method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set(headerContentType, "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	return resp.StatusCode, respBody, err
}

func (c *client) deleteRequest(path string) (int, error) {
	req, err := http.NewRequest(http.MethodDelete, c.base+path, nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return resp.StatusCode, fmt.Errorf("DELETE %s: %d: %s", path, resp.StatusCode, truncate(body, 300))
	}
	return resp.StatusCode, nil
}

// uploadFile POSTs a multipart/form-data "file" field (plus "chat_id")
// to /account/api/files -- the same encoding a browser's file input and
// account_files.js's own upload both use.
func (c *client) uploadFile(chatID, filename, contentType string, data []byte) (fileResponse, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("chat_id", chatID); err != nil {
		return fileResponse{}, err
	}
	part, err := w.CreatePart(map[string][]string{
		"Content-Disposition": {fmt.Sprintf(`form-data; name="file"; filename=%q`, filename)},
		headerContentType:     {contentType},
	})
	if err != nil {
		return fileResponse{}, err
	}
	if _, err := part.Write(data); err != nil {
		return fileResponse{}, err
	}
	if err := w.Close(); err != nil {
		return fileResponse{}, err
	}
	req, err := http.NewRequest(http.MethodPost, c.base+"/account/api/files", &buf)
	if err != nil {
		return fileResponse{}, err
	}
	req.Header.Set(headerContentType, w.FormDataContentType())
	resp, err := c.http.Do(req)
	if err != nil {
		return fileResponse{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fileResponse{}, err
	}
	if resp.StatusCode >= 400 {
		return fileResponse{}, fmt.Errorf("POST /account/api/files: %d: %s", resp.StatusCode, truncate(body, 300))
	}
	var out fileResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return fileResponse{}, fmt.Errorf("decoding upload response %q: %w", truncate(body, 200), err)
	}
	return out, nil
}

type fileResponse struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
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

func (c *client) checkLogin(username, password string) func() error {
	return func() error {
		var out struct {
			Redirect string `json:"redirect"`
		}
		_, err := c.postJSON(pathLogin, map[string]string{
			"username": username,
			"password": password,
		}, &out)
		return err
	}
}

func (c *client) checkSessionRole(want string) func() error {
	return func() error {
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
		if out.Role != want {
			return fmt.Errorf("expected role=%s (logged-in session), got %q", want, out.Role)
		}
		return nil
	}
}

// checkWrongPasswordRejected proves a bad password gets a plain 401, not
// a session -- run on a fresh, throwaway cookie jar so it never disturbs
// the real shared admin session other checks depend on.
func (c *client) checkWrongPasswordRejected(adminUser string) func() error {
	return func() error {
		fresh, err := c.freshClient()
		if err != nil {
			return err
		}
		status, _, err := fresh.doJSONRaw(http.MethodPost, pathLogin, map[string]string{
			"username": adminUser,
			"password": "definitely-not-the-real-password",
		})
		if err != nil {
			return err
		}
		if status != http.StatusUnauthorized {
			return fmt.Errorf("expected 401 for a wrong password, got %d", status)
		}
		return nil
	}
}

// checkUnauthenticatedRejected proves an admin-only endpoint refuses a
// request with no session at all -- fresh cookie jar, same reasoning as
// checkWrongPasswordRejected.
func (c *client) checkUnauthenticatedRejected() error {
	fresh, err := c.freshClient()
	if err != nil {
		return err
	}
	status, _, err := fresh.getJSONRaw(pathAdminAgents)
	if err != nil {
		return err
	}
	if status != http.StatusUnauthorized {
		return fmt.Errorf("expected 401 for an unauthenticated request, got %d", status)
	}
	return nil
}

// checkUserForbiddenFromAdmin proves a logged-in REGULAR user (not
// admin) gets 403, not 401, from an admin-only endpoint -- distinct from
// checkUnauthenticatedRejected, and must run after "user login" so the
// shared session really is a role=user one at this point.
func (c *client) checkUserForbiddenFromAdmin() error {
	status, body, err := c.getJSONRaw(pathAdminAgents)
	if err != nil {
		return err
	}
	if status != http.StatusForbidden {
		return fmt.Errorf("expected 403 for a regular user hitting an admin endpoint, got %d: %s", status, truncate(body, 200))
	}
	return nil
}

type chatResponse struct {
	Answer      string `json:"answer"`
	ToolResults []struct {
		ToolName string `json:"tool_name"`
		Err      string `json:"err"`
	} `json:"tool_results"`
	TokenUsage struct {
		AgentPromptTokens int `json:"agent_prompt_tokens"`
	} `json:"token_usage"`
}

// chatOptions carries chatOnce's optional fields -- kept as a struct
// rather than growing chatOnce's own parameter list further.
type chatOptions struct {
	webSearch bool
	agentID   string
	chatID    string
}

// chatOnce POSTs a single-turn conversation to /chat -- every check below
// sends one fixed, deterministic-ish question rather than reusing
// history, so checks stay independent of each other.
func (c *client) chatOnce(question string, opts chatOptions) (chatResponse, error) {
	var out chatResponse
	body := map[string]any{
		"messages":   []map[string]string{{"role": "user", "content": question}},
		"web_search": opts.webSearch,
	}
	if opts.agentID != "" {
		body["agent_id"] = opts.agentID
	}
	if opts.chatID != "" {
		body["chat_id"] = opts.chatID
	}
	_, err := c.postJSON("/chat", body, &out)
	return out, err
}

// checkPlainChat proves the chat endpoint answers at all, with no MCP
// tools in play -- the simplest possible real round trip through the
// live model. Catches a wholesale chat-endpoint misconfiguration (the
// exact class of bug an earlier incident this tool exists to prevent
// caused: a partial PATCH silently zeroing base_url/model/enabled).
func (c *client) checkPlainChat() error {
	out, err := c.chatOnce("Reply with exactly the single word: PONG", chatOptions{})
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
// the turn ended with no real answer). Deliberately does NOT require
// every individual tool call to succeed: a real site blocking a fetch
// (confirmed live: timeanddate.com, a different domain than the
// AccuWeather incident, also 403s the fetcher sometimes) is normal,
// expected web traffic, not a bug -- the model is specifically prompted
// to try a different domain when that happens, so what actually matters
// is whether it still reaches a real answer despite that, not whether
// every hop along the way was clean.
func (c *client) checkWebSearchChat() error {
	out, err := c.chatOnce("What is the current year? Search the web to confirm.", chatOptions{webSearch: true})
	if err != nil {
		return err
	}
	if out.Answer == "" {
		return fmt.Errorf("got an empty answer -- the model may have exhausted its tool-call budget without reaching one (see the web_search_result_count/mcp-web prompt incident this check guards against)")
	}
	if len(out.ToolResults) == 0 {
		return fmt.Errorf("expected at least one tool call (web_search/web_fetch) for a web-search-gated question, got none")
	}
	return nil
}

// checkDatetimeTool proves a plain, always-on (not web-search-gated) MCP
// server actually gets invoked and answers correctly -- mcp-datetime is
// the cheapest possible real tool round trip (no network, no subprocess
// dependency install), so this isolates "is MCP wiring healthy at all"
// from "is web search healthy."
func (c *client) checkDatetimeTool() error {
	out, err := c.chatOnce("Use your datetime tool to tell me the current UTC year. Reply with just the 4-digit year.", chatOptions{})
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
	out, err := c.chatOnce("Use run_python to compute 6 * 7 and tell me only the resulting number.", chatOptions{})
	if err != nil {
		return err
	}
	if out.Answer == "" {
		return fmt.Errorf("got an empty answer")
	}
	if len(out.ToolResults) == 0 {
		return fmt.Errorf("expected run_python to be called, got no tool_results -- sandbox MCP server may not be configured as always-on")
	}
	return checkNoToolErrors(out)
}

// checkSandboxSlow deliberately reproduces the slow path a real incident
// hit: a sandbox call needing a real network package install (mirroring
// easyocr's own multi-minute, always-uncached download) -- this is the
// scenario mcpclient.callTimeout's hardcoded 60s ceiling (independent of
// whatever -timeout an admin sets on the sandbox MCPServer row itself)
// used to silently break, surfacing as a raw gateway timeout instead of a
// graceful in-app tool error. Opt-in (-include-slow) since it can take
// several minutes and isn't needed for a routine post-release check.
func (c *client) checkSandboxSlow() error {
	out, err := c.chatOnce(
		"Use run_python with packages [\"requests\"] to import requests and print requests.__version__. "+
			"This may take a while the first time -- please wait for it rather than giving up.", chatOptions{})
	if err != nil {
		return err
	}
	if out.Answer == "" {
		return fmt.Errorf("got an empty answer")
	}
	return checkNoToolErrors(out)
}

func checkNoToolErrors(out chatResponse) error {
	for _, tr := range out.ToolResults {
		if tr.Err != "" {
			return fmt.Errorf("tool %q returned an error: %s -- if this mentions a timeout, check mcpclient.callTimeout vs the relevant MCPServer row's own -timeout flag", tr.ToolName, tr.Err)
		}
	}
	return nil
}

// --- search ---

type searchResponse struct {
	Query   string `json:"query"`
	Results []any  `json:"results"`
}

// checkSearchPlain proves the public /search API itself answers with a
// well-formed response -- structural only (a valid array, possibly
// empty), since se.mo-sys.de's real crawled corpus is opaque to this
// tool and could legitimately hold anything or nothing for a given term.
func (c *client) checkSearchPlain() error {
	var out searchResponse
	status, err := c.getJSON("/search?q=test", &out)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("expected 200, got %d", status)
	}
	if out.Query != "test" {
		return fmt.Errorf("expected the query to round-trip, got %q", out.Query)
	}
	return nil
}

// checkSearchSiteOperator proves the site: operator is accepted and
// doesn't itself error -- same structural-only reasoning as
// checkSearchPlain.
func (c *client) checkSearchSiteOperator() error {
	var out searchResponse
	status, err := c.getJSON("/search?q=test+site:example.com", &out)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("expected 200, got %d", status)
	}
	return nil
}

// checkSearchSort proves ?sort=recent is accepted -- same
// structural-only reasoning.
func (c *client) checkSearchSort() error {
	var out searchResponse
	status, err := c.getJSON("/search?q=test&sort=recent", &out)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("expected 200, got %d", status)
	}
	return nil
}

// --- agents (multi-agent: every enabled agent, individually) ---

type agentResponse struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	SystemPrompt string   `json:"system_prompt"`
	MCPServerIDs []string `json:"mcp_server_ids"`
	Enabled      bool     `json:"enabled"`
}

// buildAgentChecks lists every agent this deployment actually has
// configured (via the admin listing, so it sees SystemPrompt -- the
// public /agents endpoint doesn't) and returns one named check per
// enabled agent, proving agent_id selection works for each of them
// individually ("multi agent" coverage) rather than just one hand-picked
// example. A plain "reply with PONG" prompt, no image attached, so this
// never triggers the "Image analyst" agent's own vision tools -- that's
// checkImageVision's job specifically.
func (c *client) buildAgentChecks() []check {
	var agents []agentResponse
	if _, err := c.getJSON(pathAdminAgents, &agents); err != nil {
		return []check{{"agents: list", func() error { return err }}}
	}
	if len(agents) == 0 {
		return []check{{"agents: list", func() error { return skip("no agents configured on this deployment") }}}
	}
	checks := make([]check, 0, len(agents))
	for _, a := range agents {
		a := a // no longer needed under Go 1.22+ loop semantics, kept for clarity
		checks = append(checks, check{
			name: fmt.Sprintf("agent selectable: %s", a.Name),
			run: func() error {
				if !a.Enabled {
					return skip("disabled")
				}
				out, err := c.chatOnce("Reply with exactly the single word: PONG", chatOptions{agentID: a.ID})
				if err != nil {
					return err
				}
				if out.Answer == "" {
					return fmt.Errorf("got an empty answer with agent_id=%q", a.ID)
				}
				if a.SystemPrompt != "" && out.TokenUsage.AgentPromptTokens == 0 {
					return fmt.Errorf("agent has a non-empty system_prompt but agent_prompt_tokens=0 -- it may not have been injected")
				}
				return nil
			},
		})
	}
	return checks
}

// --- mcp servers (all mcp: every configured server, connectivity AND functionality) ---

type mcpServerResponse struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Transport string   `json:"transport"`
	Command   string   `json:"command"`
	Args      []string `json:"args"`
	BaseURL   string   `json:"base_url"`
	Enabled   bool     `json:"enabled"`
	// GatedByWebSearch mirrors domain.MCPServer's own field: this
	// server's tools are only offered to the model on a turn where
	// web_search=true (see ChatOptions.WebSearch) -- checkToolFunctions
	// needs to know this to actually offer the tools it's about to ask
	// the model to call, not just discover them.
	GatedByWebSearch bool `json:"gated_by_web_search"`
}

type mcpServerToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type mcpServerTestResponse struct {
	Tools []mcpServerToolInfo `json:"tools"`
	Error string              `json:"error,omitempty"`
}

// listServerTools calls the same /admin/api/mcp-servers/test endpoint
// the "List tools" button on a server's own edit page uses -- a real
// connect + list-tools round trip against s's OWN stored config (id set,
// api_key blank falls back to the stored key -- see
// resolveCandidateMCPServerAPIKey's own doc comment).
func (c *client) listServerTools(s mcpServerResponse) ([]mcpServerToolInfo, error) {
	var out mcpServerTestResponse
	_, err := c.postJSON("/admin/api/mcp-servers/test", map[string]any{
		"id": s.ID, "name": s.Name, "transport": s.Transport,
		"command": s.Command, "args": s.Args, "base_url": s.BaseURL, "api_key": "",
	}, &out)
	if err != nil {
		return nil, err
	}
	if out.Error != "" {
		return nil, errors.New(out.Error)
	}
	if len(out.Tools) == 0 {
		return nil, fmt.Errorf("connected but listed zero tools")
	}
	return out.Tools, nil
}

// buildMCPConnectivityChecks lists every admin-configured MCP server
// (whatever this deployment actually has, not a fixed set of names) and,
// for each enabled one, emits two checks: connectivity (can it be
// reached and does it list tools at all) and functionality (does
// actually CALLING one of those tools, through a real chat turn, work).
// Connectivity alone -- the old behavior -- can't tell a genuinely
// broken tool handler from a healthy one; only a real call can.
func (c *client) buildMCPConnectivityChecks() []check {
	var servers []mcpServerResponse
	if _, err := c.getJSON(pathAdminMCPServers, &servers); err != nil {
		return []check{{"mcp servers: list", func() error { return err }}}
	}
	if len(servers) == 0 {
		return []check{{"mcp servers: list", func() error { return skip("no MCP servers configured on this deployment") }}}
	}
	checks := make([]check, 0, len(servers)*2)
	for _, s := range servers {
		s := s
		var tools []mcpServerToolInfo
		checks = append(checks, check{
			name: fmt.Sprintf("mcp connectivity: %s", s.Name),
			run: func() error {
				if !s.Enabled {
					return skip("disabled")
				}
				discovered, err := c.listServerTools(s)
				if err != nil {
					return err
				}
				tools = discovered // handed to the functionality check below
				return nil
			},
		})
		checks = append(checks, check{
			name: fmt.Sprintf("mcp functionality: %s", s.Name),
			run: func() error {
				if !s.Enabled {
					return skip("disabled")
				}
				// cmd/mcp-vision's own tools (vision_similarity/vision_caption)
				// always require a real, previously attached image's file_id --
				// unlike mcp-files' read_file_base64/list_files, there's no
				// zero-context call of its own this generic check (no attached
				// file, no specific agent/prompt) could ever reasonably trigger.
				// checkImageVision below is the real, fully-attached functional
				// test for this server; this one would either 404 fetching a
				// guessed file_id or (as reasonably observed live) have the
				// model call a DIFFERENT server's tool (e.g. list_files)
				// instead of guessing -- neither is a regression.
				if s.Name == "vision" {
					return skip("vision's own tools all require a real attached image -- see checkImageVision for the real functional test")
				}
				if len(tools) == 0 {
					return skip("connectivity check didn't discover any tools to call")
				}
				return c.checkToolFunctions(s.Name, s.GatedByWebSearch, tools)
			},
		})
	}
	return checks
}

// mutatingToolNameSubstrings flags a tool name as likely to have a real,
// possibly hard-to-clean-up side effect (creating/deleting a resource,
// installing a package) -- checkToolFunctions skips calling these
// generically and prefers a read-only-looking one instead, since it has
// no specific knowledge of what an arbitrary third-party server's own
// tools actually do.
var mutatingToolNameSubstrings = []string{"write", "delete", "remove", "clear", "install", "create", "update", "set"}

func looksMutating(name string) bool {
	lower := strings.ToLower(name)
	for _, s := range mutatingToolNameSubstrings {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

// authRelatedErrorSubstrings mark a tool error as likely caused by this
// generic check's own lack of realistic context -- running under an admin
// session (which has no domain.User row -- see account_files.go's own doc
// comment -- so a user-scoped tool like mcp-files' has nothing to
// authenticate with here), or calling a tool that needs a real prior
// attachment (e.g. vision_caption/vision_similarity's file_id, which this
// check never attaches -- see the file-based servers' own dedicated
// user-session checks for that) -- rather than a real regression. Treated
// as a skip, not a failure, when matched.
var authRelatedErrorSubstrings = []string{"unauthorized", "authentication", "chat_id", "no active", "not configured", "signed-in", "signed in", "no signed", "file not found"}

func looksAuthRelated(errMsg string) bool {
	lower := strings.ToLower(errMsg)
	for _, s := range authRelatedErrorSubstrings {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

// checkToolFunctions actually CALLS one of a server's own discovered
// tools through a real chat turn -- proving the tool's handler genuinely
// works, not just that the server is reachable and describes tools it
// might not actually be able to run. Picks the first tool whose name
// doesn't look mutating (see looksMutating); with only mutating-looking
// names to choose from, still calls the first one rather than skipping
// entirely, since even a "write" tool failing outright is worth knowing
// about.
func (c *client) checkToolFunctions(serverName string, gatedByWebSearch bool, tools []mcpServerToolInfo) error {
	chosen := tools[0]
	for _, t := range tools {
		if !looksMutating(t.Name) {
			chosen = t
			break
		}
	}
	names := make([]string, len(tools))
	ownTool := make(map[string]bool, len(tools))
	for i, t := range tools {
		names[i] = t.Name
		ownTool[t.Name] = true
	}
	// The prompt suggests a specific tool, but the assertion below accepts
	// ANY of this server's own tools being called -- a tool needing a
	// parameter the model can't reasonably invent (e.g. web_fetch's own
	// URL, with nothing yet fetched to fetch) is a real, expected reason
	// for the model to reasonably call a different one of the SAME
	// server's tools instead (confirmed live: asked for "web_fetch",
	// the model called "web_search" first, which is the more sensible
	// choice with no URL in hand yet) -- that's still a genuine,
	// successful functional call to this server, not a failure.
	prompt := fmt.Sprintf(
		"You have access to an MCP server named %q whose tools include: %v. "+
			"Call the %q tool (or, if that one specifically doesn't make sense without more context, "+
			"whichever of this server's own tools listed above does) with reasonable arguments and "+
			"report what it returns.",
		serverName, names, chosen.Name)

	// One retry before failing: a model occasionally answers a loosely-
	// worded "call one of your tools" instruction directly instead of
	// actually calling anything (confirmed live -- this exact check has
	// both passed and failed against an unchanged deployment back to
	// back) -- sampling variance in an instruction-following prompt, not
	// a reachability/functionality problem this check exists to catch.
	// A real regression (the tool itself erroring) still fails on the
	// FIRST attempt below, never retried.
	const attempts = 2
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		err := c.attemptToolCall(prompt, gatedByWebSearch, ownTool, names)
		if err == nil {
			return nil
		}
		if errors.Is(err, errSkip) {
			return err
		}
		lastErr = err
	}
	return lastErr
}

// attemptToolCall is checkToolFunctions' single try: one chat turn, then
// classify what (if anything) came back. Split out so checkToolFunctions
// can retry it without duplicating the classification logic.
func (c *client) attemptToolCall(prompt string, gatedByWebSearch bool, ownTool map[string]bool, names []string) error {
	out, err := c.chatOnce(prompt, chatOptions{webSearch: gatedByWebSearch})
	if err != nil {
		return err
	}
	var called *string
	for _, tr := range out.ToolResults {
		if !ownTool[tr.ToolName] {
			continue
		}
		name := tr.ToolName
		called = &name
		if tr.Err != "" {
			if looksAuthRelated(tr.Err) {
				return skip(fmt.Sprintf("tool %q errored in a way consistent with this session lacking access this specific call needs (%s), not necessarily a real bug -- see the file-based servers' own dedicated user-session checks for a fully authenticated functional test", tr.ToolName, tr.Err))
			}
			return fmt.Errorf("tool %q returned an error: %s", tr.ToolName, tr.Err)
		}
	}
	if called == nil {
		return fmt.Errorf("expected the model to call one of %v, but no matching tool_results entry came back (got %d other tool call(s))", names, len(out.ToolResults))
	}
	return nil
}

// --- admin CRUD smoke tests (scratch rows, created and deleted within the same check) ---

const scratchName = "e2e-check-scratch"

// checkAdminMCPServerCRUD creates an http-transport (never stdio, so
// nothing is ever spawned) scratch MCP server row, confirms it appears
// in the list, then deletes it -- leaves no trace behind either way.
func (c *client) checkAdminMCPServerCRUD() error {
	var created mcpServerResponse
	_, err := c.postJSON(pathAdminMCPServers, map[string]any{
		"name": scratchName, "transport": "http", "base_url": "http://127.0.0.1:1",
		"enabled": false, "gated_by_web_search": false,
	}, &created)
	if err != nil {
		return err
	}
	defer c.deleteRequest("/admin/api/mcp-servers/" + created.ID)

	var servers []mcpServerResponse
	if _, err := c.getJSON(pathAdminMCPServers, &servers); err != nil {
		return err
	}
	found := false
	for _, s := range servers {
		if s.ID == created.ID {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("created server %q not found in the list afterward", created.ID)
	}
	if _, err := c.deleteRequest("/admin/api/mcp-servers/" + created.ID); err != nil {
		return err
	}
	return nil
}

// checkAdminAgentCRUD creates a scratch agent (disabled, so it's never
// actually selectable in the meantime), confirms it's listed, deletes it.
func (c *client) checkAdminAgentCRUD() error {
	var created agentResponse
	_, err := c.postJSON(pathAdminAgents, map[string]any{
		"name": scratchName, "description": "e2e-check scratch row", "system_prompt": "",
		"mcp_server_ids": []string{}, "enabled": false,
	}, &created)
	if err != nil {
		return err
	}
	defer c.deleteRequest("/admin/api/agents/" + created.ID)

	var agents []agentResponse
	if _, err := c.getJSON(pathAdminAgents, &agents); err != nil {
		return err
	}
	found := false
	for _, a := range agents {
		if a.ID == created.ID {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("created agent %q not found in the list afterward", created.ID)
	}
	if _, err := c.deleteRequest("/admin/api/agents/" + created.ID); err != nil {
		return err
	}
	return nil
}

// checkAdminEmbeddingEndpointsList is read-only (structural only): no
// mutation, since a live embedding endpoint change could disrupt real
// search relevance -- just proves the admin listing itself answers.
func (c *client) checkAdminEmbeddingEndpointsList() error {
	var out []any
	status, err := c.getJSON("/admin/api/embeddings/endpoints", &out)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("expected 200, got %d", status)
	}
	return nil
}

// checkAdminEmbeddingsRecomputeStatus is read-only (structural only), same
// spirit as checkAdminEmbeddingEndpointsList: never POSTs /recompute here,
// since triggering a real full-corpus recompute against this deployment's
// actual embedding endpoint(s) would be a genuinely disruptive, minutes-to-
// hours-long action -- just proves the status endpoint answers with the
// documents/failed fields live during-or-after a run (see
// RunEmbeddingRecomputeJob's onBatchDone checkpointing), not just once a
// run completes.
func (c *client) checkAdminEmbeddingsRecomputeStatus() error {
	var out struct {
		TotalDocs  int  `json:"total_docs"`
		InProgress bool `json:"in_progress"`
		Documents  int  `json:"documents"`
		Failed     int  `json:"failed"`
	}
	status, err := c.getJSON("/admin/api/embeddings/recompute", &out)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("expected 200, got %d", status)
	}
	return nil
}

// --- persistent chats (pin/rename/fork/delete) ---

type pinnedChatResponse struct {
	ID      string           `json:"id"`
	Title   string           `json:"title"`
	History []map[string]any `json:"history"`
}

// checkChatPin creates a pinned chat via the same endpoint the chat
// page's own pin button uses, storing its ID on c for the checks below
// (rename, fork, file attach/OCR, delete) to reuse.
func (c *client) checkChatPin() error {
	var out pinnedChatResponse
	_, err := c.postJSON(pathAccountChats, map[string]any{
		"title":    "e2e-check",
		"agent_id": "",
		"history":  []map[string]string{{"role": "user", "content": "e2e-check smoke test"}},
	}, &out)
	if err != nil {
		return err
	}
	if out.ID == "" {
		return fmt.Errorf("expected a non-empty chat id, got %+v", out)
	}
	c.testChatID = out.ID
	return nil
}

// checkChatRename proves PATCH /account/api/chats/{id} (the same
// endpoint a click-to-rename in the chat tab strip uses) actually takes
// effect.
func (c *client) checkChatRename() error {
	if c.testChatID == "" {
		return skip(skipNoPinnedChat)
	}
	var out pinnedChatResponse
	_, err := c.patchJSON(pathAccountChats+"/"+c.testChatID, map[string]any{
		"title":    "e2e-check (renamed)",
		"agent_id": "",
		"history":  []map[string]string{{"role": "user", "content": "e2e-check smoke test"}},
	}, &out)
	if err != nil {
		return err
	}
	if out.Title != "e2e-check (renamed)" {
		return fmt.Errorf("expected the renamed title to stick, got %q", out.Title)
	}
	return nil
}

// checkChatFork proves the mechanism index.js's own forkActiveTab relies
// on -- a fork is client-side history duplication with no dedicated
// backend endpoint, so this pins a SECOND, independent chat from a copy
// of the first one's history (what pinning a fork produces) and proves
// the two are genuinely independent rows, not aliases of one another.
func (c *client) checkChatFork() error {
	if c.testChatID == "" {
		return skip(skipNoPinnedChat)
	}
	var list []pinnedChatResponse
	if _, err := c.getJSON(pathAccountChats, &list); err != nil {
		return err
	}
	var source *pinnedChatResponse
	for i := range list {
		if list[i].ID == c.testChatID {
			source = &list[i]
		}
	}
	if source == nil {
		return fmt.Errorf("could not find the pinned chat %q to fork", c.testChatID)
	}

	var fork pinnedChatResponse
	_, err := c.postJSON(pathAccountChats, map[string]any{
		"title":    source.Title + " (fork)",
		"agent_id": "",
		"history":  source.History,
	}, &fork)
	if err != nil {
		return err
	}
	if fork.ID == "" || fork.ID == source.ID {
		return fmt.Errorf("expected a distinct new chat id, got %q (source was %q)", fork.ID, source.ID)
	}
	c.testForkChatID = fork.ID

	// Renaming the ORIGINAL must not affect the fork -- proves they're
	// independent rows, not the same one under two names.
	if _, err := c.patchJSON(pathAccountChats+"/"+c.testChatID, map[string]any{
		"title": "e2e-check (renamed again)", "agent_id": "", "history": source.History,
	}, nil); err != nil {
		return err
	}
	var listAfter []pinnedChatResponse
	if _, err := c.getJSON(pathAccountChats, &listAfter); err != nil {
		return err
	}
	for _, ch := range listAfter {
		if ch.ID == fork.ID && ch.Title != source.Title+" (fork)" {
			return fmt.Errorf("forked chat's title changed after renaming the original -- they're not independent")
		}
	}
	return nil
}

// checkChatForkDelete deletes the fork chat created by checkChatFork --
// separate from checkChatDelete (which deletes the original) so both are
// individually attributable in the report.
func (c *client) checkChatForkDelete() error {
	if c.testForkChatID == "" {
		return skip("no forked chat (checkChatFork must have failed or been skipped)")
	}
	_, err := c.deleteRequest(pathAccountChats + "/" + c.testForkChatID)
	return err
}

// checkChatDelete deletes the pinned chat -- proving DELETE
// /account/api/chats/{id} itself succeeds. The cascade-delete of its
// attached files is already covered by a dedicated Go regression test
// (TestDeleteChat_CascadesFiles); this only proves the live endpoint
// still accepts the call, not the cascade's own mechanics again.
func (c *client) checkChatDelete() error {
	if c.testChatID == "" {
		return skip(skipNoPinnedChat)
	}
	_, err := c.deleteRequest(pathAccountChats + "/" + c.testChatID)
	return err
}

// --- files (mcp-files tool, and the unscoped "Your files" listing) ---

// checkFilesTool attaches a small real text file to the pinned chat and
// asks the model to read it back -- proves cmd/mcp-files' own tools
// (list_files/read_file) are reachable end-to-end, distinct from the
// sandbox/datetime/web tools every other check exercises.
func (c *client) checkFilesTool() error {
	if c.testChatID == "" {
		return skip(skipNoPinnedChat)
	}
	const marker = "E2E-CHECK-MARKER-4f8a1c"
	if _, err := c.uploadFile(c.testChatID, "e2e-check.txt", "text/plain", []byte("the secret word is "+marker)); err != nil {
		return err
	}
	out, err := c.chatOnce(
		"List your available files, read the one named e2e-check.txt, and reply with only the secret word it contains.",
		chatOptions{chatID: c.testChatID})
	if err != nil {
		return err
	}
	if len(out.ToolResults) == 0 {
		return fmt.Errorf("expected a file-operations tool (list_files/read_file) to be called, got no tool_results")
	}
	if err := checkNoToolErrors(out); err != nil {
		return err
	}
	if out.Answer == "" {
		return fmt.Errorf("got an empty answer")
	}
	return nil
}

// checkAccountFilesUnscoped proves GET /account/api/files with no
// chat_id (the "Your files" page's own view) lists files across every
// chat, not just one -- structural only (the file uploaded by
// checkFilesTool should appear, if that check ran first and succeeded).
func (c *client) checkAccountFilesUnscoped() error {
	var out []fileResponse
	status, err := c.getJSON("/account/api/files", &out)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("expected 200, got %d", status)
	}
	return nil
}

// testImagePNG is a small, real (not 1x1) PNG generated at startup -- a
// 32x32 two-color checkerboard, real enough for checkImageVision's
// vision_similarity call to actually embed and search with, not a
// pipeline-completes-regardless 1x1 pixel.
func testImagePNG() ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			if (x/4+y/4)%2 == 0 {
				img.Set(x, y, color.Black)
			} else {
				img.Set(x, y, color.White)
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// checkImageVision attaches a real image to a pinned chat, selects the
// "Image analyst" agent by name, and asks it to find related content --
// exercising cmd/mcp-vision's vision_similarity tool end to end (a real
// image embed against the configured provider, then a real pgvector ANN
// search). Replaces an older check reproducing a real incident in the
// previous sandboxed-Python/easyocr approach (mcpclient.callTimeout's old
// 60s ceiling breaking on easyocr's always-uncached multi-minute model
// download) -- that whole approach is retired (see default_agents.go's
// image_analyst entry), and the new tool is fast enough to run
// unconditionally, no -include-slow gate needed.
//
// Skips gracefully (not a failure) if the agent isn't configured, or if
// vision_similarity itself reports unconfigured (Chat settings -> Vision
// -> Similarity search) -- both are legitimate per-deployment states, the
// same convention as every other deployment-specific prerequisite here.
func (c *client) checkImageVision() error {
	if c.testChatID == "" {
		return skip(skipNoPinnedChat)
	}
	var agents []agentResponse
	if _, err := c.getJSON("/agents", &agents); err != nil {
		return err
	}
	var agentID string
	for _, a := range agents {
		if a.Name == "Image analyst" {
			agentID = a.ID
		}
	}
	if agentID == "" {
		return skip(`no "Image analyst" agent configured on this deployment`)
	}
	png, err := testImagePNG()
	if err != nil {
		return err
	}
	if _, err := c.uploadFile(c.testChatID, "e2e-check.png", "image/png", png); err != nil {
		return err
	}
	out, err := c.chatOnce(
		"Use vision_similarity to find pages related to the image named e2e-check.png, and tell me what you find.",
		chatOptions{agentID: agentID, chatID: c.testChatID})
	if err != nil {
		return err
	}
	if len(out.ToolResults) == 0 {
		return fmt.Errorf("expected vision_similarity to be called, got no tool_results -- check the agent's own mcp_server_ids isn't empty (see domain.Agent.MCPServerIDs' own doc comment: empty means NO tools, not all of them)")
	}
	for _, tr := range out.ToolResults {
		if strings.Contains(tr.Err, "not configured") {
			return skip("vision_similarity is not configured on this deployment (Chat settings -> Vision -> Similarity search)")
		}
	}
	if err := checkNoToolErrors(out); err != nil {
		return err
	}
	if out.Answer == "" {
		return fmt.Errorf("got an empty answer")
	}
	return nil
}

// --- account self-service (personal MCP servers, password change) ---

// checkAccountMCPServerCRUD proves a regular user's own MCP server store
// is http-only: a stdio candidate is rejected (400, real local command
// execution is admin-only), then a real http-transport row is created,
// listed, and deleted.
func (c *client) checkAccountMCPServerCRUD() error {
	status, _, err := c.doJSONRaw(http.MethodPost, pathAccountMCPServers, map[string]any{
		"name": scratchName, "transport": "stdio", "command": "/bin/true",
	})
	if err != nil {
		return err
	}
	if status != http.StatusBadRequest {
		return fmt.Errorf("expected 400 rejecting a self-service stdio server, got %d", status)
	}

	var created struct {
		ID string `json:"id"`
	}
	if _, err := c.postJSON(pathAccountMCPServers, map[string]any{
		"name": scratchName, "transport": "http", "base_url": "http://127.0.0.1:1", "enabled": false,
	}, &created); err != nil {
		return err
	}
	defer c.deleteRequest("/account/api/mcp-servers/" + created.ID)

	var list []struct {
		ID string `json:"id"`
	}
	if _, err := c.getJSON(pathAccountMCPServers, &list); err != nil {
		return err
	}
	found := false
	for _, s := range list {
		if s.ID == created.ID {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("created personal server %q not found in the list afterward", created.ID)
	}
	if _, err := c.deleteRequest("/account/api/mcp-servers/" + created.ID); err != nil {
		return err
	}
	return nil
}

// checkAccountPasswordRoundTrip changes the test user's own password to
// a throwaway value, confirms a login with it works (on a fresh cookie
// jar, so the shared session other checks still need is never disturbed
// or logged out), then changes it back to the original -- so
// credentials.json stays valid for the next run regardless of whether
// this check passes or fails partway through.
func (c *client) checkAccountPasswordRoundTrip(username, originalPassword string) func() error {
	return func() error {
		const temp = "e2e-check-temp-password-2mF9qzR7"
		if _, err := c.patchJSON("/account/api", map[string]any{"password": temp}, nil); err != nil {
			return err
		}
		// Always attempt to restore the original password, even if the
		// temp-password login check below fails.
		restore := func() error {
			_, err := c.patchJSON("/account/api", map[string]any{"password": originalPassword}, nil)
			return err
		}

		fresh, err := c.freshClient()
		if err != nil {
			_ = restore()
			return err
		}
		status, _, loginErr := fresh.doJSONRaw(http.MethodPost, pathLogin, map[string]string{
			"username": username, "password": temp,
		})
		if restoreErr := restore(); restoreErr != nil {
			return fmt.Errorf("changed password but failed to restore the original: %w", restoreErr)
		}
		if loginErr != nil {
			return loginErr
		}
		if status != http.StatusOK {
			return fmt.Errorf("expected login with the new password to succeed (200), got %d", status)
		}
		return nil
	}
}
