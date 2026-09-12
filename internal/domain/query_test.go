package domain_test

import (
	"reflect"
	"sort"
	"testing"

	"searchengine/internal/domain"
)

func TestParseQuery_PlainWords(t *testing.T) {
	q := domain.ParseQuery("katzen hunde")
	if !reflect.DeepEqual(q.Optional, []string{"katzen", "hunde"}) {
		t.Errorf("expected two optional terms, got %+v", q)
	}
	if len(q.Required) != 0 || len(q.Excluded) != 0 || len(q.Phrases) != 0 {
		t.Errorf("expected no operators, got %+v", q)
	}
}

func TestParseQuery_RequiredWord(t *testing.T) {
	q := domain.ParseQuery("+katzen")
	if !reflect.DeepEqual(q.Required, []string{"katzen"}) {
		t.Errorf("expected 'katzen' required, got %+v", q.Required)
	}
}

func TestParseQuery_ExcludedWord(t *testing.T) {
	q := domain.ParseQuery("-hunde")
	if !reflect.DeepEqual(q.Excluded, []string{"hunde"}) {
		t.Errorf("expected 'hunde' excluded, got %+v", q.Excluded)
	}
}

func TestParseQuery_Phrase(t *testing.T) {
	q := domain.ParseQuery(`"katzen sind toll"`)
	if !reflect.DeepEqual(q.Phrases, []string{"katzen sind toll"}) {
		t.Errorf("expected one phrase, got %+v", q.Phrases)
	}
}

func TestParseQuery_Combination(t *testing.T) {
	q := domain.ParseQuery(`katzen +haustier -hund "sehr treu"`)
	if !reflect.DeepEqual(q.Optional, []string{"katzen"}) {
		t.Errorf("expected 'katzen' optional, got %+v", q.Optional)
	}
	if !reflect.DeepEqual(q.Required, []string{"haustier"}) {
		t.Errorf("expected 'haustier' required, got %+v", q.Required)
	}
	if !reflect.DeepEqual(q.Excluded, []string{"hund"}) {
		t.Errorf("expected 'hund' excluded, got %+v", q.Excluded)
	}
	if !reflect.DeepEqual(q.Phrases, []string{"sehr treu"}) {
		t.Errorf("expected one phrase, got %+v", q.Phrases)
	}
}

func TestParseQuery_MultiWordPlusIsTokenized(t *testing.T) {
	// "+word" only strips the leading '+'; Tokenize still applies (lowercasing etc).
	q := domain.ParseQuery("+Katzen")
	if !reflect.DeepEqual(q.Required, []string{"katzen"}) {
		t.Errorf("expected lowercased required term, got %+v", q.Required)
	}
}

func TestParsedQuery_AllTerms_DedupesAndExcludesExcluded(t *testing.T) {
	// "sind" is a German stopword, so it's tokenized out of the phrase's
	// contribution to AllTerms -- it still matters for the literal phrase
	// substring check in Matches, which doesn't tokenize.
	q := domain.ParseQuery(`katzen +katzen -hund "katzen sind toll"`)
	terms := q.AllTerms()
	sort.Strings(terms)
	want := []string{"katzen", "toll"}
	sort.Strings(want)
	if !reflect.DeepEqual(terms, want) {
		t.Errorf("expected %v, got %v", want, terms)
	}
}

func TestParseQuery_SiteFilter(t *testing.T) {
	q := domain.ParseQuery("cats site:example.com")
	if !reflect.DeepEqual(q.Optional, []string{"cats"}) {
		t.Errorf("expected 'cats' optional (site: stripped out), got %+v", q.Optional)
	}
	if !reflect.DeepEqual(q.Sites, []string{"example.com"}) {
		t.Errorf("expected 'example.com' as a site filter, got %+v", q.Sites)
	}
}

func TestParseQuery_SiteFilterBeforeTerm(t *testing.T) {
	q := domain.ParseQuery("site:example.com cats")
	if !reflect.DeepEqual(q.Optional, []string{"cats"}) {
		t.Errorf("expected 'cats' optional, got %+v", q.Optional)
	}
	if !reflect.DeepEqual(q.Sites, []string{"example.com"}) {
		t.Errorf("expected 'example.com' as a site filter, got %+v", q.Sites)
	}
}

func TestParseQuery_SiteFilterIsLowercased(t *testing.T) {
	q := domain.ParseQuery("Site:Example.COM")
	if !reflect.DeepEqual(q.Sites, []string{"example.com"}) {
		t.Errorf("expected lowercased site filter, got %+v", q.Sites)
	}
}

func TestParseQuery_MultipleSiteFilters(t *testing.T) {
	q := domain.ParseQuery("cats site:example.com site:example.org")
	want := []string{"example.com", "example.org"}
	if !reflect.DeepEqual(q.Sites, want) {
		t.Errorf("expected both site filters captured, got %+v", q.Sites)
	}
}

func TestParseQuery_SiteFilterCombinedWithOtherOperators(t *testing.T) {
	q := domain.ParseQuery(`katzen +haustier -hund "sehr treu" site:example.com`)
	if !reflect.DeepEqual(q.Optional, []string{"katzen"}) {
		t.Errorf("expected 'katzen' optional, got %+v", q.Optional)
	}
	if !reflect.DeepEqual(q.Required, []string{"haustier"}) {
		t.Errorf("expected 'haustier' required, got %+v", q.Required)
	}
	if !reflect.DeepEqual(q.Excluded, []string{"hund"}) {
		t.Errorf("expected 'hund' excluded, got %+v", q.Excluded)
	}
	if !reflect.DeepEqual(q.Phrases, []string{"sehr treu"}) {
		t.Errorf("expected one phrase, got %+v", q.Phrases)
	}
	if !reflect.DeepEqual(q.Sites, []string{"example.com"}) {
		t.Errorf("expected 'example.com' as a site filter, got %+v", q.Sites)
	}
}

