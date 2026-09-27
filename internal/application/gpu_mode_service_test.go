package application

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

type fakeGPUModeStore struct {
	cfg       domain.GPUModeSettings
	notConfig bool
	err       error
}

func (f *fakeGPUModeStore) GetGPUModeSettings(ctx context.Context) (domain.GPUModeSettings, error) {
	if f.notConfig {
		return domain.GPUModeSettings{}, ports.ErrGPUModeSettingsNotConfigured
	}
	if f.err != nil {
		return domain.GPUModeSettings{}, f.err
	}
	return f.cfg, nil
}

func (f *fakeGPUModeStore) SetGPUModeSettings(ctx context.Context, v domain.GPUModeSettings) error {
	f.cfg = v
	return nil
}

type fakeGPUModeController struct {
	status    domain.GPUModeStatus
	statusErr error
	switchErr error
	heartErr  error

	switchCalls int
	lastTarget  domain.GPUMode

	generateJobID  string
	generateErr    error
	generateResult domain.GPUGenerateResult
	generateResErr error
	lastOpts       domain.VisionGenerateOptions
	viewContent    string
	viewBody       io.ReadCloser
	viewErr        error
}

func (f *fakeGPUModeController) Status(ctx context.Context, cfg domain.GPUModeSettings) (domain.GPUModeStatus, error) {
	return f.status, f.statusErr
}

func (f *fakeGPUModeController) Switch(ctx context.Context, cfg domain.GPUModeSettings, target domain.GPUMode) (domain.GPUModeStatus, error) {
	f.switchCalls++
	f.lastTarget = target
	return f.status, f.switchErr
}

func (f *fakeGPUModeController) Heartbeat(ctx context.Context, cfg domain.GPUModeSettings) error {
	return f.heartErr
}

func (f *fakeGPUModeController) Generate(ctx context.Context, cfg domain.GPUModeSettings, opts domain.VisionGenerateOptions) (string, error) {
	f.lastOpts = opts
	return f.generateJobID, f.generateErr
}

func (f *fakeGPUModeController) GenerateResult(ctx context.Context, cfg domain.GPUModeSettings, jobID string) (domain.GPUGenerateResult, error) {
	return f.generateResult, f.generateResErr
}

func (f *fakeGPUModeController) ViewAsset(ctx context.Context, cfg domain.GPUModeSettings, viewURL string) (string, io.ReadCloser, error) {
	return f.viewContent, f.viewBody, f.viewErr
}

func newTestGPUModeService(store *fakeGPUModeStore, controller *fakeGPUModeController) *GPUModeService {
	return newTestGPUModeServiceWithFiles(store, controller, nil)
}

func newTestGPUModeServiceWithFiles(store *fakeGPUModeStore, controller *fakeGPUModeController, files ports.FileStore) *GPUModeService {
	s := NewGPUModeService(store, controller, files)
	s.now = time.Now
	return s
}

// fakeFileStore is a minimal ports.FileStore fake -- only SaveFile is
// exercised by GPUModeService, the rest just need to satisfy the
// interface.
type fakeFileStore struct {
	saveErr    error
	saved      []domain.UploadedFile
	savedData  [][]byte
	nextFileID string
}

func (f *fakeFileStore) ListFiles(context.Context, string) ([]domain.UploadedFile, error) {
	return nil, nil
}
func (f *fakeFileStore) ListFilesForChat(context.Context, string, string) ([]domain.UploadedFile, error) {
	return nil, nil
}
func (f *fakeFileStore) SaveFile(_ context.Context, ownerUserID, chatID, filename, contentType string, data []byte) (domain.UploadedFile, error) {
	if f.saveErr != nil {
		return domain.UploadedFile{}, f.saveErr
	}
	id := f.nextFileID
	if id == "" {
		id = "file-1"
	}
	uf := domain.UploadedFile{ID: id, OwnerUserID: ownerUserID, ChatID: chatID, Filename: filename, ContentType: contentType, Size: int64(len(data))}
	f.saved = append(f.saved, uf)
	f.savedData = append(f.savedData, data)
	return uf, nil
}
func (f *fakeFileStore) GetFile(context.Context, string, string) (domain.UploadedFile, []byte, error) {
	return domain.UploadedFile{}, nil, nil
}
func (f *fakeFileStore) DeleteFile(context.Context, string, string) error { return nil }

