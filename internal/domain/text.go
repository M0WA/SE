package domain

import "fmt"

// TruncateWithNote caps s at max bytes, appending a note naming the
// original length when it does -- shared by every caller that needs to
// bound how much of some untrusted/unbounded text (a hook script's stdout,
// a hook result fed back to a chat model) it keeps, while still telling
// whoever reads the truncated result how much was cut, rather than letting
// it look complete when it isn't.
func TruncateWithNote(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("... [truncated, %d bytes total]", len(s))
}
