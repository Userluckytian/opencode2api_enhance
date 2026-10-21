# 阶段 Y：token 自助餐厅管理后台（IP 治理 + 分析 + AI 问数）

> **状态：** 实施中
> **实施档位：** 全能
> **子代理：** 不启用（单人顺序实现，按 Task 自测）

**Goal:** 把管理后台从「能看」升级为「能管、能分析」：修掉状态误报与明文密钥等缺陷，新增按 IP 的请求汇总、窗口额度（token 或活跃时长先到先拦）、输出限速与独立覆盖策略，并提供趋势/建议/AI 问数。

**Architecture:** 网关在鉴权与转发之间插入 IP 策略检查点；日志新增 IP 字段作为一切统计的单源；策略与窗口用量分别持久化到 `ip_policies.json` / `ip_usage.json`；管理端新增 IP 策略、聚合日志、AI 问答 API。领取 Key 页面不做任何改动（「留个门」）。

**Tech Stack:** Go 1.22 `net/http`、`go:embed` 静态页、原生 HTML/CSS/JS、JSONL 日志。

---

## 前置阅读（必须）

| 优先级 | 文件 |
|--------|------|
| P0 | `AGENTS.md.bak`（仓库规则：证据验收 / 密钥不入库 / 破坏性操作需授权） |
| P0 | 本文件 |
| P0 | `web/gateway.go`、`web/admin.go`、`web/logger.go` |
| P1 | `docs/issue-log/OPEN.md`、`docs/issue-log/2026-10-20.md` |

**基线分支：** `feature/web-self-service`（当前分支，不新建分支）

---

## Global Constraints

1. 默认策略：每 IP 每 24h 窗口最多 100 万 token 或 120 分钟活跃时长，先到先拦；输出限速 60 token/s；均可全局改、可按 IP 覆盖。
2. 拦截信息对调用方可见（429 + 原因 + 重置时间）；**领取 Key 页面不展示任何 IP 策略信息**。
3. IP 来源：仅当直连对端是回环/私网代理时才信任 `X-Forwarded-For` / `X-Real-IP`，否则用 `RemoteAddr`，防止伪造。
4. 密钥/上游 Key 不进 git；管理页上游 Key 默认密码框、Key 列表默认打码。
5. **明确不做：** 领取页改动、IP 归属地查询、跨天历史窗口持久化清理（保留文件）、AI 流式输出。
6. Git：小步 commit；默认不 push。

---

## 阶段开头：上阶段遗留

无（本阶段基于 `feature/web-self-service` 现状）。

---

## 实施任务

### Task 1 — 缺陷与安全
- [x] `handleListKeys` 输出 `expired` / `exhausted` / `status`
- [x] 改密码后吊销其它会话（保留当前会话）
- [x] 登录失败限速（5 次/5 分钟，锁 1 分钟）
- [x] 日志支持日期 + Key + IP 过滤
- [x] Key 列表打码 + 复制/查看、状态徽标、搜索/筛选/排序/批量删除
- [x] 上游 Key 改密码框；alert/confirm 换成 toast + 模态框

### Task 2 — IP 治理（领导要求 1/2/3/4）
- [x] 日志写入 IP；被拦截请求也落日志（status/reason）
- [x] `ippolicy.go`：默认策略 + 按 IP 覆盖 + 持久化
- [x] `iptracker.go`：窗口 token / 活跃时长 / 重置时间 / 封禁 / 拦截计数 + 持久化
- [x] 网关检查点：429 明确返回原因与重置时间
- [x] SSE/非流式输出限速（token 桶 + 字符估算，4 字符≈1 token）
- [x] IP 管理 API + 页面（默认策略、搜索、覆盖编辑、重置、封禁/解封）

### Task 3 — 分析
- [x] 日志按 IP 聚合 API（请求数 / token / Key 数 / 错误率 / 最近活跃）
- [x] 概览：7 日趋势、错误数、活跃 IP、拦截数、Key 状态分布
- [x] 配额顾问：周预算 → 日均/窗口建议，与实际用量对比

### Task 4 — AI 问数
- [x] `POST /api/admin/ask`：聚合统计注入提示词，复用上游模型
- [x] AI 问数页面（建议问题、对话记录）

---

## File Structure（预期变更）

| 文件 | 动作 | 职责 |
|------|------|------|
| `web/ip.go` | 新建 | clientIP / 可信代理 / 登录限速 |
| `web/ippolicy.go` | 新建 | 默认与按 IP 覆盖策略、持久化 |
| `web/iptracker.go` | 新建 | 窗口用量、拦截判定、持久化 |
| `web/throttle.go` | 新建 | 输出限速与 token 估算 |
| `web/ipstats.go` | 新建 | 日志按 IP 聚合、7 日趋势 |
| `web/ask.go` | 新建 | AI 问数 |
| `web/gateway.go` | 修改 | IP 检查、日志 IP、限速 |
| `web/admin.go` / `web/main.go` | 修改 | 新 API 与路由 |
| `web/logger.go` | 修改 | LogEntry 增加 ip/error |
| `web/static/admin.html` / `admin.js` | 修改 | 新导航与页面 |
| `web/*_test.go` | 新建/修改 | 证据 |

---