func TestGPUModeService_Status_NotConfiguredMeansDisabledNotError(t *testing.T) {
	s := newTestGPUModeService(&fakeGPUModeStore{notConfig: true}, &fakeGPUModeController{})
	st, enabled, err := s.Status(context.Background())
	if err != nil || enabled || st != (domain.GPUModeStatus{}) {
		t.Fatalf("expected disabled/no-error, got st=%+v enabled=%v err=%v", st, enabled, err)
	}
}

func TestGPUModeService_Status_ExplicitlyDisabledMeansDisabled(t *testing.T) {
	s := newTestGPUModeService(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: false}}, &fakeGPUModeController{})
	_, enabled, err := s.Status(context.Background())
	if err != nil || enabled {
		t.Fatalf("expected disabled/no-error, got enabled=%v err=%v", enabled, err)
	}
}

func TestGPUModeService_Status_StoreErrorPropagates(t *testing.T) {
	wantErr := errors.New("db down")
	s := newTestGPUModeService(&fakeGPUModeStore{err: wantErr}, &fakeGPUModeController{})
	_, _, err := s.Status(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected store error to propagate, got %v", err)
	}
}

func TestGPUModeService_Status_EnabledCallsControllerAndCaches(t *testing.T) {
	want := domain.GPUModeStatus{Mode: domain.GPUModeChat}
	s := newTestGPUModeService(
		&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}},
		&fakeGPUModeController{status: want},
	)
	st, enabled, err := s.Status(context.Background())
	if err != nil || !enabled || st != want {
		t.Fatalf("unexpected result: st=%+v enabled=%v err=%v", st, enabled, err)
	}
	cached, cacheEnabled := s.CachedMode()
	if !cacheEnabled || cached != want {
		t.Fatalf("expected Status to populate the cache, got cached=%+v enabled=%v", cached, cacheEnabled)
	}
}

func TestGPUModeService_Status_ControllerErrorStillReportsEnabled(t *testing.T) {
	wantErr := errors.New("gpu-control unreachable")
	s := newTestGPUModeService(
		&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}},
		&fakeGPUModeController{statusErr: wantErr},
	)
	_, enabled, err := s.Status(context.Background())
	if !enabled {
		t.Fatal("expected enabled=true even when the live call fails")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected controller error to propagate, got %v", err)
	}
}

func TestGPUModeService_RefreshCache_DisabledClearsCache(t *testing.T) {
	s := newTestGPUModeService(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}}, &fakeGPUModeController{status: domain.GPUModeStatus{Mode: domain.GPUModeChat}})
	s.RefreshCache(context.Background())
	if _, enabled := s.CachedMode(); !enabled {
		t.Fatal("expected cache populated after first refresh")
	}

	s.settings = &fakeGPUModeStore{notConfig: true}
	s.RefreshCache(context.Background())
	if _, enabled := s.CachedMode(); enabled {
		t.Fatal("expected cache cleared once disabled")
	}
}

func TestGPUModeService_RefreshCache_StoreErrorLeavesCacheUntouched(t *testing.T) {
	s := newTestGPUModeService(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}}, &fakeGPUModeController{status: domain.GPUModeStatus{Mode: domain.GPUModeVision}})
	s.RefreshCache(context.Background())

	s.settings = &fakeGPUModeStore{err: errors.New("db down")}
	s.RefreshCache(context.Background())
	cached, enabled := s.CachedMode()
	if !enabled || cached.Mode != domain.GPUModeVision {
		t.Fatalf("expected stale cache preserved on store error, got %+v enabled=%v", cached, enabled)
	}
}

