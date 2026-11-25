# 阶段 H1：BuddyHub 插件面板改版（对齐设计稿 v2）

> **状态：** 已完成（v1.5.0 已部署，2026-11-05）
> **For agentic workers:** 按 Task 顺序执行；每 Task 测完再进下一 Task。
> **交接提示词**见文末「给接手 AI 的完整提示词」。
> **元规范:** `docs/ai-framework/phased-plan-driven.md`
> **调研依据:** `docs/buddyhub-panel-survey-2026-11-05.md`

**Goal:** 把已装 BuddyHub 插件面板（6 页 tab 风格）重做成设计稿 `docs/design/console-mockup.html` 的样子（侧栏 + 10 视图 + 11 主题），并补齐设计稿里已有数据来源的只读功能。
**Architecture:** 面板是 `go:embed panel.html` 的单文件自包含 HTML（内联 CSS/JS、零外部依赖），serve 时替换 `__PLUGIN_AUTH_TOKEN__` / `__PLUGIN_VERSION__` 两个占位符。后端 `internal/pluginhub/handler.go` 只增只读端点与透出字段，不改选号/切号/任务调度行为。
**Tech Stack:** Go 1.26（net/http、go:embed）、原生 HTML/CSS/JS（无框架、无 CDN）
**实施档位：** 全能（默认）
**子代理：** 启用（代码审查 + 独立验收）

---

## 前置阅读（必须）

| 优先级 | 文件 |
|--------|------|
| P0 | `docs/ai-framework/phased-plan-driven.md` |
| P0 | 本文件 |
| P0 | `docs/buddyhub-panel-survey-2026-11-05.md`（现状/可扩/不可为的判定依据） |
| P0 | `docs/design/console-mockup.html`（目标形态，2572 行） |
| P1 | `AGENTS.md`、`CODE_REVIEW.md` |
| P1 | `D:\AI_Projects\_plugin-work\phase5\buddyhub\README.md`（双入口结构，改错文件的常见坑） |

