#!/bin/bash
set -e

cd "$(dirname "$0")"

# 自动检测 knowly 安装路径
KNOWLY_BIN_DIR=""
if command -v knowly &>/dev/null; then
  KNOWLY_CLI=$(which knowly)
  # 解析 knowly.js 的真实路径
  if [ -L "$KNOWLY_CLI" ]; then
    KNOWLY_JS=$(readlink "$KNOWLY_CLI")
    # 如果是相对路径，基于 CLI 所在目录解析
    if [[ "$KNOWLY_JS" != /* ]]; then
      KNOWLY_JS="$(dirname "$KNOWLY_CLI")/$KNOWLY_JS"
    fi
    # 递归解析多级 symlink
    while [ -L "$KNOWLY_JS" ]; do
      LINK=$(readlink "$KNOWLY_JS")
      if [[ "$LINK" != /* ]]; then
        LINK="$(dirname "$KNOWLY_JS")/$LINK"
      fi
      KNOWLY_JS="$LINK"
    done
    KNOWLY_BIN_DIR=$(dirname "$KNOWLY_JS")
  elif [ -f "$KNOWLY_CLI" ]; then
    # 如果不是 symlink，假设 bin 在同一目录或上级 lib/bin 目录
    KNOWLY_BIN_DIR=$(dirname "$KNOWLY_CLI")
  fi
fi

# 回退到常见路径
if [ -z "$KNOWLY_BIN_DIR" ] || [ ! -d "$KNOWLY_BIN_DIR" ]; then
  for dir in \
    "$(npm root -g)/knowly/bin" \
    "/opt/homebrew/lib/node_modules/knowly/bin" \
    "/usr/local/lib/node_modules/knowly/bin"; do
    if [ -d "$dir" ]; then
      KNOWLY_BIN_DIR="$dir"
      break
    fi
  done
fi

if [ -z "$KNOWLY_BIN_DIR" ]; then
  echo "ERROR: Cannot find knowly installation directory."
  echo "Please ensure 'knowly' CLI is installed and in PATH."
  exit 1
fi

echo "Knowly bin directory: $KNOWLY_BIN_DIR"

BINARY_NAME="knowly-darwin-arm64"
TARGET="$KNOWLY_BIN_DIR/$BINARY_NAME"
TARGET_X64="$KNOWLY_BIN_DIR/knowly-darwin"

echo "Building $BINARY_NAME..."
go build -o "$BINARY_NAME" ./cmd/knowly

echo "Stopping knowly daemon..."
# 直接发 SIGTERM 并清理 PID 文件，避免 flock 竞争
PID_FILE="$HOME/.knowly/knowly.pid"
if [ -f "$PID_FILE" ]; then
  OLD_PID=$(cat "$PID_FILE" 2>/dev/null || echo "")
  if [ -n "$OLD_PID" ]; then
    kill "$OLD_PID" 2>/dev/null || true
  fi
  rm -f "$PID_FILE"
fi
sleep 2
# 确保旧进程已退出
if [ -n "$OLD_PID" ]; then
  kill -0 "$OLD_PID" 2>/dev/null && sleep 2 || true
fi

echo "Replacing binary..."
cp "$BINARY_NAME" "$TARGET"
if [ -f "$TARGET_X64" ]; then
  cp "$BINARY_NAME" "$TARGET_X64"
fi
rm -f "$BINARY_NAME"

echo "Starting knowly daemon..."
nohup "$TARGET" --daemon > /dev/null 2>&1 &
sleep 3
# 验证服务已启动
if curl -sf http://localhost:8090/api/status > /dev/null 2>&1; then
  echo "✓ knowly daemon started (PID $(cat "$PID_FILE" 2>/dev/null || echo '?'))"
else
  echo "⚠ knowly daemon may not be ready yet, check logs: $HOME/.knowly/knowly.log"
fi

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
