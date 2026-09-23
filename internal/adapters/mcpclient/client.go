// Package mcpclient implements ports.MCPToolProvider, connecting to
// admin-configured domain.MCPServer rows via github.com/modelcontextprotocol/go-sdk.
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

// callTimeout bounds a single CallTool round-trip -- the ceiling a server's
// own tool logic can never exceed. 10s measured too tight for
// cmd/mcp-sandbox's run_go (a cold "go run" recompiling stdlib took ~12s on
// se.mo-sys.de); 60s matches httpchat's own chat-completion timeout.
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
		// Defense-in-depth: "stdio" spawns a real local process, an
		// admin-only trust tier (see domain.MCPServer.Command). Refuse it
		// here for a self-service row too, rather than trusting
		// ChatService.Chat's own filter alone to never regress.
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
		// Same belt-and-suspenders SSRF guard as httpchat/httpembed: a
		// pre-flight check here plus routing every dial through
		// ConfiguredEndpointTransport below, which closes the
		// DNS-rebinding TOCTOU gap a pre-flight check alone can't.
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

	// SECURITY: argumentsJSON comes from the model's own output, which can be
	// influenced by untrusted web content (indirect prompt injection). It's
	// parsed as plain JSON and handed to the MCP session as structured
	// arguments, never interpolated into a shell command, path, or URL here;
	// the connected server's own handler is responsible for what it does
	// with each value.
	var args map[string]any
	if argumentsJSON != "" {
		if err := json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
			return "", fmt.Errorf("mcpclient: arguments is not a valid JSON object: %w", err)
		}
	}

	// Every MCP tool call is logged the same way regardless of server/tool,
	// so usage stays visible in one place rather than just the built-ins.
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

// flattenContent joins every TextContent block into one string -- the only
// content type this codebase's tools return; a non-text block is silently
// skipped rather than erroring, since a chat turn can only feed text back.
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
