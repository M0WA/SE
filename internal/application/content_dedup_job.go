package application

import (
	"context"
	"log"
	"time"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// ContentDedupRunResult reports what a RunContentDedupJob call actually did
// -- surfaced by the admin content-dedup page after a forced recompute,
// where an admin explicitly wants to see the result of the run they just
// triggered, not just whether it errored.
type ContentDedupRunResult struct {
	// GroupsFound is how many distinct groups of duplicate/near-duplicate
	// documents were found and merged into one canonical document each.
	GroupsFound int
	// DocumentsMerged is the total number of non-canonical (loser)
	// documents removed across every group.
	DocumentsMerged int
	DurationMs      int64
	// Merges reports each group's canonical document and its merged alias
	// URLs, capped at maxReportedMerges -- only bounds what's reported
	// here, not what's actually merged. The full listing is
	// ports.AdminRepository.ListDocumentAliasGroups.
	Merges []MergeRecord
}

// MergeRecord is one group RunContentDedupJob merged in a single run --
// see ContentDedupRunResult.Merges.
type MergeRecord struct {
	CanonicalID  string
	CanonicalURL string
	AliasURLs    []string
	Reason       string
}

// maxReportedMerges bounds ContentDedupRunResult.Merges -- a safety valve
// against an unusually large single run's result ballooning the admin
// API's response body, not a limit on how many groups are actually merged.
const maxReportedMerges = 200

// simHashBandBits/simHashBandCount split each 64-bit SimHash64 fingerprint
// into 4 non-overlapping 16-bit bands for LSH-style bucketing (see
// groupBySimHash) -- two documents within a realistic Hamming distance are
// guaranteed to share a band, so only band-colliding documents are ever
// pairwise-compared, avoiding a full O(n^2) scan.
const (
	simHashBandBits  = 16
	simHashBandCount = 4
)

// maxBandBucketSize caps how many documents within one band bucket are
// ever pairwise-compared. A real near-duplicate corpus never gets close to
// this in practice; a bucket that does hit it is logged and skipped rather
// than compared at O(n^2) cost -- an accepted, documented simplification,
// not a fully rigorous LSH scheme.
const maxBandBucketSize = 2000

// RunContentDedupJob scans every document's fingerprint, groups duplicates
// per method ("exact": ContentHash match; "simhash": SimHash64 within
// maxSimHashDistance), and merges each group via MergeDocuments. Called by
// cmd/crawl's ticker, after each crawl completes, and on demand. Idempotent:
// merged losers no longer exist as rows, so a later run never re-sees them.
func RunContentDedupJob(ctx context.Context, repo ports.ContentDedupRepository, method string, maxSimHashDistance int) (ContentDedupRunResult, error) {
	start := time.Now()
	fingerprints, err := repo.AllDocumentFingerprints(ctx)
	if err != nil {
		return ContentDedupRunResult{}, err
	}

	reason := domain.DocumentAliasReasonContentExact
	groups := groupByExactHash(fingerprints)
	if method == domain.ContentDedupMethodSimHash {
		reason = domain.DocumentAliasReasonContentSimHash
		groups = groupBySimHash(fingerprints, maxSimHashDistance)
	}

	var result ContentDedupRunResult
	for _, group := range groups {
		if len(group) < 2 {
			continue
		}
		canonical := chooseCanonical(group)
		loserIDs := make([]string, 0, len(group)-1)
		aliasURLs := make([]string, 0, len(group)-1)
		for _, f := range group {
			if f.ID == canonical.ID {
				continue
			}
			loserIDs = append(loserIDs, f.ID)
			aliasURLs = append(aliasURLs, f.URL)
		}
		if len(loserIDs) == 0 {
			continue
		}
		if err := repo.MergeDocuments(ctx, canonical.ID, loserIDs, reason); err != nil {
			return ContentDedupRunResult{}, err
		}
		result.GroupsFound++
		result.DocumentsMerged += len(loserIDs)
		if len(result.Merges) < maxReportedMerges {
			result.Merges = append(result.Merges, MergeRecord{
				CanonicalID: canonical.ID, CanonicalURL: canonical.URL,
				AliasURLs: aliasURLs, Reason: reason,
			})
		}
	}
	result.DurationMs = time.Since(start).Milliseconds()
	return result, nil
}

// groupByExactHash groups fingerprints sharing an identical, non-empty
// ContentHash -- O(n). A fingerprint with an empty ContentHash (a document
// row saved before this feature existed, not yet caught up by the
// migration backfill) is skipped rather than grouped with every other
// empty-hash row.
// groupsOfAtLeastTwo returns only byKey's groups with more than one member
// -- a single fingerprint sharing its key with nothing else isn't a
// duplicate. Shared by groupByExactHash (string keys) and groupBySimHash
// (int union-find root keys) below.
func groupsOfAtLeastTwo[K comparable](byKey map[K][]domain.DocumentFingerprint) [][]domain.DocumentFingerprint {
	groups := make([][]domain.DocumentFingerprint, 0, len(byKey))
	for _, g := range byKey {
		if len(g) > 1 {
			groups = append(groups, g)
		}
	}
	return groups
}

func groupByExactHash(fingerprints []domain.DocumentFingerprint) [][]domain.DocumentFingerprint {
	byHash := make(map[string][]domain.DocumentFingerprint)
	for _, f := range fingerprints {
		if f.ContentHash == "" {
			continue
		}
		byHash[f.ContentHash] = append(byHash[f.ContentHash], f)
	}
	return groupsOfAtLeastTwo(byHash)
}

// groupBySimHash finds near-duplicate groups via 4-band LSH bucketing plus
// union-find over pairs within maxSimHashDistance -- a document can join a
// group via a chain of pairwise-close documents, not just direct distance
// to every member; an accepted simplification for clustering, not a bug.
func groupBySimHash(fingerprints []domain.DocumentFingerprint, maxDistance int) [][]domain.DocumentFingerprint {
	parent := make([]int, len(fingerprints))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}

	type bandKey struct {
		band  int
		value uint16
	}
	buckets := make(map[bandKey][]int)
	hashes := make([]uint64, len(fingerprints))
	for i, f := range fingerprints {
		if f.SimHash == "" {
			continue
		}
		h := domain.DecodeSimHash64(f.SimHash)
		hashes[i] = h
		for band := 0; band < simHashBandCount; band++ {
			value := uint16(h >> uint(band*simHashBandBits))
			key := bandKey{band, value}
			buckets[key] = append(buckets[key], i)
		}
	}

	for _, idxs := range buckets {
		if len(idxs) < 2 {
			continue
		}
		if len(idxs) > maxBandBucketSize {
			log.Printf("content dedup: band bucket with %d documents exceeds %d, skipping pairwise comparison for it", len(idxs), maxBandBucketSize)
			continue
		}
		for i := 0; i < len(idxs); i++ {
			for j := i + 1; j < len(idxs); j++ {
				a, b := idxs[i], idxs[j]
				if domain.HammingDistance64(hashes[a], hashes[b]) <= maxDistance {
					union(a, b)
				}
			}
		}
	}

	byRoot := make(map[int][]domain.DocumentFingerprint)
	for i, f := range fingerprints {
		if f.SimHash == "" {
			continue
		}
		root := find(i)
		byRoot[root] = append(byRoot[root], f)
	}
	return groupsOfAtLeastTwo(byRoot)
}

