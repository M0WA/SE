// Command mcp-files is a first-party MCP server exposing "list_files"/
// "read_file"/"read_file_base64"/"write_file", letting the chat model
// inspect a signed-in user's uploaded files and produce new ones. Spawned
// as a stdio subprocess by internal/adapters/mcpclient, same model as
// cmd/mcp-web/cmd/mcp-datetime/cmd/mcp-sandbox -- not a systemd service.
//
// Unlike those three, this server holds no direct DB connection or
// admin-level credential: every call is plain HTTP back to search-server's
// /account/api/files, authenticated with a short-lived bearer token scoped
// to one user (SE_FILES_API_TOKEN, minted per turn -- see
// application.ChatOptions.FileAccessToken). This keeps a compromised
// process's blast radius to that one user's own files.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"searchengine/internal/bootstrap"
)

// callTimeout bounds a single HTTP round-trip back to search-server's own
// /account/api/files -- a loopback call, so this stays well under
// mcpclient's own 60s callTimeout.
const callTimeout = 20 * time.Second

// maxReadableBytes caps how much of a file's content read_file returns as
// text -- a runaway file must never blow up the tool result handed back
// to the chat completion call.
const maxReadableBytes = 256 * 1024

// maxBase64ReadableBytes caps a binary file's raw size for
// read_file_base64 -- far smaller than maxReadableBytes, since this
// content is embedded in a run_python/run_go call's code argument, where
// base64 inflates it by a third and every byte becomes real tokens. 300KB
// raw covers a compressed screenshot without blowing a context budget.
const maxBase64ReadableBytes = 300 * 1024

type readFileArgs struct {
	FileID string `json:"file_id" jsonschema:"the id of the file to read, from list_files"`
}

// readFileBase64Result is read_file_base64's wire shape -- a small,
// self-describing JSON object so the model can read content_type/size.
type readFileBase64Result struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int    `json:"size"`
	Base64      string `json:"base64"`
}

type writeFileArgs struct {
	Filename string `json:"filename" jsonschema:"the filename to save the new file as"`
	Content  string `json:"content" jsonschema:"the file's full text content"`
}

func main() {
	baseURL := flag.String("base-url", "http://127.0.0.1:8080", "search-server's own base URL, for calling back into /account/api/files")
	flag.Parse()

	// SE_FILES_API_TOKEN is set by mcpclient.Provider on every spawned
	// stdio server for a signed-in role=user turn. Empty means no user is
	// signed in (e.g. role=admin), so every tool call below fails with a
	// clear message rather than silently doing nothing.
	token := bootstrap.GetEnv("SE_FILES_API_TOKEN", "")

	client := &client{baseURL: strings.TrimRight(*baseURL, "/"), token: token, http: &http.Client{Timeout: callTimeout}}
	server := newServer(client)

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}

// newServer builds the mcp.Server exposing "list_files"/"read_file"/
// "write_file", factored out of main so a test can connect via an
// in-memory transport instead of a real stdio subprocess.
func newServer(c *client) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "mcp-files", Version: "1"}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name: "list_files",
		Description: "List the files the current user has uploaded (or a previous write_file call created) " +
			"and made available for inspection -- returns each file's id, filename, content type, and size. " +
			"Use this to discover what's available before calling read_file.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct{}) (*mcp.CallToolResult, any, error) {
		return toolResult(c.listFiles(ctx))
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "read_file",
		Description: "Read one of the current user's files (by id, from list_files) as text. Only works for " +
			"text-like content (source code, CSV, JSON, logs, plain text, etc.) -- a binary file (an image, a " +
			"PDF, a compiled binary) is reported as unreadable rather than returned as raw bytes.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args readFileArgs) (*mcp.CallToolResult, any, error) {
		return toolResult(c.readFile(ctx, args.FileID))
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "read_file_base64",
		Description: "Read one of the current user's files (by id, from list_files) as base64-encoded raw " +
			"bytes -- the way to actually get at a BINARY file's content (an image, a PDF), unlike read_file " +
			"which refuses one. Returns the filename, content type, size, and the base64 data itself. Meant " +
			"to be decoded inside a run_python/run_go sandbox call (e.g. base64.b64decode(...) in Python) for " +
			"further processing -- there is no other way for sandboxed code to see a file's bytes, since the " +
			"sandbox has no access to this account's files on its own. Limited to smaller files (see the " +
			"error if one is too large) since every byte becomes real tokens once base64-encoded into this " +
			"result.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args readFileArgs) (*mcp.CallToolResult, any, error) {
		return toolResult(c.readFileBase64(ctx, args.FileID))
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "write_file",
		Description: "Create a new file with the given text content, owned by the current user, who can then " +
			"download it from their Your files page. Use this to hand the user a produced artifact (a report, " +
			"generated code, reformatted data) rather than only pasting it into the chat reply. Returns the " +
			"new file's id, filename, and size.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args writeFileArgs) (*mcp.CallToolResult, any, error) {
		return toolResult(c.writeFile(ctx, args.Filename, args.Content))
	})

	return server
}

