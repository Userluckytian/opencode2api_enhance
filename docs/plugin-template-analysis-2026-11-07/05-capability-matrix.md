# 05 · 视图 × 插件能力矩阵（13 插件 × 13 视图）

> 日期：2026-11-07 · 性质：只读分析，未改任何源码
> 视图定义出处：`docs/design/console-mockup.html` 导航区（667-689 行，`data-view` 13 项）：仪表盘 dash / 账号池 accounts / 积分与余额 credits / 签到任务 tasks / 对话调试 chat / 调用日志 logs / 用量统计 usage / 负载与切号 routing / 链路与出口 egress / 自动注册 autoreg / 临时邮箱 mail / 设置 settings / 设计系统 design。
> 插件清单：buddyhub / devin / flonno2api / grok / lobsterai / lobsterhub / loomy / mmxproxy / qw2api / runbiosproxy / skywork / vibex / xhx（源码路径与版本偏差见同目录 `04-quota-views.md` §0，全部源码目录本次实际验证存在）。

## 0. 判级口径（用户已定基调 + 能力裁剪规则）

| 判级 | 含义 |
|---|---|
| **必** | 用户基调指定必须，或该插件真实能力直接支撑、砍掉即残缺 |
| **可** | 统一维护、按插件能力裁剪：能力真实存在但深度不一，或需精简/降级承载 |
| **N/A** | 上游/插件语义下无对象，做了就是空壳 |
| **待确认** | 证据不足以定级 |

基调（用户给定）：**设计系统+设置所有插件必有；仪表盘/对话调试/调用日志/用量统计必须**；账号池、签到任务、负载与切号、自动注册、临时邮箱属「统一维护、按插件能力裁剪」。

签到任务统一时点模型：**凌晨 1 点与 23 点各全量执行一遍、可配置**——现网原型即 buddyhub：`slot_hours` 默认 `[1,23]`、`slot_jitter_min` 每档随机抖动、**每个档位触发全部任务**（`phase5/buddyhub/internal/config/schedule.go:24-29,54`；部署 provider.json `_hint`「每个档位触发全部任务，其余时间不动」）。其余签到类插件（lobsterai/lobsterhub/mmxproxy/loomy）目前是各自循环，统一后应挂到该档位调度器。

---

## 1. 总矩阵（必 / 可 / N/A）

| 视图 \ 插件 | buddyhub | devin | flonno | grok | lobsterai | lobsterhub | loomy | mmx | qw2api | runbios | skywork | vibex | xhx |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 仪表盘 | 必 | 必 | 必 | 必 | 必 | 必 | 必 | 必 | 必 | 必 | 必 | 必 | 必 |
| 账号池 | 必 | 必 | 必 | 必 | 必 | 必 | N/A | 必 | 必 | 必 | 必 | N/A | 必 |
| 积分与余额 | 必 | 可 | 可 | 必 | 必 | 必 | 必 | 必 | 必 | 必 | 必 | N/A | 必 |
| 签到任务 | 必 | N/A | N/A | N/A | 必 | 必 | 可 | 必 | N/A | N/A | N/A | N/A | N/A |
| 对话调试 | 必 | 必 | 必 | 必 | 必 | 必 | 必 | 必 | 必 | 必 | 必 | 必 | 必 |
| 调用日志 | 必 | 必 | 必 | 必 | 必 | 必 | 必 | 必 | 必 | 必 | 必 | 必 | 必 |
| 用量统计 | 必 | 必 | 必 | 必 | 必 | 必 | 必 | 必 | 必 | 必 | 必 | 必 | 必 |
| 负载与切号 | 必 | 必 | 必 | 必 | 可 | 可 | N/A | 必 | 必 | 必 | 必 | N/A | 必 |
| 链路与出口 | N/A | 必 | 可 | 必 | 可 | 可 | 可 | 必 | 必 | 可 | 必 | 可 | N/A |
| 自动注册 | N/A | 可 | 必 | 必 | N/A | N/A | N/A | 必 | 必 | 必 | 必 | N/A | 可 |
| 临时邮箱 | N/A | 可 | 必 | 必 | N/A | N/A | N/A | 必 | 必 | 可 | 必 | N/A | N/A |
| 设置 | 必 | 必 | 必 | 必 | 必 | 必 | 必 | 必 | 必 | 必 | 必 | 必 | 必 |
| 设计系统 | 必 | 必 | 必 | 必 | 必 | 必 | 必 | 必 | 必 | 必 | 必 | 必 | 必 |

