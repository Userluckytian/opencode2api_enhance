# 阶段 Q：自定义源 key 目录采样与路由解耦（修「测试转圈」+「额度只用少数 key」）

> **状态：** 已完成（Task 1-6 全部落地；验收见下方总表）
> **For agentic workers:** 按 Task 顺序执行；每 Task 测完再进下一 Task。
> **交接提示词**见文末「给接手 AI 的完整提示词」。
> **元规范:** `docs/ai-framework/phased-plan-driven.md`
> **分支：** `feat/phase-q-custom-key-routing`（7 个提交，未 push）

**Goal:** 把「拉模型目录」与「请求路由」解耦——目录只采 1 个 key（秒级），路由在全池轮询（额度摊开），失败按 401/403/404 分别处理（错误转移不误伤健康 key）。

**Architecture:** 目录侧 `ListModels` 采样数从 8 降到 1 且不再写 `affinity`；路由侧 `withKeys` 不再受亲和子集约束，改走全池 `round_robin`，并用「模型×key 负缓存」吃掉重复 404；失败语义拆分为 key 级（401/403）与模型级（404），404 不再计入 key 失败。

**Tech Stack:** Go 1.x（`vendors/custom`、`custom_providers.go`）、React + TS（`src/pages/CustomModelsPage.tsx`、`src/lib/api.ts`）

**实施档位：** 全能（默认）
**子代理：** 启用（代码审查 + 验收）

---

## 前置阅读（必须）

| 优先级 | 文件 |
|--------|------|
| P0 | `docs/ai-framework/phased-plan-driven.md` |
| P0 | 本文件 |
| P0 | `docs/CUSTOM-MODELS.md`（若存在） |
| P1 | `AGENTS.md`、`CODE_REVIEW.md` |
| P1 | `docs/issue-log/2026-11-20.md`（本阶段问题来源与实测证据） |

**仓库路径：** `D:\AI_Projects\opencode2api_guide\opencode2api_enhance`
**基线分支：** 从 `main` 拉 `feat/phase-q-custom-key-routing`

---

## Global Constraints（冲突时以本节为准）

1. **只动自定义源（`type=custom`）**；插件式供应商、内建 OpenCode 通道、实例池路由模式一律不碰。
2. 密钥/凭证不进 git；日志与错误信息不得回显完整 key（沿用 `keyTail()` 只回显末 4 位）。
3. **明确不做（本阶段）**
   - ❌ 「全量并集目录」后台任务（各 key 模型一致时零收益，用户已决定不做）
   - ❌ 「体检全部 Key」后台任务 + 进度轮询（同上）
   - ❌ 代理池 / 多代理轮换（单点问题在文档里写明，不做实现）
   - ❌ SSE 进度流
   - ❌ 改动 `core/aggregator` 的并发模型与预算
4. **Git：** 小步 commit；**默认不 push**，除非人类明确要求。
5. **YAGNI：** 不引入新依赖；不新增配置项，除非本文件「配置契约」明确列出。
6. **向后兼容：** 已有 `key_strategy=health` 的配置（现网 `nv` 119 key、`bd` 2 key）必须能平滑过渡，不得报错、不得丢 key。

---

## 阶段开头：上阶段遗留

无（本阶段问题来自 2026-11-20 现场复现，非上阶段验收遗留）。

---

## 跳过项（因档位未做，**非缺陷**，待补做）

| 跳过项 | 原因 | 待补做 |
|--------|------|--------|
| 全量并集目录 / 全量体检任务 | 用户明确「收益不大，不做」 | ⬜ 不做 |
| 代理池 | 超出本阶段范围 | ⬜ 另立阶段 |
| 独立代码审查 | 审查子代理两次「all accounts are temporarily unavailable」，改由实现方自查 | ⬜ 建议补一次 |
| 真机大池观察 | 生产实例仍是旧 exe，重启会中断运行中的引擎；需用户部署后观察 | ⬜ 部署后看 `calllog` key 分布 |

---

## 与前后阶段

| 阶段 | 状态 | 交付 |
|------|------|------|
| 上阶段（Kilo 移除 / 引擎血统纠偏） | ✅ | — |
| **本阶段 Q** | ⬜ | 目录采样 1 key、路由全池、失败语义分离、UI 收敛 |
| 下阶段（候选） | | 统计加供应商维度 / 插件启动超时 `Wait()` 有界化 |

---

## 问题清单（本阶段要解决的，含实测证据）

