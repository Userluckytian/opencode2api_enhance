# 阶段 S：消除插件启停状态文件的跨进程竞态（实测复现：开关被陈旧快照写回旧值）

> **状态：** 计划已就绪
> **For agentic workers:** 按 Task 顺序执行；每 Task 测完再进下一 Task。
> **交接提示词**见文末。
> **元规范:** `docs/ai-framework/phased-plan-driven.md`

**Goal:** 让「跟随其它进程开关」这条路径不再用陈旧快照覆盖磁盘上的新值，并让状态写盘可追溯到具体进程。
**Architecture:** 宿主插件管理器（`core/manager/pluginprovider/manager.go`）的启停状态经 `<providers>/.plugin-state.json` 跨进程共享。`updateStateFile` 已是「写前重读 + 原子 rename」，但**决策值来自快照**且 `Toggle` 在落盘前会先 `killCurrent`（taskkill 可能数秒），窗口内用户新改的值会被旧快照盖掉。修法：跟随路径改为「预检 + 写时 CAS」，用户 toggle 路径保持不变（用户意图无条件生效）。
**Tech Stack:** Go 1.x / 现有 `Manager` 与测试 harness
**实施档位：** 全能
**子代理：** 不启用（本环境 `spawn_subagent` 无可用模型；审查改为实现者自查并登记）

---

## 前置阅读（必须）

| 优先级 | 文件 |
|--------|------|
| P0 | `docs/ai-framework/phased-plan-driven.md`、本文件 |
| P0 | `core/manager/pluginprovider/manager.go`（`Toggle` / `updateStateFile` / `applyStateChanges`） |
| P1 | `docs/PLUGIN-PROVIDERS.md`（§4 生命周期）、`docs/issue-log/2026-09-21.md` 第 4 条 |

**仓库路径：** `D:\AI_Projects\opencode2api_guide\opencode2api_enhance`（分支 `main`）
**基线分支：** 直接在 `main` 上小步提交（本次改动单文件 + 本地测试）

---

## Global Constraints（冲突时以本节为准）

1. **不改契约**：`provider.json` 字段、`--provider-serve` 协议、stdout 纪律一律不动。
2. **测试本地保留**：本仓库 `.gitignore` 排除 `*_test.go`（全仓跟踪 0 个），测试写完只留本地、不 `git add -f`。
3. **不 push**、不打 tag（由用户决定发版时机）。
4. **明确不做（本阶段）**
   - ❌ 不改「用户 toggle 路径」的语义（用户意图必须无条件生效，不做 CAS）。
   - ❌ 不引入跨进程文件锁 / 不改成「每插件一个状态文件」（先做本阶段最小修复，若再复现再升级——登记为下阶段候选）。
   - ❌ 不动 `Delete` 的清理路径（同样是重读合并，语义正确）。
5. **YAGNI：** 不顺手重构 `Toggle` 的其它分支、不动退避/监督循环。
6. **Git：** 提交格式 `<gitmoji><type>(<scope>): 中文描述`；默认不 push。

---

## 阶段开头：上阶段遗留

**无**（本阶段独立；vibex 插件阶段（`docs/ai-framework/plans/2026-09-21-vibex-plugin-provider.md`）的遗留项是浏览器人工验收，与本阶段无关）。

---

## 跳过项

| 跳过项 | 原因 | 待补做 |
|--------|------|--------|
| 独立代码审查 | 本环境子代理不可用（3 次均报无可用模型） | ⬜ 待有可用子代理时补做 |
| 真机多实例复现验证 | 需要重启用户的实例池并观察，属破坏性操作；本阶段用确定性单测覆盖同一代码路径 | ⬜ 待用户同意后做一次真机复核 |

---

## 与前后阶段

| 阶段 | 状态 | 交付 |
|------|------|------|
| 上阶段（vibex 插件） | ✅ | 插件 1.2.1 已部署，契约冒烟 7/0/0 |
| **本阶段** | ⬜ | 状态文件竞态修复 + 可观测日志 |
| 下阶段（候选） | | 若再复现 → 跨进程文件锁或「每插件一个状态文件」；实例停止改优雅退出 |

