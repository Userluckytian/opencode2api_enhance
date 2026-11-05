# 阶段 v2.0.0 增补：调用指标采集与分析（Call Metrics）

> **状态：** 计划已就绪 / 待实施
> **For agentic workers:** 按 P0→P4 顺序执行；每阶段验收表全绿再进下一阶段。
> **元规范：** `docs/ai-framework/phased-plan-driven.md`

**Goal:** 把引擎与供应商服务的每次调用落成**结构化指标行**（模型 / 通道 / 密钥 / 状态 / 耗时 / 逐跳），在此基础上提供「模型 × 通道 × 密钥」成功率与延迟矩阵、密钥健康度、耗时分布，以及**可执行的优化建议文案**——用于按真实使用情况迭代，且用户自己也能用。

**Architecture:** 新起一条独立的指标管道，不与现有 `call_log.jsonl` 混用（后者是给人看的日志，字段不足以做分析）。指标由两处写入：引擎 `stdout.log` 的 `request_routed` 事件（解析后补 key/channel/tier/legs），以及供应商服务的调用记录。按天滚动的 JSONL，查询时现算聚合。

**Tech Stack:** Go 1.22（纯标准库，不引第三方依赖）／ React 19 + Tailwind 4（扩展现有诊断页）。

**实施档位：** 全能（默认）
**子代理：** 不启用（改动集中在 3 个文件，串行更快且不易冲突）

---

## 前置阅读（必须）

| 优先级 | 文件 |
|--------|------|
| P0 | `docs/ai-framework/phased-plan-driven.md` |
| P0 | 本文件 |
| P0 | `docs/issue-log/OPEN.md` |
| P1 | `AGENTS.md`、`CODE_REVIEW.md` |
| P1 | `core/manager/calllog_engine.go`（现有引擎事件解析，复用其 `engineLogLine`） |
| P1 | `diag_api.go`（诊断页后端，新接口挂这里） |

**仓库路径：** `D:\AI_Projects\opencode2api_guide\opencode2api_enhance`
**基线分支：** `refactor/custom-only`
**目标版本：** 保持 `2.0.0`（本增补并入 v2.0.0 发布）

---

## 决策记录（用户已确认）

| # | 决策 | 依据 |
|---|------|------|
| D1 | 采集粒度 = **每次调用一行结构化记录** | 只有这个粒度能回答「哪个通道对哪个模型最优」 |
| D2 | **纯本地，不上报** | 用户数据不出本机；可导出报表 |
| D3 | **只做统计与建议，不自动改配置** | 先让数据可信，自动调优另立阶段 |
| D4 | 分析主轴 = **模型 × 通道 × 密钥** | 引擎实测：`egress`/`exit_id` **恒为空**，拿不到具体 WARP 出口；但 `key_id`（10 个）与 `channel`（key/anonymous）有值 |
| D5 | 存储 = **独立 JSONL 按天滚** | 不污染现有 `call_log.jsonl`；不引 SQLite/cgo |
| D6 | UI = **扩展现有「诊断」页** | 导航保持 6 项；诊断页本就是排障入口 |

---

## 实测数据约束（决定实现边界，勿越过）

依据 `runtime/free-tier/stdout.log` 实际内容（83 条 `request_routed`）：

| 字段 | 可用性 | 说明 |
|---|---|---|
| `model` | ✅ | |
| `channel` | ✅ | `key`(42) / `anonymous`(41) |
| `tier` | ✅ 但全是 `zen` | 保留字段，当前无区分度 |
| `key_id` | ✅ 10 个值 | `Pwrg3`/`OqCI7`/`viYdO`/`ICoux`/`XY-RG`/`uatIq`/`F0jAB`/`DNAIc`/`J_jXN`/`anonymous` |
| `legs` | ✅ | `Pwrg3:403:1129ms` 空格分隔 |
| `status` / `duration_ms` / `in` / `out` / `outcome` | ✅ | |
| **`egress` / `exit_id`** | ❌ **恒为空** | **不做「出口」维度**，UI 不得出现该列 |
| 首字延迟（TTFT） | ❌ | 只有总耗时；不做该指标 |
| 429 后轮换 | ✅ | `session_identity_rotated` 可按 `request_id` join，含 `proxy` IP 段 |
| 逐模型探测结果 | ✅ | `availability_checked`（`model`/`available`/`reason`/`channel`） |

---

## Global Constraints（冲突时以本节为准）

1. **不引入第三方 Go 依赖。** 纯标准库（`encoding/json`、`bufio`、`os`、`sort`）。
2. **密钥不进 git / 不进指标文件。** 指标只存 `key_id`（引擎给的短标识），**绝不**存完整 key。
3. **指标文件写在数据目录内**（`<dataDir>/runtime/call-metrics/`），不落仓库。目录不入 git。
4. **写入失败不得影响推理。** 指标是旁路，任何 IO 错误只记 warn 日志。
5. **不修改引擎。** 只读它的 stdout，符合 GPL 隔离约束。
6. **明确不做（本阶段）：**
   - ❌ 自动调优 / 自动改配置
   - ❌ 出口维度分析（无数据）
   - ❌ 上报 / 云端
   - ❌ 请求/响应内容留存（隐私）

