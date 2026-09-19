#!/usr/bin/env bash
# opencode2api 插件契约冒烟测试（通用版，供插件作者交付前自测）。
#
# 模拟宿主拉起插件（契约 §4.1/§10），逐项验证：
#   1. stdout 就绪行（state/port/auth 原样回显/id 与目录一致）
#   2. /v1/models 无令牌 → 401（鉴权生效）
#   3. /v1/models 带令牌 → 200=完全就绪；503=契约正确但账号/上游未就绪（警告）
#   4. /v1/chat/completions 无令牌 → 401
#   5. 面板可达性：panel="sidecar" → 动态端口 / ；panel_port=N → 127.0.0.1:N/（可选项）
#
# 用法:
#   bash plugin-smoke-test.sh <插件exe路径> <provider.json路径> [就绪等待秒数=12]
# 依赖: bash / curl / grep / sed / mktemp（Windows 下 Git Bash 自带）。
# 注意: provider.json 的私有配置应已填好账号（否则第 3 项只会得到警告级 503）。
set -u

EXE="${1:-}"
CFG="${2:-}"
WAIT="${3:-12}"

if [ -z "$EXE" ] || [ -z "$CFG" ] || [ ! -f "$EXE" ] || [ ! -f "$CFG" ]; then
  echo "用法: $0 <插件exe路径> <provider.json路径> [就绪等待秒数]"
  exit 2
fi

PASS=0; FAIL=0; WARN=0
ok()  { PASS=$((PASS+1)); echo "  ✓ $1"; }
bad() { FAIL=$((FAIL+1)); echo "  ✗ $1"; }
warn(){ WARN=$((WARN+1)); echo "  ⚠ $1"; }

# ---- 从 provider.json 提取关键字段（grep/sed 级解析，不依赖 node/python）----
jval() { # jval <键> <json文件>  →  该键的首个顶层字符串/数字值
  grep -oE "\"$1\"[[:space:]]*:[[:space:]]*\"?[^\",}]+\"?" "$2" 2>/dev/null \
    | head -1 | sed -E "s/\"$1\"[[:space:]]*:[[:space:]]*\"?([^\",}]+)\"?/\1/" | tr -d '"'
}

PID_VAL=$(jval id "$CFG")
ENTRY=$(jval entry "$CFG")
PANEL_MODE=$(jval panel "$CFG")      # "sidecar" | 空
PANEL_PORT=$(jval panel_port "$CFG") # 数字 | 空
if [ -z "$PID_VAL" ] || [ -z "$ENTRY" ]; then
  echo "✗ provider.json 缺少 id 或 entry，无法测试"; exit 2
fi
echo "== 插件契约冒烟测试: id=$PID_VAL entry=$ENTRY =="

# ---- 组装 providers/<id>/ 布局并模拟宿主拉起 ----
BASE=$(mktemp -d)
TESTDIR="$BASE/providers/$PID_VAL"
mkdir -p "$TESTDIR"
cp "$EXE" "$TESTDIR/$ENTRY"
cp "$CFG" "$TESTDIR/provider.json"
TOKEN="smoke-$(head -c 4 /dev/urandom | od -An -tx1 | tr -d ' \n')"

cd "$TESTDIR"
PLUGIN_AUTH_TOKEN="$TOKEN" \
PROVIDER_DIR="$TESTDIR" \
PROVIDER_CONFIG="$TESTDIR/provider.json" \
"./$ENTRY" --provider-serve --port 0 > ready.log 2> err.log &
PLUGIN_PID=$!

# ---- 1. 就绪行（轮询 stdout，超时按失败）----
READY=""; STATE=""; PORT=""; AUTH=""
for i in $(seq 1 "$WAIT"); do
  sleep 1
  if grep -q '"state":"ready"' ready.log 2>/dev/null; then STATE="ready"; break; fi
  if grep -q '"state":"need_config"' ready.log 2>/dev/null; then STATE="need_config"; break; fi
  if grep -q '"state":"fatal"' ready.log 2>/dev/null; then STATE="fatal"; break; fi
