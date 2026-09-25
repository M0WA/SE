package domain

import (
	"errors"
	"time"
)

// ErrDocumentJobNotFound is returned by DocumentJobStore.Get/GetData when
// no job with the given ID exists.
var ErrDocumentJobNotFound = errors.New("document job not found")

// DocumentJobStatus is where an admin-triggered Document-upload job
// currently stands. Reuses the same vocabulary as CrawlJobStatus, minus
// Cancelled -- a Document job is a single, largely-synchronous unit of
// work with nothing meaningful to cancel mid-flight.
type DocumentJobStatus string

const (
	DocumentJobQueued  DocumentJobStatus = "queued"
	DocumentJobRunning DocumentJobStatus = "running"
	DocumentJobDone    DocumentJobStatus = "done"
	DocumentJobFailed  DocumentJobStatus = "failed"
)

// DocumentJobSource records how a Document job's bytes were obtained --
// shown on its detail page, purely informational (doesn't change how the
// job is processed).
type DocumentJobSource string

const (
	DocumentJobSourceUpload DocumentJobSource = "upload"
	DocumentJobSourceS3     DocumentJobSource = "s3"
)

// DocumentJob is one admin-triggered "index this file" job: unlike a
// CrawlJob (many pages, a link graph), it's always exactly one file,
// indexed with no PageRank (the resulting Document's Links is always nil)
// and, for text content, an optional vocabulary/BM25-postings toggle. The
// job's raw bytes are stored alongside it (see DocumentJobStore.GetData)
// so an image can be previewed and text content re-extracted without
// re-uploading.
type DocumentJob struct {
	ID              string            `json:"id"`
	Filename        string            `json:"filename"`
	ContentType     string            `json:"content_type"`
	Size            int64             `json:"size"`
	Source          DocumentJobSource `json:"source"`
	IndexVocabulary bool              `json:"index_vocabulary"`
	Status          DocumentJobStatus `json:"status"`
	// DocID is the resulting documents.id once indexed -- empty until
	// Status is DocumentJobDone.
	DocID      string     `json:"doc_id,omitempty"`
	Error      string     `json:"error,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

var documentJobSeq int64

// NewDocumentJobID mints a job ID, unique within a process without a
// database round-trip -- same convention as NewCrawlJobID.
func NewDocumentJobID() string {
	return newSeqID("docjob", &documentJobSeq)
}
