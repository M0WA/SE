package restapi

import (
	"encoding/json"
	"net/http"

	"searchengine/internal/ports"
)

// RoutesCrawlInternal serves the single endpoint the crawl-server binary
// exposes: a POST /crawl that actually executes a crawl. It is never
// reachable from nginx or the internet -- admin-server is its only caller,
// over the network via internal/adapters/crawlclient.
func (h *Handler) RoutesCrawlInternal() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /crawl", h.handleCrawl)
	return mux
}

type crawlInternalResponse struct {
	CrawledCount int `json:"crawled_count"`
}

// handleCrawl is registered on the Go 1.22+ pattern "POST /crawl", which
// already guarantees the method -- no separate method check needed here.
func (h *Handler) handleCrawl(w http.ResponseWriter, r *http.Request) {
	var opts ports.CrawlOptions
	if err := json.NewDecoder(r.Body).Decode(&opts); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if len(opts.SeedURLs) == 0 {
		http.Error(w, "seed_urls must not be empty", http.StatusBadRequest)
		return
	}

	count, err := h.crawler.Crawl(r.Context(), opts)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, crawlInternalResponse{CrawledCount: count})
}
