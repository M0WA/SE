package application

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// ChatService orchestrates a chat turn: load the single admin-configured
// domain.ChatEndpoint, connect to any admin-configured MCP servers whose
// gating the turn's effective web-search toggle satisfies, offer their
// discovered tools to the model as native tool-calling functions, and run
// whichever ones the model actually invokes. Kept separate from
// hybridSearchService so chat's single-endpoint Get/Set config
// (ports.ChatEndpointStore) never gets confused with the multi-endpoint
// blended CRUD ports.EmbeddingEndpointStore uses.
type ChatService struct {
	endpoints ports.ChatEndpointStore
	completer ports.ChatCompleter
	// mcpServers, mcpTools, agents, and userMCPServers are all nil-safe (see
	// Chat and resolveAgent): a deployment that hasn't wired MCP servers/
	// agents/per-user servers yet simply gets an empty
	// ChatResult.ToolResults/no agent specialization/no personal servers
	// every turn.
	mcpServers ports.MCPServerStore
	mcpTools   ports.MCPToolProvider
	agents     ports.AgentStore
	// userMCPServers backs each caller's own self-service MCP servers (see
	// ChatOptions.UserID) -- kept as a separate store/field from mcpServers
	// rather than folding into it, since these rows are never subject to an
	// Agent's own MCPServerIDs scope (see domain.Agent.AllowsServer's doc
	// comment) and are merged in unconditionally for their owner.
	userMCPServers ports.UserMCPServerStore
}

// NewChatService wires a ChatService from its six collaborators: the
// endpoint config store, the client that actually talks to the configured
// OpenAI-compatible endpoint, the store/provider pair behind
// admin-configured MCP servers (see mcp_tools.go), the store behind
// admin-defined agents (see resolveAgent), and the store behind each
// caller's own self-service MCP servers.
func NewChatService(endpoints ports.ChatEndpointStore, completer ports.ChatCompleter, mcpServers ports.MCPServerStore, mcpTools ports.MCPToolProvider, agents ports.AgentStore, userMCPServers ports.UserMCPServerStore) *ChatService {
	return &ChatService{endpoints: endpoints, completer: completer, mcpServers: mcpServers, mcpTools: mcpTools, agents: agents, userMCPServers: userMCPServers}
}

// ChatOptions carries this turn's per-question overrides for
// ChatService.Chat -- a nil field falls back to the admin-configured
// endpoint default (domain.ChatEndpoint.WebSearchEnabled), a non-nil one
// decides for this question only, letting the chat UI's per-question
// toggle override a fixed global setting without changing it. See
// WebSearch's own doc comment for what the toggle actually does.
type ChatOptions struct {
	// WebSearch, when non-nil, decides for this question only whether every
	// domain.MCPServer with GatedByWebSearch=true is active -- it does not
	// itself perform a search or fetch anything; it only decides which
	// servers' tools the model is offered, leaving the model to invoke them
	// (e.g. a "web_search" or "web_fetch" tool) if it chooses to.
	WebSearch *bool
	// UserCustomPrompt, when non-empty, is injected as its own leading
	// system message for this turn -- empty means no per-user prompt is
	// injected. Set by the HTTP handler layer (restapi.handleChat) from the
	// current session's associated domain.User.CustomPrompt, only when the
	// session is role=user; a role=admin session has no associated
	// domain.User row to draw this from, so it's always empty for one.
	UserCustomPrompt string
	// UserAgent, when non-empty, is passed as the WEB_FETCH_USER_AGENT
	// environment variable to every active "stdio"-transport MCPServer
	// process (same delivery mechanism as WEB_SEARCH_BASE_URL/
	// WEB_SEARCH_RESULT_COUNT below), so the first-party mcp-web server's
	// "web_fetch" tool sends the admin-configured
	// domain.OperationalSettingsValues.UserAgent instead of its own
	// hardcoded default -- set by the HTTP handler layer (restapi.
	// handleChat) from the live-synced *domain.OperationalSettings it
	// already holds, the same source crawls use. Empty means mcp-web keeps
	// its own built-in default.
	UserAgent string
	// AgentID, when non-empty, decides for this question only which Agent
	// (see domain.Agent) is active, overriding
	// domain.ChatEndpoint.DefaultAgentID -- same "empty means use the
	// admin-configured default" convention as UserCustomPrompt above,
	// rather than WebSearch's nil-pointer one, since there's no meaningful
	// difference here between "not specified" and "specified as empty."
	AgentID string
	// UserID, when non-empty, is whose own self-service MCP servers (see
	// ports.UserMCPServerStore) get merged into this turn's active server
	// list, unconditionally (never narrowed by an active Agent's own
	// MCPServerIDs scope -- see domain.Agent.AllowsServer's doc comment).
	// Empty means no personal servers are added at all. Set by the HTTP
	// handler layer (restapi.handleChat) from the current session's own
	// userID, only when the session is role=user -- same source/condition
	// as UserCustomPrompt above.
	UserID string
	// FileAccessToken, when non-empty, is passed as the SE_FILES_API_TOKEN
	// environment variable to every active "stdio"-transport MCPServer
	// process (same delivery mechanism as WEB_SEARCH_BASE_URL/UserAgent
	// above), letting the first-party mcp-files server's list_files/
	// read_file/write_file tools call back into /account/api/files as this
	// turn's own signed-in user -- see restapi.fileTokenStore's doc
	// comment for why a short-lived bearer token, minted per turn, rather
	// than a real session cookie or DB credentials. Set by the HTTP
	// handler layer (restapi.handleChat), same condition as UserID (empty
	// whenever UserID is).
	FileAccessToken string
}

