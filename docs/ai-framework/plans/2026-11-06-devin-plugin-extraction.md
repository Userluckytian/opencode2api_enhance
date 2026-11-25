# 阶段：Devin 插件抽离（magpie → opencode2api_enhance 侧车）

> **状态：** 计划已就绪，待开工  
> **For agentic workers:** 按 Task 顺序执行，每 Task 测完再进下一 Task。  
> **交接提示词**见文末。**元规范：** `docs/ai-framework/phased-plan-driven.md`

**Goal:** 把 magpie 的 Devin 订阅抽成 opencode2api_enhance 的侧车插件 `providers/devin/`，UI 按 `docs/design/console-mockup.html`，交付到 `D:\Program Files\opencode2api` 可即插即用。
**Architecture:** 新增 Node sidecar 插件（`--provider-serve` + OpenAI 契约）；内部移植 magpie 社区插件 `index.mjs` 的 Devin 核心（Connect RPC + Messages），外层加 Windsurf 账号池 + 聚合 failover；面板为同进程 embed HTML（`panel:sidecar`）。
**Tech Stack:** Node 20+ ESM（`index.mjs` 移植）、`provider.json`（`api_version:1`）、`devin-provider.cmd`、`src/server.js`、控制台单文件 HTML（源自 `console-mockup.html` + `_panel_css.txt`）
**实施档位：** 全能（默认）
**子代理：** 启用（代码审查 + 验收独立角色）

---

## 前置阅读（必须）

| 优先级 | 文件 |
|--------|------|
| P0 | 本文件 |
| P0 | `docs/PLUGIN-PROVIDERS.md` §四/§十（侧车契约，一次性令牌/stdout 状态行） |
| P0 | `docs/design/console-mockup.html`（UI 唯一视觉基线） |
| P1 | `AGENTS.md`、`docs/PLUGIN-CAPABILITIES.md`（P1 强烈建议：内嵌面板/账号池/网络管理） |
| P1 | 社区插件实证：`D:\AI_Projects\refs\magpie-community-plugins\packages\devin\{package.json,README.md,index.mjs}`（已Clone，0.1.37） |
| P1 | magpie 残留只读实现：`D:\AI_Projects\refs\magpie\internal\provider\{devin.go,migrate_side.go}` + `internal\agent\devin.go` |
| P1 | Windsurf 账号库 schema：`D:\AI_Projects\windsurf-account-manager\source\src-tauri\src\models.rs:6-27` |

**仓库路径：** `D:\AI_Projects\opencode2api_guide\opencode2api_enhance`
**基线分支：** 从 `main`（或 `feat/devin-plugin` 分支）拉 `feat/devin-plugin-extraction`
**三方根（只读，不改）：** magpie `D:\AI_Projects\refs\magpie@84ad780b`、社区插件 `D:\AI_Projects\refs\magpie-community-plugins\packages\devin`、目标宿主上文。

---

## Global Constraints（冲突时以本节为准）

1. **宿主契约硬约束（违反拒载）：** `stdout` 只打状态行（`ready/need_config/fatal` 行流 JSON）、`--provider-serve --port 0` + `PROVIDER_DIR/PROVIDER_CONFIG/PLUGIN_AUTH_TOKEN` 三 env、仅监听 `127.0.0.1`、运行数据只写插件目录内 `data/`、`provider.json` 读侧剥 UTF-8 BOM、密钥永不进 git。
2. **❌ 做不了（本阶段明确不做，不填假数据）：** 自动注册（CDP/Turnstile）、临时邮箱源、每日签到/钱包入账、按号独立出口、虚构模型映射（`qwork-auto→…`）。UI 上这些视图**只留入口说明或隐藏**，禁止 `toast('已成功')` 式假成功。
3. **Git：** 小步 commit，中文祈使描述，**默认不 push**。
4. **验收认证据：** 每 Task 给出实际跑过的命令+期望输出，禁止「应该能过」。

---

## 阶段开头：上阶段遗留

无（本计划为首次抽离）。

---

## 跳过项（因档位未做，非缺陷）

| 跳过项 | 原因 | 待补做 |
|--------|------|--------|
| Linux/macOS 交叉编译包 | Windows 优先，`CGO_ENABLED=0` 交叉编译延后 | ⬜ 待用户确认 |
| IDE/CLI 登录 PKCE 完整闭环 | 需本机 `devin` CLI + 浏览器回调，P1 诚实降级为「粘贴 token + CLI 文件读取」 | ⬜ 见 P1 |

---

## 与前后阶段

