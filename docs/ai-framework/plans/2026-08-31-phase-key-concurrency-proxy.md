# 阶段：重启目录兜底 × 多 key 会话粘性 × 实例路由分散 × 测试拉取走代理

> **状态：** 计划已就绪
> **For agentic workers:** 按 Phase 顺序执行；每 Phase 测试通过后进下一 Phase，全部完成后跑 `go test -count=1 ./...`。
> **交接提示词**见文末「给接手 AI 的完整提示词」。
> **元规范:** `docs/ai-framework/phased-plan-driven.md`

**Goal:** 一次性收敛四个用户反馈问题：重启后 opencode 模型目录不丢失、多 key 会话不串、多实例节点路由分散、测试拉取模型可选走本机系统代理。

**Architecture:** 四个问题相互独立，按 Phase 拆分；每个 Phase 都是可独立测试的小闭环（mock/httptest 优先，不触网）。核心改动集中在 `vendors/opencode`、`vendors/custom`、`socks.go`、`custom_providers.go`、`core/manager/opencodecfg.go` 与对应前端页面。

**Tech Stack:** Go 1.x（net/http / context / sync）、React+TS（src/pages）、Tauri 壳（仅配置透传，不涉及）。

---

## 前置阅读（必须）

| 优先级 | 文件 |
|--------|------|
| P0 | `docs/ai-framework/phased-plan-driven.md` |
| P0 | 本文件 |
| P0 | `docs/AI-TESTING-GUIDE.md`（端口/环境隔离，§3、§5） |
| P1 | `AGENTS.md`、`CODE_REVIEW.md`、`coding-standards.md` |

**仓库路径：** `D:\AI_Projects\opencode2api_guide\opencode2api_enhance`
**基线分支：** 从 `main` 拉 `feat/phase-key-concurrency-proxy`

---

## Global Constraints（冲突时以本节为准）

1. **最小修改原则**：不推倒重构，只改动必要部分；命名描述性，事件处理统一 `handle` 前缀。
2. **UI 纪律**：七页 UI 是唯一事实界面；改动页面布局/新增菜单需与七页对齐，禁止另起风格。
3. **测试纪律**：每 Phase 后 `go test -count=1 ./...` 全绿才提交；不触网 mock/httptest 为准；禁止占其它环境端口、禁止 kill 非自己启动的进程。
4. 密钥/凭证不进 git；`config.json` 含敏感字段不提交。
5. **Git：** 小步 commit，消息用 `<gitmoji><type>(scope): 中文描述`；**默认不 push**。
6. **明确不做（本阶段）**
   - ❌ 不动 windsurf / 插件供应商的调度逻辑（仅 opencode 与 custom 源）
   - ❌ 问题2 范围收紧：仅自定义供应商多 key 场景（opencode/windsurf 无 key 池，不涉及）
   - ❌ 不做「问题2 的多 key 用量统计 / 限流分摊」，只做会话不串
   - ❌ 不改 auto 虚拟模型选择算法
   - ❌ 不引入新依赖库（代理走标准库 `http.ProxyFromEnvironment` / `net/http.Transport`）

---

## 阶段开头：上阶段遗留（必填小节）

> 无（本阶段为新增计划，从 main 起步；问题1 的磁盘缓存已在 `feat/fix-startup-catalog-block` 分支实现但未合入 main，见 Phase 1 说明）。

---

## 与前后阶段

| 阶段 | 状态 | 交付 |
|------|------|------|
| `feat/fix-startup-catalog-block` | ⬜ 未合并 | 问题1 核心修复（摘取用） |
| **本阶段** | ⬜ | 四问题修复 + 测试 + 审查 |
| 下阶段 | | 真机端到端验证 / 后续问题 |

---

## File Structure（预期变更）

| 文件 | 动作 | 职责 |
|------|------|------|
| `vendors/opencode/cache.go` | 新建（从分支摘取） | opencode 目录磁盘缓存 |
| `vendors/opencode/opencode.go` | 修改 | ListModels 缓存兜底 + fetchOCVersion 5s 超时 |
| `vendors/opencode/cache_test.go` | 新建 | 缓存单测 |
| `vendors/custom/custom.go` | 修改 | key 选择增加会话粘性/续写同 key |
| `vendors/custom/keypool.go` | 修改 | 支持 preferred 优先 + 会话粘性标记 |
| `vendors/custom/keypool_test.go` | 修改 | key 粘性单测 |
| `gateway_timeout.go` | 修改 | 续写重连保留原 key（经 Extra 透传） |
| `socks.go` | 修改 | smart 模式推进游标（每请求轮询） |
| `socks_perf.go` | 修改 | pickWeightedProxy 增加分散（非恒选最高分） |
| `socks_test.go` / `poolquality_test.go` | 修改 | 路由分散单测 |
| `custom_providers.go` | 修改 | 测试拉取支持 `use_local_proxy` |
| `custom_providers_test.go` | 修改 | 测试拉取走代理单测 |
| `src/pages/CustomModelsPage.tsx` | 修改 | 测试动作新增「走系统代理」开关 |
| `src/lib/api.ts` | 修改 | CustomProviderTestInput 增字段 |
| `core/manager/opencodecfg.go` | 修改 | 网关 route_mode 默认值核对/透传 |
| `docs/issue-log/2026-08-31.md` | 修改 | 追加四问题分析/修改结果 |

