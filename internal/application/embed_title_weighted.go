package application

import (
	"context"
	"strings"

	"searchengine/internal/domain"
)

// embedTitleWeighted computes a document's embedding as a weighted
// combination of its title's and body's embeddings (see
// domain.CombineWeighted and domain.OperationalSettingsValues.
// EmbeddingTitleWeight), rather than a single Embed call against a
// concatenated string. embed is caller-supplied so each call site can
// apply its own rate-limit pacing per actual Embed call issued (this can
// issue up to two) -- see sqlCrawlerService.Crawl and
// RunEmbeddingRecomputeJob.
//
// Falls back to a single Embed call whenever there's nothing real to
// combine: an empty title or body, or titleWeight at either extreme (0 or
// 1, where the other input's contribution would be zeroed out anyway) --
// so a document with no title, or EmbeddingTitleWeight left at its
// disabled (0) value, costs exactly the one Embed call this always used
// to cost.
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
