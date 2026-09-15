package domain

import (
	"math"
	"time"
)

// PageRankDamping is the classic PageRank damping factor d: the
// probability mass a page passes along its outbound links, versus (1-d)
// distributed evenly across every page regardless of link structure.
const PageRankDamping = 0.85

// PageRankMaxIterations bounds how many rounds PageRank ever runs, so a
// pathological graph (or one that oscillates rather than settling) can't
// loop forever -- convergence usually happens well before this in practice.
const PageRankMaxIterations = 50

// PageRankEpsilon is the convergence threshold: once the sum of every
// node's absolute score change from one iteration to the next drops below
// this, the scores are considered settled and iteration stops early.
const PageRankEpsilon = 1e-6

// PageRankOrphanThreshold is the pagerank value below which a document is
// treated as an "orphan" -- functionally unlinked -- for the admin
// Overview page's orphan-rate stat tile. Chosen to equal PageRankEpsilon,
// the same "negligible" convergence threshold PageRank already uses
// elsewhere: a score this close to zero only happens once PageRank has
// actually run and found the page has no real incoming link weight, never
// merely because it hasn't been recomputed yet (a never-recomputed
// document instead sits at the neutral 1/N default every document starts
// at -- see sqlrepo's backfillPageRank/SaveDocument -- which is nowhere
// near this small once there's more than a handful of documents).
const PageRankOrphanThreshold = 1e-6

// PageRankHistogramBuckets is how many equal-width buckets
// sqlrepo.Repository.PageRankHistogram divides the corpus's observed
// [min, max] pagerank range into for the admin Overview page's
// distribution histogram.
const PageRankHistogramBuckets = 10

// PageRankRunInfo reports how a PageRank computation actually ran -- how
// many iterations it took, how far the final iteration still was from full
// convergence, and how long the run took wall-clock. None of these are
// observable from the returned scores alone, and all are useful
// diagnostics for the admin PageRank debug page after a forced recompute.
// DurationMs is set by application.RunPageRankJob (it covers the whole job
// -- reading the link graph, running the algorithm, and writing scores
// back -- not just the in-memory iteration below), not by PageRank itself.
type PageRankRunInfo struct {
	Iterations int     `json:"iterations,omitempty"`
	FinalDelta float64 `json:"final_delta,omitempty"`
	DurationMs int64   `json:"duration_ms,omitempty"`
}

// PageRankStatus is the persisted, cross-process-visible record of the
// last PageRank recompute -- written to a shared SettingsStore key (see
// application.RunPageRankJobWithStatus) so the admin PageRank debug page
// shows whether a recompute triggered by ANY process (the periodic
// ticker, a post-crawl trigger, or an admin's "force recalculation"
// click, possibly from a different browser or a different admin-server
// instance) is currently running, and what the last completed run found
// -- not just whatever this one process/browser happens to remember.
// LastRunAt/Documents/Iterations/FinalDelta describe the last run that
// actually completed; a run currently in progress doesn't touch them
// until it finishes, so a concurrent viewer still sees the previous
// result rather than a blank slate while InProgress is true.
type PageRankStatus struct {
	InProgress bool      `json:"in_progress"`
	LastRunAt  time.Time `json:"last_run_at,omitempty"`
	Documents  int       `json:"documents,omitempty"`
	PageRankRunInfo
}

// PageRank computes classic iterative PageRank scores over a directed
// graph: adjacency maps each node (a document ID) to the IDs of every
// node it links to. A node that only ever appears as a link target (never
// as a key of adjacency, i.e. it has no outbound links of its own) is
// still included in the result -- every node mentioned anywhere in
// adjacency gets a score.
//
// PR(v) = (1-d)/N + d * sum over u linking to v of PR(u)/outdegree(u)
//
// with d = PageRankDamping and N the total node count. Iteration runs for
// up to PageRankMaxIterations rounds, stopping early once the total
// absolute change across every node's score (from the previous round)
// drops below PageRankEpsilon.
//
// An empty adjacency (no nodes at all) returns an empty map and a zero
// PageRankRunInfo (no iteration was needed). A node with outdegree 0
// contributes nothing to any other node's score (it simply isn't -- and
// can't be -- any other node's incoming link), matching the formula
// literally rather than redistributing its mass across the graph.
func PageRank(adjacency map[string][]string) (map[string]float64, PageRankRunInfo) {
	nodes := make(map[string]bool)
	for from, tos := range adjacency {
		nodes[from] = true
		for _, to := range tos {
			nodes[to] = true
		}
	}
	n := len(nodes)
	if n == 0 {
		return map[string]float64{}, PageRankRunInfo{}
	}

	// Build a stable node-ID <-> dense integer index mapping once up
	// front, so the whole iterative computation below can work with
	// plain, index-addressed slices instead of string-keyed maps.
	id := make([]string, n)
	idx := make(map[string]int, n)
	i := 0
	for node := range nodes {
		id[i] = node
		idx[node] = i
		i++
	}

	outdegree := make([]int32, n)
	// incomingOffsets/incomingEdges together form a CSR-style flattened
	// adjacency list: node v's contributors are
	// incomingEdges[incomingOffsets[v]:incomingOffsets[v+1]]. Built with a
	// single counting pass + single fill pass so it's one flat
	// allocation rather than n growing []string slices.
	incomingCount := make([]int32, n)
	for from, tos := range adjacency {
		fi := idx[from]
		outdegree[fi] = int32(len(tos))
		for _, to := range tos {
			incomingCount[idx[to]]++
		}
	}
	incomingOffsets := make([]int32, n+1)
	for v := 0; v < n; v++ {
		incomingOffsets[v+1] = incomingOffsets[v] + incomingCount[v]
	}
	incomingEdges := make([]int32, incomingOffsets[n])
	fillPos := make([]int32, n)
	copy(fillPos, incomingOffsets[:n])
	for from, tos := range adjacency {
		fi := int32(idx[from])
		for _, to := range tos {
			ti := idx[to]
			incomingEdges[fillPos[ti]] = fi
			fillPos[ti]++
		}
	}

	nf := float64(n)
	base := (1 - PageRankDamping) / nf

	init := 1.0 / nf
	scores := make([]float64, n)
	next := make([]float64, n)
	for v := range scores {
		scores[v] = init
	}

	// contribution[u] is what u passes along each of its outbound links
	// this iteration -- computed once per node (not once per incoming
	// edge) since every edge out of u carries the identical
	// scores[u]/outdegree[u] share.
	contribution := make([]float64, n)
	info := PageRankRunInfo{}
	for iter := 0; iter < PageRankMaxIterations; iter++ {
		for u := 0; u < n; u++ {
			if deg := outdegree[u]; deg > 0 {
				contribution[u] = scores[u] / float64(deg)
			}
		}
		delta := 0.0
		for v := 0; v < n; v++ {
			sum := 0.0
			for _, u := range incomingEdges[incomingOffsets[v]:incomingOffsets[v+1]] {
				sum += contribution[u]
			}
			val := base + PageRankDamping*sum
			next[v] = val
			delta += math.Abs(val - scores[v])
		}
		scores, next = next, scores
		info.Iterations = iter + 1
		info.FinalDelta = delta
		if delta < PageRankEpsilon {
			break
		}
	}

	result := make(map[string]float64, n)
	for v := 0; v < n; v++ {
		result[id[v]] = scores[v]
	}
	return result, info
}
