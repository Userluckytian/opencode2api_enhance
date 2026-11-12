# 阶段 O：去掉试跑 + 通道可视化（日志逐条显示 / 诊断页统计）

> **状态：** 计划已就绪，待批准后实施
> **For agentic workers:** 按 Task 顺序执行；每 Task 测完再进下一 Task。验收认证据，禁止「应该能过」。
> **元规范:** `docs/ai-framework/phased-plan-driven.md`；仓库约定 `AGENTS.md`
> **基线分支：** `main`（tip `228a159`，与 origin 同步，本地 2 分支）

**Goal:** ① 去掉 Kilo 页「试跑」按钮（浏览器跨端口 fetch 30080 被 CORS 拦、功能实际不可用，用户选择直接删除）；② 调用日志逐条显示 OpenCode 引擎请求走的是 **WARP 匿名通道** 还是 **key 通道**；③ 诊断页新增「通道分析」，统计两类通道的请求数/成功率/占比，让用户能查「具体走哪个多」。

**Architecture / 数据基础（已核实）：**
- 引擎 stdout 日志（`<free-tier>/stdout.log`）每行有 `channel`（`key` / `anonymous`）、`anonymous:bool`、`tier`（`zen`/`go`）、`legs` 字段。
- `core/manager/calllog_engine.go:215-223` 已把通道映射进 `CallLogRecord.Vendor`：`anonymous=true → "opencode"`（WARP 匿名通道）、`tier=zen/go → "zen"/"go"`（key 通道）；**但 `channel` 原值未单独保留**，且与 Kilo/自定义源的 `Vendor` 语义混在一起。
- 最近 3000 条调用日志实测：`zen` 2000 / `opencode` 935 / `buddyhub` 50 / `qw2api` 14 / `kilo` 1 —— 数据足够做通道统计。
- 因此本阶段**不新增日志写入**，只：① 给 `CallLogRecord` 增加可选 `channel` 字段并在引擎解析时填入；② 日志页按「WARP 匿名 / key(zen|go) / 其它」显示通道徽标；③ 诊断页新增聚合端点 + 图表/表格。

**Tech Stack:** Go 1.22 标准库；React 19 + TS + Tailwind；现有 `api.callLogFiltered` / `call-metrics` 风格。

**实施档位：** 全能　**子代理：** 启用（独立代码审查）

---

## 前置阅读（必须）

| 优先级 | 文件 |
|---|---|
| P0 | 本文件、`docs/ai-framework/phased-plan-driven.md` |
| P0 | `core/manager/calllog_engine.go`（engineLogLine 结构 :40-65、映射 :200-245）、`core/manager/calllog.go`（CallLogRecord :37-88） |
| P0 | `src/pages/LogsPage.tsx`（LogRow 徽标 :234-248、筛选 :414-424）、`src/pages/DiagPage.tsx`（调用分析卡片 :454-712） |
| P1 | `AGENTS.md`、`coding-standards.md`、`docs/issue-log/OPEN.md`、`src/pages/KiloPage.tsx`（试跑 :143-179, :216-227） |

---

## Global Constraints

1. **不改日志写入侧 / 不新增外部依赖**：引擎 stdout 已有 channel 字段，只在解析层补一列；不改引擎、不动生产配置。
2. **通道判定单一真源**：后端 `CallLogRecord.Channel`（新，可选）由 `calllog_engine.go` 从 `raw.Channel`/`raw.Anonymous` 推导；前端不重复造规则，只读该字段。旧记录无该字段时按 `vendor` 兜底（`opencode`→warp、`zen|go`→key），并标注「推断」。
3. **不影响现有供应商筛选**：日志页已按 `vendorOf` 三类（kilo/opencode/custom）筛选，通道徽标是**新增一列/标签**，不改动筛选逻辑。
4. **去掉试跑是删除，不是修 CORS**：删按钮 + 删 `probe` 函数 + 清理未用 import（`Play` 等）；不引入 40000 代理端点、不给 30080 加 CORS。
5. **诊断页统计只走只读 API**：新增 `GET /api/admin/channel-stats`（从现有 `ReadCallLog` 现算，进程内，不落盘），前端轮询与现有「调用分析」同款。
6. **密钥不进 git**；Git 小步 commit、规范 `<gitmoji><type>(<scope>): 中文描述`；**默认不 push**（本阶段代码是否需要发版由用户另行决定，不自动打 tag）。
7. **YAGNI**：不做按模型/按时间的通道下钻（先给总量与占比）、不做通道切换告警、不做实时流。
8. **不替换生产 exe/dist**：验证走 `npm run tauri:dev`（dev 44100/44180/44280/30082 段），前端改动需同步 `dist/` → `src-tauri/target/debug/bin/dist/`。

