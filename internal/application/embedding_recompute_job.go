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

// embedRateLimitInterval converts ratePerSecond (see
// domain.OperationalSettingsValues.EmbeddingRateLimitPerSecond)
// into the minimum interval paceEmbedCall enforces between Embed calls --
// pacing this job so it stays within a typical HTTP embeddings provider's
// rate limit rather than firing every document's call back-to-back as
// fast as this loop naturally would. This matters even with
// httpembed.Embedder's own retry-on-429/529 backoff: a rate-limited
// response returns near-instantly (no real inference work done), so an
// unthrottled loop can spin through a rate-limit condition far faster
// than any real embedding call ever would, compounding it instead of
// self-correcting. ratePerSecond <= 0 disables pacing entirely (interval
// 0) -- domain.OperationalSettings.Set never actually produces that in
// production (it self-heals to a positive default), but tests exercising
// RunEmbeddingRecomputeJob's other behavior rely on being able to opt out
// of real waits this way.
func embedRateLimitInterval(ratePerSecond int) time.Duration {
	if ratePerSecond <= 0 {
		return 0
	}
	return time.Second / time.Duration(ratePerSecond)
}

// paceEmbedCall blocks for whatever's left of interval beyond elapsed
// (already-spent time on the Embed call this paces), or returns early if
// ctx is cancelled first -- callers don't need to check its return value:
// an early return here just means the very next Embed call fails fast
// against the same cancelled context instead.
func paceEmbedCall(ctx context.Context, elapsed, interval time.Duration) {
	wait := interval - elapsed
	if wait <= 0 {
		return
	}
	select {
	case <-ctx.Done():
	case <-time.After(wait):
	}
}

// EmbeddingRecomputeResult reports what RunEmbeddingRecomputeJob actually
// did.
type EmbeddingRecomputeResult struct {
	Documents int
	Failed    int
}

// RunEmbeddingRecomputeJob re-embeds every document's already-stored text
// with embedder and writes the result back -- no recrawl, no re-fetch of
// the original page, just a fresh Embed call per document against content
// already sitting in the documents table. This is exactly what changing
// OperationalSettingsValues.EmbeddingProvider (or, for the HTTP provider,
// its model/dimensions) actually invalidates: the vector space, not the
// indexed text itself -- see that field's own doc comment, which used to
// say a full re-crawl was the only way to bring semantic ranking back in
// sync; this is the alternative.
//
// A single document's Embed failure (a flaky HTTP embeddings endpoint, a
// rate limit, a since-deleted document) is logged and counted, not fatal
// to the whole run -- losing one document's fresh embedding is far less
// harmful than aborting a recompute that's otherwise most of the way
// through a large corpus.
//
// ratePerSecond (see
// domain.OperationalSettingsValues.EmbeddingRateLimitPerSecond)
// paces Embed calls to that rate -- see embedRateLimitInterval.
func RunEmbeddingRecomputeJob(ctx context.Context, repo ports.EmbeddingRepository, embedder ports.EmbeddingProvider, ratePerSecond int) (EmbeddingRecomputeResult, error) {
	interval := embedRateLimitInterval(ratePerSecond)
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
			embedStart := time.Now()
			vec, err := embedder.Embed(ctx, doc.Text)
			paceEmbedCall(ctx, time.Since(embedStart), interval)
			if err != nil {
				log.Printf("recomputing embedding for %s: %v", id, err)
				result.Failed++
				continue
			}
			if err := repo.UpdateEmbedding(ctx, id, vec); err != nil {
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
// additionally persisting a domain.EmbeddingRecomputeStatus to settings
// (under ports.SettingsKeyEmbeddingRecomputeStatus) so any admin-server
// instance's Settings page can show whether a recompute -- triggered by
// this click or another admin's, possibly from a different browser or a
// different admin-server instance -- is currently running, and what the
// last completed run found. settings may be nil (e.g. in a test), in
// which case this behaves exactly like RunEmbeddingRecomputeJob with the
// status bookkeeping skipped.
//
// Unlike application.RunPageRankJobWithStatus (a single batched DB
// read+write, fast enough to run synchronously inside its own HTTP
// handler), this is meant to be launched in its own goroutine by the
// caller: recomputing a real corpus means one Embed call per document,
// each a network round-trip against an HTTP embeddings endpoint, so the
// whole run can easily take minutes -- see handleAdminEmbeddingsRecompute.
func RunEmbeddingRecomputeJobWithStatus(ctx context.Context, repo ports.EmbeddingRepository, embedder ports.EmbeddingProvider, settings ports.SettingsStore, ratePerSecond int) (EmbeddingRecomputeResult, error) {
	start := time.Now()
	status := LoadEmbeddingRecomputeStatus(ctx, settings)
	status.InProgress = true
	saveEmbeddingRecomputeStatus(ctx, settings, status)

	result, err := RunEmbeddingRecomputeJob(ctx, repo, embedder, ratePerSecond)

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

// LoadEmbeddingRecomputeStatus reads the persisted status back (see
// RunEmbeddingRecomputeJobWithStatus) -- used both internally, to update
// it without clobbering fields a concurrent recompute isn't touching, and
// by the admin Settings page's GET handler, to show it. A nil settings, a
// store error, a missing key (nothing has ever recomputed through this
// mechanism), or an undecodable value all just return the zero value --
// "nothing to show yet" is never treated as an error.
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
// to false, without touching LastRunAt/Documents/Failed/DurationMs from
// whatever run last actually completed -- the same self-healing this
// codebase already applies to scheduled_crawls.in_progress (see
// ports.ScheduledCrawlStore.ResetStaleInProgress and its cmd/crawl startup
// call): admin-server is the only writer of this status, so InProgress
// still true when THIS process is just starting up can only mean a
// previous instance was killed (crashed, restarted, redeployed) mid-run,
// never a genuinely still-running goroutine in this fresh process --
// otherwise a killed recompute leaves the flag stuck forever, permanently
// blocking every future trigger with handleAdminEmbeddingsRecomputeStart's
// "already in progress" 409. Returns whether a stale flag was actually
// found and cleared, for the caller to log; a nil settings, store error,
// or nothing-to-reset are all quiet no-ops, matching
// LoadEmbeddingRecomputeStatus's own error handling.
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
