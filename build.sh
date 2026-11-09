#!/bin/sh
# Cross-compile both binaries for linux/amd64 into build/ (no Docker required).
set -eu
cd "$(dirname "$0")"
GO=${GO:-go}
mkdir -p build
(cd combined_refactor && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 "$GO" build -trimpath -ldflags="-s -w" -o ../build/cfscan .)
(cd auto && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 "$GO" build -trimpath -ldflags="-s -w" -o ../build/cfscan-auto .)
cp combined_refactor/ca-certificates.crt build/ca-certificates.crt
ls -lh build/
