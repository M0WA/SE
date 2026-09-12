package domain

import "math"

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
// An empty adjacency (no nodes at all) returns an empty map. A node with
// outdegree 0 contributes nothing to any other node's score (it simply
// isn't -- and can't be -- any other node's incoming link), matching the
// formula literally rather than redistributing its mass across the graph.
func PageRank(adjacency map[string][]string) map[string]float64 {
	nodes := make(map[string]bool)
	for from, tos := range adjacency {
		nodes[from] = true
		for _, to := range tos {
			nodes[to] = true
		}
	}
	n := len(nodes)
	if n == 0 {
		return map[string]float64{}
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

	for iter := 0; iter < PageRankMaxIterations; iter++ {
		next := make(map[string]float64, n)
		delta := 0.0
		for node := range nodes {
			sum := 0.0
			for _, u := range incoming[node] {
				if outdegree[u] > 0 {
					sum += scores[u] / float64(outdegree[u])
				}
			}
			v := base + PageRankDamping*sum
			next[node] = v
			delta += math.Abs(v - scores[node])
		}
		scores = next
		if delta < PageRankEpsilon {
			break
		}
	}
	return scores
}