func TestParseQuery_SiteFilterWithoutHostIsDropped(t *testing.T) {
	q := domain.ParseQuery("cats site:")
	if len(q.Sites) != 0 {
		t.Errorf("expected a bare 'site:' with no host to be dropped, got %+v", q.Sites)
	}
	if !reflect.DeepEqual(q.Optional, []string{"cats"}) {
		t.Errorf("expected 'cats' optional, got %+v", q.Optional)
	}
}

func TestParsedQuery_HasConstraints_SiteFilterAlone(t *testing.T) {
	q := domain.ParseQuery("cats site:example.com")
	if !q.HasConstraints() {
		t.Error("expected a site: filter alone to count as a constraint")
	}
}

func TestParsedQuery_SiteAllowed(t *testing.T) {
	q := domain.ParseQuery("cats site:example.com")
	if !q.SiteAllowed(domain.Document{URL: "https://example.com/page"}) {
		t.Error("expected an exact host match to be allowed")
	}
	if !q.SiteAllowed(domain.Document{URL: "https://www.example.com/page"}) {
		t.Error("expected a subdomain of the filtered site to be allowed")
	}
	if q.SiteAllowed(domain.Document{URL: "https://other.example/page"}) {
		t.Error("expected a non-matching host to be disallowed")
	}
}

func TestParsedQuery_SiteAllowed_MultipleSitesIsAnyOf(t *testing.T) {
	q := domain.ParseQuery("cats site:example.com site:example.org")
	if !q.SiteAllowed(domain.Document{URL: "https://example.org/page"}) {
		t.Error("expected a match on the second site filter to be allowed")
	}
	if q.SiteAllowed(domain.Document{URL: "https://example.net/page"}) {
		t.Error("expected a host matching neither filter to be disallowed")
	}
}

func TestParsedQuery_SiteAllowed_NoFilterAllowsEverything(t *testing.T) {
	q := domain.ParseQuery("cats")
	if !q.SiteAllowed(domain.Document{URL: "https://anything.example/page"}) {
		t.Error("expected no site: filter to allow any host")
	}
}

func TestParsedQuery_Empty(t *testing.T) {
	if !domain.ParseQuery("   ").Empty() {
		t.Error("expected blank query to be empty")
	}
	if !domain.ParseQuery("-hund").Empty() {
		t.Error("expected a query with only an excluded term to be empty (nothing to rank on)")
	}
	if domain.ParseQuery("+katzen").Empty() {
		t.Error("expected a query with a required term to not be empty")
	}
}

func TestParsedQuery_Matches_RequiredWord(t *testing.T) {
	q := domain.ParseQuery("+katzen")
	if !q.Matches("Titel", "Hier geht es um Katzen und Hunde.") {
		t.Error("expected required word present to match")
	}
	if q.Matches("Titel", "Hier geht es nur um Hunde.") {
		t.Error("expected required word absent to not match")
	}
}

func TestParsedQuery_Matches_ExcludedWord(t *testing.T) {
	q := domain.ParseQuery("-hunde")
	if q.Matches("Titel", "Hier geht es um Katzen und Hunde.") {
		t.Error("expected excluded word present to not match")
	}
	if !q.Matches("Titel", "Hier geht es nur um Katzen.") {
		t.Error("expected excluded word absent to match")
	}
}

func TestParsedQuery_Matches_Phrase(t *testing.T) {
	q := domain.ParseQuery(`"katzen sind toll"`)
	if !q.Matches("Titel", "Manche sagen: Katzen sind toll, andere nicht.") {
		t.Error("expected exact phrase present to match")
	}
	if q.Matches("Titel", "Katzen sind wirklich toll.") {
		t.Error("expected words out of order (not an exact phrase) to not match")
	}
}

func TestParsedQuery_Matches_Combination(t *testing.T) {
	q := domain.ParseQuery(`+katzen -bellen "sehr verspielt"`)
	if !q.Matches("Titel", "Katzen sind sehr verspielt und schnurren gerne.") {
		t.Error("expected all constraints satisfied to match")
	}
	if q.Matches("Titel", "Katzen bellen und sind sehr verspielt.") {
		t.Error("expected excluded word to veto an otherwise-matching document")
	}
}

func TestParsedQuery_Matches_NoConstraintsAlwaysTrue(t *testing.T) {
	q := domain.ParseQuery("katzen")
	if !q.Matches("anything", "anything at all") {
		t.Error("expected a plain query with no operators to match unconditionally")
	}
}

// TestParsedQuery_MatchesTokens_NoConstraintsAlwaysTrue exercises
// MatchesTokens directly (not through the Matches wrapper, which never
// even calls it when the query has no constraints) -- a caller like
// hybrid_search_service.go, which calls MatchesTokens unconditionally
// whenever ranking overrides are configured, must still get "always
// matches" for a plain query with no operators, regardless of tokens.
func TestParsedQuery_MatchesTokens_NoConstraintsAlwaysTrue(t *testing.T) {
	q := domain.ParseQuery("katzen")
	if !q.MatchesTokens(nil, "anything", "anything at all") {
		t.Error("expected a plain query with no operators to match unconditionally")
	}
}
