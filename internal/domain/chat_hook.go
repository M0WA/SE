package domain

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ChatHook is one admin-configured regex trigger: whenever a chat turn's
// answer matches Pattern, the matched capture group is passed as an argv
// value to Script (see application.runChatHooks) -- a lightweight,
// deliberately narrow tool-calling mechanism (e.g. a "web_search" hook that
// lets the model ask for a follow-up search by emitting a recognizable
// marker in its own answer). Multi-row, like EmbeddingHTTPEndpoint, unlike
// the single-row ChatEndpoint -- an admin can define several hooks over
// time.
type ChatHook struct {
	ID   string
	Name string // human label, e.g. "web_search"
	// Pattern is a Go regexp that must have exactly one capture group --
	// that group's matched text is the only thing ever passed to Script,
	// and only as an opaque argv value (see runChatHooks's security doc
	// comment). A Pattern that fails to compile, or that has a capture
	// group count other than one, is skipped at match time rather than
	// failing the turn -- validation at Create/Update time (admin handler
	// layer) is expected to keep this from happening in practice, but rows
	// can pre-date that validation.
	Pattern string
	// Script is a SCRIPT FILENAME ONLY, never a path -- see
	// ports.HookScriptRunner and runChatHooks's security doc comment for
	// why (it is resolved against a fixed, admin-controlled script
	// directory by the runner, never influenced by the match itself).
	Script  string
	Enabled bool
	// Prompt is this hook's own static system-prompt text -- when non-empty
	// AND this hook is "active" for a turn (see GatedByWebSearch and
	// ChatService.Chat), it is injected as its own leading system message,
	// positioned after the endpoint's persistent SystemPrompt and before
	// any RAG/web-search context message. Typically what tells the model
	// the hook's own invocation syntax exists at all (e.g. "to search the
	// web, output SEARCH[query]") -- see packaging/chat-hooks/README.md's
	// "Suggested system prompt" section, which this supersedes on a
	// per-hook basis. Empty Prompt means no hook-specific message is added,
	// even when the hook is active.
	Prompt string
	// GatedByWebSearch, when true, ties this hook's activation (and its
	// Prompt injection) to the SAME effective web-search toggle that
	// already gates the deterministic RAG-style web-search context
	// injection (endpoint.WebSearchEnabled, overridden per-question by
	// ChatOptions.WebSearch) -- the existing public chat UI's "Web"
	// checkbox, no new UI control needed. false (the default, for a
	// hypothetical future non-web hook) means this hook is active whenever
	// Enabled is true, unaffected by the Web toggle -- today's existing
	// behavior.
	GatedByWebSearch bool
}

// ChatHookResult is the outcome of running one matched ChatHook's script
// once, surfaced on ChatResult so the caller can decide how (or whether) to
// show it.
type ChatHookResult struct {
	HookName string
	Output   string // the script's raw stdout
	Err      string // non-empty if the script failed or timed out; Output is empty then
}

// ChatHookIDPattern is every valid ChatHook.ID -- same shape as
// EmbeddingEndpointIDPattern (lowercase alphanumeric/underscore, 1-20
// characters): short enough to fit the sqlrepo MySQL dialect's chat_hooks.id
// VARCHAR(20) column.
var ChatHookIDPattern = regexp.MustCompile(`^[a-z0-9_]{1,20}$`)

var chatHookSlugRE = regexp.MustCompile(`[^a-z0-9]+`)

// NewChatHookID derives an ID from a display name the same way
// NewEmbeddingEndpointID does (lowercased, non-alphanumeric runs collapsed,
// trimmed to fit ChatHookIDPattern), appending the shortest numeric suffix
// that avoids colliding with a key in existing (every other configured
// hook's ID). Falls back to a timestamp-derived ID if name has no
// alphanumeric characters.
func NewChatHookID(name string, existing map[string]bool) string {
	slug := strings.Trim(chatHookSlugRE.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "_"), "_")
	if len(slug) > 20 {
		slug = strings.Trim(slug[:20], "_")
	}
	if slug == "" {
		slug = fmt.Sprintf("hook%d", time.Now().UnixNano()%1_000_000_000)
	}
	if !existing[slug] {
		return slug
	}
	for n := 2; ; n++ {
		suffix := fmt.Sprintf("_%d", n)
		base := slug
		if len(base)+len(suffix) > 20 {
			base = base[:20-len(suffix)]
		}
		if candidate := base + suffix; !existing[candidate] {
			return candidate
		}
	}
}
