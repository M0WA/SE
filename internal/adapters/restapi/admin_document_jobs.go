package restapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"searchengine/internal/adapters/netguard"
	"searchengine/internal/application"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

const configNameDocumentJobs = "document jobs"

// classifyDocumentContent decides whether data should be indexed as text or
// as an image -- the only two content kinds this feature supports (see
// package doc comment on admin_document_jobs.go's design rationale: no
// per-format extraction code, unlike the chat sandbox's file_ids
// mechanism). image/* by content type is trusted as-is; anything else must
// be valid UTF-8 to be treated as text. Neither true means the upload is
// rejected outright.
func classifyDocumentContent(contentType string, data []byte) (isText, isImage bool) {
	if strings.HasPrefix(contentType, "image/") {
		return false, true
	}
	return utf8.Valid(data), false
}

// documentJobResponse is domain.DocumentJob's JSON shape -- identical to
// the domain type today, but kept as its own type (not domain.DocumentJob
// directly) so a future field never leaks without a deliberate choice, same
// convention as fileResponse/toFileResponse.
type documentJobResponse struct {
	ID              string  `json:"id"`
	Filename        string  `json:"filename"`
	ContentType     string  `json:"content_type"`
	Size            int64   `json:"size"`
	Source          string  `json:"source"`
	IndexVocabulary bool    `json:"index_vocabulary"`
	Status          string  `json:"status"`
	DocID           string  `json:"doc_id,omitempty"`
	Error           string  `json:"error,omitempty"`
	CreatedAt       string  `json:"created_at"`
	StartedAt       *string `json:"started_at,omitempty"`
	FinishedAt      *string `json:"finished_at,omitempty"`
}

func toDocumentJobResponse(j domain.DocumentJob) documentJobResponse {
	resp := documentJobResponse{
		ID: j.ID, Filename: j.Filename, ContentType: j.ContentType, Size: j.Size,
		Source: string(j.Source), IndexVocabulary: j.IndexVocabulary, Status: string(j.Status),
		DocID: j.DocID, Error: j.Error, CreatedAt: j.CreatedAt.Format(time.RFC3339),
	}
	if j.StartedAt != nil {
		s := j.StartedAt.Format(time.RFC3339)
		resp.StartedAt = &s
	}
	if j.FinishedAt != nil {
		s := j.FinishedAt.Format(time.RFC3339)
		resp.FinishedAt = &s
	}
	return resp
}

// handleAdminDocumentJobs serves the Document-upload jobs collection: GET
// lists every job (most recent first), POST creates one from a multipart
// file upload and kicks off its processing in the background.
func (h *Handler) handleAdminDocumentJobs(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.documentJobs != nil, configNameDocumentJobs) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		jobs, err := h.documentJobs.ListDocumentJobs(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := make([]documentJobResponse, len(jobs))
		for i, j := range jobs {
			out[i] = toDocumentJobResponse(j)
		}
		writeJSON(w, http.StatusOK, out)
	case http.MethodPost:
		h.handleUploadDocumentJob(w, r)
	default:
		http.Error(w, msgMethodNotAllowed, http.StatusMethodNotAllowed)
	}
}

// handleUploadDocumentJob is bounded by the admin-configured
// OperationalSettingsValues.MaxDocumentUploadBytes -- independent of
// content type (text vs image), same "one flat ceiling, never a
// per-format special case" convention as the account file upload's own
// MaxFileUploadBytes.
//
// Branches by content type BEFORE reading anything -- an image/* upload
// (declared via its multipart part's own Content-Type header, no need to
// inspect any bytes to classify it, unlike text -- see
// classifyDocumentContent) is streamed straight into the configured
// vision-similarity embedding provider's own HTTP request body and never
// buffered into a []byte or persisted anywhere; see
// streamAndProcessImageDocumentJob's own doc comment for why that
// necessarily makes the request synchronous, unlike every other job kind
// here. Anything else is read fully (as before) to validate it's real
// UTF-8 text, then processed the existing fire-and-forget way.
func (h *Handler) handleUploadDocumentJob(w http.ResponseWriter, r *http.Request) {
	maxUploadBytes := int64(h.opSettings.Get().MaxDocumentUploadBytes)
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes+1)
	if err := r.ParseMultipartForm(maxUploadBytes + 1); err != nil {
		http.Error(w, fmt.Sprintf("invalid or oversized upload (max %d bytes)", maxUploadBytes), http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, `missing "file" form field`, http.StatusBadRequest)
		return
	}
	defer file.Close()
	contentType := header.Header.Get("Content-Type")
	indexVocabulary := r.FormValue("index_vocabulary") == "true"

	if strings.HasPrefix(contentType, "image/") {
		h.streamAndProcessImageDocumentJob(w, r, header.Filename, contentType, file, indexVocabulary)
		return
	}

	data, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "reading upload", http.StatusInternalServerError)
		return
	}
	h.createAndProcessDocumentJob(w, r, header.Filename, contentType, data, domain.DocumentJobSourceUpload, indexVocabulary)
}

