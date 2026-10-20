#!/bin/bash
# T1 探测执行器：6 场景 × inference/v1/responses，轮换 SOCKS 出口，原始流落 results/
DIR="$(cd "$(dirname "$0")" && pwd)"
PORTS=(42100 42101 42102 42103 42104 42105)
run() { # $1=scenario $2=model $3=portidx
  local sc="$1" model="$2" sock="127.0.0.1:${PORTS[$3]}"
  local ses; ses=$(node "$DIR/oc-gate-body.mjs" "$sc" "$model" | tail -1)
  echo "=== $sc ($model) via $sock ses=$ses"
  timeout 60 curl -sN --socks5-hostname "$sock" -m 50 \
    -H "Authorization: Bearer public" -H "Content-Type: application/json" \
    -H "Accept: text/event-stream" -H "User-Agent: opencode/prod/2.0.29/cli" \
    -H "x-opencode-client: cli" -H "x-opencode-project: global" \
    -H "x-opencode-session: $ses" -H "x-opencode-session-id: $ses" \
    -H "x-session-affinity: $ses" -H "x-session-id: $ses" \
    -w "\n__HTTP__%{http_code} __T__%{time_total}\n" \
    -d "@$DIR/results/body_$sc.json" \
    "https://opencode.ai/inference/v1/responses" > "$DIR/results/raw_$sc.txt" 2>&1
  grep -oE "__HTTP__[0-9]+" "$DIR/results/raw_$sc.txt" | tail -1
  sleep 7
}
run p4_plain      deepseek-v4.1-flash-free   0
run p1_reasoning  deepseek-v4.1-flash-free   1
run p2a_none      deepseek-v4.1-flash-free   2
run p2b_named     deepseek-v4.1-flash-free   3
run p2c_required  deepseek-v4.1-flash-free   4
run p3_image      nemotron-3.5-super-vl      5