---

## 阶段开头：上阶段遗留（如实带入，本阶段不修）

| 上阶段遗留项 | 状态 |
|---|---|
| V-6b：`custom.go:332` 硬编码 `Free:true` + auto 候选通用缺陷 | ⬜ 不修 |
| B5：生产部署 + 停机 V-7（注：40000 已被用户自行更新到新版，但本阶段改动仍需部署才生效） | ⬜ 用户自行更新 |
| `callmetric_engine_test.go` 4 条时间炸弹 | ⬜ 整包「无新增失败」判定 |
| 累计统计缺 vendor 维度 / 同步落盘偏离 / `call_log_max` 不约束 Kilo 日志 | ⬜ 已知限制 |
| 虚拟模型 SSE 流式不支持 / `race` N 倍代价 | ⬜ 登记 |
| v2.0.5 旧 Release、已发布 Release 正文的引擎血统披露 | ⬜ 外发修改，用户未要求 |

---

## File Structure（预期变更）

| 文件 | 动作 | 职责 |
|---|---|---|
| `core/manager/calllog.go` | 修改 | `CallLogRecord` 新增 `Channel string \`json:"channel,omitempty"\`` |
| `core/manager/calllog_engine.go` | 修改 | 解析时填 `rec.Channel`（`warp` / `key`），兼容旧日志无 channel 时按 anonymous/tier 推断 |
| `core/manager/admin_http.go` 或新 `channel_stats.go` | 修改/新建 | `GET /api/admin/channel-stats` 聚合（总数 / warp / key / 各自成功失败 / 占比） |
| `main.go` | 修改 | 注册 `/api/admin/channel-stats`（requireAuth） |
| `src/lib/api.ts` | 修改 | `ChannelStats` 类型 + `channelStats()` 方法；`CallLogRecord` 补 `channel?` |
| `src/pages/KiloPage.tsx` | 修改 | 删「试跑」按钮 + `probe` 函数 + 未用 import |
| `src/pages/LogsPage.tsx` | 修改 | 日志行新增「通道」徽标（WARP 匿名 / key / —），展开详情显示原始 channel 与 legs |
| `src/pages/DiagPage.tsx` | 修改 | 新增「通道分析」卡片：两类通道的请求数、成功/失败、占比条、结论文案 |
| 单测 `core/manager/calllog_engine_channel_test.go` | 新建 | channel 映射（warp/key/旧记录兜底）与聚合统计断言 |
| `docs/issue-log/2026-11-12.md`、`OPEN.md` | 修改 | 阶段 O 记录 |
| 本计划 | 修改 | 验收表 / 审查表 / 状态回填 |

---

## 配置或 API 契约

```go
// CallLogRecord 新增（omitempty 兼容旧日志）
type CallLogRecord struct {
    ...
    Channel string `json:"channel,omitempty"` // "warp" | "key"；引擎记录专用
}
```

```jsonc
// 引擎 stdout 原始字段 → 映射
// {"channel":"key","anonymous":false,"tier":"zen"}  → Channel="key",  Vendor="zen"
// {"channel":"anonymous","anonymous":true}          → Channel="warp", Vendor="opencode"
// 旧日志无 channel：anonymous==true → warp；tier!="" → key；否则 ""
```

