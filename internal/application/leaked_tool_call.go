package application

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"searchengine/internal/domain"
)

const (
	leakedToolCallOpenTag  = "<tool_call>"
	leakedToolCallCloseTag = "</tool_call>"
)

// leakedToolCallNudge is sent back to the model the one time
// detectLeakedToolCall fires -- see completeDetectingLeakedToolCalls'
// own doc comment for why this asks rather than silently parses and
// executes the leaked text itself. Explicitly gives the model an out
// (ignore this if nothing was actually intended) and teaches the
// code-fence convention detectLeakedToolCall's own fence check relies on,
// so a model that adopts it needs this nudge less often going forward.
const leakedToolCallNudge = "It looks like you wrote out a tool call as literal text " +
	"(\"<tool_call>...\") instead of actually using the tool-calling interface. " +
	"If you genuinely intended to call that tool just now, make the real call " +
	"now using the tool-calling mechanism. If you did not intend to call a " +
	"tool -- for example, you were only showing what the syntax looks like -- " +
	"ignore this and continue normally; in that case, wrap any such " +
	"illustrative/example output in a Markdown code fence (```) so it's clear " +
	"it isn't a real attempted call."

// wireLeakedToolCall is the shape a leaked tool call's JSON body actually
// has -- a literal nested JSON object for arguments, not the OpenAI wire
// format's JSON-*encoded string*. Used only to detect a plausible leaked
// call, never to extract arguments for execution -- see
// detectLeakedToolCall's own doc comment for why.
type wireLeakedToolCall struct {
	Name string `json:"name"`
}

// leakedToolCallNamePattern extracts a plausible "name" field from a
// leaked tool-call block via a simple, tolerant regex, for use only when
// the block doesn't fully parse as JSON (see detectLeakedToolCall).
// Confirmed live: a model relaying code containing an already-escaped
// quote (e.g. Python's own \") sometimes over- or under-escapes it again
// for the JSON layer, producing a "name" field that's perfectly
// extractable even though the surrounding "arguments" object is
// genuinely malformed JSON as a whole
// (`{"name": "run_python", "arguments": {"code": "print(open(\\"x\\")...`
// -- the doubled backslash breaks the string boundary right after
// "arguments", but "name" itself, appearing earlier and un-embedded, is
// unaffected). Detection only needs to know the model named a real,
// offered tool -- it never parses or executes the arguments themselves
// (see completeDetectingLeakedToolCalls), so tolerating malformed
// arguments here doesn't weaken that guarantee.
var leakedToolCallNamePattern = regexp.MustCompile(`"name"\s*:\s*"([^"]*)"`)

// detectLeakedToolCall reports whether content contains at least one
// plausible leaked tool call: a literal "<tool_call>{...}</tool_call>"
// block (or an unclosed one, e.g. truncated by max_tokens) that names one
// of the tools actually offered this turn, and which doesn't fall inside
// an open Markdown code fence.
//
// This is a known, still-open vLLM bug (github.com/vllm-project/vllm/
// issues/45167): the hermes tool-call parser locates a call's end via a
// naive literal "</tool_call>" text search rather than JSON-aware
// parsing, so a large or multi-line argument (e.g. a generated
// document's own content) can break extraction, silently dropping the
// tool call and returning the raw wrapped text as content instead --
// confirmed live against this exact deployment (asking chat to write a
// multi-section CV via write_file).
//
// Deliberately detection-only -- never parses out arguments for
// execution. Executing arguments recovered from plain content would mean
// trusting text that could originate from injected, untrusted content
// (e.g. a fetched web page embedding this exact syntax) just as readily
// as from a genuine model decision; the two guards here (name must match
// an offered tool, must not be inside a code fence) only narrow this to
// plausible cases, they don't prove intent. Real intent is instead
// re-confirmed by asking the model to make the actual call for real (see
// completeDetectingLeakedToolCalls) -- if it wasn't a genuine attempt
// (e.g. injected content the model was only quoting), a model subject to
// this deployment's own prompt-injection defenses need not comply, the
// same as it wouldn't for a genuine native tool_calls attempt prompted by
// the same injected content.
func detectLeakedToolCall(content string, tools []domain.ToolDef) bool {
	if !strings.Contains(content, leakedToolCallOpenTag) {
		return false
	}
	allowed := make(map[string]bool, len(tools))
	for _, t := range tools {
		allowed[t.Name] = true
	}

	remaining := content
	fences := 0
	for {
		start := strings.Index(remaining, leakedToolCallOpenTag)
		if start == -1 {
			return false
		}
		fences += strings.Count(remaining[:start], "```")
		body := remaining[start+len(leakedToolCallOpenTag):]

		end := strings.Index(body, leakedToolCallCloseTag)
		jsonText, rest := body, ""
		if end != -1 {
			jsonText, rest = body[:end], body[end+len(leakedToolCallCloseTag):]
		}

		insideFence := fences%2 == 1
		name := leakedToolCallName(jsonText)
		if !insideFence && name != "" && allowed[name] {
			return true
		}
		if end == -1 {
			return false
		}
		remaining = rest
	}
}