---

## 实施阶段

### P0：指标写入层

**产出：** `core/manager/callmetric.go`（新建）

| # | 任务 | 验收 |
|---|------|------|
| P0-1 | `CallMetric` 结构体：`TS`/`ReqID`/`Model`/`Channel`/`Tier`/`KeyID`/`Status`/`UpstreamStatus`/`Outcome`/`DurationMS`/`PromptTokens`/`CompletionTokens`/`Legs []Leg`/`Rotated bool`/`ProxyClass string`/`Source` | `go build` 通过 |
| P0-2 | `AppendCallMetric(m CallMetric) error`：追加到 `<runtime>/call-metrics/YYYY-MM-DD.jsonl`，一行一条 | 单测：写入后能读回 |
| P0-3 | `ReadCallMetrics(from, to time.Time, limit int) ([]CallMetric, error)`：按天文件读取，损坏行跳过 | 单测：跨天文件合并、损坏行不断流 |
| P0-4 | 保留策略：`PruneCallMetrics(keepDays int)`，默认保留 30 天 | 单测：超期文件被删 |
| P0-5 | 并发安全：写用 `sync.Mutex` + O_APPEND；读不加锁 | `go test -race` 通过 |

**验收表**

| 项 | 命令 | 期望 |
|---|---|---|
| 编译 | `go build ./...` | exit 0 |
| 单测 | `go test ./core/manager/ -run CallMetric -v` | 全 PASS |
| 竞态 | `go test ./core/manager/ -race` | PASS |

---

### P1：引擎事件 → 指标接线

**产出：** 改 `core/manager/calllog_engine.go`、`main.go`

| # | 任务 | 验收 |
|---|------|------|
| P1-1 | `readEngineCallLog` 每解析出一条 `request_routed`，同时产出 `CallMetric`（补齐 `key_id`/`channel`/`tier`/`legs`/`rotated`） | 单测：给定样例行，指标字段正确 |
| P1-2 | `session_identity_rotated` 按 `request_id` join，标记 `Rotated=true` 并提取 `proxy` 的 IP 段到 `ProxyClass`（如 `warp:2a09:bac5:1ef3::/64`） | 单测：join 命中；未命中时 `Rotated=false` |
| P1-3 | 增量消费：记录已处理到的文件偏移，避免重复写入（引擎 stdout 是追加文件） | 单测：连续两次调用不产生重复记录 |
| P1-4 | 在管理进程加一个 30s ticker 调增量消费 | 手工：连续调用模型，指标文件增长 |

**验收表**

| 项 | 命令 | 期望 |
|---|---|---|
| 单测 | `go test ./core/manager/ -run EngineMetric -v` | 全 PASS |
| 端到端 | 发 3 次真实 chat 请求后查指标行数 | ≥3，且 `key_id`/`channel` 非空 |
| 无重复 | 等两个 ticker 周期后再查 | 行数不变 |

---

### P2：聚合接口

**产出：** 改 `diag_api.go`、`main.go`

| # | 任务 | 验收 |
|---|------|------|
| P2-1 | `GET /api/admin/call-metrics/summary?days=7` → 总体（请求数/成功率/P50 P95 耗时/总 token） | `curl` 返回结构完整 |
| P2-2 | `GET /api/admin/call-metrics/matrix?days=7` → `模型 × 通道 × 密钥` 矩阵：每格 `{requests, ok, fail, okRate, avgMs, p95Ms, topReason}` | 用真实数据返回 ≥1 格 |
| P2-3 | `GET /api/admin/call-metrics/keys?days=7` → 密钥健康度：每 key `{requests, okRate, avgMs, failReasons, rotatedCount, lastSeen}` | 返回 10 个 key 的统计 |
| P2-4 | `GET /api/admin/call-metrics/suggestions?days=7` → 规则引擎生成的建议文案数组 | 有数据时返回非空数组（见下方规则） |
| P2-5 | 所有接口只读，`GET` only；鉴权走 `requireAuth` | 无 auth 返回 401 |

**建议规则（P2-4 实现依据，每条输出一句人话）**

| 条件 | 建议文案 |
|---|---|
| 某 key 成功率 < 60% 且请求数 ≥ 10 | 「密钥 `X` 近 N 天成功率仅 Y%，建议降低优先级或更换」 |
| 某模型成功率 < 50% 且请求数 ≥ 10 | 「模型 `M` 近 N 天成功率 Y%，失败主因是 `reason`，建议暂时排除」 |
| 同一模型在不同 key 间成功率差 > 30 个百分点 | 「模型 `M` 在 `keyA` 上成功率显著高于 `keyB`，建议优先分配 `keyA`」 |
| `rotated` 比例 > 30% | 「N% 的请求触发了 session 轮换，说明免费额度消耗快；可考虑减少并发或增加 key」 |
| P95 耗时 > 60s | 「P95 耗时 Ns，偏慢；建议检查 WARP 出口健康度」 |
| 无数据 | 「近 N 天没有任何调用记录」 |

