# 阶段 L：供应商维度统一 —— Kilo 调用持久化 + 日志/统计/诊断/设置四页补齐

> **状态：** 计划已就绪，待用户批准后实施
> **For agentic workers:** 按 Task 顺序执行；每 Task 测完再进下一 Task。验收认证据，禁止「应该能过」。
> **元规范:** `docs/ai-framework/phased-plan-driven.md`；仓库约定 `AGENTS.md`
> **基线分支：** 当前 `feat/phase-k-kilo-gateway`（tip `392a11f`），**不新开分支**（同主题连续阶段，小步 commit）

**Goal:** 把 Kilo 调用持久化并入统一调用日志，并在【日志/统计/诊断/设置】四页补齐「供应商」维度与四服务端口总览，让 Kilo 不再是数据孤岛。

**Architecture:**
- **持久化**：Kilo 网关在 `core/manager` 包内写自己的 JSONL（`<dataDir>/runtime/kilo/kilo_calls.jsonl`），用 `Manager.Paths().RuntimeDir` 定位；`ReadCallLog()` 把它作为第三个来源与「供应商服务 JSONL + 引擎 stdout」合并。**不反向 import main 包**（`EventLog.Append` 在 main 包，反向依赖会造成循环），也不改生产 `call_log.jsonl` 的写入路径。
- **供应商维度**：后端 `CallLogFilter` 加 `Vendor` 字段（服务端过滤）；前端抽一个共享 `vendorOf()` 工具（`src/lib/vendor.ts`），把每条记录归到 `kilo / opencode / 自定义` 三类；日志页与统计页共用。
- **四页**：日志页合并来源后删除 KiloPage 的「最近调用」卡片；统计页按天趋势/日志部分加供应商筛选；诊断页加 30080 自检；设置页补 Kilo 端口与四端口总览。

**Tech Stack:** Go 1.22 标准库（`encoding/json` / `os` / `sync` / `sort`）；React 19 + TS + Tailwind（现有面板同款）。

**实施档位：** 全能
**子代理：** 启用（独立代码审查 + 独立验收；模型沿用会话白名单）

---

## 四页面内容分析：额外缺口（独立子代理复核补充）

> 独立子代理逐行复核四页（合计 3939 行）后的补充发现，均纳入本阶段 Task 3/4/5 顺手修，或如实登记为遗留。

| # | 页面 | 确凿缺口 | 证据 | 本阶段处理 |
|---|------|---------|------|-----------|
| E1 | 日志 | **三个分析视图（时段/节点/错误）传全量 `logs`，日期/关键词/只看失败筛选对它们完全不生效** | `LogsPage.tsx:607-611` 传 `logs`，只有 list 视图 `:561` 用 `visible` | ✅ Task 3 改为传 `visible` |
| E2 | 日志 | `/api/admin/call-log/filtered` 端点闲置，全量历史一次拉到前端 | `api.ts:1342` 定义但页面未调用；`LogsPage.tsx:332/:344` | ⚠️ 本阶段保持前端筛选（避免重构 1565 行页面），Task 2 已让后端具备能力；登记为后续性能优化 |
| E3 | 日志 | `source` 字段实测 8/8 缺失，`LogRow` 的「来源」标签基本不显示；`vendor` 可用但未展示 | 实测 `call_log.jsonl`；`LogsPage.tsx:239-243/:276` | ✅ Task 3 改显示 `vendor` 分类标签 |
| E4 | 日志 | 文案「保留上限可在设置页调整」与设置页无 `call_log_max` 控件矛盾 | `LogsPage.tsx:601` vs `SettingsPage` 无该控件 | ✅ Task 5 补 `call_log_max` 展示/编辑（后端 `config.go:344-360` 已支持） |
| E5 | 统计 | **`DayStats.by_node` 前端有 / 后端无** → 「当日节点用量」表恒空 | `api.ts:950` vs `stats_day.go:13-22` 全仓 grep 无 `ByNode` | ✅ Task 4 前端类型删掉该字段或标注「后端未返回」，不伪装有数据 |
| E6 | 统计 | `GATEWAY_DISPLAY='统一网关'` 与后端来源名 `'供应商服务'` 不一致，MiniBar 成功/失败列可能恒空 | `StatsPage.tsx:10/:204` vs `stats.go:51`（推断，需实测确认） | ✅ Task 4 改用 `vendor` 作为 key 并对齐命名；实测确认 |
| E7 | 统计 | **Kilo 既不在 call_log 也不在 stats.json → 统计页无论如何统计不到 Kilo** | 子代理独立复核 | ✅ **Task 1 落盘后自动解决**：`loadDates()` 遍历 `getCallLog()`，Kilo 记录并入即被统计（这是选「写入统一日志」方案的额外收益） |
| E8 | 诊断 | `main.go:5-8` 端口图与 `:630` 注释仍写「三端口」，未提 30080 | 子代理复核 | ✅ Task 5 同步注释 |
| E9 | 设置 | `ConfigView` 大量字段（`ui_poll_interval_sec`/`base_url`/`engine_*` 等）未展示 | `api.ts:291-344` vs `SettingsPage` 仅用 engine_proxies | ⚠️ 本阶段只补端口与 `call_log_max`（诉求范围内），其余登记遗留 |
| E10 | 诊断 | 自检表 `key={c.name}`，新增 Kilo 项 name 必须唯一 | `DiagPage.tsx:271` | ✅ Task 5 用 `name="kilo"`（当前 5 项无重名） |