计数（169 格 = 13×13，逐行核对）：必 128 / 可 15 / N/A 26 / 待确认 0（均为本会话 grep/读取定级，个别深度保留项在下列分视图说明中标注「推测/待确认」）。

---

## 2. 分视图理由（逐格）

### 2.1 仪表盘（必 ×13）
全部 13 家都有池计数或单号状态 + 调用计数，KPI 卡必有数据源。深度差异只在卡片数：多池插件可放池健康度/今日消耗/低余额三卡（buddyhub `CountsDetailed` 已有，survey A1）；loomy/vibex 单号版退化为「余额/今日调用/登录态」卡（loomy `/api/status` 已返回 points+claim：survey §1.2）。

### 2.2 账号池
| 插件 | 判级 | 理由 |
|---|---|---|
| buddyhub | 必 | 多账号池（auth_dir workbuddy-*.json）+ `/api/accounts` 已含 uid/昵称/区/状态/积分（survey §1.1） |
| devin | 必 | accounts.json 号池 + strategy 三策略（部署 provider.json `_hint`） |
| flonno | 必 | data/pool.json 账号数组 + 面板注册/导入（provider.json `_hint`） |
| grok | 必 | accounts_dir CPA 账号 + 外壳 Account 列表（admin.go:194-211） |
| lobsterai | 必 | 池 + `Pick()`/冷却状态机（internal/pool/pool.go:617,707-716） |
| lobsterhub | 必 | 多账号（与桌面版共享目录），FetchQuota 按号查（client.go:149） |
| loomy | N/A | 一人一 session 无多账号体系，日上限 1000 分固定，多号无收益（survey §4 判 ❌） |
| mmx | 必 | accounts.json 池 + BalanceInfo 快照落盘（store.go:50-84） |
| qw2api | 必 | 池 + credits 三字段（pool.go:37-89） |
| runbios | 必 | data/pool.json + 余额快照 + 批量停用/低余额停用（provider.json `_hint`、admin.go:700） |
| skywork | 必 | 同 qw2api 族池结构（pool.go:37-89） |
| vibex | N/A | 单 cookie 账号，无池对象（provider.json `_hint`；部署名自述「号已封」） |
| xhx | 必 | data/accounts/<uid>/auth.json 多账号 + 池（provider.json `_hint`、pool.go） |

### 2.3 积分与余额
| 插件 | 判级 | 理由 |
|---|---|---|
| buddyhub | 必 | `/api/credits/refresh` 并发查全部余额 + 分桶（credits.go） |
| devin | 可 | 无绝对积分，仅 daily/weekly 百分比窗口（devin-core.mjs:3004-3037）——页面降级为「窗口用量」卡 |
| flonno | 可 | Balance 仅注册快照无刷新（admin.go:53-54）——只展示注册时刻值并标注 |
| grok | 必 | QuotaInfo/QuotaWindow 全套（admin.go:160-209） |
| lobsterai | 必 | profile-summary 余额 + 每日奖励回读（client.go:488-527） |
| lobsterhub | 必 | FetchQuota Total/Available + CreditItems 分桶（client.go:140-170） |
| loomy | 必 | balance/daily/available 三口径 + 台账（main.go:216-219、survey §2 实测） |
| mmx | 必 | BalanceInfo 全家桶含待入账/结算（store.go:65-84） |
| qw2api | 必 | /api/v2/quota/usage → Quota 聚合 + credits_spent（client.go:41,791-831） |
| runbios | 必 | billing/wallet 美元余额 + total_spent/month_spend（accountinfo.go:1-22） |
| skywork | 必 | qw2api 族 credits 全套（pool.go:37-89） |
| vibex | N/A | 上游无任何积分/余额端点（server.js 全文 grep 无命中） |
| xhx | 必 | /points/v1/balance 七字段 + bills（client.go:34-39、types.go:240-253） |

