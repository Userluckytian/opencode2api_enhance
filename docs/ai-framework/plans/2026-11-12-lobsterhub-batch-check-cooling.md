# 阶段 P：LobsterHub 批量检查、暂停冷却池与概览闪烁

> **状态：** ✅ 已完成（2026-11-12，代码/测试/构建/部署/运行验证均已完成）
> **For agentic workers:** 按 Task 顺序执行；每 Task 测完再进下一 Task。
> **元规范:** `docs/ai-framework/phased-plan-driven.md`

**Goal:** 增加批量检查；让被 40302 暂停的账号进入冷却池且不阻塞其他账号签到，到点自动回池；签到仍只在 01:00 / 23:00 两档执行；修掉总览「账号概览」卡片每 5 秒重渲染导致的闪烁。

**Architecture:** 后端把签到循环的错误处理拆成「账号级（暂停/会话失效）→ 跳过继续」与「其它异常 → 立即停止」；新增只读批量检查 `CheckAllAccounts` 与 `POST /api/check-all`；移除启动补签，只保留档位调度。前端用数据签名控制账号概览与日志/排程的重渲染，避免 5 秒轮询触发 `rowIn` 动画。

**Tech Stack:** Go、原生 HTML/JS 面板、`go test` / `go vet` / `gofmt`

---

## Global Constraints

1. 批量检查只走只读 `GET /api/user/profile-summary`（`FetchQuota`），**不发对话/补全/签到请求**。
2. 40302 暂停 → 记录 `SuspendedUntil/SuspendedReason`，本轮跳过该号并继续下一个；到点后 `Suspended()` 自动为 false，重新参与轮换与签到。
3. 40100/40101 会话失效 → 停用该号并继续；其它网络/未知异常仍保留「异常即停 + 剩余号标记」的既有安全语义。
4. 自动签到只在 01:00 / 23:00 档位（含 3 分钟宽限）执行；删除启动补签，其他时间不自动签到。手动按钮不受限。
5. 不新增凭证；默认不 push。

---

## File Structure

| 文件 | 动作 | 职责 |
|------|------|------|
| `scheduler.go` | 修改 | 暂停/失效账号跳过继续；移除启动补签 |
| `service.go` | 修改 | 新增 `CheckAllAccounts` 批量只读检查 |
| `panel.go` | 修改 | 新增 `POST /api/check-all` |
| `assets/panel.html` | 修改 | 批量检查按钮；概览/日志/排程签名重渲染 |
| `checkin_stop_test.go` | 修改 | 40302 继续、普通异常停止、冷却跳过/回池 |
| `panel_test.go` / `service_test.go` | 视需要补充 | 批量检查端点/行为 |

---

## Task 1：签到冷却池语义

**Files:** `scheduler.go`

**行为:**
- 循环开始前跳过 `!Enabled`、`!TokenValid`、`Suspended()` 的账号，并打印原因。
- 遇到 40302：`recordAccountError` 记暂停 → `Save` → 继续下一个账号，不标记剩余账号为「因异常未签到」。
- 遇到会话失效：停用 → `Save` → 继续。
- 遇到其它错误：保持「异常即停 + 剩余号标记」。
- 到点冷却结束的账号正常参与签到；成功后 `ClearSuspension`。

---

## Task 2：移除启动补签

**Files:** `scheduler.go`、`schedule_slots.go` 注释

**行为:** 删除 `Scheduler.Start` 里 3 秒后「今天未签到则补签」的 goroutine；只保留 `newSlotRunner("checkin", ...)` 的 01:00 / 23:00 档位。

---

## Task 3：批量检查

**Files:** `service.go`、`panel.go`

**行为:**
- `CheckAllAccounts()`：后台串行、只读 `FetchQuota`，复用 `refreshMu`；跳过仍处于冷却中的账号；每个号之间 300~700ms 抖动；结束后打印汇总。
- `POST /api/check-all` 立即返回「已触发批量检查」。

---

## Task 4：前端批量检查与闪烁修复

**Files:** `assets/panel.html`

**行为:**
- 快捷操作与账号池工具栏增加「批量检查」按钮，调用 `act('/api/check-all')`。
- 账号概览 `accounts-mini` 只在数据签名变化时重渲染（复用 `lastAccSig`）。
- 日志与排程也按签名更新，避免 5 秒轮询反复重写 DOM。
- 冷却结束徽章/备注改为「冷却结束 / 已自动回池」，仍提示可点「检查」确认。