**未核实项（如实登记，不臆断）**：Kilo `KiloCallRecord.time` 与日志 `ts` 的日期筛选格式兼容性 → Task 1 落盘时统一写 ISO8601 `ts` 即消除；`GATEWAY_DISPLAY` 是否已在运行时导致 MiniBar 恒空 → Task 4 浏览器实测确认；`trace_id` 前端是否有消费方 → 不影响本阶段。

---

## 前置阅读（必须）

| 优先级 | 文件 |
|--------|------|
| P0 | `docs/ai-framework/phased-plan-driven.md`、本文件 |
| P0 | `core/manager/calllog.go`（`ReadCallLog` 合并结构 :125-178）、`core/manager/calllog_filter.go`（`CallLogFilter` :14-22、`matches` :25-60） |
| P0 | `core/manager/kilo_gateway.go`（`KiloCallRecord` :62、`recordCall` :763、`handleChat` :589-694） |
| P0 | `src/pages/LogsPage.tsx`（筛选 :322-424、`vendorOf` :957）、`src/pages/StatsPage.tsx`（`loadDates` :181-235） |
| P1 | `AGENTS.md`、`coding-standards.md`、`docs/issue-log/OPEN.md`、`docs/ai-framework/plans/2026-11-11-kilo-independent-gateway.md`（阶段 K 遗留） |

---

## Global Constraints（冲突时以本节为准）

1. **独立性红线延续阶段 K**：30080 网关仍**不得**引用 `globalAgg` / `chatRouterVar` / `vendors/custom` / `ProviderService` / 50080 / 40080 / **main 包**（`EventLog.Append` 在 main 包，禁止反向依赖）。持久化用 `core/manager` 内部实现。
2. **Kilo 日志格式必须与 `CallLogRecord` 兼容**：写出的 JSONL 行要能被 `readCallLogFileTail` 直接解析，字段用 `req_id/ts/path/model/stream/status/duration_ms/vendor/serving_port/upstream_status/error_message/tier/route_verdict`；`vendor="kilo"`、`source` 由合并器填「Kilo 独立网关」。
3. **不改生产 `call_log.jsonl` 的写入侧**：现有供应商服务/引擎链路零改动；Kilo 是**新增第三个来源**，避免回归。
4. **密钥不进 git**；Kilo 日志**不得记录请求体 / 上游响应体 / 密钥**（与现有 call_log 一致，只记元数据）；`Bearer anonymous` 是公开匿名档可入代码。
5. **不做 auto 竞速路由**（2-4 未决，另立阶段）；**不做 2-4 相关配置**。
6. **YAGNI**：不做 Kilo 日志轮转之外的独立清理 API；不做跨进程日志锁（单进程写、单进程读，用文件 append + `O_APPEND`）；不做供应商筛选的服务端分页（现有 `CallLogFilter.Offset/Limit` 够用）。
7. **Git**：小步 commit，规范 `<gitmoji><type>(<scope>): 中文描述`；**默认不 push**。
8. **不重建/不替换生产 exe**；验证走 `npm run tauri:dev`（dev 槽位 44100/44180/44280/30082，与生产隔离）。前端改动需把 `dist/` 同步到 `src-tauri/target/debug/bin/dist/`（阶段 K 踩过的坑）。

