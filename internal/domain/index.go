package domain

import (
	"math"
	"sort"
	"strings"
	"sync"
)

// InvertedIndex ist die zentrale Suchstruktur: Term -> DocID -> Häufigkeit.
// Thread-safe durch internes Mutex.
type InvertedIndex struct {
	mu         sync.RWMutex
	postings   map[string]map[string]int // term -> docID -> Häufigkeit
	docLengths map[string]int            // docID -> Token-Anzahl
	docs       map[string]Document       // docID -> Dokument
}

func NewInvertedIndex() *InvertedIndex {
	return &InvertedIndex{
		postings:   make(map[string]map[string]int),
		docLengths: make(map[string]int),
		docs:       make(map[string]Document),
	}
}

// Add indexiert Titel + Text eines Dokuments.
func (idx *InvertedIndex) Add(doc Document) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	tokens := Tokenize(doc.Title + " " + doc.Text)
	idx.docLengths[doc.ID] = len(tokens)
	idx.docs[doc.ID] = doc

	counts := make(map[string]int)
	for _, t := range tokens {
		counts[t]++
	}
	for term, freq := range counts {
		if idx.postings[term] == nil {
			idx.postings[term] = make(map[string]int)
		}
		idx.postings[term][doc.ID] = freq
	}
}

// DocCount liefert die Anzahl indexierter Dokumente.
func (idx *InvertedIndex) DocCount() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.docs)
}

func (idx *InvertedIndex) tfidf(term, docID string) float64 {
	postings, ok := idx.postings[term]
	if !ok {
		return 0
	}
	freq, ok := postings[docID]
	if !ok {
		return 0
	}
	tf := float64(freq) / float64(idx.docLengths[docID])
	df := float64(len(postings))
	idf := math.Log(float64(len(idx.docs)+1)/(df+1)) + 1
	return tf * idf
}

// Search rankt indexierte Dokumente per TF-IDF und liefert die Top-K Ergebnisse.
func (idx *InvertedIndex) Search(query string, topK int) []SearchResult {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	terms := Tokenize(query)
	scores := make(map[string]float64)
	for _, term := range terms {
		for docID := range idx.postings[term] {
			scores[docID] += idx.tfidf(term, docID)
		}
	}

	type scored struct {
		docID string
		score float64
	}
	ranked := make([]scored, 0, len(scores))
	for id, s := range scores {
		ranked = append(ranked, scored{id, s})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score == ranked[j].score {
			return ranked[i].docID < ranked[j].docID
		}
		return ranked[i].score > ranked[j].score
	})

	if topK > 0 && len(ranked) > topK {
		ranked = ranked[:topK]
	}

	results := make([]SearchResult, 0, len(ranked))
	for _, r := range ranked {
		doc := idx.docs[r.docID]
		results = append(results, SearchResult{
			URL:     doc.URL,
			Title:   doc.Title,
			Snippet: Snippet(doc.Text, terms, 200),
			Score:   round4(r.score),
		})
	}
	return results
}

func round4(f float64) float64 {
	return math.Round(f*10000) / 10000
}

// Snippet extrahiert ein Textfenster um den ersten Treffer und hebt Treffer hervor.
func Snippet(text string, terms []string, maxLen int) string {
	lower := strings.ToLower(text)
	pos := -1
	for _, t := range terms {
		if p := strings.Index(lower, t); p != -1 && (pos == -1 || p < pos) {
			pos = p
		}
	}
	if pos == -1 {
		if len(text) <= maxLen {
			return text
		}
		return text[:maxLen] + "…"
	}

	start := pos - 60
	if start < 0 {
		start = 0
	}
	end := start + maxLen
	if end > len(text) {
		end = len(text)
	}
	snippet := text[start:end]

	for _, t := range terms {
		snippet = highlight(snippet, t)
	}
	if start > 0 {
		snippet = "… " + snippet
	}
	if end < len(text) {
		snippet += " …"
	}
	return snippet
}

func highlight(s, term string) string {
	lower := strings.ToLower(s)
	var b strings.Builder
	i := 0
	for {
		idx := strings.Index(lower[i:], term)
		if idx == -1 {
			b.WriteString(s[i:])
			break
		}
		start := i + idx
		end := start + len(term)
		b.WriteString(s[i:start])
		b.WriteString("<mark>")
		b.WriteString(s[start:end])
		b.WriteString("</mark>")
		i = end
	}
	return b.String()
}
