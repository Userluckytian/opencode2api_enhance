# 阶段 Q：xhx 每日签到事件可审计修复

> **状态：** ✅ 已完成（2026-11-13，代码/测试/构建/部署/运行验证均已完成）
> **For agentic workers:** 按 Task 顺序执行；每 Task 测完再进下一 Task。
> **元规范:** `docs/ai-framework/phased-plan-driven.md`

**Goal:** 让 4 个账号的每日签到都能在面板「行为日志」里审计到，即使发放流水 diff 没确认到新增积分，也不再静默只写 DEBUG。

**Architecture:** `dailyCheckIn` 对每个账号每天恰好产出一条 `checkin` 事件：`TriggerDailyCheckIn` 成功、落 `checkinDate` 旗标后，若 `credit > 0` 写「每日签到：积分已发放」，否则写「每日签到：已触发，未从流水确认新增发放」并带 `bills_available` / `bills_ok` 排查字段。不再把「已触发」只留在 DEBUG 日志里；失败路径仍写 WARN 并进入 6 小时退避。

**Tech Stack:** Go、`go test` / `go vet` / `gofmt`

---

## Global Constraints

1. 不改变签到触发流程、不增加上游请求、不改上游接口。
2. 不改变幂等语义：同日旗标命中时仍零请求、零新增事件。
3. 不把「未从流水确认」误报成「签到失败」或「已发放」。
4. 不新增凭证；默认不 push。

---

## File Structure

| 文件 | 动作 | 职责 |
|------|------|------|
| `internal/pool/checkin.go` | 修改 | 触发成功即写事件；区分发放已确认/未确认 |
| `internal/pool/checkin_test.go` | 修改 | 新增 `credit==0` 与流水不可读两个用例；补断言 |

---

## Task 1：事件口径修正

**Files:** `internal/pool/checkin.go`

**行为:**
- `TriggerDailyCheckIn` 完成后，对每个账号每天恰好写一条 `checkin` 事件（幂等旗标保证）。
- `credit > 0`：原 INFO 与「每日签到：积分已发放」事件不变。
- `credit == 0`：把 DEBUG 升级为 INFO + `checkin` 事件，消息为「每日签到：已触发，未从流水确认新增发放」，`meta` 带 `bills_available`、`bills_ok`、`credit=0`。
- `bills` 快照/复核失败时不误报，但事件里明确「流水未确认」并带排查字段。
- 失败路径仍写 WARN 并进入 6 小时退避。

**Steps:**
1. 在 `dailyCheckIn` 里记录 `before != nil`、`billsOK`。
2. 把 `credit > 0` / `credit == 0` 都改成 INFO + `checkin` 事件，二选一，不重复写「已触发」。
3. 跑 `go test ./internal/pool -run DailyCare`。

---

## Task 2：回归测试

**Files:** `internal/pool/checkin_test.go`

**Steps:**
1. 增加 `TestDailyCare签到触发但流水未报数仍写事件`：上游 bills 两次都返回空 items，断言有 `checkin` 事件且包含「未从流水确认」。
2. 增加 `TestDailyCare流水接口失败仍写签到事件`：bills 返回 500，断言仍产生 `checkin` 事件。
3. 原 `TestDailyCare行为日志` 增加断言：`credit>0` 时含「积分已发放」；同日第二遍无新增事件。

---

## 验收标准总表

| # | 标准 | 通过条件 |
|---|------|----------|
| 1 | 触发成功必可审计 | 任一账号 `TriggerDailyCheckIn` 成功即产生 `checkin` 事件 |
| 2 | 无 diff 不静默 | `credit==0` 时产生含「未从流水确认」的事件，不再只写 DEBUG |
| 3 | 已发放语义不变 | `credit>0` 仍产生「每日签到：积分已发放」 |
| 4 | 幂等不变 | 同日第二次 `MaybeDailyCare` 零请求、零新增事件 |
| 5 | 构建 | `gofmt -l .` 无输出、`go vet ./...`、`go test ./...`、`go build` 全绿 |

### 验收结果（2026-11-13）

| # | 结果 | 证据 |
|---|------|------|
| 1 | ✅ | `TestDailyCare签到已触发但流水未报数仍写事件`：两次流水为空 / 流水接口失败两种场景下，都恰好产生 1 条 `checkin` 事件，并落 `checkinDate=今天` |
| 2 | ✅ | 新增用例断言事件消息含「未从流水确认新增发放」，`meta.bills_ok` 分别为 true / false；旧 DEBUG 分支已删除 |
| 3 | ✅ | `TestDailyCare行为日志` 增加断言：仍含「每日签到：积分已发放」 |
| 4 | ✅ | `TestDailyCare行为日志` 原有「同日第二遍零新增事件」断言通过；`TestMaybeDailyCare同日只跑一次并落旗标` 通过 |
| 5 | ✅ | `gofmt -l .` 无输出；`go vet ./...` exit 0；`go test ./...` 全绿；`go build -trimpath -ldflags "-s -w -X xhx-provider.commit=2e3e182 -X xhx-provider.buildTime=2026-11-13T05:50:37Z" -o xhx-provider.exe ./cmd/xhx-provider` 成功 |

**部署记录：**
- 修复同时把版本从 `1.8.0` 递增到 `1.8.1`（`version.go` / 源码 `provider.json`；生产 `provider.json` 只改 version 一行，逐字保留私有配置、UTF-8 无 BOM）。
- 备份：`xhx-provider.exe.bak-20261113-134904`、`provider.json.bak-20261113-134904`。
- 新 exe sha256：`75e344a84f4858e5ad9ea7c4b62ef15534918581fef6eb1116c1660e9c8d22e3`。
- 宿主 toggle 停/启后 xhx running（pid 1936，`http://127.0.0.1:54542`）；启动日志 `version=1.8.1 build="commit 2e3e182 · 2026-11-13T05:50:37Z"`；4 个账号正常载入。
- 面板 `/panel/api/status` 返回 `version=1.8.1`、`build_time=2026-11-13T05:50:37Z`、`account_count=4`；`/panel/api/events` 返回 20 条。
- 回滚：停插件 → 还原上述两个备份文件 → 启插件。

**运行时行为说明：**
今天 01:00 窗口已经跑过，4 个账号 `checkin_date=2026-11-13`，按幂等旗标不会补写今天的事件；新口径从**下一个维护窗口**（今晚 23:00 ±5 分钟或明晨 01:00 ±5 分钟）开始生效，届时每个触发成功的账号都会有一条 `checkin` 事件。

---

## 给接手 AI 的完整提示词

你是负责 **xhx 插件** 的实现代理。按本计划 Task 1~2 顺序改代码，不要只写方案。

### 基线
- 目录：`D:\AI_Projects\plugins\xhx`
- 计划：`docs/ai-framework/plans/2026-11-13-xhx-checkin-event-audit.md`（enhance 仓库）

### 做
1. 改 `internal/pool/checkin.go`：触发成功即写 `checkin` 事件；`credit==0` 升级为 INFO + 事件。
2. 补 `internal/pool/checkin_test.go` 两个回归用例。
3. 跑 `gofmt -l .`、`go vet ./...`、`go test ./...`、`go build`。
4. 部署前备份现 exe，部署后验证宿主 running。

### 不做
- 不改签到三步流程、不增加上游请求、不改面板视图、不提交密钥、不 push。