---

## 验收标准总表

| # | 标准 | 通过条件 |
|---|------|----------|
| 1 | 暂停不阻塞签到 | 40302 账号后，后续健康账号仍被签到；暂停号 `SuspendedUntil > 0` |
| 2 | 冷却池跳过 | 冷却中的账号签到请求数为 0；到期后重新被请求 |
| 3 | 普通异常仍即停 | 非 40302/40100 错误时，后续账号不被请求，并标记「因异常未签到」 |
| 4 | 签到档位 | 无启动补签；只有 01:00/23:00 档触发自动签到 |
| 5 | 批量检查 | `POST /api/check-all` 返回 200；串行只读；汇总日志含可用/暂停/失效/失败数 |
| 6 | 概览不闪烁 | 5 秒轮询数据不变时 `accounts-mini` 不重渲染；数据变化时才更新 |
| 7 | 构建 | `gofmt -l .` 无输出、`go vet ./...`、`go test ./...`、`go build` 全绿 |

### 验收结果（2026-11-12）

| # | 结果 | 证据 |
|---|------|------|
| 1 | ✅ | `TestRunCheckinAllSuspensionDoesNotStopOthers`：40302 账号记入 `Suspended()` 后，健康账号 `tok-good` 仍完成签到 |
| 2 | ✅ | `TestRunCheckinAllSkipsCoolingAndReturnsAfterExpiry`：冷却中无任何请求；`SuspendedUntil` 改为过期后重新被签到并写入 `LastCheckin` |
| 3 | ✅ | `TestRunCheckinAllStopsOnGenericError`：普通失败后日志含「本轮已停止」，后续账号未再请求；坏号记录「签到失败」 |
| 4 | ✅ | `scheduler.go` 已删除启动补签 goroutine；启动日志实测 `定时档位: 每天 01:00 / 23:00（宽限 3 分钟，来源 settings）` |
| 5 | ✅ | `TestCheckAllAccountsReadOnlyAndSkipsCooling`、`TestPanelCheckAllEndpoint` 通过；生产 `POST /api/check-all` 返回 `{"message":"已触发批量检查"}`；11 秒后日志 `🔎 批量检查完成：可用 8，冷却中 0，会话失效 0，其它失败 0，跳过 0` |
| 6 | ✅ | `panel.html` 新增 `renderAccountViews`、`lastLogSig`、`lastSchedSig`；5 秒轮询保留但仅签名变化时重画；部署后页面包含新代码 |
| 7 | ✅ | `gofmt -l .` 无输出；`go vet ./...` exit 0；`go test ./...` 全绿；`go build -trimpath -ldflags "-s -w" -o lobsterhub.exe .` 成功 |

**部署记录：**
- 备份：`lobsterhub.exe.bak-20261112-141851`、`provider.json.bak-20261112-141851`
- 新 exe sha256：`bda25267dfb84c8e6b3f5a7a08d5fb9b76347cffc46feebf8f65e4a4f393a40e`
- 宿主 toggle 停/启后插件 running（pid 40644，`http://127.0.0.1:50088`）
- 页面验证：包含「批量检查」按钮、`renderAccountViews`、`lastLogSig`、`冷却结束 · 已回池`
- 回滚：停插件 → 还原上述两个备份文件 → 启插件

---

## 给接手 AI 的完整提示词

你是负责 **LobsterHub 插件** 的实现代理。按本计划 Task 1~4 顺序改代码，不要只写方案。

### 基线
- 目录：`D:\AI_Projects\plugins\lobsterhub`
- 计划：`docs/ai-framework/plans/2026-11-12-lobsterhub-batch-check-cooling.md`（enhance 仓库）

### 做
1. 改 `scheduler.go`：暂停/失效跳过继续，普通异常仍即停；删除启动补签。
2. 加 `CheckAllAccounts` 与 `/api/check-all`，只读、串行、带抖动。
3. 改 `panel.html`：批量检查按钮；概览/日志/排程签名更新。
4. 补测试，跑 `gofmt -l .`、`go vet ./...`、`go test ./...`、`go build`。

### 不做
- 不发对话/补全请求；不自动 probe 所有暂停号；不改网关轮换策略；不提交密钥；不 push。