**代码仓库路径：** `D:\AI_Projects\_plugin-work\phase5\buddyhub`（**用户已确认：以已装 exe 为准**）
**宿主仓库路径：** `D:\AI_Projects\opencode2api_guide\opencode2api_enhance`（本计划文档所在）
**部署目标：** `D:\Program Files\opencode2api\bin\providers\buddyhub\`（工作区外，需用户授权）

---

## Global Constraints（冲突时以本节为准）

1. **只改 `_plugin-work/phase5/buddyhub`**。不要改 `D:\AI_Projects\buddyhub`（那是 123 行旧面板的源码，与已装 exe 不同份，改了上不了机）。
   判定依据：把 panel.html 的两个占位符当通配符正则匹配 exe 字节流，phase5 版命中、源码版不命中。
2. **面板必须单文件自包含**：`panel.html` 内联全部 CSS/JS，**不得引入任何外部 CDN/字体/图片**。设计稿本身零依赖，可直接取材。
3. **必须保留两个占位符**：`__PLUGIN_AUTH_TOKEN__`、`__PLUGIN_VERSION__`（`panel.go` 在 serve 时替换）。写死或漏掉 → 面板全部 401。
4. **不改动行为面**：选号策略、冷却/熔断算法、任务调度时点、realm 路由，一律不动。本阶段只做「面板 + 只读端点」。
5. **只读设置**（用户已定）：设置页只展示，不写 `provider.json`。面板**不得**出现保存配置的调用。
6. **凭证不进 git / 不进日志**：`data/auths/workbuddy-*.json` 只读数量，不读内容；面板任何位置不得渲染 accessToken/refreshToken。
7. **`_plugin-work/phase5/buddyhub` 不是 git 仓库**：开工前先做时间戳备份目录（沿用 `_backup_YYYYMMDD-HHMMSS` 惯例），每个 Task 完成后跑 `go build ./... && go test ./...` 作回归闸门。
8. **部署需授权**：替换 `D:\Program Files\opencode2api\...\buddyhub-provider.exe` 前必须问用户；替换后需杀掉运行中的 `buddyhub-provider.exe`，由宿主监督协程用新 exe 重拉。
9. **明确不做（本阶段）**
   - ❌ 自动注册页、临时邮箱页（BuddyHub 走 OAuth 扫码，无注册/邮箱链路，做了是空壳）
   - ❌ 链路与出口页、按号出线（引擎无代理支持，单一共享 Transport）
   - ❌ 错误转移规则可配、新增「最少在途/粘性单选」策略（需改 `Handler.Config` 热改与 `pool.pick`，另立阶段）
   - ❌ 余额同步进度条（需把同步阻塞改异步任务，另立阶段）
   - ❌ 导出全量凭据（敏感操作）
   - ❌ 切号事件埋点（需在 pool 冷却路径挂事件回调，另立阶段；本阶段「负载与切号」页用调用记录里的失败行充当线索，并在页面上明说数据来源）

---

## 阶段开头：上阶段遗留

> 上一阶段（Phase 5 视觉令牌统一）的验证结论：`_plugin-work/phase5/buddyhub` 副本 `build/test` 通过，已部署 v1.4.0。无 ⬜/❌ 遗留。

| 上阶段遗留项 | 来源 | 未通过原因 | 状态 |
|-------------|------|-----------|------|
| 无 | — | — | ✅ |

---

## 跳过项（因档位未做，**非缺陷**，待补做）

| 跳过项 | 原因 | 待补做 |
|--------|------|--------|
| 真实浏览器端到端点击回归 | 面板需登录态与宿主运行；自动化只做 HTTP 抓取 + JSON 断言 | ⬜ 待用户在真机点一遍（见「残留手工验收清单」） |
| 账号池批量选择（多选 + 批量停用/签到） | 计划 Task 3 列为内容项，实际未做；单号操作已可用 | ⬜ H2 补做 |
| 签到任务「下次执行倒计时」 | 需前端定时器实时递减 | ⬜ H2 补做 |
| 仪表盘「模型占比条」 | `/api/stats` 有数据，未加可视化 | ⬜ H2 补做 |
| 积分同步实时进度条 | 需把 `refreshCredits` 从同步阻塞改异步任务（C 档） | ⬜ 另立阶段 |
| 面板体积控制到 ≤90KB | 实为 108.9KB；无外部依赖、单次内联加载 | ⬜ 后续按需拆分样式 |
| `dataDirUsage` 函数当前无调用方 | 为后续「清理缓存」只读统计预留 | ⬜ 接入或删除 |

---

## 与前后阶段

| 阶段 | 状态 | 交付 |
|------|------|------|
| Phase 5（视觉令牌统一） | ✅ | v1.4.0 已装，6 页暖白抹茶 tab 面板 |
| **本阶段 H1** | ✅ | v1.5.0：10 视图侧栏面板 + 11 主题 + 只读增强端点（已装并真机验证） |
| 后续（H2，勿塞进本阶段） | | 切号事件埋点 / 错误转移可配 / 余额异步同步进度 |

---

## 视图映射（13 → 10）

| # | 设计稿视图 | 本阶段 | 数据来源 | 说明 |
|---|---|---|---|---|
| 1 | 仪表盘 dash | ✅ | `/api/usage` + `/api/stats` + `/api/accounts` | KPI + 池健康度 + 模型占比 |
| 2 | 账号池 accounts | ✅ | `/api/accounts`（扩字段） | 筛选/搜索；批量操作用串行调用现有单号端点 |
| 3 | 积分与余额 credits | ✅ | `/api/accounts` + `POST /api/credits/refresh` | 逐号余额；「即将过期」需扩字段 |
| 4 | 签到任务 tasks | ✅ | `/api/tasks` + `/api/tasks/logs` | 7 个任务 + 摘要 + 手动执行 |
| 5 | 对话调试 chat | ✅ | 插件自身 `/v1/chat/completions` | 显示请求模型→真实模型 |
| 6 | 调用日志 logs | ✅ | `/api/calls`（扩字段） | 加 TTFB / 扣积分 / 裸模型 / 区 |
| 7 | 用量统计 usage | ✅ | `/api/stats` + `/api/usage` | 按模型 / 按账号 |
| 8 | 负载与切号 routing | ⚠️ 只读 | `/api/config`（新）+ `/api/calls` | 展示策略配置；切号线索来自失败调用，页面明确标注 |
| 9 | 设置 settings | ✅ 只读 | `/api/meta`（新） | 目录/占用/版本/端点；**无保存按钮** |
| 10 | 设计系统 design | ✅ | 前端 | 11 主题全览 + 令牌对照（保留为可切换页） |
| — | 链路与出口 egress | ❌ 删 | — | 引擎无代理层 |
| — | 自动注册 autoreg | ❌ 删 | — | OAuth 扫码，无注册链路 |
| — | 临时邮箱 mail | ❌ 删 | — | 登录不经过邮箱验证码 |

---

## File Structure（预期变更）

| 文件 | 动作 | 职责 |
|------|------|------|
| `internal/pluginhub/panel.html` | 重写 | 单文件面板：侧栏 + 10 视图 + 11 主题 + 动效 |
| `internal/pluginhub/panel.html.orig` | 保留 | 现状 580 行版（回滚参照，勿删） |
| `internal/pluginhub/handler.go` | 修改 | `accountItem`/`callEntry` 扩字段；注册 `/api/stats`、`/api/config`、`/api/meta` |
| `internal/pluginhub/status.go` | 新建 | `/api/stats`（包 `server.MetricsSnapshotOf`）、`/api/config`、`/api/meta` 三个只读处理器 |
| `internal/pluginhub/status_test.go` | 新建 | 三个新端点的表驱动用例（含鉴权、字段齐备） |
| `internal/pluginhub/handler_test.go` | 扩展 | `accountItem`/`callEntry` 新字段断言 |
| `cmd/pluginbuddy/main.go` | 修改 | `var version = "1.5.0"` |
| `providers/buddyhub/provider.json` | 修改 | `"version": "1.5.0"` |
| `README.md`（buddyhub） | 修改 | 版本号约定处补「本次两处同步为 1.5.0」 |

---

## API 契约（新增/变更）

### 变更：`GET /api/accounts` 每项新增字段

```json
{
  "uid": "…", "nickname": "…", "realm": "cn", "status": "normal",
  "credits": 1234,
  "credits_expiring": 300,
  "today_calls": 12,
  "in_flight": 0,
  "success_count": 480,
  "err_total": 21,
  "consecutive_fails": 0,
  "cool_remaining_sec": 1800,
  "cool_reason": "429 rate limit",
  "last_success": "2026-11-05T15:02:11+08:00",
  "last_err": "2026-11-05T14:40:02+08:00",
  "rate_limited_models": [{"model":"…","until":"…","reset_at":"…","reason":"6004 model rate limit"}],
  "model_costs": [{"model":"…","cost_per_1k":0.002,"last_seen":"…","samples":17}],
  "disabled": false, "cooling": false,
  "manual_disabled": false, "manual_reason": "…"
}
```

**取数口径**：一律走 `pool.Pool.Status(uid)` 或 `List()` 返回的 `pool.Status`，**不新增引擎读取逻辑**。
`credits_expiring` 需在 `pool.Status` 上补一个字段（见 Task 1 Step 3）。

### 变更：`GET /api/calls` 每项新增字段

```json
{
  "time": "11-05 15:04:05", "model": "cn:deepseek-v4.1-flash",
  "bare": "deepseek-v4.1-flash", "realm": "cn", "uid": "…", "nick": "…",
  "status": 200, "latMs": 4210, "ttfbMs": 312,
  "prompt": 1200, "completion": 340, "tokens": 1540,
  "hasUsage": true, "credit": 0.42, "hasCredit": true,
  "mode": "stream"
}
```

### 新增：`GET /api/stats`

包一层 `server.MetricsSnapshotOf()`，原样透出（含 `total` 与 `models[]` 的 TTFB / tokens/s / 缓存 / 每请求成本）。

### 新增：`GET /api/config`（只读）

```json
{
  "rotation_mode": "round-robin", "cost_first": true, "daily_limit": 0,
  "sticky_on": true, "sticky_max_uses": 3,
  "cn_on": true, "global_on": true, "default_realm": "cn",
  "prompt_mode": "passthrough",
  "tasks": [
    {"key":"checkin","on":true,"hours":[9,21]},
    {"key":"travel","on":true,"hours":[9,21]},
    {"key":"activity","on":true,"hours":[10]},
    {"key":"keepalive","on":true,"hours":[22]},
    {"key":"blackcat","on":true,"hours":[23]}
  ]
}
```

**数据源**：`Engine.ConfigSnapshot()`（`StartConfigWatch` 每 3 秒 `SetCfg` 写回，`cfgMu` 保护）。**无写入口**。

### 新增：`GET /api/meta`

```json
{
  "version": "1.5.0", "plugin_id": "buddyhub",
  "data_dir": "…\\data", "auth_dir": "…\\data\\auths",
  "auth_files": 2,
  "files": [
    {"name":"state.json","bytes":1469},
    {"name":"usage-log.json","bytes":1750868},
    {"name":"events.jsonl","bytes":1013},
    {"name":"task-state.json","bytes":1293}
  ]
}
```

**注意**：`auth_files` 只返回数量，不返回文件名以外的任何凭据内容。

---

## Task 1：后端只读字段与端点

**Files:** `internal/pluginhub/handler.go`、`internal/pluginhub/status.go`（新建）、`internal/pluginhub/status_test.go`（新建）、`internal/pool/entry.go`（仅加一个字段）、`internal/pool/state.go`（`statusOf` 补一行）

**行为:**
1. `pool.Status` 增加 `CreditsExpiring int64 \`json:"credits_expiring"\``，`statusOf` 里写 `e.creditsExpiring`。
2. `accountItem` 扩为契约所列字段，数据取自 `eng.Pool.Status(uid)`（**不要**再用 `List()` 的精简投影；`List()` 本身已是 `Status`，可直接用，只需在 `accountItem` 里挑字段）。
3. `callEntry` 扩字段，取自 `server.CallLogSince(0)` 的 `CallRecord` 全字段。
4. 新建 `status.go`：`statsHandler`（包 `MetricsSnapshotOf`）、`configHandler`（读 `ConfigSnapshot()`）、`metaHandler`（`filepath.Walk` 只统计 `DataDir` 顶层文件 + `AuthDir` 的 `.json` 数量）。
5. `RegisterRoutes` 注册 `GET /api/stats`、`GET /api/config`、`GET /api/meta`，全部走既有 `requireToken`。

