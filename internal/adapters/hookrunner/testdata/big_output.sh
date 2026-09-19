#!/bin/sh
# Stdout-truncation fixture: writes well over maxStdout (64KB) bytes of
# output, all the letter 'a', so a test can assert RunHookScript caps and
# annotates it rather than returning it whole.
head -c 70000 /dev/zero | tr '\0' 'a'
