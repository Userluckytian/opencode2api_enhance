# 阶段 O：LobsterHub 40302 旧状态清理与单号检查

> **状态：** ✅ 已完成（2026-11-12，代码/测试/构建/部署/运行验证均已完成）
> **For agentic workers:** 按 Task 顺序执行；每 Task 测完再进下一 Task。
> **交接提示词**见文末「给接手 AI 的完整提示词」。
> **元规范:** `docs/ai-framework/phased-plan-driven.md`

**Goal:** 修掉「签到失败: slot code=40302 …」在恢复时间过后仍常驻面板的问题，并给面板增加单号手动检查能力。
**Architecture:** 后端把签到失败统一走账号错误分类（40302 → 暂停，40100/40101 → 停用，其它 → 普通失败）；`ClearSuspension` 同时清理旧版签到路径写入的 40302 文案；新增只读单号检查 `RefreshAccount` 与 `POST /api/accounts/{uid}/refresh`。前端每行增加「检查 / 签到」按钮，过期暂停显示「已到恢复时间，待检查」。
**Tech Stack:** Go（LobsterHub 插件）、原生 HTML/JS 面板、`go test` / `go vet` / `gofmt`
**实施档位：** 全能
**子代理：** 不启用（改动集中、可本地验证）

---

## 前置阅读（必须）

| 优先级 | 文件 |
|--------|------|
| P0 | `docs/ai-framework/phased-plan-driven.md` |
| P0 | 本文件 |
| P0 | `D:\AI_Projects\plugins\lobsterhub\scheduler.go`、`store.go`、`service.go`、`panel.go`、`assets/panel.html` |
| P1 | `AGENTS.md`、`CODE_REVIEW.md` |

**仓库路径：** `D:\AI_Projects\plugins\lobsterhub`
**基线分支：** `main`（插件仓库本地）

---

## Global Constraints（冲突时以本节为准）

1. 只做 A（状态语义修正）+ B（手动检查）；**不做 C 自动化**：不加启动清理、不加面板轮询自动清理、不加定时 probe。
2. 「检查」只允许走只读接口 `GET /api/user/profile-summary`（`FetchQuota`），**绝不发对话/补全请求**；单号串行，不并发打上游。
3. 不改变「签到异常即停」的既有安全语义。
4. 密钥/凭证不进 git；默认不 push。
5. **明确不做（本阶段）**
   - ❌ 不自动对所有暂停号发 probe
   - ❌ 不改签到三步流程
   - ❌ 不改网关轮换策略
6. **Git：** 小步 commit；默认不 push，除非人类明确要求。

---

## 阶段开头：上阶段遗留

无。

---

## 跳过项

| 跳过项 | 原因 | 待补做 |
|--------|------|--------|
| 独立代码审查 | 本轮改动集中、按用户要求快速修复 | ⬜ 待用户确认补做 |

---

## 与前后阶段

| 阶段 | 状态 | 交付 |
|------|------|------|
| 上阶段 | ✅ | 插件源码单仓库与账号快照归档 |
| **本阶段** | ✅ | 40302 状态清理 + 单号检查（已部署验证） |
| 下阶段 | | 无 |

---

## File Structure（预期变更）

| 文件 | 动作 | 职责 |
|------|------|------|
| `store.go` | 修改 | `ClearSuspension` 清 40302 旧文案 |
| `suspension.go` | 修改 | 新增 `isSuspensionStatus` |
| `service.go` | 修改 | 抽出 `recordAccountError`；`CheckinNow` 失败落盘；新增 `RefreshAccount` |
| `scheduler.go` | 修改 | 签到失败走统一分类；成功补 `ClearSuspension` |
| `panel.go` | 修改 | `apiAccountOp` 增加 `refresh` |
| `assets/panel.html` | 修改 | 行内「检查 / 签到」；过期暂停展示 |
| `*_test.go` | 修改/新增 | 40302 分类、旧文案清理、成功清暂停 |

---

## 配置或 API 契约

`POST /api/accounts/{uid}/refresh`

```json
{ "message": "已触发检查" }
```

无请求体；后台执行单号只读积分查询，完成后通过 `/api/status` 轮询反映。

---

## Task 1：统一账号错误分类

**Files:** `service.go`、`scheduler.go`

**行为:** 抽出 `recordAccountError(acc, msg, stage)`；`RunCheckinAll` 失败分支调用它，40302 记暂停、40100/40101 停用、其它写普通失败；保留「异常即停 + 剩余号标记」。

