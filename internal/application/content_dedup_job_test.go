package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"testing"
	"time"

	"searchengine/internal/application"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

type mergeCall struct {
	canonicalID string
	loserIDs    []string
	reason      string
}

type fakeContentDedupRepo struct {
	fingerprints    []domain.DocumentFingerprint
	fingerprintsErr error
	merges          []mergeCall
	mergeErr        error
	// lockBusy simulates another process already holding the lock --
	// zero-value false (the default every existing test implicitly relies
	// on) means TryAcquireContentDedupLock succeeds.
	lockBusy     bool
	acquireErr   error
	releaseErr   error
	acquireCalls int
	releaseCalls int
}

func (r *fakeContentDedupRepo) TryAcquireContentDedupLock(context.Context) (bool, error) {
	r.acquireCalls++
	if r.acquireErr != nil {
		return false, r.acquireErr
	}
	return !r.lockBusy, nil
}

func (r *fakeContentDedupRepo) ReleaseContentDedupLock(context.Context) error {
	r.releaseCalls++
	return r.releaseErr
}

func (r *fakeContentDedupRepo) AllDocumentFingerprints(context.Context) ([]domain.DocumentFingerprint, error) {
	if r.fingerprintsErr != nil {
		return nil, r.fingerprintsErr
	}
	return r.fingerprints, nil
}

func (r *fakeContentDedupRepo) MergeDocuments(_ context.Context, canonicalID string, loserIDs []string, reason string) error {
	if r.mergeErr != nil {
		return r.mergeErr
	}
	sorted := append([]string(nil), loserIDs...)
	sort.Strings(sorted)
	r.merges = append(r.merges, mergeCall{canonicalID: canonicalID, loserIDs: sorted, reason: reason})
	return nil
}

