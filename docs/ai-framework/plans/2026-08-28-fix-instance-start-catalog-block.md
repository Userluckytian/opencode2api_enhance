# 阶段 1：修复「实例池全部实例 15s 启动超时」——启动期模型目录刷新异步化

> **状态：** 已完成（验收表 #4 真机端到端为遗留手工项，待用户验证）
> **For agentic workers:** 按 Task 顺序执行；每 Task 测完再进下一 Task。
> **元规范:** `docs/ai-framework/phased-plan-driven.md`

**Goal:** 实例子进程（含统一网关子进程）冷启动不再因自定义供应商上游直连不可达而阻塞监听端口，消除「opencode2api 在 15s 内未能监听 0.0.0.0:40100」100% 复现的启动失败。

**Architecture:** 根因是 `main()` 在 `http.ListenAndServe` **之前**同步调用 `refreshModelCatalog()`（60s ctx 上限），配置了自定义供应商后，子进程启动会对该上游发起真实 HTTP 拉取（未开 via_proxy 时 TierPaid=直连）；上游不可达时子进程迟迟不监听，manager 的 15s 就绪等待必然超时。修复：把启动期目录刷新移入后台协程（先监听后刷新），目录未就绪时 `/v1/models` 已有 `refreshModelCatalogIfDue` 冷启动防惊群路径（10s 最小间隔）+ custom 厂商磁盘缓存兜底，异步化不引入新的语义缺口。

**Tech Stack:** Go 1.x（同仓库单模块），`log/slog`，`go test`（httptest/mock，不触网）。

---

## 前置阅读（必须）

| 优先级 | 文件 |
|--------|------|
| P0 | 本文件 |
| P0 | `docs/issue-log/2026-08-28.md`（根因分析全文） |
| P1 | `AGENTS.md`、`CODE_REVIEW.md`、`docs/AI-TESTING-GUIDE.md` |

**仓库路径：** `D:\AI_Projects\opencode2api_guide\opencode2api_enhance`
**基线分支：** `main`（commit 478d395）→ 拉 `feat/fix-startup-catalog-block`

---

## Global Constraints（冲突时以本节为准）

1. 单元测试不触网：mock/httptest 为准；本阶段**不启动任何真实服务**（无需端口检查，但仍禁占其它环境端口）。
2. **明确不做（本阶段）**
   - ❌ 不改 manager 侧 15s 等待/端口逻辑（`core/manager/instance.go`、`core/manager/tcp.go`）；
   - ❌ 不改 `isPortFree` 127.0.0.1 探测盲区（已登记为独立隐患，另行立项）；
   - ❌ 不改 `rebuildVendors` 内的同步 `refreshModelCatalog()`（custom_providers.go:179，其调用方均已在异步上下文；保存 API 阻塞属另一议题）；
   - ❌ 不动七页 UI。
3. **Git：** 小步 commit，中文 gitmoji 规范；**默认不 push**。
4. **YAGNI：** 不加配置项、不加开关；最小 diff。
5. 每步改动后 `go test -count=1 ./...` 全绿才 commit。

---

## 阶段开头：上阶段遗留

无（本计划为独立 bugfix 阶段）。

---

## File Structure（预期变更）

| 文件 | 动作 | 职责 |
|------|------|------|
| `models_source.go` | 修改 | 新增 `startInitialCatalogRefresh()`：后台协程执行 `refreshModelCatalog()` + 迁移原 `models loaded` 日志 |
| `main.go` | 修改 | `main()` 中同步 `refreshModelCatalog()` 及紧随的 models loaded 日志块替换为 `startInitialCatalogRefresh()` |
| `models_source_async_test.go`（或并入既有测试文件，实现者按仓库习惯定） | 新建 | 回归单测：启动刷新不阻塞调用方 + 目录最终被填充 |

---

## 行为契约（修复前后对比）