---

## Phase 1：opencode 目录磁盘缓存兜底（问题1）

**Files:**
- 新建：`vendors/opencode/cache.go`
- 修改：`vendors/opencode/opencode.go`
- 测试：`vendors/opencode/cache_test.go`
- 修改：`main.go`（启动异步化）、`models_source.go`（startInitialCatalogRefresh）

**行为:**
1. 从 `feat/fix-startup-catalog-block` 摘取以下改动（**仅**这些，不整支合并）：
   - `vendors/opencode/cache.go`（磁盘缓存，`<DATA_DIR>/opencode_models/<id>.json`，原子写）
   - `opencode.go`：`ListModels` 失败/空目录回退内存→磁盘上一代；`SetCatalog(nil)` 不清缓存；`fetchOCVersion` 5s 短超时 + 非 2xx 回退默认版本
   - `main.go`：`refreshModelCatalog()` → `startInitialCatalogRefresh()`（异步）
   - `models_source.go`：新增 `startInitialCatalogRefresh()`（后台协程刷新，先监听后刷新）
   - `config.go`：`configStartupRewriteNeeded`（启动回写收窄）——**如与 main 现状冲突以 main 为准，仅保留必要部分**
2. 其余分支改动（供应商加固 / UI / 版本 bump 1.7.2）**不摘取**。

**Steps:**

1. 先核对 `feat/fix-startup-catalog-block` 与 main 在相关文件的差异：`git diff main feat/fix-startup-catalog-block -- vendors/opencode main.go models_source.go config.go`，确认摘取范围。
   期望：差异清晰、无意外依赖。

2. 写失败测试（TDD）：
   - 磁盘缓存不存在时 `New()` 不 panic，`ListModels` 失败返回上一代（先 `saveModelsCache` 再模拟失败）。
   - `SetCatalog(nil)` 不清空预热缓存。
   - `fetchOCVersion` 超时/非 2xx 返回默认版本（mock transport，不触网）。

3. 实现：摘取分支改动，适配 main 现状（如 `New()` 已固化 transport，见分支 diff 里的 `v.tr` 改造）。

4. 跑：`go test -count=1 ./vendors/opencode/...`
   期望：PASS（含既有 `cache_test.go`、`chat_test.go`、`cancel_test.go` 等全绿）。

5. 跑：`go test -count=1 ./core/aggregator/... ./...`（可放 Phase 末统一跑全量）
   期望：无回归。

6. Commit：`✨ feat(opencode): 模型目录磁盘缓存+版本探测短超时——重启后先出上一代目录`

---

## Phase 2：多 key 会话粘性 / 续写同 key（问题2）

**Files:**
- 修改：`vendors/custom/custom.go`、`vendors/custom/keypool.go`
- 修改：`gateway_timeout.go`
- 修改：`vendors/custom/keypool_test.go`、`vendors/custom/custom_test.go`
- 修改：`upstream.go`（Extra 透传 preferred key）

**行为:**
1. **范围**：仅自定义供应商多 key 场景（opencode/windsurf 无 key 池，不涉及）。
2. **核心手段（续写同 key）**：流式中断续写（`streamWithResume` → 重连 `callOpenCodeAPIStream`）时，把**首次选择的 key 索引**经 `contract.Message.Extra` 透传，`withKeysStream` 优先用该 key（同请求续写不换 key）。
3. **会话级粘性（次要）**：同一 `conversationKey`（可暂用请求里 `x-session-id` 或客户端特征 + 模型名，需与现有架构核对）下，尽量命中同一 key；无会话标识时保持现有 round_robin/failover。
4. 调用日志 `CallRecord` 增加 `KeyTail` 字段（key 末 4 位），便于定位串对话。

**Steps:**

1. 写失败测试：
   - `keypool_test.go`：`tryAcquirePrefer` 传 preferred 下标时优先命中；会话粘性键下重复调用稳定返回同一 key（除非被禁用/冷却）。
   - `custom_test.go`：Chat 携带 preferred key 后不轮询其它 key。
   - 续写场景：模拟 `withKeysStream` 首次 key=idx0、续写携带 idx0 → 命中 idx0。