// ChatResult is one completed chat turn's answer.
type ChatResult struct {
	Answer string
	// ContextTrimmed reports whether trimToBudget actually dropped one or
	// more older messages to fit endpoint.MaxContextTokens for this turn --
	// the client sends its full running history on every call (the backend
	// keeps no session state), so without this flag a user has no way to
	// know the model answered without seeing the whole conversation.
	ContextTrimmed bool
	// ToolResults is one entry per tool call the model made this turn (see
	// mcp_tools.go's runToolCalls), in call order -- empty whenever
	// s.mcpTools is nil or the model never invoked a tool.
	ToolResults []domain.ToolCallResult
	// TokenUsage breaks down the estimated size of what was actually sent to
	// the model for this turn's first completion call -- lets the chat UI
	// show where a turn's context budget went (global prompt vs. active
	// servers' own prompts vs. search context vs. conversation history)
	// instead of just a single opaque total.
	TokenUsage TokenUsage
}

// TokenUsage is one turn's leading-context token estimate, broken down by
// where each piece came from -- see estimateTokens for the (deliberately
// approximate, character-count-based) estimation method.
// GlobalPromptTokens/UserPromptTokens/ToolPromptTokens are computed
// directly from the same pieces ChatService.Chat assembles into `leading`,
// so they're exact for what was actually sent (not re-derived from the
// final message list). HistoryTokens is measured after trimToBudget, so it
// reflects what actually made it into the request, not the client's full
// untrimmed history. MaxContextTokens echoes endpoint.MaxContextTokens (0
// means unbounded) so the UI can render usage against the configured
// budget, not just relative proportions.
type TokenUsage struct {
	GlobalPromptTokens int
	// UserPromptTokens is this turn's ChatOptions.UserCustomPrompt
	// contribution -- zero whenever UserCustomPrompt is empty (a role=admin
	// session, or a role=user session with no custom prompt set).
	UserPromptTokens int
	// AgentPromptTokens is the active Agent's own SystemPrompt contribution
	// (see resolveAgent) -- zero whenever no agent is active for this turn.
	AgentPromptTokens int
	ToolPromptTokens  int
	HistoryTokens     int
	MaxContextTokens  int
}