---

## File Structure（预期变更）

| 文件 | 动作 | 职责 |
|------|------|------|
| `core/manager/pluginprovider/manager.go` | 修改 | `toggle(id, enabled, expect *bool)` 内部化；新增 `updateStateFileCAS`；`applyStateChanges` 改走「预检 + CAS」；写盘加 pid 日志 |
| `core/manager/pluginprovider/plugin_state_test.go` | 修改（本地保留） | CAS 语义 + 跟随不覆盖并发新值 |
| `docs/PLUGIN-PROVIDERS.md` | 修改 | 状态文件契约补「跟随路径用 CAS」一句 |
| `CHANGELOG.md` | 修改 | 修复条目 |

---

## Task 1：跟随路径改为「写时 CAS」+ 写盘日志

**Files:** `core/manager/pluginprovider/manager.go`

**行为:** 新增 `updateStateFileCAS(id, enabled, expect)`：写前重读文件，若该 id 当前值 ≠ `expect`（或条目不存在）→ **放弃本次落盘**并 `slog.Info` 记录；否则与 `updateStateFile` 同款合并写盘。`Toggle` 内部抽出 `toggle(id, enabled, expect *bool)`：`expect == nil` 走原无条件写（用户意图），非 nil 走 CAS（跟随）。所有成功写盘补 `slog.Info("plugin state written", "id", "enabled", "pid", ...)`。

**Steps:**

1. 实现上述三处改动（保持 `Toggle` 对外签名不变，`Delete` 不动）。
2. 跑：`go build ./... && go vet ./core/manager/pluginprovider/`
   期望：exit 0。
3. Commit：`🐛 fix(pluginprovider): 跟随开关改用写时 CAS，防陈旧快照覆盖新值`

---

## Task 2：`applyStateChanges` 走预检 + CAS

**Files:** `core/manager/pluginprovider/manager.go`

**行为:** `applyStateChanges` 对 diff 里每个 id：先**预检**（重读该 id 当前值是否仍等于快照值；不等则跳过，避免无谓的 kill/spawn 抖动），再调 `toggle(id, want, &want)` 走 CAS 落盘。CAS 失败时内存与磁盘会短暂不一致，**下一个扫描周期（≤3s）自动收敛到磁盘新值**。

**Steps:**

1. 改写 `applyStateChanges`；日志用 `slog.Debug`/`Info` 标明「跳过」原因（并发变更）。
2. 跑：`go test ./core/manager/pluginprovider/ -run 'StateFile' -count=1`
   期望：既有 `TestStateFileFollow` / `TestStateFilePreDisabled` 仍通过（跟随语义未变）。
3. Commit：`🐛 fix(pluginprovider): 状态跟随加预检，避免用陈旧快照覆盖并发新值`

---

## Task 3：本地测试（不入库）

**Files:** `core/manager/pluginprovider/plugin_state_test.go`

**行为:** 覆盖两条：
- `TestUpdateStateFileCASSkipsStaleWrite`：文件 `{a:true, b:false}`，`CAS(a,false,expect=false)` → 文件不变（仍 a=true）且其他条目保留；`CAS(a,false,expect=true)` → a 变 false。
- `TestStateFollowDoesNotClobberNewerValue`：harness 装插件、`Start` 后把内存置为与文件相反，再用 `toggle(id, want, &staleExpect)`（expect 与文件不符）→ 断言**文件未被改写**（新值保留）。

**Steps:**

1. 写测试 → 先跑一次确认**改前会失败**（红）：把 CAS 临时退化成无条件写，观察断言失败。
2. 恢复实现 → 跑 `go test ./core/manager/pluginprovider/ -count=1`
   期望：全绿。
3. 不 commit 测试文件（仓库约定）。

---

## Task 4：文档与日志

**Files:** `docs/PLUGIN-PROVIDERS.md`、`CHANGELOG.md`、`docs/issue-log/2026-09-21.md`、`docs/issue-log/OPEN.md`

**Steps:**

