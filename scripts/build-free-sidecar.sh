#!/usr/bin/env bash
# 构建 OpenCode 免费层 Bun sidecar（Linux/macOS）。
# 产物：bin/free-sidecar（约 100MB，不入库）。
#
# 用法：
#   ./scripts/build-free-sidecar.sh
#   BUN=/path/to/bun ./scripts/build-free-sidecar.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

BUN="${BUN:-}"
if [ -z "$BUN" ]; then
  if command -v bun >/dev/null 2>&1; then
    BUN="$(command -v bun)"
  else
    echo "未找到 bun。请安装 Bun（https://bun.sh）或设置 BUN=/path/to/bun。" >&2
    exit 1
  fi
fi

mkdir -p bin
OUT="$ROOT/bin/free-sidecar"
echo "building $OUT"
"$BUN" build --compile "$ROOT/sidecar/free-sidecar.mjs" --outfile "$OUT"
if [ ! -x "$OUT" ]; then
  chmod +x "$OUT" 2>/dev/null || true
fi
if [ ! -f "$OUT" ]; then
  echo "sidecar 构建失败：$OUT 不存在" >&2
  exit 1
fi
ls -lh "$OUT"
