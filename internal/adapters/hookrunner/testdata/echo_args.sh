#!/bin/sh
# Happy-path fixture: echoes its argv back, space-separated, so a test can
# assert args reached the script verbatim (never through a shell).
echo "$@"
