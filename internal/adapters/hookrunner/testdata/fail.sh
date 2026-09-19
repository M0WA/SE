#!/bin/sh
# Non-zero-exit fixture: writes a recognizable marker to stderr, then exits
# 3, so a test can assert the error surfaces both the exit and the stderr
# text.
echo "boom" >&2
exit 3