| 阶段 | 状态 | 交付 |
|------|------|------|
| 上游抽离盘点 | ✅ 完成 | 四方已摸清：magpie Devin 已退役内置、社区插件单文件 `index.mjs`、Windsurf 账号库本地 SQLite、设计稿 84 条基线（见 `docs/plugin-panel-redesign-gap-analysis-2026-11-05.md`） |
| **本阶段** | ⬜ | `providers/devin/` 侧车插件 + console 面板 + 部署到 `D:\Program Files\opencode2api` 的可运行闭环 |
| 下阶段 | — | 见文末「后续阶段」 |

---

## File Structure（预期变更）

| 文件 | 动作 | 职责 |
|------|------|------|
| `providers/devin/provider.json` | 新建 | 契约清单 + 私有配置（账号池源/端口/面板开关/`_hint`） |
| `providers/devin/devin-provider.cmd` | 新建 | Windows 入口：`--provider-serve --port 0 → node src/server.js` |
| `providers/devin/src/server.js` | 新建 | 契约外壳：stdout 状态行、Bearer 鉴权、`/v1/models`、`/v1/chat/completions`、面板挂载 |
| `providers/devin/src/devin-core.mjs` | 移植自 `index.mjs` | 上游协议核心：`GetChatMessage` protobuf、Claude `Messages`、gzip、reasoning/tool 转换、失败状态保留 |
| `providers/devin/src/auth.mjs` | 移植自 `index.mjs` | 登录：浏览器 PKCE（需 CLI 回调降级见 P1）/CLI 凭据文件读取/`GetUserStatus` 命名 |
| `providers/devin/src/usage.mjs` | 移植自 `index.mjs` | 配额：日/周窗、ACU、overage 余额、过期标记 |
| `providers/devin/src/models.mjs` | 移植自 `index.mjs` | 模型目录：`GetCliModelConfigs` 94 家族/变体/强度/快慢/图像支持、免费模型排除 |
| `providers/devin/src/accounts.mjs` | 新建 | Windsurf 账号池：JSONL/SQLite 导入、选号轮换、冷却/耗尽、掩码面板数据源 |
| `providers/devin/src/keepalive.mjs` | 移植自 `index.mjs` | 缓存保活 `285s/6 次`（默认关，Options 可改） |
| `providers/devin/src/panel.html` | 新建（源自 `console-mockup.html`） | 13 视图 SPA，配 `_panel_css.txt` 11 主题令牌 |
| `providers/devin/src/package.json` | 新建 | Node ESM 依赖声明 |
| `providers/devin/README.md` | 新建 | 安装/配置/安全说明 |
| `scripts/…`（或插件自带 smoke） | 新建 | 透传宿主契约冒烟 `plugin-smoke-test.sh` |

---

## 一、来源盘点：magpie Devin 功能全集（一条一条，全部进入本插件）

来源：`packages/devin/README.md`（协议说明书）+ `index.mjs:2977-3233 DevinAuthPlugin` + magpie 残留 `devin.go/migrate_side.go`。

### 1.1 账号与登录

| # | magpie/插件功能 | 落地（本插件） | UI 视图 |
|---|-----------------|-----------------|---------|
| A1 | 账号 = `devin-session-token$…`（CLI 会话 token），即 `windsurf_api_key` | 私有配置必填 `accounts_jsonl` / `windsurf_db` 粘贴一键导入 | 账号池：新增/导入抽屉 |
| A2 | 双登录：浏览器 `app.devin.ai/auth/cli/continue` PKCE → `server.codeium.com` 兑换；CLI 凭据文件 `%APPDATA%\devin\credentials.toml` 只读 | 见 P1 降级：本阶段只做「粘贴 token」+「读 CLI 文件」；PKCE 需`devin auth login` 真实回调时全开 | 账号池：新增账号 |
| A3 | 命名：`GetUserStatus` 取邮箱/组织/tier；失败则问 `devin auth status`（隔离目录 `~/.cache/opencode-devin-auth/<hash>/`，勿动用户 CLI 登录） | `health: /v1/usage` + 面板账号行「名称/套餐」 | 账号池行 + 积分 KPI |
| A4 | 存档形状 `{type:"api",key,metadata:{email,plan,orgId,teamId}}`，Devin 不过期、无 refresh；401 标过期「请重新登录」，干净读回销标记 | `data/accounts.json` 同形；401 标 `signedOut` | 账号池：状态徽章 |
| A5 | mover 搬运字段 `key + metadata{email,plan,server,cli}` | 账号池与 Windsurf JSONL 字段做映射表（`email/key/plan/server`） | 账号池导入 |