---

## 阶段开头：上阶段遗留

| 上阶段遗留项 | 来源 | 未通过原因 | 状态 |
|-------------|------|-----------|------|
| V-6b：`custom.go:332` 硬编码 `Free:true` + `auto_model.go:266` auto 候选通用缺陷 | 阶段 K 独立审查 B4 / 验收表 V-6b | 按 Global Constraints 8「本阶段保持不共享即可」显式转下阶段 | ⬜ **本阶段不修**（与本阶段无依赖，留 2-4 阶段一并处理；不伪装全绿） |
| B5：部署 + 生产停机 V-7 | 阶段 K 独立审查 B5 | 需人类授权停机窗口 | ⬜ **本阶段不修**（非代码缺陷） |
| `callmetric_engine_test.go` 4 条时间炸弹 | OPEN.md 11-10 #5 | 既有失败，与本阶段无关 | ⬜ **本阶段不修**（验收表按「无新增失败」判定） |

> 说明：以上三项均非本阶段引入、也不阻塞本阶段功能；按框架规则**如实登记**，不冒充全绿。本阶段验收表会包含「整包无新增失败」。

---

## 与前后阶段

| 阶段 | 状态 | 交付 |
|------|------|------|
| 阶段 K（Kilo 独立网关） | ✅ 代码/验收收口，**待部署** | 30080 网关 + 独立菜单 + V-1~V-10 证据 |
| **本阶段 L** | ⬜ | Kilo 日志持久化 + 四页供应商维度与端口总览 |
| 下阶段 M（auto 竞速路由，2-4） | ⬜ | 待用户定 A/B 方案与候选范围 —— **勿塞进本阶段** |
| P0 托管层修复 / 部署窗口 | ⬜ | 与阶段 K B5 合并另立 |

---

## File Structure（预期变更）

| 文件 | 动作 | 职责 |
|------|------|------|
| `core/manager/kilo_calls_log.go` | 新建 | Kilo 调用日志：路径、append（带轮转与上限）、把 `KiloCallRecord` 映射成 `CallLogRecord` |
| `core/manager/kilo_gateway.go` | 修改 | `recordCall` 同步落盘（异步 goroutine + 有界队列，不阻塞响应路径） |
| `core/manager/calllog.go` | 修改 | `ReadCallLog` 的 `files` 增加 Kilo 来源；导出 `KiloCallsLogPath()` |
| `core/manager/calllog_filter.go` | 修改 | `CallLogFilter` 加 `Vendor` 字段 + `matches` 分类判定（kilo / opencode / 自定义） |
| `diag_api.go` | 修改 | `runDiag` 增加 Kilo 30080 `/healthz` 自检项 |
| `src/lib/api.ts` | 修改 | `ConfigView` 补 `kilo_*` 字段；`CallLogFilter` 类型补 `vendor`；新增 `KiloCallRecord` 类型（若仍需） |
| `src/lib/vendor.ts` | 新建 | 共享 `VendorKind` / `vendorOf(rec)` / `VENDOR_OPTIONS`（三类归并 + 标签） |
| `src/pages/LogsPage.tsx` | 修改 | 供应商下拉筛选（与日期/关键词/只看失败联动）；删除「来源」显示歧义；复用 `vendorOf` |
| `src/pages/StatsPage.tsx` | 修改 | 供应商下拉；按天趋势/日志部分按 `vendorOf` 过滤；累计来源表注明口径 |
| `src/pages/DiagPage.tsx` | 修改 | 展示 Kilo 自检项；四端口健康总览 |
| `src/pages/SettingsPage.tsx` | 修改 | 服务端口表补第 4 行 Kilo 30080；增加「配置值 / 生效值」双列 |
| `src/pages/KiloPage.tsx` | 修改 | 删除「最近调用·实际模型」卡片（已并入日志页），保留一句引导文案 |
| `core/manager/kilo_calls_log_test.go` | 新建 | 落盘/映射/轮转/上限/分类单测 |
| `core/manager/calllog_filter_test.go` | 修改（若存在） | `Vendor` 过滤三种分类断言 |
| `docs/issue-log/2026-11-12.md`、`OPEN.md` | 修改 | 阶段 L 记录与开放项同步 |
| 本计划 | 修改 | 验收表 / 审查结论 / 状态回填 |

