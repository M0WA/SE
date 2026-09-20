// Package hookrunner implements ports.HookScriptRunner by executing a
// chat hook's script directly out of a fixed directory. See the package's
// RunHookScript doc comment and application.runChatHooks's security doc
// comment for why this never builds a shell command string.
package hookrunner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"searchengine/internal/domain"
)

// defaultTimeout bounds how long a single hook script may run before it is
// killed -- a hung script must never block a chat response indefinitely.
const defaultTimeout = 10 * time.Second

// maxStdout caps how much of a hook script's stdout is kept in memory and
// returned to the caller, so a misbehaving script can't balloon memory or
// response size. Same "cap and note" convention as domain.TruncateWithNote's
// other callers, scaled up since a hook's legitimate output (e.g. web
// search results) is naturally larger than an HTTP error body.
const maxStdout = 64 * 1024

// maxStderrInError bounds how much of a failing script's stderr is folded
// into the returned error, for diagnosability without risking a huge error
// message.
const maxStderrInError = 200

// waitDelay bounds how long Cmd.Wait keeps waiting for stdout/stderr to
// reach EOF after the timed-out process has been killed. Without this, a
// killed script whose own child inherited its stdout/stderr fds (e.g. a
// shell script's "sleep" child) can leave those pipes open long after the
// direct process is dead, and Cmd.Run would otherwise block until that
// grandchild exits on its own -- exactly the indefinite-block scenario the
// timeout exists to prevent. See Cmd.WaitDelay's doc comment (Go 1.20+).
const waitDelay = 2 * time.Second

// Runner is a ports.HookScriptRunner that executes a hook's script
// directly (never through a shell) out of a fixed directory.
type Runner struct {
	// Dir is the directory every hook script must live directly inside.
	// ChatHook.Script is validated as a bare filename (no path separators,
	// no ".." or "." component) and resolved as filepath.Join(Dir,
	// scriptName) -- this is what stops an admin-controlled (but
	// defense-in-depth-worthy) Script value from ever executing something
	// outside Dir.
	Dir string

	// Timeout overrides the default 10s hard timeout applied to every
	// script execution when non-zero. Exposed mainly so tests can exercise
	// the real timeout path without waiting the real 10s default.
	Timeout time.Duration
}

// New returns a Runner whose scripts must live directly inside dir.
func New(dir string) *Runner {
	return &Runner{Dir: dir}
}

// RunHookScript validates scriptName, resolves it against r.Dir, and
// executes it directly via exec.CommandContext (never a shell) with args
// passed verbatim as argv elements -- see application.runChatHooks's
// security doc comment for why this must never build a shell command
// string, even when an arg contains characters like ; | & $ ( ) `.
//
// A hard timeout (r.Timeout, default 10s) wraps the call, automatically
// bounded further by ctx's own deadline if it already has an earlier one
// (context.WithTimeout derives from ctx, so whichever deadline is sooner
// wins). Stdout is capped at maxStdout bytes. A non-zero exit is returned
// as an error carrying stderr's first maxStderrInError bytes.
//
// env, when non-empty, is set as additional process environment variables
// for the script (cmd.Env = append(os.Environ(), "KEY=value", ...) --
// standard Go idiom, inherits the process's own environment plus these
// additions -- never through a shell, so there is no risk of a value being
// re-interpreted as shell syntax the way an interpolated command string
// would be). These carry only ADMIN-CONFIGURED settings (e.g.
// WEB_SEARCH_BASE_URL, sourced from domain.ChatEndpoint.WebSearchBaseURL by
// ChatService.Chat) -- never data derived from the model's own output or a
// hook pattern's capture group, which remain confined to args exactly as
// before. Setting configuration via the normal OS process-environment
// mechanism, rather than as an extra argv element, keeps args reserved
// solely for the regex capture group per runChatHooks's security design.
func (r *Runner) RunHookScript(ctx context.Context, scriptName string, args []string, env map[string]string) (string, error) {
	if scriptName == "" {
		return "", errors.New("hookrunner: script name must not be empty")
	}
	if scriptName == ".." || scriptName == "." ||
		strings.ContainsAny(scriptName, `/\`) ||
		filepath.Base(scriptName) != scriptName {
		return "", fmt.Errorf("hookrunner: invalid script name %q: must be a bare filename with no path separators", scriptName)
	}

	timeout := r.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resolved := filepath.Join(r.Dir, scriptName)

	cmd := exec.CommandContext(runCtx, resolved, args...)
	cmd.WaitDelay = waitDelay
	if len(env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if runCtx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("hookrunner: script %q timed out after %s", scriptName, timeout)
	}
	if err != nil {
		return "", fmt.Errorf("hookrunner: script %q failed: %v: %s", scriptName, err, domain.TruncateWithEllipsis(stderr.String(), maxStderrInError))
	}

	return truncateStdout(stdout.String()), nil
}

// truncateStdout caps s at maxStdout bytes, appending a note when it does.
func truncateStdout(s string) string {
	return domain.TruncateWithNote(s, maxStdout)
}