### 1.2 请求（chat / messages）

| # | 功能 | 落地 | UI |
|---|------|------|----|
| R1 | `POST https://server.codeium.com/exa.api_server_pb.ApiServerService/GetChatMessage`（Connect RPC + `Authorization: Basic <token>`） | `devin-core` 主流 | 对话调试（经网关） |
| R2 | OpenAI Chat → CLI 请求：system 拼首条 user、工具描述独段 `<tool_descriptions>`、未答工具调用自动补「Tool use was interrupted…」、图片必须 `data:` URL、工具返回图随结果走 | 映射层完整移植 | 对话调试：多轮/流式开关(render) |
| R3 | Claude 模型走 Anthropic `Messages`（`POST …/devin/v1/messages`）；其余走 Chat Completions；块边界+c缓存标记逐块、system/notice 保留历史、去 `x-anthropic-billing-header`、签名透传 | 分流函数移植 | 对话调试：命中模型行 |
| R4 | >8KiB gzip 发送；解析流式 `text/reasoning/tool_calls/usage`；失败状态保留（401/429/400）；仅元数据+usage 无实际内容按 502（等实际内容才开成功流，供网关换通道） | 移植 | 日志：状态列 |
| R5 | 失败工具结果文本以「Error: 」开头 | 移植 | 日志：失败行 |

### 1.3 模型与图像

| # | 功能 | 落地 | UI |
|---|------|------|----|
| M1 | 家族列表：94 例（`swe-2/claude-opus-5-5/gpt-6-sol…`），变体按强度（`swe-2-high/xhigh`、fast/priority=`-fast`），无强度独立列（`glm-5-2-1m`），Adaptive/Fusion 不列 | `models.mjs` + 启动声明列表 | 设置：模型暴露白名单 |
| M2 | 登录后 `provider.models` 以 `GetCliModelConfigs` 实时替换（`devin models list` 等价），失败回退 CLI → 声明列表；缓存 5 分钟、失败 1 分钟重试 | 5 分钟/1 分钟缓存 | 仪表盘：模型数 KPI |
| M3 | 图片支持按模型（`supports_images`）：`swe-2/swe-1.7` 等支持，GLM/DeepSeek V4 等不支持；未知模型走 `models.dev` | 缓存一次 | 对话调试提示行 |

### 1.4 用量与配额

| # | 功能 | 落地 | UI |
|---|------|------|----|
| U1 | `GetUserStatus`：plan 名/到期、日/周配额剩余+重置（0% 空窗为耗尽）、ACU 已用/上限、extra usage 余额（`$`）、Free 模型不计日/周窗 | `/v1/usage/*` | 积分与余额 KPI + 账号行余额条 |
| U2 | 401 标过期、其余失败保留标记状态；面板「Sign in again」入口 | 状态机 | 账号池：批量刷新积分 |

### 1.5 缓存保活（默认关）

| # | 功能 | 落地 | UI |
|---|------|------|----|
| K1 | 回复后同请求 `continue + max_tokens 1` 每 285s 重发，最多 6 次（可配 Interval/MaxPings），首达/ping 间隔按请求发送时刻计；新真实请求停旧保活；401/402/429、两读缓存 <50%、达次、开关关时停 | 开关 + 记用量的 Cache keep-alive 调用 | 设置：缓存保活开关 |
| K2 | 保活归因需会话键：magpie 靠 `x-magpie-session/subagent`；本宿主改用网关自带 session 头（无则用首消息哈希兜底） | 适配 | 日志：Cache keep-alive 行 |

---

## 二、UI 映射：设计稿逐条 → 功能来源（供逐条实现）

大句：13 视图 × 84 基线（`console-mockup.html` L654-1650）。每条注明来源：`已有` / `缺` / `❌`，逐条做。

