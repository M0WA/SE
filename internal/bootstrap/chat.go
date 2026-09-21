package bootstrap

import (
	"searchengine/internal/adapters/httpchat"
	"searchengine/internal/ports"
)

// NewHTTPChatCompleter constructs the shared ports.ChatCompleter used both
// for real chat completions (cmd/search) and, via its optional
// ModelMaxContextTokens capability, to probe a candidate/saved chat
// endpoint's own advertised context length from the admin API
// (restapi.Handler's chat-endpoint PATCH handler) -- unlike
// NewHTTPEmbedder, httpchat.Client's methods all take the endpoint config
// per call rather than at construction, so one shared instance works for
// every candidate config, not a throwaway built fresh each time.
func NewHTTPChatCompleter() ports.ChatCompleter {
	return httpchat.New()
}