2. 实现：keypool 增加 sticky 支持；custom `withKeys`/`withKeysStream` 读取 Extra 里的 `KeyPreferredIndex`；`streamWithResume` 重连时把当前 key 写入 Extra。

3. 跑：`go test -count=1 ./vendors/custom/...`
   期望：PASS。

4. 跑：`go test -count=1 ./...`（确认 gateway_timeout / upstream 无回归）
   期望：全绿。

5. Commit：`🐛 fix(custom): 多key会话粘性+续写同key——不再串对话/重复输出`

---

## Phase 3：多实例节点路由分散（问题3）

**Files:**
- 修改：`socks.go`、`socks_perf.go`
- 修改：`core/manager/opencodecfg.go`（核对 route_mode 默认与透传）
- 测试：`socks_test.go`、`socks_perf.go` 对应测试、`poolquality_test.go`

**行为:**
1. `getHTTPClientWithProxy` 的 smart/failover 分支：**每次请求也推进游标**（`atomic.AddUint32`），保留健康跳过/坏池/熔断逻辑——即「每请求轮询健康节点」而非「成功粘住一个节点」。
2. `pickWeightedProxy`：选最高分节点的同时加入少量随机扰动（jitter）或轮转，避免同一高分节点被连续选中；保证分数显著差异仍优先高分，同档分散。
3. 核对 UI 默认 `route_mode`（PoolPage 现状：smart 默认）与本实现一致；确认网关子进程 `buildRouterCfg` 透传正确。

**Steps:**

1. 写失败测试：
   - smart 模式下连续多次 `getHTTPClientWithProxy` 命中不同健康节点（mock 多个 proxy，均健康）。
   - 单节点健康时仍恒命中该节点（退化不回归）。
   - `pickWeightedProxy` 同分节点间有分散（多次调用覆盖多个候选）。
   - 既有 round_robin 测试不回归。

2. 实现：socks.go smart 分支推进游标；socks_perf.go 加 jitter/轮转。

3. 跑：`go test -count=1 ./...`（重点 socks、poolquality、gateway 相关）
   期望：全绿。

4. Commit：`✨ fix(pool): smart路由每请求推进游标+质量加权加抖动——实例不再扎堆`

---

## Phase 4：测试拉取模型走本机系统代理（问题4）

**Files:**
- 修改：`custom_providers.go`
- 修改：`src/pages/CustomModelsPage.tsx`、`src/lib/api.ts`
- 修改：`custom_providers_test.go`

**行为:**
1. 自定义源「测试并获取模型」请求体新增字段 `use_local_proxy: boolean`（默认 false=直连/节点池按 via_proxy）。
2. 后端 `customProvidersTestHandler`：当 `use_local_proxy=true` 时，测试用 custom vendor 的 Transport 使用本机系统代理（`http.ProxyFromEnvironment` 或显式配置地址），`via_proxy`（节点池）不叠加。
3. UI：测试弹窗/表单新增开关「走本机系统代理」，与既有「走节点池 via_proxy」区分说明。
4. 直连/节点池/系统代理三种路径在测试结果上可区分（latency/error 透传即可）。

**Steps:**

1. 写失败测试：
   - `custom_providers_test.go`：构造测试请求带 `use_local_proxy=true`，断言测试 vendor 的 transport 使用环境代理（可注入 `HTTP_PROXY` 环境变量 + mock transport 断言调用）。
   - 不带该字段（false）时仍走原逻辑（直连或节点池）。

2. 实现：`customProvidersTestHandler` 读取 `use_local_proxy`；构造 `custom.Config` 时按需换 Transport。

3. 前端：`CustomModelsPage.tsx` 表单加开关 + `api.ts` 类型/字段更新；开关文案与 via_proxy 区分。

4. 跑：`go test -count=1 ./...`（重点 custom_providers 相关）
   期望：全绿。

5. 前端构建校验（如项目有）：`npm run build` 或按项目脚本；若无则跳过并说明。

6. Commit：`✨ feat(custom): 测试拉取模型支持走本机系统代理开关`

---

## 代码审查（阶段级强制环节，验收前）

**审查方：** 独立角色（非本阶段实现者，建议 `@project-auditor` / `@code-stylespector`）

**审查面：** 代码风格 / 测试完整性 / 依赖合理性 / 架构红线 / 安全（密钥、注入、越权）/ API 契约一致性

| 审查项 | 结论（✅/⚠️/❌） | 问题清单 |
|--------|------------------|----------|
| 风格 | | |
| 测试完整性 | | |
| 依赖与架构红线 | | |
| 安全 | | |
| API 契约 | | |

