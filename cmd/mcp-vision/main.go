// Command mcp-vision is a first-party MCP server exposing "vision_similarity"
// and "vision_caption", letting the chat model make use of an attached
// image the way search already uses its own embedding model -- but
// configured entirely separately and independently (see
// domain.ChatVisionSettings). Spawned as a stdio subprocess by
// internal/adapters/mcpclient, same model as
// cmd/mcp-web/cmd/mcp-datetime/cmd/mcp-files/cmd/mcp-sandbox -- not a
// systemd service.
//
// Like mcp-files, this server holds no direct DB connection or
// admin-level credential: reading the attached image is plain HTTP back
// to search-server's /account/api/files, authenticated with the same
// short-lived per-turn bearer token mcp-files uses (SE_FILES_API_TOKEN).
// vision_similarity calls back into search-server's own
// /search/api/vision-similarity (internal, X-Internal-API-Key-gated) --
// it never talks to the embeddings endpoint or the database directly, so
// a compromised process here can't reach either. vision_caption, by
// contrast, calls the configured captioning endpoint directly (a
// different capability, not indexed data), the same way cmd/mcp-web calls
// out to SearXNG directly rather than through search-server.
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
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"searchengine/internal/adapters/netguard"
	"searchengine/internal/bootstrap"
)

// callTimeout bounds a single HTTP round-trip back to search-server's own
// /account/api/files or /search/api/vision-similarity -- both loopback
// calls, so this stays well under mcpclient's own 60s callTimeout.
const callTimeout = 20 * time.Second

// captionCallTimeout bounds a call to the (possibly remote, possibly
// slower-to-respond) configured vision-language captioning endpoint --
// generous, matching httpchat's own real-model-latency budget rather than
// callTimeout's loopback-call budget.
const captionCallTimeout = 45 * time.Second

// imageURLFetchTimeout bounds fetching an external image URL a user pasted
// into chat -- a real network hop to an arbitrary third-party server,
// unlike callTimeout's loopback-only budget.
const imageURLFetchTimeout = 15 * time.Second

// maxImageBytes caps how large an attached image this server will fetch
// and forward -- every byte becomes a base64 char (1/3 inflation) plus
// real network/inference cost on the receiving end; a much larger image
// should be resized/cropped by the user first.
const maxImageBytes = 8 << 20

// headerContentType names the header read (a fetched image's own MIME
// type) and set (every JSON POST below) more than once in this file.
const headerContentType = "Content-Type"

type visionSimilarityArgs struct {
	FileID   string `json:"file_id,omitempty" jsonschema:"the id of an attached image file to use, from list_files -- mutually exclusive with image_url"`
	ImageURL string `json:"image_url,omitempty" jsonschema:"a public http(s) URL of an image to use instead of an attached file, e.g. one the user pasted in chat -- mutually exclusive with file_id"`
	Limit    int    `json:"limit,omitempty" jsonschema:"how many matches to return -- optional, defaults to 5"`
}

type visionCaptionArgs struct {
	FileID   string `json:"file_id,omitempty" jsonschema:"the id of an attached image file to use, from list_files -- mutually exclusive with image_url"`
	ImageURL string `json:"image_url,omitempty" jsonschema:"a public http(s) URL of an image to use instead of an attached file, e.g. one the user pasted in chat -- mutually exclusive with file_id"`
	Question string `json:"question,omitempty" jsonschema:"what to ask about the image -- optional, defaults to a general description"`
}

