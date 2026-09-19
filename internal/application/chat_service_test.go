package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// fakeChatEndpointStore is a minimal ports.ChatEndpointStore fake: a
// single stored endpoint plus an error to return instead, mirroring the
// port's Get/Set-on-one-row shape (no need for a map of many, unlike
// EmbeddingEndpointStore).
type fakeChatEndpointStore struct {
	endpoint domain.ChatEndpoint
	getErr   error
}

func (f *fakeChatEndpointStore) GetChatEndpoint(ctx context.Context) (domain.ChatEndpoint, error) {
	if f.getErr != nil {
		return domain.ChatEndpoint{}, f.getErr
	}
	return f.endpoint, nil
}

func (f *fakeChatEndpointStore) SetChatEndpoint(ctx context.Context, e domain.ChatEndpoint) error {
	f.endpoint = e
	return nil
}

// fakeChatCompleter is a minimal ports.ChatCompleter fake recording the
// messages it was called with, so a test can assert whether/what RAG
// context got prepended.
type fakeChatCompleter struct {
	answer      string
	err         error
	calledWith  []domain.ChatMessage
	calledEndpt domain.ChatEndpoint
	wasCalled   bool
}

func (f *fakeChatCompleter) Complete(ctx context.Context, endpoint domain.ChatEndpoint, messages []domain.ChatMessage) (string, error) {
	f.wasCalled = true
	f.calledEndpt = endpoint
	f.calledWith = messages
	if f.err != nil {
		return "", f.err
	}
	return f.answer, nil
}

// fakeSearchService is a minimal ports.SearchService fake recording the
// query/opts it was called with.
type fakeSearchService struct {
	results     []domain.SearchResult
	err         error
	wasCalled   bool
	calledQuery string
	calledOpts  ports.SearchQuery
}

func (f *fakeSearchService) Search(ctx context.Context, query string, opts ports.SearchQuery) ([]domain.SearchResult, error) {
	f.wasCalled = true
	f.calledQuery = query
	f.calledOpts = opts
	if f.err != nil {
		return nil, f.err
	}
	return f.results, nil
}

// fakeWebSearcher is a minimal ports.WebSearcher fake recording the
// baseURL/query/count it was called with.
type fakeWebSearcher struct {
	results     []domain.WebSearchResult
	err         error
	wasCalled   bool
	calledURL   string
	calledQuery string
	calledCount int
}

func (f *fakeWebSearcher) Search(ctx context.Context, baseURL, query string, count int) ([]domain.WebSearchResult, error) {
	f.wasCalled = true
	f.calledURL = baseURL
	f.calledQuery = query
	f.calledCount = count
	if f.err != nil {
		return nil, f.err
	}
	return f.results, nil
}

func TestChatService_EmptyHistory(t *testing.T) {
	svc := NewChatService(&fakeChatEndpointStore{}, &fakeChatCompleter{}, &fakeSearchService{}, &fakeWebSearcher{})
	_, err := svc.Chat(context.Background(), nil, ChatOptions{})
	if err == nil {
		t.Fatal("expected error for empty history, got nil")
	}
}

func TestChatService_NotConfigured(t *testing.T) {
	wantErr := errors.New("boom")
	endpoints := &fakeChatEndpointStore{getErr: wantErr}
	svc := NewChatService(endpoints, &fakeChatCompleter{}, &fakeSearchService{}, &fakeWebSearcher{})

	_, err := svc.Chat(context.Background(), []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}, ChatOptions{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected wrapped/equal sentinel error, got %v", err)
	}
}

func TestChatService_NotConfigured_ErrIsPreserved(t *testing.T) {
	endpoints := &fakeChatEndpointStore{getErr: ports.ErrChatEndpointNotConfigured}
	svc := NewChatService(endpoints, &fakeChatCompleter{}, &fakeSearchService{}, &fakeWebSearcher{})

	_, err := svc.Chat(context.Background(), []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}, ChatOptions{})
	if !errors.Is(err, ports.ErrChatEndpointNotConfigured) {
		t.Fatalf("expected errors.Is to match ErrChatEndpointNotConfigured, got %v", err)
	}
}