**结论：** ✅ 通过 / ⚠️ 有条件通过（问题进验收表，❌ 下放下阶段）/ ❌ 不通过（阻塞）

---

## 验收标准总表

| # | 标准 | 通过条件 | 验证责任人 |
|---|------|----------|------------|
| 1 | Phase1 目录兜底 | `go test -count=1 ./vendors/opencode/...` PASS；缓存回退/版本超时单测通过 | 自动化（执行方） |
| 2 | Phase2 会话粘性 | `go test -count=1 ./vendors/custom/...` PASS；续写同 key 单测通过 | 自动化（执行方） |
| 3 | Phase3 路由分散 | `go test -count=1 ./...` 中 socks/poolquality PASS；smart 推进游标单测通过 | 自动化（执行方） |
| 4 | Phase4 测试走代理 | `go test -count=1 ./...` PASS；use_local_proxy 单测通过；前端开关到位 | 自动化（执行方） |
| 5 | 全量回归 | `go test -count=1 ./...` exit 0（无 flake 噪声） | 自动化（执行方） |
| 6 | 代码审查 | 审查结论 ✅ 或 ⚠️（问题已登记）；❌ 不通过则下放 | 独立角色 |
| 7 | 红线 | 无禁止项；密钥未入库；未占其它环境端口 | 自动化（执行方） |
| 8 | 密钥 | `git ls-files` 无敏感文件（config.json 不提交） | 自动化（执行方） |

---

## 风险与降级

| 风险 | 缓解 |
|------|------|
| Phase1 摘取分支改动与 main 有冲突 | 先 `git diff` 核对，冲突处按 main 现状改写，保留必要语义 |
| 会话粘性缺会话标识 | 先用「请求特征+模型」近似，或直接续写同 key（不依赖会话）兜底 |
| smart 推进游标影响 failover 语义 | 保留健康/坏池/熔断逻辑，仅把「成功粘住」改为「每请求轮询健康节点」，单节点退化不回归 |
| 系统代理在无代理环境下不可用 | use_local_proxy=false 默认；true 且无代理时报清晰错误提示 |

---

## 给接手 AI 的完整提示词

将下面整段粘贴给执行 AI 即可开工：

---

你是负责 **opencode2api_enhance** 的实现代理。请**完整执行本阶段**，不要只写方案。

### 基线
- 目录：`D:\AI_Projects\opencode2api_guide\opencode2api_enhance`
- 从 `main` 创建并切换：`feat/phase-key-concurrency-proxy`
- 已完成：问题根因分析（见本计划）；`feat/fix-startup-catalog-block` 分支含 Phase1 参考实现（只摘取，不整支合并）
- 唯一实施计划：`docs/ai-framework/plans/2026-08-31-phase-key-concurrency-proxy.md`
- 必读：`docs/ai-framework/phased-plan-driven.md`、`AGENTS.md`、`docs/AI-TESTING-GUIDE.md`

### 做
1. 严格按 Phase 1→4 顺序；每 Phase 写完测试+实现+跑通再 commit。
2. 测试用 mock/httptest，不触网；禁止占其它环境端口、禁止 kill 非自己启动的进程。
3. Phase1 用 `git diff main feat/fix-startup-catalog-block -- vendors/opencode main.go models_source.go config.go` 核对摘取范围，**不**整体合并该分支。
4. 每 Phase 后跑 `go test -count=1 ./...` 确认全绿。
5. Phase4 前端改动后按项目脚本构建校验（若无构建脚本则说明）。

### 不做
- 不推倒重构；不改 windsurf/插件供应商调度；不改 auto 模型选择。
- 提交密钥或 `config.json`；未授权的 `git push`。

### 工作方式
1. 先跑 `go test -count=1 ./...` 确认基线干净。
2. 每 Task 测试后 commit，消息 `<gitmoji><type>(scope): 中文描述`。
3. 证据优先：完成前必须重跑计划中的验证命令并贴真实输出。
4. 用简体中文回复进度；代码标识符保持原样。

### 交卷
全部完成后给出：分支名、提交列表、验收表自评（含每 Phase 测试输出）、残留风险、是否已跑全量 `go test -count=1 ./...`。

现在开始：读完本阶段计划，从 Phase 1 执行到最后。

---

## 残留手工验收清单

（自动化之外的 GUI / 真机项）

1. 真机重启网关：opencode 模型在重启后**立即**出现（无需等目录刷新）。
2. 配置 2 个 key 的真实供应商：长对话触发流中断后，续写由同一 key 承接，不重复输出。
3. 开 3+ 实例并发对话：节点在多个实例间分散，而非集中 2-3 个。
4. 测试拉取国外供应商：勾选「走本机系统代理」后能拉通，取消后按原逻辑。
