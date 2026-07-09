#!/bin/bash
set -e
cd /Users/ygs/ygs/knowly
GOCACHE=/tmp/go-cache go build -o /opt/homebrew/lib/node_modules/knowly/bin/knowly-darwin-arm64 ./cmd/knowly
echo "Build OK"
ls -lh /opt/homebrew/lib/node_modules/knowly/bin/knowly-darwin-arm64