### 2.4 签到任务（统一档位：slot_hours [1,23] 全量、可配置）
| 插件 | 判级 | 理由 |
|---|---|---|
| buddyhub | 必 | 7 任务（签到/旅行/活跃/保活/夜猫/连登/开学季）+ 档位调度原型（survey §1.1、schedule.go） |
| devin | N/A | tasks.mjs:4 明注「there is no check-in API to call」，任务体系只有 quota refresh/pool heal |
| flonno | N/A | internal 全文 grep 无 checkin/签到/奖励命中（本次已 grep） |
| grok | N/A | 无签到对象；补号走 keeper 注册而非签到（keeper.go、provider.json `_hint`） |
| lobsterai | 必 | RunDailyRewards：活动→领取→回读→刷余额（scheduler.go:263-298） |
| lobsterhub | 必 | 每日登录奖励三步确认到账（checkin.go:10-28） |
| loomy | 可 | 宠物打工真实存在但单号（30 分钟循环 tier_1/2/3：survey §1.3）；挂到档位调度后降级为单号任务卡 |
| mmx | 必 | signin/status + signin/claim 双端点 + TodayPoints 落盘（minimax.go:485-510、store.go:69-72） |
| qw2api | N/A | QwenWork 无签到接口（gap-analysis 判 ➕6 即全空；本次 grep 亦无命中） |
| runbios | N/A | internal 全文 grep 无 checkin/签到命中（本次已 grep） |
| skywork | N/A | internal 全文 grep 无 checkin/签到/奖励命中（本次已 grep） |
| vibex | N/A | 无账号体系亦无任务对象 |
| xhx | N/A | 短信/扫码会话无签到通道（browserlogin.go 实测注释；gap-analysis 判 ➕6） |

### 2.5 对话调试（必 ×13）
13 家全部实现插件契约 `/v1/chat/completions`（宿主聚合目录的来源），调试面统一走「面板 POST → 插件服务端代转」模式（宿主无 CORS，先例见 loomy survey §5.1、skywork `PLUGIN-CAPABILITIES.md` §11.2）。增强项按能力裁剪：显示真实模型（loomy/buddyhub/grok 可得）、扣分（loomy points_consumed/buddyhub credit/xhx 估算）、命中账号（多池插件）。

### 2.6 调用日志（必 ×13）
| 深度 | 插件 | 依据 |
|---|---|---|
| 已有台账 | buddyhub（/api/calls 100 条 + usage-log 5000）、lobsterai（store reqEntry+usage.json）、mmx（usage.go）、runbios（billing/calllog.go）、lobsterhub（CallRecord onCallDone）、devin（usageCount+/api/usage 缓存）、flonno（server/calllog.go+dailylog.go）、xhx（requestlog.go） | 各源码文件 |
| 需补字段/落盘 | loomy（reqEntry 已扩 Points 于 1.9.0：store.go:170-182，但需 data/ 落盘层）、grok（外壳事件 + 内核 stats 需聚合页） | survey §0.3、本次文件列表 |
| 需从零建 | vibex（现状无请求落盘，仅 SSE usage 内存透传）——列为必但标注工作量 | server.js grep |

### 2.7 用量统计（必 ×13）
同 2.6 的台账基础直接聚合成按天/按模型/按账号三维：lobsterai usageDay 已是现成模型（store.go:324-335，90 天保留）；mmx usage.go 按天 Points；buddyhub `/v1/stats`+UsageStat.ByAccount（survey A6）；qw2api/skywork creditsSpent 池级聚合；其余由调用日志推导。vibex 仅 token 维度（无积分维度）。