func TestRunContentDedupJob_ExactMethodGroupsIdenticalHashesOnly(t *testing.T) {
	repo := &fakeContentDedupRepo{fingerprints: []domain.DocumentFingerprint{
		{ID: "a", URL: "http://a.example", Host: "a.example", ContentHash: "same"},
		{ID: "b", URL: "http://b.example", Host: "b.example", ContentHash: "same"},
		{ID: "c", URL: "http://c.example", Host: "c.example", ContentHash: "different"},
	}}
	result, err := application.RunContentDedupJob(context.Background(), repo, domain.ContentDedupMethodExact, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.GroupsFound != 1 || result.DocumentsMerged != 1 {
		t.Fatalf("expected exactly 1 group merging 1 loser, got %+v", result)
	}
	if len(repo.merges) != 1 {
		t.Fatalf("expected exactly 1 MergeDocuments call, got %d", len(repo.merges))
	}
	m := repo.merges[0]
	if len(m.loserIDs) != 1 || (m.loserIDs[0] != "a" && m.loserIDs[0] != "b") {
		t.Errorf("expected one of a/b merged as the loser, got %+v", m)
	}
	if m.canonicalID != "a" && m.canonicalID != "b" {
		t.Errorf("expected a or b as canonical, got %q", m.canonicalID)
	}
	if m.reason != domain.DocumentAliasReasonContentExact {
		t.Errorf("expected reason=content_exact, got %q", m.reason)
	}
	// c (a distinct hash) must never be merged.
	for _, loser := range m.loserIDs {
		if loser == "c" {
			t.Error("expected the distinct-hash document never merged")
		}
	}
}

func TestRunContentDedupJob_ExactMethodIgnoresEmptyHash(t *testing.T) {
	repo := &fakeContentDedupRepo{fingerprints: []domain.DocumentFingerprint{
		{ID: "a", URL: "http://a.example", ContentHash: ""},
		{ID: "b", URL: "http://b.example", ContentHash: ""},
	}}
	result, err := application.RunContentDedupJob(context.Background(), repo, domain.ContentDedupMethodExact, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.GroupsFound != 0 {
		t.Errorf("expected two not-yet-fingerprinted (empty hash) documents never grouped together, got %+v", result)
	}
}

func TestRunContentDedupJob_ExactMethodSingletonHashNeverMerges(t *testing.T) {
	repo := &fakeContentDedupRepo{fingerprints: []domain.DocumentFingerprint{
		{ID: "a", URL: "http://a.example", ContentHash: "unique"},
	}}
	result, err := application.RunContentDedupJob(context.Background(), repo, domain.ContentDedupMethodExact, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.GroupsFound != 0 || len(repo.merges) != 0 {
		t.Errorf("expected a single document with no duplicates never merged, got %+v", result)
	}
}

func TestRunContentDedupJob_SimHashMethodMergesWithinThreshold(t *testing.T) {
	a := domain.EncodeSimHash64(0x0000000000000000)
	b := domain.EncodeSimHash64(0x0000000000000001) // Hamming distance 1 from a
	repo := &fakeContentDedupRepo{fingerprints: []domain.DocumentFingerprint{
		{ID: "a", URL: "http://a.example", Host: "a.example", SimHash: a},
		{ID: "b", URL: "http://b.example", Host: "b.example", SimHash: b},
	}}
	result, err := application.RunContentDedupJob(context.Background(), repo, domain.ContentDedupMethodSimHash, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.GroupsFound != 1 || result.DocumentsMerged != 1 {
		t.Fatalf("expected a and b (distance 1, threshold 3) merged into one group, got %+v", result)
	}
	if repo.merges[0].reason != domain.DocumentAliasReasonContentSimHash {
		t.Errorf("expected reason=content_simhash, got %q", repo.merges[0].reason)
	}
}

func TestRunContentDedupJob_SimHashMethodDoesNotMergeAboveThreshold(t *testing.T) {
	a := domain.EncodeSimHash64(0x0000000000000000)
	b := domain.EncodeSimHash64(0xFFFFFFFFFFFFFFFF) // Hamming distance 64 from a
	repo := &fakeContentDedupRepo{fingerprints: []domain.DocumentFingerprint{
		{ID: "a", URL: "http://a.example", SimHash: a},
		{ID: "b", URL: "http://b.example", SimHash: b},
	}}
	result, err := application.RunContentDedupJob(context.Background(), repo, domain.ContentDedupMethodSimHash, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.GroupsFound != 0 || len(repo.merges) != 0 {
		t.Errorf("expected documents far apart in Hamming distance never merged, got %+v", result)
	}
}

// TestRunContentDedupJob_SimHashMethodRespectsExactBoundaryDistance proves
// the threshold comparison is inclusive (<=), not exclusive.
func TestRunContentDedupJob_SimHashMethodRespectsExactBoundaryDistance(t *testing.T) {
	a := domain.EncodeSimHash64(0x0000000000000000)
	b := domain.EncodeSimHash64(0x0000000000000007) // exactly 3 bits set -- distance 3
	repo := &fakeContentDedupRepo{fingerprints: []domain.DocumentFingerprint{
		{ID: "a", URL: "http://a.example", SimHash: a},
		{ID: "b", URL: "http://b.example", SimHash: b},
	}}
	result, err := application.RunContentDedupJob(context.Background(), repo, domain.ContentDedupMethodSimHash, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.GroupsFound != 1 {
		t.Errorf("expected a distance exactly equal to the threshold to still merge, got %+v", result)
	}
}

// TestRunContentDedupJob_SimHashMethodDetectsDifferenceInHighestBand
// proves the banding shift logic is correct up through the top band (bits
// 48-63) -- a bug in the band index/shift arithmetic would only show up
// for a difference confined to the highest band, not the lowest one every
// other test above exercises.
func TestRunContentDedupJob_SimHashMethodDetectsDifferenceInHighestBand(t *testing.T) {
	a := domain.EncodeSimHash64(0x0000000000000000)
	b := domain.EncodeSimHash64(0x0001000000000000) // a single bit set in the top 16-bit band
	repo := &fakeContentDedupRepo{fingerprints: []domain.DocumentFingerprint{
		{ID: "a", URL: "http://a.example", SimHash: a},
		{ID: "b", URL: "http://b.example", SimHash: b},
	}}
	result, err := application.RunContentDedupJob(context.Background(), repo, domain.ContentDedupMethodSimHash, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.GroupsFound != 1 {
		t.Errorf("expected a single-bit difference confined to the top band still merged (distance 1), got %+v", result)
	}
}

func TestRunContentDedupJob_ChoosesShortestHostAsCanonical(t *testing.T) {
	repo := &fakeContentDedupRepo{fingerprints: []domain.DocumentFingerprint{
		{ID: "long", URL: "https://www.example.com/x", Host: "www.example.com", ContentHash: "same"},
		{ID: "short", URL: "https://example.com/x", Host: "example.com", ContentHash: "same"},
	}}
	result, err := application.RunContentDedupJob(context.Background(), repo, domain.ContentDedupMethodExact, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.GroupsFound != 1 {
		t.Fatalf("expected one group, got %+v", result)
	}
	if repo.merges[0].canonicalID != "short" {
		t.Errorf("expected the bare (shorter, www.-stripped) host to win as canonical, got %q", repo.merges[0].canonicalID)
	}
}

func TestRunContentDedupJob_TieBreaksByEarliestCrawledAt(t *testing.T) {
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	repo := &fakeContentDedupRepo{fingerprints: []domain.DocumentFingerprint{
		{ID: "newer", URL: "https://aaaaa.example/x", Host: "aaaaa.example", ContentHash: "same", CrawledAt: newer},
		{ID: "older", URL: "https://bbbbb.example/x", Host: "bbbbb.example", ContentHash: "same", CrawledAt: older},
	}}
	result, err := application.RunContentDedupJob(context.Background(), repo, domain.ContentDedupMethodExact, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.GroupsFound != 1 {
		t.Fatalf("expected one group, got %+v", result)
	}
	if repo.merges[0].canonicalID != "older" {
		t.Errorf("expected the earliest-crawled document to win the equal-length-host tie-break, got %q", repo.merges[0].canonicalID)
	}
}

func TestRunContentDedupJob_MergesGroupsLargerThanTwo(t *testing.T) {
	repo := &fakeContentDedupRepo{fingerprints: []domain.DocumentFingerprint{
		{ID: "a", URL: "http://a.example", Host: "a.example", ContentHash: "same"},
		{ID: "b", URL: "http://b.example", Host: "b.example", ContentHash: "same"},
		{ID: "c", URL: "http://c.example", Host: "c.example", ContentHash: "same"},
	}}
	result, err := application.RunContentDedupJob(context.Background(), repo, domain.ContentDedupMethodExact, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.GroupsFound != 1 || result.DocumentsMerged != 2 {
		t.Errorf("expected one group of 3 merging 2 losers, got %+v", result)
	}
}

func TestRunContentDedupJob_RecordsDuration(t *testing.T) {
	repo := &fakeContentDedupRepo{}
	result, err := application.RunContentDedupJob(context.Background(), repo, domain.ContentDedupMethodExact, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.DurationMs < 0 {
		t.Errorf("expected a non-negative DurationMs, got %d", result.DurationMs)
	}
}

func TestRunContentDedupJob_PropagatesFingerprintsError(t *testing.T) {
	wantErr := errors.New("boom")
	repo := &fakeContentDedupRepo{fingerprintsErr: wantErr}
	if _, err := application.RunContentDedupJob(context.Background(), repo, domain.ContentDedupMethodExact, 3); !errors.Is(err, wantErr) {
		t.Errorf("expected AllDocumentFingerprints error to propagate, got %v", err)
	}
}

func TestRunContentDedupJob_PropagatesMergeError(t *testing.T) {
	wantErr := errors.New("merge failed")
	repo := &fakeContentDedupRepo{
		fingerprints: []domain.DocumentFingerprint{
			{ID: "a", ContentHash: "same"},
			{ID: "b", ContentHash: "same"},
		},
		mergeErr: wantErr,
	}
	if _, err := application.RunContentDedupJob(context.Background(), repo, domain.ContentDedupMethodExact, 3); !errors.Is(err, wantErr) {
		t.Errorf("expected MergeDocuments error to propagate, got %v", err)
	}
}

func TestRunContentDedupJobWithStatus_RecordsCompletedRun(t *testing.T) {
	repo := &fakeContentDedupRepo{fingerprints: []domain.DocumentFingerprint{
		{ID: "a", ContentHash: "same"},
		{ID: "b", ContentHash: "same"},
	}}
	settings := newFakeSettingsStore()

	result, err := application.RunContentDedupJobWithStatus(context.Background(), repo, settings, domain.ContentDedupMethodExact, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	status := application.LoadContentDedupStatus(context.Background(), settings)
	if status.InProgress {
		t.Error("expected InProgress false once the run has finished")
	}
	if status.LastRunAt.IsZero() {
		t.Error("expected LastRunAt to be set")
	}
	if status.GroupsFound != result.GroupsFound || status.DocumentsMerged != result.DocumentsMerged || status.DurationMs != result.DurationMs {
		t.Errorf("expected persisted status to match the run result, got %+v want %+v", status, result)
	}
}

func TestRunContentDedupJobWithStatus_SetsInProgressBeforeRunning(t *testing.T) {
	repo := &fakeContentDedupRepo{}
	settings := newFakeSettingsStore()

	if _, err := application.RunContentDedupJobWithStatus(context.Background(), repo, settings, domain.ContentDedupMethodExact, 3); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(settings.saveCalls) < 2 {
		t.Fatalf("expected at least 2 saves (in-progress, then completed), got %d", len(settings.saveCalls))
	}
	var firstSave domain.ContentDedupStatus
	if err := json.Unmarshal([]byte(settings.saveCalls[0]), &firstSave); err != nil {
		t.Fatalf("decoding first save: %v", err)
	}
	if !firstSave.InProgress {
		t.Error("expected the first persisted status to have InProgress true")
	}
}

func TestRunContentDedupJobWithStatus_ErrorClearsInProgressButKeepsLastResult(t *testing.T) {
	repo := &fakeContentDedupRepo{fingerprints: []domain.DocumentFingerprint{
		{ID: "a", ContentHash: "same"},
		{ID: "b", ContentHash: "same"},
	}}
	settings := newFakeSettingsStore()
	if _, err := application.RunContentDedupJobWithStatus(context.Background(), repo, settings, domain.ContentDedupMethodExact, 3); err != nil {
		t.Fatalf("unexpected error on first (successful) run: %v", err)
	}
	successStatus := application.LoadContentDedupStatus(context.Background(), settings)

	repo.fingerprintsErr = errors.New("db unavailable")
	if _, err := application.RunContentDedupJobWithStatus(context.Background(), repo, settings, domain.ContentDedupMethodExact, 3); err == nil {
		t.Fatal("expected the second run's error to propagate")
	}

	status := application.LoadContentDedupStatus(context.Background(), settings)
	if status.InProgress {
		t.Error("expected InProgress false after a failed run")
	}
	if !status.LastRunAt.Equal(successStatus.LastRunAt) || status.GroupsFound != successStatus.GroupsFound {
		t.Errorf("expected the last successful run's result preserved after a failure, got %+v want %+v", status, successStatus)
	}
}

func TestRunContentDedupJobWithStatus_NilSettingsStoreIsANoop(t *testing.T) {
	repo := &fakeContentDedupRepo{}
	if _, err := application.RunContentDedupJobWithStatus(context.Background(), repo, nil, domain.ContentDedupMethodExact, 3); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestRunContentDedupJobWithStatus_AlreadyRunningSkipsEntirely proves the
// atomic lock, not just the display-only InProgress flag, gates a run: when
// another process already holds it, this call must not touch
// AllDocumentFingerprints/MergeDocuments at all, and must not perturb the
// persisted status (a concurrent run's own status writes are what's
// authoritative).
func TestRunContentDedupJobWithStatus_AlreadyRunningSkipsEntirely(t *testing.T) {
	repo := &fakeContentDedupRepo{
		lockBusy: true,
		fingerprints: []domain.DocumentFingerprint{
			{ID: "a", ContentHash: "same"},
			{ID: "b", ContentHash: "same"},
		},
	}
	settings := newFakeSettingsStore()

	_, err := application.RunContentDedupJobWithStatus(context.Background(), repo, settings, domain.ContentDedupMethodExact, 3)
	if !errors.Is(err, ports.ErrContentDedupAlreadyRunning) {
		t.Fatalf("expected ErrContentDedupAlreadyRunning, got %v", err)
	}
	if len(repo.merges) != 0 {
		t.Errorf("expected no merges when the lock is already held, got %v", repo.merges)
	}
	if len(settings.saveCalls) != 0 {
		t.Errorf("expected no status writes when the lock is already held, got %d", len(settings.saveCalls))
	}
	if repo.releaseCalls != 0 {
		t.Errorf("expected no release call for a lock this call never acquired, got %d", repo.releaseCalls)
	}
}

// TestRunContentDedupJobWithStatus_ReleasesLockOnSuccess proves the lock is
// always released after a run, not just leaked until the next restart.
func TestRunContentDedupJobWithStatus_ReleasesLockOnSuccess(t *testing.T) {
	repo := &fakeContentDedupRepo{}
	settings := newFakeSettingsStore()

	if _, err := application.RunContentDedupJobWithStatus(context.Background(), repo, settings, domain.ContentDedupMethodExact, 3); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.acquireCalls != 1 {
		t.Errorf("expected exactly one acquire attempt, got %d", repo.acquireCalls)
	}
	if repo.releaseCalls != 1 {
		t.Errorf("expected the lock to be released after a successful run, got %d release calls", repo.releaseCalls)
	}
}

// TestRunContentDedupJobWithStatus_ReleasesLockOnError proves the lock is
// released even when the run itself fails, so one failed run can't wedge
// every future run behind a lock nothing will ever clear.
func TestRunContentDedupJobWithStatus_ReleasesLockOnError(t *testing.T) {
	repo := &fakeContentDedupRepo{fingerprintsErr: errors.New("db unavailable")}
	settings := newFakeSettingsStore()

	if _, err := application.RunContentDedupJobWithStatus(context.Background(), repo, settings, domain.ContentDedupMethodExact, 3); err == nil {
		t.Fatal("expected an error to propagate")
	}
	if repo.releaseCalls != 1 {
		t.Errorf("expected the lock to be released after a failed run, got %d release calls", repo.releaseCalls)
	}
}

func TestRunContentDedupJobWithStatus_AcquireLockErrorPropagates(t *testing.T) {
	repo := &fakeContentDedupRepo{acquireErr: errors.New("db unavailable")}
	settings := newFakeSettingsStore()

	_, err := application.RunContentDedupJobWithStatus(context.Background(), repo, settings, domain.ContentDedupMethodExact, 3)
	if err == nil {
		t.Fatal("expected the lock acquisition error to propagate")
	}
	if repo.releaseCalls != 0 {
		t.Errorf("expected no release call for a lock this call never acquired, got %d", repo.releaseCalls)
	}
}

// TestRunContentDedupJobWithStatus_ReleaseErrorDoesNotFailTheRun proves a
// failure to release the lock afterward (logged, per RunContentDedupJobWithStatus's
// deferred cleanup) never turns an otherwise-successful run into an error --
// the lock row will simply time out or be cleared by the next process that
// notices, not something worth failing the caller's own result over.
func TestRunContentDedupJobWithStatus_ReleaseErrorDoesNotFailTheRun(t *testing.T) {
	repo := &fakeContentDedupRepo{releaseErr: errors.New("db unavailable")}
	settings := newFakeSettingsStore()

	if _, err := application.RunContentDedupJobWithStatus(context.Background(), repo, settings, domain.ContentDedupMethodExact, 3); err != nil {
		t.Fatalf("expected a release error not to fail the run, got %v", err)
	}
}

func TestLoadContentDedupStatus_NilSettingsStoreReturnsZeroValue(t *testing.T) {
	status := application.LoadContentDedupStatus(context.Background(), nil)
	if status.InProgress || !status.LastRunAt.IsZero() {
		t.Errorf("expected the zero value, got %+v", status)
	}
}

func TestLoadContentDedupStatus_StoreErrorReturnsZeroValue(t *testing.T) {
	settings := newFakeSettingsStore()
	settings.getErr = errors.New("db unavailable")
	status := application.LoadContentDedupStatus(context.Background(), settings)
	if status.InProgress || !status.LastRunAt.IsZero() {
		t.Errorf("expected the zero value on a store error, got %+v", status)
	}
}

func TestLoadContentDedupStatus_UndecodableValueReturnsZeroValue(t *testing.T) {
	settings := newFakeSettingsStore()
	settings.values[ports.SettingsKeyContentDedupStatus] = "not json"
	status := application.LoadContentDedupStatus(context.Background(), settings)
	if status.InProgress || !status.LastRunAt.IsZero() {
		t.Errorf("expected the zero value on an undecodable value, got %+v", status)
	}
}
