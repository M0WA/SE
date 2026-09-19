#!/bin/sh
# Env fixture: echoes the value of a named environment variable (empty line
# if it's unset), so a test can confirm RunHookScript's env parameter
# actually reaches the child process. $1 names the variable to echo -- an
# ordinary argv element the test controls, not data derived from a hook's
# capture group, so the eval below carries none of the risk
# application.runChatHooks's security doc comment warns against.
eval "echo \"\${$1:-}\""