**验收表**

| 项 | 命令 | 期望 |
|---|---|---|
| 编译 | `go build ./...` | exit 0 |
| 接口 | `curl -s .../call-metrics/summary?days=7` | JSON 含 `requests`/`ok_rate`/`p95_ms` |
| 矩阵 | `curl -s .../call-metrics/matrix?days=7` | `cells` 长度 ≥1 |
| 建议 | `curl -s .../call-metrics/suggestions?days=7` | 数组，元素为字符串 |
| vet | `go vet ./core/... .` | exit 0 |

---

### P3：前端「调用分析」

**产出：** 改 `src/pages/DiagPage.tsx`、`src/lib/api.ts`

| # | 任务 | 验收 |
|---|------|------|
| P3-1 | `api.ts` 加 4 个类型 + 4 个方法 | `tsc --noEmit` 通过 |
| P3-2 | 诊断页新增「调用分析」区块：时间范围选择（1/7/30 天）+ 总览 4 个数字卡 | 页面渲染出数字 |
| P3-3 | 模型×通道×密钥矩阵表：可排序、成功率用颜色分级（≥90 绿 / ≥60 琥珀 / <60 红） | 截图确认 |
| P3-4 | 密钥健康度表：key / 请求数 / 成功率 / 平均耗时 / 主要失败原因 / 轮换次数 | 截图确认 |
| P3-5 | 优化建议卡片：逐条列出，每条前面带 💡 | 截图确认 |
| P3-6 | 无数据时四个区块都要显示明确空态文案，不能白屏 | 清空指标目录后刷新验证 |

**验收表**

| 项 | 命令 | 期望 |
|---|---|---|
| 类型 | `npx tsc --noEmit` | exit 0 |
| 构建 | `npm run build` | exit 0 |
| 渲染 | Playwright 断言 4 个区块可见 + 无 console error | 全部 true |
| 空态 | 临时改目录名后再加载 | 显示空态文案，不崩 |

---

### P4：收尾

| # | 任务 | 验收 |
|---|------|------|
| P4-1 | 更新 `CHANGELOG.md` v2.0.0「新增」段 | 含调用指标条目 |
| P4-2 | 更新 `docs/issue-log/2026-11-05.md` | 记录设计与实测数据 |
| P4-3 | 更新 `docs/issue-log/OPEN.md` | 新增「自动调优」为后续待办 |
| P4-4 | 完整验收跑一遍 | 见下方汇总 |

**最终验收表**

| 项 | 命令 | 期望 |
|---|---|---|
| Go 编译 | `go build ./...` | exit 0 |
| Go vet | `go vet ./core/... ./vendors/... .` | exit 0 |
| Go 测试 | `go test ./core/... ./vendors/...` | 全 ok |
| 竞态 | `go test ./core/manager/ -race` | PASS |
| 前端类型 | `npx tsc --noEmit` | exit 0 |
| 前端构建 | `npm run build` | exit 0 |
| Rust | `cargo check --manifest-path src-tauri/Cargo.toml` | exit 0 |
| 真实数据 | 矩阵接口返回 ≥1 格、建议非空 | 通过 |

---

## 交接提示词（交给下一个执行者）

```
你在 D:\AI_Projects\opencode2api_guide\opencode2api_enhance，分支 refactor/custom-only。
先读 docs/ai-framework/plans/2026-11-05-call-metrics.md 全文，特别看「实测数据约束」一节
—— 引擎的 egress/exit_id 实测恒为空，所以绝对不要实现「出口维度」，只做模型×通道×密钥。

按 P0 → P4 顺序做，每阶段验收表全绿再进下一阶段。
硬约束：不引第三方 Go 依赖；指标只存 key_id 不存完整 key；写入失败不影响推理；
不改引擎源码（GPL 隔离）。

P0 结束时应该有一个能被单测覆盖的 AppendCallMetric / ReadCallMetrics。
P1 的关键是增量消费 stdout.log（记偏移，避免重复写）与 request_id join。
P2 的 suggestions 规则表在本文件里，照它实现，不要自己发明判据。
P3 只改 DiagPage.tsx 与 api.ts，不要新增路由或一级菜单。
```

---

## 风险与回退

| 风险 | 缓解 |
|---|---|
| stdout.log 无限增长 | 已有日志轮转机制；指标只记偏移不复制内容 |
| 增量消费误判偏移导致漏记 | 偏移文件损坏时回退到「整文件重扫 + 去重（按 req_id）」 |
| 指标文件写到一半崩溃 | 逐行 JSON + 读时跳过损坏行，不整文件 fail |
| 用户清空数据目录 | 指标随之清空，属预期；UI 显示空态 |

**回退方式：** 删除 `core/manager/callmetric.go` 与 4 个接口注册、回退 `DiagPage.tsx` 的区块即可，无 schema 变更、无数据迁移。