## API 契约

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/admin/ip-policies` | `{defaults, overrides[], usage[]}` |
| PUT | `/api/admin/ip-policies/defaults` | 全局默认策略 |
| PUT | `/api/admin/ip-policies/{ip}` | 覆盖策略（null=继承） |
| DELETE | `/api/admin/ip-policies/{ip}` | 删除覆盖 |
| POST | `/api/admin/ip-policies/{ip}/reset` | 重置窗口用量 |
| GET | `/api/admin/logs?date=&key_id=&ip=` | 明细过滤 |
| GET | `/api/admin/logs/aggregate?date=` | 按 IP 汇总 |
| POST | `/api/admin/ask` | `{question, model?}` → `{answer}` |

策略字段：`window_tokens`、`window_minutes`、`max_active_minutes`、`output_tps`、`disabled`、`note`；`0` 表示不限。

---

## 验收表

| # | 验收项 | 证据 |
|---|--------|------|
| 1 | `go vet ./web/...` 通过 | ✅ 命令输出无报错 |
| 2 | `go test ./web/... -count=1` 全绿 | ✅ `ok github.com/6Kmfi6HP/opencode2api/web 1.368s` |
| 3 | `go test ./web/... -race -count=1` 全绿 | ✅ `ok ... 2.567s`（无 race 报告） |
| 4 | 过期 Key 在 API/UI 显示「已过期」 | ✅ `handleListKeys` 输出 `expired/exhausted/status`；前端 `keyStatus()` 渲染徽标 |
| 5 | XFF 仅可信代理生效 | ✅ `TestClientIPIgnoresForwardedHeaderFromPublicPeer` / `...UsesForwardedHeaderFromPrivateProxy` / `...SkipsTrustedHops` / `...FallsBackToRealIP` / `...NormalizesIPv6` |
| 6 | 窗口 token 用尽 → 429 + 原因 + 重置时间 | ✅ `TestGatewayBlocksWhenIPWindowExhausted`（429 + 「额度已用尽」+ `Retry-After` + 落日志）；本地 curl 封禁实测 429 |
| 7 | 窗口时长用尽 → 429 + 原因 | ✅ `TestIPTrackerBlocksWhenActiveDurationExhausted`（「时长已用尽」） |
| 8 | 按 IP 覆盖优先于默认；删除覆盖回落默认 | ✅ `TestIPPolicyOverrideWinsOverDefault` / `TestIPPolicyPersistsToDisk` / `TestAdminIPPolicyAPI`；本地 curl 验过删除后回落 |
| 9 | 输出限速 60 t/s 生效且不阻塞上下文取消 | ✅ `TestOutputThrottleDelaysToRespectRate`（1000 t/s 等待 ≥900ms）、`TestOutputThrottleNilMeansUnlimited`、`TestEstimateTokens`、`TestSSEDeltaTextExtraction` |
| 10 | 日志 IP 字段 + 聚合接口正确 | ✅ `TestGatewayRecordsIPOnSuccess`、`TestAdminLogAggregateEndpoint`、`TestE2EAdminAPIsThroughRealRoutes`；本地 curl 聚合正确归集 |
| 11 | 改密后旧会话失效、当前会话保留 | ✅ `TestPasswordChangeRevokesOtherSessions` |
| 12 | 登录爆破被锁 | ✅ `TestLoginLimiterLocksAfterRepeatedFailures` |
| 13 | AI 问答在无模型/上游失败时返回明确错误 | ✅ `TestAskEndpointReturnsUpstreamAnswer`、`TestAskEndpointRejectsWithoutModel`、`TestE2EAskRouteWired` |
| 14 | 领取页不泄露 IP 策略（留个门） | ✅ `TestClaimPageDoesNotExposeIPPolicy`；`index.html`/`app.js` 零改动 |
| 15 | 后台页面与新 API 对接 | ✅ `TestE2EAdminPagesExposeNewViews`（页面含「IP 管理」「AI 问数」，`admin.js` 对接 4 个新接口） |

**本地端到端（独立数据目录 + curl，测试后已清理）**

| 步骤 | 结果 |
|------|------|
| 登录 → 领 Key → `/v1` 调用 | 鉴权正常（上游不可达返回 502 属预期） |
| 覆盖策略 PUT/GET | 300 万 token / 120 分钟 / 80 token/s 生效；中文备注 UTF-8 落盘正确 |
| 封禁 127.0.0.1 后 `/v1` | **429 `该 IP 已被管理员封禁，请联系管理员`**；解封后恢复 |
| 按 IP 聚合接口 | 1 个 IP / 1 次请求 / 1 次拦截 / 错误率 100%，归集正确 |
| 删除覆盖 / 删除 `ip_policies.json` 重启 | 均正确回落默认策略 |
| 浏览器 7 页渲染 | 概览/Key/日志/IP/模型/参数/AI 问数全部渲染正常，无横向溢出、无零高度卡片（1440×900 实测） |
| Key 打码/显示/复制 | 列表显示 `sk-••••••••898f`，👁 可显示明文，⧉ 复制成功 toast |
| 日志明细 → 汇总 → 下钻 | 明细含 IP 列与 429 拦截备注；汇总显示错误率 17%、被拦截 1；「查看明细」可下钻 |
| IP 策略全流程（浏览器） | 覆盖保存 → 「单独设置 / 300 万 / 80 token/s」+ 备注 → 封禁 → 「已封禁」+ 网关 429 → 解封 → 删策略 → 回落「继承默认 / 100 万 / 60 token/s」 |
| AI 问数 | 页内发送问题后拿到上游回答气泡 |

---

## 给接手 AI 的完整提示词

> 在 `feature/web-self-service` 分支实现「阶段 Y」。先读本计划与 `web/gateway.go`、`web/admin.go`。按 Task 顺序实现，每个 Task 完成后跑 `go test ./web/... -count=1`。所有新逻辑必须有单测，验收表逐项填证据。不要改 `web/static/index.html` / `app.js`（领取页）。不要 push。完成后更新 `docs/issue-log/2026-10-20.md` 与 `OPEN.md`。
