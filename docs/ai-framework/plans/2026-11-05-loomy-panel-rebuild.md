# 阶段 L1：LOOMy 插件面板按设计稿重建（9 页 + 图片生成）

> **状态：** 已完成（源码侧）
> **For agentic workers:** 按 Task 顺序执行；每 Task 测完再进下一 Task。
> **交接提示词**见文末「给接手 AI 的完整提示词」。
> **元规范:** `docs/ai-framework/phased-plan-driven.md`

**Goal:** 把 loomy 插件现有 4 卡片面板换成设计稿的 13 视图版式，裁掉做不到的 4 页，新增图片生成页，设置页只读，保留 11 主题。
**Architecture:** `panel.go` 的 `panelHTML` 常量整体替换为设计稿版式；后端按页补只读 API（包上游 `points/records`、`pet-work`、`onboarding/tasks`、`models`、`images/generations`）；新增 `data/` 落盘层支撑调用日志与用量统计。
**Tech Stack:** Go 1.26（标准库，无第三方依赖）· 单文件内嵌 HTML（原生 JS，零 CDN）
**实施档位：** 全能
**子代理：** 启用（验收阶段）

---

## 前置阅读（必须）

| 优先级 | 文件 |
|--------|------|
| P0 | `docs/loomy-panel-survey-2026-11-05.md`（本阶段唯一事实依据） |
| P0 | `docs/design/console-mockup.html`（版式与交互来源） |
| P0 | `docs/PLUGIN-PROVIDERS.md` §4/§5/§10（契约与 stdout 纪律） |
| P1 | `AGENTS.md`、`CODE_REVIEW.md` |

