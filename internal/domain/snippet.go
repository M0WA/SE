package domain

import (
	"html"
	"regexp"
	"strings"
)

// Snippet extracts a text window around the first match and highlights it.
// phrases take priority over terms for centering/highlighting so nothing
// double-wraps. text is untrusted, so it's always HTML-escaped before any
// <mark> tag is added (a term with an HTML metacharacter like "AT&T" may
// then fail to highlight -- acceptable).
func Snippet(text string, phrases, terms []string, maxLen int) string {
	// Compiled once per call and reused across every use below, avoiding
	// recompiling the same pattern up to four times over.
	phraseFinders := compileFinders(phrases)
	termFinders := compileFinders(terms)

	pos := indexOfEarliest(text, phraseFinders)
	if pos == -1 {
		pos = indexOfEarliest(text, termFinders)
	}
	if pos == -1 {
		if len(text) <= maxLen {
			return html.EscapeString(text)
		}
		return html.EscapeString(text[:maxLen]) + "…"
	}

	start := pos - 60
	if start < 0 {
		start = 0
	}
	end := start + maxLen
	if end > len(text) {
		end = len(text)
	}
	snippet := html.EscapeString(text[start:end])

	for _, re := range phraseFinders {
		snippet = highlight(snippet, re)
	}
	for _, re := range termFinders {
		snippet = highlightOutsideMarks(snippet, re)
	}
	if start > 0 {
		snippet = "… " + snippet
	}
	if end < len(text) {
		snippet += " …"
	}
	return snippet
}

// caseInsensitiveFinder compiles word into a case-insensitive regexp whose
// match offsets are always valid within the original string -- unlike
// searching a separately lowercased copy, which breaks when
// strings.ToLower changes byte length (e.g. Turkish İ, German ẞ),
// misaligning offsets and causing out-of-range slicing. Root cause of a
// real "slice bounds out of range" crash on ordinary crawled content.
// Returns nil for an empty or uncompilable word.
func caseInsensitiveFinder(word string) *regexp.Regexp {
	if word == "" {
		return nil
	}
	re, err := regexp.Compile(`(?i)` + regexp.QuoteMeta(word))
	if err != nil {
		return nil
	}
	return re
}

// compileFinders compiles every word via caseInsensitiveFinder, dropping any
// that return nil (empty or uncompilable) so callers never need to nil-check
// individual entries.
func compileFinders(words []string) []*regexp.Regexp {
	finders := make([]*regexp.Regexp, 0, len(words))
	for _, w := range words {
		if re := caseInsensitiveFinder(w); re != nil {
			finders = append(finders, re)
		}
	}
	return finders
}

// indexOfEarliest returns the lowest byte offset at which any of finders
// matches text, or -1 if none of them match at all.
func indexOfEarliest(text string, finders []*regexp.Regexp) int {
	pos := -1
	for _, re := range finders {
		if loc := re.FindStringIndex(text); loc != nil && (pos == -1 || loc[0] < pos) {
			pos = loc[0]
		}
	}
	return pos
}

// highlight is only ever called with a re from compileFinders' output
// (never nil), so it needs no nil check of its own.
func highlight(s string, re *regexp.Regexp) string {
	matches := re.FindAllStringIndex(s, -1)
	if matches == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range matches {
		b.WriteString(s[last:m[0]])
		b.WriteString("<mark>")
		b.WriteString(s[m[0]:m[1]])
		b.WriteString("</mark>")
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

var markRe = regexp.MustCompile(`(?s)<mark>.*?</mark>`)

// highlightOutsideMarks is highlight, but skips text already wrapped in a
// <mark> span from a prior pass, so a phrase's words aren't re-wrapped.
func highlightOutsideMarks(s string, re *regexp.Regexp) string {
	spans := markRe.FindAllStringIndex(s, -1)
	if spans == nil {
		return highlight(s, re)
	}
	var b strings.Builder
	last := 0
	for _, span := range spans {
		b.WriteString(highlight(s[last:span[0]], re))
		b.WriteString(s[span[0]:span[1]])
		last = span[1]
	}
	b.WriteString(highlight(s[last:], re))
	return b.String()
}
