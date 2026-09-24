package application

import (
	"context"
	"log"
	"sync"
	"time"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// EmbeddingRecomputeBatchSize bounds how many documents' text is held in
// memory at once while recomputing -- keeps memory bounded regardless of
// corpus size, same reason AllDocumentIDs returns bare IDs up front. Also
// how often progress is checkpointed (see RunEmbeddingRecomputeJob's
// onBatchDone) -- a smaller batch means finer-grained resume/progress at
// the cost of more frequent status writes.
const EmbeddingRecomputeBatchSize = 50

// EmbeddingRecomputeResult reports what RunEmbeddingRecomputeJob actually
// did.
type EmbeddingRecomputeResult struct {
	Documents int
	Failed    int
}

// RunEmbeddingRecomputeJob re-embeds every document's already-stored text
// and writes it back -- no recrawl, just a fresh Embed call, since a
// changed provider/model invalidates the vector space, not the text.
//
// resumeFromID, when non-empty, starts from repo.DocumentIDsAfter(afterID)
// instead of the full repo.AllDocumentIDs -- lets a caller resume a run
// interrupted partway through (see RunEmbeddingRecomputeJobWithStatus)
// rather than re-embedding documents already done. Empty means start at
// the beginning, same as before this existed.
//
// onBatchDone, when non-nil, is called after every batch finishes with the
// batch's last document ID and the running totals so far -- lets a caller
// persist a resumable checkpoint and live progress without waiting for the
// whole run (which can take hours for a real corpus) to finish. A nil
// onBatchDone is a plain no-op, unused by a caller (e.g. a test) that
// doesn't need either.
//
// concurrency, when non-nil, is called fresh at the START OF EVERY BATCH
// (not once at job start) to decide how many of that batch's documents are
// processed in-flight at once -- so an admin raising/lowering it on the
// recompute page takes effect on this run's very next batch, not only on a
// future run. A nil concurrency, or one returning <= 0, processes one
// document at a time (the original sequential behavior). Raising it is safe
// by design: each embedder's own httpembed.Embedder already rate-limits
// itself when configured, so extra concurrency here mainly overlaps network
// latency rather than adding load an endpoint didn't already allow through.
//
// A single Embed failure is logged and counted, not fatal to the run.
// Every embedders entry gets recomputed, not just the active one, so
// switching later needs no second recompute. titleWeight blends each
// title the same way sqlCrawlerService.Crawl does at crawl time.
func RunEmbeddingRecomputeJob(ctx context.Context, repo ports.EmbeddingRepository, embedders map[string]ports.EmbeddingProvider, titleWeight float64, resumeFromID string, concurrency func() int, onBatchDone func(lastID string, documents, failed int)) (EmbeddingRecomputeResult, error) {
	ids, err := recomputeCandidateIDs(ctx, repo, resumeFromID)
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

		recomputeBatch(ctx, recomputeJobConfig{repo: repo, embedders: embedders, titleWeight: titleWeight}, batch, docs, batchConcurrency(concurrency), &result)

		if onBatchDone != nil {
			onBatchDone(batch[len(batch)-1], result.Documents, result.Failed)
		}
	}
	return result, nil
}

// recomputeCandidateIDs resolves the ID list a run walks -- the full
// corpus, or everything after a checkpoint, per resumeFromID's own doc
// comment on RunEmbeddingRecomputeJob.
func recomputeCandidateIDs(ctx context.Context, repo ports.EmbeddingRepository, resumeFromID string) ([]string, error) {
	if resumeFromID == "" {
		return repo.AllDocumentIDs(ctx)
	}
	return repo.DocumentIDsAfter(ctx, resumeFromID)
}

// batchConcurrency resolves concurrency fresh -- see RunEmbeddingRecomputeJob's
// own doc comment for why this can't be captured once at job start -- and
// defaults a nil/non-positive result to 1 (fully sequential).
func batchConcurrency(concurrency func() int) int {
	if concurrency == nil {
		return 1
	}
	if c := concurrency(); c > 0 {
		return c
	}
	return 1
}