func TestChatService_DisabledEndpoint(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: false}}
	svc := NewChatService(endpoints, &fakeChatCompleter{}, &fakeSearchService{}, &fakeWebSearcher{})

	_, err := svc.Chat(context.Background(), []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}, ChatOptions{})
	if !errors.Is(err, ports.ErrChatEndpointNotConfigured) {
		t.Fatalf("expected ErrChatEndpointNotConfigured, got %v", err)
	}
}

func TestChatService_RAGDisabled_SearchNeverCalled(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, RAGEnabled: false}}
	completer := &fakeChatCompleter{answer: "the answer"}
	search := &fakeSearchService{results: []domain.SearchResult{{URL: "http://x", Title: "X", Snippet: "snip"}}}
	svc := NewChatService(endpoints, completer, search, &fakeWebSearcher{})

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if search.wasCalled {
		t.Fatal("expected search not to be called when RAG disabled")
	}
	if result.Answer != "the answer" {
		t.Fatalf("unexpected answer: %q", result.Answer)
	}
	if len(result.Sources) != 0 {
		t.Fatalf("expected no sources, got %v", result.Sources)
	}
	if len(completer.calledWith) != 1 || completer.calledWith[0].Content != "hi" {
		t.Fatalf("expected plain history passed through, got %v", completer.calledWith)
	}
}

func TestChatService_RAGEnabled_WithResults(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{
		Enabled:        true,
		RAGEnabled:     true,
		RAGResultCount: 3,
	}}
	completer := &fakeChatCompleter{answer: "rag answer"}
	search := &fakeSearchService{results: []domain.SearchResult{
		{URL: "http://a", Title: "A", Snippet: "snippet a"},
		{URL: "http://b", Title: "B", Snippet: "snippet b"},
	}}
	svc := NewChatService(endpoints, completer, search, &fakeWebSearcher{})

	history := []domain.ChatMessage{
		{Role: domain.ChatRoleSystem, Content: "be nice"},
		{Role: domain.ChatRoleUser, Content: "first question"},
		{Role: domain.ChatRoleAssistant, Content: "first answer"},
		{Role: domain.ChatRoleUser, Content: "second question"},
	}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !search.wasCalled {
		t.Fatal("expected search to be called when RAG enabled")
	}
	if search.calledQuery != "second question" {
		t.Fatalf("expected search on last user message, got %q", search.calledQuery)
	}
	if search.calledOpts.TopK != 3 {
		t.Fatalf("expected TopK from endpoint.RAGResultCount, got %d", search.calledOpts.TopK)
	}
	if len(result.Sources) != 2 || result.Sources[0].URL != "http://a" || result.Sources[1].URL != "http://b" {
		t.Fatalf("unexpected sources: %v", result.Sources)
	}
	if len(completer.calledWith) != len(history)+1 {
		t.Fatalf("expected one prepended system message, got %d messages", len(completer.calledWith))
	}
	if completer.calledWith[0].Role != domain.ChatRoleSystem {
		t.Fatalf("expected first message to be the RAG system message, got role %q", completer.calledWith[0].Role)
	}
	if completer.calledWith[1] != history[0] {
		t.Fatalf("expected original history preserved after prepended message")
	}
	if result.Answer != "rag answer" {
		t.Fatalf("unexpected answer: %q", result.Answer)
	}
}

func TestChatService_RAGEnabled_NoUserMessage(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, RAGEnabled: true, RAGResultCount: 5}}
	completer := &fakeChatCompleter{answer: "answer"}
	search := &fakeSearchService{results: []domain.SearchResult{{URL: "http://a", Title: "A"}}}
	svc := NewChatService(endpoints, completer, search, &fakeWebSearcher{})

	history := []domain.ChatMessage{{Role: domain.ChatRoleSystem, Content: "system only"}}
	_, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if search.wasCalled {
		t.Fatal("expected search not to be called when history has no user message")
	}
}

