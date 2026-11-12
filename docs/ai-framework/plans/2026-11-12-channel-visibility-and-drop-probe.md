# 阶段 O：去掉试跑 + 通道可视化（日志逐条显示 / 诊断页统计）

> **状态：** ✅ 代码/测试/浏览器验收完成（2026-11-12），**待部署**（用户自行更新，不自动发版）
> **For agentic workers:** 按 Task 顺序执行；每 Task 测完再进下一 Task。验收认证据，禁止「应该能过」。
> **元规范:** `docs/ai-framework/phased-plan-driven.md`；仓库约定 `AGENTS.md`
> **基线分支：** `main`（tip `228a159`，与 origin 同步，本地 2 分支）
> **交付提交：** `e6a1260`(计划) / `9aaaf32` / `e2152eb` / `a131bff` / `2acd751` / `dccc833`（均未 push）

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
  "total": 3000,          // 全部调用日志（含 Kilo / 自定义源）
  "engine_total": 2935,   // 引擎记录 = warp+key，通道占比的分母
  "warp": { "requests": 935, "ok": 800, "fail": 135 },
  "key":  { "requests": 2000, "ok": 1900, "fail": 100 },
  "other": { "requests": 65, "ok": 60, "fail": 5 },
  "inferred": 0,         // 旧日志按 vendor 推断的条数
  "generated_at": "2026-11-12T..."
}
// 占比 = warp|key / engine_total（不含 other），避免非引擎记录稀释。
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

**独立子代理结论（11-12 回传）：✅ 有条件通过** —— 六面无 ❌、**无 P0/P1 阻断项**；通道判定/不改现有行为/端点/前端/删试跑/测试均达标，审查中提出的两处关键疑点（占比分母、isEngine 收紧）已由 `dccc833` 修复并验证（84 条引擎记录 / WARP 49% / key 51%）。6 条 P2 处置：

| # | 问题 | 处置 |
|---|---|---|
| 1 | 日志页「·推断」前端判定比后端 `channelOf` 宽（不校验 source） | ✅ 已修：前端条件改为与后端同款（明确引擎来源 + vendor 白名单） |
| 2 | `channelOf` 的 `Source==""` 分支冗余、且收紧语义无单测 | ✅ 已修：`isEngine` 收紧为只认 `Source=="OpenCode 引擎"`，补 3 例单测（非空非引擎来源 / 空 source + opencode → other,false） |
| 3 | `ReadCallLog(0)` 对 JSONL 整文件读入、15s 轮询下大日志开销；计划「同款 fail-aggregate」表述不准 | ⚠️ 登记遗留（既有行为、非本阶段引入）；**订正**：fail-aggregate 实为 `ReadCallLog(2000)` 走尾窗，channel-stats 不限流，大日志另立阶段加 max/缓存 |
| 4 | `KiloPage` busy 联合残留死成员 `'stop'` | ✅ 已删 |
| 5 | 计划契约示例缺 `engine_total` | ✅ 已回填字段 + 占比分母说明 |
| 6 | `ChannelStatsCard.load` 无代次守卫，慢响应可覆盖新快照（React 19 下 setState 为 no-op） | ⚠️ 登记遗留（低优先，不泄漏） |

**部署一致性风险（审查特别提示）**：`dist/` 与生产 exe 均未入库，用户自行部署时**必须前后端同批更新** —— 旧后端不返回 `engine_total` 会让新前端占比显示 0%；旧 dist + 新后端会退回旧的 99 分母口径。已写入残留清单。

**复核跑数**：`go test -run 'Channel|EngineChannel' -race` 全 PASS、`go build ./...`=0、`npx tsc -b`=0、`git diff --check` 干净。

**结论：** ✅ **有条件通过** —— 条件 = P2 #3/#6 按遗留登记、部署时前后端同批更新并复核诊断卡片结论，不伪装全绿。

---

## 验收标准总表（2026-11-12 回填，逐条附真实输出）