---

## 配置或 API 契约

```jsonc
// Kilo 落盘行（kilo_calls.jsonl），与 CallLogRecord 兼容
{
  "req_id": "kilo-1794441103-1",
  "ts": "2026-11-12T08:52:26+08:00",
  "path": "/v1/chat/completions",
  "model": "nvidia/nemotron-3.5-lightning:free",   // 请求名
  "stream": false,
  "status": "ok",                                   // ok | error
  "duration_ms": 4683,
  "vendor": "kilo",
  "serving_port": "30082",
  "upstream_status": 200,
  "tier": "free",
  "route_verdict": "direct_by_design",
  // 实际返回的 model 记在 err_msg 之外的独立字段（新增，见下）
  "actual_model": "nvidia/nemotron-3.5-lightning:free"
}
```

> `actual_model` 是 `CallLogRecord` 的**新增可选字段**（`json:"actual_model,omitempty"`），用于承载「上游真实模型」—— 这是阶段 K 需求 #1 的核心信息，日志页要显示它。旧记录无此字段不影响解析。

```jsonc
// POST /api/admin/call-log/filtered 请求体新增
{ "vendor": "kilo" | "opencode" | "custom" | "", "from_ts": "...", ... }
// "" / 缺省 = 不过滤；"custom" = 非 kilo 且非 opencode 的一切
```

```jsonc
// GET /api/admin/config 已有但未下发到前端类型的字段（本阶段补类型）
{ "kilo_enabled": true, "kilo_free_only": true, "kilo_proxy": "",
  "kilo_port": 0, "kilo_port_effective": 30080 }
```

```jsonc
// GET /api/admin/diag 新增一项
{ "name": "kilo", "target": "127.0.0.1:30080", "ok": true,
  "detail": "status=ready models_shown=12 may_train=true", "duration_ms": 3 }
```

---

## Task 1：Kilo 调用日志持久化（后端，独立性红线内）

**Files:** `core/manager/kilo_calls_log.go`（新建）、`core/manager/kilo_gateway.go`、`core/manager/calllog.go`

**行为:**
- `KiloCallsLogPath()` = `<Paths().RuntimeDir>/kilo/kilo_calls.jsonl`；目录自动 `MkdirAll`。
- `appendKiloCall(rec KiloCallRecord, extra {...})`：单行 JSON append（`O_APPEND|O_CREATE|O_WRONLY`，0644），超过 8MB 轮转为 `.1`（与 `readCallLogFileTail` 的 `.1` 读取兼容）；单文件最大条数由 `call_log_max` 配置约束（软上限，超出轮转）。
- 映射：`KiloCallRecord` → `CallLogRecord`，字段见契约；`actual_model` 新增字段加到 `core/manager/calllog.go` 的结构体。
- `recordCall` 落盘**异步**：有界 channel（容量 256）+ 单写 goroutine；队列满时丢弃最新一条并 `slog.Warn`（**绝不阻塞响应路径**，与「失败不拖垮管理面」同原则）。进程退出前提供 `FlushKiloCalls()` 供 `Stop()` 调用。
- `ReadCallLog` 的 `files` 增加 `{m.KiloCallsLogPath(), "Kilo 独立网关"}`，`parts` 数组相应扩为 `len(files)+2`（原有引擎 stdout 分支）。
- **不 import main 包**；K8 独立性守卫需同步把 `main` 加入 banned 列表。