func TestGPUModeService_RefreshCache_LiveErrorLeavesCacheUntouched(t *testing.T) {
	controller := &fakeGPUModeController{status: domain.GPUModeStatus{Mode: domain.GPUModeChat}}
	s := newTestGPUModeService(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}}, controller)
	s.RefreshCache(context.Background())

	controller.statusErr = errors.New("timeout")
	s.RefreshCache(context.Background())
	cached, enabled := s.CachedMode()
	if !enabled || cached.Mode != domain.GPUModeChat {
		t.Fatalf("expected stale cache preserved on live error, got %+v enabled=%v", cached, enabled)
	}
}

func TestGPUModeService_CachedMode_DefaultsToDisabled(t *testing.T) {
	s := newTestGPUModeService(&fakeGPUModeStore{notConfig: true}, &fakeGPUModeController{})
	if _, enabled := s.CachedMode(); enabled {
		t.Fatal("expected a fresh service to report disabled")
	}
}

func TestGPUModeService_Switch_NotEnabledReturnsErrGPUModeNotEnabled(t *testing.T) {
	s := newTestGPUModeService(&fakeGPUModeStore{notConfig: true}, &fakeGPUModeController{})
	_, err := s.Switch(context.Background(), "acct1", domain.GPUModeVision)
	if !errors.Is(err, ErrGPUModeNotEnabled) {
		t.Fatalf("expected ErrGPUModeNotEnabled, got %v", err)
	}
}

func TestGPUModeService_Switch_StoreErrorPropagates(t *testing.T) {
	wantErr := errors.New("db down")
	s := newTestGPUModeService(&fakeGPUModeStore{err: wantErr}, &fakeGPUModeController{})
	_, err := s.Switch(context.Background(), "acct1", domain.GPUModeVision)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected store error to propagate, got %v", err)
	}
}

func TestGPUModeService_Switch_Success(t *testing.T) {
	want := domain.GPUModeStatus{Mode: domain.GPUModeChat, Target: domain.GPUModeVision, InProgress: true}
	controller := &fakeGPUModeController{status: want}
	s := newTestGPUModeService(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}}, controller)

	st, err := s.Switch(context.Background(), "acct1", domain.GPUModeVision)
	if err != nil {
		t.Fatalf("Switch: %v", err)
	}
	if st != want {
		t.Fatalf("unexpected status: %+v", st)
	}
	if controller.switchCalls != 1 || controller.lastTarget != domain.GPUModeVision {
		t.Fatalf("expected exactly one Switch(vision) call, got calls=%d target=%s", controller.switchCalls, controller.lastTarget)
	}
	if cached, enabled := s.CachedMode(); !enabled || cached != want {
		t.Fatalf("expected a successful Switch to populate the cache, got %+v enabled=%v", cached, enabled)
	}
}

func TestGPUModeService_Switch_ControllerErrorStillConsumesDwellAndRateLimit(t *testing.T) {
	controller := &fakeGPUModeController{switchErr: errors.New("gpu-control unreachable")}
	s := newTestGPUModeService(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}}, controller)

	_, err := s.Switch(context.Background(), "acct1", domain.GPUModeVision)
	if err == nil {
		t.Fatal("expected the controller error to propagate")
	}
	// A second immediate attempt must still be blocked by dwell, proving
	// the gate is applied before the (failing) live call, not after.
	_, err2 := s.Switch(context.Background(), "acct1", domain.GPUModeChat)
	if !errors.Is(err2, ErrGPUModeSwitchTooSoon) {
		t.Fatalf("expected ErrGPUModeSwitchTooSoon on the immediate retry, got %v", err2)
	}
}

func TestGPUModeService_Switch_DwellBlocksImmediateSecondSwitch(t *testing.T) {
	s := newTestGPUModeService(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}}, &fakeGPUModeController{})
	fixed := time.Now()
	s.now = func() time.Time { return fixed }

	if _, err := s.Switch(context.Background(), "acct1", domain.GPUModeVision); err != nil {
		t.Fatalf("first switch: %v", err)
	}
	_, err := s.Switch(context.Background(), "acct2", domain.GPUModeChat)
	if !errors.Is(err, ErrGPUModeSwitchTooSoon) {
		t.Fatalf("expected ErrGPUModeSwitchTooSoon (dwell is global, not per-account), got %v", err)
	}
}

