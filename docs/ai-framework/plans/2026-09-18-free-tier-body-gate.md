# 阶段计划：opencode 免费通道 body 级门禁适配（2026-09-18）

> 状态：已完成（2026-09-18） ｜ 档位：全能（含本地测试 + 真机验证） ｜ 子代理：未启用

## 背景（实测证据）

上游 2026-09-18 起对匿名免费通道（`Authorization: Bearer public`）在原有「身份头」门禁之外，
新增 **请求体级门禁**，curl 对照实验结论：

| 条件 | 结果 |
|------|------|
| body `stream:false`（其余全对） | 403 FreeTierError |
| body `stream:true` 但 `tools` 缺失/不足 | 403 FreeTierError |
| body `stream:true` + `tools` 含 `bash/glob/grep/read`（大小写敏感，空 schema 即可） | 200 |
| 上述 + `tool_choice:"none"` | 200（且模型不乱调工具） |
| 头门禁（UA ≥1.17.0、`ses_`+12hex+14base62） | 仍生效（旧格式 session / 无版本 UA 依旧 403） |

`tools` 名字要求经穷举确认：缺 `bash`/`glob`/`grep`/`read` 任一 → 403；缺 `edit`/`write` → 仍 200；
5 个未知名字或 5 个非核心官方名字 → 403。

## 目标

1. 免费通道（public）上游请求：body 强制 `stream:true`，`tools` 补齐 4 个门禁工具。
2. 下游非流式请求：厂商内完成 SSE → JSON 聚合（上游已无法返回非流式）。
3. 下游未提供 tools 的纯聊天：`tool_choice:"none"`，避免模型调用注入的占位工具。
4. 付费通道（zen/go，带 key）与既有行为完全不变。

## 任务与验收

| # | 任务 | 验收 |
|---|------|------|
| 1 | `gate.go`：门禁工具定义 + `ensureAgentGate`（注入 tools / 强制 stream / 纯聊天置 none） | 本地单测：注入后 body 含 4 名 + stream=true |
| 2 | `sse_aggregate.go`：chat SSE → 非流式 JSON（content / reasoning / tool_calls / usage） | 本地单测：多 chunk 聚合结果与预期一致 |
| 3 | `call()` 接线：public 通道加门禁；非流式分支按需聚合（chat SSE、responses SSE） | 本地单测 + 真机 e2e |
| 4 | responses 路径同步（muse-spark） | 本地单测（该模型本机被地区限制，真机不可验） |
| 5 | 回归：既有单测全绿 | `go build ./... && go vet ./... && go test ./...` |
| 6 | 真机端到端：非流式 + 流式各一发 | 上游 200 且正文正常 |

## 不做（本次）

- 注入工具被模型调用时的响应侧改名/过滤（先用 `tool_choice:none` + 占位描述降低概率，风险记入 OPEN.md）
- responses 端点门禁的**本机**真机验证（本机 muse-spark 受地区限制）——
  已由社区 PR decolua/9router#4132 独立探针交叉印证「两端口同门禁」，实现与单测已覆盖

## 完成记录（2026-09-18）

- 任务 1-4 全部完成，单测 + 真机 e2e 通过；详见 `docs/issue-log/2026-09-18.md`。
- 任务 5：`go build ./...` / `go vet ./...` / `go test ./...` 全绿（`core/manager` 首轮偶发失败已复跑排除）。
- 任务 6：非流式下游 200 且聚合正确；流式下游 200 SSE 正常。
