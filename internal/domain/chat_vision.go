package domain

import "time"

// ChatVisionSettings configures two independent, optional capabilities for
// an image a user attaches to a chat turn -- deliberately separate from
// search's embedding endpoints (EmbeddingHTTPEndpoint) and from
// ChatEndpoint, even when Similarity ends up pointed at the same
// underlying model as search, per the explicit requirement that image
// handling in chat be configured independently of search.
type ChatVisionSettings struct {
	// SimilarityEnabled turns on the "vision_similarity" mcp-vision tool:
	// embed an attached image and vector-search it against this instance's
	// own indexed documents ("reverse image search into your own index").
	SimilarityEnabled bool
	// SimilarityProviderID names which EmbeddingHTTPEndpoint (by ID) to
	// embed the image against and search document_embeddings with --
	// reuses that endpoint's own BaseURL/APIKey/Model/Dimensions (the
	// "same Embeddings Model as search" part of the requirement) while
	// staying independent of EmbeddingSearchWeights (the "configured
	// separately" part): enabling an endpoint here never changes whether
	// it contributes to search's own blended score, and vice versa. The
	// referenced endpoint must support ImageEmbedder (see ports.go) --
	// checked at call time, not here, since that's a live model
	// capability, not something this config alone can guarantee.
	SimilarityProviderID string

	// CaptionEnabled turns on the "vision_caption" mcp-vision tool: send
	// an attached image to a chat-completions-style vision-language
	// endpoint and return its generated description. A different
	// capability from Similarity (generation, not embedding/search) --
	// almost certainly a different model deployment, so this gets its own
	// full endpoint config rather than referencing an existing one.
	CaptionEnabled bool
	CaptionBaseURL string
	// CaptionAPIKey is encrypted at rest (restapi handles
	// encryption/decryption, this struct just carries whatever string
	// it's given), same convention as EmbeddingHTTPEndpoint.APIKey.
	CaptionAPIKey string
	CaptionModel  string

	UpdatedAt time.Time
}