### 2.8 负载与切号
| 插件 | 判级 | 理由 |
|---|---|---|
| buddyhub | 必 | rotation weighted/round-robin + sticky + cost_first + breaker/degrade（survey §1.4/§3） |
| devin | 必 | round-robin/sticky/least-used 三策略 + max_switch + retry_401/429/5xx（provider.json `_hint`） |
| flonno | 必 | scheduler 策略/每日上限热改 + 3 连败熔断（scheduler.go:25-30,114-160） |
| grok | 必 | 内核轮选 + 外壳 failover 事件（failover_sync_test.go:113-163）+ keeper 补号联动 |
| lobsterai | 可 | 有 Pick/冷却/换号重试，无策略配置面（pool.go:617,707-716）——统一策略页后接入 |
| lobsterhub | 可 | isQuotaError→标记耗尽换号重试（gateway.go:274-280），无策略面 |
| loomy | N/A | 单 session 无号可切，402/429 只能退避（survey §4 判 ❌） |
| mmx | 必 | 多账号 + keep_available 守护 + 签到/注册调度联动（provider.json `_hint`、regtask.go） |
| qw2api | 必 | switch_threshold + failover_rounds + preflight_credits（provider.json、main.go:364） |
| runbios | 必 | dispatcher 失败换号重试（manifest 粗判 18 文件；dispatcher.go 存在） |
| skywork | 必 | switch_threshold + failover_rounds（provider.json） |
| vibex | N/A | 单 cookie；403 自愈走 auto_heal/relay 重建，不是切号 |
| xhx | 必 | switch_threshold(50)/switch_floor_points(200)/strategy=balanced/failover（provider.json、select_test.go:4-5） |

### 2.9 链路与出口
| 插件 | 判级 | 理由 |
|---|---|---|
| buddyhub | N/A | 引擎 Transport 单一共享实例、无 per-account proxy、无代理支持（survey §4 C4 判 ❌） |
| devin | 必 | egress.mjs：mihomo 节点列表/延迟/切换 + 按号独占节点绑定（egress.mjs:1-17）；明注限制：非静态独立 IP（同文件 13-17 行） |
| flonno | 可 | internal/clash 链路守护（manifest 粗判 21 文件） |
| grok | 必 | 每号 EgressNodeID（admin.go:190）+ register_clash_api 三键（provider.json）+ Web 自动建 egress 节点（`_hint`） |
| lobsterai | 可 | internal/clash 全局绑定（clash.go） |
| lobsterhub | 可 | clash_binding.go 与 lobsterai 共享绑定表（provider.json `_hint`） |
| loomy | 可 | 仅 proxy_url 输入+探测按钮（上游国内直连，代理非必需：survey B6） |
| mmx | 必 | clash_url/token + 自动找网络 + 专属分组建议（provider.json `_hint`、net.go） |
| qw2api | 必 | clash_* 全套守护：探测/自动切换开关/劣化判定/冷却（provider.json `_hint`） |
| runbios | 可 | ClashAPI/Secret/Group/ProxyURL 配置已在 settings（settings.go:61-67），守护深度待确认（未逐行验证探测/切换实现） |
| skywork | 必 | clash_auto_switch 自动换节点 + 全套参数（provider.json；gap-analysis 判其链路域最强 ✓6） |
| vibex | 可 | 无出站代理，仅 UA/sec-ch-ua 指纹伪装（provider.json `_hint`、README）——若视图含「指纹/风控对抗」项可承载 |
| xhx | N/A | config.go 无 proxy/clash 键（本次已 grep），国内直连 |

### 2.10 自动注册（统一要求：过 CF / 多 IP 并行 / 实时日志）
| 插件 | 判级 | 理由 |
|---|---|---|
| buddyhub | N/A | 入池唯一通道是 OAuth device_code 扫码，上游无邮箱注册接口（survey §4 判 ❌） |
| devin | 可 | kernel/register.mjs（306 行）+ 验证码处理存在（manifest 粗判 7 文件），非部署主打能力 |
| flonno | 必 | internal/autoreg + cmd/register-browser + captcha_provider 打码（provider.json `_hint`、manifest 粗判 37 文件） |
| grok | 必 | register/grok-register.exe（源码=参考项目，DrissionPage+turnstilePatch 过 CF）+ pool_keeper 自动补号 + 多窗口限流（keeper.go、provider.json） |
| lobsterai | N/A | 面板添加账号（auth_dir refresh token 一次性），无注册链路（provider.json `_hint`） |
| lobsterhub | N/A | importer.go 是桌面凭据导入非注册（文件列表；gateway 仅消费账号） |
| loomy | N/A | 仅手机号短信登录，无邮箱/密码注册通道（survey §4 判 ❌） |
| mmx | 必 | nightregister/regtask/relogin + keep_available 自动补号 + 打码 hint（provider.json、文件列表） |
| qw2api | 必 | autoreg_* 步骤级参数全套 + 夜间窗口 nightly（默认 04:00-06:00）+ 日目标/上限（provider.json） |
| runbios | 必 | Turnstile/captcha_provider 打码通道（manifest 粗判 45 文件；provider.json `_hint`「留空=离线受阻模式」） |
| skywork | 必 | autoreg 步骤参数 + 邮箱四源（provider.json `_hint`；manifest 粗判注册 94 文件为七插件最高） |
| vibex | N/A | cookie 手动同步（油猴脚本 userscript/），无注册 |
| xhx | 可 | 受控浏览器代登（browserlogin.go：开浏览器→官网登录→自动抓令牌入池），是「登录自动化」非「邮箱注册」——挂到该页需改文案 |