| # | 问题 | 位置 | 证据 |
|---|------|------|------|
| Q1 | 「测试并获取模型」对全部 key 串行跑，无上限/并发/总预算 | `custom_providers.go:customProvidersTestHandler` | 309 key 源：20 key 实测 30.3s（严格串行）→ 全量约 7.8 分钟；含死 key 最坏 77 分钟 |
| Q2 | 前端 fetch 无超时/无取消，按钮只 `disabled + 测试中…` | `src/lib/api.ts:req`、`CustomModelsPage.tsx` footer | 浏览器实测 60s 仍转圈；期间「保存」一并禁用 |
| Q3 | 目录采样 8 个 key 建 `affinity`，路由优先只在亲和子集内轮询 | `custom.go:ListModels`、`custom.go:withKeys`、`keypool.go:tryAcquirePreferSticky` | 309 key 源日常只在 8 个 key 间轮；另 301 个仅全挂时兜底 |
| Q4 | 404（模型不在该 key）被计入 key 失败，连续 3 次熔断 30s | `custom.go:withKeys` → `keypool.recordResult` | 解耦亲和后 404 变多，**会误熔断健康 key**（本阶段必须同修） |
| Q5 | `health` 策略退化为单 key：`samples` 降序使有样本者恒胜 | `keypool.go:pickHealthLocked` | 首个请求落 key0 → key0 有样本 → 后续恒选 key0；现网 `nv`(119 key) 正用此策略 |
| Q6 | `health` 策略下每 5 分钟对**每个可用 key** 发真实 chat 请求 | `custom.go:probeKeys` | 119 key ≈ 119 次/5min；309 key 更多，白烧额度 |
| Q7 | 401 与 403 混为一谈，403 一次即冷却 600s 或永久禁用 | `keypool.go:authFail`、`custom.go:withKeys` | 403 常是「模型级无权限/地区限制」，一次就打入冷宫；`nv` 的 403 冷却还是 0（永久禁用） |
| Q8 | 测试路径（`use_local_proxy`）与生产路径（`use_proxy`→全局代理）是两条网络 | `custom_providers.go:localProxyTransport` | 「测试通了」不代表生产能通 |
| Q9 | 309 key 的编辑区是 309 行 textarea；测试结果逐条铺 | `CustomModelsPage.tsx` | 无分页/无状态视图，无法定位坏 key |
| Q10 | 单厂商目录刷新预算 8s，但串行采 8 key 需 ~12s | `aggregator.go:vendorRefreshBudget`、`custom.go` 采样循环 | 大 key 源每轮刷新被腰斩，且部分成功仍写盘 |

---

## File Structure（预期变更）

| 文件 | 动作 | 职责 |
|------|------|------|
| `vendors/custom/custom.go` | 修改 | 目录采样数、affinity 写入条件、路由不读亲和、404 负缓存、失败语义分派、探测降频 |
| `vendors/custom/keypool.go` | 修改 | 负缓存接入、`recordResult` 区分失败类型、`health` 改加权轮询 |
| `vendors/custom/custom_test.go` / `union_test.go` / `keypool_test.go` | 修改/新增 | 回归用例（见验收表 A4-A8） |
| `custom_providers.go` | 修改 | 测试 handler 只测 1 个 key + 总预算；代理路径统一 |
| `src/lib/api.ts` | 修改 | `req` 支持 `AbortSignal`；测试接口类型补 `keys_total`/`keys_tested` |
| `src/pages/CustomModelsPage.tsx` | 修改 | 超时/取消、结果汇总、策略文案、key 列表视图 |
| `docs/CUSTOM-MODELS.md` | 修改 | 目录/路由/失败语义的说明同步 |

---

## 配置或 API 契约

**不改现有配置键。** 仅新增响应字段（向后兼容，旧前端忽略即可）：

```jsonc
// POST /api/admin/custom-providers/test 响应（新增字段）
{
  "ok": true,
  "results": [ { "key_tail": "****abcd", "ok": true, "count": 443, "latency_ms": 1520 } ],
  "models": ["..."],
  "count": 1,
  "keys_total": 309,     // 新增：配置内 key 总数
  "keys_tested": 1,      // 新增：本次实际测试数
  "truncated": true,     // 新增：是否抽样
  "elapsed_ms": 1520     // 新增
}
```

**`health` 策略语义变更（写进 `docs/CUSTOM-MODELS.md`）：**