func TestChatService_RAGEnabled_SearchErrorFallsBack(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, RAGEnabled: true, RAGResultCount: 5}}
	completer := &fakeChatCompleter{answer: "plain answer"}
	search := &fakeSearchService{err: errors.New("search backend down")}
	svc := NewChatService(endpoints, completer, search, &fakeWebSearcher{})

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("expected search error to be swallowed, got %v", err)
	}
	if result.Answer != "plain answer" {
		t.Fatalf("unexpected answer: %q", result.Answer)
	}
	if len(result.Sources) != 0 {
		t.Fatalf("expected no sources on search error, got %v", result.Sources)
	}
	if len(completer.calledWith) != 1 {
		t.Fatalf("expected plain history passed through on search error, got %v", completer.calledWith)
	}
}

func TestChatService_RAGEnabled_EmptyResults(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, RAGEnabled: true, RAGResultCount: 5}}
	completer := &fakeChatCompleter{answer: "plain answer"}
	search := &fakeSearchService{results: []domain.SearchResult{}}
	svc := NewChatService(endpoints, completer, search, &fakeWebSearcher{})

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Sources) != 0 {
		t.Fatalf("expected no sources for empty results, got %v", result.Sources)
	}
	if len(completer.calledWith) != 1 {
		t.Fatalf("expected no RAG message prepended for empty results, got %v", completer.calledWith)
	}
}

func TestChatService_RAGOverride_TrueOverridesDisabledEndpoint(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, RAGEnabled: false, RAGResultCount: 3}}
	completer := &fakeChatCompleter{answer: "answer"}
	search := &fakeSearchService{results: []domain.SearchResult{{URL: "http://a", Title: "A", Snippet: "snip"}}}
	svc := NewChatService(endpoints, completer, search, &fakeWebSearcher{})

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	ragOn := true
	result, err := svc.Chat(context.Background(), history, ChatOptions{RAG: &ragOn})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !search.wasCalled {
		t.Fatal("expected search to be called when rag override is true, even with RAGEnabled false")
	}
	if len(result.Sources) != 1 {
		t.Fatalf("expected one source, got %v", result.Sources)
	}
}

func TestChatService_RAGOverride_FalseOverridesEnabledEndpoint(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, RAGEnabled: true, RAGResultCount: 3}}
	completer := &fakeChatCompleter{answer: "answer"}
	search := &fakeSearchService{results: []domain.SearchResult{{URL: "http://a", Title: "A", Snippet: "snip"}}}
	svc := NewChatService(endpoints, completer, search, &fakeWebSearcher{})

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	ragOff := false
	result, err := svc.Chat(context.Background(), history, ChatOptions{RAG: &ragOff})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if search.wasCalled {
		t.Fatal("expected search not to be called when rag override is false, even with RAGEnabled true")
	}
	if len(result.Sources) != 0 {
		t.Fatalf("expected no sources, got %v", result.Sources)
	}
}

func TestChatService_WebSearchDisabled_WebSearcherNeverCalled(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, WebSearchEnabled: false, WebSearchBaseURL: "http://searx.example"}}
	completer := &fakeChatCompleter{answer: "the answer"}
	webSearch := &fakeWebSearcher{results: []domain.WebSearchResult{{URL: "http://x", Title: "X", Snippet: "snip"}}}
	svc := NewChatService(endpoints, completer, &fakeSearchService{}, webSearch)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if webSearch.wasCalled {
		t.Fatal("expected web search not to be called when WebSearchEnabled is false")
	}
	if len(result.Sources) != 0 {
		t.Fatalf("expected no sources, got %v", result.Sources)
	}
}

