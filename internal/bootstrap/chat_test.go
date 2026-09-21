package bootstrap_test

import (
	"testing"

	"searchengine/internal/adapters/httpchat"
	"searchengine/internal/bootstrap"
)

// TestNewHTTPChatCompleter_BuildsHTTPChatClient mirrors
// TestNewHTTPEmbedder_BuildsHTTPEmbedder's same "factory returns the real
// adapter type" check for the chat-completer side of the same
// nil-Config-defaults-to-bootstrap pattern (see handler.go's New()).
func TestNewHTTPChatCompleter_BuildsHTTPChatClient(t *testing.T) {
	c := bootstrap.NewHTTPChatCompleter()
	if _, ok := c.(*httpchat.Client); !ok {
		t.Fatalf("expected *httpchat.Client, got %T", c)
	}
}