// toolResult wraps a (text, error) pair into an MCP tool result --
// IsError only for a genuine call failure, never ordinary content.
func toolResult(text string, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil, nil
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
}

// client is a thin HTTP client for search-server's /account/api/files
// endpoints, authenticated with a per-turn bearer token (see package doc).
type client struct {
	baseURL string
	token   string
	http    *http.Client
}

// errNoToken is returned by every client method when no token was
// configured (SE_FILES_API_TOKEN unset) -- a clear, expected message
// rather than a confusing 401 from the HTTP call.
var errNoToken = fmt.Errorf("no signed-in user for this chat turn -- file tools are only available when a regular user account is signed in")

func (c *client) do(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	if c.token == "" {
		return nil, errNoToken
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling search-server: %w", err)
	}
	return resp, nil
}

func (c *client) listFiles(ctx context.Context) (string, error) {
	resp, err := c.do(ctx, http.MethodGet, "/account/api/files", nil, "")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxReadableBytes))
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("listing files: server returned %d: %s", resp.StatusCode, body)
	}
	return string(body), nil
}

func (c *client) readFile(ctx context.Context, fileID string) (string, error) {
	if fileID == "" {
		return "", fmt.Errorf("file_id must not be empty")
	}
	resp, err := c.do(ctx, http.MethodGet, "/account/api/files/"+fileID, nil, "")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxReadableBytes+1))
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("reading file: server returned %d: %s", resp.StatusCode, body)
	}
	if len(body) > maxReadableBytes {
		body = body[:maxReadableBytes]
	}
	if !utf8.Valid(body) {
		return "", fmt.Errorf("this file is not text (binary content) -- read_file can only return text-like files")
	}
	return string(body), nil
}

func (c *client) readFileBase64(ctx context.Context, fileID string) (string, error) {
	if fileID == "" {
		return "", fmt.Errorf("file_id must not be empty")
	}
	resp, err := c.do(ctx, http.MethodGet, "/account/api/files/"+fileID, nil, "")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBase64ReadableBytes+1))
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("reading file: server returned %d: %s", resp.StatusCode, body)
	}
	if len(body) > maxBase64ReadableBytes {
		return "", fmt.Errorf("file is larger than %d bytes -- too large to return as base64 (every byte becomes real tokens once encoded); upload a smaller/resized copy instead", maxBase64ReadableBytes)
	}
	filename := fileID
	if _, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); err == nil {
		if fn, ok := params["filename"]; ok {
			filename = fn
		}
	}
	out, err := json.Marshal(readFileBase64Result{
		Filename:    filename,
		ContentType: resp.Header.Get("Content-Type"),
		Size:        len(body),
		Base64:      base64.StdEncoding.EncodeToString(body),
	})
	if err != nil {
		// json.Marshal on this plain struct cannot actually fail -- handled
		// anyway rather than silently swallowed.
		return "", fmt.Errorf("encoding result: %w", err)
	}
	return string(out), nil
}

func (c *client) writeFile(ctx context.Context, filename, content string) (string, error) {
	if filename == "" {
		return "", fmt.Errorf("filename must not be empty")
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return "", fmt.Errorf("building upload: %w", err)
	}
	if _, err := part.Write([]byte(content)); err != nil {
		return "", fmt.Errorf("building upload: %w", err)
	}
	if err := mw.Close(); err != nil {
		return "", fmt.Errorf("building upload: %w", err)
	}
	resp, err := c.do(ctx, http.MethodPost, "/account/api/files", &buf, mw.FormDataContentType())
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxReadableBytes))
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("writing file: server returned %d: %s", resp.StatusCode, respBody)
	}
	return string(respBody), nil
}
