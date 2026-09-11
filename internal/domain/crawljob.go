package domain

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// CrawlJobStatus is where a triggered crawl currently stands.
type CrawlJobStatus string

const (
	CrawlJobQueued  CrawlJobStatus = "queued"
	CrawlJobRunning CrawlJobStatus = "running"
	CrawlJobDone    CrawlJobStatus = "done"
	CrawlJobFailed  CrawlJobStatus = "failed"
)

// CrawlPageStatus is the outcome of one URL a crawl job attempted.
type CrawlPageStatus string

const (
	CrawlPageIndexed          CrawlPageStatus = "indexed"
	CrawlPageThinContent      CrawlPageStatus = "thin_content"
	CrawlPageRobotsDisallowed CrawlPageStatus = "robots_disallowed"
	CrawlPageFetchFailed      CrawlPageStatus = "fetch_failed"
)

// CrawlPageEvent reports what happened to one URL a crawl job visited.
type CrawlPageEvent struct {
	URL    string          `json:"url"`
	Status CrawlPageStatus `json:"status"`
	Title  string          `json:"title,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// CrawlJobRequest is a redacted summary of the request that started a job:
// it deliberately carries HasCookie/HasBasicAuth booleans rather than the
// actual credentials, so a crawl's secrets never appear in a job listing
// or detail view. RespectRobots and UserAgent aren't secrets, so they're
// carried through as-is.
type CrawlJobRequest struct {
	SeedURLs      []string `json:"seed_urls"`
	MaxPages      int      `json:"max_pages"`
	HasCookie     bool     `json:"has_cookie"`
	HasBasicAuth  bool     `json:"has_basic_auth"`
	RespectRobots bool     `json:"respect_robots"`
	UserAgent     string   `json:"user_agent,omitempty"`
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
// concurrent use by the HTTP handler goroutine that creates a job and the
// background goroutine that runs it and reports progress.
type CrawlJobStore struct {
	mu    sync.RWMutex
	jobs  map[string]*CrawlJob
	order []string
}

func NewCrawlJobStore() *CrawlJobStore {
	return &CrawlJobStore{jobs: make(map[string]*CrawlJob)}
}

var crawlJobSeq int64

func newCrawlJobID() string {
	return fmt.Sprintf("job-%d-%d", time.Now().UnixNano(), atomic.AddInt64(&crawlJobSeq, 1))
}

// Create registers a new job in CrawlJobQueued status and returns it.
func (s *CrawlJobStore) Create(req CrawlJobRequest) *CrawlJob {
	job := &CrawlJob{ID: newCrawlJobID(), Request: req, Status: CrawlJobQueued, CreatedAt: time.Now()}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[job.ID] = job
	s.order = append(s.order, job.ID)
	if len(s.order) > maxRetainedCrawlJobs {
		delete(s.jobs, s.order[0])
		s.order = s.order[1:]
	}
	return job
}

func (s *CrawlJobStore) MarkRunning(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.jobs[id]; ok {
		j.Status = CrawlJobRunning
		now := time.Now()
		j.StartedAt = &now
	}
}

// AppendPage records one page's outcome and, for an indexed page, advances
// PagesCrawled.
func (s *CrawlJobStore) AppendPage(id string, ev CrawlPageEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return
	}
	j.Pages = append(j.Pages, ev)
	if ev.Status == CrawlPageIndexed {
		j.PagesCrawled++
	}
}

func (s *CrawlJobStore) MarkDone(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.jobs[id]; ok {
		j.Status = CrawlJobDone
		now := time.Now()
		j.FinishedAt = &now
	}
}

func (s *CrawlJobStore) MarkFailed(id string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.jobs[id]; ok {
		j.Status = CrawlJobFailed
		j.Error = err.Error()
		now := time.Now()
		j.FinishedAt = &now
	}
}

// Get returns a snapshot of the job (its Pages slice copied) so a caller
// reading it concurrently with AppendPage never races or sees a slice that
// mutates under it.
func (s *CrawlJobStore) Get(id string) (CrawlJob, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	j, ok := s.jobs[id]
	if !ok {
		return CrawlJob{}, false
	}
	cp := *j
	cp.Pages = append([]CrawlPageEvent{}, j.Pages...)
	return cp, true
}

// List returns every retained job's summary, most recently created first.
func (s *CrawlJobStore) List() []CrawlJobSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]CrawlJobSummary, 0, len(s.order))
	for i := len(s.order) - 1; i >= 0; i-- {
		out = append(out, s.jobs[s.order[i]].summary())
	}
	return out
}
