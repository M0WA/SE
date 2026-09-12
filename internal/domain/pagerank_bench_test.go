package domain_test

import (
	"fmt"
	"math/rand"
	"testing"

	"searchengine/internal/domain"
)

// buildSyntheticLinkGraph generates a synthetic directed link graph over n
// documents, each linking to avgOutDegree other randomly chosen documents
// (a realistic average out-degree for crawled web pages -- most pages carry
// a handful to a few dozen outbound links), deterministically seeded so
// runs are comparable. Mirrors the shape RunPageRankJob's repo.LinkGraph
// returns: a map from document ID to the IDs it links to.
func buildSyntheticLinkGraph(n, avgOutDegree int) map[string][]string {
	rng := rand.New(rand.NewSource(7))
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("doc-%d", i)
	}
	graph := make(map[string][]string, n)
	for i, id := range ids {
		// Vary out-degree +/-50% around the average, like real pages do,
		// rather than every page linking to exactly the same fixed count.
		degree := avgOutDegree/2 + rng.Intn(avgOutDegree+1)
		if degree > n-1 {
			degree = n - 1
		}
		links := make([]string, 0, degree)
		seen := map[int]bool{i: true} // never link to self
		for len(links) < degree {
			j := rng.Intn(n)
			if seen[j] {
				continue
			}
			seen[j] = true
			links = append(links, ids[j])
		}
		graph[id] = links
	}
	return graph
}

// BenchmarkPageRank characterizes how the periodic recompute job
// (RunPageRankJob, called on cmd/crawl's ticker and after every completed
// crawl) scales: convergence time and memory (via b.ReportAllocs) on
// synthetic graphs of 1,000 and 20,000 documents with a realistic average
// out-degree of 15 outbound links per page. PageRank itself runs to
// convergence (or PageRankMaxIterations, whichever comes first) inside a
// single call, so this benchmark's per-op time already *is* the
// end-to-end convergence time for a graph of that size.
func BenchmarkPageRank(b *testing.B) {
	const avgOutDegree = 15

	for _, n := range []int{1000, 20000} {
		graph := buildSyntheticLinkGraph(n, avgOutDegree)
		b.Run(fmt.Sprintf("Docs=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				scores := domain.PageRank(graph)
				if len(scores) != n {
					b.Fatalf("expected %d scored nodes, got %d", n, len(scores))
				}
			}
		})
	}
}
