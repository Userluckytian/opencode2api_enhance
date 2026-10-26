#!/usr/bin/env bash
# 构建 OpenCode 免费层 Bun sidecar（Linux/macOS）。
# 产物：bin/free-sidecar（约 100MB，不入库）。
#
# 用法：
#   ./scripts/build-free-sidecar.sh
#   BUN=/path/to/bun ./scripts/build-free-sidecar.sh
#   BUN_VERSION=1.4.4 ./scripts/build-free-sidecar.sh
#
# 与 PowerShell 脚本保持一致：未安装 Bun 时固定下载 Bun 1.4.4，
# 避免 CI 每次拉最新版导致行为漂移 / bun.sh 安装脚本网络失败。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

BUN_VERSION="${BUN_VERSION:-1.4.4}"
BUN="${BUN:-}"

if [ -z "$BUN" ]; then
  if command -v bun >/dev/null 2>&1; then
    BUN="$(command -v bun)"
  else
    os="$(uname -s)"
    arch="$(uname -m)"
    case "$os-$arch" in
      Linux-x86_64) asset="bun-linux-x64.zip" ;;
      Linux-aarch64 | Linux-arm64) asset="bun-linux-aarch64.zip" ;;
      Darwin-arm64) asset="bun-darwin-aarch64.zip" ;;
      Darwin-x86_64) asset="bun-darwin-x64.zip" ;;
      *)
        echo "不支持的平台: $os-$arch（请设置 BUN=/path/to/bun）" >&2
        exit 1
        ;;
    esac
    cache="$ROOT/.scratch/bun/bun-$os-$arch"
    BUN="$cache/bun"
    if [ ! -x "$BUN" ]; then
      mkdir -p "$cache"
      tmp="$(mktemp -d)"
      url="https://github.com/oven-sh/bun/releases/download/bun-v${BUN_VERSION}/${asset}"
      ok=0
      for base in "" "https://gh-proxy.com/" "https://ghfast.top/"; do
        echo "== downloading Bun ${BUN_VERSION}: ${base}${url}"
        if curl -fsSL --retry 3 --retry-all-errors --retry-delay 2 -o "$tmp/bun.zip" "${base}${url}"; then
          ok=1
          break
        fi
      done
      if [ "$ok" -ne 1 ]; then
        echo "下载 Bun ${BUN_VERSION} 失败: $url" >&2
        rm -rf "$tmp"
        exit 1
      fi
      unzip -q -o "$tmp/bun.zip" -d "$tmp"
      find "$tmp" -type f -name bun -exec cp {} "$BUN" \;
      chmod +x "$BUN"
      rm -rf "$tmp"
    fi
  fi
fi

echo "using bun: $BUN ($("$BUN" --version 2>/dev/null || echo unknown))"
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
