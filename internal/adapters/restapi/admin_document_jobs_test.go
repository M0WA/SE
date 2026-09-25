package restapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"searchengine/internal/adapters/restapi"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// fakeDocumentJobStore is a minimal ports.DocumentJobStore fake.
type fakeDocumentJobStore struct {
	mu sync.Mutex

	jobs           map[string]domain.DocumentJob
	data           map[string][]byte
	createErr      error
	listErr        error
	getErr         error
	dataErr        error
	deleteErr      error
	markRunningErr error

	// created records every CreateDocumentJob call's data, for tests
	// asserting what actually got persisted.
	created [][]byte
}

func newFakeDocumentJobStore() *fakeDocumentJobStore {
	return &fakeDocumentJobStore{jobs: map[string]domain.DocumentJob{}, data: map[string][]byte{}}
}

func (f *fakeDocumentJobStore) CreateDocumentJob(_ context.Context, filename, contentType string, size int64, source domain.DocumentJobSource, indexVocabulary bool, data []byte) (domain.DocumentJob, error) {
	if f.createErr != nil {
		return domain.DocumentJob{}, f.createErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	id := "docjob-" + filename
	job := domain.DocumentJob{
		ID: id, Filename: filename, ContentType: contentType, Size: size,
		Source: source, IndexVocabulary: indexVocabulary, Status: domain.DocumentJobQueued,
		CreatedAt: time.Now().UTC(),
	}
	f.jobs[id] = job
	f.data[id] = data
	f.created = append(f.created, data)
	return job, nil
}

func (f *fakeDocumentJobStore) MarkDocumentJobRunning(_ context.Context, id string) error {
	if f.markRunningErr != nil {
		return f.markRunningErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	j := f.jobs[id]
	j.Status = domain.DocumentJobRunning
	f.jobs[id] = j
	return nil
}

func (f *fakeDocumentJobStore) MarkDocumentJobDone(_ context.Context, id, docID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	j := f.jobs[id]
	j.Status = domain.DocumentJobDone
	j.DocID = docID
	f.jobs[id] = j
	return nil
}

func (f *fakeDocumentJobStore) MarkDocumentJobFailed(_ context.Context, id string, failErr error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	j := f.jobs[id]
	j.Status = domain.DocumentJobFailed
	j.Error = failErr.Error()
	f.jobs[id] = j
	return nil
}

func (f *fakeDocumentJobStore) GetDocumentJob(_ context.Context, id string) (domain.DocumentJob, error) {
	if f.getErr != nil {
		return domain.DocumentJob{}, f.getErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.jobs[id]
	if !ok {
		return domain.DocumentJob{}, domain.ErrDocumentJobNotFound
	}
	return j, nil
}

func (f *fakeDocumentJobStore) GetDocumentJobData(_ context.Context, id string) ([]byte, string, error) {
	if f.dataErr != nil {
		return nil, "", f.dataErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.jobs[id]
	if !ok {
		return nil, "", domain.ErrDocumentJobNotFound
	}
	return f.data[id], j.ContentType, nil
}

func (f *fakeDocumentJobStore) ListDocumentJobs(_ context.Context) ([]domain.DocumentJob, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.DocumentJob, 0, len(f.jobs))
	for _, j := range f.jobs {
		out = append(out, j)
	}
	return out, nil
}

func (f *fakeDocumentJobStore) DeleteDocumentJob(_ context.Context, id string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.jobs[id]; !ok {
		return domain.ErrDocumentJobNotFound
	}
	delete(f.jobs, id)
	delete(f.data, id)
	return nil
}

// jobsAwait polls store's job with id until pred is true or the deadline
// passes -- processDocumentJob runs in a detached background goroutine
// (by design, see its own doc comment), so a test observing its outcome
// must poll rather than assume synchronous completion.
func jobsAwait(t *testing.T, store *fakeDocumentJobStore, id string, pred func(domain.DocumentJob) bool) domain.DocumentJob {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		store.mu.Lock()
		j, ok := store.jobs[id]
		store.mu.Unlock()
		if ok && pred(j) {
			return j
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for document job %s", id)
	return domain.DocumentJob{}
}

func documentJobsHandler(t *testing.T, store ports.DocumentJobStore, admin ports.AdminRepository) (*restapi.Handler, *http.Cookie) {
	t.Helper()
	return adminAuthedHandlerFromConfig(t, restapi.Config{
		DocumentJobs: store, Admin: admin,
		OpSettings: domain.NewOperationalSettings(domain.OperationalSettingsValues{}), DBDriver: "pgx",
	})
}

func uploadDocumentJob(t *testing.T, h *restapi.Handler, cookie *http.Cookie, filename, contentType string, content []byte, indexVocabulary bool) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("index_vocabulary", map[bool]string{true: "true", false: "false"}[indexVocabulary]); err != nil {
		t.Fatalf("building multipart request: %v", err)
	}
	part, err := mw.CreatePart(map[string][]string{
		"Content-Disposition": {`form-data; name="file"; filename="` + filename + `"`},
		"Content-Type":        {contentType},
	})
	if err != nil {
		t.Fatalf("building multipart request: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("building multipart request: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("building multipart request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/api/document-jobs", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	return rec
}

func TestHandleAdminDocumentJobs_UploadText_Succeeds(t *testing.T) {
	store := newFakeDocumentJobStore()
	admin := &fakeAdminRepo{}
	h, cookie := documentJobsHandler(t, store, admin)

	rec := uploadDocumentJob(t, h, cookie, "notes.txt", "text/plain", []byte("hello world"), true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	job := jobsAwait(t, store, resp.ID, func(j domain.DocumentJob) bool { return j.Status == domain.DocumentJobDone })
	if job.DocID == "" {
		t.Error("expected DocID to be set once done")
	}
	admin.mu.Lock()
	saved := admin.savedDocs
	vocab := admin.savedIndexVocabulary
	admin.mu.Unlock()
	if len(saved) != 1 || saved[0].Text != "hello world" || saved[0].Links != nil {
		t.Errorf("unexpected saved document: %+v", saved)
	}
	if len(vocab) != 1 || vocab[0] != true {
		t.Errorf("expected indexVocabulary=true to reach SaveDocumentOptionalVocabulary, got %v", vocab)
	}
}

func TestHandleAdminDocumentJobs_UploadUnsupportedType_BadRequest(t *testing.T) {
	store := newFakeDocumentJobStore()
	h, cookie := documentJobsHandler(t, store, &fakeAdminRepo{})

	// Invalid UTF-8, not image/* -- neither text nor image.
	rec := uploadDocumentJob(t, h, cookie, "file.bin", "application/octet-stream", []byte{0xff, 0xfe, 0xfd}, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminDocumentJobs_NotConfigured(t *testing.T) {
	h, cookie := documentJobsHandler(t, nil, &fakeAdminRepo{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/document-jobs", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

func TestHandleAdminDocumentJobs_MethodNotAllowed(t *testing.T) {
	h, cookie := documentJobsHandler(t, newFakeDocumentJobStore(), &fakeAdminRepo{})
	req := httptest.NewRequest(http.MethodPut, "/admin/api/document-jobs", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestHandleAdminDocumentJobs_List(t *testing.T) {
	store := newFakeDocumentJobStore()
	store.jobs["docjob-a"] = domain.DocumentJob{ID: "docjob-a", Filename: "a.txt", Status: domain.DocumentJobDone}
	h, cookie := documentJobsHandler(t, store, &fakeAdminRepo{})

	req := httptest.NewRequest(http.MethodGet, "/admin/api/document-jobs", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var jobs []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &jobs)
	if len(jobs) != 1 || jobs[0]["filename"] != "a.txt" {
		t.Errorf("unexpected list response: %+v", jobs)
	}
}

func TestHandleAdminDocumentJobs_ListError(t *testing.T) {
	store := newFakeDocumentJobStore()
	store.listErr = errors.New("db down")
	h, cookie := documentJobsHandler(t, store, &fakeAdminRepo{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/document-jobs", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAdminDocumentJob_GetFound(t *testing.T) {
	store := newFakeDocumentJobStore()
	store.jobs["docjob-a"] = domain.DocumentJob{ID: "docjob-a", Filename: "a.txt"}
	h, cookie := documentJobsHandler(t, store, &fakeAdminRepo{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/document-jobs/docjob-a", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminDocumentJob_GetNotFound(t *testing.T) {
	store := newFakeDocumentJobStore()
	h, cookie := documentJobsHandler(t, store, &fakeAdminRepo{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/document-jobs/missing", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestHandleAdminDeleteDocumentJob_Succeeds(t *testing.T) {
	store := newFakeDocumentJobStore()
	store.jobs["docjob-a"] = domain.DocumentJob{ID: "docjob-a"}
	h, cookie := documentJobsHandler(t, store, &fakeAdminRepo{})
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/document-jobs/docjob-a", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminDeleteDocumentJob_NotFound(t *testing.T) {
	store := newFakeDocumentJobStore()
	h, cookie := documentJobsHandler(t, store, &fakeAdminRepo{})
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/document-jobs/missing", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestHandleAdminDocumentJobData_ServesRawBytes(t *testing.T) {
	store := newFakeDocumentJobStore()
	store.jobs["docjob-a"] = domain.DocumentJob{ID: "docjob-a", ContentType: "image/png"}
	store.data["docjob-a"] = []byte("fake-png-bytes")
	h, cookie := documentJobsHandler(t, store, &fakeAdminRepo{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/document-jobs/docjob-a/data", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("expected Content-Type image/png, got %q", ct)
	}
	if rec.Body.String() != "fake-png-bytes" {
		t.Errorf("unexpected body: %q", rec.Body.String())
	}
}

func TestHandleAdminDocumentJobData_NotFound(t *testing.T) {
	store := newFakeDocumentJobStore()
	h, cookie := documentJobsHandler(t, store, &fakeAdminRepo{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/document-jobs/missing/data", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestHandleAdminGetDocument_Found(t *testing.T) {
	admin := &fakeAdminRepo{postingsDocs: map[string]domain.Document{"doc-1": {ID: "doc-1", URL: "upload://doc-1", Title: "t", Text: "body text"}}}
	h, cookie := documentJobsHandler(t, newFakeDocumentJobStore(), admin)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/documents/doc-1", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Text != "body text" {
		t.Errorf("expected text %q, got %q", "body text", resp.Text)
	}
}

func TestHandleAdminGetDocument_NotFound(t *testing.T) {
	admin := &fakeAdminRepo{postingsDocs: map[string]domain.Document{}}
	h, cookie := documentJobsHandler(t, newFakeDocumentJobStore(), admin)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/documents/missing", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestHandleAdminGetDocument_NotConfigured(t *testing.T) {
	h, cookie := documentJobsHandler(t, newFakeDocumentJobStore(), nil)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/documents/doc-1", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

// --- S3 import ---

func TestHandleAdminImportDocumentFromS3_ValidationErrors(t *testing.T) {
	h, cookie := documentJobsHandler(t, newFakeDocumentJobStore(), &fakeAdminRepo{})
	cases := []struct {
		name string
		body map[string]any
	}{
		{"missing region", map[string]any{"bucket": "b", "key": "k", "access_key_id": "a", "secret_access_key": "s"}},
		{"missing bucket", map[string]any{"region": "r", "key": "k", "access_key_id": "a", "secret_access_key": "s"}},
		{"missing key", map[string]any{"region": "r", "bucket": "b", "access_key_id": "a", "secret_access_key": "s"}},
		{"missing access_key_id", map[string]any{"region": "r", "bucket": "b", "key": "k", "secret_access_key": "s"}},
		{"missing secret_access_key", map[string]any{"region": "r", "bucket": "b", "key": "k", "access_key_id": "a"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body, _ := json.Marshal(c.body)
			req := httptest.NewRequest(http.MethodPost, "/admin/api/document-jobs/import-s3", bytes.NewReader(body))
			req.AddCookie(cookie)
			rec := httptest.NewRecorder()
			h.RoutesAdmin().ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandleAdminImportDocumentFromS3_FetchFailureIsBadGateway(t *testing.T) {
	h, cookie := documentJobsHandler(t, newFakeDocumentJobStore(), &fakeAdminRepo{})
	body, _ := json.Marshal(map[string]any{
		"endpoint": "http://127.0.0.1:1", // nothing listens here
		"region":   "us-east-1", "bucket": "b", "key": "k",
		"access_key_id": "a", "secret_access_key": "s",
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/document-jobs/import-s3", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandleAdminImportDocumentFromS3_LinkLocalEndpointRejected proves the
// S3 import path is guarded by the same netguard.ConfiguredEndpointURLAllowed
// check every other admin-configured-endpoint caller in this codebase uses
// (httpembed/httpchat) -- an admin-supplied "endpoint" must never be able to
// reach a link-local address (e.g. cloud metadata services).
func TestHandleAdminImportDocumentFromS3_LinkLocalEndpointRejected(t *testing.T) {
	h, cookie := documentJobsHandler(t, newFakeDocumentJobStore(), &fakeAdminRepo{})
	body, _ := json.Marshal(map[string]any{
		"endpoint": "http://169.254.169.254/",
		"region":   "us-east-1", "bucket": "b", "key": "k",
		"access_key_id": "a", "secret_access_key": "s",
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/document-jobs/import-s3", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not allowed") {
		t.Errorf("expected a not-allowed message, got %q", rec.Body.String())
	}
}

func TestHandleAdminImportDocumentFromS3_Succeeds(t *testing.T) {
	var gotAuth, gotAmzDate, gotContentSHA string
	s3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAmzDate = r.Header.Get("X-Amz-Date")
		gotContentSHA = r.Header.Get("X-Amz-Content-Sha256")
		if r.URL.Path != "/my-bucket/reports/q1.txt" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("quarterly report"))
	}))
	defer s3.Close()

	store := newFakeDocumentJobStore()
	h, cookie := documentJobsHandler(t, store, &fakeAdminRepo{})
	body, _ := json.Marshal(map[string]any{
		"endpoint": s3.URL, "region": "us-east-1", "bucket": "my-bucket", "key": "reports/q1.txt",
		"access_key_id": "AKIAEXAMPLE", "secret_access_key": "secret", "index_vocabulary": true,
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/document-jobs/import-s3", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(gotAuth, "AWS4-HMAC-SHA256 Credential=AKIAEXAMPLE/") {
		t.Errorf("unexpected Authorization header: %q", gotAuth)
	}
	if gotAmzDate == "" || gotContentSHA == "" {
		t.Errorf("expected X-Amz-Date/X-Amz-Content-Sha256 headers to be set")
	}

	var resp struct {
		ID     string `json:"id"`
		Source string `json:"source"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Source != "s3" {
		t.Errorf("expected source s3, got %q", resp.Source)
	}
	jobsAwait(t, store, resp.ID, func(j domain.DocumentJob) bool { return j.Status == domain.DocumentJobDone })

	store.mu.Lock()
	data := store.created[0]
	store.mu.Unlock()
	if string(data) != "quarterly report" {
		t.Errorf("unexpected imported data: %q", data)
	}
}

func TestHandleAdminImportDocumentFromS3_NotConfigured(t *testing.T) {
	h, cookie := documentJobsHandler(t, nil, &fakeAdminRepo{})
	body, _ := json.Marshal(map[string]any{"region": "r", "bucket": "b", "key": "k", "access_key_id": "a", "secret_access_key": "s"})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/document-jobs/import-s3", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

// A GET here never reaches handleAdminImportDocumentFromS3's own internal
// requireMethod check at all -- "POST /admin/api/document-jobs/import-s3"
// is the only pattern registered for this literal path, so Go's ServeMux
// itself reports a plain 404 for any other method (405 only applies when
// the same literal path is ALSO registered under a different method).
// Same convention as handleAdminMCPServersTest's identical
// single-method-only registration.
func TestHandleAdminImportDocumentFromS3_WrongMethodIsNotFound(t *testing.T) {
	h, cookie := documentJobsHandler(t, newFakeDocumentJobStore(), &fakeAdminRepo{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/document-jobs/import-s3", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

// --- Image upload + vision-similarity embedding wiring ---

func TestHandleAdminDocumentJobs_UploadImage_EmbedsThroughTheConfiguredSimilarityProvider(t *testing.T) {
	store := newFakeDocumentJobStore()
	admin := &fakeAdminRepo{}
	embedder := &fakeImageEmbeddingProvider{vec: []float32{0.1, 0.2}}
	chatVision := &fakeChatVisionStore{settings: domain.ChatVisionSettings{SimilarityEnabled: true, SimilarityProviderID: "vision-provider"}}

	h, cookie := adminAuthedHandlerFromConfig(t, restapi.Config{
		DocumentJobs: store, Admin: admin, ChatVision: chatVision,
		Embedders:  map[string]ports.EmbeddingProvider{"vision-provider": embedder},
		OpSettings: domain.NewOperationalSettings(domain.OperationalSettingsValues{}), DBDriver: "pgx",
	})

	rec := uploadDocumentJob(t, h, cookie, "photo.png", "image/png", []byte("fake-png-bytes"), false)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct{ ID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	jobsAwait(t, store, resp.ID, func(j domain.DocumentJob) bool { return j.Status == domain.DocumentJobDone })

	admin.mu.Lock()
	saved := admin.savedEmbeddings
	admin.mu.Unlock()
	if len(saved) != 1 || len(saved[0]["vision-provider"]) != 2 {
		t.Errorf("expected the image embedding to be saved under vision-provider, got %+v", saved)
	}
}

func TestHandleAdminDocumentJobs_UploadImage_NoSimilarityProviderConfiguredStillSucceeds(t *testing.T) {
	store := newFakeDocumentJobStore()
	admin := &fakeAdminRepo{}
	h, cookie := documentJobsHandler(t, store, admin)

	rec := uploadDocumentJob(t, h, cookie, "photo.png", "image/png", []byte("fake-png-bytes"), false)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct{ ID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	job := jobsAwait(t, store, resp.ID, func(j domain.DocumentJob) bool { return j.Status == domain.DocumentJobDone })
	if job.DocID == "" {
		t.Error("expected the job to still succeed, metadata-only, without a vision-similarity provider")
	}
}

// --- Coverage for the remaining branches: missing form fields, store
// errors, background-processing failure paths, embedImageForDocument's
// individual "no vector" reasons, and the per-endpoint 503s. ---

func TestHandleUploadDocumentJob_MissingFileField(t *testing.T) {
	h, cookie := documentJobsHandler(t, newFakeDocumentJobStore(), &fakeAdminRepo{})
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("index_vocabulary", "true")
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/admin/api/document-jobs", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleUploadDocumentJob_InvalidMultipartBody(t *testing.T) {
	h, cookie := documentJobsHandler(t, newFakeDocumentJobStore(), &fakeAdminRepo{})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/document-jobs", strings.NewReader("not multipart"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminDocumentJobs_UploadStoreErrorIsInternalError(t *testing.T) {
	store := newFakeDocumentJobStore()
	store.createErr = errors.New("db down")
	h, cookie := documentJobsHandler(t, store, &fakeAdminRepo{})
	rec := uploadDocumentJob(t, h, cookie, "notes.txt", "text/plain", []byte("hello"), true)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminDocumentJob_ResponseIncludesStartedAndFinishedTimestamps(t *testing.T) {
	started := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	finished := started.Add(time.Minute)
	store := newFakeDocumentJobStore()
	store.jobs["docjob-a"] = domain.DocumentJob{ID: "docjob-a", Filename: "a.txt", StartedAt: &started, FinishedAt: &finished}
	h, cookie := documentJobsHandler(t, store, &fakeAdminRepo{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/document-jobs/docjob-a", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	var resp struct {
		StartedAt  string `json:"started_at"`
		FinishedAt string `json:"finished_at"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.StartedAt == "" || resp.FinishedAt == "" {
		t.Errorf("expected started_at/finished_at to be present, got %+v", resp)
	}
}

func TestHandleAdminDocumentJob_NotConfigured(t *testing.T) {
	h, cookie := documentJobsHandler(t, nil, &fakeAdminRepo{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/document-jobs/docjob-a", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

func TestHandleAdminDeleteDocumentJob_NotConfigured(t *testing.T) {
	h, cookie := documentJobsHandler(t, nil, &fakeAdminRepo{})
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/document-jobs/docjob-a", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

func TestHandleAdminDocumentJobData_NotConfigured(t *testing.T) {
	h, cookie := documentJobsHandler(t, nil, &fakeAdminRepo{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/document-jobs/docjob-a/data", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

func TestHandleAdminGetDocument_RepositoryError(t *testing.T) {
	admin := &fakeAdminRepo{err: errors.New("db down")}
	h, cookie := documentJobsHandler(t, newFakeDocumentJobStore(), admin)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/documents/doc-1", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAdminDocumentJobs_UploadWithNoAdminRepositoryConfiguredFailsInBackground(t *testing.T) {
	store := newFakeDocumentJobStore()
	h, cookie := documentJobsHandler(t, store, nil)
	rec := uploadDocumentJob(t, h, cookie, "notes.txt", "text/plain", []byte("hello"), true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct{ ID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	job := jobsAwait(t, store, resp.ID, func(j domain.DocumentJob) bool { return j.Status == domain.DocumentJobFailed })
	if job.Error == "" {
		t.Error("expected a recorded failure reason")
	}
}

func TestHandleAdminDocumentJobs_TextEmbeddingFailureMarksTheJobFailed(t *testing.T) {
	store := newFakeDocumentJobStore()
	embedder := fakeEmbeddingProvider{err: errors.New("embedder down")}
	h, cookie := adminAuthedHandlerFromConfig(t, restapi.Config{
		DocumentJobs: store, Admin: &fakeAdminRepo{},
		Embedders:  map[string]ports.EmbeddingProvider{"hash": embedder},
		OpSettings: domain.NewOperationalSettings(domain.OperationalSettingsValues{}), DBDriver: "pgx",
	})
	rec := uploadDocumentJob(t, h, cookie, "notes.txt", "text/plain", []byte("hello"), true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct{ ID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	job := jobsAwait(t, store, resp.ID, func(j domain.DocumentJob) bool { return j.Status == domain.DocumentJobFailed })
	if !strings.Contains(job.Error, "embedding text") {
		t.Errorf("expected an embedding-text failure reason, got %q", job.Error)
	}
}

func TestHandleAdminDocumentJobs_IndexingFailureMarksTheJobFailed(t *testing.T) {
	store := newFakeDocumentJobStore()
	admin := &fakeAdminRepo{saveDocumentErr: errors.New("disk full")}
	h, cookie := documentJobsHandler(t, store, admin)
	rec := uploadDocumentJob(t, h, cookie, "notes.txt", "text/plain", []byte("hello"), true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct{ ID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	job := jobsAwait(t, store, resp.ID, func(j domain.DocumentJob) bool { return j.Status == domain.DocumentJobFailed })
	if !strings.Contains(job.Error, "indexing document") {
		t.Errorf("expected an indexing failure reason, got %q", job.Error)
	}
}

// embedImageForDocument's individual "no vector" reasons -- each exercised
// through a full image upload, since the function itself is unexported.
// Every case still expects the job to finish Done, just with no embedding
// saved (see TestHandleAdminDocumentJobs_UploadImage_EmbedsThroughTheConfiguredSimilarityProvider
// for the one case that DOES save an embedding).

func uploadImageAndAwaitDone(t *testing.T, store *fakeDocumentJobStore, h *restapi.Handler, cookie *http.Cookie) domain.DocumentJob {
	t.Helper()
	rec := uploadDocumentJob(t, h, cookie, "photo.png", "image/png", []byte("fake-png-bytes"), false)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct{ ID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return jobsAwait(t, store, resp.ID, func(j domain.DocumentJob) bool { return j.Status == domain.DocumentJobDone })
}

func TestEmbedImageForDocument_ChatVisionSettingsFetchError(t *testing.T) {
	store := newFakeDocumentJobStore()
	admin := &fakeAdminRepo{}
	chatVision := &fakeChatVisionStore{getErr: errors.New("db down")}
	h, cookie := adminAuthedHandlerFromConfig(t, restapi.Config{
		DocumentJobs: store, Admin: admin, ChatVision: chatVision,
		OpSettings: domain.NewOperationalSettings(domain.OperationalSettingsValues{}), DBDriver: "pgx",
	})
	uploadImageAndAwaitDone(t, store, h, cookie)
	admin.mu.Lock()
	defer admin.mu.Unlock()
	if len(admin.savedEmbeddings) != 1 || len(admin.savedEmbeddings[0]) != 0 {
		t.Errorf("expected no embedding saved, got %+v", admin.savedEmbeddings)
	}
}

func TestEmbedImageForDocument_SimilarityDisabled(t *testing.T) {
	store := newFakeDocumentJobStore()
	admin := &fakeAdminRepo{}
	chatVision := &fakeChatVisionStore{settings: domain.ChatVisionSettings{SimilarityEnabled: false, SimilarityProviderID: "vision-provider"}}
	h, cookie := adminAuthedHandlerFromConfig(t, restapi.Config{
		DocumentJobs: store, Admin: admin, ChatVision: chatVision,
		Embedders:  map[string]ports.EmbeddingProvider{"vision-provider": &fakeImageEmbeddingProvider{vec: []float32{1}}},
		OpSettings: domain.NewOperationalSettings(domain.OperationalSettingsValues{}), DBDriver: "pgx",
	})
	uploadImageAndAwaitDone(t, store, h, cookie)
	admin.mu.Lock()
	defer admin.mu.Unlock()
	if len(admin.savedEmbeddings[0]) != 0 {
		t.Errorf("expected no embedding saved when similarity is disabled, got %+v", admin.savedEmbeddings)
	}
}

func TestEmbedImageForDocument_ProviderNotFound(t *testing.T) {
	store := newFakeDocumentJobStore()
	admin := &fakeAdminRepo{}
	chatVision := &fakeChatVisionStore{settings: domain.ChatVisionSettings{SimilarityEnabled: true, SimilarityProviderID: "missing-provider"}}
	h, cookie := adminAuthedHandlerFromConfig(t, restapi.Config{
		DocumentJobs: store, Admin: admin, ChatVision: chatVision,
		OpSettings: domain.NewOperationalSettings(domain.OperationalSettingsValues{}), DBDriver: "pgx",
	})
	uploadImageAndAwaitDone(t, store, h, cookie)
	admin.mu.Lock()
	defer admin.mu.Unlock()
	if len(admin.savedEmbeddings[0]) != 0 {
		t.Errorf("expected no embedding saved for an unconfigured provider, got %+v", admin.savedEmbeddings)
	}
}

func TestEmbedImageForDocument_ProviderNotImageCapable(t *testing.T) {
	store := newFakeDocumentJobStore()
	admin := &fakeAdminRepo{}
	chatVision := &fakeChatVisionStore{settings: domain.ChatVisionSettings{SimilarityEnabled: true, SimilarityProviderID: "text-only"}}
	h, cookie := adminAuthedHandlerFromConfig(t, restapi.Config{
		DocumentJobs: store, Admin: admin, ChatVision: chatVision,
		Embedders:  map[string]ports.EmbeddingProvider{"text-only": fakeEmbeddingProvider{}},
		OpSettings: domain.NewOperationalSettings(domain.OperationalSettingsValues{}), DBDriver: "pgx",
	})
	uploadImageAndAwaitDone(t, store, h, cookie)
	admin.mu.Lock()
	defer admin.mu.Unlock()
	if len(admin.savedEmbeddings[0]) != 0 {
		t.Errorf("expected no embedding saved for a non-image-capable provider, got %+v", admin.savedEmbeddings)
	}
}

func TestEmbedImageForDocument_EmbedImageError(t *testing.T) {
	store := newFakeDocumentJobStore()
	admin := &fakeAdminRepo{}
	chatVision := &fakeChatVisionStore{settings: domain.ChatVisionSettings{SimilarityEnabled: true, SimilarityProviderID: "vision-provider"}}
	h, cookie := adminAuthedHandlerFromConfig(t, restapi.Config{
		DocumentJobs: store, Admin: admin, ChatVision: chatVision,
		Embedders:  map[string]ports.EmbeddingProvider{"vision-provider": &fakeImageEmbeddingProvider{err: errors.New("vision endpoint down")}},
		OpSettings: domain.NewOperationalSettings(domain.OperationalSettingsValues{}), DBDriver: "pgx",
	})
	uploadImageAndAwaitDone(t, store, h, cookie)
	admin.mu.Lock()
	defer admin.mu.Unlock()
	if len(admin.savedEmbeddings[0]) != 0 {
		t.Errorf("expected no embedding saved when EmbedImage fails, got %+v", admin.savedEmbeddings)
	}
}

func TestHandleAdminDocumentJobData_RepositoryError(t *testing.T) {
	store := newFakeDocumentJobStore()
	store.dataErr = errors.New("db down")
	h, cookie := documentJobsHandler(t, store, &fakeAdminRepo{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/document-jobs/docjob-a/data", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminDocumentJobData_EmptyContentTypeOmitsHeader(t *testing.T) {
	store := newFakeDocumentJobStore()
	store.jobs["docjob-a"] = domain.DocumentJob{ID: "docjob-a"}
	store.data["docjob-a"] = []byte("raw bytes")
	h, cookie := documentJobsHandler(t, store, &fakeAdminRepo{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/document-jobs/docjob-a/data", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "" {
		t.Errorf("expected no Content-Type header, got %q", ct)
	}
}

func TestHandleAdminImportDocumentFromS3_InvalidJSONBody(t *testing.T) {
	h, cookie := documentJobsHandler(t, newFakeDocumentJobStore(), &fakeAdminRepo{})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/document-jobs/import-s3", strings.NewReader("not json"))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminDocumentJobs_MarkRunningFailureLeavesTheJobUntouched(t *testing.T) {
	store := newFakeDocumentJobStore()
	store.markRunningErr = errors.New("db down")
	h, cookie := documentJobsHandler(t, store, &fakeAdminRepo{})
	rec := uploadDocumentJob(t, h, cookie, "notes.txt", "text/plain", []byte("hello"), true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct{ ID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	// The job never advances past Queued -- processDocumentJob returns
	// immediately when MarkDocumentJobRunning fails, before ever calling
	// MarkDocumentJobFailed (there's nothing meaningful to record failure
	// against if the store itself is unreachable).
	time.Sleep(50 * time.Millisecond)
	store.mu.Lock()
	status := store.jobs[resp.ID].Status
	store.mu.Unlock()
	if status != domain.DocumentJobQueued {
		t.Errorf("expected job to remain Queued, got %s", status)
	}
}