**仓库路径：** `D:\AI_Projects\_plugin-work\phase5\loomy`（代码基准 = 已装 exe 1.8.0）
**部署目标：** `D:\Program Files\opencode2api\bin\providers\loomy\`
**基线分支：** 从 `master` 拉 `feat/phase-l1-panel-rebuild`

---

## Global Constraints（冲突时以本节为准）

1. **不做（用户已明确）**：账号池、负载与切号、按号出线、自动注册、临时邮箱。
2. **设置页只读**：不写 `provider.json`，不碰宿主 admin API。
3. **保留 11 主题** + localStorage 记忆 + `prefers-reduced-motion` 降级。
4. **stdout 纪律**：只有 `ready`/`need_config`/`fatal` 三类状态行；日志一律 stderr。
5. **鉴权组合**：上游请求必须同时带 `token` + `Authorization: Bearer` + `traceparent`
   （实测只带 `token` 会 `100002 缺少 token`）。
6. **运行数据只写 `data/`**（`PROVIDER_DIR` 环境变量），不写宿主目录。
7. **零第三方依赖**：只用 Go 标准库（与现有 `go.mod` 一致）。
8. **Git**：小步 commit；**不 push**。

---

## 阶段开头：上阶段遗留

| 上阶段遗留项 | 来源 | 未通过原因 | 状态 |
|-------------|------|-----------|------|
| （无上一阶段） | — | — | — |

---

## 跳过项

| 跳过项 | 原因 | 待补做 |
|--------|------|--------|
| 真机部署（覆盖已装 exe） | 需重启插件进程，属破坏性操作，等用户单独授权 | ⬜ 待用户确认 |
| 覆盖率统计 | 未跑 `go test -cover` | ⬜ 待用户确认补做 |

---

## 与前后阶段

| 阶段 | 状态 | 交付 |
|------|------|------|
| 上阶段 | — | — |
| **本阶段** | ⬜ | 9 页面板 + 图片生成 + 落盘层，源码侧完成并本地验证 |
| 下阶段 | | 真机部署与观感调整（勿塞进本阶段） |

---

## 页面清单（9 页 + 删除的 4 页)

| # | 设计稿视图 | 本阶段 | 数据来源 |
|---|---|---|---|
| 1 | 仪表盘 | ✅ 保留 | `/api/status` + `/api/usage` |
| 2 | 账号池 | ❌ **删除** | 单 session，无多账号 |
| 3 | 积分与余额 | ✅ 保留（单号版） | `/api/points` → 上游 `points/records` |
| 4 | 签到任务 → **任务中心** | ✅ 保留并改名 | `/api/tasks` → `pet-work` + `onboarding/tasks` + `invitation-codes` |
| 5 | 对话调试 | ✅ 保留 | `/api/chat-test`（服务端代转） |
| 6 | 调用日志 | ✅ 保留 | `data/requests.jsonl` |
| 7 | 用量统计 | ✅ 保留 | `data/usage.json` |
| 8 | 负载与切号 | ❌ **删除** | 无多号可切 |
| 9 | 链路与出口 → **出站设置** | ✅ **精简保留** | `proxy_url` 配置 + 探测 |
| 10 | 自动注册 | ❌ **删除** | 仅手机号登录 |
| 11 | 临时邮箱 | ❌ **删除** | 无邮箱通道 |
| 12 | 设置 | ✅ 保留（只读） | provider.json 现状 |
| 13 | 设计系统 | ✅ 保留 | 纯前端 |
| ➕ | **图片生成** | ✅ **新增** | `/api/image` → 上游 `images/generations` |

---

## File Structure（预期变更）

| 文件 | 动作 | 职责 |
|------|------|------|
| `panel.go` | 重写 | `panelHTML` 换成设计稿版式（9+2 页）；新增面板 API |
| `upstream_api.go` | **新建** | 上游业务接口封装（points / tasks / images / models） |
| `store.go` | **新建** | `data/` 落盘层（requests.jsonl / usage.json / 原子写） |
| `plugin.go` | 修改 | `pluginVersion` → 1.9.0；`reqEntry` 扩字段；`data/` 初始化 |
| `main.go` | 修改 | `reqEntry` 字段同步（普通代理模式） |
| `panel_test.go` | **新建** | 面板 API 单测（httptest mock 上游） |
| `provider.json` | 修改 | `version` → 1.9.0 |

---

## Task 1：落盘层 + reqEntry 扩字段

**Files:** `store.go`（新建）、`plugin.go`、`main.go`

**行为：** 每次 `/v1/*` 请求落一行到 `data/requests.jsonl`，字段含
`ts/model/req_model/status/tokens_in/tokens_out/points/dur_ms/ttfb_ms/err`；
按天聚合写 `data/usage.json`。原子写（tmp + rename）。

**Steps:**
1. 实现 `store.go`：`appendRequest(rec)`、`loadRequests(n)`、`bumpUsage(date, model, tokens, points)`。
2. `forwardToUpstream` 解析响应体里的 `model` / `usage`（非流式读 body；流式解析末帧）。
3. 跑 `go build ./... && go vet ./...`　期望：exit 0
4. Commit：`✨feat(loomy): 新增 data/ 落盘层与请求字段扩展`

**验收：** 本地起 mock 上游，发 3 次请求后 `data/requests.jsonl` 有 3 行且字段齐全。

---

## Task 2：上游业务接口封装

**Files:** `upstream_api.go`（新建）

**行为：** 封装 5 组只读接口，统一「token + Bearer + traceparent」头组合与 envelope 拆包
（`code==='000000'` 取 `data`，否则报错）。

| 方法 | 上游路径 |
|---|---|
| `fetchPointsRecords(page, size)` | `/points/records` |
| `fetchPetwork()` | `/pet-work` |
| `fetchOnboardingTasks()` | `/onboarding/tasks` |
| `fetchInvitationCodes()` | `/invitation-codes` |
| `fetchModels()` | `/models` |
| `generateImage(prompt, model, size)` | `/images/generations` |

**Steps:**
1. 实现上述 6 个函数 + `envelope` 结构。
2. 单测覆盖：成功拆包、`code!=000000` 报错、HTTP 非 200 报错。
3. 跑 `go test ./... -run Upstream`　期望：PASS
4. Commit：`✨feat(loomy): 封装上游业务接口（积分/任务/邀请/模型/图片）`

---

## Task 3：面板 API 补全

**Files:** `panel.go`

**行为：** 在现有 `/api/status`、`/api/claim` 基础上新增：

| 端点 | 返回 |
|---|---|
| `GET /api/points` | 余额三段 + 台账最近 N 条 |
| `GET /api/tasks` | 宠物打工状态 + 新手任务进度 + 邀请码 |
| `GET /api/models` | 模型列表（含 context/caps） |
| `GET /api/requests` | `data/requests.jsonl` 最近 N 条（可筛选） |
| `GET /api/usage` | 按天 + 按模型聚合 |
| `GET /api/settings` | provider.json 只读快照（session 打码） |
| `POST /api/chat-test` | 服务端代转到 `?gateway=` 地址，聚合 SSE 后返回 |
| `POST /api/image` | 调 `generateImage` 返回图片 URL + 扣分 |

**Steps:**
1. 实现 8 个 handler，全部 `127.0.0.1` + CORS 现状不变。
2. `/api/chat-test` 必须服务端代转（宿主无 CORS，浏览器直连会被拦）。
3. 跑 `go test ./... -run Panel`　期望：PASS
4. Commit：`✨feat(loomy): 面板 API 补全 8 个只读/代理端点`

---

## Task 4：面板 HTML 重建（视觉主体）

**Files:** `panel.go`

**行为：** 用设计稿的 13 视图版式替换 `panelHTML`，删除 4 页、新增图片生成页，共 10 页；
保留 11 主题 + 动效 + 侧栏 5 组导航；所有 mock 数据替换为真实 API 调用。

**Steps:**
1. 从 `console-mockup.html` 取样式层（CSS 变量 21 令牌 × 11 主题）、侧栏、card/tbl/chip 组件。
2. 保留：仪表盘 / 积分与余额 / 任务中心 / 对话调试 / 调用日志 / 用量统计 / 出站设置 / 设置 / 设计系统。
3. 删除：账号池 / 负载与切号 / 自动注册 / 临时邮箱。
4. 新增：图片生成（提示词 + 模型下拉 + 结果 URL + 扣分）。
5. 接真实 API，去掉全部 mock 数组。
6. 跑 `node --check`（抽取内联 JS）　期望：exit 0
7. Commit：`✨feat(loomy): 面板按设计稿重建（10 页 / 11 主题）`

**验收：** 浏览器打开 `127.0.0.1:11457`，10 页均可切换、无空白页、11 主题切换正常。

---

## Task 5：回归与契约冒烟

**Files:** 全部

**Steps:**
1. `go build ./... && go vet ./... && go test ./...`　期望：全绿
2. 契约冒烟：`bash scripts/plugin-smoke-test.sh`（宿主提供）　期望：ready/401/200 全通过
3. stdout 纪律检查：`./loomy-provider.exe --provider-serve` 输出只有状态行
4. Commit：`✅test(loomy): 面板重建回归与契约冒烟`

---

## 验收表

| # | 验收项 | 命令/方法 | 期望 | 实测 | 状态 |
|---|---|---|---|---|---|
| 1 | 编译 | `go build ./...` | exit 0 | exit 0 | ✅ |
| 2 | vet | `go vet ./...` | exit 0 | exit 0 | ✅ |
| 3 | 单测 | `go test ./...` | 全 PASS | 47 个 PASS | ✅ |
| 4 | 内联 JS 语法 | `node --check` | exit 0 | exit 0（36630 B） | ✅ |
| 5 | 页面数 | 导航 + section 对照 | 9 页（原计划写 10，含"账号池删除后剩 9"，以 9 为准） | 9 页一一对应 | ✅ |
| 6 | 主题数 | `html[data-palette]` 计数 | 11 套可切 | 11 套，porcelain/festival 实渲染验证 | ✅ |
| 7 | 删页确认 | 全文搜索 4 个页面名 | 均不存在 | 4/4 不存在 | ✅ |
| 8 | 图片生成 | `POST /api/image` | 返回图片 URL | 200，扣 110 分，12711ms | ✅ |
| 9 | 调用日志落盘 | 发 3 次（含流式）后查 `data/` | 字段齐全 | 3 行，含 ttfb/tokens/points/stream | ✅ |
| 10 | 积分台账 | 打开积分页 | 真实余额与记录 | 76,753 / 17,636 条 | ✅ |
| 11 | stdout 纪律 | 观察 stdout/stderr | stdout 仅状态行 | 仅 ready 行；panel_ready 走 stderr | ✅ |
| 12 | 契约冒烟 | `plugin-smoke-test.sh` | 全通过 | 7 通过 / 0 失败 | ✅ |

### 视觉验收（9 页实渲染）

Headless Edge 逐页截图 + 人工核对，9/9 通过；另验证 3 套非默认主题
（青花瓷 / 中国节 / 翠绿）配色与对比度正常。

### 过程中发现并修复的缺陷

| 缺陷 | 根因 | 修复 |
|---|---|---|
| 仪表盘余额长期「—」 | `getPointsCached` 调旧 `fetchPoints`，只带 `token` 头 → 上游 900000 | 改走 `fetchPointsPage`（三头组合） |
| 表格行/整页偶发空白 | 入场动画用 `opacity` + `fill-mode: both`，低帧率/首帧停在第 0 帧 | 改位移入场，不锁透明度 |
| 前端 `forEach` 报错 | 空切片序列化成 `null` | 统一返回 `[]` |
| 空提示词返回 502 | handler 未本地校验，错误被映射成上游故障 | 本地拦截返回 400 |

---

## 代码审查（验收前）

**审查方：** 独立子代理（非本阶段实现者）

| 审查项 | 结论 | 问题清单 |
|--------|------|----------|
| 风格 | ⬜ | |
| 测试完整性 | ⬜ | |
| 依赖与架构红线 | ⬜ | |
| 安全（session 打码、仅 127.0.0.1） | ⬜ | |
| API 契约一致性 | ⬜ | |

---

## 给接手 AI 的完整提示词

```
你在 D:\AI_Projects\_plugin-work\phase5\loomy 工作（这是 LOOMy 插件 1.8.0 的权威源码，
不要用 D:\AI_Projects\loomy2api——那是过期的 1.5.1）。

任务：按 docs/ai-framework/plans/2026-11-05-loomy-panel-rebuild.md 执行 Task 1~5。

必读：
- D:\AI_Projects\opencode2api_guide\opencode2api_enhance\docs\loomy-panel-survey-2026-11-05.md
- D:\AI_Projects\opencode2api_guide\opencode2api_enhance\docs\design\console-mockup.html
- D:\AI_Projects\opencode2api_guide\opencode2api_enhance\docs\PLUGIN-PROVIDERS.md（§5 stdout、§10 契约）

硬约束：
1. 删 4 页：账号池、负载与切号、自动注册、临时邮箱（做不到，理由见调研 §4）
2. 设置页只读，不写 provider.json
3. 保留 11 主题 + localStorage + prefers-reduced-motion
4. stdout 只打 ready/need_config/fatal；日志走 stderr
5. 上游请求必须同时带 token + Authorization: Bearer + traceparent
6. 运行数据只写 data/（PROVIDER_DIR）；零第三方依赖
7. 不 push；不覆盖已装 exe（部署等用户授权）

每 Task 完成后跑验证命令并贴真实输出，禁止「应该能过」。
```