**Steps:**

1. 备份：`cp -r internal/pluginhub internal/pluginhub.bak-$(date +%Y%m%d-%H%M%S)`（或整目录备份到 `_backup_<ts>`）。
2. 按上表改代码。
3. 跑：`go build ./... && go test ./internal/pluginhub/ ./internal/server/ ./internal/pool/`
   期望：`ok` 三个包，无 FAIL。
4. 新增用例覆盖：401（无 token）、字段齐备（用构造的 `pool.Status` 断言 JSON 键存在）、`/api/meta` 不泄漏 token 字样（断言响应体不含 `accessToken`/`refreshToken`）。
5. 本目录非 git，**不做 commit**；记一行到宿主仓库 `docs/issue-log/2026-11-05.md`。

---

## Task 2：面板骨架与设计令牌移植

**Files:** `internal/pluginhub/panel.html`（重写第一部分）

**行为:** 从 `docs/design/console-mockup.html` 移植：
- 11 套调色板变量（`html[data-palette="…"]`）与形状令牌
- `.shell/.side/.nav-item/.main/.top` 布局
- `.card/.kpi/.tbl/.chip/.btn/.seg/.bar/.ring/.timeline/.tagcloud` 组件样式
- `.drawer/.toast-wrap/.switch/.checkline/.field/.hint/.divider` 辅助件
- 侧栏 5 个分组（总览 / 账号运营 / 调用与调试 / 网络与调度 / 系统），只挂 10 个视图的按钮
- **底部状态区**：网关可达性圆点 + 端点 + 版本（版本用 `__PLUGIN_VERSION__`）