| 场景 | 修复前 | 修复后 |
|------|--------|--------|
| 实例子进程冷启动（配置含自定义供应商，上游直连不可达） | 阻塞 ≤60s 才监听 → manager 15s 超时报错，100% 失败 | 立即监听（<1s），目录后台刷新，实例正常 Running |
| 实例子进程冷启动（无自定义供应商） | 正常 | 行为不变（刷新变异步，目录就绪略延后，无功能影响） |
| `/v1/models` 在目录就绪前被请求 | 不可能出现 | 走 `refreshModelCatalogIfDue` 冷启动路径（models.go:101），custom 磁盘缓存兜底 |
| 主管理器自身启动 | 同样可能被 60s 阻塞 | 一并异步化（同一 `main()`） |

---

## Task 1：新增 `startInitialCatalogRefresh()` 并替换 main() 同步调用

**Files:** `models_source.go`、`main.go`

**Steps:**

1. 在 `models_source.go` 的 `refreshModelCatalog` 附近新增函数（含上述契约的注释说明，注释风格对齐文件内既有中文注释）：

   ```go
   // startInitialCatalogRefresh 启动期目录刷新异步化（先监听后刷新）。
   // 背景：实例子进程与主管理器同一 main()；配置自定义供应商后，同步刷新会对
   // 其上游直连发起真实拉取（60s ctx 上限），上游不可达时进程迟迟不监听——
   // manager 实例启动 15s 就绪等待必超时（2026-08-28 实例池全挂事故）。
   // 目录未就绪期间 /v1/models 走 refreshModelCatalogIfDue 冷启动路径
   // （10s 防惊群），custom 厂商另有磁盘缓存兜底首请求。
   func startInitialCatalogRefresh() { ... go func(){ refreshModelCatalog(); ...日志... }() }
   ```

2. `main.go:142` 处：`refreshModelCatalog()` → `startInitialCatalogRefresh()`；删除紧随其后的 `modelMu.RLock()/len(modelsCache)/models loaded` 日志块（日志迁入新函数）。
3. 复核 `main.go` 中 `refreshModelCatalog` 调用点之后到 `ListenAndServe` 之间无其它目录就绪依赖（预期无）。
4. 跑：`go build ./... && go vet ./...`
   期望：均 exit 0。
5. 跑：`go test -count=1 ./...`
   期望：全绿（既有测试不依赖启动同步刷新）。
6. Commit：`🐛 fix(startup): 启动期模型目录刷新异步化——自定义源上游不可达不再阻塞实例子进程监听（15s 启动超时根因）`

## Task 2：回归单测

**Files:** `models_source_async_test.go`（新建，或按仓库习惯并入既有 `*_test.go`）

**Steps:**

1. 写测试（根 package `main`，风格对齐 `models_agg_test.go`）：
   - 用可注入的假 vendor（参考仓库既有 fake/stub 模式）构造 `globalAgg`：其 `ListModels` 阻塞 ≥1s 后返回模型（用 channel/time.After，**不触网**）；
   - 测试开始前保存 `globalAgg` / 相关全局态，`t.Cleanup` 恢复（防污染其它测试）；
   - 断言 A：调用 `startInitialCatalogRefresh()` 立即返回（<300ms）；
   - 断言 B：在 ≥2s 内轮询到目录被填充（`globalAgg.Catalog()` 非空）。
2. 跑：`go test -count=1 -run TestStartInitialCatalogRefresh -v .`
   期望：PASS，且总耗时 <3s。
3. 跑：`go test -count=1 ./...`
   期望：全绿。
4. Commit：`✅ test(startup): 启动目录刷新异步化回归——不阻塞调用方且目录最终填充`

## Task 3：文档闭环

**Steps:**

1. 更新 `docs/issue-log/2026-08-28.md`：问题 1 状态 → 已修复待验证，补真实测试输出。
2. 更新本计划状态头 → 已完成（由验收方执行亦可）。
3. Commit：`📝 docs(issue-log): 2026-08-28 实例池启动超时修复记录`

