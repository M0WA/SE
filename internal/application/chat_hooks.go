package application

import (
	"context"
	"log"
	"regexp"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// maxHookMatchesPerTurn bounds how many ChatHookResult a single call to
// runChatHooks can produce, across all hooks and matches combined -- a
// defensive cap against an adversarial answer engineered (via indirect
// prompt injection, see the security note below) to contain many matches
// and force many script executions in one turn.
const maxHookMatchesPerTurn = 5

// runChatHooks scans answer for every enabled hook's regex, and for each
// match runs that hook's script with the match's capture group as an
// argument, returning one ChatHookResult per match, capped at
// maxHookMatchesPerTurn total. Best-effort: a script error/timeout produces
// a ChatHookResult with Err set, never fails the whole chat turn -- the
// model's own answer always reaches the user regardless.
//
// SECURITY: capture groups come from the model's own OUTPUT (answer), which
// can itself be influenced by untrusted web content when RAG/web-search
// context is enabled (indirect prompt injection). The ONLY thing a matched
// capture group can ever influence is an argv VALUE passed to a script
// whose IDENTITY was fixed by the admin ahead of time (hook.Script, never
// derived from the match itself) -- this function and every
// ports.HookScriptRunner implementation must NEVER build a shell command
// line by string concatenation/interpolation; capture groups are always
// passed as a real argv slice (e.g. exec.CommandContext(ctx, path,
// args...), args being the capture groups verbatim), never through `sh -c`
// or similar. A capture group is passed as an opaque argument; it is never
// evaluated, sourced, or interpreted as a path/URL/command by Go code.
//
// A hook whose Pattern fails to compile, or whose capture group count is
// not exactly 1, is skipped (logged, not fatal) -- validation at
// Create/UpdateChatHook time (admin handler layer) is expected to keep this
// from happening in practice, but existing rows could pre-date stricter
// validation, so this defends against that defensively.
func runChatHooks(ctx context.Context, hooks []domain.ChatHook, runner ports.HookScriptRunner, answer string) []domain.ChatHookResult {
	var results []domain.ChatHookResult
	for _, h := range hooks {
		if len(results) >= maxHookMatchesPerTurn {
			break
		}
		if !h.Enabled {
			continue
		}
		re, err := regexp.Compile(h.Pattern)
		if err != nil {
			log.Printf("chat hook %q: invalid regex %q: %v", h.Name, h.Pattern, err)
			continue
		}
		if re.NumSubexp() != 1 {
			log.Printf("chat hook %q: pattern %q must have exactly 1 capture group, has %d", h.Name, h.Pattern, re.NumSubexp())
			continue
		}

		matches := re.FindAllStringSubmatch(answer, -1)
		for _, m := range matches {
			if len(results) >= maxHookMatchesPerTurn {
				break
			}
			arg := m[1]
			output, err := runner.RunHookScript(ctx, h.Script, []string{arg})
			result := domain.ChatHookResult{HookName: h.Name}
			if err != nil {
				result.Err = err.Error()
			} else {
				result.Output = output
			}
			results = append(results, result)
		}
	}
	return results
}