**Steps:**

1. 建骨架：`<aside>` 侧栏 + `<main>` 主区 + 10 个空 `<section class="view">`。
2. 粘贴令牌与组件 CSS（**删除**设计稿里只服务已砍掉三页的选择器，如 `.node-*`）。
3. 跑：`go build ./...`
   期望：exit 0（HTML 是 embed，编译期只校验文件存在）。
4. 跑：起插件后 `curl -s http://127.0.0.1:<port>/ | grep -c 'data-view'`
   期望：`10`。

---

## Task 3：十个视图的内容实现

**Files:** `internal/pluginhub/panel.html`（逐视图填充 + JS）

**行为:**

| 视图 | 必含内容 |
|---|---|
| 仪表盘 | 4 个 KPI（可用账号/池内积分/今日调用/今日 Token）、近 N 调用柱状图（canvas 手绘）、池健康度环（healthy/cooling/disabled）、模型占比条、最近调用表（10 行） |
| 账号池 | 状态 seg 筛选 + 搜索 + 表格（账号/区/状态/积分/今日调用/在途/最近成功/限流模型/操作）+ 批量选择（前端串行调用单号端点） |
| 积分与余额 | 池总积分 / 今日消耗 / 低余额数 + 逐号表（剩余/即将过期/今日消耗/最近同步）+ 「同步积分」进度（**只显示同步前后差值，不做实时进度**） |
| 签到任务 | 今日完成度条 + 下次执行倒计时 + 7 个任务卡（名称/描述/时点/状态/摘要/耗时/执行按钮）+ 运行日志 |
| 对话调试 | 多轮气泡（保留现有 `chatHistory.slice(-12)`）+ 请求模型/真实模型/耗时/Token 命中卡 |
| 调用日志 | 表格（时间/请求模型/真实模型/区/账号/状态/输入/输出/积分/TTFB/耗时/模式）+ 状态与模型筛选 |
| 用量统计 | 按模型表（调用/成功/失败/TTFB/tokens/s/缓存命中/成本）+ 按账号表 |
| 负载与切号 | **只读**：选号策略卡（高亮当前 rotation_mode）、粘性/每日上限/分区开关、最近失败调用表（标注「线索来源：调用记录，非引擎切号事件」） |
| 设置 | **只读**：数据目录表（路径/占用）、auths 数量、版本、端点、刷新按钮 |
| 设计系统 | 11 主题卡片（点即切换）+ 当前令牌色板 + 语义色/按钮对照 |