// createAndProcessDocumentJob validates data's content kind, persists a new
// queued job, responds with it, then processes it in a detached background
// goroutine (see processDocumentJob's own doc comment for why a fresh
// context.Background() is used instead of r.Context()). Only ever called
// with data already fully in hand (a buffered text upload, or any S3
// import regardless of content kind -- s3GetObject already necessarily
// buffers the whole object) -- a direct-upload image never reaches this,
// see streamAndProcessImageDocumentJob instead.
func (h *Handler) createAndProcessDocumentJob(w http.ResponseWriter, r *http.Request, filename, contentType string, data []byte, source domain.DocumentJobSource, indexVocabulary bool) {
	if isText, isImage := classifyDocumentContent(contentType, data); !isText && !isImage {
		http.Error(w, "unsupported file type -- Document upload only supports plain text and image files; "+
			"for PDFs, Word/Excel documents, or other formats, attach the file to a chat turn instead "+
			"(the model can process it via the sandbox)", http.StatusBadRequest)
		return
	}
	job, err := h.documentJobs.CreateDocumentJob(r.Context(), filename, contentType, int64(len(data)), source, indexVocabulary)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, toDocumentJobResponse(job))
	go h.processDocumentJob(job.ID, contentType, data, indexVocabulary)
}

// processDocumentJob indexes one Document job's content as a
// domain.Document with no PageRank (Links always nil -- an upload has no
// link graph) and, for text, an optional vocabulary/BM25-postings toggle.
// Runs detached from the triggering request via context.Background(),
// same fire-and-forget convention as every other background job in this
// codebase (crawl jobs, bulk delete) -- it must survive the request that
// started it. Only reached with data already fully buffered (see
// createAndProcessDocumentJob's own doc comment) -- an isImage job here is
// always an S3 import, never a direct upload.
func (h *Handler) processDocumentJob(jobID, contentType string, data []byte, indexVocabulary bool) {
	ctx := context.Background()
	if err := h.documentJobs.MarkDocumentJobRunning(ctx, jobID); err != nil {
		return
	}
	docID := "doc-" + jobID
	isText, isImage := classifyDocumentContent(contentType, data)
	doc := domain.Document{ID: docID, URL: "upload://" + jobID, Title: jobID, CrawledAt: time.Now().UTC()}
	embeddings := make(map[string][]float32)
	v := h.opSettings.Get()

	switch {
	case isText:
		doc.Text = string(data)
		for provider, embedder := range h.embedders {
			vec, err := application.EmbedTitleWeighted(ctx, embedder.Embed, doc.Title, doc.Text, v.EmbeddingTitleWeight)
			if err != nil {
				_ = h.documentJobs.MarkDocumentJobFailed(ctx, jobID, fmt.Errorf("embedding text: %w", err))
				return
			}
			embeddings[provider] = vec
		}
	case isImage:
		if vec, provider, ok := h.embedImageForDocument(ctx, data, contentType); ok {
			embeddings[provider] = vec
		}
		// No configured/capable vision-similarity provider: the job still
		// succeeds, just without an ANN vector -- metadata-only, same as a
		// text upload with every text-embedding provider disabled.
	}

	if err := h.saveDocumentJobContent(ctx, doc, embeddings, v.MaxDocumentVersions, v.TitleWeight, indexVocabulary); err != nil {
		_ = h.documentJobs.MarkDocumentJobFailed(ctx, jobID, fmt.Errorf("indexing document: %w", err))
		return
	}
	_ = h.documentJobs.MarkDocumentJobDone(ctx, jobID, docID, int64(len(data)))
}