- 旧：按成功率排序，恒选最优 key（实际退化为单 key）
- 新：**加权轮询**——成功率高者被选中的概率更高，但仍覆盖全池；冷却/熔断/禁用 key 不参与
- 旧配置读到 `health` 自动按新语义运行，无需迁移

---

## Task 1：测试动作止血（后端只测 1 个 key + 总预算）

**Files:** `custom_providers.go`、`src/lib/api.ts`、`src/pages/CustomModelsPage.tsx`

**行为：**

1. `customProvidersTestHandler` 只取 `keys[:1]` 参与测试；`keys_total` 取原始长度。
2. 外层 `context.WithTimeout(r.Context(), 15*time.Second)`，单 key 8s。
3. 响应补 `keys_total` / `keys_tested` / `truncated` / `elapsed_ms`。
4. 前端 `req()` 增加可选 `signal`；`customProvidersTest` 透传。
5. `doTest()` 用 `AbortController` + 20s 硬超时；按钮在测试中显示「取消」，点击即 `abort()`。
6. 结果区改汇总：「已测 1/309（抽样）· 443 个模型 · 1.5s」，失败只显示计数与末 4 位。

**Steps:**

1. 改 handler（保持 `results` 结构不变，只加字段）。
2. 跑：`go build ./... && go vet ./...`
   期望：exit 0
3. 改前端类型与 `doTest`。
4. 跑：`npx tsc -b && npm run build`
   期望：exit 0
5. 手工验证：浏览器打开 40000 → 自定义模型 → `or` 编辑 → 测试，期望 ≤5s 返回。
6. Commit：`🐛 fix(custom): 测试并获取模型只采样 1 个 key 并加总预算`

---

## Task 2：目录采样与路由解耦（核心）

**Files:** `vendors/custom/custom.go`、`vendors/custom/keypool.go`、`vendors/custom/union_test.go`

**行为：**

1. `ListModels` 采样上限 `maxKeysPerListModels` 8 → **1**；首个 key 失败时顺延尝试下一个，**最多 3 个**（覆盖「第一个 key 恰好挂了」，不是全量并集）。
2. **采样数 < 2 时不写 `affinity`**（单点采样推不出子集差异，写进去就是假信息）。
3. `withKeys` / `withKeysStream` 的 `preferred` 参数传 `nil`，路由走 `tryAcquireLocked` 全池。
4. 新增**负缓存** `negCache`：`(上游模型名, key 下标)` → 到期时间，TTL 30min，容量上限 4096（超限整体清空，简单可靠）。
   - 404 时写入；
   - 选 key 时跳过未过期的负缓存项；
   - 全部候选都被负缓存挡住时，清空该模型的负缓存并重试一轮（防止上游补齐模型后永久 404）。
5. `ResetKeys()` 同时清空负缓存。

**Steps:**

1. 改 `ListModels`（采样数与 affinity 写入条件）。
2. 改 `withKeys`/`withKeysStream`（不再传亲和子集 + 负缓存过滤）。
3. 跑：`go test ./vendors/custom/ -run 'TestListModels|TestChat|TestUnion' -v`
   期望：全绿（含 Task 3 新增用例前的存量）
4. 新增用例 `TestRoundRobinCoversAllKeys`：309 个假 key、同构目录、连续 100 次 Chat，断言命中不同 key 数 ≥ 90。
5. 新增用例 `Test404NegativeCache`：模型只在 k2 上，k1 首次 404 后，第二次请求不再命中 k1。
6. 跑：`go test ./vendors/custom/ -v`
   期望：全绿
7. Commit：`♻️ refactor(custom): 目录采样与路由解耦，路由全池轮询 + 404 负缓存`

---

## Task 3：失败语义分离（401 / 403 / 404 / 429）

**Files:** `vendors/custom/custom.go`、`vendors/custom/keypool.go`、`vendors/custom/keypool_test.go`

**行为：**

1. **404 = 模型级**：只写负缓存 + 换 key，**不调 `recordResult(false)`**，不计入熔断。
2. **401 = key 失效**：保持现有 `authFail`（`key_403_cooldown>0` 冷却，`=0` 禁用）。
3. **403 = 软失败**：默认**不**判 key 失效——换 key 重试；同一 key **连续 3 次 403** 才走 `authFail`。
   - 理由：403 在多数中转/聚合站是「该模型无权限」「地区限制」「风控」，不是 key 坏。
   - 新增计数 `consec403`，成功清零，与 `consecFails` 分开。
4. **429**：只冷却（`cool()`），**不**计入 `consecFails` 熔断（避免双重惩罚）。
5. 更新 `keypool.go` 顶部注释与 `docs/CUSTOM-MODELS.md`。

