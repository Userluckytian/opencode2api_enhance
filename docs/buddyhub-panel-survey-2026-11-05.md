# BuddyHub 面板改版前调研：现状功能 / 可扩 / 不可为

> 日期：2026-11-05 · 状态：**已调研，待用户拍板**
> 输入：设计稿 `docs/design/console-mockup.html`（2572 行 / 13 视图 / 11 主题）
> 现状基线：`D:\Program Files\opencode2api\bin\providers\buddyhub\`（v1.4.0 已装 exe）
> 源码基线：`D:\AI_Projects\buddyhub`（插件入口 `cmd/pluginbuddy` + `internal/pluginhub`）
> 本阶段**只读**，未改任何源码、未杀进程、未动 `data/`

---

## 0. 关键事实：源码与已装 exe 不是同一份

| 项 | 源码 `D:\AI_Projects\buddyhub` | 工作副本 `_plugin-work/phase5/buddyhub` | 已装 exe |
|---|---|---|---|
| panel.html | 123 行，纯 tab，深墨+荧光绿 | 580 行，侧栏 + 暖白抹茶 | **= phase5 版**（已逐字核对命中） |
| 事件页 `/api/events` | 无 | 有（`eventlog.go`） | 有 |
| 版本号 | `main.go` = `"dev"`，provider.json = 1.0.0 | 1.4.0 | 1.4.0 |

**结论：改面板要动 `_plugin-work/phase5/buddyhub`（或先把它并回源码目录），不要在 `D:\AI_Projects\buddyhub` 上改**，否则改动上不了机。
核对方法：把 panel.html 的 `__PLUGIN_AUTH_TOKEN__`/`__PLUGIN_VERSION__` 当通配符正则匹配 exe 内的 HTML 段 —— phase5 版命中，源码版不命中。

---

## 1. BuddyHub 现有能力（= 面板真能做的）

### 1.1 管理 API（12 个端点，全部 Bearer 鉴权）

| 端点 | 对应设计稿视图 | 备注 |
|---|---|---|
| GET `/api/accounts` | 账号池 | uid/昵称/区/状态/积分 + 手动停用位 |
| POST `/api/accounts/{uid}/toggle` | 账号池 | 手动摘流量（`manual_disabled`，独立于系统 disabled） |
| POST `/api/accounts/{uid}/revive` | 账号池 | 解系统禁用 |
| DELETE `/api/accounts/{uid}` | 账号池 | 删凭证文件 + 重新 SyncToDir |
| GET `/api/tasks` | 签到任务 | 7 个任务 + 今日状态 + 下次时点 |
| POST `/api/tasks/{key}/run` | 签到任务 | 手动触发（白名单 7 个 key） |
| GET `/api/tasks/logs` | 签到任务 | 环形 50 条运行日志 |
| GET `/api/calls` | 调用日志 | 最近 100 条逐次记录 |
| GET `/api/usage` | 仪表盘 / 用量统计 | 总量 + 今日 + 按账号 |
| POST `/api/credits/refresh` | 积分与余额 | 并发 3 查全部余额 |
| POST `/api/oauth/start` + GET `/api/oauth/poll` | 账号池（新增号） | 浏览器 device_code 扫码登录，cn/global |
| GET `/api/events` | 事件 | `data/events.jsonl` 最近 N 条 |

### 1.2 面板已有 6 个视图

总览 / 账号池 / 每日任务 / 调用记录 / 对话调试 / 事件。

### 1.3 引擎侧已有、但面板没显示的能力（**最容易拿的增项**）

| 能力 | 数据在哪 | 说明 |
|---|---|---|
| 7 个任务定义 | `taskDefs` | 签到/旅行/活跃/保活/夜猫/连登/开学季，已有 desc 但面板没展示 desc |
| 每账号今日调用数 | `Status.today_calls` | 面板没取 |
| 每账号在途请求 | `Status.in_flight` | 面板没取 |
| 熔断/降权状态 | `breaker_fails`/`breaker_until`/`degrade_until`/`consecutive_fails` | 面板只显示"冷却"一档 |
| 模型级限流台账 | `rate_limited_models[]`（含模型名 + 重置时刻） | 面板完全没显示 |
| 每模型实测成本台账 | `model_costs[]`（per_1k / samples / last_seen） | 面板完全没显示 |
| 累计成功/失败 | `success_count` / `err_total` | 面板没取 |
| 最近成功/失败时刻 | `last_success` / `last_err` | 面板没取 |
| 逐模型聚合统计 | `GET /v1/stats`（TTFB / tokens/s / 缓存命中 / 每请求成本） | 网关端点已存在，面板没调 |
| 全局健康 | `GET /status`（healthy/cooling/disabled + realm_totals + sticky_sessions） | 面板没调 |
| usage-log 全量 | `data/usage-log.json`（封顶 5000） | 面板只取 100 条 |
| 任务执行摘要 | `TaskRunState.summary` / `duration` | 面板只显示状态词 |
| 事件 meta 全字段 | events.jsonl 的 `meta` | 面板只 raw 打印 |

### 1.4 配置层（provider.json → 3 秒热重载）

`cn_on/global_on/default_realm/rotation_mode/cost_first/daily_limit`、`sticky_on/sticky_max_uses`、
7 组任务开关 + 时点、`prompt_mode`、`user_agent`、`client_name`。

**注意：这些是宿主配置，面板目前只读不写。** 写有两条路（见 §3.2）。

---

## 2. 可以扩充（设计稿有 / 现在没有 / 技术上做得到）

分四档，按"投入小且立刻有用"排序。

### A 档：纯前端，后端数据已就绪，1~2 天

| # | 设计稿视图 | 要做的事 | 后端现状 |
|---|---|---|---|
| A1 | 仪表盘 KPI + 池健康度环 | 补 healthy/cooling/disabled 三档计数、按账号积分分布 | `CountsDetailed` / `List()` 已有 |
| A2 | 账号池表格补列 | 加 今日调用 / 在途 / 成功累计 / 最近成功 / 限流模型 / 成本台账 | `Status` 全字段已有，只需在 `accountItem` 透出 |
| A3 | 账号池筛选 + 搜索 | 前端过滤（all/可用/冷却/禁用 + 关键字） | 前端即可 |
| A4 | 任务清单补摘要 | 显示 `summary`（"12 号：成功 10/已签 2"）、耗时、成功率 | `SnapshotAll` 已有 |
| A5 | 调用日志补列 + 筛选 | 加 首包 TTFB / 扣积分 / 裸模型名 / 账号区；按状态/模型筛选 | `CallRecord` 有 ttfbMs/credit/bare，只是 `callEntry` 没透出 |
| A6 | 用量统计页 | 按天趋势 + 按模型占比 + 按账号表 | `/v1/stats`（metrics）+ `UsageStat.ByAccount` |
| A7 | 事件页结构化 | 按 type/level 筛选、按账号过滤、可读化 meta | `readEvents` 已返回结构 |
| A8 | 侧栏底部状态灯 | 网关连不上/未就绪时变红 | 前端心跳 `/api/usage` 即可 |

### B 档：需补 1 个只读 API，3~5 天

| # | 设计稿视图 | 要做的事 | 缺口 |
|---|---|---|---|
| B1 | 负载与切号（只读部分） | 展示 rotation_mode / cost_first / sticky / daily_limit / 单号并发上限；最近切号轨迹 | 配置值需 `/api/config` 只读暴露；切号事件目前没落 events |
| B2 | 积分与余额 | 逐账号 剩余/即将过期/同步时刻 + 池总量/预估可用天数 | `creditsExpiring` 未透出；`CycleEndTime` 未透出；只有 `credits` 一个数 |
| B3 | 账号池 → 新增账号抽屉 | 把 OAuth 登录嵌进抽屉（现在只有两个按钮）；显示扫码/等待状态 | 已有 oauth start/poll，缺"未完成流程"列表与取消 |
| B4 | 对话调试增强 | 多轮历史（现在有 slice(-12)）、流式开关、显示"请求模型→真实模型"、命中账号/扣分/耗时 | 真实模型在 SSE 末帧 `model` 里，可解析；命中账号需从 `/api/calls` 回查 |
| B5 | 设置页（只读） | 展示数据目录、文件占用、auths 数量、插件版本、端点 | 需加 `/api/settings`（读 provider_private_configs + 目录 stat） |
| B6 | 每日任务 → 时点编辑 | 面板改 `checkin_hours` 等 | 见 §3.2 写配置路径 |
| B7 | 账号池 → 批量操作 | 多选 + 批量停用/启用/刷新积分/签到 | 需补 `POST /api/accounts/batch`（或前端串行调用现有单号端点，先做串行更省事） |

### C 档：需改引擎行为，1~2 周，有真实价值但风险更高

| # | 设计稿视图 | 要做的事 | 难点 |
|---|---|---|---|
| C1 | 负载与切号 → 错误转移规则 | 401/402/429/5xx 各自开关、最大换号轮数、冷却秒数 | `MaxRotate`/`SoftCooldown` 是 `Handler.Config` 一次性注入，需做成可热改（atomic 或 Config 重载） |
| C2 | 负载与切号 → 最少在途 / 粘性单选 | 新增两种选号策略 | `pool.pick` 只有 weighted / round-robin，需新增策略分支 + 权重测试 |
| C3 | 积分与余额 → 同步进度条 | 逐账号进度（成功/失败/待处理/耗时）实时推进 | 现在 `refreshCredits` 同步阻塞返回，需改异步任务 + 进度查询端点 |
| C4 | 链路与出口（对话侧） | Clash/mihomo 探测 + 切换 + 按号出线 | 引擎**完全没有代理支持**（无 `ProxyFromEnvironment`、无 dialer 代理），要做等于新增一层 Transport |
| C5 | 自动注册 / 临时邮箱 | 浏览器注册 + 收码 | BuddyHub 是 **OAuth 扫码**入池，没有注册接口；见 §4 |

### D 档：纯视觉（设计稿的核心资产，但零后端）

| # | 项 | 说明 |
|---|---|---|
| D1 | 11 套主题（抹茶/奶绿/茉莉/青花瓷/马卡龙/竹青/翠绿/灵动/暖阳/中国节/祥瑞） | 纯 CSS 变量，`data-palette` 切换 + localStorage |
| D2 | 动效 6 件套（磁吸/青花/波纹/果冻/流光/叶形） + 环境画布 | 零依赖手写，`prefers-reduced-motion` 降级 |
| D3 | 侧栏 + 分组导航 | 现面板已是侧栏，需按设计稿分成 5 组 |
| D4 | KPI 数字滚动 / 图表描线 / 表格行入场 | 纯前端 |
| D5 | 抽屉（drawer）+ toast + 骨架屏 | 纯前端 |
| D6 | 设计系统页（主题全览 + 令牌 + 动效对照） | 纯前端，可保留为隐藏页 |

---

## 3. 硬约束（必须知道的三件事）

### 3.1 面板是"内嵌 HTML + 一次性令牌"，不是 SPA 工程

`panel.go` 用 `go:embed panel.html`，serve 时把 `__PLUGIN_AUTH_TOKEN__` 替换成宿主令牌。
所以：**设计稿必须是单个自包含 HTML**（内联 CSS/JS、零外部 CDN）。
设计稿正好满足（零外部依赖），可直接移植 —— 但要**保留两处占位符**，否则面板全部 401。

### 3.2 配置写入只有一条合法路径

面板不能直接写 `provider.json`（会和宿主 3 秒热重载打架，且面板不知道宿主端口/密钥）。
合法路径：`POST http://127.0.0.1:<宿主端口>/api/admin/plugins/buddyhub/config`
（body `{"provider_json":"<全文>"}`，宿主原子写盘 + 触发重载）。

