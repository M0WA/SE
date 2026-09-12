package domain_test

import (
	"math"
	"testing"

	"searchengine/internal/domain"
)

func almostEqual(a, b, tol float64) bool {
	return math.Abs(a-b) < tol
}

func TestPageRank_Empty(t *testing.T) {
	scores := domain.PageRank(map[string][]string{})
	if len(scores) != 0 {
		t.Errorf("expected empty result for empty graph, got %+v", scores)
	}
}

// TestPageRank_ThreeNodeCycle verifies the textbook symmetric case: A->B->C->A
// with nothing breaking the symmetry should converge to (almost) equal
// scores for every node, each close to 1/3.
func TestPageRank_ThreeNodeCycle(t *testing.T) {
	adjacency := map[string][]string{
		"a": {"b"},
		"b": {"c"},
		"c": {"a"},
	}
	scores := domain.PageRank(adjacency)
	if len(scores) != 3 {
		t.Fatalf("expected 3 scored nodes, got %d: %+v", len(scores), scores)
	}
	want := 1.0 / 3.0
	for _, node := range []string{"a", "b", "c"} {
		if !almostEqual(scores[node], want, 1e-4) {
			t.Errorf("node %s: expected score close to %f, got %f", node, want, scores[node])
		}
	}
	// Total probability mass should sum to ~1 (N * average of 1/N).
	sum := scores["a"] + scores["b"] + scores["c"]
	if !almostEqual(sum, 1.0, 1e-3) {
		t.Errorf("expected scores to sum to ~1, got %f", sum)
	}
}

// TestPageRank_WellLinkedNodeScoresHigherThanIsolated builds a small graph
// where several established, well-linked nodes all point at "hub", while
// "isolated" has no incoming or outgoing links at all -- hub should end up
// with a clearly higher score.
func TestPageRank_WellLinkedNodeScoresHigherThanIsolated(t *testing.T) {
	adjacency := map[string][]string{
		"p1":       {"hub"},
		"p2":       {"hub"},
		"p3":       {"hub"},
		"hub":      {"p1"},
		"isolated": {},
	}
	scores := domain.PageRank(adjacency)
	for _, node := range []string{"p1", "p2", "p3", "hub", "isolated"} {
		if scores[node] <= 0 {
			t.Errorf("expected node %s to have a positive score, got %f", node, scores[node])
		}
	}
	if scores["hub"] <= scores["isolated"] {
		t.Errorf("expected hub (%f), linked to by three other pages, to score higher than isolated (%f), which has no links at all", scores["hub"], scores["isolated"])
	}
	if scores["hub"] <= scores["p1"] {
		t.Errorf("expected hub (%f), which receives links from p1, p2 and p3, to score higher than p1 (%f), which only receives a link from hub", scores["hub"], scores["p1"])
	}
}

// TestPageRank_DanglingNodeGetsNoIncomingContribution confirms a node with
// no outbound links simply never appears as anyone's contributor -- it
// still gets a score (the base (1-d)/N term, plus whatever incoming links
// it has), but never distributes any of its own score onward.
func TestPageRank_DanglingNodeGetsNoIncomingContribution(t *testing.T) {
	adjacency := map[string][]string{
		"a": {"sink"},
		// "sink" has no outgoing links of its own.
	}
	scores := domain.PageRank(adjacency)
	if len(scores) != 2 {
		t.Fatalf("expected 2 nodes (a, sink), got %+v", scores)
	}
	// a has no incoming links, so it should sit at the base score.
	base := (1 - domain.PageRankDamping) / 2
	if !almostEqual(scores["a"], base, 1e-6) {
		t.Errorf("expected a's score to stay at the base %f (no incoming links), got %f", base, scores["a"])
	}
	// sink receives all of a's score via a's one outbound edge.
	if scores["sink"] <= scores["a"] {
		t.Errorf("expected sink (%f) to outscore a (%f), since a links to it", scores["sink"], scores["a"])
	}
}

// TestPageRank_ConvergesWithinMaxIterations sanity-checks that a
// moderately connected graph settles rather than oscillating forever --
// running PageRank twice on the same input should be deterministic.
func TestPageRank_Deterministic(t *testing.T) {
	adjacency := map[string][]string{
		"a": {"b", "c"},
		"b": {"c"},
		"c": {"a", "b"},
		"d": {"a"},
	}
	first := domain.PageRank(adjacency)
	second := domain.PageRank(adjacency)
	for node, v := range first {
		if second[node] != v {
			t.Errorf("expected deterministic output for node %s: %f vs %f", node, v, second[node])
		}
	}
}