func TestGPUModeService_Switch_DwellClearsAfterInterval(t *testing.T) {
	s := newTestGPUModeService(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}}, &fakeGPUModeController{})
	current := time.Now()
	s.now = func() time.Time { return current }

	if _, err := s.Switch(context.Background(), "acct1", domain.GPUModeVision); err != nil {
		t.Fatalf("first switch: %v", err)
	}
	current = current.Add(gpuModeSwitchDwell + time.Second)
	if _, err := s.Switch(context.Background(), "acct1", domain.GPUModeChat); err != nil {
		t.Fatalf("expected the second switch to succeed once the dwell interval passes, got %v", err)
	}
}

func TestGPUModeService_Switch_PerAccountRateLimit(t *testing.T) {
	s := newTestGPUModeService(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}}, &fakeGPUModeController{})
	current := time.Now()
	s.now = func() time.Time { return current }

	for i := 0; i < gpuModeSwitchRateLimit; i++ {
		// Advance past the dwell each time so only the rate limit is
		// under test here, not the global dwell.
		current = current.Add(gpuModeSwitchDwell + time.Second)
		if _, err := s.Switch(context.Background(), "acct1", domain.GPUModeVision); err != nil {
			t.Fatalf("switch %d: unexpected error %v", i, err)
		}
	}
	current = current.Add(gpuModeSwitchDwell + time.Second)
	_, err := s.Switch(context.Background(), "acct1", domain.GPUModeVision)
	if !errors.Is(err, ErrGPUModeRateLimited) {
		t.Fatalf("expected ErrGPUModeRateLimited on the %dth switch, got %v", gpuModeSwitchRateLimit+1, err)
	}
}

func TestGPUModeService_Switch_RateLimitWindowSlidesAndFreesUp(t *testing.T) {
	s := newTestGPUModeService(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}}, &fakeGPUModeController{})
	current := time.Now()
	s.now = func() time.Time { return current }

	for i := 0; i < gpuModeSwitchRateLimit; i++ {
		current = current.Add(gpuModeSwitchDwell + time.Second)
		if _, err := s.Switch(context.Background(), "acct1", domain.GPUModeVision); err != nil {
			t.Fatalf("switch %d: unexpected error %v", i, err)
		}
	}
	// Move past the rate-limit window entirely -- every earlier switch
	// should have aged out, freeing the account back up.
	current = current.Add(gpuModeRateLimitWindow + time.Minute)
	if _, err := s.Switch(context.Background(), "acct1", domain.GPUModeVision); err != nil {
		t.Fatalf("expected the account to be free again once the window slides, got %v", err)
	}
}

func TestGPUModeService_Switch_RateLimitIsPerAccount(t *testing.T) {
	s := newTestGPUModeService(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}}, &fakeGPUModeController{})
	current := time.Now()
	s.now = func() time.Time { return current }

	for i := 0; i < gpuModeSwitchRateLimit; i++ {
		current = current.Add(gpuModeSwitchDwell + time.Second)
		if _, err := s.Switch(context.Background(), "acct1", domain.GPUModeVision); err != nil {
			t.Fatalf("acct1 switch %d: unexpected error %v", i, err)
		}
	}
	current = current.Add(gpuModeSwitchDwell + time.Second)
	if _, err := s.Switch(context.Background(), "acct2", domain.GPUModeVision); err != nil {
		t.Fatalf("expected a different account to be unaffected by acct1's rate limit, got %v", err)
	}
}

func TestGPUModeService_Heartbeat_NotEnabled(t *testing.T) {
	s := newTestGPUModeService(&fakeGPUModeStore{notConfig: true}, &fakeGPUModeController{})
	if err := s.Heartbeat(context.Background()); !errors.Is(err, ErrGPUModeNotEnabled) {
		t.Fatalf("expected ErrGPUModeNotEnabled, got %v", err)
	}
}

