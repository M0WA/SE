package domain

import (
	"html"
	"regexp"
	"strings"
)

// Snippet extracts a text window around the first match and highlights it.
// phrases take priority over terms for centering and highlighting -- a
// phrase marks one contiguous span, and terms only highlight outside it, so
// nothing double-wraps. text is untrusted crawled content rendered as HTML,
// so it's always escaped before any <mark> tag is added (a term containing
// an HTML metacharacter like "AT&T" may then fail to highlight -- acceptable).
func Snippet(text string, phrases, terms []string, maxLen int) string {
	lower := strings.ToLower(text)
	pos := indexOfEarliest(lower, phrases)
	if pos == -1 {
		pos = indexOfEarliest(lower, terms)
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

	for _, p := range phrases {
		snippet = highlight(snippet, p)
	}
	for _, t := range terms {
		snippet = highlightOutsideMarks(snippet, t)
	}
	if start > 0 {
		snippet = "… " + snippet
	}
	if end < len(text) {
		snippet += " …"
	}
	return snippet
}

// indexOfEarliest returns the lowest index at which any of words occurs in
// lower (already-lowercased haystack), or -1 if none of them occur at all.
func indexOfEarliest(lower string, words []string) int {
	pos := -1
	for _, w := range words {
		if p := strings.Index(lower, w); p != -1 && (pos == -1 || p < pos) {
			pos = p
		}
	}
	return pos
}

func highlight(s, term string) string {
	lower := strings.ToLower(s)
	var b strings.Builder
	i := 0
	for {
		idx := strings.Index(lower[i:], term)
		if idx == -1 {
			b.WriteString(s[i:])
			break
		}
		start := i + idx
		end := start + len(term)
		b.WriteString(s[i:start])
		b.WriteString("<mark>")
		b.WriteString(s[start:end])
		b.WriteString("</mark>")
		i = end
	}
	return b.String()
}

var markRe = regexp.MustCompile(`(?s)<mark>.*?</mark>`)

// highlightOutsideMarks is highlight, but skips any text already wrapped in
// a <mark> span from a prior (higher-priority) highlight pass -- so a
// phrase's own words don't get separately re-wrapped inside the phrase's
// own contiguous span.
func highlightOutsideMarks(s, term string) string {
	spans := markRe.FindAllStringIndex(s, -1)
	if spans == nil {
		return highlight(s, term)
	}
	var b strings.Builder
	last := 0
	for _, span := range spans {
		b.WriteString(highlight(s[last:span[0]], term))
		b.WriteString(s[span[0]:span[1]])
		last = span[1]
	}
	b.WriteString(highlight(s[last:], term))
	return b.String()
}