done
echo "-- 就绪行（stdout）--"; cat ready.log 2>/dev/null | head -5
if [ -s err.log ]; then echo "-- stderr（前 5 行）--"; head -5 err.log; fi

if [ "$STATE" = "fatal" ]; then
  bad "插件报告 fatal：$(grep -o '"error":[^}]*' ready.log | head -1)"
elif [ "$STATE" = "need_config" ]; then
  warn "插件报告 need_config（私有配置不齐）——契约项通过，但后续端点测试只能验证鉴权"
else
  PORT=$(grep -oE '"port":[0-9]+' ready.log | head -1 | grep -oE '[0-9]+')
  AUTH=$(grep -oE '"auth":"[^"]*"' ready.log | head -1 | sed 's/"auth":"//;s/"//')
  RID=$(grep -oE '"id":"[^"]*"' ready.log | head -1 | sed 's/"id":"//;s/"//')
  [ -n "$PORT" ] && ok "就绪行携带端口 $PORT" || bad "就绪行缺少 port"
  [ "$AUTH" = "$TOKEN" ] && ok "auth 一次性令牌原样回显" || bad "auth 回显不符（期望 $TOKEN）"
  [ "$RID" = "$PID_VAL" ] && ok "id 与目录名一致（$RID）" || bad "id 回显 $RID ≠ 目录名 $PID_VAL"
fi

# ---- 端点鉴权与可用性 ----
if [ -n "$PORT" ]; then
  C1=$(curl -s -o /dev/null -m 8 -w "%{http_code}" "http://127.0.0.1:$PORT/v1/models" 2>/dev/null)
  [ "$C1" = "401" ] && ok "/v1/models 无令牌 → 401（鉴权生效）" || bad "/v1/models 无令牌 → $C1（期望 401）"
  C2=$(curl -s -o /dev/null -m 20 -w "%{http_code}" -H "Authorization: Bearer $TOKEN" \
        "http://127.0.0.1:$PORT/v1/models" 2>/dev/null)
  case "$C2" in
    200) ok  "/v1/models 带令牌 → 200（目录就绪）" ;;
    503) warn "/v1/models 带令牌 → 503（契约正确；账号池/上游未就绪）" ;;
    "")  bad "/v1/models 带令牌 → 无响应" ;;
    *)   warn "/v1/models 带令牌 → $C2（检查插件语义）" ;;
  esac
  C3=$(curl -s -o /dev/null -m 8 -w "%{http_code}" -X POST -H "Content-Type: application/json" \
        -d '{"model":"m","messages":[]}' "http://127.0.0.1:$PORT/v1/chat/completions" 2>/dev/null)
  [ "$C3" = "401" ] && ok "/v1/chat/completions 无令牌 → 401" || bad "/v1/chat/completions 无令牌 → $C3（期望 401）"
else
  warn "无可用端口，跳过端点测试"
fi

# ---- 面板可达性（可选能力）----
if [ "$PANEL_MODE" = "sidecar" ] && [ -n "$PORT" ]; then
  C4=$(curl -s -o /dev/null -m 8 -w "%{http_code}" "http://127.0.0.1:$PORT/?gateway=x" 2>/dev/null)
  [ "$C4" = "200" ] && ok "面板（sidecar 动态端口 /）→ 200" || warn "面板（sidecar）→ $C4"
elif [ -n "$PANEL_PORT" ] && [ "$PANEL_PORT" != "0" ]; then
  C4=$(curl -s -o /dev/null -m 8 -w "%{http_code}" "http://127.0.0.1:$PANEL_PORT/" 2>/dev/null)
  [ "$C4" = "200" ] && ok "面板（固定端口 $PANEL_PORT）→ 200" || warn "面板（固定端口 $PANEL_PORT）→ $C4"
else
  echo "  - 未声明面板（panel/panel_port），跳过面板检查"
fi

# ---- 清理 ----
kill "$PLUGIN_PID" 2>/dev/null
sleep 1
kill -9 "$PLUGIN_PID" 2>/dev/null
cd "$BASE/.." && rm -rf "$BASE"

echo "== 结果: 通过 $PASS / 失败 $FAIL / 警告 $WARN =="
[ "$FAIL" = "0" ] && exit 0
exit 1