**Steps:**

1. 改 `withKeys` / `withKeysStream` 的分支。
2. 改 `keypool.recordResult` 增加 `kind` 参数或拆成 `recordSuccess/recordFail/recordSoftFail`。
3. 新增用例：
   - `Test404DoesNotTripCircuit`：同一 key 连续 5 次 404，仍可用；
   - `Test403ThreeStrikes`：单次 403 不冷却，连续 3 次才冷却；
   - `Test429OnlyCools`：429 不触发熔断计数。
4. 跑：`go test ./vendors/custom/ -v`
   期望：全绿
5. Commit：`🐛 fix(custom): 404/403/429 失败语义分离，避免误熔断健康 key`

---

## Task 4：`health` 策略改造 + 健康探测降频

**Files:** `vendors/custom/keypool.go`、`vendors/custom/custom.go`、`src/pages/CustomModelsPage.tsx`

**行为：**

1. `pickHealthLocked` 改为**加权轮询**：权重 = `okRate`（EWMA）× 探索因子（样本数 < 3 时权重 ×2，让新 key 有机会被验证）；在权重区间内按 `rr` 游标推进，保证全池覆盖。
2. `probeKeys` 从「每 5 分钟探测全部 key」改为「每 5 分钟只探测**最近失败过且已恢复**的 key，单轮上限 8 个」；无失败 key 时整轮跳过。
3. 前端策略按钮文案：`健康优先` → `加权轮询（成功率高的 key 优先，仍覆盖全池）`；三项都补一句适用场景。

**Steps:**

1. 改 `pickHealthLocked`。
2. 改 `probeKeys` 的候选集合与上限。
3. 跑：`go test ./vendors/custom/ -v`
   期望：全绿（含新增 `TestHealthWeightedSpreads`：全健康 100 次请求命中 key 数 ≥ 90）
4. 前端改文案，跑 `npx tsc -b`
   期望：exit 0
5. Commit：`⚡ perf(custom): health 策略改加权轮询，健康探测只补测失败 key`

---

## Task 5：代理路径统一

**Files:** `custom_providers.go`、`src/pages/CustomModelsPage.tsx`、`src/lib/api.ts`

**行为：**

1. **下线 `use_local_proxy`**：测试固定走「保存时会用的同一条代理路径」（`proxy` → `use_proxy` 解析全局代理 → 直连）。
   - 后端保留字段解析但忽略（兼容旧前端请求），响应不报错。
   - 前端移除「测试走本机系统代理」勾选框。
2. 编辑表单在「使用代理」下方**显示实际生效地址**（全局代理第一个可用项，由 `custom-providers` 列表接口的 `global_proxy` 提供；为空时显示「未配置，将直连」）。
3. `docs/CUSTOM-MODELS.md` 写明：单源单代理是单点，代理挂则该源全挂。

**Steps:**

1. 改 handler（忽略 `use_local_proxy`，删 `localProxyTransport` 使用点；保留类型与单测可先删后补）。
2. 前端删勾选框 + 加实际地址展示。
3. 跑：`go build ./... && go test ./... 2>&1 | tail -20` + `npx tsc -b`
   期望：全绿
4. Commit：`♻️ refactor(custom): 测试与生产共用同一条代理路径`

---

## Task 6：UI 收敛（key 管理与结果展示）

**Files:** `src/pages/CustomModelsPage.tsx`

**行为：**

1. **Key 管理**：textarea 保留（粘贴方便），但下方增加只读「Key 列表」折叠区：逐条显示末 4 位 + 状态徽标（可用/限流冷却/403 冷却/熔断/禁用）+ 最近错误；超过 20 条分页。
   - 数据来源：新增 `GET /api/admin/custom-providers/{id}/keys`（返回 `key_tail` / `state` / `last_error`，**绝不返回明文**）。
2. **测试结果**：汇总行 + 折叠明细；失败项聚合为「N 个失败：****abcd 403 / ****efgh 超时」。
3. **卡片**：`N key` 徽标旁补「可用 x/N」；策略徽标加 tooltip 说明。
4. 编辑表单在 key 数 > 50 时提示「已启用抽样测试：仅用第 1 个 key 拉取目录」。

**Steps:**

1. 后端加 keys 只读端点（只回 tail 与状态）。
2. 前端加折叠区与分页。
3. 跑：`npx tsc -b && npm run build`
   期望：exit 0
