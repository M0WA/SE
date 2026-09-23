package domain

// CrawlJobOutcomeCount is how many crawl jobs finished in a given terminal
// status within a lookback window -- the admin Overview page's
// crawl-outcome donut. A still queued/running job is never one of these.
type CrawlJobOutcomeCount struct {
	Status CrawlJobStatus
	Count  int
}

// DailyCount is one day's count for a day-bucketed series (e.g. documents
// indexed per day). Date is "YYYY-MM-DD" (UTC), the first 10 characters of
// the RFC3339Nano text every *_at column stores.
type DailyCount struct {
	Date  string
	Count int
}

// DailyFetchOutcome is one (day, outcome) count from crawl_job_pages -- the
// admin Overview page's fetch-outcome-breakdown stacked bar. Several share
// a Date, one per CrawlPageStatus actually seen that day.
type DailyFetchOutcome struct {
	Date   string
	Status CrawlPageStatus
	Count  int
}

// DailyAvgDuration is one day's mean crawl_job_pages.duration_ms -- the
// admin Overview page's fetch-duration trend. A day with no fetches has no
// entry, never a zero-filled gap.
type DailyAvgDuration struct {
	Date          string
	AvgDurationMs float64
}

// PageRankBucket is a count of documents whose pagerank falls in one
// labeled "lo–hi" range -- the Overview page's PageRank histogram.
type PageRankBucket struct {
	Label string
	Count int
}