**Steps:**

1. 在 `service.go` 新增 `recordAccountError`，`Service.applyAccountError` 改为调用它并打印日志。
2. `scheduler.go` 失败分支删除 `a.StatusMsg = "签到失败: " + out.Error`，改为 `recordAccountError`；仅对账号级错误打印分类日志。
3. 跑：`go test ./...`
   期望：exit 0

---

## Task 2：清理 40302 旧文案

**Files:** `suspension.go`、`store.go`、`scheduler.go`、`service.go`

**行为:** `ClearSuspension` 除 `上游暂停` 前缀外，也清含 `40302` 的状态；`RunCheckinAll` 成功与 `CheckinNow` 成功都调用 `ClearSuspension`；`CheckinNow` 失败分类后落盘并推送状态。

**Steps:**

1. `suspension.go` 新增 `isSuspensionStatus(msg)`。
2. `store.go` `ClearSuspension` 用 `isSuspensionStatus` 判断，去掉「无暂停字段就提前返回」对旧文案的遮挡。
3. `scheduler.go` 成功分支补 `a.ClearSuspension()`。
4. `service.go` `CheckinNow` 失败分支补 `vault.Save()` + `ui.Emit("state")`。
5. 跑：`go test ./...`
   期望：exit 0

---

## Task 3：单号检查接口

**Files:** `service.go`、`panel.go`

**行为:** 新增 `Service.RefreshAccount(uid)`：后台串行、只读 `FetchQuota`，按现有分类更新状态；`apiAccountOp` 增加 `refresh`。

**Steps:**

1. `service.go` 新增 `RefreshAccount(uid int64) string`，复用 `refreshQuotaFor`、`refreshMu`、`Save`、`Emit`。
2. `panel.go` 的 `apiAccountOp` 增加 `case "refresh"`，账号不存在返回 404。
3. 跑：`go test ./...`
   期望：exit 0

---

## Task 4：面板按钮与过期展示

**Files:** `assets/panel.html`

**行为:** 每行增加「检查」；`lastCheckin != today` 时增加「签到」；暂停过期显示「已到恢复时间，待检查」；抽屉只在暂停中显示暂停原因。

**Steps:**

1. 增加 `todayStr()`、`checkAccount(uid)`、`checkinAccount(uid)`。
2. `accountRow` 插入按钮；`badge`、`note`、`accDrawer` 处理过期暂停。
3. 更新 `load()` 的账号签名，纳入 `suspendedUntil`、`lastCheckin`。
4. 手工检查：打开面板，确认按钮渲染与点击无 JS 报错。

---

## Task 5：测试与构建

**Files:** `suspension_test.go`、`checkin_stop_test.go`

**Steps:**

1. 新增/补充测试：
   - `ClearSuspension` 清 `签到失败: ...40302...`；
   - `ClearSuspension` 保留无关失败文案；
   - `recordAccountError` 对 40302 记暂停；
   - `RunCheckinAll` 失败后失败账号 `SuspendedUntil > 0`；
   - `RunCheckinAll` 成功后清掉旧暂停。
2. 跑：
   - `gofmt -l .` 期望无输出
   - `go vet ./...` 期望 exit 0
   - `go test ./...` 期望 exit 0
   - `go build -trimpath -ldflags "-s -w" -o lobsterhub.exe .` 期望 exit 0

---

## 验收标准总表

| # | 标准 | 通过条件 | 验证责任人 |
|---|------|----------|------------|
| 1 | 40302 签到失败记为暂停 | `RunCheckinAll` 后失败账号 `SuspendedUntil > 0` 且 `StatusMsg` 含「上游暂停」 | 自动化（执行方） |
| 2 | 旧文案可清 | `ClearSuspension` 后 `签到失败: ...40302...` 为空 | 自动化（执行方） |
| 3 | 成功清暂停 | `RunCheckinAll` 成功后 `SuspendedUntil=0`、`SuspendedReason=""` | 自动化（执行方） |
| 4 | 单号检查接口 | `POST /api/accounts/{uid}/refresh` 返回 200；面板状态随后更新 | 端到端（部署后用户） |
| 5 | 检查不发对话请求 | 代码路径只调用 `FetchQuota` → `GET /api/user/profile-summary` | 自动化（执行方 grep） |
| 6 | 构建 | `go test ./...`、`go vet ./...`、`gofmt -l .`、`go build` 全绿 | 自动化（执行方） |
| 7 | 密钥 | 本阶段不新增任何凭证文件 | 自动化（执行方） |

