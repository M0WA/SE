package domain_test

import (
	"reflect"
	"testing"

	"searchengine/internal/domain"
)

func TestTokenize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"leerer String", "", nil},
		{"Stoppwörter gefiltert", "Der Hund und die Katze", []string{"hund", "katze"}},
		{"Satzzeichen entfernt", "Web-Suche, Version 2.0!", []string{"web", "suche", "version"}},
		{"kurze Tokens verworfen", "KI ist gut", []string{"gut"}},
		{"Umlaute bleiben erhalten", "Größe Straße", []string{"größe", "straße"}},
		{"nur Stoppwörter", "der die das", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := domain.Tokenize(c.in)
			if len(got) == 0 && len(c.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("Tokenize(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}
