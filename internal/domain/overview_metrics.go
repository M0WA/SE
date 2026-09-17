package domain

// CrawlJobOutcomeCount is how many crawl jobs finished in a given terminal
// status (done/failed/cancelled) within a lookback window -- the admin
// Overview page's crawl-outcome donut. A job still queued or running has no
// terminal outcome yet, so it's never one of these.
type CrawlJobOutcomeCount struct {
	Status CrawlJobStatus
	Count  int
}

// DailyCount is one day's count for a day-bucketed series (e.g. documents
// indexed per day). Date is "YYYY-MM-DD" (UTC) -- the first 10 characters
// of the RFC3339Nano text every *_at column already stores, bucketed on
// directly rather than parsed into a native date type.
type DailyCount struct {
	Date  string
	Count int
}

// DailyFetchOutcome is one (day, outcome) count from crawl_job_pages -- the
// admin Overview page's throughput/fetch-outcome-breakdown stacked bar.
// Several of these share a Date, one per CrawlPageStatus actually seen that
// day (a status with zero occurrences on a given day simply has no row).
type DailyFetchOutcome struct {
	Date   string
	Status CrawlPageStatus
	Count  int
}

// DailyAvgDuration is one day's mean crawl_job_pages.duration_ms -- the
// admin Overview page's fetch-duration trend. A day with no fetches simply
// has no entry -- never a zero-filled gap -- so a trend line only ever
// connects days that actually had crawl activity.
type DailyAvgDuration struct {
	Date          string
	AvgDurationMs float64
}

// PageRankBucket is a count of documents whose pagerank falls in one
// labeled range -- the Overview page's PageRank histogram. Label is a
// pre-formatted "lo–hi" range, same convention as AgeBucket/VersionCount.
type PageRankBucket struct {
	Label string
	Count int
}