**Steps:**
1. 先写 `kilo_calls_log_test.go` 失败用例（映射/轮转/目录创建/不阻塞），再实现到 PASS。
2. 跑：`go test ./core/manager/ -run 'Kilo|CallLog' -count=1 -race -v` → 全 PASS；`go build ./...` = 0；`go vet ./core/manager/` = 0；`gofmt -l core/manager/kilo_*.go` 无输出。
3. 手工验证：起隔离网关发 1 次请求 → `<runtime>/kilo/kilo_calls.jsonl` 出现 1 行且字段齐全 → `GET /api/admin/call-log?limit=5` 能看到 `vendor=kilo` 条目。
4. Commit：`✨ feat(kilo): 调用日志落盘并并入统一日志`

---

## Task 2：供应商维度后端过滤

**Files:** `core/manager/calllog_filter.go`、`src/lib/api.ts`

**行为:**
- `CallLogFilter` 加 `Vendor string \`json:"vendor,omitempty"\``；`matches` 里按 `vendorOf(r)` 判定三类：`kilo`（`r.Vendor=="kilo"`）/ `opencode`（`r.Vendor=="opencode"` 或 `r.Source` 含「引擎」或 `route_verdict` 属于池路径且无自定义前缀）/ `custom`（其余）。
- 分类函数放 `core/manager/calllog_vendor.go`，**后端单一真源**；前端 `src/lib/vendor.ts` 用同款规则（两处保持口径一致，加注释互相指向）。
- 前端 `CallLogFilter` 类型补 `vendor?: string`。

**Steps:**
1. 单测：`kilo`/`opencode`/`custom`/`""`（不过滤）四条断言 + 旧记录（无 vendor 字段）兜底。
2. 跑：`go test ./core/manager/ -run 'CallLog|Vendor' -count=1 -race` → PASS。
3. Commit：`✨ feat(log): 调用日志新增供应商维度过滤`

---

## Task 3：日志页合并与供应商下拉

**Files:** `src/pages/LogsPage.tsx`、`src/pages/KiloPage.tsx`、`src/lib/vendor.ts`

**行为:**
- 供应商下拉 `VENDOR_OPTIONS`（全部 / kilo / opencode / 自定义），与现有日期、关键词、只看失败**联动**（`visible` 的 useMemo 加一个条件）。
- **E1**：把 `LogsPage.tsx:607-611` 三个分析视图的入参由 `logs` 改为 `visible`，让筛选真正联动分析视图。
- **E3**：`LogRow` 把基本不显示的 `source` 标签换成 `vendor` 分类标签（共享 `vendorOf`）。
- 下拉值经 `api.callLogFiltered({ vendor, ... })` 走服务端；但现有页面用 `api.getCallLog()` 全量 + 前端筛选 —— **保持现有前端筛选为主**（避免重构 1565 行页面），`vendor` 下拉同样在前端 `visible` 里过滤，服务端 `Vendor` 字段留给后续大数据量场景（Task 2 已具备能力）。在代码注释写明这个取舍（E2 登记遗留）。
- 日志行展示：Kilo 条目显示「实际模型」（`actual_model`）与「供应商」徽标；复用 `vendorOf` 统一标签。
- 删除 `KiloPage.tsx` 的「最近调用·实际模型」卡片，替换为一句「调用记录已并入【日志】页，可按供应商筛选」+ 跳转提示。

**Steps:**
1. `npx tsc -b` = 0；`npm run build` = 0。
2. 内置浏览器（dev）验证：供应商下拉选 kilo → 只剩 Kilo 记录且显示实际模型；选 opencode/自定义/全部各自正确；与日期筛选叠加正确。
3. Commit：`✨ feat(log): 日志页供应商筛选与 Kilo 记录合并`

---

## Task 4：统计页供应商筛选

**Files:** `src/pages/StatsPage.tsx`、`src/lib/vendor.ts`

