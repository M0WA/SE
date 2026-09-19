#!/bin/sh
# Timeout fixture: sleeps far longer than any test-configured Runner.Timeout,
# so RunHookScript's context deadline fires and kills it before it exits on
# its own.
sleep 5