func TestChatService_WebSearchEnabled_WithResults(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{
		Enabled: true, WebSearchEnabled: true, WebSearchBaseURL: "http://searx.example", WebSearchResultCount: 3,
	}}
	completer := &fakeChatCompleter{answer: "web answer"}
	webSearch := &fakeWebSearcher{results: []domain.WebSearchResult{
		{URL: "http://a", Title: "A", Snippet: "snippet a"},
		{URL: "http://b", Title: "B", Snippet: "snippet b"},
	}}
	svc := NewChatService(endpoints, completer, &fakeSearchService{}, webSearch)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "second question"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !webSearch.wasCalled {
		t.Fatal("expected web search to be called when WebSearchEnabled")
	}
	if webSearch.calledURL != "http://searx.example" {
		t.Fatalf("expected baseURL from endpoint.WebSearchBaseURL, got %q", webSearch.calledURL)
	}
	if webSearch.calledQuery != "second question" {
		t.Fatalf("expected search on last user message, got %q", webSearch.calledQuery)
	}
	if webSearch.calledCount != 3 {
		t.Fatalf("expected count from endpoint.WebSearchResultCount, got %d", webSearch.calledCount)
	}
	if len(result.Sources) != 2 || result.Sources[0].URL != "http://a" || result.Sources[1].URL != "http://b" {
		t.Fatalf("unexpected sources: %v", result.Sources)
	}
	if len(completer.calledWith) != len(history)+1 {
		t.Fatalf("expected one prepended system message, got %d messages", len(completer.calledWith))
	}
	if completer.calledWith[0].Role != domain.ChatRoleSystem {
		t.Fatalf("expected first message to be the web-search system message, got role %q", completer.calledWith[0].Role)
	}
	if result.Answer != "web answer" {
		t.Fatalf("unexpected answer: %q", result.Answer)
	}
}

func TestChatService_WebSearchEnabled_NoBaseURLSkipsSearch(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, WebSearchEnabled: true, WebSearchBaseURL: ""}}
	completer := &fakeChatCompleter{answer: "answer"}
	webSearch := &fakeWebSearcher{results: []domain.WebSearchResult{{URL: "http://a", Title: "A"}}}
	svc := NewChatService(endpoints, completer, &fakeSearchService{}, webSearch)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	if _, err := svc.Chat(context.Background(), history, ChatOptions{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if webSearch.wasCalled {
		t.Fatal("expected web search not to be called when WebSearchBaseURL is empty")
	}
}

func TestChatService_WebSearchEnabled_SearchErrorFallsBack(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, WebSearchEnabled: true, WebSearchBaseURL: "http://searx.example", WebSearchResultCount: 5}}
	completer := &fakeChatCompleter{answer: "plain answer"}
	webSearch := &fakeWebSearcher{err: errors.New("searxng down")}
	svc := NewChatService(endpoints, completer, &fakeSearchService{}, webSearch)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("expected web search error to be swallowed, got %v", err)
	}
	if result.Answer != "plain answer" {
		t.Fatalf("unexpected answer: %q", result.Answer)
	}
	if len(result.Sources) != 0 {
		t.Fatalf("expected no sources on web search error, got %v", result.Sources)
	}
	if len(completer.calledWith) != 1 {
		t.Fatalf("expected plain history passed through on web search error, got %v", completer.calledWith)
	}
}

func TestChatService_WebSearchEnabled_EmptyResults(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, WebSearchEnabled: true, WebSearchBaseURL: "http://searx.example", WebSearchResultCount: 5}}
	completer := &fakeChatCompleter{answer: "plain answer"}
	webSearch := &fakeWebSearcher{results: []domain.WebSearchResult{}}
	svc := NewChatService(endpoints, completer, &fakeSearchService{}, webSearch)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Sources) != 0 {
		t.Fatalf("expected no sources for empty results, got %v", result.Sources)
	}
	if len(completer.calledWith) != 1 {
		t.Fatalf("expected no system message prepended for empty results, got %v", completer.calledWith)
	}
}

func TestChatService_WebSearchOverride_TrueOverridesDisabledEndpoint(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, WebSearchEnabled: false, WebSearchBaseURL: "http://searx.example", WebSearchResultCount: 3}}
	completer := &fakeChatCompleter{answer: "answer"}
	webSearch := &fakeWebSearcher{results: []domain.WebSearchResult{{URL: "http://a", Title: "A"}}}
	svc := NewChatService(endpoints, completer, &fakeSearchService{}, webSearch)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	on := true
	result, err := svc.Chat(context.Background(), history, ChatOptions{WebSearch: &on})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !webSearch.wasCalled {
		t.Fatal("expected web search to be called when override is true, even with WebSearchEnabled false")
	}
	if len(result.Sources) != 1 {
		t.Fatalf("expected one source, got %v", result.Sources)
	}
}

