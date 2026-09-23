// Package mcpclient implements ports.MCPToolProvider by connecting to
// admin-configured domain.MCPServer rows as a real MCP (Model Context
// Protocol) client, using the official
// github.com/modelcontextprotocol/go-sdk.
package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"searchengine/internal/adapters/netguard"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// connectTimeout bounds how long connecting to (and initializing a session
// with, and listing tools from) one configured server may take -- so one
// misbehaving dependency can never hang a whole chat turn.
const connectTimeout = 10 * time.Second

// callTimeout bounds a single CallTool round-trip once connected -- the
// outer ceiling a server's own tool logic (e.g. dockersandbox's
// admin-configured Limits.Timeout, via an MCPServer row's -timeout Args)
// can never exceed, since its context is derived from this one. 10s
// turned out too tight for cmd/mcp-sandbox's run_go: even with its Docker
// image already cached, "go run" recompiling the standard library from
// scratch (no build cache persists across a fresh, ephemeral container)
// measured ~12s on the real se.mo-sys.de deployment -- confirmed live,
// not a hypothetical. 60s matches httpchat's own requestTimeout for a
// chat completion call, so this stays within the same order of "how long
// one step of a chat turn may reasonably take" this codebase already
// accepts elsewhere.
const callTimeout = 60 * time.Second

// implementationName/Version identify this client to every server it
// connects to, per the MCP handshake.
const implementationName = "searchengine"

// Provider implements ports.MCPToolProvider.
type Provider struct{}

// New returns a ready-to-use Provider. Stateless -- every Open call
// connects fresh, no connections are pooled or reused across turns.
func New() *Provider { return &Provider{} }

// serverConn pairs one connected MCP session with the server config that
// produced it, so Session.CallTool can route a call back to the right
// connection.
type serverConn struct {
	serverName string
	session    *mcp.ClientSession
}

// Session implements ports.MCPSession.
type Session struct {
	byTool map[string]*serverConn
	conns  []*serverConn
}

var _ ports.MCPToolProvider = (*Provider)(nil)
var _ ports.MCPSession = (*Session)(nil)

// Open implements ports.MCPToolProvider. Best-effort per server: one that
// fails to connect or list its tools is skipped (logged), never fails the
// whole turn. A tool name collision across two different active servers is
// resolved by skipping (logging) the later one, never silently misrouting.
func (p *Provider) Open(ctx context.Context, servers []domain.MCPServer, env map[string]string) (ports.MCPSession, []domain.MCPTool) {
	session := &Session{byTool: map[string]*serverConn{}}
	var tools []domain.MCPTool
	for _, s := range servers {
		if !s.Enabled {
			continue
		}
		conn, err := connect(ctx, s, env)
		if err != nil {
			log.Printf("mcpclient: connecting to server %q: %v", s.Name, err)
			continue
		}

		listCtx, cancel := context.WithTimeout(ctx, connectTimeout)
		result, err := conn.session.ListTools(listCtx, nil)
		cancel()
		if err != nil {
			log.Printf("mcpclient: listing tools for server %q: %v", s.Name, err)
			_ = conn.session.Close()
			continue
		}

		session.conns = append(session.conns, conn)
		for _, t := range result.Tools {
			if _, exists := session.byTool[t.Name]; exists {
				log.Printf("mcpclient: tool %q from server %q collides with an already-registered tool from another active server, skipping", t.Name, s.Name)
				continue
			}
			session.byTool[t.Name] = conn
			schema, err := json.Marshal(t.InputSchema)
			if err != nil || len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			tools = append(tools, domain.MCPTool{
				ServerID: s.ID, ServerName: s.Name,
				Name: t.Name, Description: t.Description, InputSchema: schema,
			})
		}
	}
	return session, tools
}

