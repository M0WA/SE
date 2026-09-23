package application

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// ChatService runs one chat turn: loads the configured ChatEndpoint,
// connects to MCP servers passing web-search gating, offers their tools
// to the model, and runs whichever it invokes. Separate from
// hybridSearchService since chat uses a single-endpoint config store.
type ChatService struct {
	endpoints ports.ChatEndpointStore
	completer ports.ChatCompleter
	// mcpServers, mcpTools, agents, userMCPServers are all nil-safe (see
	// Chat/resolveAgent): unwired ones just mean empty ToolResults, no
	// agent, no personal servers.
	mcpServers ports.MCPServerStore
	mcpTools   ports.MCPToolProvider
	agents     ports.AgentStore
	// userMCPServers backs each caller's own self-service servers (see
	// ChatOptions.UserID) -- kept separate from mcpServers since these
	// rows are never subject to an Agent's MCPServerIDs scope (see
	// domain.Agent.AllowsServer) and are merged in unconditionally.
	userMCPServers ports.UserMCPServerStore
	// visionSettings backs cmd/mcp-vision's env-var configuration (see
	// Chat's env-building block) -- nil-safe, same as mcpServers/mcpTools:
	// unwired just means the vision tools stay unconfigured for this turn.
	visionSettings ports.ChatVisionStore
	// internalVisionAPIKey is handed to every spawned stdio server as
	// CHAT_VISION_INTERNAL_API_KEY, letting cmd/mcp-vision authenticate
	// its own call back into /search/api/vision-similarity -- see
	// restapi.Handler.internalVisionAPIKey, which must hold the identical
	// value for that call to succeed.
	internalVisionAPIKey string
}

// NewChatService wires a ChatService from its collaborators: the endpoint
// config store, the completion client, the admin MCP server store/tool
// provider (see mcp_tools.go), the agent store (see resolveAgent), and
// the per-user MCP server store.
func NewChatService(endpoints ports.ChatEndpointStore, completer ports.ChatCompleter, mcpServers ports.MCPServerStore, mcpTools ports.MCPToolProvider, agents ports.AgentStore, userMCPServers ports.UserMCPServerStore, visionSettings ports.ChatVisionStore, internalVisionAPIKey string) *ChatService {
	return &ChatService{
		endpoints: endpoints, completer: completer, mcpServers: mcpServers, mcpTools: mcpTools,
		agents: agents, userMCPServers: userMCPServers,
		visionSettings: visionSettings, internalVisionAPIKey: internalVisionAPIKey,
	}
}

// ChatOptions carries this turn's per-question overrides for Chat -- a
// nil field falls back to the endpoint's admin-configured default; a
// non-nil one applies for this question only, without changing the
// global setting. See WebSearch's doc comment for the toggle itself.
type ChatOptions struct {
	// WebSearch, when non-nil, decides for this question only whether
	// GatedByWebSearch MCP servers are active. It doesn't search or fetch
	// itself -- it only decides which servers' tools the model is offered.
	WebSearch *bool
	// UserCustomPrompt, when non-empty, is injected as its own leading
	// system message. Set by restapi.handleChat from the session's
	// domain.User.CustomPrompt for role=user sessions only -- always empty
	// for role=admin, which has no associated User row.
	UserCustomPrompt string
	// UserAgent, when non-empty, is passed as WEB_FETCH_USER_AGENT to every
	// active stdio-transport MCPServer, so mcp-web's "web_fetch" tool uses
	// the admin-configured UserAgent instead of its built-in default. Set
	// by restapi.handleChat from the same OperationalSettings crawls use.
	UserAgent string
	// AgentID, when non-empty, overrides
	// domain.ChatEndpoint.DefaultAgentID for this question only -- same
	// empty-means-default convention as UserCustomPrompt, not WebSearch's
	// nil-pointer one.
	AgentID string
	// UserID, when non-empty, is whose self-service MCP servers (see
	// ports.UserMCPServerStore) get merged into this turn's server list,
	// unconditionally -- never narrowed by an Agent's MCPServerIDs scope.
	// Set by restapi.handleChat for role=user sessions, same as
	// UserCustomPrompt.
	UserID string
	// FileAccessToken, when non-empty, is passed as SE_FILES_API_TOKEN to
	// every active stdio-transport MCPServer, letting mcp-files' tools
	// call back into /account/api/files as this turn's signed-in user via
	// a short-lived per-turn bearer token (see restapi.fileTokenStore).
	// Set alongside UserID, empty whenever it is.
	FileAccessToken string
}