**Steps:**

1. 按表逐视图写结构 + 渲染函数（`renderDash()` 等，替换现有 `loadDash()`/`loadAccounts()` 等，函数名保持可读）。
2. 图表用原生 canvas 手绘（参考设计稿 `drawMainChart`/`drawUsageChart`，**去掉环境粒子画布**以控制体积；保留 `prefers-reduced-motion` 降级）。
3. 跑：`go build ./... && go test ./internal/pluginhub/`
   期望：通过。
4. 跑：`curl -s http://127.0.0.1:<port>/ | grep -c 'view-'`
   期望：至少 `10`（视图 id 出现次数）。

---

## Task 4：主题切换与动效

**Files:** `internal/pluginhub/panel.html`

**行为:**
- 11 主题：右上角圆点组 + 设置页下拉 + 设计系统页卡片，三处联动；`localStorage['buddyhub.palette']` 记忆；默认 `matcha`。
- 动效开关：`localStorage['buddyhub.motion']`；关闭时加 `data-motion="off"`；`prefers-reduced-motion: reduce` 强制关。
- 数字滚动、表格行入场、按钮微交互（保留 2~3 个，不做粒子画布）。

**Steps:**

1. 实现 `setPalette(name)` / `setMotion(on)`，与设计稿同名以便对照。
2. 跑：`go build ./...`
3. 手工核对：切换后 `document.documentElement.dataset.palette` 变化；刷新后保留。

---

## Task 5：构建与本机冒烟

**Files:** 无代码改动（产生 `buddyhub-provider.exe`）

**行为:** 编译插件 exe，起一个临时实例，抓面板并断言。

**Steps:**

1. 跑：`cd /d D:\AI_Projects\_plugin-work\phase5\buddyhub && set CGO_ENABLED=0 && go build -trimpath -o providers/buddyhub/buddyhub-provider.exe ./cmd/pluginbuddy`
   期望：exit 0，产物 > 10MB。
2. 跑：`go test ./...`（全量）
   期望：全 ok。
3. 起临时实例（**用临时 PROVIDER_DIR 副本，不碰已装目录**）：
   设 `PROVIDER_DIR`/`PROVIDER_CONFIG`/`PLUGIN_AUTH_TOKEN` → `buddyhub-provider.exe --provider-serve --port 0` → 读 ready 行端口。
4. 用 ready 行里的 `auth` 作 Bearer，逐个断言：
   - `GET /` → 200 且含 `data-view="design"`
   - `GET /api/accounts` → 200，首项含 `credits_expiring`/`today_calls`/`in_flight`
   - `GET /api/calls` → 200，首项含 `ttfbMs`/`hasCredit`/`bare`
   - `GET /api/stats` → 200，含 `total`/`models`
   - `GET /api/config` → 200，含 `rotation_mode`/`tasks`
   - `GET /api/meta` → 200，含 `version`/`files`，且 **不含** `accessToken`/`refreshToken`
   - `GET /api/events` → 200
   - 不带 Bearer 访问上述任一 → 401