func TestGPUModeService_Heartbeat_StoreErrorPropagates(t *testing.T) {
	wantErr := errors.New("db down")
	s := newTestGPUModeService(&fakeGPUModeStore{err: wantErr}, &fakeGPUModeController{})
	if err := s.Heartbeat(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("expected store error to propagate, got %v", err)
	}
}

func TestGPUModeService_Heartbeat_Success(t *testing.T) {
	s := newTestGPUModeService(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}}, &fakeGPUModeController{})
	if err := s.Heartbeat(context.Background()); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
}

func TestGPUModeService_Heartbeat_ControllerErrorPropagates(t *testing.T) {
	wantErr := errors.New("gpu-control unreachable")
	s := newTestGPUModeService(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}}, &fakeGPUModeController{heartErr: wantErr})
	if err := s.Heartbeat(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("expected controller error to propagate, got %v", err)
	}
}

func TestGPUModeService_Generate_NotEnabled(t *testing.T) {
	s := newTestGPUModeService(&fakeGPUModeStore{notConfig: true}, &fakeGPUModeController{})
	if _, err := s.Generate(context.Background(), "user1", "chat1", domain.VisionGenerateOptions{Prompt: "a cat"}); !errors.Is(err, ErrGPUModeNotEnabled) {
		t.Fatalf("expected ErrGPUModeNotEnabled, got %v", err)
	}
}

func TestGPUModeService_Generate_StoreErrorPropagates(t *testing.T) {
	wantErr := errors.New("db down")
	s := newTestGPUModeService(&fakeGPUModeStore{err: wantErr}, &fakeGPUModeController{})
	if _, err := s.Generate(context.Background(), "user1", "chat1", domain.VisionGenerateOptions{Prompt: "a cat"}); !errors.Is(err, wantErr) {
		t.Fatalf("expected store error to propagate, got %v", err)
	}
}

func TestGPUModeService_Generate_Success(t *testing.T) {
	s := newTestGPUModeService(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}}, &fakeGPUModeController{generateJobID: "abc-123"})
	id, err := s.Generate(context.Background(), "user1", "chat1", domain.VisionGenerateOptions{Prompt: "a cat"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if id != "abc-123" {
		t.Fatalf("expected abc-123, got %q", id)
	}
}

func TestGPUModeService_Generate_ControllerErrorPropagates(t *testing.T) {
	wantErr := errors.New("gpu-control unreachable")
	s := newTestGPUModeService(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}}, &fakeGPUModeController{generateErr: wantErr})
	if _, err := s.Generate(context.Background(), "user1", "chat1", domain.VisionGenerateOptions{Prompt: "a cat"}); !errors.Is(err, wantErr) {
		t.Fatalf("expected controller error to propagate, got %v", err)
	}
}

func TestGPUModeService_Generate_OptionsPassThrough(t *testing.T) {
	controller := &fakeGPUModeController{generateJobID: "abc-123"}
	s := newTestGPUModeService(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}}, controller)
	opts := domain.VisionGenerateOptions{
		Prompt:          "a cat",
		AspectRatio:     "9:16 (Portrait Widescreen)",
		DurationSeconds: 8,
		Megapixels:      1.2,
		NegativePrompt:  "blurry",
		EnhancePrompt:   true,
	}
	if _, err := s.Generate(context.Background(), "user1", "chat1", opts); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if controller.lastOpts != opts {
		t.Fatalf("expected opts passed through unchanged, got %+v want %+v", controller.lastOpts, opts)
	}
}

func TestGPUModeService_GenerateResult_NotEnabled(t *testing.T) {
	s := newTestGPUModeService(&fakeGPUModeStore{notConfig: true}, &fakeGPUModeController{})
	if _, err := s.GenerateResult(context.Background(), "abc-123"); !errors.Is(err, ErrGPUModeNotEnabled) {
		t.Fatalf("expected ErrGPUModeNotEnabled, got %v", err)
	}
}

func TestGPUModeService_GenerateResult_StoreErrorPropagates(t *testing.T) {
	wantErr := errors.New("db down")
	s := newTestGPUModeService(&fakeGPUModeStore{err: wantErr}, &fakeGPUModeController{})
	if _, err := s.GenerateResult(context.Background(), "abc-123"); !errors.Is(err, wantErr) {
		t.Fatalf("expected store error to propagate, got %v", err)
	}
}