```jsonc
// GET /api/admin/channel-stats → 从 ReadCallLog(0) 现算
{
  "total": 3000,
  "warp": { "requests": 935, "ok": 800, "fail": 135 },
  "key":  { "requests": 2000, "ok": 1900, "fail": 100 },
  "other": { "requests": 65, "ok": 60, "fail": 5 },
  "generated_at": "2026-11-12T..."
}
```

---

## Task 1：去掉 Kilo 页「试跑」

**Files:** `src/pages/KiloPage.tsx`

**Steps:**
1. 删 `probe` 函数（:143-179）、删按钮（:216-227）、清理 import 中 `Play`、以及 `busy` 联合类型里的 `'start'`（若仅此处用）。
2. `npx tsc -b` = 0；`npm run build` = 0。
3. 内置浏览器确认 Kilo 页不再有「试跑」按钮，其余（刷新目录/停用/虚拟模型）不受影响。
4. Commit：`🔥 feat(kilo): 移除浏览器直连不可用的试跑按钮`

---

## Task 2：后端通道字段 + 聚合端点

**Files:** `core/manager/calllog.go`、`calllog_engine.go`、`channel_stats.go`(新)、`main.go`、单测

**行为:**
- `CallLogRecord.Channel` 新字段（omitempty）。
- `calllog_engine.go` 映射：`raw.Anonymous==true` 或 `raw.Channel=="anonymous"` → `"warp"`；`raw.Channel=="key"` 或 `raw.Tier!=""` → `"key"`；旧日志两者皆空 → `""`（前端按 vendor 兜底）。
- `ChannelStats()`：遍历 `ReadCallLog(0)`，按 `Channel` 分组（空值按 vendor 推断且计入 `inferred` 计数），统计 requests/ok/fail；只统计引擎记录（`Source=="OpenCode 引擎"` 或 `Channel!=""` 或 vendor in opencode/zen/go），Kilo/自定义源归入 `other`。
- `GET /api/admin/channel-stats` 挂 `requireAuth`。
- 单测：warp/key/旧记录/空 channel 四条映射 + 聚合计数正确。

**Steps:** 1. 先写失败用例 → 实现到 PASS。2. `go build ./...`=0、`go vet . ./core/manager/`=0、`gofmt -l` 无输出。3. curl 复核端点返回真实分布。4. Commit：`✨ feat(log): 调用日志新增通道字段与统计端点`

---

## Task 3：日志页通道徽标

**Files:** `src/pages/LogsPage.tsx`、`src/lib/api.ts`

**行为:**
- 日志行在「供应商」徽标旁新增「通道」徽标：`warp` → 蓝/紫「WARP 匿名」、`key` → 绿「key 通道」、其它 → 不显示或显示「—」。
- 展开详情显示 `channel` 原值与 `legs` 逐跳（若已有 events 时间线则补一行「通道」）。
- **不改动** `vendorOf` 三类筛选、`visible` 逻辑、`CallLogFilter.Vendor`。
- `npx tsc -b`=0、`npm run build`=0；浏览器验证：筛 OpenCode 时能看到 WARP/key 两种徽标。

**Steps:** Commit：`✨ feat(log): 日志页逐条显示 WARP/key 通道`

---

## Task 4：诊断页通道分析卡片

**Files:** `src/pages/DiagPage.tsx`、`src/lib/api.ts`

**行为:** 新增「通道分析」卡片，展示：
- 总请求数、WARP 匿名通道 / key 通道各自的请求数、成功数、失败数、成功率、占比（纯 CSS 占比条，与现有 MiniBar 同款）
- 一句结论文案：如「key 通道占 68%，WARP 匿名通道占 31%」；若某通道 0 请求则提示「引擎当前未走 WARP 匿名通道」
- 5s/15s 轮询与现有调用分析一致；加载/错误态
- 不引入图表库