**行为:**
- 新增供应商下拉（与日志页同款选项），作用于**按天趋势 + 调用日志派生的图表**（`loadDates` 里的 `logs` 过滤）。
- 累计 `sources` 表**保持原样**，但在表头/注释明确「累计来源表按统计文件口径，不随供应商筛选变化」—— 不伪装成已按供应商拆分（后端统计文件暂无 vendor 维度，属 Task 2 之外的独立缺口，如实登记）。
- **E7（关键收益）**：Task 1 让 Kilo 记录并入 `call_log` 后，`loadDates()`（`StatsPage.tsx:181-235`）遍历 `getCallLog()` 会自动把 Kilo 计入趋势/总数 —— 统计页无需额外端点即可覆盖 Kilo，这是选「写统一日志」而非「独立合并端点」的主要理由。
- **E5**：前端 `DayStats` 类型删掉后端不返回的 `by_node`（或标注「后端未返回」），「当日节点用量」表相应隐藏/标注，不显示恒空表。
- **E6**：`GATEWAY_DISPLAY` 与后端 `'供应商服务'` 命名对齐（改用 `vendor` 作 key），浏览器实测 MiniBar 成功/失败列是否正常。
- 失败原因分布视图若已有 scope（池/全部），与供应商下拉并列、互不覆盖；`FailStatsCard`（`:69-146`）按 `b.vendor` 过滤行。

**Steps:**
1. `npx tsc -b` = 0；`npm run build` = 0。
2. 浏览器验证：切供应商后趋势图/按天数字变化；累计表不变且有口径说明。
3. Commit：`✨ feat(stats): 统计页新增供应商筛选`

---

## Task 5：诊断页 + 设置页

**Files:** `diag_api.go`、`src/pages/DiagPage.tsx`、`src/pages/SettingsPage.tsx`、`src/lib/api.ts`

**行为:**
- **E4**：补 `call_log_max` 展示/编辑（后端 `config.go:344-360` 已支持），消除 `LogsPage.tsx:601` 矛盾文案；`ui_poll_interval_sec` 一并展示（E9 范围内）。
- **E8**：同步 `main.go:5-8` 端口图与 `:630` 注释（补 30080，不再写「三端口」）。
- `runDiag` 增加第 6 项：`probeHealthz(ctx, view.KiloPortEffective, "/healthz")` 变体（Kilo 的 healthz 无鉴权、只回 4 字段），`Name="kilo"`，`OK = HTTP 200 且 status=="ready"`；`kilo_enabled=false` 时标记 `OK=true` + `detail="已停用（按配置）"`，不算失败。
- `DiagPage` 展示该项；新增「四端口健康总览」小表（管理面板 / 引擎 / 供应商 / Kilo + 端口 + 状态），数据来自 diag checks + config。
- `ConfigView` 类型补 `kilo_enabled/kilo_free_only/kilo_proxy/kilo_port/kilo_port_effective`。
- `SettingsPage` 服务端口表从 3 行扩为 4 行（加 Kilo 30080），并增加「配置值 / 生效值」双列；Kilo 行显示启用状态。

**Steps:**
1. `go build ./...` = 0；`go vet ./...` = 0；`npx tsc -b` = 0；`npm run build` = 0。
2. 浏览器验证：诊断页 Kilo 项显示 ready；设置页四端口齐全（dev 环境应显示 44100/44180/44280/30082）。
3. Commit：`✨ feat(diag): 补 Kilo 自检与服务端口总览`

---

## Task 6：文档与日志（AGENTS.md 强制第 6 条）

- `docs/issue-log/2026-11-12.md` 追加阶段 L 条目（描述/分析/修改结果/验证证据/遗留）。
- `docs/issue-log/OPEN.md`：Kilo 行状态更新；新增「累计统计缺 vendor 维度」待办（如实）。
- 本计划：验收表 / 审查结论 / 状态回填。
- 若 `docs/ai-framework/plans/2026-11-11-kilo-independent-gateway.md` 的 V-5/遗留里提到「最近调用在 Kilo 页」，加一句「已并入日志页（阶段 L）」。

---

## 代码审查（阶段级环节，验收前）