实时日志载体：flonno/mmx/skywork/lobsterhub 均有 eventlog.go（本次文件列表验证），buddyhub 有 events.jsonl（survey §1.1）；统一模板直接复用该落盘约定。多 IP 并行先例：grok register_clash_api + register_threads（provider.json）。

### 2.11 临时邮箱
| 插件 | 判级 | 理由 |
|---|---|---|
| buddyhub | N/A | 登录链路不经过邮箱验证码（survey §4 判空壳） |
| devin | 可 | kernel/mail.mjs（185 行）在库，服务注册链路（manifest 粗判 2 文件） |
| flonno | 必 | 临时邮箱 10 文件命中（manifest 粗判），注册链路依赖 |
| grok | 必 | temp_mail_base/temp_mail_key + email_domains 三域名 + CF Worker 邮箱源（provider.json；参考项目 cf_temp_main_worker.js） |
| lobsterai | N/A | 无注册无邮箱（provider.json `_hint`） |
| lobsterhub | N/A | 同上（checkin.go/邮箱无关联；文件列表无 mail 模块） |
| loomy | N/A | 登录不经邮箱（survey §4 判空壳） |
| mmx | 必 | 四邮箱源：guerrilla/freecustom/mailtm/mailsac（providerChain 硬编码序 [Guerr,FCE,MailTM,Mailsac] store.go:260-266；App 字段组 store.go:93-96）。**均为外部服务，无自建**：mail.tm / Guerrilla / mailsac + 公共匿名服务 freecustom.email（providers.go:136-147，POST https://www.freecustom.email/api/auth）。勘误 2026-11-07：原文「mailTM/guerrilla/mailsac + 自建」与源码不符，按 store.go:93-96 实读改正，与 02 §3.6 providerChain 记载一致 |
| qw2api | 必 | 临时邮箱 6 文件命中（manifest 粗判），autoreg 邮件验证码依赖（autoreg_code_tries） |
| runbios | 可 | 邮箱源测试在库（internal/i8417_mail_source_test.go），深度低于五强 |
| skywork | 必 | 四源 cloudflare/maliapi/temp.tf/beeinbox，面板「邮箱设置」卡可改（provider.json `_hint`） |
| vibex | N/A | inbox_port 是面板 cookie 同步服务非邮箱（server.js:2750-2756） |
| xhx | N/A | 短信/扫码登录，无邮箱通道（browserlogin.go） |

### 2.12 设置（必 ×13）
全部 13 家均有 provider.json 私有配置（本次逐一读取）+ 面板只读/可写两条合法路径（插件自写 provider.json，`PROVIDER_CONFIG` 环境变量已有：buddyhub survey §3.2、loomy survey §5.2）。统一设置页至少承载：上游地址/代理/打码键/端口策略/热更新说明。

### 2.13 设计系统（必 ×13）
mockup 的 11 主题 + 动效 6 件套为纯 CSS/JS 资产，零后端依赖（buddyhub survey D 档、loomy survey D 档一致结论）；所有面板均为内嵌单文件 HTML（go:embed 或 SEA 注入），可直接移植。两插件面板已部分对齐（buddyhub 4/5 条、loomy 4/5 条：gap-analysis §3）。

---

## 3. 术语映射表（插件原生词 → 统一词汇）

统一原则：**存量叫「积分」，时段性配额叫「额度窗口」，花掉叫「消耗」，货币计价加货币单位**。视图名保持 mockup 原名「积分与余额」。

