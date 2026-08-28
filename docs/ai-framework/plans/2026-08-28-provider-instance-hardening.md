# 阶段 2：供应商×实例池并发与生命周期加固（3 个反证审查缺口）

> **状态：** 实施中
> **For agentic workers:** 按 Task 顺序执行；每 Task 测完再进下一 Task。
> **元规范:** `docs/ai-framework/phased-plan-driven.md`

**Goal:** 修复 2026-08-28 时序反证审查发现的 3 个缺口：插件状态文件多写者丢更新（高）、runtime 配置非原子写与子进程启动回写丢条目（中高）、插件孤儿仅启动期回收（中）。

**Architecture:** 缺口 1 根因是 `updateStateFile` 用进程启动时的陈旧全量快照覆写共享状态文件 + 固定 tmp 名跨进程碰撞——改为「写前重读文件、以文件为基底合并本次单条变更、唯一 tmp + rename」。缺口 2 根因是 runtime 配置 4 类写者全部裸 `os.WriteFile`（撕裂读/写），且子进程启动时无条件 `saveConfig` 回写会覆掉并发落盘的 propagate 补丁——统一改原子写helper，并把启动回写收窄为「文件存在且解析成功则跳过」。缺口 3 根因是 `reapOrphans` 仅在各进程 `Start()` 时执行一次——挂入既有 `watch()` 扫描周期。

**Tech Stack:** Go（同仓库单模块），`os.CreateTemp`+`os.Rename` 原子写，`go test`（内存/临时目录，不触网）。

---

## 前置阅读（必须）

| 优先级 | 文件 |
|--------|------|
| P0 | 本文件 |
| P0 | `docs/issue-log/2026-08-28.md` 问题 2（缺口全文与证据行号） |
| P1 | `AGENTS.md`、`CODE_REVIEW.md` |

**仓库路径：** `D:\AI_Projects\opencode2api_guide\opencode2api_enhance`
**基线分支：** 在 `feat/fix-startup-catalog-block` 当前 HEAD（95a9887，含阶段 1 修复）上继续，不新建分支。

---

## Global Constraints（冲突时以本节为准）

1. 单元测试不触网、不启动真实服务、不占真实端口；`*_test.go` 按仓库策略不入 git（本地保留）。
2. **明确不做（本阶段）**
   - ❌ 不把 toggle 管理路由从子进程摘除（对外 API 兼容性不动，靠读合并写保证安全）；
   - ❌ 不引入跨进程文件锁（rename 原子性已足够，锁是过度设计）；
   - ❌ 不改 `main.go` 启动顺序（阶段 1 成果不动）；不改七页 UI；不动 manager 15s 等待逻辑；
   - ❌ 不做 gateway sync 触发点扩充（admin_ops 实例启停补 sync 属行为变更，另立议题）。
3. **Git：** 小步 commit，中文 gitmoji；不 push。
4. **YAGNI：** 不加配置项/开关；最小 diff；注释风格对齐既有中文注释。
5. 每步 `go test -count=1 ./...` 全绿才 commit。

---

## 阶段开头：上阶段遗留

无未通过项（阶段 1 验收 ✅；真机端到端为用户手工项，不阻塞本阶段）。

---

## File Structure（预期变更）

| 文件 | 动作 | 职责 |
|------|------|------|
| `core/manager/pluginprovider/manager.go` | 修改 | Task 1：`updateStateFile` 读合并写 + 唯一 tmp + Warn 日志；`Delete` 同步清理状态文件条目；抽 `writeStateFile(state)` 辅助 |
| `core/manager/pluginprovider/manager.go` | 修改 | Task 3：`watch()` 扫描周期内周期性调用 `reapOrphans` |
| `core/manager/fsutil.go`（或并入既有合适文件） | 新建 | Task 2：`WriteFileAtomic(path, data, perm)` 导出辅助（CreateTemp 同目录 + rename + 失败清理） |
| `core/manager/instance.go`、`core/manager/gateway.go`、`core/manager/custom_propagate.go`、`core/manager/automodel.go` | 修改 | Task 2：4 处裸 `os.WriteFile` 改 `WriteFileAtomic` |
| `main.go`（或 `config.go`） | 修改 | Task 2：启动 `saveConfig` 收窄——配置文件存在且解析成功时跳过回写 |
| `custom_propagate.go`（根包）/ `core/manager/custom_propagate.go:3` | 修改 | Task 2：修正「1s 配置监视」注释漂移（实际 3s，config.go:225） |
| 对应 `*_test.go`（本地不入库） | 新建/修改 | 各 Task 回归测试 |

---

## Task 1：插件状态文件读合并写（缺口 1，高）

**Files:** `core/manager/pluginprovider/manager.go`

**行为:**

