package restapi

import (
	"net/http"
	"sort"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// visionSimilarityDefaultLimit/visionSimilarityMaxLimit bound how many
// matches a single call returns -- same "sane default, hard ceiling"
// convention as intQueryParam's callers elsewhere in this file.
const (
	visionSimilarityDefaultLimit = 5
	visionSimilarityMaxLimit     = 20
)

// visionSimilarityANNPoolMultiplier over-fetches ANN candidates beyond the
// requested limit before this handler's own cosine-similarity re-ranking
// -- TopSemanticMatches' candidate pool isn't returned in similarity
// order (see hybrid_search_service.go's identical use), so a pool no
// larger than limit itself would silently drop the true top matches.
const visionSimilarityANNPoolMultiplier = 10

type visionSimilarityRequest struct {
	// Provider names an EmbeddingHTTPEndpoint.ID -- must support
	// ports.ImageEmbedder and have ANN enabled for meaningful results.
	Provider string `json:"provider"`
	// Base64/MimeType describe the image to embed and search with, same
	// shape mcp-files' read_file_base64 tool already hands the model.
	Base64   string `json:"base64"`
	MimeType string `json:"mime_type"`
	// Limit bounds how many matches are returned; <=0 uses
	// visionSimilarityDefaultLimit, clamped to visionSimilarityMaxLimit.
	Limit int `json:"limit"`
}

type visionSimilarityMatch struct {
	URL   string  `json:"url"`
	Title string  `json:"title"`
	Score float64 `json:"score"`
}

type visionSimilarityResponse struct {
	Matches []visionSimilarityMatch `json:"matches"`
}

// handleVisionSimilarity embeds an attached image against the configured
// provider and vector-searches it against this instance's own indexed
// documents -- "reverse image search into your own index" for
// cmd/mcp-vision's "vision_similarity" tool. Deliberately NOT wrapped in
// requireAuthAPI (mirrors /account/api/files): the only caller is a
// trusted local stdio subprocess with no session cookie, so this
// self-gates via X-Internal-API-Key instead (see internalVisionAPIKey).
func (h *Handler) handleVisionSimilarity(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if h.internalVisionAPIKey == "" || !requestHasSecretHeader(r, internalAPIKeyHeader, h.internalVisionAPIKey) {
		http.Error(w, authRequiredMsg, http.StatusUnauthorized)
		return
	}
	if !requireConfigured(w, h.semanticMatcher != nil && h.embeddingRepo != nil && len(h.embedders) > 0, "vision similarity") {
		return
	}
	req, ok := decodeJSON[visionSimilarityRequest](w, r)
	if !ok {
		return
	}
	if req.Base64 == "" {
		http.Error(w, "base64 must not be empty", http.StatusBadRequest)
		return
	}
	embedder, ok := h.embedders[req.Provider]
	if !ok {
		http.Error(w, "unknown or disabled provider: "+req.Provider, http.StatusBadRequest)
		return
	}
	imageEmbedder, ok := embedder.(ports.ImageEmbedder)
	if !ok {
		http.Error(w, "provider "+req.Provider+" does not support image embedding", http.StatusBadRequest)
		return
	}
	limit := req.Limit
	if limit <= 0 {
		limit = visionSimilarityDefaultLimit
	} else if limit > visionSimilarityMaxLimit {
		limit = visionSimilarityMaxLimit
	}

	queryVec, err := imageEmbedder.EmbedImage(r.Context(), req.Base64, req.MimeType)
	if err != nil {
		http.Error(w, "embedding image: "+err.Error(), http.StatusBadGateway)
		return
	}
	candidates, ok, err := h.semanticMatcher.TopSemanticMatches(r.Context(), queryVec, limit*visionSimilarityANNPoolMultiplier, req.Provider)
	if err != nil {
		http.Error(w, "searching index: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "pgvector ANN is not available for provider "+req.Provider, http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, visionSimilarityResponse{Matches: h.rankVisionMatches(r, candidates, queryVec, limit)})
}

// rankVisionMatches scores every ANN candidate by cosine similarity
// against queryVec (TopSemanticMatches' own pool isn't similarity-ordered
// -- see visionSimilarityANNPoolMultiplier), takes the top limit, and
// resolves their URL/Title via DocumentsByIDs. A document that's since
// been deleted (present in the ANN index, gone from documents) is
// silently skipped, same convention as embedding_recompute_job.go's
// deleted-between-list-and-fetch handling.
func (h *Handler) rankVisionMatches(r *http.Request, candidates map[string]domain.EmbeddedVector, queryVec []float32, limit int) []visionSimilarityMatch {
	type scored struct {
		id    string
		score float64
	}
	ranked := make([]scored, 0, len(candidates))
	for id, vec := range candidates {
		ranked = append(ranked, scored{id: id, score: domain.CosineSimilarity(queryVec, vec.Vector)})
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}

	ids := make([]string, len(ranked))
	for i, c := range ranked {
		ids[i] = c.id
	}
	docs, err := h.embeddingRepo.DocumentsByIDs(r.Context(), ids)
	if err != nil {
		return nil
	}
	out := make([]visionSimilarityMatch, 0, len(ranked))
	for _, c := range ranked {
		doc, ok := docs[c.id]
		if !ok {
			continue
		}
		out = append(out, visionSimilarityMatch{URL: doc.URL, Title: doc.Title, Score: c.score})
	}
	return out
}