// connect opens one MCP client session against s, per its configured
// Transport. env is set on the spawned process's environment for a
// "stdio" server, unconditionally -- see ports.MCPToolProvider's own doc
// comment on why this never reopens an injection surface.
func connect(ctx context.Context, s domain.MCPServer, env map[string]string) (*serverConn, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: implementationName, Version: "1"}, nil)

	var transport mcp.Transport
	switch s.Transport {
	case "stdio":
		// Defense-in-depth, independent of ChatService.Chat's own filter
		// (which already never lets a SelfService row reach here with
		// "stdio" transport -- see its own doc comment): this package
		// refuses to spawn a real local process for a personal server on
		// its own terms too, rather than trusting a caller's filter to
		// never regress. "stdio" execution is an admin-only trust tier
		// (see domain.MCPServer.Command's own doc comment) -- a genuine,
		// deliberate elevation for a row an admin configured, but one a
		// regular user's own self-service row must never reach, however
		// it got here.
		if s.SelfService {
			return nil, fmt.Errorf("mcpclient: refusing \"stdio\" transport for a self-service (non-admin) server (id=%q)", s.ID)
		}
		cmd := exec.CommandContext(ctx, s.Command, s.Args...)
		cmd.Env = os.Environ()
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		transport = &mcp.CommandTransport{Command: cmd}
	case "http":
		// Same belt-and-suspenders guard as httpchat/httpembed's own
		// admin-configured BaseURL (see netguard.ConfiguredEndpointURLAllowed's
		// doc comment): a pre-flight check here, plus routing every dial
		// (including a redirect hop) through ConfiguredEndpointTransport below,
		// which is what actually closes the DNS-rebinding TOCTOU gap a
		// pre-flight check alone can't. This server config is admin-trusted
		// today, but per-user MCP servers (self-service, http-only) will reuse
		// this same connect path, so the guard belongs here rather than at
		// each caller.
		if !netguard.ConfiguredEndpointURLAllowed(s.BaseURL) {
			return nil, fmt.Errorf("mcpclient: endpoint URL is not allowed: %s", s.BaseURL)
		}
		httpClient := &http.Client{Timeout: connectTimeout, Transport: netguard.ConfiguredEndpointTransport()}
		if s.APIKey != "" {
			httpClient.Transport = &bearerTransport{apiKey: s.APIKey, base: netguard.ConfiguredEndpointTransport()}
		}
		transport = &mcp.StreamableClientTransport{Endpoint: s.BaseURL, HTTPClient: httpClient}
	default:
		return nil, fmt.Errorf("unknown transport %q", s.Transport)
	}

	connectCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	sess, err := client.Connect(connectCtx, transport, nil)
	if err != nil {
		return nil, err
	}
	return &serverConn{serverName: s.Name, session: sess}, nil
}

// bearerTransport injects an "Authorization: Bearer <apiKey>" header into
// every request -- used for an "http"-transport MCPServer with a
// configured APIKey.
type bearerTransport struct {
	apiKey string
	base   http.RoundTripper
}

func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+t.apiKey)
	return t.base.RoundTrip(req)
}

// CallTool implements ports.MCPSession.
func (s *Session) CallTool(ctx context.Context, toolName, argumentsJSON string) (string, error) {
	conn, ok := s.byTool[toolName]
	if !ok {
		return "", fmt.Errorf("mcpclient: no active tool named %q", toolName)
	}

	// SECURITY: argumentsJSON comes from the model's own OUTPUT, which can
	// itself be influenced by untrusted web content when search context is
	// enabled (indirect prompt injection). It is parsed as a plain JSON
	// object and handed to the MCP session as structured arguments -- never
	// interpolated into a shell command, a path, or a URL by this package;
	// whatever the connected server's own tool handler does with each
	// argument value is that server's responsibility, same trust boundary
	// as any other admin-configured tool provider.
	var args map[string]any
	if argumentsJSON != "" {
		if err := json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
			return "", fmt.Errorf("mcpclient: arguments is not a valid JSON object: %w", err)
		}
	}

	// Every MCP tool call is logged the same way regardless of which
	// server or tool it names -- there's nothing web_search/web_fetch-
	// specific to single out now that they're MCP tools like any other
	// (mcp-web, mcp-sandbox, mcp-files, or a third-party server an admin
	// configures); one log line per call keeps every tool's usage visible
	// in the same place, not just the built-in ones.
	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	result, err := conn.session.CallTool(callCtx, &mcp.CallToolParams{Name: toolName, Arguments: args})
	if err != nil {
		log.Printf("mcpclient: tool %q (server %q) call failed: %v", toolName, conn.serverName, err)
		return "", err
	}
	text := flattenContent(result.Content)
	if result.IsError {
		log.Printf("mcpclient: tool %q (server %q) returned an error result: %s", toolName, conn.serverName, text)
		return "", fmt.Errorf("tool error: %s", text)
	}
	log.Printf("mcpclient: tool %q (server %q) call succeeded", toolName, conn.serverName)
	return text, nil
}

// flattenContent joins every TextContent block in content into one string
// -- the only content type the tools this codebase configures (mcp-web,
// and any future first-party/third-party MCP server) are expected to
// return; a non-text block (image/audio/embedded resource) is silently
// skipped rather than erroring the whole call, since a chat turn can only
// feed text back to the model anyway.
func flattenContent(content []mcp.Content) string {
	var b strings.Builder
	for _, c := range content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// Close implements ports.MCPSession.
func (s *Session) Close() {
	for _, c := range s.conns {
		_ = c.session.Close()
	}
}