### 验收结果（2026-11-12）

| # | 结果 | 证据 |
|---|------|------|
| 1 | ✅ | `TestRunCheckinAllStopsOnError` 断言失败账号 `SuspendedUntil > 0`、`StatusMsg` 含「上游暂停」；`TestRecordAccountErrorSuspendsOn40302` 通过 |
| 2 | ✅ | `TestClearSuspensionClearsLegacyCheckin40302` 通过；`TestClearSuspensionKeepsUnrelatedCheckinFailure` 证明不误清 |
| 3 | ✅ | `TestRunCheckinAllCompletesWhenAllOK` 预置旧暂停后签到成功，断言 `SuspendedUntil=0`、`SuspendedReason=""`、无「上游暂停」文案 |
| 4 | ✅ | 部署后实测 `POST /api/accounts/150003/refresh` 返回 `{"message":"已触发检查"}`；3 秒后 `suspended:false`、`suspendedUntil:null`、`statusMsg:""`、积分 532.43；账号 99666 显示「今天已签到过（回读确认 +100 已到账）」 |
| 5 | ✅ | `RefreshAccount` 仅调用 `refreshQuotaFor` → `FetchQuota` → `GET /api/user/profile-summary`；运行日志无对话/补全请求 |
| 6 | ✅ | `gofmt -l .` 无输出；`go vet ./...` exit 0；`go test ./...` 全绿；`go build -trimpath -ldflags "-s -w" -o lobsterhub.exe .` 成功 |
| 7 | ✅ | 未新增凭证文件；`git status` 仅源码/文档变更 |

**部署记录：**
- 备份：`lobsterhub.exe.bak-20261112-131752`、`provider.json.bak-20261112-131752`
- 新 exe sha256：`8a277e7c930977f880bf93f195891725fa857acfc449b7ea2ed1dadea4e4ccac`
- 宿主 toggle 停/启后插件 running（pid 1732，`http://127.0.0.1:56990`）
- 日志：`13:18:36 ⏸ 账号 150003 签到被上游暂停，8 小时 24 分钟后自动恢复`；`13:19:06 ✅ 账号 99666 检查：可用，积分 313.85`；`13:19:15 ✅ 账号 150003 检查：可用，积分 532.43`
- 回滚：停插件 → 还原上述两个备份文件 → 启插件

---

## 风险与降级

| 风险 | 缓解 |
|------|------|
| 单号检查阻塞面板请求 | 后台 goroutine + 前端轮询；接口立即返回「已触发检查」 |
| 检查与全员刷新并发打上游 | 复用 `refreshMu`，全局串行 |
| 40302 无恢复时刻 | 沿用 `suspendedFallbackCooldown` 30 分钟 |
| 旧版数据没有 `SuspendedUntil` | `ClearSuspension` 通过 `isSuspensionStatus` 识别文案并清理 |
| 部署覆盖 exe 失败 | 部署前备份旧 exe，失败即回滚 |

---

## 给接手 AI 的完整提示词

你是负责 **LobsterHub 插件** 的实现代理。请完整执行本阶段，不要只写方案。

### 基线
- 目录：`D:\AI_Projects\plugins\lobsterhub`
- 唯一实施计划：`docs/ai-framework/plans/2026-11-12-lobsterhub-refresh-check.md`（在 enhance 仓库）
- 必读：`AGENTS.md`、本计划

### 做
1. 按 Task 1~5 顺序改代码。
2. 「检查」只走只读 `FetchQuota`，不发对话请求。
3. 不做启动/轮询/定时自动清理（C 不做）。
4. 每 Task 后跑 `go test ./...`，最后跑 `go vet ./...`、`gofmt -l .`、`go build`。

### 不做
- 不自动 probe 所有暂停号
- 不改签到三步流程
- 不提交密钥；未授权的 `git push`

### 交卷
给出：改动文件、测试/构建结果、验收表自评、部署与回滚步骤。

---

## 残留手工验收清单

1. 部署后在面板点击 45 号「检查」，确认旧 `签到失败: ...40302...` 消失。
2. 若账号仍暂停，确认显示新的恢复时刻。
3. 点击「签到」确认单号签到仍可用。
