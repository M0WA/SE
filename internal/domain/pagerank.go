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

// PageRankRunInfo reports how a PageRank computation actually ran --
// how many iterations it took and how far the final iteration still was
// from full convergence. Neither number is observable from the returned
// scores alone, and both are useful diagnostics for the admin PageRank
// debug page after a forced recompute.
type PageRankRunInfo struct {
	Iterations int     `json:"iterations,omitempty"`
	FinalDelta float64 `json:"final_delta,omitempty"`
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

	outdegree := make(map[string]int, len(adjacency))
	// incoming[v] lists every u with an edge u->v, so each iteration looks
	// up a node's contributors directly rather than rescanning every edge.
	incoming := make(map[string][]string, len(adjacency))
	for from, tos := range adjacency {
		outdegree[from] = len(tos)
		for _, to := range tos {
			incoming[to] = append(incoming[to], from)
		}
	}

	nf := float64(n)
	base := (1 - PageRankDamping) / nf

	scores := make(map[string]float64, n)
	init := 1.0 / nf
	for node := range nodes {
		scores[node] = init
	}

	// contribution[u] is what u passes along each of its outbound links
	// this iteration -- computed once per node (not once per incoming
	// edge) since every edge out of u carries the identical
	// scores[u]/outdegree[u] share.
	contribution := make(map[string]float64, len(outdegree))
	info := PageRankRunInfo{}
	for iter := 0; iter < PageRankMaxIterations; iter++ {
		for u, deg := range outdegree {
			if deg > 0 {
				contribution[u] = scores[u] / float64(deg)
			}
		}
		next := make(map[string]float64, n)
		delta := 0.0
		for node := range nodes {
			sum := 0.0
			for _, u := range incoming[node] {
				sum += contribution[u]
			}
			v := base + PageRankDamping*sum
			next[node] = v
			delta += math.Abs(v - scores[node])
		}
		scores = next
		info.Iterations = iter + 1
		info.FinalDelta = delta
		if delta < PageRankEpsilon {
			break
		}
	}
	return scores, info
}
