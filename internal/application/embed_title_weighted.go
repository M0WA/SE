package application

import (
	"context"
	"strings"

	"searchengine/internal/domain"
)

// embedTitleWeighted computes a document's embedding as a weighted
// combination of its title's and body's embeddings, rather than one call
// against a concatenated string. Falls back to a single Embed call when
// there's nothing to combine (empty title/body, or titleWeight 0 or 1).
func embedTitleWeighted(ctx context.Context, embed func(context.Context, string) ([]float32, error), title, text string, titleWeight float64) ([]float32, error) {
	title = strings.TrimSpace(title)
	text = strings.TrimSpace(text)
	switch {
	case title == "":
		return embed(ctx, text)
	case text == "":
		return embed(ctx, title)
	case titleWeight <= 0:
		return embed(ctx, text)
	case titleWeight >= 1:
		return embed(ctx, title)
	}

	titleVec, err := embed(ctx, title)
	if err != nil {
		return nil, err
	}
	bodyVec, err := embed(ctx, text)
	if err != nil {
		return nil, err
	}
	return domain.CombineWeighted(titleVec, bodyVec, titleWeight), nil
}