1. 抽辅助 `writeStateFile(state map[string]bool) error`：`os.CreateTemp(<state 目录>, <base>+".tmp-*")` → 写入 → close → `os.Rename` → 失败时清理 tmp 并 `slog.Warn`（替换现有 `slog.Debug`）。
2. `updateStateFile(id, enabled)`：更新内存 `m.state` 后，**写前重读** `loadPluginState(m.cfg.StateFile)` 作为基底，`base[id]=enabled` 合并后整体写盘——消除「陈旧快照覆写其它进程开关」。文件损坏/缺失时基底为空表，行为退化为现状（可接受）。
3. `Delete(id)`：同步 `delete(m.state, id)` 并以同一辅助重写状态文件（消除「删插件残留 enabled 条目、目录重建后自动启用」）。
4. 更新 `updateStateFile` 顶部注释说明「写前重读合并」的多进程契约。

**测试（`plugin_orphan_test.go` 同目录新建/扩展，参照既有 fake harness）:**

- 外部并发内容保留：两次 `updateStateFile` 之间手工改写状态文件（模拟其它进程写入），断言第三方条目在最终文件中保留；
- tmp 残留：连续 N 次 `updateStateFile` 后状态目录无 `.tmp-*` 残留；
- `Delete` 后文件中该 id 消失；
- 撕裂/非法 JSON 存在时 `updateStateFile` 仍产出合法 JSON。

**验证:** `go test -count=1 -race ./core/manager/pluginprovider/` → PASS；`go test -count=1 ./...` 全绿。
**Commit:** `🐛 fix(plugin): 插件状态文件写前重读合并+唯一tmp——多进程开关不再互相覆写丢失`

## Task 2：runtime 配置原子写 + 启动回写收窄（缺口 2，中高）

**Files:** `core/manager/fsutil.go`（新）、`instance.go`、`gateway.go`、`core/manager/custom_propagate.go`、`automodel.go`、`main.go`/`config.go`、根包 `custom_propagate.go`（注释）

**行为:**

1. 新增导出辅助 `manager.WriteFileAtomic(path string, data []byte, perm os.FileMode) error`：同目录 `os.CreateTemp` → 写 → close → rename；失败清理 tmp；目标目录不存在时返回错误（调用方既有 MkdirAll 语义不变）。
2. 替换 4 处裸写：`instance.go:134`（子进程 ocCfg）、`gateway.go:256`（网关配置）、`core/manager/custom_propagate.go:105`（propagate patch）、`automodel.go` 中 propagateAutoModel 的写盘点。**逐一核对行号可能有漂移，以实际代码为准**。
3. 启动回写收窄：`main.go:127-131` 的 `saveConfig` 仅在「配置文件不存在或解析失败」时执行（文件健康时跳过，杜绝子进程用启动瞬间解析的旧 providers 覆掉并发落盘的 propagate 补丁）。注意保留首次创建/损坏自愈路径；在 `loadConfig`/`saveConfig` 现有结构上最小化实现（如返回/记录解析是否成功的判据）。
4. 修正注释漂移：「1s 配置监视」→「3s」（根包 `custom_propagate.go:3`、`custom_providers.go:488` 附近，实际 `config.go:225` 为 3s ticker）。

**测试:**

- `WriteFileAtomic`：正常写入/覆盖旧内容、内容与权限正确、失败路径（目标目录不存在）不残留 tmp、并发 20 goroutine 写同一路径最终内容为合法完整 JSON 之一（无撕裂）。
- 启动回写收窄：若实现为可测辅助函数则直接单测（文件存在+合法→false；缺失→true；损坏→true）。

**验证:** `go build ./... && go vet ./... && go test -count=1 ./...` 全绿。
**Commit:** `🐛 fix(config): runtime配置统一原子写+子进程启动回写收窄——消除propagate补丁被覆写丢失`

## Task 3：孤儿插件周期性回收（缺口 3，中）

**Files:** `core/manager/pluginprovider/manager.go`

**行为:**

1. `watch()` 扫描循环中周期性调用 `m.reapOrphans()`（每轮 scan 后执行或按计数器每 ~5 轮一次，实现者权衡 `listProviderProcesses` 的枚举成本后定，并注释说明）。
2. 复核既有约束不被破坏：只处理本 providers 目录、`--provider-serve`、宿主存活豁免、当前持活 pid 豁免（manager.go:563-601 逻辑不动，仅增加调用时机）。

**测试:** 参照 `plugin_orphan_test.go` 既有 harness：伪造孤儿进程状态（或抽出可注入判定函数）验证 watch 周期内孤儿被回收；若既有测试已覆盖 reapOrphans 本体，则补「watch 循环会触发 reap」的间接断言或最小重构使其可测。

**验证:** `go test -count=1 -race ./core/manager/pluginprovider/` → PASS；`go test -count=1 ./...` 全绿。
**Commit:** `🐛 fix(plugin): 孤儿插件回收挂入周期扫描——强杀实例后的插件副本不再无限期存活`