5. 记录输出到 `docs/issue-log/2026-11-05.md`。

---

## Task 6：部署与真机验证（需用户授权）

**Files:** `D:\Program Files\opencode2api\bin\providers\buddyhub\buddyhub-provider.exe`、`provider.json`

**行为:** 备份现有 exe → 覆盖 → 杀进程让宿主重拉 → 面板点一遍。

**Steps:**

1. **先问用户**确认可以替换已装 exe。
2. 备份：`cp buddyhub-provider.exe buddyhub-provider.exe.<ts>.old`（沿用目录内既有 `.old` 惯例）。
3. 覆盖 exe；`provider.json` 的 `version` 改 `1.5.0`（宿主 3 秒扫描会自动感知版本变化）。
4. 杀 `buddyhub-provider.exe`，等宿主重拉；`netstat` 确认新端口；日志找 `state":"ready"` 且 `version":"1.5.0"`。
5. 从宿主 UI 点面板入口打开，逐页点一遍（见下方手工清单）。

---

## 代码审查（阶段级环节，验收前）

**审查方：** 独立角色（非本阶段实现者）

**审查面：**

| 审查项 | 结论（✅/⚠️/❌） | 问题清单 |
|--------|------------------|----------|
| 风格（Go 注释密度、命名、面板 JS 与既有风格一致） | ⚠️ | 注释是「为什么」风格、密度与周边一致；面板 JS 用 `var`+IIFE 比旧面板（const/let/async）更保守，功能等价。**已修**：4 个文件 `gofmt` 未过（基线干净），已 `gofmt -w` |
| 测试完整性（新端点是否都有 401/字段断言） | ✅ | 新 3 端点有 401（无 token + 错 token）与字段断言；**已补** `handler_contract_test.go`（`accountItemOf`/`callEntryOf`/`taskInfo` 映射断言，含 `tokens=-1` 哨兵、状态优先级、零值时间） |
| 依赖与架构红线（无新增第三方依赖；未触碰选号/冷却/调度） | ✅ | `go.mod`/`go.sum` 与备份逐字节一致；`pool/pick.go` 零差异；选号/冷却/调度/realm 路由未动；`state.json` 序列化零漂移 |
| 安全（占位符保留；meta 不泄漏凭据；无 XSS——所有插值走 `esc()`） | ✅ | 占位符保留（VERSION 2 处，`ReplaceAll` 下正确）；28 处 `innerHTML` 逐点核查均走 `esc()`/数值化；设置页零输入框、零写调用；`/api/meta` 只报数量 |
| API 契约（`/api/accounts`、`/api/calls` 新字段与契约节一致） | ✅ | 字段逐一对上（见下「审查修复记录」）。**已修**：`eng.Cfg` 从不写回导致 `/api/config` 返回启动快照 → 改 `SetCfg`/`ConfigSnapshot` + `cfgMu`（另修 `nextFireToday` 直读竞争）；`last_err`/`last_success` 去掉 `omitempty`（键消失与空串是两种「无值」表示）；`taskInfo` 补 `Summary` 字段（面板摘要行原为死分支） |

**结论：** ⚠️ 有条件通过 → 条件已全部修复（见下节），复测通过。

### 审查修复记录（部署前完成）

