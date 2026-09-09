# 阶段：opencode 厂商支持 Responses-only 模型（muse-spark contributor-free → /zen/v1/responses）

> **状态：** 计划已就绪
> **For agentic workers:** 按 Phase 顺序执行；每 Phase 测试通过后进下一 Phase，全部完成后跑 `go test -count=1 ./...`。
> **元规范:** `docs/ai-framework/phased-plan-driven.md`；问题日志 `docs/issue-log/2026-09-09.md` #1。

**Goal:** 40080 网关当前对 muse-spark-1.3-contributor-free 恒 500。根因已实测定位：opencode 上游 zen 只通过
`/zen/v1/responses` 提供该模型，`/zen/v1/chat/completions` 对它在任何出口都立即 500；本项目 `vendors/opencode`
只支持 chat/completions。本阶段给 opencode 厂商补 responses 端点支持并逐模型路由，使 muse-spark（contributor-free /
contributor）走 responses，其余模型保持 chat/completions 原路径不动。

**Architecture:** 契约层下游（chat/anthropic/responses handler + 断点续写 streamWithResume）统一以
「OpenAI chat.completion（JSON/SSE）」为上游规范。因此本次改动把转换全部收在 opencode 厂商内部：
在 `vendors/opencode/call()` 按模型选端点；responses 上游的非流式 JSON / 流式 SSE 在厂商内翻译回 OpenAI chat
形态再返回，下游与续写/竞速逻辑零改动。

**Tech Stack:** Go 1.26；纯厂商层改动 + 单测（mock/httptest 为主，网络断言仅手工自测）。

---

## 前置阅读（必须）

| 优先级 | 文件 |
|--------|------|
| P0 | `vendors/opencode/chat.go`（call/buildRequest/竞速/重试） |
| P0 | `vendors/opencode/opencode.go`、`registry.go`、`cache.go`（Vendor 结构、模型目录 Meta） |
| P1 | `docs/issue-log/2026-09-09.md` #1（本问题分析+实测证据） |
| P1 | `coding-standards.md` |

**仓库路径：** `D:\AI_Projects\opencode2api_guide\opencode2api_enhance`
**基线：** 当前 main 工作树（未提交改动多，本阶段只增 `vendors/opencode` 下文件与改动 call()）。

---

## Global Constraints（冲突时以本节为准）

1. **最小修改**：chat/completions 路径行为逐字节不变（buildRequest 不动）；只在 call() 加一个分支。
2. **responses 模型名单**：muse-spark 系列（名字前缀 `muse-spark`），照 9router `open-sse/executors/opencode.js`。
3. **加密 reasoning 不伪造**：上游 reasoning 内容为 `encrypted_content`（无明文），翻译时不计入
   `reasoning_content`，仅在 usage 透出 `completion_tokens_details.reasoning_tokens`。
4. **测试纪律**：单测不触网；`go test -count=1 ./vendors/opencode/` 与 `./...` 全绿。
5. **不做**：❌ 改 9router；❌ 改代理池/出口；❌ 改三种下游 handler；❌ 付费 zen/go 订阅行为（代码兼容即可，不验证付费）。

---

## File Structure（预期变更）

| 文件 | 变更 |
|------|------|
| `vendors/opencode/responses.go` | 新增：模型判定、端点选择、chat→responses 请求体、responses JSON/SSE→chat 翻译 |
| `vendors/opencode/chat.go` | `call()` 内按模型选 buildRequest/buildResponsesRequest，2xx 后按需翻译 |
| `vendors/opencode/responses_test.go` | 新增单测（判定/端点/请求体/JSON 翻译/SSE 翻译） |

---

## Phase 1：判定与端点选择 + 请求体构造

- `isResponsesModelID(id)`：前缀 `muse-spark`（忽略大小写/下划线）。
- `(v *Vendor) responsesEndpoint(modelID, a) (url string, ok bool)`：ok 时按 `a.useGoEndpoint`
  （authGo/auto+goOnly）选 `zen/go/v1/responses`，否则 `zen/v1/responses`。
- `buildResponsesRequest`：bodyMap（OpenAI chat）→ Responses：`input` 由 messages 提取文本
  （tool 角色/纯图/空内容消息丢弃；muse 无工具无视觉）；`max_tokens|max_completion_tokens→max_output_tokens`；
  `reasoning_effort`（∈ low/medium/high）→ `reasoning:{effort,summary:"auto"}`；透传 temperature/top_p/stream；
  丢弃 chat 专属字段（messages/thinking/stream_options/工具）。头与 chat 一致 + Accept 按流式切换。

## Phase 2：非流式 + 流式响应翻译

- `translateResponsesJSON`：`object=="response"` → chat.completion JSON（message.content 汇总 output_text、
  finish_reason 由 status/incomplete_details 推导、usage 映射 prompt/completion/total + reasoning 明细）。
- `wrapResponsesSSE`：io.Pipe + goroutine 逐事件翻译 `response.created / output_text.delta / completed`
  为 OpenAI chat chunk SSE（content 增量、finish chunk、usage chunk、`data: [DONE]`）；reasoning/ping 忽略；
  error 事件原样透传 data 行；EOF 无 completed 不伪造 DONE。Close 幂等停 goroutine。

## Phase 3：call() 接线 + 回归

- call()：预判 `responsesEndpoint`，循环内二选一 builder；2xx 流式包 wrap、非流式 translateResponsesJSON
  （chat 模型保持原 convertAnthropic 链）。竞速/重试/429/401/错误体透传逻辑不动。

## 验收标准总表

| # | 验收项 | 命令/方式 | 期望 |
|---|--------|-----------|------|
| 1 | 单测全绿 | `go test -count=1 ./vendors/opencode/` | PASS |
| 2 | 全仓编译/测试不回归 | `go test -count=1 ./...`（尽力；含网络隔离） | 无新增失败 |
| 3 | 模型判定 | 单测 | muse-spark*→true，big-pickle/deepseek→false |
| 4 | 端点选择 | 单测 | free→zen/v1/responses；goOnly paid muse→zen/go/v1/responses |
| 5 | 请求体 | 单测 | input 文本正确、max_output_tokens、无 messages/tool/thinking 残留 |
| 6 | 非流式翻译 | 单测+手工 | responses JSON→chat JSON 内容/usage/finish 正确 |
| 7 | 流式翻译 | 单测+手工 | SSE→chunk SSE 内容/finish/usage/[DONE] 正确 |
| 8 | 手工端到端（可选） | 本地临时起网关打 40080 | muse 200 出内容；big-pickle 仍 200 |

## 风险与降级

- muse 偶发返回空 content（reasoning 占满预算）：如实透传（finish=length），非网关错误。
- 上游对某字段 400：isRetryable 不含 400 → 错误体透传，客户端可见（可加日志后处理）。
- 付费 zen/go 通路本机无法验证 → 只保证判定/构建正确，验收表登记「未验证付费」。

## 给接手 AI 的完整提示词

**基线**：main（工作树含大量未提交改动与本阶段文件）。
**做**：核验收表全绿；`go test -count=1 ./vendors/opencode/` 与 `go test -count=1 ./...`；用手工 curl 对 40080
验证 muse-spark-1.3-contributor-free（chat/非流与流）200 出正文、big-pickle 回归 200。
**不做**：改下游 handler / 出口池 / 9router；提交 git（默认不提交，除非用户要求）。
**交卷**：验收表自评（附每项命令真实输出）、残留风险。
