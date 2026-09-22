package domain

import (
	"fmt"
	"strings"
)

// TruncateWithNote caps s at max bytes, appending a note naming the
// original length when it does -- shared by every caller that needs to
// bound how much of some untrusted/unbounded text (a hook script's stdout,
// a hook result fed back to a chat model) it keeps, while still telling
// whoever reads the truncated result how much was cut, rather than letting
// it look complete when it isn't.
func TruncateWithNote(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes] + fmt.Sprintf("... [truncated, %d bytes total]", len(s))
}

// TruncateWithEllipsis caps s at max bytes, appending a plain "..." when it
// does -- shared by every HTTP-client adapter (httpembed, httpchat) that
// bounds how much of a non-2xx response body carries into an error message,
// so a large HTML error page doesn't blow up a log line. Distinct from
// TruncateWithNote's own "... [truncated, N bytes total]" suffix -- these
// adapters' error text predates that format and changing it would change
// the exact error text they produce.
func TruncateWithEllipsis(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes] + "..."
}

// RedactSecret replaces every occurrence of secret in s with "[REDACTED]",
// or returns s unchanged if secret is empty -- shared by every HTTP-client
// adapter that must keep an API key out of an error message a gateway
// might otherwise echo back in a response body (that error can end up
// persisted to a crawl job's record or shown in an admin page, so this
// isn't just a log-line concern).
func RedactSecret(s, secret string) string {
	if secret == "" {
		return s
	}
	return strings.ReplaceAll(s, secret, "[REDACTED]")
}

// ApproxCharsPerToken estimates a text's token count from its character
// count when no real tokenizer is available (httpembed's chunking when no
// TokenizeURL is configured, application.estimateTokens for the chat
// context budget). Deliberately lower than real English's ~4 chars/token
// so the estimate errs toward assuming MORE tokens than there really are --
// overflowing a token budget is the failure mode this exists to prevent,
// in both callers.
const ApproxCharsPerToken = 3