func main() {
	baseURL := flag.String("base-url", "http://127.0.0.1:8080", "search-server's own base URL, for reading attached files and calling the internal vision-similarity endpoint")
	flag.Parse()

	// SE_FILES_API_TOKEN is set by mcpclient.Provider on every spawned
	// stdio server for a signed-in role=user turn -- same as mcp-files.
	filesToken := bootstrap.GetEnv("SE_FILES_API_TOKEN", "")
	files := &filesClient{baseURL: strings.TrimRight(*baseURL, "/"), token: filesToken, http: &http.Client{Timeout: callTimeout}}

	// urlFetcher reaches arbitrary third-party servers a user's pasted URL
	// names -- attacker-influenced input, unlike files' own trusted,
	// already-uploaded account files -- so it goes through netguard's
	// strict, crawler-grade AllowedIP policy (netguard.Transport()), never
	// the permissive ConfiguredEndpoint* policy used for admin-configured
	// backends.
	urlFetcher := newURLImageFetcher(&http.Client{Timeout: imageURLFetchTimeout, Transport: netguard.Transport()})

	// The VISION_* env vars below are set by application.ChatService's
	// addVisionEnv, sourced from domain.ChatVisionSettings -- see its own
	// doc comment. Each tool independently reports itself unconfigured
	// (via its own Enabled flag) rather than failing the whole process,
	// since an admin may enable only one of the two capabilities.
	similarity := &similarityTool{
		files:      files,
		urlFetcher: urlFetcher,
		enabled:    bootstrap.GetEnv("VISION_SIMILARITY_ENABLED", "") == "true",
		provider:   bootstrap.GetEnv("VISION_SIMILARITY_PROVIDER_ID", ""),
		baseURL:    strings.TrimRight(*baseURL, "/"),
		apiKey:     bootstrap.GetEnv("CHAT_VISION_INTERNAL_API_KEY", ""),
		http:       &http.Client{Timeout: callTimeout},
	}
	caption := &captionTool{
		files:      files,
		urlFetcher: urlFetcher,
		enabled:    bootstrap.GetEnv("VISION_CAPTION_ENABLED", "") == "true",
		baseURL:    strings.TrimRight(bootstrap.GetEnv("VISION_CAPTION_BASE_URL", ""), "/"),
		apiKey:     bootstrap.GetEnv("VISION_CAPTION_API_KEY", ""),
		model:      bootstrap.GetEnv("VISION_CAPTION_MODEL", ""),
		http:       &http.Client{Timeout: captionCallTimeout},
	}

	server := newServer(similarity, caption)
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}

// newServer builds the mcp.Server exposing "vision_similarity"/
// "vision_caption", factored out of main so a test can connect via an
// in-memory transport instead of a real stdio subprocess.
func newServer(similarity *similarityTool, caption *captionTool) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "mcp-vision", Version: "1"}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name: "vision_similarity",
		Description: "Find documents in this instance's own search index that are visually/semantically " +
			"similar to an image, by embedding the image against the same model search uses and " +
			"vector-searching the index with it -- \"reverse image search into your own index.\" Use this " +
			"when the user attaches an image (pass file_id) or pastes an image URL in chat (pass image_url) " +
			"and asks what it's related to, or wants to find pages about the same subject. Returns each " +
			"match's URL, title, and similarity score. Reports itself unavailable if the admin hasn't enabled " +
			"this on the Chat settings page.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args visionSimilarityArgs) (*mcp.CallToolResult, any, error) {
		return toolResult(similarity.run(ctx, args))
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "vision_caption",
		Description: "Describe what's in an image (or answer a specific question about it) using a " +
			"configured vision-language model. Use this when the user attaches an image (pass file_id) or " +
			"pastes an image URL in chat (pass image_url) and asks what it shows, or asks a question about " +
			"its content -- you cannot see the image yourself, so this is the only way to actually know what " +
			"it contains. Reports itself unavailable if the admin hasn't configured a captioning endpoint on " +
			"the Chat settings page.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args visionCaptionArgs) (*mcp.CallToolResult, any, error) {
		return toolResult(caption.run(ctx, args))
	})

	return server
}

func toolResult(text string, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil, nil
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
}

// errNoToken mirrors cmd/mcp-files' identical message.
var errNoToken = fmt.Errorf("no signed-in user for this chat turn -- vision tools are only available when a regular user account is signed in")

// filesClient reads an attached image's bytes back from search-server's
// /account/api/files/{id} -- the same call mcp-files' read_file_base64
// makes, duplicated here rather than shared across binaries (each
// first-party MCP server is a fully independent, separately-built
// command, per this codebase's convention).
type filesClient struct {
	baseURL string
	token   string
	http    *http.Client
}

type fetchedImage struct {
	data        []byte
	contentType string
	filename    string
}

