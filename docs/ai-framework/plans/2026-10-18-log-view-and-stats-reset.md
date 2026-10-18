# 阶段 L1：调用日志取消 5000 展示上限 + 统计重置与按天视图一致

> **状态：** 已完成（本地自动化验收通过；未部署，UI 全量加载与“重置→切日期”待复验）
> **元规范：** `docs/ai-framework/phased-plan-driven.md`

**Goal:** 日志页不再固定只显示最近 5000 条；重置统计后，按天视图与累计统计从同一时间点起算，不再出现“Token 清零但按天仍有数据”。

**Architecture:**
- 日志读取：`ReadCallLog(0)` 表示全部；前端不再用 `call_log_max` 作为展示上限；`call_log_max` 仅保留为内存环形缓冲/回放上限。
- 统计重置：在 `runtime/stats_reset_at` 落一个 RFC3339 时间点；`StatsByDay` 与前端日期/趋势聚合都过滤早于该时间点的调用日志；日志文件本身不删。

**实施档位：** 全能（默认）；子代理：不启用（改动集中在 4 个文件，验收方直接实施）

---

## Global Constraints

1. 不删除调用日志文件；重置仍只复位 `stats.json` / `node_stats.json`。
2. `ReadCallLog(0)` = 全部；`>0` 仍按尾窗口读取；负数按 0 处理。
3. 付费/网关行为不变；不碰 Phase 1 的坏池逻辑。
4. 不改变 64MB 轮转策略（历史硬上限仍在，文档写明）。
5. 不 push。

---

## File Structure

| 文件 | 动作 | 职责 |
|------|------|------|
| `core/manager/calllog.go` | 修改 | `ReadCallLog(0)=全部`；去掉 5000 常量；filtered/aggregate 读全量 |
| `core/manager/admin_http.go` | 修改 | `/call-log` 默认 `limit=0`（全部） |
| `core/manager/stats.go` | 修改 | 重置时间点读写；`StatsSummary.reset_at` |
| `core/manager/stats_day.go` | 修改 | `StatsByDay` 过滤早于重置时间点的记录 |
| `core/manager/calllog_filter.go` | 修改 | 过滤/聚合不再被 5000 截断 |
| `src/pages/LogsPage.tsx` | 修改 | 移除 `min(call_log_max,5000)`，直接拉全部 |
| `src/pages/StatsPage.tsx` | 修改 | 拉全部 + 按 `reset_at` 过滤日期/趋势 |
| `src/lib/api.ts` | 修改 | `StatsSummary.reset_at`、`ResetStatsResult.reset_at` |
| `src/pages/PoolPage.tsx` | 修改 | `call_log_max` 文案改为“内存/回放保留上限” |
| `docs/DEPLOYMENT.md`、`CHANGELOG.md` | 修改 | 配置语义与行为变更 |
| `docs/issue-log/2026-10-18.md`、`OPEN.md` | 修改 | 状态与验证证据 |

---

## Task 1：日志读取取消 5000 上限

1. `ReadCallLog(max)`：`max<=0` 全部；删除 50000 硬上限；按文件读取时把 `max` 透传，`0` 表示不截尾。
2. `readCallLogFileTail` / `readCallLogFilePart` / `parseCallLogRecords`：`tail<=0` 不做截断；大文件 `tail<=0` 整读。
3. `CallLogHandler`：默认 `limit=0`（全部），显式 `limit=N>0` 仍生效。
4. `ReadCallLogFiltered` / `AggregateCallLog`：改用 `ReadCallLog(0)`，过滤/聚合不再只看最近 5000。
5. 测试：临时日志 3 条，`ReadCallLog(0)` 返回 3、`ReadCallLog(1)` 返回 1。

## Task 2：前端日志页/统计页拉全部

1. `LogsPage` 删除 `fetchLimitRef` 与 configGet 逻辑，`api.getCallLog()` 不带 limit。
2. `StatsPage.loadDates` 改为 `api.getCallLog()`。
3. `PoolPage` 的 `call_log_max` 标签/说明改为“内存日志/回放保留上限（不影响日志页展示）”。

## Task 3：统计重置时间点

1. `StatsResetAtPath` = `<runtime>/stats_reset_at`；`statsResetAt()` / `writeStatsResetAt()`。
2. `ResetStats` 成功后写当前时间；`ResetStatsResult.ResetAt` 返回 RFC3339。
3. `StatsSummary.ResetAt` 返回当前时间点；`StatsByDay` 过滤 `ts < reset_at`。
4. 前端 `StatsPage`：`resetAtRef` 保存时间点；`loadDates` 过滤早于它的记录；`load` 检测变化后重拉日期。
5. 测试：写入重置时间点后，`StatsByDay` 只统计之后的记录。

## Task 4：文档与验收

1. `DEPLOYMENT.md` 更新 `call_log_max` 语义；`CHANGELOG.md` 记录两项修复。
2. issue log / OPEN 更新为“已修复待验证”，附命令结果。
3. 验收：`go build ./...`、`go test -count=1 .`、受影响包、前端 `tsc` 构建；全量测试的既有 flaky 按 OPEN.md 取证。

---

## 验收结果（2026-10-18，本地）

**结论：代码完成，本地自动化通过；未部署。**

分支：`fix/log-view-and-stats-reset`（已合并 Phase 1；未 push）

| 提交 | 说明 |
|------|------|
| `8954291` | 取消 5000 条展示上限，日志页读取全部历史 |
| `106cf40` | 重置统计后按天视图从重置时刻起算 |
| `bf21dfd` | 补日志全量展示与统计重置说明 |
| `37ff6e0` | 合并 Phase 1 节点池自愈 |

| 验收项 | 结果 | 证据 |
|--------|------|------|
| 日志读取 0=全部 | ✅ | `TestReadCallLogZeroMeansAllAndPositiveTails`（3 条全读；limit=2 取尾 2；-1 视为全部） |
| 后端编译 | ✅ | `go build ./...` |
| 静态检查 | ✅ | `go vet ./...` exit 0 |
| 根包测试 | ✅ | `go test -count=1 .` ok（46.5s） |
| manager 包测试 | ✅ | `go test -count=1 ./core/manager` ok（62.0s） |
| Q2/Q3 定向测试 | ✅ | `TestReadCallLogZero*`、`TestStatsByDayHonorsResetEpoch`、`TestCallLog*`、`TestStats*` |
| 前端类型检查 | ✅ | `node ./node_modules/typescript/bin/tsc -b` exit 0 |
| 前端构建 | ✅ | `node ./node_modules/vite/bin/vite.js build` built |
| 全量测试 | ⚠️ | 唯一失败为 pluginprovider `TestPluginNeedConfigThenReady`（全量并发负载下 5s 超时）；单独 `-count=5` 通过，main 基线也受同类负载敏感影响，非本阶段改动引入 |
| 部署/真机 | ⬜ | 未部署；“日志页全量加载”和“重置→切日期”需部署后人工复验 |

**风险：**
- 日志页每 5s 拉取全部历史；当前约 8.6k 条/5MB 可接受，接近 64MB 轮转上限时请求体明显变大，后续可做服务端分页/增量拉取。
- 64MB 轮转与 `.1` 单代备份未改，历史硬上限仍在。