// Chat answers the conversation in history using the admin-configured chat
// endpoint. opts.WebSearch, when non-nil, decides for this question only
// whether GatedByWebSearch MCP servers are active, falling back to
// endpoint.WebSearchEnabled otherwise -- so an admin's default can still be
// overridden per question without changing it globally. This layer never
// performs a web search or fetch itself: it only decides which servers the
// model is offered tools from (each discovered tool's Name/Description/
// InputSchema sent in the request's tools list, each active server's own
// Prompt injected) and leaves the model to invoke them.
func (s *ChatService) Chat(ctx context.Context, history []domain.ChatMessage, opts ChatOptions) (ChatResult, error) {
	if len(history) == 0 {
		return ChatResult{}, errors.New("chat: message history must not be empty")
	}

	endpoint, err := s.endpoints.GetChatEndpoint(ctx)
	if err != nil {
		return ChatResult{}, err
	}
	if !endpoint.Enabled {
		return ChatResult{}, ports.ErrChatEndpointNotConfigured
	}

	useWebSearch := endpoint.WebSearchEnabled
	if opts.WebSearch != nil {
		useWebSearch = *opts.WebSearch
	}

	// resolveAgent is best-effort, same tolerance as the ListMCPServers
	// call below: a lookup error or an unknown/disabled id just means no
	// agent is active this turn, never a failed turn.
	agentID := endpoint.DefaultAgentID
	if opts.AgentID != "" {
		agentID = opts.AgentID
	}
	agent := s.resolveAgent(ctx, agentID)
	// agentActive distinguishes "no agent selected at all" (the zero
	// domain.Agent{} resolveAgent returns, ID == "") from "an agent IS
	// selected, and its own MCPServerIDs happens to be empty" -- the latter
	// now means that agent gets NO global tools (see MCPServerIDs' own doc
	// comment), which would be wrong to apply when there's no agent in the
	// picture at all. Only when an agent is genuinely active does its scope
	// (agent.AllowsServer) get consulted below; with no agent active, every
	// enabled/gated-appropriate global server stays available, same as
	// always.
	agentActive := agent.ID != ""

	// List ALL servers early, before building the messages sent to the
	// first completion call -- a server's discovered tools and its own
	// Prompt (see below) both need to reach the model before it can decide
	// to invoke one of its tools at all, so this can't wait until after an
	// answer comes back. A ListMCPServers error is best-effort: it just
	// leaves activeServers empty rather than failing the turn.
	var activeServers []domain.MCPServer
	if s.mcpServers != nil {
		if all, err := s.mcpServers.ListMCPServers(ctx); err == nil {
			for _, srv := range all {
				if srv.Enabled && (!srv.GatedByWebSearch || useWebSearch) && (!agentActive || agent.AllowsServer(srv.ID)) {
					activeServers = append(activeServers, srv)
				}
			}
		}
	}

	// The caller's own self-service MCP servers (ChatOptions.UserID) are
	// appended AFTER the global catalog, unconditionally -- never filtered
	// by agent.AllowsServer, since a personal server is never subject to an
	// Agent's own scope (see domain.Agent.AllowsServer's doc comment). Global
	// servers listed first means a tool-name collision (see
	// ports.MCPToolProvider's own doc comment on how Open resolves one)
	// favors the admin-configured server over a same-named personal one, the
	// safer default. Transport is force-checked here, not just trusted from
	// storage/restapi validation, as a last line of defense: a "stdio"
	// server grants real local command execution on the server host, a
	// trust tier that must never reach a regular (non-admin) user's own
	// configuration, however it ended up in this store's rows. srv.SelfService
	// = true tags each row as it's appended -- a SECOND, independent layer
	// of the same defense, enforced by mcpclient itself rather than trusted
	// solely on this filter never regressing (see domain.MCPServer.
	// SelfService and mcpclient's own doc comment). Best-effort, same
	// tolerance as the global ListMCPServers call above.
	if opts.UserID != "" && s.userMCPServers != nil {
		if own, err := s.userMCPServers.ListUserMCPServers(ctx, opts.UserID); err == nil {
			for _, srv := range own {
				if srv.Enabled && srv.Transport == "http" && (!srv.GatedByWebSearch || useWebSearch) {
					srv.SelfService = true
					activeServers = append(activeServers, srv)
				}
			}
		}
	}

	// Open one MCP session spanning the whole turn -- discovery through
	// every follow-up round's tool calls below -- rather than reconnecting
	// per call, matching MCP's own intended session-oriented usage. Nil-safe:
	// a deployment with no mcpTools wired gets a nil session and no
	// discoveredTools, and every use of session below is guarded.
	var session ports.MCPSession
	var discoveredTools []domain.MCPTool
	if s.mcpTools != nil && len(activeServers) > 0 {
		env := map[string]string{"WEB_SEARCH_BASE_URL": endpoint.WebSearchBaseURL}
		if endpoint.WebSearchResultCount > 0 {
			env["WEB_SEARCH_RESULT_COUNT"] = strconv.Itoa(endpoint.WebSearchResultCount)
		}
		if opts.UserAgent != "" {
			env["WEB_FETCH_USER_AGENT"] = opts.UserAgent
		}
		if opts.FileAccessToken != "" {
			env["SE_FILES_API_TOKEN"] = opts.FileAccessToken
		}
		session, discoveredTools = s.mcpTools.Open(ctx, activeServers, env)
		defer session.Close()
	}

	messages := history

	// Leading system messages, in order: (1) the persistent per-endpoint
	// system prompt, unconditional, when set; (2) the calling user's own
	// personal custom prompt (opts.UserCustomPrompt), when set -- see
	// ChatOptions.UserCustomPrompt's doc comment for who sets this and why;
	// (3) the active agent's own SystemPrompt (see resolveAgent), when one
	// is active and non-empty -- this is the agent's actual specialization,
	// as opposed to its Description, which is never sent to the model at
	// all; (4) each activeServers entry's own non-empty Prompt, in list
	// order, each its OWN separate system message (not concatenated into
	// one blob) -- so a server's invocation guidance reaches the model
	// before the first completion call, letting it decide whether to
	// invoke one of that server's tools at all. Building this as one
	// ordered slice (rather than prepending piecemeal) keeps that order
	// obvious and gives trimToBudget a single well-defined run of leading
	// system-role messages to keep intact.
	tokenUsage := TokenUsage{MaxContextTokens: endpoint.MaxContextTokens}
	var leading []domain.ChatMessage
	if endpoint.SystemPrompt != "" {
		msg := domain.ChatMessage{Role: domain.ChatRoleSystem, Content: endpoint.SystemPrompt}
		leading = append(leading, msg)
		tokenUsage.GlobalPromptTokens = estimateTokens([]domain.ChatMessage{msg})
	}
	if opts.UserCustomPrompt != "" {
		msg := domain.ChatMessage{Role: domain.ChatRoleSystem, Content: opts.UserCustomPrompt}
		leading = append(leading, msg)
		tokenUsage.UserPromptTokens = estimateTokens([]domain.ChatMessage{msg})
	}
	if agent.SystemPrompt != "" {
		msg := domain.ChatMessage{Role: domain.ChatRoleSystem, Content: agent.SystemPrompt}
		leading = append(leading, msg)
		tokenUsage.AgentPromptTokens = estimateTokens([]domain.ChatMessage{msg})
	}
	for _, srv := range activeServers {
		if srv.Prompt != "" {
			msg := domain.ChatMessage{Role: domain.ChatRoleSystem, Content: srv.Prompt}
			leading = append(leading, msg)
			tokenUsage.ToolPromptTokens += estimateTokens([]domain.ChatMessage{msg})
		}
	}
	if len(leading) > 0 {
		withLeading := make([]domain.ChatMessage, 0, len(leading)+len(messages))
		withLeading = append(withLeading, leading...)
		withLeading = append(withLeading, messages...)
		messages = withLeading
	}

	contextTrimmed := false
	if endpoint.MaxContextTokens > 0 {
		before := len(messages)
		messages = trimToBudget(messages, endpoint.MaxContextTokens)
		contextTrimmed = len(messages) < before
	}
	// messages[len(leading):] is the actual conversation history sent to the
	// model this turn -- post-trim, since trimToBudget only ever drops
	// history messages, never the leading system messages just measured
	// above (see trimToBudget's own doc comment).
	tokenUsage.HistoryTokens = estimateTokens(messages[len(leading):])

	tools := toolDefsFrom(discoveredTools)
	assistantMsg, err := s.completer.Complete(ctx, endpoint, messages, tools)
	if err != nil {
		return ChatResult{}, fmt.Errorf("chat: %w", err)
	}

	// Whenever the model chooses to invoke one or more tools instead of
	// answering directly (assistantMsg.ToolCalls non-empty), run them and
	// feed the results back as domain.ChatRoleTool messages correlated by
	// ToolCallID, then ask again -- native tool-calling's own standard
	// multi-turn shape. This repeats up to maxHookFollowUpRounds times --
	// not just once -- because a model that reasonably decides to retry
	// (e.g. a fetch came back with an empty/blocked page, so it tries
	// another URL or another search) makes ANOTHER tool call in that
	// follow-up answer; capping this at exactly one round would leave that
	// second tool call completely unprocessed. Every round's toolResults are
	// accumulated into the final ChatResult, so the UI's folded transparency
	// panel shows every attempt, not just the last. Reuses the SAME session
	// opened above every round -- Open is never called again mid-turn.
	var toolResults []domain.ToolCallResult
	currentMessages := messages
	for round := 0; round < maxHookFollowUpRounds && len(assistantMsg.ToolCalls) > 0; round++ {
		roundResults := runToolCalls(ctx, session, assistantMsg.ToolCalls)
		toolResults = append(toolResults, roundResults...)

		followUp := make([]domain.ChatMessage, 0, len(currentMessages)+1+len(roundResults))
		followUp = append(followUp, currentMessages...)
		followUp = append(followUp, assistantMsg)
		followUp = append(followUp, toolResultMessages(roundResults)...)

		nextMsg, err := s.completer.Complete(ctx, endpoint, followUp, tools)
		if err != nil {
			// Best-effort, same convention as every other augmentation
			// source in this method: keep the current assistantMsg (which
			// may still carry unresolved tool calls) rather than failing
			// the turn.
			break
		}
		assistantMsg = nextMsg
		currentMessages = followUp
	}

	answer := assistantMsg.Content

	// If the loop above ran out of rounds while the model was STILL trying
	// to invoke one more tool (assistantMsg.ToolCalls still non-empty --
	// per the wire convention, Content is typically empty on such a
	// message), or the model returned a genuinely empty answer despite
	// tools being available, answer is empty here. Left as-is, the user
	// would see a blank response with no explanation, even though every
	// result gathered so far (in currentMessages/toolResults) is still
	// right there. Force one last completion call with NO tools offered
	// (so the model can't request yet another one) instead of returning
	// nothing: the model already has everything it found, it just needs
	// telling plainly that no more tool calls are available and to answer
	// with what it has now. Best-effort like every other augmentation here
	// -- a failure just leaves answer empty, no worse than doing nothing.
	if answer == "" && len(tools) > 0 {
		forceFinal := make([]domain.ChatMessage, 0, len(currentMessages)+1)
		forceFinal = append(forceFinal, currentMessages...)
		forceFinal = append(forceFinal, domain.ChatMessage{
			Role:    domain.ChatRoleSystem,
			Content: "No more tool calls are available for this turn. Answer the user's question directly now, using only the information already gathered above.",
		})
		if finalMsg, err := s.completer.Complete(ctx, endpoint, forceFinal, nil); err == nil {
			answer = finalMsg.Content
		}
	}

	return ChatResult{Answer: answer, ContextTrimmed: contextTrimmed, ToolResults: toolResults, TokenUsage: tokenUsage}, nil
}