func TestChatService_WebSearchOverride_FalseOverridesEnabledEndpoint(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, WebSearchEnabled: true, WebSearchBaseURL: "http://searx.example", WebSearchResultCount: 3}}
	completer := &fakeChatCompleter{answer: "answer"}
	webSearch := &fakeWebSearcher{results: []domain.WebSearchResult{{URL: "http://a", Title: "A"}}}
	svc := NewChatService(endpoints, completer, &fakeSearchService{}, webSearch)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	off := false
	result, err := svc.Chat(context.Background(), history, ChatOptions{WebSearch: &off})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if webSearch.wasCalled {
		t.Fatal("expected web search not to be called when override is false, even with WebSearchEnabled true")
	}
	if len(result.Sources) != 0 {
		t.Fatalf("expected no sources, got %v", result.Sources)
	}
}

// TestChatService_RAGAndWebSearchBothEnabled_CombinedIntoOneSystemMessage
// proves both sources can contribute to the same turn: one system message
// carries both sections, and sources from both are surfaced together.
func TestChatService_RAGAndWebSearchBothEnabled_CombinedIntoOneSystemMessage(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{
		Enabled:    true,
		RAGEnabled: true, RAGResultCount: 5,
		WebSearchEnabled: true, WebSearchBaseURL: "http://searx.example", WebSearchResultCount: 5,
	}}
	completer := &fakeChatCompleter{answer: "combined answer"}
	search := &fakeSearchService{results: []domain.SearchResult{{URL: "http://local", Title: "Local"}}}
	webSearch := &fakeWebSearcher{results: []domain.WebSearchResult{{URL: "http://web", Title: "Web"}}}
	svc := NewChatService(endpoints, completer, search, webSearch)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !search.wasCalled || !webSearch.wasCalled {
		t.Fatal("expected both sources to be queried")
	}
	if len(result.Sources) != 2 || result.Sources[0].URL != "http://local" || result.Sources[1].URL != "http://web" {
		t.Fatalf("expected sources from both sources, got %v", result.Sources)
	}
	// Exactly one system message, not two -- both sections folded together.
	systemCount := 0
	for _, m := range completer.calledWith {
		if m.Role == domain.ChatRoleSystem {
			systemCount++
		}
	}
	if systemCount != 1 {
		t.Fatalf("expected exactly one combined system message, got %d", systemCount)
	}
	if !strings.Contains(completer.calledWith[0].Content, "http://local") || !strings.Contains(completer.calledWith[0].Content, "http://web") {
		t.Fatalf("expected the combined system message to mention both sources, got %q", completer.calledWith[0].Content)
	}
}

func TestChatService_MaxContextTokens_Zero_NoTrimming(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, MaxContextTokens: 0}}
	completer := &fakeChatCompleter{answer: "answer"}
	svc := NewChatService(endpoints, completer, &fakeSearchService{}, &fakeWebSearcher{})

	history := []domain.ChatMessage{
		{Role: domain.ChatRoleUser, Content: strings.Repeat("x", 100)},
		{Role: domain.ChatRoleAssistant, Content: strings.Repeat("y", 100)},
		{Role: domain.ChatRoleUser, Content: strings.Repeat("z", 100)},
	}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(completer.calledWith) != len(history) {
		t.Fatalf("expected no trimming with MaxContextTokens=0, got %d messages", len(completer.calledWith))
	}
	if result.ContextTrimmed {
		t.Error("expected ContextTrimmed=false when MaxContextTokens=0 disables trimming")
	}
}

func TestChatService_MaxContextTokens_TrimsOldestMessages(t *testing.T) {
	// Budget fits only the newest message (30 chars ~= 10 tokens) -- each
	// older 90-char (~30-token) message would blow past 15.
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, MaxContextTokens: 15}}
	completer := &fakeChatCompleter{answer: "answer"}
	svc := NewChatService(endpoints, completer, &fakeSearchService{}, &fakeWebSearcher{})

	history := []domain.ChatMessage{
		{Role: domain.ChatRoleUser, Content: strings.Repeat("a", 90)},
		{Role: domain.ChatRoleAssistant, Content: strings.Repeat("b", 90)},
		{Role: domain.ChatRoleUser, Content: strings.Repeat("c", 30)},
	}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(completer.calledWith) != 1 || completer.calledWith[0].Content != history[2].Content {
		t.Fatalf("expected only the most recent message kept, got %v", completer.calledWith)
	}
	if !result.ContextTrimmed {
		t.Error("expected ContextTrimmed=true when older messages were dropped")
	}
}