| 视图 | 主要条目（84 中的分量） | 本插件落点（一句） |
|------|--------------------------|---------------------|
| 全局框架 6 | 导航、徽标、面包屑、11 色板、动效总开关、刷新 | `panel.html` 头栏 + `_panel_css` 令牌，静态 |
| ① 仪表盘 7 | 在线徽标、探测出口、KPI 4、24h 图、P95/TTFB/并发/切号、池健康卡、最近 8 表 | 后端给：连接状态、账号数、用量聚合；P95 等从网关日志聚合补 |
| ② 账号池 11 | 新增 4 源+绑定出口、导入、导出、状态筛、搜索、多选、批量、10 列账号表、行详情、空态、落地说明 | 后端给：`accounts.mjs`；绑定出口/批量签到 ❌（见约束2） |
| ③ 积分余额 4 | 同步、KPI 3、进度卡、逐账号余额表 | 后端给 U1-U2；待入账/结算无源 → 不画 |
| ④ 签到任务 6 | 重排、进度、下次执行、收益、任务清单、执行记录 | ❌（Devin 无签到/任务体系）→ 该视图整体隐藏，留一行说明 |
| ⑤ 对话调试 8 | 网关/直连双切换、清空、流式/多轮、气泡、输入区、打码提示、命中卡、失败轨迹 | 后端给 R1-R5（经网关）；直连=本插件进程调上游（P2 可选） |
| ⑥ 调用日志 4 | 导出 CSV/清空、四筛、总量、12 列明细 | 网关已有口；插件侧补 `data/requests-*.jsonl` |
| ⑦ 用量统计 4 | 7/30 天、趋势图、模型占比、按账号聚合表 | 后端给 U1 聚合 |
| ⑧ 负载切号 7 | 保存策略、四选一、错误转移勾选、轮数/冷却、粘性阈值、取号水位、切号时间线 | 后端给本插件进程内选号（粘性/轮询/加权/最少在途四选一）；切号时间线需新增 `data/events.jsonl` |
| ⑨ 链路出口 6 | 链路抽屉、探测、Clash 未配置横幅、节点延迟、当前出口、按号出线 | 探测/节点表可由探测地址走外控；按号出线 ❌（无 per-account 出口） → 该子表隐藏 |
| ⑩ 自动注册 7 | 注册队列、通道、夜间窗口、注册出口、进度、日志、历史 | ❌ 整体不做 |
| ⑪ 临时邮箱 4 | 测试/生成、源优先表、最近收件、生成规则 | ❌ 不做 |
| ⑫ 设置 5 | 保存、上游接入、数据目录、面板外观、危险操作 | 后端给：Base URL/代理/超时/重试/测试连通；目录占用补 |
| ⑬ 设计系统 5 | 主题卡片、令牌、按钮动效、语义色、动效三开关 | `panel.html` 静态（11 主题同步顶栏） |

---

## 三、安装与部署坐标（用户原话四条）