// ChatResult is one completed chat turn's answer.
type ChatResult struct {
	Answer string
	// ContextTrimmed reports whether trimToBudget dropped older messages to
	// fit MaxContextTokens -- without it, the stateless client has no way
	// to know the model didn't see the whole conversation.
	ContextTrimmed bool
	// ToolResults is one entry per tool call this turn, in call order --
	// empty when s.mcpTools is nil or none were invoked.
	ToolResults []domain.ToolCallResult
	// TokenUsage breaks down the estimated size sent to the model this
	// turn, so the UI can show where the context budget went instead of
	// one opaque total.
	TokenUsage TokenUsage
}

// TokenUsage is one turn's leading-context token estimate, broken down by
// source (see estimateTokens for the approximate char-count method).
// GlobalPromptTokens/UserPromptTokens/ToolPromptTokens come directly from
// the pieces assembled into `leading`. HistoryTokens is measured after
// trimToBudget, so it reflects what was actually sent. MaxContextTokens
// echoes endpoint.MaxContextTokens (0 = unbounded).
type TokenUsage struct {
	GlobalPromptTokens int
	// UserPromptTokens is this turn's UserCustomPrompt contribution --
	// zero whenever it's empty.
	UserPromptTokens int
	// AgentPromptTokens is the active Agent's own SystemPrompt contribution
	// (see resolveAgent) -- zero whenever no agent is active for this turn.
	AgentPromptTokens int
	ToolPromptTokens  int
	HistoryTokens     int
	MaxContextTokens  int
}

// Chat answers history using the configured chat endpoint. opts.WebSearch,
// when non-nil, overrides endpoint.WebSearchEnabled for this question only.
// This layer never searches or fetches itself: it only offers the model
// tools from active servers and leaves invocation to the model.
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

	// Best-effort, like ListMCPServers below: a lookup error or unknown/
	// disabled id just means no agent this turn, never a failed one.
	agentID := endpoint.DefaultAgentID
	if opts.AgentID != "" {
		agentID = opts.AgentID
	}
	agent := s.resolveAgent(ctx, agentID)
	// agentActive distinguishes "no agent selected" (zero domain.Agent{},
	// ID == "") from "an agent IS selected with an empty MCPServerIDs" --
	// the latter means NO global tools for that agent, which would be
	// wrong to apply with no agent at all. Only when active does
	// agent.AllowsServer narrow the servers below.
	agentActive := agent.ID != ""

	// List ALL servers before the first completion call -- tools and
	// prompts must reach the model before it can invoke anything. A
	// ListMCPServers error is best-effort: leaves activeServers empty.
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

	// Self-service servers (ChatOptions.UserID) are appended AFTER the
	// global catalog, unconditionally, never filtered by agent.AllowsServer
	// (personal servers aren't subject to an Agent's scope). Global-first
	// means a name collision favors the admin-configured server. Transport
	// is force-checked to "http" here, not just trusted from storage --
	// "stdio" grants real command execution and must never reach a
	// regular user's own config. srv.SelfService = true is a second,
	// independent layer of that same defense, enforced by mcpclient
	// itself. Best-effort, same as ListMCPServers above.
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

	// One MCP session spans the whole turn (discovery through every
	// follow-up round) rather than reconnecting per call. Nil-safe: no
	// mcpTools wired means a nil session and no discoveredTools.
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
		s.addVisionEnv(ctx, env)
		session, discoveredTools = s.mcpTools.Open(ctx, activeServers, env)
		defer session.Close()
	}

	messages := history

	// Leading system messages, in order: (1) the endpoint's persistent
	// system prompt; (2) opts.UserCustomPrompt; (3) the active agent's own
	// SystemPrompt (its Description is never sent); (4) each active
	// server's own Prompt, as its own separate message, so it reaches the
	// model before the first completion call. Built as one ordered slice
	// so trimToBudget has a single well-defined leading run to keep intact.
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
	// messages[len(leading):] is the post-trim history sent this turn --
	// trimToBudget only ever drops history, never the leading system
	// messages measured above.
	tokenUsage.HistoryTokens = estimateTokens(messages[len(leading):])

	tools := toolDefsFrom(discoveredTools)
	assistantMsg, err := s.completer.Complete(ctx, endpoint, messages, tools)
	if err != nil {
		return ChatResult{}, fmt.Errorf("chat: %w", err)
	}

	// When the model invokes tools (ToolCalls non-empty), run them, feed
	// results back as ChatRoleTool messages, and ask again -- standard
	// multi-turn tool-calling. Repeats up to maxHookFollowUpRounds times,
	// since a retry (e.g. a blocked fetch) can trigger another tool call
	// in the follow-up. All rounds' toolResults accumulate into the final
	// ChatResult. Reuses the SAME session every round -- Open isn't
	// called again mid-turn.
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
			// Best-effort: keep the current assistantMsg (which may still
			// carry unresolved tool calls) rather than fail the turn.
			break
		}
		assistantMsg = nextMsg
		currentMessages = followUp
	}

	answer := assistantMsg.Content

	// answer is empty here if the loop ran out of rounds mid-tool-call or
	// the model returned nothing despite tools being available -- even
	// though currentMessages/toolResults still hold everything gathered.
	// Force one last completion with NO tools offered, telling the model
	// plainly to answer with what it has. Best-effort: failure just
	// leaves answer empty.
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

