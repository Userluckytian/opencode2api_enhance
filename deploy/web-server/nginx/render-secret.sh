#!/usr/bin/env bash
# 渲染 nginx 反代共享密钥片段。
# 用法：TOKEN_BUFFET_PROXY_SECRET=xxxx bash render-secret.sh /etc/nginx/snippets/token-buffet-proxy-secret.conf
#
# 为什么不用 nginx 的 env 变量：nginx 的 `env` 指令只把变量继承给 worker 进程，
# 不能在配置里当 $变量 引用（实测 nginx -t 报 unknown "xxx" variable）。
# 改成部署时生成一个 0600 的 include 片段，密钥同样不落版本库。
set -euo pipefail

OUT="${1:-/etc/nginx/snippets/token-buffet-proxy-secret.conf}"
SECRET="${TOKEN_BUFFET_PROXY_SECRET:-}"

if [ -z "$SECRET" ]; then
  echo "错误：请通过环境变量 TOKEN_BUFFET_PROXY_SECRET 传入密钥" >&2
  exit 1
fi

# 密钥必须是十六进制串（web-server 的 proxy_secret 形态），拒绝引号/分号等注入字符
if ! printf '%s' "$SECRET" | grep -Eq '^[A-Za-z0-9._-]{16,128}$'; then
  echo "错误：密钥格式非法（只允许 16~128 位字母数字 . _ -）" >&2
  exit 1
fi

mkdir -p "$(dirname "$OUT")"
umask 077
cat > "$OUT" <<EOF
# 由 render-secret.sh 生成，请勿提交到版本库。生成时间：$(date -u +%Y-%m-%dT%H:%M:%SZ)
proxy_set_header X-Proxy-Secret "$SECRET";
EOF
chmod 600 "$OUT"
echo "[OK] 已写入 $OUT （权限 600）"