// chooseCanonical picks which document in a duplicate group survives:
// shortest host wins. A www-vs-bare pair of the *same* domain is already
// folded into one document before either is saved, so this really compares
// genuinely different hosts (e.g. a mirror) with identical content. Ties
// (equal length) fall back to whichever was crawled first.
func chooseCanonical(group []domain.DocumentFingerprint) domain.DocumentFingerprint {
	best := group[0]
	for _, f := range group[1:] {
		switch {
		case len(f.Host) < len(best.Host):
			best = f
		case len(f.Host) == len(best.Host) && f.CrawledAt.Before(best.CrawledAt):
			best = f
		}
	}
	return best
}

// RunContentDedupJobWithStatus wraps RunContentDedupJob, persisting a
// domain.ContentDedupStatus so any process's admin page can show whether a
// run (triggered by anything, anywhere) is in progress and what the last
// one found. settings may be nil (bookkeeping then skipped). On error,
// InProgress clears but the last successful run's fields are left as-is.
//
// The status.InProgress bookkeeping above is display-only and, on its own,
// race-prone (a plain read-then-write, not atomic) -- the actual mutual
// exclusion is repo.TryAcquireContentDedupLock, a real conditional DB
// UPDATE every process contends for. Returns
// ports.ErrContentDedupAlreadyRunning, without touching status at all, if
// this call loses that race -- production once had cmd/crawl's own
// scheduler and the admin-server's "recompute now" button run this
// concurrently, each merging from its own snapshot of
// AllDocumentFingerprints, which left a document_aliases row pointing at a
// canonical the other run's transaction had already deleted (see
// ports.ContentDedupRepository's doc comment).
func RunContentDedupJobWithStatus(ctx context.Context, repo ports.ContentDedupRepository, settings ports.SettingsStore, method string, maxSimHashDistance int) (ContentDedupRunResult, error) {
	acquired, err := repo.TryAcquireContentDedupLock(ctx)
	if err != nil {
		return ContentDedupRunResult{}, err
	}
	if !acquired {
		return ContentDedupRunResult{}, ports.ErrContentDedupAlreadyRunning
	}
	defer func() {
		if releaseErr := repo.ReleaseContentDedupLock(ctx); releaseErr != nil {
			log.Printf("releasing content dedup lock: %v", releaseErr)
		}
	}()

	status := LoadContentDedupStatus(ctx, settings)
	status.InProgress = true
	saveContentDedupStatus(ctx, settings, status)

	result, err := RunContentDedupJob(ctx, repo, method, maxSimHashDistance)

	status.InProgress = false
	if err == nil {
		status.LastRunAt = time.Now().UTC()
		status.GroupsFound = result.GroupsFound
		status.DocumentsMerged = result.DocumentsMerged
		status.DurationMs = result.DurationMs
	}
	saveContentDedupStatus(ctx, settings, status)
	return result, err
}

// LoadContentDedupStatus reads the persisted status back -- used
// internally and by the admin GET handler.
func LoadContentDedupStatus(ctx context.Context, settings ports.SettingsStore) domain.ContentDedupStatus {
	return loadJSONStatus[domain.ContentDedupStatus](ctx, settings, ports.SettingsKeyContentDedupStatus, "content dedup status")
}

func saveContentDedupStatus(ctx context.Context, settings ports.SettingsStore, status domain.ContentDedupStatus) {
	saveJSONStatus(ctx, settings, ports.SettingsKeyContentDedupStatus, "content dedup status", status)
}