| # | 标准 | 结果 | 命令 + 输出摘要 |
|---|------|------|----------------|
| O-1 | 构建/静态 | ✅ | `go build ./...`=0；`go vet . ./core/manager/`=0；本阶段文件 `gofmt -l` 无输出；`npx tsc -b`=0；`npm run build`=0（467 kB） |
| O-2 | 单测 | ✅ | `go test ./core/manager/ -run 'Channel\|EngineChannel\|Kilo\|CallLog' -race` 全 PASS；`channel_stats_test.go` 含映射 6 种 / channelOf 7 种 / 聚合 5 条（含 1 失败）+ `EngineTotal` 断言；整包 `go test ./core/manager/ ./vendors/custom/` 仅既有 4 条时间炸弹 FAIL，无新增失败 |
| O-3 | 去掉试跑 | ✅ | 内置浏览器 dev 44100 → Kilo 页 `hasProbe=false`，`刷新目录/停用/虚拟模型` 均在；`KiloPage.tsx` 无 `probe`/`Play`/busy 的 start 态 |
| O-4 | 通道字段 | ✅ | `GET /api/admin/call-log` 引擎记录样本 `channel=key, vendor=zen`；`CallLogRecord.Channel` omitempty 兼容旧记录 |
| O-5 | 聚合端点 | ✅ | `GET /api/admin/channel-stats` 实测 `total=99 engine_total=84 warp=41(ok6/fail35) key=43(ok23/fail20) other=15 inferred=0`；自查修复后占比以引擎子集为分母（41%/43% → **49%/51%**） |
| O-6 | 日志页 | ✅ | 浏览器：43 个「key 通道」+ 41 个「WARP 匿名」徽标，与 API 一致；供应商筛选（kilo/opencode/自定义）仍在；旧记录标「·推断」 |
| O-7 | 诊断页 | ✅ | 「通道分析」卡片显示 `引擎请求中 key 通道占 51%、WARP 匿名通道占 49%`，占比条/成功率/推断提示齐全；截图存 artifacts |
| O-8 | 红线 | ✅ | 生产 40000/40080/50080 在听；生产 exe/dist 未替换；隔离端口清场；无密钥入库；工作区干净 |
| O-9 | 文档 | ✅ | `docs/issue-log/2026-11-12.md` 阶段 O；`OPEN.md` 新增阶段 O 行并订正「40000 kilo/status 404」过时状态（实测 200）；本计划状态/验收表/审查表回填 |
| O-10 | 独立审查 | ✅ | 独立子代理判「有条件通过」，无 P0/P1 阻断项；P2 #1/#2/#4/#5 已修（`dccc833` + 本次），#3/#6 登记遗留。见 §代码审查 |

**总评：** O-1~O-10 全部通过（O-10 按「独立审查有条件通过、无阻断项、P2 部分修复部分登记」记）。**阶段 O 代码与验收完成，唯一未完 = 部署**（用户自行更新，不自动发版）。
**已知限制（如实）**：旧日志无 channel 靠 vendor 推断（UI 标「·推断」、API 给 inferred）；`ComputeChannelStats` 全量遍历 `ReadCallLog(0)`、受 5000 条 ring 与 8MB 尾窗约束，未做时间窗口下钻。

---

## 风险与降级

| 风险 | 缓解 |
|---|---|
| 旧 stdout 日志无 `channel` 字段，统计不准 | 按 `anonymous`/`tier`/`vendor` 推断并计入 `inferred` 计数；UI 标注「含推断」；不改历史日志 |
| `ReadCallLog(0)` 全量遍历在大日志下慢 | 与现有 `call-metrics` 同为进程内现算；但 `fail-aggregate` 实为 `ReadCallLog(2000)`（走 8MB 尾窗），channel-stats 用 `ReadCallLog(0)` 会整文件读 JSONL、无条数上限，15s 轮询下大日志放大开销 —— **既有行为、非本阶段引入，登记为 P2 遗留**（将来另立阶段加 max/缓存/节流） |
| 通道徽标与供应商徽标视觉混淆 | 用不同色系 + 明确文字「WARP 匿名 / key 通道」；不改变现有供应商标签 |
| 删试跑后误删其它按钮 | Task 1 只删 `probe` 与对应按钮，浏览器逐个复核刷新目录/停用/虚拟模型 |
| dev 前端不同步 | `npm run build` 后同步 `dist/` → `src-tauri/target/debug/bin/dist/`（阶段 K 踩过） |

---

## 残留手工验收清单

1. 本阶段代码上生产需用户自行部署（用户已明确「我自己更新」）；本阶段不自动打 tag/发 Release。
   **前后端必须同批更新**（旧后端不返回 `engine_total` → 新前端占比显示 0%；旧 dist + 新后端 → 退回 99 分母口径），部署后复核诊断页结论文本。
2. 阶段 K/L/M/N 遗留按各计划「阶段开头遗留」带入。
3. 独立审查 P2 遗留：① `ComputeChannelStats` 用 `ReadCallLog(0)` 整文件读 JSONL、15s 轮询下大日志放大开销（既有行为，另立阶段加 max/缓存；计划风险表「同款 fail-aggregate」已订正为「后者用 ReadCallLog(2000) 走尾窗」）；② `ChannelStatsCard.load` 无代次守卫，慢响应可覆盖新快照（React 19 下 setState 为 no-op，低优先）。
4. 未知 channel 值（如未来引擎输出 `direct`）当前一律归 `key`；实测只有 `key`/`anonymous`，属 YAGNI 假设，若上游扩展需补白名单。
