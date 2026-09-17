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
// treated as an "orphan" (functionally unlinked) for the admin Overview
// page. Equals PageRankEpsilon: this small only happens once PageRank has
// actually run and found no real incoming link weight, not merely because
// it hasn't been recomputed yet (a never-recomputed doc sits at 1/N).
const PageRankOrphanThreshold = 1e-6

// PageRankHistogramBuckets is how many equal-width buckets
// sqlrepo.Repository.PageRankHistogram divides the corpus's observed
// [min, max] pagerank range into for the admin Overview page's
// distribution histogram.
const PageRankHistogramBuckets = 10

// PageRankRunInfo reports how a PageRank computation ran (iterations
// taken, distance from full convergence, wall-clock duration) -- useful
// diagnostics for the admin PageRank debug page. DurationMs is set by
// application.RunPageRankJob (covers the whole job, not just the
// in-memory iteration below), not by PageRank itself.
type PageRankRunInfo struct {
	Iterations int     `json:"iterations,omitempty"`
	FinalDelta float64 `json:"final_delta,omitempty"`
	DurationMs int64   `json:"duration_ms,omitempty"`
}

// PageRankStatus is the persisted, cross-process-visible record of the
// last PageRank recompute, so the admin debug page shows whether any
// process is currently recomputing, and what the last run found. An
// in-progress run leaves the other fields untouched, so a viewer sees
// the previous result rather than a blank slate.
type PageRankStatus struct {
	InProgress bool      `json:"in_progress"`
	LastRunAt  time.Time `json:"last_run_at,omitempty"`
	Documents  int       `json:"documents,omitempty"`
	PageRankRunInfo
}

// PageRank computes classic iterative PageRank over a directed graph:
// adjacency maps a node (document ID) to the IDs it links to. Every node
// mentioned anywhere gets a score, even a link-target-only node.
// PR(v) = (1-d)/N + d * sum over u linking to v of PR(u)/outdegree(u)
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
