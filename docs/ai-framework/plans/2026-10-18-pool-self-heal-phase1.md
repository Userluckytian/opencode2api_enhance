# 阶段 P1：节点池自愈（429 可恢复 + 池空显式报错 + 健康重置）

> **状态：** 已完成（本地自动化验收通过；未部署，真实 429 场景待复验）
> **For agentic workers:** 按 Task 顺序执行；每 Task 验证后再进下一 Task。
> **交接提示词**见文末「给接手 AI 的完整提示词」。
> **元规范：** `docs/ai-framework/phased-plan-driven.md`

**Goal:** 让「全部出口被 429 打入坏池」的免费通道在不重启任何进程的情况下自动恢复；池空时返回明确错误而不是空代理地址，并暴露可查询/可重置的池健康状态。

**Architecture:** 改动集中在网关进程（`package main`）的 SOCKS 健康表与传输层，外加一个共享的 `contract` 哨兵错误。坏池分类从「账号类永久」拆成「认证类永久 / 额度类长冷却 / 链路类短冷却」；额度类到期放行 1 个恢复探测；无候选时返回 `ErrNoHealthyNode`，由 `vendors/opencode` 映射为 503 `no_healthy_node`，不再构造空代理 client。管理器自愈阶梯、UI、全池重启**不在本阶段**。

**Tech Stack:** Go（`net/http`、`sync/atomic`、既有 SOCKS5 池）、既有 `go test` 套件、React 前端仅改类型定义（不新增页面）。

**实施档位：** 全能（默认）
**子代理：** 启用（独立代码审查 + 阶段验收；若用户改选，按档位在验收表登记跳过项）

---

## 前置阅读（必须）

| 优先级 | 文件 |
|--------|------|
| P0 | `docs/issue-log/2026-10-18.md`（本次事故取证与三个问题的分析） |
| P0 | 本文件 |
| P0 | `socks.go`、`socks_perf.go`、`socks_state.go` |
| P0 | `core/contract/contract.go`、`vendors/opencode/chat.go` |
| P1 | `gateway_timeout.go`（`recordCall` / `routeVerdict`）、`models_source.go`（`rootTransport`） |
| P1 | `AGENTS.md`、`docs/ai-framework/phased-plan-driven.md` |

**仓库路径：** `D:\AI_Projects\opencode2api_guide\opencode2api_enhance`
**基线分支：** 从 `main` 拉 `fix/phase-p1-pool-self-heal`

---

## 取证结论（本阶段的事实基础）