// resolveAgent looks up id (opts.AgentID or endpoint.DefaultAgentID, see
// Chat) among every configured agent, returning it only when found AND
// Enabled -- a blank id, a nil s.agents, a lookup error, an unknown id, or
// a disabled one all resolve to the zero domain.Agent (ID == ""), which
// Chat's own agentActive check treats as no agent being active at all
// (empty SystemPrompt injected, no MCPServerIDs-based narrowing applied).
// ports.AgentStore has no single-row get (like MCPServerStore), so this
// scans ListAgents, same tolerance as MCP server lookups elsewhere in this
// file.
func (s *ChatService) resolveAgent(ctx context.Context, id string) domain.Agent {
	if id == "" || s.agents == nil {
		return domain.Agent{}
	}
	agents, err := s.agents.ListAgents(ctx)
	if err != nil {
		return domain.Agent{}
	}
	for _, a := range agents {
		if a.ID == id && a.Enabled {
			return a
		}
	}
	return domain.Agent{}
}

// maxHookFollowUpRounds bounds how many times ChatService.Chat will run a
// round of tool calls and feed the results back to the model for another
// completion -- each round costs one more completion call and one more
// batch of MCP tool calls, so this is a real cost bound, not just a
// correctness one. 4 covers web_search's own suggested Prompt text
// (see domain.MCPServer's doc comment): one search, then fetching the 3
// most relevant results to cross-verify -- 4 tool calls total, executed by
// processing the search (round 0), fetch 1 (round 1), fetch 2 (round 2),
// and fetch 3 (round 3). Keep this in sync with that prompt text if either
// changes: a smaller value here than what the prompt asks for silently
// drops the model's last permitted call (see the post-loop
// force-final-answer fallback below, which exists specifically to catch a
// model that still tries ONE more call than this allows, whatever the
// reason).
const maxHookFollowUpRounds = 4