1. `PLUGIN-PROVIDERS.md` 状态文件契约处补一句：**跟随路径必须用 CAS 落盘**（陈旧快照不得覆盖新值），并说明「用户 toggle 路径无条件生效」。
2. `CHANGELOG.md` 加修复条目。
3. 问题日志第 4 条状态改「已关闭」+ 附验证证据；`OPEN.md` 对应行移出/更新。
4. Commit：`📝 docs(pluginprovider): 补状态文件 CAS 契约与修复记录`

---

## 代码审查（阶段级环节）

**审查方：** 实现者自查（**非独立**——本环境子代理不可用，已登记跳过项）

| 审查项 | 结论 | 证据 |
|--------|------|------|
| 风格 | | |
| 测试完整性 | | |
| 依赖与架构红线 | | |
| 安全 | | |
| API 契约 | | |

**结论：**

---

## 验收标准总表

| # | 标准 | 通过条件 | 验证责任人 |
|---|------|----------|----------|
| 1 | 构建/静态检查 | `go build ./...` 与 `go vet ./core/manager/pluginprovider/` exit 0 | 自动化（执行方） |
| 2 | CAS 语义 | CAS 值不符时不落盘、条目保留；值符时落盘 | 自动化（执行方） |
| 3 | 跟随不覆盖新值 | 并发新值保留（确定性单测） | 自动化（执行方） |
| 4 | 既有跟随语义未破 | `TestStateFileFollow`、`TestStateFilePreDisabled` 通过 | 自动化（执行方） |
| 5 | 包测试 | `go test ./core/manager/pluginprovider/ -count=1` 全绿 | 自动化（执行方） |
| 6 | 可观测 | 写盘日志含 `id/enabled/pid`，跳过日志含 `expect/current` | 人工核对代码 |
| 7 | 红线 | 未改 `provider.json` 契约、未改 stdout 纪律、未提交测试文件、未 push | 自动化（执行方） |
| 8 | 代码审查 | 自查结论（独立性不足已登记） | 实现者自查 |

---

## 风险与降级

| 风险 | 缓解 |
|------|------|
| CAS 失败后内存与磁盘短暂不一致 | 下一扫描周期（≤3s）自动收敛；日志留痕 |
| 用户 toggle 与用户 toggle 并发仍可能丢条目（跨进程毫秒窗口） | 本阶段不动；登记为下阶段候选（每插件一文件/文件锁） |
| 预检引入额外读盘 | 仅跟随路径、仅 diff 命中时读一次；每 3s 一轮，量级可忽略 |
| 修改 `Toggle` 内部结构影响其它调用点 | `Toggle` 对外签名不变；`grep` 全部调用点核对 |

---

## 给接手 AI 的完整提示词

---

你是负责 **opencode2api_enhance** 的实现代理。请完整执行本阶段。

### 基线
- 目录：`D:\AI_Projects\opencode2api_guide\opencode2api_enhance`（分支 `main`）
- 唯一实施计划：`docs/ai-framework/plans/2026-09-21-plugin-state-cas.md`
- 必读：`docs/ai-framework/phased-plan-driven.md`、`AGENTS.md`、`docs/issue-log/2026-09-21.md` 第 4 条

### 做
1. Task 1~4 按顺序执行，每 Task 验证后 commit（测试文件不提交）。
2. 跟随路径「预检 + 写时 CAS」，用户 toggle 路径不变。
3. 写盘/跳过都打带 pid 的日志，便于下次定位。

### 不做
- ❌ 改 `provider.json` 契约 / stdout 纪律；❌ 引入跨进程锁或改每插件一文件（下阶段候选）
- ❌ 提交 `*_test.go`；❌ push / 打 tag

### 工作方式
1. 先跑基线：`go build ./...` + `go test ./core/manager/pluginprovider/ -count=1` 确认干净。
2. 证据优先：完成前重跑计划中的验证命令并贴输出。
3. 简体中文回复；标识符保持原样。

### 交卷
提交列表、验收表自评、验证命令与输出、残留风险、下阶段候选。

---

## 残留手工验收清单

1. 真机多实例复核（需用户同意后重启实例池观察状态不再被写回）。