// leakedToolCallName extracts a leaked block's tool name, trying strict
// JSON parsing first (after repairRawControlCharsInJSONStrings' repair)
// and falling back to leakedToolCallNamePattern's looser regex extraction
// when the block doesn't fully parse -- see that pattern's own doc
// comment for why a malformed "arguments" object shouldn't stop the name
// itself from being found. Returns "" when neither approach finds one.
func leakedToolCallName(jsonText string) string {
	var parsed wireLeakedToolCall
	repaired := repairRawControlCharsInJSONStrings(strings.TrimSpace(jsonText))
	if json.Unmarshal([]byte(repaired), &parsed) == nil && parsed.Name != "" {
		return parsed.Name
	}
	if m := leakedToolCallNamePattern.FindStringSubmatch(jsonText); m != nil {
		return m[1]
	}
	return ""
}

// repairRawControlCharsInJSONStrings escapes a literal newline/carriage-
// return/tab byte that appears INSIDE a JSON string literal, leaving
// everything already properly escaped, and everything outside a string,
// untouched. Confirmed live: the model's own leaked JSON routinely embeds
// a real multi-line document as a string value using literal newline
// bytes rather than the required "\n" escape sequence -- technically
// invalid JSON even though every other part of the structure is
// well-formed. A spec-compliant parser (Go's encoding/json included)
// correctly rejects a raw control character inside a string, so without
// this repair, detection would essentially never fire for the one case
// -- large, multi-line generated content -- this mitigation exists to
// catch in the first place.
func repairRawControlCharsInJSONStrings(s string) string {
	var out strings.Builder
	out.Grow(len(s))
	inString := false
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inString && !escaped {
			switch c {
			case '\n':
				out.WriteString(`\n`)
				continue
			case '\r':
				out.WriteString(`\r`)
				continue
			case '\t':
				out.WriteString(`\t`)
				continue
			}
		}
		out.WriteByte(c)
		switch {
		case !inString:
			if c == '"' {
				inString = true
			}
		case escaped:
			escaped = false
		case c == '\\':
			escaped = true
		case c == '"':
			inString = false
		}
	}
	return out.String()
}

// maxLeakedToolCallRetries bounds how many extra nudge-and-retry rounds
// completeDetectingLeakedToolCalls attempts before giving up and
// returning whatever the last attempt produced (even if it's still
// leaking). Confirmed live against se.mo-sys.de: a single retry (the
// original bound, matching checkToolFunctions' own one-retry precedent
// elsewhere in this codebase) still left the model re-leaking on its own
// nudged retry in 3 of 6 real attempts for this specific large-argument
// case -- a materially higher flake rate than the simpler "did it call
// any tool at all" scenario that precedent was tuned for, warranting a
// slightly larger, still-bounded budget here specifically.
const maxLeakedToolCallRetries = 2

// completeDetectingLeakedToolCalls wraps completer.Complete with a
// bounded number of retries (see maxLeakedToolCallRetries) for the
// leaked-tool-call failure mode detectLeakedToolCall describes. Returns
// the message to use going forward and the message history to continue
// from (unchanged unless at least one retry happened, in which case it
// includes each leaked-looking message and its own nudge, so subsequent
// rounds see accurate history).
//
// Deliberately asks the model to re-assert its own intent rather than
// parsing and executing the leaked text's own arguments -- see
// detectLeakedToolCall's doc comment for the full reasoning. A retry that
// itself errors is best-effort (keeps the last good message rather than
// failing the whole turn over a retry-specific error).
func (s *ChatService) completeDetectingLeakedToolCalls(ctx context.Context, endpoint domain.ChatEndpoint, messages []domain.ChatMessage, tools []domain.ToolDef) (domain.ChatMessage, []domain.ChatMessage, error) {
	msg, err := s.completer.Complete(ctx, endpoint, messages, tools)
	if err != nil {
		return domain.ChatMessage{}, messages, err
	}
	currentMessages := messages
	for i := 0; i < maxLeakedToolCallRetries; i++ {
		if len(msg.ToolCalls) > 0 || !detectLeakedToolCall(msg.Content, tools) {
			return msg, currentMessages, nil
		}

		retryMessages := make([]domain.ChatMessage, 0, len(currentMessages)+2)
		retryMessages = append(retryMessages, currentMessages...)
		retryMessages = append(retryMessages, msg, domain.ChatMessage{Role: domain.ChatRoleSystem, Content: leakedToolCallNudge})

		retryMsg, err := s.completer.Complete(ctx, endpoint, retryMessages, tools)
		if err != nil {
			return msg, currentMessages, nil
		}
		msg, currentMessages = retryMsg, retryMessages
	}
	return msg, currentMessages, nil
}