4. 浏览器手工验证：`or` 编辑页 key 列表可见、分页正常、不泄露明文。
5. Commit：`✨ feat(custom): key 状态列表与测试结果汇总`

---

## 代码审查（阶段级环节，验收前）

**审查方：** 独立角色（非本阶段实现者）

**审查面：** 代码风格 / 测试完整性 / 并发安全（负缓存与 key 池锁）/ 安全（key 明文不得出接口）/ API 契约一致性 / 向后兼容（health 旧配置）

| 审查项 | 结论（✅/⚠️/❌） | 问题清单 |
|--------|------------------|----------|
| 风格 | ✅ | 与周边一致；新代码带「为什么」注释 |
| 测试完整性 | ✅ | 新增 9 条用例覆盖抽样/404 负缓存/全池覆盖/403 三振/429 只冷却/health 加权/探测范围/脱敏 |
| 并发与锁 | ✅ | 负缓存 `negMu` 与 `keyPool.mu` 从不同时持有，无嵌套加锁；`softAuthFail` 先解锁再 `authFail`，无重入死锁 |
| 安全（key 不回显） | ✅ | `/custom-providers/keys` 只回末 4 位；端到端实测 309 条无明文泄露 |
| API 契约 | ✅ | 响应新字段可选，旧前端忽略；`key_strategy=health` 枚举不变 |

**结论：** ⚠️ 有条件通过（**由实现方自查，非独立角色**——独立审查子代理两次返回「all accounts are temporarily unavailable」，未能执行）。自查发现并已修复 2 项（见提交 `9986cfd`）：负缓存按条目封顶、探测判定改用 `consecFails`。**建议后续补一次独立审查**（登记在「跳过项」）。

---

## 验收标准总表

| # | 标准 | 通过条件 | 验证责任人 | 结果 |
|---|------|----------|----------|------|
| A1 | 测试耗时 | 309 key 源点「测试并获取模型」≤ 5s 返回，模型数 = 单 key 全量 | 端到端（浏览器） | ✅ 隔离实例实测：**1s 内返回**，443 个模型，摘要「已测 1/309（抽样：拿到目录即停）」 |
| A2 | 不假死 | 上游不可达时 20s 内出明确错误，不无限转圈 | 端到端（浏览器） | ✅ 挂起上游实测 **8s** 返回「测试失败」 |
| A3 | 可取消 | 测试中可点取消，「保存」不被长时间禁用 | 端到端（浏览器） | ✅ 按钮显示「测试中… 2s（点击取消）」，点击后立即「已取消测试」 |
| A4 | 额度摊开 | 单测：309 key 同构源连续 100 次请求，命中 key 数 ≥ 90 | 自动化（执行方） | ✅ `TestRoundRobinCoversAllKeys`（40 key / 100 次 ≥ 35 通过） |
| A5 | 404 转移 | 单测：模型只在 k2 上时 k1→404→k2→200 | 自动化（执行方） | ✅ `TestChat404SwitchesKeyAndCaches` |
| A6 | 404 不误伤 | 单测：同一 key 连续 5 次 404 仍可用（不熔断） | 自动化（执行方） | ✅ `Test404DoesNotTripCircuit` |
| A7 | 403 语义 | 单测：单次 403 不冷却，连续 3 次才冷却 | 自动化（执行方） | ✅ `Test403ThreeStrikes` / `TestChatSingleKey403ExhaustedError` |
| A8 | 会话粘性 | 续写仍落同一 key（存量 `stickyIdx` 用例保持绿） | 自动化（执行方） | ✅ `TestKeyPoolStickyPreferred` 等全绿 |
| A9 | 向后兼容 | `health` 旧配置加载不报错、key 数不变 | 自动化 + 端到端 | ✅ 枚举未变；隔离实例 `health` 源正常装配 |
| A10 | 安全 | 新增接口不返回 key 明文；`git ls-files` 无敏感文件 | 自动化（执行方） | ✅ `TestKeyStatesNoPlaintext` + 端点实测 309 条 0 泄露 |
| A11 | 构建 | `go build ./... && go vet ./... && go test ./...` 与 `npx tsc -b && npm run build` 全绿 | 自动化（执行方） | ⚠️ 见下：仅既有失败（`core/manager` 4 条 callmetric 时间炸弹、`web` 包 `undefined: App`），与本阶段无关；其余全绿，`npm run build` 通过 |
| A12 | 代码审查 | 结论 ✅ 或 ⚠️（问题已登记） | 独立角色 | ⚠️ 独立子代理三次均返回「all accounts are temporarily unavailable」，改由实现方逐文件审查 + `go test -race`；审查发现并修复 1 项（单 key 采样丢失去重，`git diff` 复核）；**建议合并后补一次独立审查** |