**审查方：** 独立角色子代理（非实施者）

**审查面：** 独立性红线（Task 1 是否零 main 包依赖）/ 日志不记录请求体与密钥 / 异步落盘不阻塞响应、队列满降级不丢关键错误 / `ReadCallLog` 并发读与排序正确性 / 供应商分类三处口径一致（后端 filter / 前端 vendor.ts / 日志页 vendorOf）/ 前端筛选与分页/汇总联动 / `gofmt` 与 `coding-standards.md` / 测试是否真断言

| 审查项 | 结论（✅/⚠️/❌） | 问题清单 |
|--------|------------------|----------|
| 独立性红线 | | |
| 日志安全（无请求体/密钥） | | |
| 并发与降级 | | |
| 供应商口径一致性 | | |
| 测试有效性 | | |
| 风格与规范 | | |

**结论：** ⬜

---

## 验收标准总表

| # | 标准 | 通过条件（命令 + 期望） | 责任人 |
|---|------|------------------------|-----------|
| L-1 | 构建/静态检查 | `go build ./...`=0；`go vet ./...`=0；`gofmt -l core/manager/kilo_*.go core/manager/calllog*.go diag_api.go` 无输出；`npx tsc -b`=0；`npm run build`=0 | 自动化 |
| L-2 | 单测 | `go test ./core/manager/ -run 'Kilo\|CallLog\|Vendor' -count=1 -race` 全 PASS；整包 `go test ./core/manager/ ./vendors/custom/` **无新增失败**（既有 4 条时间炸弹豁免） | 自动化 |
| L-3 | 持久化 | 隔离实例发 1 次 Kilo 请求 → `kilo_calls.jsonl` 有 1 行且字段齐全；**重启实例后** `/api/admin/call-log` 仍能看到该 `vendor=kilo` 记录（这是「要持久化」的判据） | 端到端 |
| L-4 | 日志页 | 供应商下拉四类各自正确；与日期/关键词/只看失败叠加正确；Kilo 行显示 `actual_model`；KiloPage 不再有「最近调用」卡片 | 浏览器 |
| L-5 | 统计页 | 供应商下拉影响趋势图/按天数字；累计来源表保持不变且有口径说明 | 浏览器 |
| L-6 | 诊断页 | Kilo 30080 自检项存在且 ready；四端口总览齐全（dev 显示 44100/44180/44280/30082） | 浏览器 |
| L-7 | 设置页 | 服务端口表 4 行齐全 + 配置值/生效值双列 | 浏览器 |
| L-8 | 红线 | 无密钥/请求体入库；生产 exe/dist 未被替换；隔离端口清场；生产 40000/40080/50080 全程在听 | 自动化 + 主会话 |
| L-9 | 文档 | issue-log + OPEN.md + 计划回填 + 阶段 K 交叉引用 | 主会话 |
| L-10 | 独立审查 | 6 面结论 + 阻断项清单；不通过项返工后复验 | 子代理 |

---

## 风险与降级

| 风险 | 缓解 |
|------|------|
| `ReadCallLog` 增加第三个来源破坏既有排序/分页 | 复用现成 `readCallLogFileTail`（含 `.1` 轮转与 8MB 尾窗），单测覆盖三来源合并排序；`parts` 扩容后引擎 stdout 分支下标同步改 |
| 异步落盘队列满丢记录 | 容量 256 + 单写 goroutine；满时 `slog.Warn` 并丢弃**最新**一条（保留旧记录，避免日志断层被误读）；`Stop()` 时 `Flush` |
| 供应商分类口径三处漂移 | 后端 `calllog_vendor.go` 为单一真源，前端 `vendor.ts` 同款规则并互相注释指向；单测锁死三类 |
| 日志文件被两个进程同时写 | 仅 manager 进程写 Kilo 日志（provider-service 模式不启动 Kilo 网关，见阶段 K 约束）；用 `O_APPEND` 单行写 |
| dev 前端改动不生效 | `npm run build` 后必须同步 `dist/` → `src-tauri/target/debug/bin/dist/`（阶段 K 已踩） |
| 统计累计来源无 vendor 维度 | 如实标注口径，不伪装已拆分；登记 OPEN.md 待办，不在本阶段扩后端统计写入 |

