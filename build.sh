#!/bin/sh
# Build the forge binary from forge/ into the entry root.
# The committed binary is what the grader runs; this script is only for
# rebuilding it during development.
set -e
cd "$(dirname "$0")"
export CGO_ENABLED=0
if [ -x "$HOME/go-toolchain/go/bin/go" ]; then
  GO="$HOME/go-toolchain/go/bin/go"
else
  GO=go
fi
cd forge-src
"$GO" build -ldflags="-s -w" -trimpath -o ../forge .
cd ..
ls -l forge
