package domain

import (
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

// newSeqID mints a "<prefix>-<unix-nano>-<seq>" ID, unique within a process
// without needing a database round-trip first -- shared by NewCrawlJobID
// and NewScheduledCrawlID, which were previously identical copies of this
// same scheme differing only in their prefix and which package-level
// counter they incremented.
func newSeqID(prefix string, seq *int64) string {
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), atomic.AddInt64(seq, 1))
}

// slugIDRE matches every run of characters a mintSlugID-derived ID must
// collapse away -- shared by every resource type that mints its own ID
// from an admin-typed display name (ChatHook, EmbeddingHTTPEndpoint).
var slugIDRE = regexp.MustCompile(`[^a-z0-9]+`)

// mintSlugID derives a <=20-character ID from name (lowercased,
// non-alphanumeric runs collapsed to "_", trimmed), appending the shortest
// numeric suffix that avoids colliding with a key in existing. Falls back
// to a timestamp-derived ID using fallbackPrefix if name has no
// alphanumeric characters at all. Shared by NewChatHookID and
// NewEmbeddingEndpointID, which were previously identical copies of this
// same algorithm differing only in their fallback prefix.
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
