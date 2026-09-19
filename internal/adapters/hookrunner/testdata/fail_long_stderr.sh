#!/bin/sh
# Non-zero-exit fixture with stderr well over maxStderrInError (200 bytes),
# so a test can assert the returned error truncates it rather than growing
# unbounded.
head -c 500 /dev/zero | tr '\0' 'e' >&2
exit 1