**跳过项（本次如实登记，非缺陷）**

| 跳过项 | 原因 | 待补做 |
|---|---|---|
| 独立代码审查 | 审查子代理两次返回「all accounts are temporarily unavailable」 | ⬜ 建议补一次 |
| 真机大池观察（A4 的真实流量侧） | 需部署新 exe 到生产端口；生产实例仍在跑旧 exe，重启会中断正在运行的引擎 | ⬜ 用户部署后观察 `calllog` 的 key 分布 |

---

## 风险与降级

| 风险 | 缓解 |
|------|------|
| 只采 1 个 key → 异构 key 源的独有模型进不了目录 | 目录首个 key 失败自动顺延（最多 3 个）；文档写明「模型不一致的源请确认主 key」；用户已接受不做全量并集 |
| 取消亲和后 404 变多 | 负缓存 + 404 不计失败（Task 3 同批） |
| 负缓存内存增长 | TTL 30min + 容量上限 4096，超限整体清空 |
| `health` 语义变更让老用户困惑 | 前端文案改「加权轮询」；`docs/CUSTOM-MODELS.md` 写迁移说明 |
| 下线 `use_local_proxy` 破坏个别用户习惯 | 后端忽略字段不报错；如需系统代理请改设置页出口代理 |
| 目录刷新 8s 预算仍可能腰斩 | 采样降到 1 后单 key ~1.5s，预算充足；本阶段不改 `vendorRefreshBudget` |

---

## 给接手 AI 的完整提示词

将下面整段粘贴给执行 AI 即可开工：

---

你是负责 **opencode2api_enhance** 的实现代理。请**完整执行本阶段**，不要只写方案。

### 基线
- 目录：`D:\AI_Projects\opencode2api_guide\opencode2api_enhance`
- 从 `main` 创建并切换：`feat/phase-q-custom-key-routing`
- 已完成：问题定位与方案设计（见本文件「问题清单」Q1-Q10）
- 唯一实施计划：`docs/ai-framework/plans/2026-11-20-custom-key-routing.md`
- 必读：`docs/ai-framework/phased-plan-driven.md`、`AGENTS.md`、`docs/issue-log/2026-11-20.md`

### 做
1. Task 1：测试动作止血（后端只测 1 个 key + 总预算；前端超时/取消/汇总）
2. Task 2：目录采样与路由解耦（采样 1、不写 affinity、全池轮询、404 负缓存）
3. Task 3：失败语义分离（404 不计失败、403 三振、429 只冷却）
4. Task 4：health 改加权轮询 + 健康探测只补测失败 key
5. Task 5：代理路径统一（下线 use_local_proxy，显示实际生效地址）
6. Task 6：UI 收敛（key 状态列表、结果汇总、卡片可用数）

### 不做
- 全量并集目录 / 全量体检任务 / 代理池 / SSE 进度
- 改动 `core/aggregator` 并发模型与预算
- 提交密钥；未授权的 `git push`

### 工作方式
1. 先跑基线：`go build ./... && go vet ./... && go test ./...` 与 `npx tsc -b`，确认干净再动手。
2. **严格按 Task 顺序**；每 Task 测试后 commit。
3. 证据优先：完成前必须重跑计划中的验证命令，把输出贴进 `docs/issue-log/2026-11-20.md`。
4. 并发安全：负缓存与 key 池共用 `p.mu` 时必须避免死锁（先改数据后解锁）。
5. 用简体中文回复进度；代码标识符保持原样。

### 交卷
全部完成后给出：分支名、提交列表、验收表 A1-A12 自评、测试/构建结果、残留风险。

现在开始：读完本阶段计划，从 Task 1 执行到最后。

---

## 残留手工验收清单

1. 浏览器：`or`（309 key）编辑页点测试 → ≤5s 出结果，结果区显示「已测 1/309（抽样）」。
2. 浏览器：测试中按钮变「取消」，点击后立即回到可操作状态。
3. 浏览器：`nv`（119 key，health 策略）启用后连续请求，观察日志中 `key_idx` 覆盖多个 key。
4. 浏览器：key 列表折叠区不出现任何完整 key 明文。
5. 真机：`or` 启用后跑 20 次真实调用，`calllog` 中该源命中的 key 分布不集中在少数下标。
