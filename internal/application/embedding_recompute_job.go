package application

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// EmbeddingRecomputeBatchSize bounds how many documents' text is held in
// memory at once while recomputing embeddings -- keeps memory bounded
// regardless of total corpus size, the same reason AllDocumentIDs returns
// bare IDs rather than full documents up front.
const EmbeddingRecomputeBatchSize = 50

// EmbeddingRecomputeResult reports what RunEmbeddingRecomputeJob actually
// did.
type EmbeddingRecomputeResult struct {
	Documents int
	Failed    int
}

// RunEmbeddingRecomputeJob re-embeds every document's already-stored text
// and writes the result back -- no recrawl or re-fetch, just a fresh Embed
// call against content already in the documents table. This is what a
// changed embedding provider/model actually invalidates: the vector space,
// not the indexed text.
//
// A single document's Embed failure is logged and counted, not fatal to
// the whole run. Each embedders entry gets a freshly recomputed vector,
// not just whichever is active for search, so switching the active one
// never needs a second recompute. titleWeight blends each title in the
// same way sqlCrawlerService.Crawl does at crawl time.
func RunEmbeddingRecomputeJob(ctx context.Context, repo ports.EmbeddingRepository, embedders map[string]ports.EmbeddingProvider, titleWeight float64) (EmbeddingRecomputeResult, error) {
	ids, err := repo.AllDocumentIDs(ctx)
	if err != nil {
		return EmbeddingRecomputeResult{}, err
	}

	var result EmbeddingRecomputeResult
	for start := 0; start < len(ids); start += EmbeddingRecomputeBatchSize {
		end := start + EmbeddingRecomputeBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		docs, err := repo.DocumentsByIDs(ctx, batch)
		if err != nil {
			return EmbeddingRecomputeResult{}, err
		}
		for _, id := range batch {
			doc, ok := docs[id]
			if !ok {
				// Deleted between AllDocumentIDs listing it and this batch
				// being fetched -- nothing to recompute.
				continue
			}
			embeddings := make(map[string][]float32, len(embedders))
			var embedErr error
			for provider, embedder := range embedders {
				vec, err := embedTitleWeighted(ctx, embedder.Embed, doc.Title, doc.Text, titleWeight)
				if err != nil {
					log.Printf("recomputing %s embedding for %s: %v", provider, id, err)
					embedErr = err
					break
				}
				embeddings[provider] = vec
			}
			if embedErr != nil {
				result.Failed++
				continue
			}
			if err := repo.UpdateEmbedding(ctx, id, embeddings); err != nil {
				log.Printf("saving recomputed embedding for %s: %v", id, err)
				result.Failed++
				continue
			}
			result.Documents++
		}
	}
	return result, nil
}

// RunEmbeddingRecomputeJobWithStatus wraps RunEmbeddingRecomputeJob,
// persisting a domain.EmbeddingRecomputeStatus so any admin-server
// instance can show whether a recompute is running and what it last
// found. settings may be nil (bookkeeping then skipped). Unlike
// RunPageRankJobWithStatus, this is meant to run in its own goroutine --
// one Embed network call per document can take minutes for a real corpus.
func RunEmbeddingRecomputeJobWithStatus(ctx context.Context, repo ports.EmbeddingRepository, embedders map[string]ports.EmbeddingProvider, settings ports.SettingsStore, titleWeight float64) (EmbeddingRecomputeResult, error) {
	start := time.Now()
	status := LoadEmbeddingRecomputeStatus(ctx, settings)
	status.InProgress = true
	saveEmbeddingRecomputeStatus(ctx, settings, status)

	result, err := RunEmbeddingRecomputeJob(ctx, repo, embedders, titleWeight)

	status.InProgress = false
	if err == nil {
		status.LastRunAt = time.Now().UTC()
		status.Documents = result.Documents
		status.Failed = result.Failed
		status.DurationMs = time.Since(start).Milliseconds()
	}
	saveEmbeddingRecomputeStatus(ctx, settings, status)
	return result, err
}

// LoadEmbeddingRecomputeStatus reads the persisted status back -- used
// internally and by the admin Settings GET handler. A nil settings, store
// error, missing key, or bad value all just return the zero value;
// "nothing to show yet" is never an error.
func LoadEmbeddingRecomputeStatus(ctx context.Context, settings ports.SettingsStore) domain.EmbeddingRecomputeStatus {
	if settings == nil {
		return domain.EmbeddingRecomputeStatus{}
	}
	value, found, err := settings.GetSetting(ctx, ports.SettingsKeyEmbeddingRecomputeStatus)
	if err != nil || !found {
		return domain.EmbeddingRecomputeStatus{}
	}
	var status domain.EmbeddingRecomputeStatus
	if err := json.Unmarshal([]byte(value), &status); err != nil {
		log.Printf("decoding embedding recompute status: %v", err)
		return domain.EmbeddingRecomputeStatus{}
	}
	return status
}

// ResetStaleEmbeddingRecomputeStatus clears a leftover InProgress=true back
// to false without touching the last completed run's fields -- called at
// startup since InProgress still true then can only mean a previous
// instance was killed mid-run, never a real still-running goroutine here.
// Otherwise a killed recompute leaves every future trigger 409ing forever.
// Returns whether a stale flag was found; nil settings/errors are no-ops.
func ResetStaleEmbeddingRecomputeStatus(ctx context.Context, settings ports.SettingsStore) bool {
	if settings == nil {
		return false
	}
	status := LoadEmbeddingRecomputeStatus(ctx, settings)
	if !status.InProgress {
		return false
	}
	status.InProgress = false
	saveEmbeddingRecomputeStatus(ctx, settings, status)
	return true
}

func saveEmbeddingRecomputeStatus(ctx context.Context, settings ports.SettingsStore, status domain.EmbeddingRecomputeStatus) {
	if settings == nil {
		return
	}
	data, err := json.Marshal(status)
	if err != nil {
		log.Printf("encoding embedding recompute status: %v", err)
		return
	}
	if err := settings.SaveSetting(ctx, ports.SettingsKeyEmbeddingRecomputeStatus, string(data)); err != nil {
		log.Printf("saving embedding recompute status: %v", err)
	}
}
