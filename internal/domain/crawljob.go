package domain

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrCrawlJobNotFound is returned by CrawlJobStore.Get when no job with the
// given ID exists. A separate sentinel from ports.ErrCrawlJobNotFound since
// domain can't import ports.
var ErrCrawlJobNotFound = errors.New("crawl job not found")

// ErrCrawlAlreadyActiveForSeed is returned when starting a crawl for a seed
// URL some other Queued/Running job is already crawling -- the same seed
// must never run twice at once, regardless of how the triggers overlapped.
var ErrCrawlAlreadyActiveForSeed = errors.New("a crawl is already active for this seed")

// CrawlJobStatus is where a triggered crawl currently stands.
type CrawlJobStatus string

const (
	CrawlJobQueued    CrawlJobStatus = "queued"
	CrawlJobRunning   CrawlJobStatus = "running"
	CrawlJobDone      CrawlJobStatus = "done"
	CrawlJobFailed    CrawlJobStatus = "failed"
	CrawlJobCancelled CrawlJobStatus = "cancelled"
)

// CrawlPageStatus is the outcome of one URL a crawl job attempted.
type CrawlPageStatus string

const (
	CrawlPageIndexed          CrawlPageStatus = "indexed"
	CrawlPageThinContent      CrawlPageStatus = "thin_content"
	CrawlPageRobotsDisallowed CrawlPageStatus = "robots_disallowed"
	CrawlPageFetchFailed      CrawlPageStatus = "fetch_failed"
	// CrawlPageAliased marks a page whose <link rel="canonical"> points
	// elsewhere -- its links are still followed, but it gets no document
	// row, so unlike CrawlPageIndexed it doesn't advance PagesCrawled.
	CrawlPageAliased CrawlPageStatus = "aliased"
)

// CrawlPageEvent reports what happened to one URL a crawl job visited.
// DocLength/LinksFound are zero unless the page was actually parsed;
// DurationMs is zero for a robots-disallowed URL (no fetch attempted).
type CrawlPageEvent struct {
	URL        string          `json:"url"`
	Status     CrawlPageStatus `json:"status"`
	Title      string          `json:"title,omitempty"`
	Error      string          `json:"error,omitempty"`
	DocLength  int             `json:"doc_length,omitempty"`
	LinksFound int             `json:"links_found,omitempty"`
	DurationMs int64           `json:"duration_ms,omitempty"`
	FetchedAt  time.Time       `json:"fetched_at"`
}

// CrawlJobRequest is a redacted summary of the request that started a job:
// HasCookie/HasBasicAuth are booleans, not the actual credentials, so
// secrets never appear in a job listing. Other fields aren't secrets and
// are carried through as-is.
type CrawlJobRequest struct {
	SeedURLs      []string `json:"seed_urls"`
	MaxPages      int      `json:"max_pages"`
	HasCookie     bool     `json:"has_cookie"`
	HasBasicAuth  bool     `json:"has_basic_auth"`
	RespectRobots bool     `json:"respect_robots"`
	UserAgent     string   `json:"user_agent,omitempty"`
	// LinkScope is "" when this job used the Tuning page's global default,
	// or an explicit override otherwise.
	LinkScope string `json:"link_scope,omitempty"`
	// AllowedDomains/BlockedDomains/FollowIndexedDomains: see
	// ports.CrawlOptions' doc comment for allow/block precedence vs LinkScope.
	AllowedDomains       []string `json:"allowed_domains,omitempty"`
	BlockedDomains       []string `json:"blocked_domains,omitempty"`
	FollowIndexedDomains bool     `json:"follow_indexed_domains,omitempty"`
	UseSitemap           bool     `json:"use_sitemap"`
	// FetchTimeoutSeconds/MinTextLength/CrawlDelayMs/MaxResponseKB are 0
	// when this job used the global operational default, non-zero when overridden.
	FetchTimeoutSeconds int  `json:"fetch_timeout_seconds,omitempty"`
	MinTextLength       int  `json:"min_text_length,omitempty"`
	CrawlDelayMs        int  `json:"crawl_delay_ms,omitempty"`
	MaxResponseKB       int  `json:"max_response_kb,omitempty"`
	PrioritizeUnindexed bool `json:"prioritize_unindexed,omitempty"`
	// Renderer is "" for the Tuning page's global default rendering mode,
	// or an explicit override ("none"/"chromium"/"firefox") otherwise.
	Renderer string `json:"renderer,omitempty"`
}

// CrawlJob is one triggered crawl's full state, including every page event
// seen so far.
type CrawlJob struct {
	ID           string           `json:"id"`
	Request      CrawlJobRequest  `json:"request"`
	Status       CrawlJobStatus   `json:"status"`
	PagesCrawled int              `json:"pages_crawled"`
	Pages        []CrawlPageEvent `json:"pages"`
	Error        string           `json:"error,omitempty"`
	CreatedAt    time.Time        `json:"created_at"`
	StartedAt    *time.Time       `json:"started_at,omitempty"`
	FinishedAt   *time.Time       `json:"finished_at,omitempty"`
}