| # | 审查发现 | 处理 |
|---|---|---|
| 1 | `eng.Cfg` 全仓库从不写回，`/api/config` 注释宣称热更口径但实际返回启动快照 | `configwatch.apply()` 末尾调 `eng.SetCfg(*cfg)`；`Cfg` 改私有 `cfg` + `cfgMu sync.RWMutex`；新增 `SetCfg`/`ConfigSnapshot`；`configHandler`、`nextFireToday` 全改走快照 |
| 2 | 4 个文件 `gofmt` 未过（基线干净） | `gofmt -w`，现 `gofmt -l ./internal ./cmd` 无输出 |
| 3 | `t.summary` 是死分支（Go 侧 `taskInfo` 无该字段） | `taskInfo` 新增 `Summary`，`listTasks` 从 `TaskRunState.Summary` 回填 |
| 4 | `/api/accounts`、`/api/calls` 新字段零测试覆盖 | 映射逻辑抽成 `accountItemOf`/`callEntryOf` 纯函数；新增 `handler_contract_test.go` 6 个用例 |
| 5 | `last_err`/`last_success` 带 `omitempty`，零值时整个键消失 | 去掉 `omitempty`，零值序列化为 `""`（契约测试钉住） |
| 6 | 账号池缺「限流模型」列（计划 Task 3 要求） | 补列 + `rate_limited_models` 渲染（模型名 + 重置时刻 chip） |
| 7 | `provider.json` 被格式化重排，diff 面变大 | 还原原文件，只改 `version` 一行 |
| 8 | 面板 108KB 超 90KB 目标 | **未处理**，登记为已知偏差（11 套主题令牌占 5.7KB、10 视图渲染逻辑是主体；无外部依赖，加载仍为单次内联）。见「跳过项」 |
| 9 | 计划 Task 3 部分视图组件未做：批量选择、任务倒计时、仪表盘模型占比条、积分同步进度 | **未做**，登记为下阶段（见「跳过项」） |

---

## 验收标准总表

| # | 标准 | 通过条件 | 验证责任人 | 结果 |
|---|------|----------|----------|------|
| 1 | 面板形态 | `GET /` 返回 200，含 10 个 `data-view`；零外部依赖 | 自动化（执行方） | ✅ 已装实例 10 个 `data-view`；`<script src`/`<link>`/`@import`/`url(http` 均为 0 |
| 2 | 占位符 | `__PLUGIN_AUTH_TOKEN__` 与 `__PLUGIN_VERSION__` 均保留，起进程后已替换 | 自动化 | ✅ AUTH_TOKEN 1 处、VERSION 2 处；serve 后 HTML 内已无占位符、含 `v1.5.0` |
| 3 | 新端点 | `/api/stats`、`/api/config`、`/api/meta` 均 200，字段与契约节一致 | 自动化 | ✅ 三端点 200；`meta.version=1.5.0`、`auth_files=2`、`config.tasks` 5 项 |
| 4 | 字段透出 | `/api/accounts` 含 `credits_expiring`/`today_calls`/`in_flight`/`rate_limited_models`；`/api/calls` 含 `ttfbMs`/`hasCredit`/`bare` | 自动化 | ✅ 冒烟 54/54 + `handler_contract_test.go` 映射断言 |
| 5 | 鉴权 | 8 个 `/api/*` 端点无 Bearer 时全部 401 | 自动化 | ✅ 冒烟 8/8 均 401 |
| 6 | 不泄漏 | `/api/meta` 响应体不含 `accessToken`/`refreshToken`/凭据文件名 | 自动化 | ✅ 冒烟 6 项 banned 字符串全不命中 |
| 7 | 只读 | 面板源码不含 `provider_json`、不含 `POST /api/admin/plugins` | 自动化（`grep`） | ✅ 均为 0；设置页实测 0 个 input、0 个 switch |
| 8 | 构建与测试 | `go build ./...`、`go test ./...` 均 exit 0 | 自动化 | ✅ 全 7 包 ok；`go vet` 干净；`gofmt -l` 无输出；`-race` 通过 |
| 9 | 代码审查 | 审查结论 ✅ 或 ⚠️（问题已登记）；❌ 下放 H2 | 独立角色 | ✅ ⚠️ 有条件通过 → 9 项已修/已登记（见「审查修复记录」） |
| 10 | 真机 | 10 页逐页打开无白屏、无 console 报错；主题切换刷新后保留 | 用户（手工） | ✅ 已装实例 10 页全部 `active` 且有数据（调 100 条/账号 2/用量 3 行）；主题切青花瓷→刷新保留；动效关→刷新保留 |
| 11 | 红线 | 未改 `D:\AI_Projects\buddyhub`；未触碰宿主仓库代码；密钥未入库 | 自动化 | ✅ `buddyhub` 源码 panel.html 仍 10-29 21:43；宿主仓库无代码改动 |

---

## 风险与降级

