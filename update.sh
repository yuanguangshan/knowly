#!/bin/bash
set -e

cd "$(dirname "$0")"

BINARY_NAME="knowly-darwin-arm64"
INSTALL_DIR="/opt/homebrew/lib/node_modules/knowly/bin"
TARGET="$INSTALL_DIR/$BINARY_NAME"

echo "Building $BINARY_NAME..."
go build -o "$BINARY_NAME" ./cmd/knowly

echo "Stopping knowly daemon..."
knowly stop 2>/dev/null || true
sleep 1

echo "Replacing binary..."
cp "$BINARY_NAME" "$TARGET"
rm -f "$BINARY_NAME"

echo "Starting knowly daemon..."
knowly start

echo "Committing and pushing..."
gdox
git add -A
if git diff --cached --quiet; then
  echo "No changes to commit."
else
  git commit -m "release"
  git push
fi

echo "Done."