**但**：宿主无 CORS 头，面板在浏览器里跨源 POST 会被拦。三条可选方案：

| 方案 | 做法 | 代价 |
|---|---|---|
| ① 插件自写（推荐） | 面板 POST 到插件自己的 `/api/config`，插件读-合并-写 provider.json | 插件需知道自己的 manifest 路径（`PROVIDER_CONFIG` 环境变量已有）；纯本地，无 CORS 问题 |
| ② 宿主代写 | 面板经 `fetch` 直连宿主 admin 端点 | 需宿主加 CORS 或改用表单/代理；**要改宿主** |
| ③ 只读 | 面板展示配置，改了去宿主界面改 | 零风险但"设置页"名存实亡 |

### 3.3 数据目录与多实例

`data/state.json` 由**同一插件的多个子进程共享**（5s auths 热加载 + 内存池各自持有）。
面板读到的 `credits`/`cooling` 可能滞后于另一个进程的写入。**改版不要承诺"实时一致"**，
文案按"最近一次同步"表述，与设计稿 `上次同步 14:22` 一致。

---

## 4. 确实做不到 / 不该做的（逐项说明理由）

| 设计稿项 | 判定 | 理由 |
|---|---|---|
| **自动注册**（浏览器真实渲染 + Turnstile + 夜间窗口 + 注册队列） | ❌ 做不到 | BuddyHub 的入池方式是 **OAuth device_code 扫码**（`copilot.tencent.com` / `codebuddy.ai`），上游没有"邮箱+密码注册"接口。要自动注册等于逆向 CodeBuddy 注册页 + 过人机验证，与本插件无关，且极高风险 |
| **临时邮箱**（多源 + 自动取码） | ❌ 没有对象 | 同上：登录链路不经过邮箱验证码，面板上放邮箱源是空壳 |
| **按号出线 / 每号独立端口 18301** | ❌ 做不到 | 引擎 Transport 是单一共享实例，无 per-account proxy；也无监听多端口的架构。真要做需重写出站层 |
| **链路与出口：Clash 探测 + 自动切节点** | ⚠️ 能做但与 BuddyHub 语义不符 | 探测需接 mihomo external-controller，切换会**改变本机其它应用的出网**。设计稿自己也写了"自动切换默认关闭"——建议**整页不做**，或只保留"当前出站是否走得通"一行（需先加代理支持） |
| **对话日志 → 缓存命中列** | ⚠️ 只有逐次调用没有 | `CallRecord` 没存 cache 三段（只在 metrics 聚合里），要加字段 + 落盘 |
| **P95 延迟 / 并发峰值** | ⚠️ 需新增直方图 | 现在只有 sum/count 算均值，没有分位数；要么加采样环，要么先显示均值 |
| **预估可用天数 / 结算状态 / 待入账** | ⚠️ 部分 | 可用天数可算（余额 ÷ 近 3 日均耗）；"待入账/结算"上游不提供，只能是"—" |
| **租户/分组/标签（A7）** | ❌ 上游无 | 账号只有 realm 两档，没有分组概念 |
| **账号密码重登（A6）** | ❌ 无对象 | OAuth 只有 refresh_token（保活任务已在做），没有密码 |
| **导出 CSV / 导入 CSV** | ⚠️ 导入可做、导出要慎重 | 导入可复用桌面版 `importer.go`（读本地 CodeBuddy 明文/加密存储）；导出凭据属敏感操作，建议只导"脱敏清单" |
| **危险操作：删除全部账号** | ⚠️ 建议不做 | 一行误点等于清空号池；保留单号删除 + 确认即可 |
| **"全部服务在线"这类总健康 chip** | ✅ 可做 | 用 `/api/usage` 可达性 + 账号池计数推导，不必新端点 |