1. **宿主项目：** `D:\AI_Projects\opencode2api_guide\opencode2api_enhance`（唯一开发地点）。
2. **UI 样子：** `docs/design/console-mockup.html`（全量 13 视图，§二逐条映射）。
3. **抽离清单：** §一 A1-A5 / R1-R5 / M1-M3 / U1-U2 / K1-K2 逐条移植，§二每条落视图。
4. **宿主安装地：** `D:\Program Files\opencode2api`（供应商目录按官方契约 `providers/<id>/`；当前实际部署在 `bin\providers\`，以 `--provider-service` 默认 `127.0.0.1:50080` 的扫描路径为准，部署时双核实）。

---

## 四、Task 拆分（阶段计划驱动）

### Task 1 — 契约脚手架 + 冒烟（S，最先做）

**Goal:** 空插件「插件式供应商」标签 3 秒内自动发现、面板见「待配置」。
- [ ] 建 `providers/devin/{provider.json,devin-provider.cmd,src/server.js,src/package.json,README.md}`
- [ ] `server.js`：env 三件套、stdout 状态行、`need_config→ready` 补打、仅 `127.0.0.1`、3s 轮询重载
- [ ] `provider.json` 模板：`id=devin/name/version/api_version=1/entry=devin-provider.cmd/provider_private_configs{_hint,accounts_jsonl,port,panel}`

**验证：**
```powershell
Copy-Item -Recurse providers/devin D:\ProgramFiles\opencode2api\bin\providers\
bash scripts/plugin-smoke-test.sh devin-provider.exe provider.json
```
期望：`/v1/models` 无令牌 401；带令牌 200（503=账号未就绪为警告级）；`panel` 可达。

### Task 2 — 上游协议移植：chat + messages + 模型（M）

**Goal:** 一个账号跑通 `POST /v1/chat/completions`（含 SSE）与 `/v1/models`（94 家族）。
- [ ] 移植 R1-R5 + M1-M3 到 `devin-core/models.mjs`（protobuf、缓存标记、签名、gzip、502 等实际内容门槛）
- [ ] 连接 `GetCliModelConfigs` 失败回退声明列表（状态标备用）

**验证：** `smoke` + 本地 `curl` 经网关分别发 chat 与 messages（Claude 模型）各一轮，期望 200 且工具调用/推理透出。

### Task 3 — 账号池 + failover（M）

**Goal:** Windsurf 账号多号轮换跑通切号。
- [ ] `accounts.mjs`：Windsurf JSONL 导入（`email/user_id/org_id/auth1_token/api_key/windsurf_session_token`）、`data/accounts.json` 唯一源
- [ ] 选号：粘性/轮询/加权/最少在途四选一；401/402/429/5xx 触发换号、单号冷却；`data/events.jsonl` 落切号轨迹

**验证：** 双账号一号 401、一号正常，期望自动切到正常号且事件可查。

### Task 4 — 用量 + 日志 + 保活（M）

**Goal:** 用量/日志/保活真实可查。
- [ ] U1-U2：`data/usage-*.json` 按日落盘；面板数据源
- [ ] `data/requests-*.jsonl`（请求级日志） + 7/30 天聚合
- [ ] K1-K2：缓存保活（默认关，285s/6 次可配），记 Cache keep-alive 调用

**验证：** 一轮对话后用量/日志新增一条，保活开后 5 分钟内可见 ping 调用。

### Task 5 — Console 全量 UI（S→M）

**Goal:** 13 视图可达，❌ 四项隐藏/说明，不填假数。
- [ ] `panel.html` 以 `console-mockup.html` 为底，§二逐条对齐；11 主题令牌、动效、抽屉、Toast
- [ ] 真数据接：仪表盘/账号池/积分余额/日志/用量/切号/设置；签到/自动注册/临时邮箱/按号出线四子表按约束2处理

**验证：** 浏览器 13 视图逐条走测（期望：真数据实时刷新，无 `toast('已成功')` 假回调）。

### Task 6 — 部署到 `D:\Program Files\opencode2api` + 全链路联调（S）

**Goal:** 安装目录即开即用。
- [ ] `Copy-Item providers/devin → bin\providers\devin`（并核实 `--provider-service` 扫描路径）
- [ ] 网关跑一轮 `devin/<model>` 对话 + 全部 Task 验收表复核

---

## 五、验收表

| 验收项 | 证据命令 | 期望 |
|--------|----------|------|
| 契约冒烟 | `bash scripts/plugin-smoke-test.sh …` | 退出码 0；面板见运行中+模型数>0 |
| 网关 chat | 宿主 `/v1/chat/completions` 发 `devin/*` | 200，流式 SSE 正常，日志新增 |
| 多号 failover | 一号 401 | 自动换号+事件落 `data/events.jsonl` |
| 用量 | 触发一次配额读 | `data/usage-*.json` 更新，积分 KPI 变化 |
| ❌ 项 | 浏览签到/自动注册/临时邮箱/按号出线 | 不出现假成功/假数字 |
| 全量回归 | `go test -count=1 ./...`（宿主） | 全绿 |
| 部署 | `D:\Program Files\opencode2api` 面板 | 无需重启，3 秒内发现 |

---

## 六、风险与未知

| 风险 | 应对 |
|------|------|
| Windsurf 哪个字段 = `windsurf_api_key` 未证 | Task 3 前拿你机器一行脱敏样本核对，未证前不建导入映射 |
| PKCE 浏览器回调需 `devin` CLI | 本阶段只做粘贴 token + CLI 文件读取（§P1） |
| Claude 签名/缓存细节 | 跨同源 `index.mjs` 逐行移植（含 `field 8 EPHEMERAL`、`signature/delta_signature`） |
| per-account 出口/签到 | 约束2明示不做，UI 隐藏不造假 |

---

## 七、后续阶段

- **P1：** 打开 PKCE 完整登录 + CLI 更新通道（需真实环境验证）。
- **P2：** 按号出口和多出站如 Clash `sync` 扩展等附议业余功能（需独立兼容性评审）。

---

## 给接手 AI 的完整提示词（零上下文）

> 在 `D:\AI_Projects\opencode2api_guide\opencode2api_enhance` 仓库执行本计划 `docs/ai-framework/plans/2026-11-06-devin-plugin-extraction.md`，从 Task 1 开始。只读核查：`docs/PLUGIN-PROVIDERS.md`、`docs/design/console-mockup.html`、`D:\AI_Projects\refs\magpie-community-plugins\packages\devin\index.mjs`（0.1.37）。新建 `providers/devin/`（provider.json/devin-provider.cmd/src/server.js…），契约见计划。明确不做：自动注册/临时邮箱/签到入账/按号出口/虚构映射（见策略2）。每 Task 报实际命令+期望输出，绿了再下一个；部署路径 `D:\Program Files\opencode2api`。Git 中文祈使默认不 push；密钥不进 git。