// CrawlJobSummary is the lightweight view used for a jobs list -- every
// CrawlJob field except its full per-page event log.
type CrawlJobSummary struct {
	ID           string          `json:"id"`
	Request      CrawlJobRequest `json:"request"`
	Status       CrawlJobStatus  `json:"status"`
	PagesCrawled int             `json:"pages_crawled"`
	Error        string          `json:"error,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
	StartedAt    *time.Time      `json:"started_at,omitempty"`
	FinishedAt   *time.Time      `json:"finished_at,omitempty"`
}

func (j CrawlJob) summary() CrawlJobSummary {
	return CrawlJobSummary{
		ID: j.ID, Request: j.Request, Status: j.Status,
		PagesCrawled: j.PagesCrawled, Error: j.Error,
		CreatedAt: j.CreatedAt, StartedAt: j.StartedAt, FinishedAt: j.FinishedAt,
	}
}

// maxRetainedCrawlJobs bounds the store's memory use on a long-running
// crawl-server process -- the oldest job is dropped once the limit is
// exceeded, same tradeoff as the admin document/vocabulary list limits.
const maxRetainedCrawlJobs = 200

// CrawlJobStore holds every crawl job triggered on this process, safe for
// concurrent use. Purely in-memory (lost on restart) -- a lightweight
// ports.CrawlJobStore for tests; crawl-server uses sqlrepo's DB-backed
// implementation instead.
type CrawlJobStore struct {
	mu    sync.RWMutex
	jobs  map[string]*CrawlJob
	order []string
}

func NewCrawlJobStore() *CrawlJobStore {
	return &CrawlJobStore{jobs: make(map[string]*CrawlJob)}
}

var crawlJobSeq int64

// NewCrawlJobID mints a job ID, unique within a process without a database
// round-trip -- shared by this in-memory store and sqlrepo's persistent one.
func NewCrawlJobID() string {
	return newSeqID("job", &crawlJobSeq)
}

// Create registers a new job in CrawlJobQueued status and returns it.
func (s *CrawlJobStore) Create(_ context.Context, req CrawlJobRequest) (CrawlJob, error) {
	job := &CrawlJob{ID: NewCrawlJobID(), Request: req, Status: CrawlJobQueued, CreatedAt: time.Now()}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[job.ID] = job
	s.order = append(s.order, job.ID)
	if len(s.order) > maxRetainedCrawlJobs {
		delete(s.jobs, s.order[0])
		s.order = s.order[1:]
	}
	return *job, nil
}

func (s *CrawlJobStore) MarkRunning(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.jobs[id]; ok {
		j.Status = CrawlJobRunning
		now := time.Now()
		j.StartedAt = &now
	}
	return nil
}

// AppendPage records one page's outcome and, for an indexed page, advances
// PagesCrawled.
func (s *CrawlJobStore) AppendPage(_ context.Context, id string, ev CrawlPageEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return nil
	}
	j.Pages = append(j.Pages, ev)
	if ev.Status == CrawlPageIndexed {
		j.PagesCrawled++
	}
	return nil
}

func (s *CrawlJobStore) MarkDone(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.jobs[id]; ok {
		j.Status = CrawlJobDone
		now := time.Now()
		j.FinishedAt = &now
	}
	return nil
}

func (s *CrawlJobStore) MarkFailed(_ context.Context, id string, failErr error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.jobs[id]; ok {
		j.Status = CrawlJobFailed
		j.Error = failErr.Error()
		now := time.Now()
		j.FinishedAt = &now
	}
	return nil
}

// MarkCancelled records an admin-stopped job, distinct from MarkFailed so
// the UI can tell a cancellation from an actual error.
func (s *CrawlJobStore) MarkCancelled(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.jobs[id]; ok {
		j.Status = CrawlJobCancelled
		now := time.Now()
		j.FinishedAt = &now
	}
	return nil
}

// Get returns a snapshot (Pages copied) so a concurrent AppendPage never
// races. Returns ErrCrawlJobNotFound if id isn't retained.
func (s *CrawlJobStore) Get(_ context.Context, id string) (CrawlJob, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	j, ok := s.jobs[id]
	if !ok {
		return CrawlJob{}, ErrCrawlJobNotFound
	}
	cp := *j
	cp.Pages = append([]CrawlPageEvent{}, j.Pages...)
	return cp, nil
}

// List returns every retained job's summary, most recently created first.
func (s *CrawlJobStore) List(_ context.Context) ([]CrawlJobSummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]CrawlJobSummary, 0, len(s.order))
	for i := len(s.order) - 1; i >= 0; i-- {
		out = append(out, s.jobs[s.order[i]].summary())
	}
	return out, nil
}

// ListActive returns every crawl job currently Queued or Running, same
// contract as sqlrepo's DB-backed equivalent.
func (s *CrawlJobStore) ListActive(_ context.Context) ([]CrawlJobSummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []CrawlJobSummary
	for i := len(s.order) - 1; i >= 0; i-- {
		j := s.jobs[s.order[i]]
		if !IsEndedCrawlJobStatus(j.Status) {
			out = append(out, j.summary())
		}
	}
	return out, nil
}

// IsEndedCrawlJobStatus reports whether a job has finished (as opposed to
// still-active Queued/Running) -- shared with sqlrepo's DB-backed equivalent.
func IsEndedCrawlJobStatus(status CrawlJobStatus) bool {
	return status == CrawlJobDone || status == CrawlJobFailed || status == CrawlJobCancelled
}

// DeleteEndedCrawlJobs removes every done/failed/cancelled job, keeping
// queued/running ones, and returns how many were removed.
func (s *CrawlJobStore) DeleteEndedCrawlJobs(_ context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.order[:0]
	removed := 0
	for _, id := range s.order {
		if IsEndedCrawlJobStatus(s.jobs[id].Status) {
			delete(s.jobs, id)
			removed++
			continue
		}
		kept = append(kept, id)
	}
	s.order = kept
	return removed, nil
}
