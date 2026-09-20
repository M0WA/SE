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
	pos := indexOfEarliest(text, phrases)
	if pos == -1 {
		pos = indexOfEarliest(text, terms)
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

// caseInsensitiveFinder compiles word into a case-insensitive regexp whose
// match offsets are always valid byte offsets within whatever string it is
// matched against -- unlike searching a separately lowercased copy (the
// previous approach here), which silently breaks whenever
// strings.ToLower changes a string's byte length, as it does for some
// Unicode characters (e.g. Turkish İ, German ẞ): offsets found in the
// lowercased copy no longer line up with the original string, and slicing
// the original at those offsets can read out of range or misalign
// entirely. This was the root cause of a real crash ("slice bounds out of
// range") triggered by ordinary crawled content containing such a
// character. Returns nil (matches nothing) for an empty word or one that
// fails to compile as a regexp (defensive -- Tokenize-derived words
// shouldn't normally fail this).
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

// indexOfEarliest returns the lowest byte offset at which any of words
// occurs in text (case-insensitively), or -1 if none of them occur at all.
func indexOfEarliest(text string, words []string) int {
	pos := -1
	for _, w := range words {
		re := caseInsensitiveFinder(w)
		if re == nil {
			continue
		}
		if loc := re.FindStringIndex(text); loc != nil && (pos == -1 || loc[0] < pos) {
			pos = loc[0]
		}
	}
	return pos
}

func highlight(s, term string) string {
	re := caseInsensitiveFinder(term)
	if re == nil {
		return s
	}
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