func TestGPUModeService_GenerateResult_Success(t *testing.T) {
	s := newTestGPUModeService(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}},
		&fakeGPUModeController{generateResult: domain.GPUGenerateResult{Status: "done", ViewURL: "/gpu/api/view?filename=out.mp4"}})
	result, err := s.GenerateResult(context.Background(), "abc-123")
	if err != nil {
		t.Fatalf("GenerateResult: %v", err)
	}
	if result.Status != "done" || result.ViewURL == "" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestGPUModeService_GenerateResult_ControllerErrorPropagates(t *testing.T) {
	wantErr := errors.New("gpu-control unreachable")
	s := newTestGPUModeService(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}}, &fakeGPUModeController{generateResErr: wantErr})
	if _, err := s.GenerateResult(context.Background(), "abc-123"); !errors.Is(err, wantErr) {
		t.Fatalf("expected controller error to propagate, got %v", err)
	}
}

func TestGPUModeService_GenerateResult_SavesGeneratedFileOnce(t *testing.T) {
	controller := &fakeGPUModeController{
		generateJobID:  "abc-123",
		generateResult: domain.GPUGenerateResult{Status: "done", ViewURL: "/gpu/api/view?filename=out.mp4"},
		viewContent:    "video/mp4",
		viewBody:       io.NopCloser(strings.NewReader("video-bytes")),
	}
	files := &fakeFileStore{}
	s := newTestGPUModeServiceWithFiles(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}}, controller, files)

	jobID, err := s.Generate(context.Background(), "user1", "chat1", domain.VisionGenerateOptions{Prompt: "a cat"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	result, err := s.GenerateResult(context.Background(), jobID)
	if err != nil {
		t.Fatalf("GenerateResult: %v", err)
	}
	if result.FileID == "" {
		t.Fatalf("expected a non-empty FileID, got %+v", result)
	}
	if len(files.saved) != 1 {
		t.Fatalf("expected exactly one saved file, got %d", len(files.saved))
	}
	if files.saved[0].OwnerUserID != "user1" || files.saved[0].ChatID != "chat1" {
		t.Fatalf("expected the file saved under user1/chat1, got %+v", files.saved[0])
	}
	if files.saved[0].ContentType != "video/mp4" {
		t.Fatalf("expected content type video/mp4, got %q", files.saved[0].ContentType)
	}
	if string(files.savedData[0]) != "video-bytes" {
		t.Fatalf("expected the fetched bytes saved, got %q", files.savedData[0])
	}

	// A second poll of the same already-done job must not save again.
	result2, err := s.GenerateResult(context.Background(), jobID)
	if err != nil {
		t.Fatalf("GenerateResult (second poll): %v", err)
	}
	if result2.FileID != "" {
		t.Fatalf("expected an empty FileID on the second poll, got %q", result2.FileID)
	}
	if len(files.saved) != 1 {
		t.Fatalf("expected still exactly one saved file after a second poll, got %d", len(files.saved))
	}
}

func TestGPUModeService_GenerateResult_SkipsSaveWhenChatIDEmpty(t *testing.T) {
	controller := &fakeGPUModeController{
		generateJobID:  "abc-123",
		generateResult: domain.GPUGenerateResult{Status: "done", ViewURL: "/gpu/api/view?filename=out.mp4"},
	}
	files := &fakeFileStore{}
	s := newTestGPUModeServiceWithFiles(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}}, controller, files)

	jobID, err := s.Generate(context.Background(), "user1", "", domain.VisionGenerateOptions{Prompt: "a cat"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	result, err := s.GenerateResult(context.Background(), jobID)
	if err != nil {
		t.Fatalf("GenerateResult: %v", err)
	}
	if result.FileID != "" {
		t.Fatalf("expected an empty FileID with no chat id, got %q", result.FileID)
	}
	if len(files.saved) != 0 {
		t.Fatalf("expected no saved file, got %d", len(files.saved))
	}
}

func TestGPUModeService_GenerateResult_SkipsSaveWhenFilesNil(t *testing.T) {
	controller := &fakeGPUModeController{
		generateJobID:  "abc-123",
		generateResult: domain.GPUGenerateResult{Status: "done", ViewURL: "/gpu/api/view?filename=out.mp4"},
	}
	s := newTestGPUModeService(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}}, controller)

	jobID, err := s.Generate(context.Background(), "user1", "chat1", domain.VisionGenerateOptions{Prompt: "a cat"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	result, err := s.GenerateResult(context.Background(), jobID)
	if err != nil {
		t.Fatalf("GenerateResult: %v", err)
	}
	if result.FileID != "" {
		t.Fatalf("expected an empty FileID with no FileStore wired, got %q", result.FileID)
	}
}

func TestGPUModeService_GenerateResult_SaveFailureDoesNotFailRequest(t *testing.T) {
	controller := &fakeGPUModeController{
		generateJobID:  "abc-123",
		generateResult: domain.GPUGenerateResult{Status: "done", ViewURL: "/gpu/api/view?filename=out.mp4"},
		viewContent:    "video/mp4",
		viewBody:       io.NopCloser(strings.NewReader("video-bytes")),
	}
	files := &fakeFileStore{saveErr: errors.New("disk full")}
	s := newTestGPUModeServiceWithFiles(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}}, controller, files)

	jobID, err := s.Generate(context.Background(), "user1", "chat1", domain.VisionGenerateOptions{Prompt: "a cat"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	result, err := s.GenerateResult(context.Background(), jobID)
	if err != nil {
		t.Fatalf("expected GenerateResult to still succeed despite the save failure, got %v", err)
	}
	if result.Status != "done" {
		t.Fatalf("expected status done, got %q", result.Status)
	}
	if result.FileID != "" {
		t.Fatalf("expected an empty FileID after a save failure, got %q", result.FileID)
	}
}

func TestGPUModeService_GenerateResult_SaveSkipsWhenViewAssetFails(t *testing.T) {
	controller := &fakeGPUModeController{
		generateJobID:  "abc-123",
		generateResult: domain.GPUGenerateResult{Status: "done", ViewURL: "/gpu/api/view?filename=out.mp4"},
		viewErr:        errors.New("unreachable"),
	}
	files := &fakeFileStore{}
	s := newTestGPUModeServiceWithFiles(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}}, controller, files)

	jobID, err := s.Generate(context.Background(), "user1", "chat1", domain.VisionGenerateOptions{Prompt: "a cat"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	result, err := s.GenerateResult(context.Background(), jobID)
	if err != nil {
		t.Fatalf("expected GenerateResult to still succeed, got %v", err)
	}
	if result.FileID != "" {
		t.Fatalf("expected an empty FileID when fetching the asset fails, got %q", result.FileID)
	}
	if len(files.saved) != 0 {
		t.Fatalf("expected no saved file, got %d", len(files.saved))
	}
}

// erroringReadCloser mirrors httpgpumode's own test helper -- exercises
// "reading asset" without a real fault.
type erroringReadCloser struct{}

func (erroringReadCloser) Read(p []byte) (int, error) { return 0, errors.New("simulated read failure") }
func (erroringReadCloser) Close() error               { return nil }

func TestGPUModeService_GenerateResult_SaveSkipsWhenReadingAssetFails(t *testing.T) {
	controller := &fakeGPUModeController{
		generateJobID:  "abc-123",
		generateResult: domain.GPUGenerateResult{Status: "done", ViewURL: "/gpu/api/view?filename=out.mp4"},
		viewContent:    "video/mp4",
		viewBody:       erroringReadCloser{},
	}
	files := &fakeFileStore{}
	s := newTestGPUModeServiceWithFiles(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}}, controller, files)

	jobID, err := s.Generate(context.Background(), "user1", "chat1", domain.VisionGenerateOptions{Prompt: "a cat"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	result, err := s.GenerateResult(context.Background(), jobID)
	if err != nil {
		t.Fatalf("expected GenerateResult to still succeed, got %v", err)
	}
	if result.FileID != "" {
		t.Fatalf("expected an empty FileID when reading the asset fails, got %q", result.FileID)
	}
}

func TestGPUModeService_GenerateResult_SaveFallsBackToVideoMP4WhenContentTypeEmpty(t *testing.T) {
	controller := &fakeGPUModeController{
		generateJobID:  "abc-123",
		generateResult: domain.GPUGenerateResult{Status: "done", ViewURL: "/gpu/api/view?filename=out.mp4"},
		viewContent:    "",
		viewBody:       io.NopCloser(strings.NewReader("video-bytes")),
	}
	files := &fakeFileStore{}
	s := newTestGPUModeServiceWithFiles(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}}, controller, files)

	jobID, err := s.Generate(context.Background(), "user1", "chat1", domain.VisionGenerateOptions{Prompt: "a cat"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, err := s.GenerateResult(context.Background(), jobID); err != nil {
		t.Fatalf("GenerateResult: %v", err)
	}
	if len(files.saved) != 1 || files.saved[0].ContentType != "video/mp4" {
		t.Fatalf("expected a fallback content type of video/mp4, got %+v", files.saved)
	}
}

func TestGPUModeService_GenerateResult_UnknownJobIDSkipsSave(t *testing.T) {
	controller := &fakeGPUModeController{
		generateResult: domain.GPUGenerateResult{Status: "done", ViewURL: "/gpu/api/view?filename=out.mp4"},
	}
	files := &fakeFileStore{}
	s := newTestGPUModeServiceWithFiles(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}}, controller, files)

	// Never called Generate for this job id -- GenerateResult must not
	// panic or save anything.
	result, err := s.GenerateResult(context.Background(), "never-submitted")
	if err != nil {
		t.Fatalf("GenerateResult: %v", err)
	}
	if result.FileID != "" {
		t.Fatalf("expected an empty FileID for an unknown job id, got %q", result.FileID)
	}
	if len(files.saved) != 0 {
		t.Fatalf("expected no saved file, got %d", len(files.saved))
	}
}