2026-10-18 本机日志（`%APPDATA%\opencode2api-manager\runtime\_unified-gateway\`）：

- 01:00 起上游对 `deepseek-v4.1-flash-free` 连续返回 `429 FreeUsageLimitError`；每个出口累计 3 次 429 后进入账号类坏池，`badUntil` 为零、永久禁用（`socks.go:233-243`、`socks.go:335-352`）。
- 01:35–01:40，42104/42107/42106/42103/42109 全部进坏池；01:53:57 最后两个出口 42108/42110 熔断。
- 此后 `pickWeightedProxy` 无候选，返回零值代理（`socks_perf.go:584-585`）；`getHTTPClientWithProxy` 返回 `clientForProxy(empty)` + 空地址（`socks.go:437`），dial 立即失败。
- call_log 中 opencode 的 2598 条 `direct_unexpected` **全部 fail**，耗时约 35ms；不是真实直连。
- 08:08 网关进程重启后才恢复：内存 `socks5Health` / 熔断表被清空，不是自愈机制。
- 现有探活看不到该故障：`health.go` 只测 TCP 端口；`poolquality.go` 测 `GET /v1/models`，该端点走本地目录缓存（样本延迟 0~1ms），不触达上游 chat。
- 付费层（`zen`/`go`/`auto`+key）直连是设计行为（`socks.go:480-484`）；本阶段不得改变。

---

## Global Constraints（冲突时以本节为准）

1. **付费层直连语义不变**：`getHTTPClientForTierWithProxy(TierPaid)` 仍返回 `httpClient, ""`。
2. **坏池分类**：401 = 认证类永久；402/429 = 额度类长冷却（可恢复）；503 等 = 链路类短冷却（保持现状）。
3. **池空不得静默直连**：免费层且 `active_socks5` 已配置、但没有可用节点时，必须返回明确错误，不得落到 `httpClient`。
4. **不重启进程**：本阶段不调用 `RestartPool`、不自动重启实例/网关；恢复只靠健康状态过期与探测。
5. **明确不做（本阶段）**
   - ❌ 管理器 `PoolSupervisor`（探测/定向重启/全池重启阶梯）
   - ❌ 前端池健康卡片、重置按钮、真实 chat 金丝雀
   - ❌ 修改付费通道端点/身份契约
   - ❌ 修改 `call_log` 5000 条窗口与统计清空语义（另案，见 `2026-10-18.md` 问题 2/3）
6. **Git：** 小步 commit；**默认不 push**。
7. **YAGNI：** 不新增调度器；额度冷却只用一个网关配置键 + 常量上限。

---

## 阶段开头：上阶段遗留

无（本阶段是独立修复，不承接上一阶段验收项）。

以下既有开放项**不在本阶段范围**：`refreshOCSession` 的 `sync.Once` 复用、`*_test.go` gitignore、`core/manager` 负载敏感 flaky。

---

## 与前后阶段

| 阶段 | 状态 | 交付 |
|------|------|------|
| 本阶段 P1 | ⬜ | 429 可恢复、池空显式报错、池健康查询/重置端点、单测与文档 |
| P2 | ⬜ | 管理器 PoolSupervisor：轮询池健康、按阶梯 reset → 定向重启 → 网关重启；UI 状态与按钮 |
| P3 | ⬜ | 真实 chat 金丝雀、全池重启开关、恢复指标与告警 |

---

## File Structure（预期变更）

| 文件 | 动作 | 职责 |
|------|------|------|
| `socks.go` | 修改 | 坏池分类（auth/quota/link）、额度类冷却与回退探测、健康表快照/重置辅助 |
| `socks_perf.go` | 修改 | `pickWeightedProxy` 无候选语义保持；必要时补 quota 探测路径 |
| `socks_state.go` | 修改 | 新增 `no_healthy_node` 路由结论 |
| `models_source.go` | 修改 | `rootTransport.Client` 在「已配置但无节点」时返回哨兵 client，并计数 |
| `core/contract/contract.go` | 修改 | `ErrNoHealthyNode` 哨兵 + `NoCandidateClient()` |
| `vendors/opencode/chat.go` | 修改 | 哨兵错误映射为 503 `no_healthy_node`，不重试 |
| `gateway_timeout.go` | 修改 | `recordCall` 识别哨兵并写 `route_verdict` |
| `pool_health.go` | 新建 | 池健康快照 + `GET /api/admin/pool/health` + `POST /api/admin/pool/reset-health` |
| `main.go` | 修改 | 注册两个管理端点 |
| `types_config.go` / `config.go` | 修改 | `quota_bad_pool_reset_sec` 配置读取 |
| `core/manager/config.go` / `opencodecfg.go` | 修改 | 配置字段、get/set、透传到网关 `opencode2api.json` |
| `src/lib/api.ts` | 修改 | `RouteVerdict` 增加 `"no_healthy_node"`（仅类型） |
| `*_test.go` | 新建/修改 | 429 恢复、哨兵错误、健康快照/重置、route verdict |
| `docs/DEPLOYMENT.md`、`CHANGELOG.md` | 修改 | 新配置与行为说明 |
| `docs/issue-log/2026-10-18.md`、`OPEN.md` | 修改 | 状态回填 |

---

## 配置或 API 契约

### 新配置

```json
{
  "quota_bad_pool_reset_sec": 600
}
```

- 含义：额度类（402/429）坏池的**首次**冷却秒数；默认 600，允许 60~1800。
- 退避：节点每次再次 429 时按 `base × 2^(badCount-阈值)` 递增，上限 1800s；附加 0~60s 随机抖动，避免全池同时探测。
- 401 不使用该配置（永久，直到手动重置或重启）。

### 新端点

```http
GET /api/admin/pool/health
```

```json
{
  "configured": true,
  "active_socks5": "__round_robin__",
  "route_mode": "smart",
  "total": 6,
  "available": 0,
  "cooling": 0,
  "bad_quota": 6,
  "bad_auth": 0,
  "breaker_open": 0,
  "no_candidate_total": 12,
  "last_no_candidate_ts": "2026-10-18T02:00:01+08:00",
  "nodes": [
    {
      "addr": "127.0.0.1:42103",
      "state": "bad_quota",
      "reason": "429：最大额度上限",
      "bad_count": 4,
      "until": "2026-10-18T02:10:00+08:00",
      "breaker": "closed",
      "failures": 4
    }
  ]
}
```

```http
POST /api/admin/pool/reset-health?scope=quota
POST /api/admin/pool/reset-health?scope=all
POST /api/admin/pool/reset-health?scope=node&node=127.0.0.1:42103
```

```json
{
  "reset_health": 6,
  "reset_breakers": 6,
  "scope": "quota",
  "nodes": ["127.0.0.1:42103", "..."]
}
```

- `scope` 缺省 = `quota`；`all` 清全部健康/熔断状态；`node` 只清指定地址。
- 端点只清网关内存状态，不动实例、不动网关进程、不动质量分/历史日志。
- 必须走既有 `requireAuth`。

### 哨兵错误与路由结论

- `contract.ErrNoHealthyNode`：免费层已配置代理但无可用节点。
- `route_verdict = "no_healthy_node"`：替代本次事故里误导性的 `direct_unexpected`。
- 客户端响应：HTTP 503，body `{"error":{"message":"no healthy proxy node","type":"no_healthy_node"}}`。

---

## Task 1：429/402 改为额度类可恢复坏池

**Files:** `socks.go`、`socks_perf.go`、`socks_test.go`（或新增 `pool_health_test.go`）

**行为:**
- `socks5HealthState` 增加 `badClass string`（`auth` / `quota` / `link`）。
- `badPoolClass(status)`：401 → auth；402/429 → quota；503 等 → link。
- `markSocks5Result`：
  - auth：`badUntil` 保持零值（永久），行为不变；
  - quota：`badUntil = now + quotaBadPoolResetDuration(badCount) + jitter`，`badProbeUsed=false`；
  - link：沿用 `badPoolResetSec`（默认 300s）。
- 新增 `quotaBadPoolResetSec atomic.Int64`（默认 600）与 `quotaBadPoolResetMaxSec = 1800`。
- 冷却到期后，`badPoolProbeReady` 已能放行 1 次探测（现有逻辑无需重写，只补注释与分类）。
- 探测成功：`markSocks5Result(2xx)` 删除健康表项并复位熔断（现有逻辑）。
- 探测再次 429：重新进坏池，退避递增，下一窗口再探测。

**Steps:**

1. 改分类与冷却写入；补 `badClass` 到健康表。
2. 加配置读取（见 Task 4）。
3. 跑：`go test -count=1 -run 'TestBadPool|TestSocks|TestPool' .` 期望 ok。
4. Commit：`🐛 fix(pool): 429 坏池改为可恢复长冷却`

---

## Task 2：池空返回明确错误，不再构造空代理

**Files:** `core/contract/contract.go`、`models_source.go`、`vendors/opencode/chat.go`、`gateway_timeout.go`、`socks_state.go`、`src/lib/api.ts`

**行为:**
- `contract` 增加：
  - `var ErrNoHealthyNode = errors.New("no healthy proxy node")`
  - `func NoCandidateClient() *http.Client`：`RoundTripper` 恒返回 `ErrNoHealthyNode`；
  - 不改变 `Transport` 接口（避免破坏 custom/remote 实现）。
- `rootTransport.Client`：
  - 仅当 `tier == TierFree`、返回地址为空、且 `active_socks5` 非空（显式配置了代理）时，返回 `contract.NoCandidateClient(), ""`；
  - `active_socks5` 为空 = 用户明确直连，保持 `httpClient`；
  - 每次返回哨兵 client 时 `noCandidateTotal.Add(1)` 并记录时间戳。
- `vendors/opencode/chat.go`：
  - `client.Do` 返回 `errors.Is(err, contract.ErrNoHealthyNode)` 时**不重试**、不 `Mark`（addr 为空，Mark 本身也会跳过）；
  - 流式/非流式都返回 `status=503` + 上述 JSON body；非流式同时返回哨兵 error 供上层记录。
- `gateway_timeout.go`：
  - `routeVerdict` 增加 `errMsg` 参数；`recordCall` 调用处传入 `rec.ErrMsg`；
  - 当 `errMsg` 含 `no healthy proxy node` / `no_healthy_node` 时返回 `RouteVerdictNoHealthyNode`；
  - 保留 `direct_by_design` / `direct_config_missing` / `direct_unexpected` 现有语义。
- `socks_state.go` 增加枚举；`src/lib/api.ts` 的 `RouteVerdict` 增加 `"no_healthy_node"`。

**Steps:**

1. 加哨兵与 client；改 `rootTransport.Client`。
2. 改 opencode `call()` 的错误分支。
3. 改 route verdict 与前端类型。
4. 跑：`go test -count=1 -run 'TestNoHealthyNode|TestRouteVerdict|TestDirect' .` 与 `go test -count=1 ./vendors/opencode/`。
5. Commit：`🐛 fix(pool): 池空返回 503 而非空代理直连`

---

## Task 3：池健康快照 + 重置端点

**Files:** 新建 `pool_health.go`、修改 `main.go`、新增 `pool_health_test.go`

**行为:**
- `snapshotPoolHealth()`：
  - 锁序：先短暂持 `socks5Mu.RLock` 复制代理列表与 `active_socks5`；释放后再持 `socks5HealthMu` 复制健康表；再持 `poolBreakerMu` 复制熔断表（**不得嵌套持锁**）。
  - 节点 `state` 取值：`healthy` / `cooling` / `bad_quota` / `bad_auth` / `bad_link` / `breaker_open`；同时暴露 `until`、`bad_count`、`failures`。
  - `available` = 未进坏池、未冷却、熔断非 open 的节点数（与 `pickWeightedProxy` 口径一致；实现时在注释里写明近似口径）。
  - `no_candidate_total` / `last_no_candidate_ts` 来自 Task 2 的原子计数。
- `resetPoolHealth(scope, node)`：
  - `quota`：删除 `badClass == "quota"` 的健康项，并删除这些地址的熔断项；
  - `auth`：同上（可选，便于手动恢复 401）；
  - `all`：清空 `socks5Health` 与 `poolBreakers`；
  - `node`：只处理指定地址；
  - 返回重置数量与地址列表；不动 `socks5Proxies`、不动质量分、不动日志。
- `main.go` 注册：
  - `GET /api/admin/pool/health`
  - `POST /api/admin/pool/reset-health`
  - 均套 `loggingMiddleware(requireAuth(...))`，与 `/api/admin/pool/quality` 同款。

**Steps:**

1. 实现快照与重置辅助函数。
2. 实现 handlers 并注册路由。
3. 单测：构造 3 个节点（healthy / quota bad / breaker open），断言快照计数；调用 reset 后断言状态与计数变化；并发读写下 `-race` 通过。
4. 跑：`go test -race -count=1 -run 'TestPoolHealth' .`。
5. Commit：`✨ feat(pool): 新增池健康查询与重置端点`

---

## Task 4：配置透传与文档

**Files:** `types_config.go`、`config.go`、`core/manager/config.go`、`core/manager/opencodecfg.go`、`docs/DEPLOYMENT.md`、`CHANGELOG.md`

**行为:**
- 网关 `AppConfig` 增加 `QuotaBadPoolResetSec int`（json `quota_bad_pool_reset_sec`）。
- `config.go` 加载时：`>0` 写入 `quotaBadPoolResetSec` 原子变量。
- 管理器 `Config` 增加同名字段；`configGet/configSet` 与 `ConfigView` 补该键；`opencodecfg.go` 用 `applyIf` 透传到 `runtime/_unified-gateway/opencode2api.json`。
- `DEPLOYMENT.md` 配置表补一行；`CHANGELOG.md` 记录行为变更（429 不再永久禁用、池空返回 503）。
- `docs/issue-log/2026-10-18.md` 更新问题 1 状态为「修复中/已修复待验证」，附测试证据；`OPEN.md` 同步。

**Steps:**

1. 改配置结构与读写。
2. 跑：`go build ./...`。
3. 跑：`go test -count=1 ./...`（若已知 flaky 失败，按 OPEN.md 记录，单包重跑取证，不得静默忽略）。
4. Commit：`🔧 chore(pool): 透传额度坏池冷却配置并补文档`

---

## 代码审查（阶段级环节，验收前）

**审查方：** 独立角色（非本阶段实现者；建议用 `cat-jam` 或人工）

**审查面：**

| 审查项 | 结论（✅/⚠️/❌） | 问题清单 |
|--------|------------------|----------|
| 风格（与周边代码一致） | | |
| 测试完整性（429 恢复 / 池空 / 重置 / 付费直连不回归） | | |
| 锁序与并发（`socks5Mu` → `socks5HealthMu` → `poolBreakerMu` 不嵌套） | | |
| 安全（管理端点鉴权、无密钥、无任意文件写） | | |
| API 契约（route_verdict 前后端一致、503 body 稳定） | | |
| 架构红线（付费直连不变、无进程重启、无管理器耦合） | | |

**结论：** ✅ 通过 / ⚠️ 有条件通过（问题进验收表）/ ❌ 不通过（阻塞）

---

## 验收标准总表

| # | 标准 | 通过条件 | 验证责任人 |
|---|------|----------|-----------|
| 1 | 429 不再永久禁用 | 单测：3×429 后 `badUntil` 非零且约等于 base；401 仍为零值 | 自动化（执行方） |
| 2 | 额度冷却后可恢复 | 单测：把 `badUntil` 拨到过去后，`pickWeightedProxy`/`pickHealthyProxy` 返回该节点；2xx 后健康表项被删除 | 自动化 |
| 3 | 退避与上限 | 连续 429 时冷却递增，最终不超过 1800s；抖动在 0~60s | 自动化 |
| 4 | 池空不直连 | 单测：全部坏池且未到期 → `Client` 返回 `ErrNoHealthyNode`，不返回 `httpClient`；opencode 返回 503 `no_healthy_node` | 自动化 |
| 5 | 路由结论准确 | call log 的 `route_verdict` 为 `no_healthy_node`，不再是 `direct_unexpected` | 自动化 + 日志抽样 |
| 6 | 健康端点 | `GET /api/admin/pool/health` 返回 total/available/bad_quota/breaker_open；数字与构造状态一致 | 自动化 |
| 7 | 重置端点 | `POST reset-health?scope=quota` 只清额度类；随后请求进入 proxied；`scope=all` 清空 | 自动化 |
| 8 | 付费层不回归 | `TierPaid` 仍返回 `httpClient, ""`；现有付费直连测试全绿 | 自动化 |
| 9 | 构建/测试 | `go build ./...` exit 0；`go test -count=1 ./...` 通过（flaky 按既有约定取证） | 自动化 |
| 10 | 文档 | DEPLOYMENT/CHANGELOG/issue log/OPEN 均已更新 | 执行方 |
| 11 | 代码审查 | 审查结论 ✅ 或 ⚠️（问题已登记）；❌ 则阻塞 | 独立角色 |
| 12 | 红线 | 无进程重启调用、无密钥入库、默认不 push | 执行方 |

---

## 风险与降级

| 风险 | 缓解 |
|------|------|
| 上游 429 是账号级配额，恢复探测也会 429 | 探测频率由 600s 起、退避到 1800s、每次只放 1 个节点；不重启、不扇出 |
| 429 被错误恢复导致继续打上游 | 只有冷却到期才放 1 次探测；失败立即重新坏池并递增退避 |
| 健康快照与真实选择口径不一致 | 快照只用于观测；`available` 近似口径在注释与文档中写明，不以它做路由决策 |
| 重置端点被滥用导致瞬间打上游 | 仅 `requireAuth` 管理面；scope 限制；文档注明「重置不等于恢复，仍需探测」 |
| 改动破坏现有 socks 测试 | 先跑基线；429 永久断言按新语义更新，并保留 401 永久用例 |
| `.gitignore` 使 `*_test.go` 不入库 | 本地跑测试并在验收表登记；不把测试文件强行入库（既有策略） |

---

## 验收结果（2026-10-18，本地）

**结论：代码完成，本地自动化通过；未部署，真实 429 场景待复验。**

分支：`fix/phase-p1-pool-self-heal`（未 push）

| # | 提交 | 说明 |
|---|------|------|
| 1 | `d951bb7` | 落库本实施计划 |
| 2 | `700dd21` | 429 坏池改为可恢复长冷却 |
| 3 | `a5bb973` | 池空返回 503 而非空代理直连 |
| 4 | `8df0107` | 新增池健康查询与重置端点 |
| 5 | `00add7f` | 配置透传与文档/issue log |
| 6 | `fd328bd` | 补齐 ConfigGet/ConfigSet/ConfigView 接线 |
| 7 | `41ba8dc` | 坏池分类注释对齐 + gofmt |
| 8 | `861176b` | 移除误入库的测试与本地日志 |

| 验收项 | 结果 | 证据 |
|--------|------|------|
| 429 不再永久禁用 | ✅ | `TestQuotaClassBadPoolHasExpiry` / `TestQuotaClassBadPoolExpiryProbeSelectable` |
| 401 仍永久 | ✅ | `TestQuotaClassAuthStillPermanent`（badUntil 零值、不可探测） |
| 退避与上限 | ✅ | `TestQuotaClassBadPoolBackoffCap`（≤1800s） |
| 池空不直连 | ✅ | `TestRootTransportNoCandidate`、`TestNoHealthyNodeMapsTo503` |
| 路由结论 | ✅ | `TestRouteVerdictNoHealthyNode` |
| 健康/重置端点 | ✅ | `TestPoolHealthSnapshotAndReset`、`TestPoolHealthHandlerAuth`、`TestPoolResetAll`、`TestPoolHealthRace` |
| 付费层不回归 | ✅ | `TestRootTransportNoCandidate` 内付费分支断言 |
| 构建 | ✅ | `go build ./...` |
| 根包测试 | ✅ | `go test -count=1 .`（42.4s） |
| 受影响包测试 | ✅ | `go test -count=1 ./vendors/... ./core/contract ./core/router ./core/aggregator` |
| 全量测试 | ⚠️ | `go test -count=1 ./...` 唯一失败为既有 flaky `TestHTTPRequestChunked`；单独 `-count=3` 通过，其余包全绿 |
| 文档 | ✅ | DEPLOYMENT / CHANGELOG / issue log / OPEN 已更新 |

**偏差说明：**
- 主体实现由 `cat-jam` 完成；后续 `cat-jam` 会话无法产出（0 tokens），配置接线与 auth 测试修正由验收方直接收尾。
- `*_test.go` 按仓库策略仅本地保留，未入库；分支提交中不包含测试文件。
- 全量测试的 `TestHTTPRequestChunked` 失败为 OPEN.md 已登记的既有负载敏感 flaky，不是本阶段引入。

---

## 给接手 AI 的完整提示词

将下面整段粘贴给执行 AI 即可开工：

---

你是负责 **opencode2api_enhance** 的实现代理。请**完整执行本阶段**，不要只写方案。

### 基线
- 目录：`D:\AI_Projects\opencode2api_guide\opencode2api_enhance`
- 从 `main` 创建并切换：`fix/phase-p1-pool-self-heal`
- 已完成：`docs/issue-log/2026-10-18.md` 已记录事故取证；不要重做分析
- 唯一实施计划：`docs/ai-framework/plans/2026-10-18-pool-self-heal-phase1.md`
- 必读：`docs/ai-framework/phased-plan-driven.md`、`AGENTS.md`、`docs/issue-log/2026-10-18.md`

### 做
1. 按 Task 1–4 顺序实现：429/402 额度类可恢复坏池；池空返回 `ErrNoHealthyNode` → 503 `no_healthy_node`；新增 `GET /api/admin/pool/health` 与 `POST /api/admin/pool/reset-health`；配置透传与文档。
2. 每完成一个 Task，先跑该 Task 指定的测试，再 commit（格式见 AGENTS.md）。
3. 最后跑 `go build ./...` 与 `go test -count=1 ./...`，把真实输出写进验收表。
4. 更新 `docs/issue-log/2026-10-18.md` 问题 1 的状态与验证证据，并同步 `OPEN.md`。

### 不做
- 不实现管理器 PoolSupervisor、不自动重启实例/网关、不做全池重启。
- 不修改付费层直连语义（`TierPaid` 恒直连）。
- 不修改调用日志 5000 条窗口与统计清空语义（问题 2/3 另案）。
- 不提交密钥；未授权的 `git push`。

### 工作方式
1. 先跑基线测试/构建确认干净。
2. **严格按 Task 顺序**；每 Task 测试后 commit。
3. 证据优先：完成前必须重跑计划中的验证命令，禁止「应该能过」。
4. 锁序：`socks5Mu` → `socks5HealthMu` → `poolBreakerMu`，不得嵌套持锁。
5. 用简体中文回复进度；代码标识符保持原样。

### 交卷
全部完成后给出：分支名、提交列表、验收表自评、测试/构建真实输出、残留风险。

现在开始：读完本阶段计划，从 Task 1 执行到最后。

---

## 残留手工验收清单

1. 在测试环境把全部出口模拟为 429（或直接构造健康表），确认冷却到期后请求恢复 `proxied`，且无需重启网关。
2. 调 `GET /api/admin/pool/health`，确认 `bad_quota` / `available` / `no_candidate_total` 与日志一致。
3. 调 `POST /api/admin/pool/reset-health?scope=quota`，确认下一次请求不再返回 `no_healthy_node`（若上游确实已恢复）。
4. 确认付费 key 请求仍走直连（`direct_by_design`），未被本阶段改动影响。