**Steps:** `npx tsc -b`=0；浏览器验证卡片渲染、数字与 `/api/admin/channel-stats` 一致。Commit：`✨ feat(diag): 新增 WARP/key 通道分析`

---

## Task 5：文档与日志

- `docs/issue-log/2026-11-12.md` 追加阶段 O；`OPEN.md` 更新（去掉试跑/CORS 已知限制若有）。
- 本计划：验收表 / 审查表 / 状态回填。

---

## 代码审查（阶段级环节）

**审查方：** 独立角色子代理

| 审查项 | 结论 | 问题清单 |
|---|---|---|
| 通道判定一致性（后端单一真源、旧记录兜底不误导） | | |
| 不改现有供应商筛选/日志结构兼容（omitempty） | | |
| 删除试跑无残留（未用 import / busy 状态 / 死代码） | | |
| 聚合端点只读、无性能隐患（全量遍历上限） | | |
| 前端数字与 API 一致、无永真 UI | | |
| 风格与规范 | | |

**结论：** ⬜

---

## 验收标准总表

| # | 标准 | 通过条件 | 责任人 |
|---|---|---|---|
| O-1 | 构建/静态 | `go build ./...`=0；`go vet . ./core/manager/`=0；`gofmt -l` 本阶段文件无输出；`npx tsc -b`=0；`npm run build`=0 | 自动化 |
| O-2 | 单测 | `go test ./core/manager/ -run 'Channel\|Kilo' -race` 全 PASS；整包无新增失败（4 条时间炸弹豁免） | 自动化 |
| O-3 | 去掉试跑 | Kilo 页无「试跑」按钮/无 `probe` 函数/无未用 import；其余按钮正常 | 浏览器 |
| O-4 | 通道字段 | `GET /api/admin/call-log` 引擎记录带回 `channel=warp|key`；旧记录无该字段仍可解析 | curl |
| O-5 | 聚合端点 | `GET /api/admin/channel-stats` 返回真实分布（warp/key/other 与日志样本一致） | curl |
| O-6 | 日志页 | 筛 OpenCode 逐条显示 WARP 匿名 / key 通道徽标；筛选逻辑未变 | 浏览器 |
| O-7 | 诊断页 | 通道分析卡片数字与 O-5 一致、占比条与结论文案正确 | 浏览器 |
| O-8 | 红线 | 无密钥入库；生产 exe/dist 未替换；隔离端口清场；生产 40000/40080/50080 在听 | 主会话 |
| O-9 | 文档 | issue-log / OPEN.md / 计划回填 | 主会话 |
| O-10 | 独立审查 | 6 面结论 + 阻断项清单 | 子代理 |

---

## 风险与降级

| 风险 | 缓解 |
|---|---|
| 旧 stdout 日志无 `channel` 字段，统计不准 | 按 `anonymous`/`tier`/`vendor` 推断并计入 `inferred` 计数；UI 标注「含推断」；不改历史日志 |
| `ReadCallLog(0)` 全量遍历在大日志下慢 | 与现有 `fail-aggregate`/`call-metrics` 同款进程内现算；上限由 `call_log_max` 与 8MB 尾窗约束，不加新存储 |
| 通道徽标与供应商徽标视觉混淆 | 用不同色系 + 明确文字「WARP 匿名 / key 通道」；不改变现有供应商标签 |
| 删试跑后误删其它按钮 | Task 1 只删 `probe` 与对应按钮，浏览器逐个复核刷新目录/停用/虚拟模型 |
| dev 前端不同步 | `npm run build` 后同步 `dist/` → `src-tauri/target/debug/bin/dist/`（阶段 K 踩过） |

---

## 残留手工验收清单

1. 本阶段代码上生产需用户自行部署（用户已明确「我自己更新」）；本阶段不自动打 tag/发 Release。
2. 阶段 K/L/M/N 遗留按各计划「阶段开头遗留」带入。