// maxHookOutputCharsForModel bounds how much of each tool result's own
// Output toolResultMessages feeds back into the follow-up completion call --
// a single misbehaving MCP tool returning an unbounded amount of text could
// otherwise blow up the follow-up prompt; this is a tight cap specifically
// on what actually reaches the model.
const maxHookOutputCharsForModel = 8000

// estimateTokens sums messages' character-count-based token estimate (see
// domain.ApproxCharsPerToken) -- exact tokenization isn't worth the
// complexity here, since trimToBudget drops a whole message at a time,
// which already has slack an exact tokenizer's extra precision wouldn't
// meaningfully improve.
func estimateTokens(messages []domain.ChatMessage) int {
	chars := 0
	for _, m := range messages {
		chars += len(m.Content)
	}
	return chars / domain.ApproxCharsPerToken
}

// trimToBudget drops the oldest messages in messages -- keeping every
// leading system-role message intact (there can now be several: the
// persistent per-endpoint SystemPrompt, then one per active MCP server's own
// Prompt, then the search-context message, see ChatService.Chat), and
// always keeping at least the single most recent message even if it
// alone exceeds budget, since trimming it away would leave nothing left to
// answer -- until the estimated token count fits within maxTokens.
func trimToBudget(messages []domain.ChatMessage, maxTokens int) []domain.ChatMessage {
	if estimateTokens(messages) <= maxTokens {
		return messages
	}

	leadingSystem := 0
	for leadingSystem < len(messages) && messages[leadingSystem].Role == domain.ChatRoleSystem {
		leadingSystem++
	}
	system, rest := messages[:leadingSystem], messages[leadingSystem:]
	budget := maxTokens - estimateTokens(system)

	// Walk backward from the newest message, keeping as many as fit --
	// the newest one is always kept regardless of budget (the `i != last`
	// guard skips its own size check).
	start := len(rest)
	used := 0
	for i := len(rest) - 1; i >= 0; i-- {
		t := estimateTokens(rest[i : i+1])
		if i != len(rest)-1 && used+t > budget {
			break
		}
		used += t
		start = i
	}

	kept := make([]domain.ChatMessage, 0, len(system)+len(rest)-start)
	kept = append(kept, system...)
	kept = append(kept, rest[start:]...)
	return kept
}