// countingReader wraps an io.Reader, tallying bytes actually read -- used
// by streamAndProcessImageDocumentJob to learn a streamed image's true
// size (never known upfront: a multipart part carries no overall
// Content-Length of its own) without buffering it.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// streamAndProcessImageDocumentJob is handleUploadDocumentJob's image
// branch: file's bytes are streamed straight into the configured
// vision-similarity provider's embedding request (see
// embedImageForDocumentStream), never buffered into a []byte or persisted
// anywhere. This must run synchronously, unlike every other job kind's
// fire-and-forget background goroutine -- file is a *multipart.Reader
// backed by this request's own body, which Go closes out from under a
// goroutine the instant this handler returns, so there is no way to keep
// reading it after responding. The caller's HTTP call therefore blocks
// for the embedding round-trip (typically a few seconds, bounded by
// httpembed's own requestTimeout) -- an accepted, deliberate tradeoff for
// never buffering/persisting the image (see this feature's own design
// notes). The job still passes through the same
// Queued->Running->Done/Failed states every other job does, just
// compressed into one request instead of spread across a poll loop.
func (h *Handler) streamAndProcessImageDocumentJob(w http.ResponseWriter, r *http.Request, filename, contentType string, file io.Reader, indexVocabulary bool) {
	// r.Context(), deliberately -- unlike processDocumentJob's own
	// context.Background() (fire-and-forget work that must outlive a
	// disconnected client), everything here runs before this handler ever
	// returns, so a client disconnect SHOULD cancel it: file itself is
	// this same request's body and becomes invalid the moment it ends.
	ctx := r.Context()
	job, err := h.documentJobs.CreateDocumentJob(ctx, filename, contentType, 0, domain.DocumentJobSourceUpload, indexVocabulary)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.documentJobs.MarkDocumentJobRunning(ctx, job.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	counting := &countingReader{r: file}
	vec, provider, embedErr := h.embedImageForDocumentStream(ctx, counting, contentType)
	if embedErr != nil {
		_ = h.documentJobs.MarkDocumentJobFailed(ctx, job.ID, fmt.Errorf("embedding image: %w", embedErr))
		h.respondWithCurrentJobState(w, ctx, job.ID)
		return
	}

	docID := "doc-" + job.ID
	doc := domain.Document{ID: docID, URL: "upload://" + job.ID, Title: filename, CrawledAt: time.Now().UTC()}
	embeddings := map[string][]float32{}
	if provider != "" {
		embeddings[provider] = vec
	}
	v := h.opSettings.Get()
	if err := h.saveDocumentJobContent(ctx, doc, embeddings, v.MaxDocumentVersions, v.TitleWeight, indexVocabulary); err != nil {
		_ = h.documentJobs.MarkDocumentJobFailed(ctx, job.ID, fmt.Errorf("indexing document: %w", err))
	} else {
		_ = h.documentJobs.MarkDocumentJobDone(ctx, job.ID, docID, counting.n)
	}
	h.respondWithCurrentJobState(w, ctx, job.ID)
}

// respondWithCurrentJobState re-reads id and writes it as the response --
// streamAndProcessImageDocumentJob's finished (Done or Failed) job state,
// rather than the initial Queued row every other job kind responds with.
func (h *Handler) respondWithCurrentJobState(w http.ResponseWriter, ctx context.Context, id string) {
	job, err := h.documentJobs.GetDocumentJob(ctx, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, toDocumentJobResponse(job))
}

// saveDocumentJobContent calls AdminRepository's
// SaveDocumentOptionalVocabulary -- a separate, narrowly-typed method
// (h.admin, not h.documentJobs) since DocumentJobStore's own surface has no
// reason to carry full document-indexing capability.
func (h *Handler) saveDocumentJobContent(ctx context.Context, doc domain.Document, embeddings map[string][]float32, maxVersions, titleWeight int, indexVocabulary bool) error {
	if h.admin == nil {
		return fmt.Errorf("admin repository not configured")
	}
	return h.admin.SaveDocumentOptionalVocabulary(ctx, doc, embeddings, maxVersions, titleWeight, indexVocabulary)
}

// embedImageForDocument embeds data through the SAME provider chat's own
// vision-similarity feature uses (ChatVisionSettings.SimilarityProviderID)
// -- never by looping over every configured text-embedding provider and
// type-asserting ImageEmbedder, since an unrelated text-only endpoint
// could otherwise silently "succeed" with a meaningless vector. ok is
// false (not an error) whenever similarity search isn't configured/capable
// -- an uploaded image with no ANN vector is a valid, expected outcome.
func (h *Handler) embedImageForDocument(ctx context.Context, data []byte, contentType string) (vec []float32, provider string, ok bool) {
	if h.chatVision == nil {
		return nil, "", false
	}
	settings, err := h.chatVision.GetChatVisionSettings(ctx)
	if err != nil || !settings.SimilarityEnabled || settings.SimilarityProviderID == "" {
		return nil, "", false
	}
	embedder, exists := h.embedders[settings.SimilarityProviderID]
	if !exists {
		return nil, "", false
	}
	imageEmbedder, capable := embedder.(ports.ImageEmbedder)
	if !capable {
		return nil, "", false
	}
	v, err := imageEmbedder.EmbedImage(ctx, base64StdEncode(data), contentType)
	if err != nil {
		return nil, "", false
	}
	return v, settings.SimilarityProviderID, true
}

// embedImageForDocumentStream is embedImageForDocument's streaming
// counterpart -- same "only through the chat-configured vision-similarity
// provider" rule and same "not configured/capable" contract (provider ""
// with a nil error, not itself a failure -- see embedImageForDocument's
// own doc comment for why), but never buffers r into a []byte: it's
// streamed straight into the embedding request body. A non-nil error here
// means the provider WAS configured and capable but the call itself
// failed (network error, non-2xx, bad response shape) -- genuinely
// distinct from "not configured", and the one case
// streamAndProcessImageDocumentJob treats as a real job failure.
func (h *Handler) embedImageForDocumentStream(ctx context.Context, r io.Reader, contentType string) (vec []float32, provider string, err error) {
	streamer, provider := h.streamingImageEmbedderFor(ctx)
	if streamer == nil {
		// No capable/configured provider: still fully drain r (discarding
		// the bytes, never buffering them) rather than leaving it unread --
		// the caller's countingReader wraps r specifically so its own
		// streamAndProcessImageDocumentJob learns the upload's true size
		// regardless of whether an embedding was actually attempted.
		if _, err := io.Copy(io.Discard, r); err != nil {
			return nil, "", err
		}
		return nil, "", nil
	}
	v, err := streamer.EmbedImageStream(ctx, r, contentType)
	if err != nil {
		return nil, "", err
	}
	return v, provider, nil
}

// streamingImageEmbedderFor resolves the SAME provider chat's own
// vision-similarity feature uses (ChatVisionSettings.SimilarityProviderID)
// -- never by looping over every configured text-embedding provider and
// type-asserting StreamingImageEmbedder, for the same reason
// embedImageForDocument's own doc comment gives. streamer is nil (not an
// error) whenever similarity search isn't configured/capable for
// streaming -- an uploaded image with no ANN vector is a valid, expected
// outcome.
func (h *Handler) streamingImageEmbedderFor(ctx context.Context) (streamer ports.StreamingImageEmbedder, provider string) {
	if h.chatVision == nil {
		return nil, ""
	}
	settings, err := h.chatVision.GetChatVisionSettings(ctx)
	if err != nil || !settings.SimilarityEnabled || settings.SimilarityProviderID == "" {
		return nil, ""
	}
	embedder, exists := h.embedders[settings.SimilarityProviderID]
	if !exists {
		return nil, ""
	}
	streamer, capable := embedder.(ports.StreamingImageEmbedder)
	if !capable {
		return nil, ""
	}
	return streamer, settings.SimilarityProviderID
}

func (h *Handler) handleAdminDocumentJob(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.documentJobs != nil, configNameDocumentJobs) {
		return
	}
	job, err := h.documentJobs.GetDocumentJob(r.Context(), r.PathValue("id"))
	respondOrNotFound(w, err, domain.ErrDocumentJobNotFound, "document job not found", toDocumentJobResponse(job))
}

