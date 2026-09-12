package bootstrap

import (
	"context"
	"time"
)

// pollRefresh runs refresh once immediately, then again every interval for
// as long as ctx stays alive -- the shared "seed it now, then keep it
// fresh in the background" mechanism behind SyncSettings, SyncCorpusStats,
// and SyncVocabulary, each of which otherwise hand-rolled the identical
// ticker/select loop around its own refresh step.
func pollRefresh(ctx context.Context, interval time.Duration, refresh func()) {
	refresh()
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				refresh()
			}
		}
	}()
}