## Task 4：文档闭环

更新 `docs/issue-log/2026-08-28.md` 问题 2 三项状态 → 已修复待验证（附真实测试输出）；本计划状态头 → 已完成（验收方可代执行）。

---

## 代码审查（阶段级强制环节，验收前）

**审查方：** 独立子代理（非实现者）

| 审查项 | 结论（✅/⚠️/❌） | 问题清单 |
|--------|------------------|----------|
| 正确性（读合并写竞态窗口/原子写平台语义） | 待审查 | |
| 测试完整性 | 待审查 | |
| 风格 | 待审查 | |
| 红线（不做清单/夹带） | 待审查 | |

**重点反证点（审查者必查）：**
1. 读合并写仍存在的窗口（读→合并→写之间他人写入仍会被本次 rename 覆盖）是否可接受、是否有更优解不引入锁；
2. `WriteFileAtomic` 在 Windows 上 rename 覆盖已存在文件的行为（Go `os.Rename` 在 Windows 对已存在目标的行为与 POSIX 差异！）；
3. 启动回写收窄是否破坏既有依赖：首次安装默认配置落盘、config 结构迁移、`config_merge_test.go` 既有语义、以及阶段 1 的 watcher 基线初始化时序；
4. Delete 清状态文件与 applyStateChanges 的交互（删除进行中其它进程 Toggle 同 id）；
5. 周期 reap 与插件重启窗口（killCurrent→spawn 之间）的误杀可能。

**结论：** 待审查

---

## 验收标准总表

| # | 标准 | 通过条件 | 验证责任人 |
|---|------|----------|----------|
| 1 | Task 1/2/3 定向测试 | `-race` PASS | 测试子代理 + 验收方复跑 |
| 2 | 全量单测/构建 | `go build ./... && go vet ./... && go test -count=1 ./...` exit 0 | 测试子代理 + 验收方复跑 |
| 2b | 代码审查 | ✅ 或 ⚠️（登记）；❌ 返工 | 审查子代理 |
| 3 | 红线 | 未触碰不做清单；无密钥 | 验收方 |
| 4 | 端到端（真机，遗留手工项） | 双进程同时开关不同插件互不丢失；强杀实例后孤儿插件在扫描周期内被回收 | 用户 |

---

## 风险与降级

| 风险 | 缓解 |
|------|------|
| Windows `os.Rename` 覆盖已存在文件语义差异 | Task 2 实现时先核实 Go 版本行为（go1.26 `os.Rename` 在 Windows 已支持覆盖目标，需实现者以文档/测试确认），不成立则改「先 rename 备份/删除目标再 rename」或 MoveFileEx 语义封装，审查点 2 专门核 |
| 启动回写收窄影响首次部署默认配置 | 仅跳过「文件存在且解析成功」情形，首次创建/损坏自愈保留；审查点 3 专查 |
| 读合并写窗口残留 | 单条变更毫秒级窗口 + rename 原子；对比替代方案（文件锁/单写者收权）属过度设计，登记为已知边界 |
| 周期 reap 枚举开销 | 按计数器降频（~15s），watch 循环已存在无新增协程 |

---

## 给接手 AI 的完整提示词

---

你是负责 **opencode2api_enhance** 的实现代理。请**完整执行本阶段**，不要只写方案。

### 基线
- 目录：`D:\AI_Projects\opencode2api_guide\opencode2api_enhance`（Windows / Git Bash）
- 分支：`feat/fix-startup-catalog-block` 当前 HEAD（含阶段 1 修复），直接工作
- 唯一实施计划：`docs/ai-framework/plans/2026-08-28-provider-instance-hardening.md`
- 缺口证据：`docs/issue-log/2026-08-28.md` 问题 2（含 文件:行号）
- 必读：本计划全文、`AGENTS.md`；动手前先重读目标文件确认行号（可能有漂移）

### 做
Task 1 → Task 2 → Task 3 → Task 4 严格顺序；每步测试后 commit（中文 gitmoji）。测试文件本地保留不入 git。

### 不做
- Global Constraints 第 2 条「明确不做」清单全部内容；不 push；不触网测试。

### 工作方式
先跑基线 `go test -count=1 ./...` 确认全绿；Task 2 动手前先确认 Windows `os.Rename` 覆盖语义（写一个小验证测试即可）。

### 交卷
分支提交列表、验收表自评、真实测试输出、diff 概览、残留风险。简体中文。

---

## 残留手工验收清单

1. 用户真机：主管理器与某实例子进程并存时，分别（连不同端口）快速交替开关两个插件 → 双方最终状态一致、无插件被误拉起；
2. 用户真机：启动实例后用任务管理器强杀实例子进程 → 其插件副本在 ≤1~2 个扫描周期内被回收（任务管理器观察 `--provider-serve` 进程消失）。