func (c *filesClient) fetch(ctx context.Context, fileID string) (fetchedImage, error) {
	if c.token == "" {
		return fetchedImage{}, errNoToken
	}
	if fileID == "" {
		return fetchedImage{}, fmt.Errorf("file_id must not be empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/account/api/files/"+fileID, nil)
	if err != nil {
		return fetchedImage{}, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return fetchedImage{}, fmt.Errorf("calling search-server: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		return fetchedImage{}, fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fetchedImage{}, fmt.Errorf("reading file: server returned %d: %s", resp.StatusCode, body)
	}
	if len(body) > maxImageBytes {
		return fetchedImage{}, fmt.Errorf("image is larger than %d bytes -- resize/crop it first", maxImageBytes)
	}
	filename := fileID
	if _, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); err == nil {
		if fn, ok := params["filename"]; ok {
			filename = fn
		}
	}
	return fetchedImage{data: body, contentType: resp.Header.Get(headerContentType), filename: filename}, nil
}

// urlImageFetcher fetches an arbitrary external image URL a user pasted
// into chat -- unlike filesClient's already-uploaded, trusted account
// files, this is attacker-influenced input, so every dial (including
// redirect hops) goes through netguard's strict, crawler-grade AllowedIP
// policy rather than the permissive ConfiguredEndpoint* policy admin-
// configured backends get.
type urlImageFetcher struct {
	http *http.Client
	// urlAllowed defaults to netguard.URLAllowed (set by newURLImageFetcher)
	// -- overridable so a test can point at a loopback httptest server
	// without netguard's real, always-blocks-loopback policy getting in the
	// way, the same seam netguard itself uses (lookupIP) for the same
	// reason.
	urlAllowed func(string) bool
}

func newURLImageFetcher(client *http.Client) *urlImageFetcher {
	return &urlImageFetcher{http: client, urlAllowed: netguard.URLAllowed}
}

func (f *urlImageFetcher) fetch(ctx context.Context, rawURL string) (fetchedImage, error) {
	if rawURL == "" {
		return fetchedImage{}, fmt.Errorf("image_url must not be empty")
	}
	if !f.urlAllowed(rawURL) {
		return fetchedImage{}, fmt.Errorf("image_url is not allowed -- it must be a public http(s) URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fetchedImage{}, fmt.Errorf("building request: %w", err)
	}
	resp, err := f.http.Do(req)
	if err != nil {
		return fetchedImage{}, fmt.Errorf("fetching image_url: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		return fetchedImage{}, fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fetchedImage{}, fmt.Errorf("fetching image_url: server returned %d", resp.StatusCode)
	}
	if len(body) > maxImageBytes {
		return fetchedImage{}, fmt.Errorf("image is larger than %d bytes -- resize/crop it first", maxImageBytes)
	}
	contentType := resp.Header.Get(headerContentType)
	if !looksLikeImage(contentType) {
		return fetchedImage{}, fmt.Errorf("image_url did not return an image (content-type %q)", contentType)
	}
	filename := rawURL
	if idx := strings.LastIndex(rawURL, "/"); idx != -1 && idx+1 < len(rawURL) {
		filename = rawURL[idx+1:]
	}
	return fetchedImage{data: body, contentType: contentType, filename: filename}, nil
}

// looksLikeImage accepts a real image/* content-type, and also an empty or
// generic octet-stream one -- some servers (in particular CDNs serving a
// bare image path) omit or genericize it, and the receiving
// embedding/captioning endpoint is the real validator of whether the bytes
// actually decode as an image.
func looksLikeImage(contentType string) bool {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if ct == "" || strings.Contains(ct, "octet-stream") {
		return true
	}
	return strings.HasPrefix(ct, "image/")
}

// imageSource resolves the image either tool operates on: args.FileID (an
// already-uploaded account file, via files) or args.ImageURL (an arbitrary
// external URL a user pasted in chat, via urlFetcher) -- exactly one of
// the two must be given. Shared by similarityTool.run/captionTool.run so
// this "which source, and the exactly-one-of rule" lives in one place.
func imageSource(ctx context.Context, files *filesClient, urlFetcher *urlImageFetcher, fileID, imageURL string) (fetchedImage, error) {
	switch {
	case fileID != "" && imageURL != "":
		return fetchedImage{}, fmt.Errorf("file_id and image_url are mutually exclusive -- pass only one")
	case imageURL != "":
		return urlFetcher.fetch(ctx, imageURL)
	case fileID != "":
		return files.fetch(ctx, fileID)
	default:
		return fetchedImage{}, fmt.Errorf("either file_id or image_url must be given")
	}
}

// similarityTool implements vision_similarity: fetch the image, then call
// search-server's own internal vision-similarity endpoint (which embeds
// it and runs the ANN search) -- never touches an embedding endpoint or
// the database directly.
type similarityTool struct {
	files      *filesClient
	urlFetcher *urlImageFetcher
	enabled    bool
	provider   string
	baseURL    string
	apiKey     string
	http       *http.Client
}

type visionSimilarityRequest struct {
	Provider string `json:"provider"`
	Base64   string `json:"base64"`
	MimeType string `json:"mime_type"`
	Limit    int    `json:"limit,omitempty"`
}

func (t *similarityTool) run(ctx context.Context, args visionSimilarityArgs) (string, error) {
	if !t.enabled {
		return "", fmt.Errorf("vision similarity is not configured -- an admin needs to enable it on the Chat settings page")
	}
	img, err := imageSource(ctx, t.files, t.urlFetcher, args.FileID, args.ImageURL)
	if err != nil {
		return "", err
	}
	reqBody, err := json.Marshal(visionSimilarityRequest{
		Provider: t.provider, Base64: base64.StdEncoding.EncodeToString(img.data), MimeType: img.contentType, Limit: args.Limit,
	})
	if err != nil {
		return "", fmt.Errorf("encoding request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/search/api/vision-similarity", bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("building request: %w", err)
	}
	req.Header.Set(headerContentType, "application/json")
	req.Header.Set("X-Internal-API-Key", t.apiKey)
	resp, err := t.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("calling search-server: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("vision similarity search failed: server returned %d: %s", resp.StatusCode, body)
	}
	return string(body), nil
}

// captionTool implements vision_caption: fetch the image, then call the
// configured vision-language endpoint's own chat-completions API
// directly (an OpenAI-compatible request with image_url content), the
// same image_url content-block shape httpembed.Embedder.EmbedImage sends
// for the (different) embedding case.
type captionTool struct {
	files      *filesClient
	urlFetcher *urlImageFetcher
	enabled    bool
	baseURL    string
	apiKey     string
	model      string
	http       *http.Client
}

type captionMessage struct {
	Role    string           `json:"role"`
	Content []captionContent `json:"content"`
}

type captionContent struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *captionImage `json:"image_url,omitempty"`
}

type captionImage struct {
	URL string `json:"url"`
}

type captionRequest struct {
	Model    string           `json:"model,omitempty"`
	Messages []captionMessage `json:"messages"`
}

type captionResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

const defaultCaptionQuestion = "Describe what this image shows in a few sentences."

func (t *captionTool) run(ctx context.Context, args visionCaptionArgs) (string, error) {
	if !t.enabled {
		return "", fmt.Errorf("vision captioning is not configured -- an admin needs to set up a captioning endpoint on the Chat settings page")
	}
	img, err := imageSource(ctx, t.files, t.urlFetcher, args.FileID, args.ImageURL)
	if err != nil {
		return "", err
	}
	question := args.Question
	if question == "" {
		question = defaultCaptionQuestion
	}
	mimeType := img.contentType
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	reqBody, err := json.Marshal(captionRequest{
		Model: t.model,
		Messages: []captionMessage{{
			Role: "user",
			Content: []captionContent{
				{Type: "text", Text: question},
				{Type: "image_url", ImageURL: &captionImage{URL: "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(img.data)}},
			},
		}},
	})
	if err != nil {
		return "", fmt.Errorf("encoding request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("building request: %w", err)
	}
	req.Header.Set(headerContentType, "application/json")
	if t.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+t.apiKey)
	}
	resp, err := t.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("calling captioning endpoint: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("captioning endpoint returned status %d: %s", resp.StatusCode, body)
	}
	var parsed captionResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("decoding response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("captioning endpoint returned no choices")
	}
	return parsed.Choices[0].Message.Content, nil
}
