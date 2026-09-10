package domain

import (
	"regexp"
	"strings"
)

var stopwords = map[string]struct{}{
	"der": {}, "die": {}, "das": {}, "und": {}, "oder": {}, "ein": {}, "eine": {},
	"ist": {}, "sind": {}, "von": {}, "zu": {}, "im": {}, "in": {}, "mit": {},
	"auf": {}, "für": {}, "als": {}, "auch": {}, "an": {}, "den": {}, "des": {},
	"dem": {}, "sich": {}, "wird": {}, "werden": {}, "wurde": {}, "nicht": {},
	"es": {}, "er": {}, "sie": {}, "the": {}, "and": {}, "of": {}, "to": {},
	"is": {}, "are": {}, "was": {}, "were": {}, "for": {}, "at": {}, "by": {},
}

var nonWordRe = regexp.MustCompile(`[^a-zA-ZäöüßÄÖÜ0-9\s]+`)

// Tokenize normalisiert Text zu bedeutungstragenden Kleinbuchstaben-Termen:
// Satzzeichen entfernen, auf Wörter splitten, Stoppwörter und kurze Tokens filtern.
func Tokenize(text string) []string {
	cleaned := nonWordRe.ReplaceAllString(strings.ToLower(text), " ")
	fields := strings.Fields(cleaned)

	tokens := make([]string, 0, len(fields))
	for _, f := range fields {
		if len(f) <= 2 {
			continue
		}
		if _, stop := stopwords[f]; stop {
			continue
		}
		tokens = append(tokens, f)
	}
	return tokens
}