---

## 5. 建议的落地顺序（未开工，等拍板）

1. **第一步（纯视觉）**：把设计稿的 11 主题 + 侧栏 + 卡片/表格/tokens 移植进 `panel.html`，保留现有 6 个视图与全部 JS 逻辑。零后端风险，先看到效果。
2. **第二步（A 档）**：补 `/api/accounts` 与 `/api/calls` 的透出字段，把账号池/调用日志两张表做满。
3. **第三步（B 档）**：加 `/api/config`（读）+ `/api/metrics`（包一层 `/v1/stats`）+ 设置页只读。
4. **第四步（B4/B2）**：对话调试显示真实模型/命中账号；余额页补"即将过期"。
5. **暂不做**：C4 链路出口、C5 自动注册/临时邮箱（§4 已说明是空壳）。

---

## 6. 待用户确认

1. 改面板的代码基准选哪个？`_plugin-work/phase5/buddyhub`（= 已装 exe）还是先把 phase5 并回 `D:\AI_Projects\buddyhub`？
2. 设计稿 13 个视图，砍掉「自动注册 / 临时邮箱 / 链路与出口」三页后剩 10 个，是否接受？
3. 设置页要"可写"（走 §3.2 方案① 插件自写）还是"只读"（去宿主改）？
4. 是否保留设计稿的 11 主题切换（有 localStorage 记忆）？

**状态：待处理**（调研完成，等以上 4 项决策后再开阶段计划）