// recomputeJobConfig bundles RunEmbeddingRecomputeJob's three
// job-wide-constant parameters (the same for every batch/document the
// whole run touches) into one -- keeps recomputeBatch/recomputeOneDocument
// under the linter's own parameter-count threshold, and groups what's
// conceptually one concern (what a single embed-and-save needs) at every
// call site too.
type recomputeJobConfig struct {
	repo        ports.EmbeddingRepository
	embedders   map[string]ports.EmbeddingProvider
	titleWeight float64
}

// recomputeBatch processes batch's documents up to n at a time (a
// semaphore-bounded goroutine per document), accumulating into result
// (mutex-guarded, since goroutines write it concurrently) and blocking
// until every document has been attempted before returning -- so the
// caller's checkpoint reflects a truly finished batch, never a partial one.
func recomputeBatch(ctx context.Context, cfg recomputeJobConfig, batch []string, docs map[string]domain.Document, n int, result *EmbeddingRecomputeResult) {
	sem := make(chan struct{}, n)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, id := range batch {
		doc, ok := docs[id]
		if !ok {
			// Deleted between the ID listing and this batch being
			// fetched -- nothing to recompute.
			continue
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(id string, doc domain.Document) {
			defer wg.Done()
			defer func() { <-sem }()

			failed := recomputeOneDocument(ctx, cfg, id, doc)

			mu.Lock()
			if failed {
				result.Failed++
			} else {
				result.Documents++
			}
			mu.Unlock()
		}(id, doc)
	}
	wg.Wait()
}

// recomputeOneDocument re-embeds one document against every embedders
// entry and writes the combined result back, reporting whether it
// failed -- a single Embed/UpdateEmbedding error is logged and counted,
// never fatal to the batch/run.
func recomputeOneDocument(ctx context.Context, cfg recomputeJobConfig, id string, doc domain.Document) bool {
	embeddings := make(map[string][]float32, len(cfg.embedders))
	for provider, embedder := range cfg.embedders {
		vec, err := embedTitleWeighted(ctx, embedder.Embed, doc.Title, doc.Text, cfg.titleWeight)
		if err != nil {
			log.Printf("recomputing %s embedding for %s: %v", provider, id, err)
			return true
		}
		embeddings[provider] = vec
	}
	if err := cfg.repo.UpdateEmbedding(ctx, id, embeddings); err != nil {
		log.Printf("saving recomputed embedding for %s: %v", id, err)
		return true
	}
	return false
}

// RunEmbeddingRecomputeJobWithStatus wraps RunEmbeddingRecomputeJob,
// persisting a domain.EmbeddingRecomputeStatus so any admin-server
// instance can show progress and last result. settings may be nil
// (bookkeeping skipped). Meant to run in its own goroutine -- one Embed
// call per document can take minutes for a real corpus.
//
// resumeFromID has the same meaning as RunEmbeddingRecomputeJob's: pass
// "" for a fresh, full pass (the normal explicit-trigger case -- never
// uses any old checkpoint as a resume cursor, since it may belong to a
// run under different settings), or a previous run's own LastDocID to
// resume it after an interruption (see ResumeStaleEmbeddingRecomputeIfAny,
// the only other caller that passes a non-empty value).
//
// concurrency is passed straight through to RunEmbeddingRecomputeJob -- see
// its own doc comment for why this must stay a closure over live settings
// rather than a value captured once here, for a run that can last hours.
func RunEmbeddingRecomputeJobWithStatus(ctx context.Context, repo ports.EmbeddingRepository, embedders map[string]ports.EmbeddingProvider, settings ports.SettingsStore, titleWeight float64, resumeFromID string, concurrency func() int) (EmbeddingRecomputeResult, error) {
	runStart := time.Now()
	status := LoadEmbeddingRecomputeStatus(ctx, settings)
	status.InProgress = true

	// For a resumed run, carry the checkpoint's own prior progress forward
	// as a base so live/final counts stay cumulative across the
	// interruption, not reset to only what this resumed pass itself does.
	// A fresh trigger has no prior progress of its own to add -- its first
	// batch's onBatchDone below naturally overwrites whatever the previous
	// run left in status with this run's own real numbers. Deliberately
	// NOT reset here, upfront: if this fresh run fails before even one
	// batch completes (e.g. the corpus listing itself errors), the
	// previous run's last-known-good result should stay visible, exactly
	// like before onBatchDone/checkpointing existed -- not get clobbered
	// by a run that never actually produced anything of its own.
	baseDocuments, baseFailed := 0, 0
	if resumeFromID != "" {
		baseDocuments, baseFailed = status.Documents, status.Failed
	}
	saveEmbeddingRecomputeStatus(ctx, settings, status)

	onBatchDone := func(lastID string, documents, failed int) {
		status.LastDocID = lastID
		status.Documents = baseDocuments + documents
		status.Failed = baseFailed + failed
		saveEmbeddingRecomputeStatus(ctx, settings, status)
	}
	result, err := RunEmbeddingRecomputeJob(ctx, repo, embedders, titleWeight, resumeFromID, concurrency, onBatchDone)

	status.InProgress = false
	if err == nil {
		status.LastRunAt = time.Now().UTC()
		status.Documents = baseDocuments + result.Documents
		status.Failed = baseFailed + result.Failed
		status.DurationMs = time.Since(runStart).Milliseconds()
		status.LastDocID = ""
	}
	saveEmbeddingRecomputeStatus(ctx, settings, status)
	return result, err
}

// LoadEmbeddingRecomputeStatus reads the persisted status back -- a nil
// settings, store error, missing key, or bad value all just return the
// zero value; "nothing to show yet" is never an error.
func LoadEmbeddingRecomputeStatus(ctx context.Context, settings ports.SettingsStore) domain.EmbeddingRecomputeStatus {
	return loadJSONStatus[domain.EmbeddingRecomputeStatus](ctx, settings, ports.SettingsKeyEmbeddingRecomputeStatus, "embedding recompute status")
}

// ResumeStaleEmbeddingRecomputeIfAny checks for a recompute run a previous
// instance was killed mid-flight (InProgress left true from a restart --
// RunEmbeddingRecomputeJobWithStatus always clears it on a clean finish),
// and if found, resumes that SAME run in the background from its
// LastDocID checkpoint instead of silently discarding the progress --
// unlike a fresh POST /admin/api/embeddings/recompute trigger, which
// always starts over (see RunEmbeddingRecomputeJobWithStatus's
// resumeFromID: "" there). Returns whether a resume was actually started,
// so the caller can log it; nil settings or no stale run found are both a
// no-op, not an error.
func ResumeStaleEmbeddingRecomputeIfAny(ctx context.Context, repo ports.EmbeddingRepository, embedders map[string]ports.EmbeddingProvider, settings ports.SettingsStore, titleWeight float64, concurrency func() int) bool {
	if settings == nil {
		return false
	}
	status := LoadEmbeddingRecomputeStatus(ctx, settings)
	if !status.InProgress {
		return false
	}
	resumeFromID := status.LastDocID
	go func() {
		bgCtx := context.Background()
		if _, err := RunEmbeddingRecomputeJobWithStatus(bgCtx, repo, embedders, settings, titleWeight, resumeFromID, concurrency); err != nil {
			log.Printf("resuming interrupted embedding recompute: %v", err)
		}
	}()
	return true
}

func saveEmbeddingRecomputeStatus(ctx context.Context, settings ports.SettingsStore, status domain.EmbeddingRecomputeStatus) {
	saveJSONStatus(ctx, settings, ports.SettingsKeyEmbeddingRecomputeStatus, "embedding recompute status", status)
}