func TestGPUModeService_ViewAsset_NotEnabled(t *testing.T) {
	s := newTestGPUModeService(&fakeGPUModeStore{notConfig: true}, &fakeGPUModeController{})
	if _, _, err := s.ViewAsset(context.Background(), "/gpu/api/view?filename=out.mp4"); !errors.Is(err, ErrGPUModeNotEnabled) {
		t.Fatalf("expected ErrGPUModeNotEnabled, got %v", err)
	}
}

func TestGPUModeService_ViewAsset_StoreErrorPropagates(t *testing.T) {
	wantErr := errors.New("db down")
	s := newTestGPUModeService(&fakeGPUModeStore{err: wantErr}, &fakeGPUModeController{})
	if _, _, err := s.ViewAsset(context.Background(), "/gpu/api/view?filename=out.mp4"); !errors.Is(err, wantErr) {
		t.Fatalf("expected store error to propagate, got %v", err)
	}
}

func TestGPUModeService_ViewAsset_Success(t *testing.T) {
	body := io.NopCloser(strings.NewReader("video-bytes"))
	s := newTestGPUModeService(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}},
		&fakeGPUModeController{viewContent: "video/mp4", viewBody: body})
	ct, gotBody, err := s.ViewAsset(context.Background(), "/gpu/api/view?filename=out.mp4")
	if err != nil {
		t.Fatalf("ViewAsset: %v", err)
	}
	defer gotBody.Close()
	if ct != "video/mp4" {
		t.Fatalf("expected video/mp4, got %q", ct)
	}
}

func TestGPUModeService_ViewAsset_ControllerErrorPropagates(t *testing.T) {
	wantErr := errors.New("gpu-control unreachable")
	s := newTestGPUModeService(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}}, &fakeGPUModeController{viewErr: wantErr})
	if _, _, err := s.ViewAsset(context.Background(), "/gpu/api/view?filename=out.mp4"); !errors.Is(err, wantErr) {
		t.Fatalf("expected controller error to propagate, got %v", err)
	}
}