func TestChatService_MaxContextTokens_KeepsAsManyRecentMessagesAsFit(t *testing.T) {
	// Budget fits the newest (10 tokens) plus the one before it (30 tokens)
	// but not the oldest (another 30 tokens): 10+30=40 <= 40 < 70.
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, MaxContextTokens: 40}}
	completer := &fakeChatCompleter{answer: "answer"}
	svc := NewChatService(endpoints, completer, &fakeSearchService{}, &fakeWebSearcher{})

	history := []domain.ChatMessage{
		{Role: domain.ChatRoleUser, Content: strings.Repeat("a", 90)},
		{Role: domain.ChatRoleAssistant, Content: strings.Repeat("b", 90)},
		{Role: domain.ChatRoleUser, Content: strings.Repeat("c", 30)},
	}
	if _, err := svc.Chat(context.Background(), history, ChatOptions{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(completer.calledWith) != 2 || completer.calledWith[0] != history[1] || completer.calledWith[1] != history[2] {
		t.Fatalf("expected the two newest messages kept, got %v", completer.calledWith)
	}
}

func TestChatService_MaxContextTokens_AlwaysKeepsNewestMessageEvenIfOversized(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, MaxContextTokens: 1}}
	completer := &fakeChatCompleter{answer: "answer"}
	svc := NewChatService(endpoints, completer, &fakeSearchService{}, &fakeWebSearcher{})

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: strings.Repeat("a", 300)}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(completer.calledWith) != 1 {
		t.Fatalf("expected the single oversized message kept regardless of budget, got %v", completer.calledWith)
	}
	if result.ContextTrimmed {
		t.Error("expected ContextTrimmed=false when nothing was actually dropped (only message kept regardless)")
	}
}

func TestChatService_MaxContextTokens_KeepsRAGSystemMessageIntact(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{
		Enabled: true, RAGEnabled: true, RAGResultCount: 5, MaxContextTokens: 50,
	}}
	completer := &fakeChatCompleter{answer: "answer"}
	search := &fakeSearchService{results: []domain.SearchResult{{URL: "http://a", Title: "A", Snippet: strings.Repeat("s", 60)}}}
	svc := NewChatService(endpoints, completer, search, &fakeWebSearcher{})

	history := []domain.ChatMessage{
		{Role: domain.ChatRoleUser, Content: strings.Repeat("old", 30)},
		{Role: domain.ChatRoleAssistant, Content: strings.Repeat("old", 30)},
		{Role: domain.ChatRoleUser, Content: "newest question"},
	}
	if _, err := svc.Chat(context.Background(), history, ChatOptions{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(completer.calledWith) < 2 {
		t.Fatalf("expected at least the RAG system message plus the newest message, got %v", completer.calledWith)
	}
	if completer.calledWith[0].Role != domain.ChatRoleSystem {
		t.Fatalf("expected the RAG system message to survive trimming as the first message, got role %q", completer.calledWith[0].Role)
	}
	last := completer.calledWith[len(completer.calledWith)-1]
	if last.Content != "newest question" {
		t.Fatalf("expected the newest message to survive trimming, got %v", last)
	}
}

func TestEstimateTokens(t *testing.T) {
	messages := []domain.ChatMessage{{Content: "abcdef"}, {Content: "abc"}}
	if got, want := estimateTokens(messages), 3; got != want {
		t.Fatalf("estimateTokens() = %d, want %d", got, want)
	}
}

func TestTrimToBudget_UnderBudget_ReturnsUnchanged(t *testing.T) {
	messages := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	got := trimToBudget(messages, 1000)
	if len(got) != 1 {
		t.Fatalf("expected messages unchanged, got %v", got)
	}
}

func TestChatService_CompleterError(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	completer := &fakeChatCompleter{err: errors.New("upstream 500")}
	svc := NewChatService(endpoints, completer, &fakeSearchService{}, &fakeWebSearcher{})

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	_, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err == nil {
		t.Fatal("expected error from completer to propagate")
	}
}
