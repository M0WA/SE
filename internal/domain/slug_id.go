package domain

import (
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

// newSeqID mints a "<prefix>-<unix-nano>-<seq>" ID, unique within a process
// without a database round-trip -- shared by NewCrawlJobID/NewScheduledCrawlID.
func newSeqID(prefix string, seq *int64) string {
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), atomic.AddInt64(seq, 1))
}

// slugIDRE matches every run of characters a mintSlugID-derived ID must
// collapse away -- shared by every resource type minting an ID from an
// admin-typed display name (User, MCPServer, EmbeddingHTTPEndpoint).
var slugIDRE = regexp.MustCompile(`[^a-z0-9]+`)

// SlugIDPattern is every valid mintSlugID-derived ID (lowercase
// alphanumeric/underscore, 1-20 chars) -- short enough for sqlrepo's
// VARCHAR(20) id columns.
var SlugIDPattern = regexp.MustCompile(`^[a-z0-9_]{1,20}$`)

// mintSlugID derives a <=20-character ID from name (lowercased,
// non-alphanumeric runs collapsed to "_"), appending the shortest numeric
// suffix avoiding a collision with existing. Falls back to a
// timestamp-derived ID using fallbackPrefix if name has no alphanumeric
// characters. Shared by NewMCPServerID/NewEmbeddingEndpointID.
func mintSlugID(name string, existing map[string]bool, fallbackPrefix string) string {
	slug := strings.Trim(slugIDRE.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "_"), "_")
	if len(slug) > 20 {
		slug = strings.Trim(slug[:20], "_")
	}
	if slug == "" {
		slug = fmt.Sprintf("%s%d", fallbackPrefix, time.Now().UnixNano()%1_000_000_000)
	}
	if !existing[slug] {
		return slug
	}
	for n := 2; ; n++ {
		suffix := fmt.Sprintf("_%d", n)
		base := slug
		if len(base)+len(suffix) > 20 {
			base = base[:20-len(suffix)]
		}
		if candidate := base + suffix; !existing[candidate] {
			return candidate
		}
	}
}