| 统一词汇 | 定义 | 插件原生词 | 证据 |
|---|---|---|---|
| **积分**（存量） | 账号当前可消耗的计量余额 | buddyhub `credits/creditsExpiring`（池字段，state.go:126,202-210） | 「credits」 |
| | | qw2api/skywork `credits`（pool.go:37） | 「credits」 |
| | | lobsterai/lobsterhub `totalCreditsRemaining/Credits`（client.go:505；lobsterhub client.go:141-142） | 「credits」 |
| | | loomy `points{balance,daily,available}`（survey §1.2） | 「points（点数）」 |
| | | mmxproxy `Remaining/CreditsTotal`（store.go:75-76） | 「credits」 |
| | | xhx `AvailablePoints`（types.go:247） | 「points（积分）」 |
| | | vibex 免费额度类营销词（RunningHub 页面语，代码无） | 「免费额度」→积分 |
| **额度窗口**（时段配额） | 按时间窗重置的用量配额，可与积分并存 | grok `QuotaWindow{WindowSeconds,ResetAt}`（admin.go:160-168） | 「quota 窗口」 |
| | | devin `dailyQuota/weeklyQuota*Percent`（devin-core.mjs:3036-3037） | 「quota（日/周）」 |
| | | loomy `dailyBalance`（日上限 1000 分） | 「今日额度」 |
| | | xhx `DailyPoints/MonthlyPoints` 桶（types.go:248-249） | 「每日/每月积分桶」→额度窗口 |
| | | qw2api `TotalUsagePercent/IsQuotaExceeded`（client.go:791-800） | 「quota usage」 |
| **余额**（货币计价） | 以真实货币计价的存量，展示带 $ | runbios `balance_cents/balance_dollars`（accountinfo.go:14-15） | 「balance（美元分）」 |
| **消耗**（流出） | 已花掉的计量，分今日/本月/累计 | loomy `usage.points_consumed`（main.go:77） | 「消耗（积分）」 |
| | | buddyhub usage.credit 逐次扣分（state.go:193-198） | 「credit（扣分）」 |
| | | qw2api/skywork `credits_spent/creditsSpent`（pool.go:60,83-89） | 「累计消耗」 |
| | | xhx `pointsSpent`（pool.go:185） | 「累计消耗」 |
| | | runbios `total_spent/month_spend`（admin.go:180-181） | 「累计/本月消耗」 |
| | | lobsterai/loomy/mmx 按天聚合 `Points`（store.go:324 等） | 「消耗（按天）」 |
| **每日任务**（签到统称） | 档位调度器在 slot_hours 触发的全量任务 | buddyhub 签到/旅行/活跃/保活/夜猫/连登/开学季 7 任务（survey §1.3） | 「任务」 |
| | | lobsterai `RunDailyRewards`（scheduler.go:263） | 「每日奖励」→每日任务 |
| | | lobsterhub 每日登录奖励（checkin.go:10） | 「签到」→每日任务 |
| | | mmxproxy `signin/status|claim`（minimax.go:487,510） | 「签到」→每日任务 |
| | | loomy 宠物打工 `pet-work`（survey §1.2） | 「打工领取」→每日任务 |
| **待入账** | 已获但未落账的积分 | mmxproxy `PendingCredit`（get_membership_info，store.go:77） | 唯一真实来源，其余展示「—」 |
| **结算** | 赠送积分的到账确认 | mmxproxy `RetentionDays/SettleAt/Settled`（store.go:81-84） | 「结算状态」 |

> 不入统一词汇的原生词：devin「usage」（调用计数非余额）、vibex「project quota slot」（relay 应用槽位，server.js:1066，与账号额度无关）——矩阵与视图中不得与「额度」混淆。

---

## 4. 遗留待办

1. runbios clash 守护的实现深度（settings.go 有配置键，探测/切换逻辑未逐行验证）——**待确认**，不影响判级（可）。
2. skywork 上游取数端点与 qw2api 同构的判断基于池结构同名同注释（pool.go:37-89 对照 qw2api pool.go:37-89），未读 skywork upstream 客户端源码——**待确认**（不影响判级）。
3. grok 内核 stats→外壳调用日志页的聚合口径未验证（判级已按「必有数据源」给必，聚合方式属实现期问题）。
4. 宿主侧统一计数（今日调用列的兜底来源）未在本次核查范围——**推测**：宿主网关中继已有逐请求统计可复用，接通方式需宿主侧确认。