---

## 代码审查（阶段级强制环节，验收前）

**审查方：** 独立子代理（非实现者），按 `CODE_REVIEW.md` 三视角：风格 / 测试 / 依赖架构。

| 审查项 | 结论（✅/⚠️/❌） | 问题清单 |
|--------|------------------|----------|
| 风格 | 待审查 | |
| 测试完整性 | 待审查 | |
| 依赖与架构红线 | 待审查 | |

**结论：** ✅ 通过（独立审查子代理，2026-08-28；建议项「server starting 日志 models 字段恒 0」已采纳修复于 commit 9884194）

---

## 验收标准总表

| # | 标准 | 通过条件 | 验证责任人 |
|---|------|----------|----------|
| 1 | 新回归测试 | `go test -count=1 -run TestStartInitialCatalogRefresh -v .` PASS | 测试子代理 + 验收方复跑 |
| 2 | 全量单测/构建 | `go build ./... && go vet ./... && go test -count=1 ./...` exit 0 | 测试子代理 + 验收方复跑 |
| 2b | 代码审查 | 审查结论 ✅ 或 ⚠️（问题已登记）；❌ 则返工 | 审查子代理 |
| 3 | 红线 | 未触碰 Global Constraints 第 2 条「明确不做」；无密钥入库 | 验收方 |
| 4 | 端到端（真机，遗留手工项） | 用户环境：自定义供应商 A 保持配置，从节点池重建实例并启动 → 实例在 15s 内 Running；统一网关重启亦正常 | 用户（需真实上游环境，自动化无法覆盖） |

---

## 风险与降级

| 风险 | 缓解 |
|------|------|
| 目录未就绪窗口内客户端拿到不完整 `/v1/models` | 窗口 ≤ 刷新完成（有上游时秒级）；冷启动防惊群路径已存在；custom 磁盘缓存兜底首请求 |
| 某处隐藏代码假设「启动后目录必就绪」 | Task 1 Step 3 复核 + 全量测试；审查子代理专门排查 `modelsCache`/`Catalog()` 启动期读取方 |
| 测试并发污染全局 `globalAgg` | 保存/恢复 + t.Cleanup；`-count=1` 全量复跑验证 |

---

## 给接手 AI 的完整提示词

---

你是负责 **opencode2api_enhance** 的实现代理。请**完整执行本阶段**，不要只写方案。

### 基线
- 目录：`D:\AI_Projects\opencode2api_guide\opencode2api_enhance`（Windows / Git Bash）
- 从 `main` 创建并切换：`feat/fix-startup-catalog-block`
- 已完成：根因分析（见 `docs/issue-log/2026-08-28.md`），不要重做
- 唯一实施计划：`docs/ai-framework/plans/2026-08-28-fix-instance-start-catalog-block.md`
- 必读：本计划全文、`AGENTS.md`

### 做
1. Task 1 → Task 2 → Task 3 严格顺序；每步测试后 commit（中文 gitmoji 规范）。
2. 只改计划列出的文件；禁止触网测试；禁止启动真实服务。
3. Task 2 的假 vendor 必须复用/新建内存 stub，禁止真实 HTTP。

### 不做
- `core/manager/` 下任何改动；UI；配置项；push。

### 工作方式
1. 先跑 `go test -count=1 ./...` 确认基线全绿。
2. 证据优先：完成前重跑计划中全部验证命令并附真实输出。

### 交卷
给出：分支名、提交列表（hash+说明）、验收表自评、测试/构建真实输出、残留风险。

现在开始：读完本阶段计划，从 Task 1 执行到最后。

---

## 残留手工验收清单

1. 用户真机（生产/日常使用环境）：保持自定义供应商 A 配置不变，从节点池重新添加实例并启动，确认实例 15s 内 Running、`/v1/models` 正常返回；统一网关重启一次确认同样正常。