| 风险 | 缓解 |
|------|------|
| 面板体积膨胀（设计稿 CSS+JS ~141KB） | 砍掉粒子画布与未用视图样式；目标面板 ≤ 90KB（现 34KB） |
| `pool.Status` 新增字段引发 `state.json` 序列化漂移 | `CreditsExpiring` 在 `stateAccount` 里**已存在**且已持久化，本次只是把它从 `entry` 提到 `Status`，零落盘变化 |
| 复制 phase5 副本到别处导致部署错版本 | Task 5 产物必须落在 `providers/buddyhub/`；Task 6 用时间戳 `.old` 备份，可秒回滚 |
| 宿主 3 秒重扫与手动杀进程竞争 | 先备份再覆盖，再杀；若重拉失败，回滚 `.old` 并再杀一次 |
| 用户已装目录数据不可重建 | 全程不碰 `data/`；临时验证用独立 `PROVIDER_DIR` 副本 |
| 11 主题 CSS 体量大导致首屏闪白 | 令牌用 `:root` 默认 + `html[data-palette]` 覆盖；`localStorage` 在 `<head>` 内联脚本里同步应用 |

---

## 给接手 AI 的完整提示词

将下面整段粘贴给执行 AI 即可开工：

---

你是负责 **BuddyHub 插件面板改版** 的实现代理。请**完整执行本阶段**，不要只写方案。

### 基线
- 代码目录：`D:\AI_Projects\_plugin-work\phase5\buddyhub`（**不是** `D:\AI_Projects\buddyhub`）
- 该目录**不是 git 仓库**：开工前先做 `_backup_<时间戳>` 整目录备份；每 Task 用 `go build ./... && go test ./...` 作回归闸门
- 唯一实施计划：`docs/ai-framework/plans/2026-11-05-buddyhub-panel-redesign.md`（宿主仓库内）
- 必读：本计划、`docs/buddyhub-panel-survey-2026-11-05.md`、`docs/design/console-mockup.html`、`AGENTS.md`
- 已确认的基线状态：`go build ./...` 通过；`go test ./internal/pluginhub/ ./internal/server/` 全绿

### 做
1. 按 Task 1→6 顺序执行：后端只读字段与端点 → 面板骨架与令牌 → 10 个视图 → 主题与动效 → 构建冒烟 →（经用户授权）部署。
2. 面板是**单文件自包含 HTML**，零外部依赖，必须保留 `__PLUGIN_AUTH_TOKEN__` / `__PLUGIN_VERSION__` 两个占位符。
3. 只做只读：设置页不写 `provider.json`；不得出现 `provider_json` 或 `POST /api/admin/plugins`。
4. 所有插值走 `esc()`，防 XSS。
5. 删掉设计稿里「链路与出口 / 自动注册 / 临时邮箱」三页及其专属样式。

### 不做
- ❌ 改 `D:\AI_Projects\buddyhub`（旧源码，与已装 exe 不同份）
- ❌ 触碰选号策略、冷却/熔断算法、任务调度时点、realm 路由
- ❌ 触碰已装目录的 `data/`
- ❌ 提交密钥；未授权的 `git push`；未授权替换已装 exe

### 工作方式
1. 先跑基线 `go build ./... && go test ./...` 确认干净。
2. 严格按 Task 顺序；每 Task 完成即跑计划里的验证命令，**保留真实输出**。
3. 证据优先：不要说「应该能过」。
4. 用简体中文回复进度；代码标识符保持原样。
5. 遇到计划与实际不符（例如某字段其实不存在），**停下来记录并调整计划**，不要擅自扩大范围。

### 交卷
全部完成后给出：改动文件列表、验收表自评（11 项逐条 + 证据）、构建/测试输出、面板体积、残留风险、需要用户手工确认的项。

现在开始：读完本阶段计划，从 Task 1 执行到最后。

---

## 残留手工验收清单

（自动化之外的 GUI 项，需用户在真机点）

1. 从宿主 UI 打开 BuddyHub 面板，10 个侧栏项逐一点开，无白屏、无 console 报错。
2. 右上角切 3 个以上主题，刷新页面，主题保留。
3. 关掉动效开关，刷新，动效仍关。
4. 账号池：筛选「冷却」/「禁用」，数量与仪表盘健康度环一致。
5. 签到任务：点某个任务「执行」，20 秒内运行日志出现该任务行。
6. 对话调试：发一条消息，命中卡显示请求模型与真实模型，两者可区分。
7. 设置页：确认**没有**任何保存/输入框（只读）。
8. 窗口宽度拖到 800px 以下，侧栏折行为横向导航，内容不溢出。
