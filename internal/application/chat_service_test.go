package application

import (
	"context"
	"errors"
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

func TestChatService_EmptyHistory(t *testing.T) {
	svc := NewChatService(&fakeChatEndpointStore{}, &fakeChatCompleter{}, &fakeSearchService{})
	_, err := svc.Chat(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected error for empty history, got nil")
	}
}

func TestChatService_NotConfigured(t *testing.T) {
	wantErr := errors.New("boom")
	endpoints := &fakeChatEndpointStore{getErr: wantErr}
	svc := NewChatService(endpoints, &fakeChatCompleter{}, &fakeSearchService{})

	_, err := svc.Chat(context.Background(), []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}, nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected wrapped/equal sentinel error, got %v", err)
	}
}

func TestChatService_NotConfigured_ErrIsPreserved(t *testing.T) {
	endpoints := &fakeChatEndpointStore{getErr: ports.ErrChatEndpointNotConfigured}
	svc := NewChatService(endpoints, &fakeChatCompleter{}, &fakeSearchService{})

	_, err := svc.Chat(context.Background(), []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}, nil)
	if !errors.Is(err, ports.ErrChatEndpointNotConfigured) {
		t.Fatalf("expected errors.Is to match ErrChatEndpointNotConfigured, got %v", err)
	}
}

func TestChatService_DisabledEndpoint(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: false}}
	svc := NewChatService(endpoints, &fakeChatCompleter{}, &fakeSearchService{})

	_, err := svc.Chat(context.Background(), []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}, nil)
	if !errors.Is(err, ports.ErrChatEndpointNotConfigured) {
		t.Fatalf("expected ErrChatEndpointNotConfigured, got %v", err)
	}
}

func TestChatService_RAGDisabled_SearchNeverCalled(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, RAGEnabled: false}}
	completer := &fakeChatCompleter{answer: "the answer"}
	search := &fakeSearchService{results: []domain.SearchResult{{URL: "http://x", Title: "X", Snippet: "snip"}}}
	svc := NewChatService(endpoints, completer, search)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	result, err := svc.Chat(context.Background(), history, nil)
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
	svc := NewChatService(endpoints, completer, search)

	history := []domain.ChatMessage{
		{Role: domain.ChatRoleSystem, Content: "be nice"},
		{Role: domain.ChatRoleUser, Content: "first question"},
		{Role: domain.ChatRoleAssistant, Content: "first answer"},
		{Role: domain.ChatRoleUser, Content: "second question"},
	}
	result, err := svc.Chat(context.Background(), history, nil)
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
	svc := NewChatService(endpoints, completer, search)

	history := []domain.ChatMessage{{Role: domain.ChatRoleSystem, Content: "system only"}}
	_, err := svc.Chat(context.Background(), history, nil)
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
	svc := NewChatService(endpoints, completer, search)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	result, err := svc.Chat(context.Background(), history, nil)
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
	svc := NewChatService(endpoints, completer, search)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	result, err := svc.Chat(context.Background(), history, nil)
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
	svc := NewChatService(endpoints, completer, search)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	ragOn := true
	result, err := svc.Chat(context.Background(), history, &ragOn)
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
	svc := NewChatService(endpoints, completer, search)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	ragOff := false
	result, err := svc.Chat(context.Background(), history, &ragOff)
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

func TestChatService_CompleterError(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	completer := &fakeChatCompleter{err: errors.New("upstream 500")}
	svc := NewChatService(endpoints, completer, &fakeSearchService{})

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	_, err := svc.Chat(context.Background(), history, nil)
	if err == nil {
		t.Fatal("expected error from completer to propagate")
	}
}