---

## 给接手 AI 的完整提示词（零上下文可直接粘贴）

---

你是负责 **opencode2api_enhance** 的实现代理。请**完整执行阶段 L**，不要只写方案。

### 基线
- 目录：`D:\AI_Projects\opencode2api_guide\opencode2api_enhance`（Windows，Git Bash）
- 当前分支 `feat/phase-k-kilo-gateway`（tip `392a11f`），**不新开分支**，小步 commit，不 push
- 唯一实施计划：`docs/ai-framework/plans/2026-11-12-vendor-dimension-and-four-pages.md`（本文件）
- 必读：`AGENTS.md`、`coding-standards.md`、`docs/ai-framework/phased-plan-driven.md`、`docs/issue-log/OPEN.md`
- 环境：Go 1.22+ 在 PATH；`npm/npx` 可用；**PowerShell 5.1 读 UTF-8 必须显式指定编码**；验证走 `npm run tauri:dev`（`OPCODE2API_KILO_PORT=30082`），生产 40000/40080/50080 不许停

### 做（按 Task 1→6，每 Task 测过再 commit）
1. Task 1 Kilo 调用日志落盘 + 并入 `ReadCallLog`（**不得 import main 包**；`kilo_calls.jsonl` 放 `<RuntimeDir>/kilo/`；异步落盘不阻塞响应）
2. Task 2 `CallLogFilter` 加 `Vendor` 与后端分类（kilo/opencode/custom），前端 `CallLogFilter` 类型同步
3. Task 3 日志页供应商下拉 + 删 KiloPage 最近调用卡片；新建共享 `src/lib/vendor.ts`
4. Task 4 统计页供应商下拉（只作用于趋势/日志派生，累计表注明口径）
5. Task 5 诊断页加 Kilo 30080 自检与四端口总览；设置页补 Kilo 端口与配置值/生效值双列；`ConfigView` 类型补 kilo 字段
6. Task 6 文档 + 计划验收表回填

### 不做
- ❌ 不引用 main 包 / `globalAgg` / `chatRouterVar` / `vendors/custom` / `ProviderService` / 50080 / 40080
- ❌ 不改生产 `call_log.jsonl` 写入侧；不替换生产 exe/dist；不停生产端口
- ❌ 不做 auto 竞速路由（2-4 未决）；不修 `callmetric_engine_test.go` 时间炸弹；不修 `custom.go:332` 通用缺陷
- ❌ 日志不记录请求体/响应体/密钥
- ❌ 不 `gofmt -w` 整仓库；不 push

### 工作方式
1. 开工先跑基线：`go build ./...`、`go test ./core/manager/ -run Kilo -count=1`、`npx tsc -b`，确认干净（整包既有 4 条时间炸弹已知）。
2. 证据优先：完成前重跑 L-1~L-10 每条命令，把**真实输出**贴进交付说明；禁止「应该能过」。
3. 前端改完 `npm run build` 后**必须**把 `dist/` 同步到 `src-tauri/target/debug/bin/dist/` 才在 dev 生效。
4. 用简体中文汇报；代码标识符保持原样。
5. 隔离验证用 30082/44100 段，结束杀进程并复查端口为空。

### 交卷
提交列表、L-1~L-10 自评表（每条附命令与输出摘要）、独立审查结论、内置浏览器截图路径、残留风险与需人类执行项（部署窗口、2-4 决策、累计统计 vendor 维度待办）。

现在开始：读完计划，从 Task 1 执行到最后。

---

## 残留手工验收清单

1. **部署**：阶段 K + L 的代码一起上生产需停机窗口（用户授权）。
2. **2-4 auto 竞速路由**：A/B 方案与候选范围待用户拍板，另立阶段。
3. **累计统计 vendor 维度**：后端统计文件暂无 vendor，需另立阶段（已登记 OPEN.md）。
4. **生产 V-7 停机实测**：阶段 K B5 保留项。
