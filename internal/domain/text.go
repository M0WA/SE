package domain

import (
	"fmt"
	"strings"
)

// TruncateWithNote caps s at max bytes, appending a note naming the
// original length -- so truncated, unbounded text (a hook's stdout, a
// tool result fed to a chat model) never looks complete when it isn't.
func TruncateWithNote(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes] + fmt.Sprintf("... [truncated, %d bytes total]", len(s))
}

// TruncateWithEllipsis caps s at max bytes, appending plain "..." -- used by
// HTTP-client adapters bounding a non-2xx response body in an error
// message. Kept distinct from TruncateWithNote to preserve their exact,
// pre-existing error text.
func TruncateWithEllipsis(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes] + "..."
}

// RedactSecret replaces every occurrence of secret in s with "[REDACTED]"
// (no-op if secret is empty) -- keeps an API key out of error text that
// could end up persisted or shown in an admin page.
func RedactSecret(s, secret string) string {
	if secret == "" {
		return s
	}
	return strings.ReplaceAll(s, secret, "[REDACTED]")
}

// ApproxCharsPerToken estimates token count from character count when no
// real tokenizer is available. Deliberately lower than English's ~4
// chars/token, so it errs toward overestimating rather than overflowing a
// token budget.
const ApproxCharsPerToken = 3