func (h *Handler) handleAdminDeleteDocumentJob(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.documentJobs != nil, configNameDocumentJobs) {
		return
	}
	err := h.documentJobs.DeleteDocumentJob(r.Context(), r.PathValue("id"))
	respondOrNotFound(w, err, domain.ErrDocumentJobNotFound, "document job not found", map[string]bool{"ok": true})
}

// handleAdminGetDocument returns one indexed document's full metadata and
// text, by ID -- generically useful for any document (not just one a
// Document-upload job produced), same as handleAdminDeleteDocument already
// is. No equivalent GET existed before this -- only DELETE and /versions.
type adminDocumentDetail struct {
	ID    string `json:"id"`
	URL   string `json:"url"`
	Title string `json:"title"`
	Text  string `json:"text"`
}

func (h *Handler) handleAdminGetDocument(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.admin != nil, configNameAdminDiagnostics) {
		return
	}
	id := r.PathValue("id")
	docs, err := h.admin.DocumentsByIDs(r.Context(), []string{id})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	doc, ok := docs[id]
	if !ok {
		http.Error(w, "document not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, adminDocumentDetail{ID: doc.ID, URL: doc.URL, Title: doc.Title, Text: doc.Text})
}

// --- S3 import (per-upload credentials, never stored -- see CLAUDE.md-style
// design rationale in the PR this shipped with) ---

// s3ImportRequest is decoded straight from the admin's POST body -- every
// field here is used once, to fetch this one object, then discarded. It is
// NEVER persisted (document_jobs stores only the resulting bytes, not
// these credentials) and never logged.
type s3ImportRequest struct {
	// Endpoint is the S3-compatible service's base URL, e.g.
	// "https://s3.example.com" -- empty selects real AWS
	// (https://s3.<region>.amazonaws.com), virtual-hosted-style; a
	// non-empty Endpoint always uses path-style (bucket in the path), the
	// broadly-compatible choice for third-party S3-compatible providers.
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region"`
	Bucket          string `json:"bucket"`
	Key             string `json:"key"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	SessionToken    string `json:"session_token"`
	IndexVocabulary bool   `json:"index_vocabulary"`
}

func (req s3ImportRequest) validate() error {
	switch {
	case req.Region == "":
		return fmt.Errorf("region must not be empty")
	case req.Bucket == "":
		return fmt.Errorf("bucket must not be empty")
	case req.Key == "":
		return fmt.Errorf("key must not be empty")
	case req.AccessKeyID == "":
		return fmt.Errorf("access_key_id must not be empty")
	case req.SecretAccessKey == "":
		return fmt.Errorf("secret_access_key must not be empty")
	}
	return nil
}

func (h *Handler) handleAdminImportDocumentFromS3(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.documentJobs != nil, configNameDocumentJobs) {
		return
	}
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	req, ok := decodeJSON[s3ImportRequest](w, r)
	if !ok {
		return
	}
	if err := req.validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	data, contentType, err := s3GetObject(r.Context(), req, int64(h.opSettings.Get().MaxDocumentUploadBytes))
	if err != nil {
		http.Error(w, "fetching from S3: "+err.Error(), http.StatusBadGateway)
		return
	}
	filename := req.Key
	if idx := strings.LastIndexByte(filename, '/'); idx >= 0 {
		filename = filename[idx+1:]
	}
	h.createAndProcessDocumentJob(w, r, filename, contentType, data, domain.DocumentJobSourceS3, req.IndexVocabulary)
}

// s3HTTPClient routes every dial through netguard.ConfiguredEndpointDialContext,
// so a redirect hop or a changed DNS answer can't land on a blocked address --
// the belt-and-suspenders pair to s3GetObject's own pre-request
// ConfiguredEndpointURLAllowed check, same convention as httpembed/httpchat's
// identically-purposed client field.
var s3HTTPClient = &http.Client{Transport: netguard.ConfiguredEndpointTransport()}

// s3GetObject fetches one object via a hand-signed AWS Signature Version 4
// request -- no AWS SDK dependency, since a single authenticated GET is a
// small, well-documented algorithm (stdlib crypto/hmac + crypto/sha256
// only), consistent with this codebase's preference for a small,
// dependency-free implementation over a heavy SDK for one API call.
// maxObjectBytes bounds the fetch -- same admin-configured ceiling as a
// direct document upload (OperationalSettingsValues.MaxDocumentUploadBytes),
// so neither path can be used to pull down an unbounded amount of data.
func s3GetObject(ctx context.Context, req s3ImportRequest, maxObjectBytes int64) (data []byte, contentType string, err error) {
	pathStyle := req.Endpoint != ""
	host, rawURL := s3RequestURL(req, pathStyle)
	// Same SSRF guard every other admin-configured-endpoint caller in this
	// codebase uses (httpembed/httpchat's checkEndpointURL) -- an admin
	// typing "endpoint" controls this URL entirely, so it must never be
	// allowed to reach a link-local/private/cloud-metadata address.
	if !netguard.ConfiguredEndpointURLAllowed(rawURL) {
		return nil, "", fmt.Errorf("endpoint URL is not allowed: %s", rawURL)
	}

	amzDate := time.Now().UTC().Format("20060102T150405Z")
	dateStamp := amzDate[:8]
	payloadHash := sha256Hex(nil)

	headers := map[string]string{
		"host":                 host,
		"x-amz-content-sha256": payloadHash,
		"x-amz-date":           amzDate,
	}
	if req.SessionToken != "" {
		headers["x-amz-security-token"] = req.SessionToken
	}
	signedHeaders, canonicalHeaders := s3CanonicalHeaders(headers)

	canonicalURI := s3CanonicalURI(pathStyle, req.Bucket, req.Key)
	canonicalRequest := strings.Join([]string{
		"GET", canonicalURI, "", canonicalHeaders, signedHeaders, payloadHash,
	}, "\n")

	credentialScope := dateStamp + "/" + req.Region + "/s3/aws4_request"
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256", amzDate, credentialScope, sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	signingKey := s3SigningKey(req.SecretAccessKey, dateStamp, req.Region)
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))

	authHeader := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		req.AccessKeyID, credentialScope, signedHeaders, signature)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	httpReq.Header.Set("Host", host)
	httpReq.Header.Set("X-Amz-Date", amzDate)
	httpReq.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if req.SessionToken != "" {
		httpReq.Header.Set("X-Amz-Security-Token", req.SessionToken)
	}
	httpReq.Header.Set("Authorization", authHeader)

	resp, err := s3HTTPClient.Do(httpReq)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, "", fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxObjectBytes+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(body)) > maxObjectBytes {
		return nil, "", fmt.Errorf("object exceeds %d byte limit", maxObjectBytes)
	}
	return body, resp.Header.Get("Content-Type"), nil
}

// s3RequestURL builds the object's host and full request URL. Path-style
// (Endpoint set) puts the bucket in the path -- works against virtually
// every S3-compatible provider, not just ones that support
// virtual-hosted-style bucket subdomains. Virtual-hosted-style (real AWS,
// no Endpoint) is AWS's own modern default.
func s3RequestURL(req s3ImportRequest, pathStyle bool) (host, rawURL string) {
	key := s3EncodePath(req.Key)
	if pathStyle {
		base := strings.TrimRight(req.Endpoint, "/")
		u, _ := url.Parse(base)
		return u.Host, base + "/" + req.Bucket + "/" + key
	}
	host = req.Bucket + ".s3." + req.Region + ".amazonaws.com"
	return host, "https://" + host + "/" + key
}

func s3CanonicalURI(pathStyle bool, bucket, key string) string {
	encodedKey := s3EncodePath(key)
	if pathStyle {
		return "/" + bucket + "/" + encodedKey
	}
	return "/" + encodedKey
}

// s3EncodePath percent-encodes each path segment per RFC 3986 while
// preserving "/" separators -- url.PathEscape alone would also escape "/".
func s3EncodePath(key string) string {
	segments := strings.Split(key, "/")
	for i, seg := range segments {
		segments[i] = url.PathEscape(seg)
	}
	return strings.Join(segments, "/")
}

// s3CanonicalHeaders returns (signedHeaders, canonicalHeaders) for SigV4:
// headers sorted by lowercase name, each rendered as "name:value\n" for
// canonicalHeaders, and joined by ";" for signedHeaders -- both must list
// names in the identical sorted order.
func s3CanonicalHeaders(headers map[string]string) (signedHeaders, canonicalHeaders string) {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		b.WriteString(name)
		b.WriteByte(':')
		b.WriteString(strings.TrimSpace(headers[name]))
		b.WriteByte('\n')
	}
	return strings.Join(names, ";"), b.String()
}

func s3SigningKey(secret, dateStamp, region string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), dateStamp)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, "s3")
	return hmacSHA256(kService, "aws4_request")
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// base64StdEncode is a tiny alias kept local to this file so its one call
// site (embedImageForDocument) reads plainly -- ImageEmbedder's contract
// takes base64-encoded image data (same as vision_similarity.go's request
// shape), not raw bytes.
func base64StdEncode(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}