// addVisionEnv adds cmd/mcp-vision's configuration to env, in place --
// nil-safe (s.visionSettings unwired just means neither var is added, so
// mcp-vision's own tools report themselves unconfigured, same convention
// as every other optional MCP integration here). A store error is
// logged and treated the same as "nothing configured," never fatal to
// the turn -- mirrors every other best-effort lookup in this method.
func (s *ChatService) addVisionEnv(ctx context.Context, env map[string]string) {
	if s.internalVisionAPIKey != "" {
		env["CHAT_VISION_INTERNAL_API_KEY"] = s.internalVisionAPIKey
	}
	if s.visionSettings == nil {
		return
	}
	vs, err := s.visionSettings.GetChatVisionSettings(ctx)
	if err != nil {
		return
	}
	if vs.SimilarityEnabled {
		env["VISION_SIMILARITY_ENABLED"] = "true"
		env["VISION_SIMILARITY_PROVIDER_ID"] = vs.SimilarityProviderID
	}
	if vs.CaptionEnabled {
		env["VISION_CAPTION_ENABLED"] = "true"
		env["VISION_CAPTION_BASE_URL"] = vs.CaptionBaseURL
		env["VISION_CAPTION_API_KEY"] = vs.CaptionAPIKey
		env["VISION_CAPTION_MODEL"] = vs.CaptionModel
	}
}

// resolveAgent looks up id (opts.AgentID or endpoint.DefaultAgentID) among
// configured agents, returning it only when found AND Enabled -- any
// other case (blank id, nil store, lookup error, unknown/disabled id)
// resolves to the zero domain.Agent, which Chat's agentActive treats as
// no agent active. Scans ListAgents since AgentStore has no single-row get.
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

// maxHookFollowUpRounds bounds how many tool-call rounds Chat runs -- a
// real cost bound, not just correctness. 4 covers web_search's suggested
// Prompt: one search plus 3 fetches to cross-verify. Keep in sync with
// that prompt text -- a smaller value silently drops the model's last
// permitted call (caught by the post-loop force-final-answer fallback).
const maxHookFollowUpRounds = 4

// maxHookOutputCharsForModel caps how much of each tool result's Output
// toolResultMessages feeds back into the follow-up call -- guards against
// a misbehaving MCP tool blowing up the prompt with unbounded text.
const maxHookOutputCharsForModel = 8000

// estimateTokens sums messages' char-count-based estimate (see
// domain.ApproxCharsPerToken) -- exact tokenization isn't worth it since
// trimToBudget drops whole messages at a time anyway.
func estimateTokens(messages []domain.ChatMessage) int {
	chars := 0
	for _, m := range messages {
		chars += len(m.Content)
	}
	return chars / domain.ApproxCharsPerToken
}

// trimToBudget drops the oldest messages, keeping every leading
// system-role message intact (see ChatService.Chat) and always keeping
// at least the most recent message even if it alone exceeds budget --
// otherwise nothing would be left to answer.
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
	// the newest is always kept (the `i != last` guard skips its check).
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
