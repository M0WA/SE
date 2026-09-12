package domain

import "strings"

// Snippet extracts a text window around the first match and highlights matches.
func Snippet(text string, terms []string, maxLen int) string {
	lower := strings.ToLower(text)
	pos := -1
	for _, t := range terms {
		if p := strings.Index(lower, t); p != -1 && (pos == -1 || p < pos) {
			pos = p
		}
	}
	if pos == -1 {
		if len(text) <= maxLen {
			return text
		}
		return text[:maxLen] + "…"
	}

	start := pos - 60
	if start < 0 {
		start = 0
	}
	end := start + maxLen
	if end > len(text) {
		end = len(text)
	}
	snippet := text[start:end]

	for _, t := range terms {
		snippet = highlight(snippet, t)
	}
	if start > 0 {
		snippet = "… " + snippet
	}
	if end < len(text) {
		snippet += " …"
	}
	return snippet
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
