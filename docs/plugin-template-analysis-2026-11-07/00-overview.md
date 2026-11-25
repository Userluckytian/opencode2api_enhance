# 00 — 总纲：统一 Go 共享模块总体设计与落地路线

> 日期：2026-11-07　|　性质：归并设计文档，不写实现代码
> 输入：同目录 `01-cf-challenge.md`、`02-temp-mail.md`、`03-routing-scheduling.md`、`04-quota-views.md`、`05-capability-matrix.md`（本会话已用 Read 通读全文）+ `docs/design/console-mockup.html`（本会话 grep `data-view` 667-689 行确认 13 视图，并实读 accounts/credits/routing/egress/autoreg/mail 六个 section）。
> 范围说明：目录中原有 `06-ai-framework-skill-audit.md`（另一仓库 ai-framework-skill 的通用性审计报告，与本系列主题无关，亦未被 00-05 引用），2026-11-07 修订时移出至 `docs/ai-framework-skill-audit-2026-11-07.md`；本系列即为 00-05 五份。
> 引用约定：`01:113` = `docs/plugin-template-analysis-2026-11-07/01-cf-challenge.md` 第 113 行；`mockup:860` = `docs/design/console-mockup.html` 第 860 行。

---

## 1. 统一 Go 共享模块总体设计

### 1.1 包结构

```text
pluginsdk/                      # 共享 Go module（分发形态见 §5-C17 开放问题）
├── challenge/                  # CF 人机验证 token 通道（来源 01）
│   ├── solver.go               # Solver / Request / Result / Kind / 哨兵错误
│   ├── browser.go              # BrowserSolver：CDP + go:embed turnstilePatch + 真实手势
│   ├── api.go                  # APISolver：createTask 系 / create.php 系双协议打码
│   ├── assist.go               # AssistSolver：人工协助兜底（文件投喂/等点击）
│   ├── chain.go                # ChainSolver：有序回退链
│   └── config.go               # Config{Order, Browser, API, Assist}
├── tempmail/                   # 临时邮箱多源（来源 02；包名裁决见 C6）
│   ├── types.go source.go registry.go wait.go extract.go domain.go httpspec.go redact.go
│   └── providers/{cloudflare,temptf,maliapi,mailtm,g22do,emailmux,guerrilla,beeinbox,freecustom,mailsac}.go   # 10 文件（freecustom/mailsac 为 2026-11-07 修订补收，收录/排除决策表见 02 §7）
├── routing/                    # 选号策略 / 错误分类 / 冷却账本 / failover 循环（来源 03 §1）
├── schedule/                   # 定时任务：Spec / Tracker / Scheduler（来源 03 §4）
├── egress/                     # 出口代理：Lease / 四种 Provider / ClashClient（来源 03 §3）
├── quota/                      # AccountQuota 统一额度模型 + 刷新编排（来源 04）
└── register/                   # 注册编排：Job / Runner / 台账幂等补跑（来源 03 §2）
```

包依赖方向（全部向下，无环）：`register → {challenge, tempmail, egress}`；`routing → quota(只读视图)`；`schedule → 无包依赖（Job 由上层注入）`；`challenge / tempmail 不 import egress`（出口以 string 流动，裁决 C1）。

### 1.2 各包职责与对外接口清单

**challenge** — 一句话：把「过 Cloudflare Turnstile / managed challenge」收敛为可插拔 token 通道，浏览器真实渲染为首选、打码 API 为付费兜底、人工协助为最后兜底。
对外接口（归并自 01:133-205）：

- `type Solver interface { Solve(ctx, Request) (Result, error) }`（01:168-170；语义对齐 flonno `autoreg.CaptchaSolver`、runbios `TurnstileProvider`、grok-register `solve_turnstile` 三处既有契约，01:164-167）
- `Request{Kind, PageURL, SiteKey, Proxy, Attempt, Email, Headless, Timeout}`（01:144-153）；硬约束：`Proxy` 与后续注册提交必须同一出口 IP（01:91）
- `Result{Token, Provider, Elapsed, ExpiresIn, OneShot}`；`OneShot` 恒 true——token 一次性，重试必须重新 Solve（01:89, 161）
- `Kind`：`turnstile_managed` / `turnstile_interactive` / `cf_clearance`（v1 对 clearance 返回 `ErrUnsupported`，01:139-140, 296）
- 哨兵错误：`ErrUnavailable / ErrTokenEmpty / ErrInvalid / ErrUnsupported / ErrAssistNeeded`；实现保证「拿不到即报错，绝不返回空 token」（01:173-179, 92）
- 内置实现：`BrowserSolver`（默认；go:embed 补丁 + FindChrome + 探针三口径，01:187-189）、`APISolver`（yescaptcha/2captcha/capmonster/anticaptcha，01:192-193）、`AssistSolver`、`StubSolver`、`ChainSolver`（有序回退，01:200-205）
- `Config{Order, Browser, API, Assist}`（01:210-245）；`Headless` 默认 false——四项目独立留有「无头易被拦」失效证据（01:22, 65-70）

**tempmail** — 一句话：多源临时邮箱（优先级 + 失败换源 + 随机模式 + 凭地址恢复），骨架抄 skywork、声明式兜底抄 flonno/runbios、坑位注释搬 qw2api/grok-register（02:180-186）。
对外接口（归并自 02:213-229, 243-250）：

- `Source` 接口：`Kind() / NewMailbox(ctx) / ListMessages(ctx, mb) / GetMessage(ctx, mb, id)`（02:214-221，skywork provider.go:152-157 原样采纳）
- 可选能力槽（接口断言探测）：`StatsSource`、`Recoverer{RecoverMailbox}`、`HealthSource`（02:223-225）
- `Registry` + `PickMailboxMode(prefer, randomMode)`：逐源建箱，err/池空都继续下一源（02:233-235）；`RandomSource`（crypto/rand 打乱）/`RandomDomain`（02:236）
- 轮询出口：`WaitMessage / WaitCode / WaitLink`（wait.go；详情补取、空正文不提前返回、自适应退避 300ms×1.5 进默认值，02:280）
- `ExtractCode(Target, subject, body) []string`：候选制提取，内置 xAI 三段码 / 关键词前缀 / Clerk 唯一六位数 / CSS-aware 四流派 + 通用兜底链（02:243-249, 165-176）
- `HTTPSpec` 声明式 Source：新源零逻辑接入（02:226-228；表达不了的协议如 Livewire 键序写代码适配器，02:185）
- 域名策略：`DomainPolicy{Preferred, Blocked, Selection}`、`RejectPlus`、`RecreateUntil`（02:260-268）
- 哨兵错误：`ErrEmptyPool / ErrMailboxEmpty / ErrNotRecoverable`（02:66）
- 配置 schema：`tempmail` 段（preferred/random_mode/sources[]，02:284-298）
- providers 10 个（2026-11-07 修订：freecustom/mailsac 补收、tmaily 与 static/none 显式排除，逐源决策表见 02 §7），来源映射见 02:302-316 资产复用表

**routing** — 一句话：一个包覆盖「选号策略 + 错误语义 + 冷却账本 + 换号循环」，13 供应商以配置差异复用（03:131）。
对外接口（归并自 03:137-198）：

- `ErrClass` 八类（auth/quota/rate_limit/upstream/transport/protocol/client_gone/other，03:137-147）；`Action` 四种（Switch/Cool/Exhaust/Fatal，03:149-156）
- `Classifier` 接口：`Classify(err, status, body) Failure`；供应商可用 ErrRules 覆盖状态码语义（03:166-169；对齐宿主 contract.go:192-197）
- `Strategy` 接口：`Pick(exclude) / ReportSuccess / ReportFailure`（03:174-178）；内置 7 策略：RoundRobin / StickyUntilFail(mmx) / Failover 粘主(宿主) / LeastInFlight(flonno lc) / Weighted(余额+闲置, flonno) / Health(EWMA, 宿主) / TwoRoundQuota(DeepAI)；`StickySession` 装饰器（03:181-184）
- `Failover.Dispatch(ctx, call)`：attemptBudget（≤0 取可用号数）+ excluded 排除集 + relaxed 豁免轮 + firstByteGate 20s + 换号重发强制 refreshRequestIDs（03:186-193）
- 流中续写为可选组件：Switchable(class)、maxResume≤3、triedAddrs 强制换出口、EOF 无 [DONE] 已吐内容即终止（03:196-198；宿主 streamWithResume 独有，见 §5-A5 风险）
- 默认错误→动作表（可直接固化为模板默认）：402=耗尽标记+换号、429=按 Retry-After 冷却+换号、401/403=冷却或禁用、5xx=换号不惩罚、400 类=不换直接透传、客户端断开=不惩罚不续写（03:86）
- 配置 schema：`routing` 段（strategy/weighted/failover_rounds/first_byte_gate_sec/refresh_request_ids/cooldowns/sticky，03:203-219）

**egress** — 一句话：出口代理统一为「一个 Lease 接口 + 四种 Provider 实现 + 最小 Clash 外控客户端」（03:321-345）。
对外接口（归并自 03:322-349）：

- `Lease` 接口：`ProxyURL() / Release()`；nil Lease = 直连（03:322-326）
- `Provider` 接口：`Acquire(ctx, hint) (Lease, error)`；四种内置：GlobalList（宿主 engine_proxies）/ ClashSelector（mmx ensureNetwork 探针自愈 + flonno 每次尝试切节点）/ ClashPinned（sky Pin/Binding 每号常驻端口，优先于独占借用）/ ExclusiveBorrow（devin 降级模式）（03:327-339, 374）
- `ClashClient` 最小外控面：GET /proxies、PUT /proxies/<group>、GET /proxies/<node>/delay（非破坏探测）、GET /configs、GET /version；Bearer 鉴权、永不改全局 mode（03:342-345）
- 账号绑定持久化：email/uid → port，JSON 落盘（03:347-348）
- 配置 schema：`egress` 段（mode/global_list/clash{api,secret,group,auto_switch,watch,ensure}/pinned/proxy_url/follow_system_proxy，03:351-370）；`auto_switch` 默认 false（切节点影响用户其它应用出网，03:373）

**register** — 一句话：多 IP 并行注册编排器（并发、节奏、出口绑定、台账幂等补跑），不含具体站点逻辑（03:250-252）。
对外接口（归并自 03:253-278）：

- `Job` 接口：`Run(ctx, eg egress.Lease) (Result, error)`——站点差异由供应商实现，每任务绑定独立出口租约（03:255-258）
- `Runner`：`RunBatch(ctx, n)`（worker-pool、结果按索引回填、并发自动压到出口数 CapConcurrency、全局最小间隔 pacing）+ `Resume(ctx)`（台账 ledger.json 幂等补跑）（03:260-268；DeepAI pipeline.go:436-525 蓝本）
- 触发条件两种：threshold（池内 < min 补到 target）与 slot/window（时间窗内每日一次，去重交给 schedule.Tracker）（03:269-273）
- 配置 schema：`register` 段（concurrency/min_interval_ms/per_task_timeout_sec/threads_cap；`daily_max/daily_target` 移除——裁决 C4，03:281-291）

**schedule** — 一句话：一种 Spec + 一个 Tracker + 一个 Run 循环 + Job 注册表，以 buddyhub slot+确定性抖动+(天,档)去重为骨架（03:399-401）。
对外接口（归并自 03:406-441）：

- `Spec`：Slots（小时档）/ IntervalSec / Window{Start,End}（跨午夜）/ JitterSec（**确定性纯函数，硬约束**）/ DailyTarget + DailyMaxAttempts（双闸）/ MinGapSec / GraceSec（03:409-421）
- `Job` 接口：`Name() / Run(ctx, slot)`；slot=-1 手动触发不占档（03:423-427）
- `Tracker`：`DoneInSlot / Start / FinishOK`；去重粒度=(job, 自然日, 档)；落盘 JSON，Start 清旧状态防「跑一半重启」误判（03:429-434）
- `Scheduler`：`Run(ctx) / Apply(specs) / RunNow(name)`；nextWake 重算 + wake channel 热重载 + 迟到宽限 + 档内并行派发（03:436-441）
- 配置 schema：`schedule` 段（job/slots/jitter_sec/双闸/min_gap_sec/grace_sec/trigger，03:443-457）

**quota** — 一句话：把 13 家「积分/余额/额度窗口/消耗」收敛为一个带单位、带来源、带置信度的 `AccountQuota` 模型，余额（上游快照）与消耗（本地流水）分离（04:156-162）。
对外接口（归并自 04:166-209）：

- `Unit`（points/credits/usd_cents/tokens/percent/unknown）、`Source`（upstream-api/deduction/local-estimate/registration/none）、`Confidence`（exact/estimated/stale/unknown）（04:167-172）
- `QuotaBucket{Label, Remaining, ExpiresAt}`（分桶：buddyhub 快过期 / lobsterhub CreditItems / xhx 五桶 / mmx 待入账，04:174-178）
- `QuotaWindow{Kind, Percent, Remaining, Total, ResetAt, Hide}`（窗口：grok 多窗口 / devin 日+周百分比 / loomy 日额度，04:180-187）
- `AccountQuota{Unit, Remaining, Total, Buckets, UsedToday, CallsToday, SpentTotal, SpentMonth, Windows, Exhausted, PendingCredit, CheckedAt, NextSyncAt, Source, Confidence}`（04:189-209）
- `Refresher` 接口（**本总纲归并时新增**，非 04 原文）：`Refresh(ctx, accountID) (AccountQuota, error)`——把 04:231-245 各家刷新节奏（周期 refresh_interval_sec / 手动面板按钮 / 签到回填）抽象为被 schedule 的 `credits_refresh` Job 与面板同步按钮共同调用的统一入口（03:473）；并发预算与进度回报协议（buddyhub 并发 3 先例 04:68、mmx syncRunning/syncTotal 04:266）需阶段二定稿（§5-C18）
- 记账约定：估算+结算双口径（xhx MarkUsed/applyBalance 先例，04:149）；「0」与「未知」用 Source/Confidence 表达，不用哨兵值（04:159）

### 1.3 接口冲突与裁决（归并时发现）

| # | 冲突 | 证据 | 裁决 |
|---|------|------|------|
| C1 | 出口参数形态：`challenge.Request.Proxy` 是 string（01:148），`egress` 给的是 `Lease` 接口（03:322-326）；tempmail 个别源也需代理（mmx mailsac 依赖本地代理，02:35） | 01:148 vs 03:322 | 出口值以 **string** 流动；`Lease` 由编排层（register / 请求调用方）持有并归还；challenge/tempmail 不 import egress，装配时 `Request.Proxy = lease.ProxyURL()`。依据：01:251 包方向硬约束（browser 包单向 import autoreg 的 runbios 先例） |
| C2 | Clash 配置键双命名：mmx `clash_url/clash_token` vs sky/qw `clash_api/clash_secret` | 03:311 明注「两套命名并存，模板需统一」 | 统一 **`clash_api`/`clash_secret`**（03:359 schema 草案已采用；sky/qw 参数族更完整：watch/ensure 全套） |
| C3 | 轮询策略命名：宿主/flonno `rr`、bios `round`、devin `round-robin` | 03:49-55 | 统一 **`round_robin`**（03:206 schema 已用） |
| C4 | 每日双闸重复承载：register schema 有 `daily_max/daily_target`（03:287-288），schedule Spec 也有 `DailyTarget/DailyMaxAttempts`（03:417-418） | 03:287 vs 03:417 | 归 **schedule.Spec**（03:462 设计决策 3 把双闸定位为框架语义）；register 配置删去两键，Runner 由补号 Job 传入本次目标数——避免两处独立计数漂移 |
| C5 | 余额口径：doc 03 Strategy 快照 P 沿用 flonno Account 含裸 `Balance`（03:174-175 注），doc 04 要求单位显式化、0 与未知分离（04:158-159） | 03:174 vs 04:158 | routing 不再定义裸 Balance/Quota 字段；`P` 内嵌 **quota 只读视图**（Remaining/Unit/Confidence/Exhausted）；weighted/two_round 从该视图取数，避免重现「注册快照当余额」的 flonno 坑（04:279 坑位 7） |
| C6 | 包名：doc 02 建议 `tempmailx`（02:190），skywork/qw2api 现状为 `internal/tempmail`（02:147, 137），ask 建议 `tempmail` | 02:190 | 定名 **`tempmail`**（与两个最强蓝本目录名一致，迁移少一次 rename） |
| C7 | TokenProvider 接口归属：01:251 给两案（注册包定义小接口由 challenge 满足 vs 直接 import challenge），01 自身推荐后者 | 01:251 | **直接 import `challenge.Solver`**，不设 TokenProvider 中间适配层；供应商 Job 组装 Solver 注入 |
| C8 | 邮箱轮询双形态：flonno/runbios `Mailbox.Create/Poll(since)` 二方法 vs skywork `Source` 四方法 | 02:276-280 | **`Source` 为正典接口**；`Create/Poll` 做兼容适配器，`since` 过滤统一在 wait.go（02:278 已给方案） |
| C9 | 验证码提取返回值：qw2api/skywork 现状单值 `msg.Code()` vs doc 02 候选制 `[]string` | 02:176, 245-247 | **候选制**（flonno「唯一才采用」证明单值正则不够，02:176）；旧调用点取首候选 |
| C10 | doc 03 草案内部笔误：`Spec.Window *Window` 字段（03:411）与同结构体内嵌 `Window struct{...}` 定义（03:413-415）重复 | 03:411-415 | 落地为独立 `type Window struct{ Start, End string }`（跨午夜支持，依据 03:393 grok keeper.go:350-361）；**2026-11-07 修订已落地**：03 §4.2 已改为独立 type 并就地标注勘误 |

---

## 2. 需求映射表（7 需求 → 模块 → 文档 → 收录决策）

| # | 需求 | 模块 | 主文档 | 收录决策与取舍（含 qw2api） |
|---|------|------|--------|------------------------------|
| 1 | 过 CF 人机验证 | `challenge` | 01 | 收录 5+1 项：grok-register（Python 参考实现，补丁/打码/代理池三法齐全，01:275）、flonno2api（Go 契约+补丁内嵌+人工兜底，01:276）、runbiosproxy（token 生命周期硬约束最全，01:277）、skywork（真实手势 CDP+盾位混合编排，01:278）、grok 外壳（「外壳整体委托注册机 exe」模式样本，01:279）、devin 仅收 M10 避盾思想（01:280）。**qw2api 不收录进 CF 方法表**：其人机验证是阿里云验证码 2.0 非 CF（cdp.go:3-5 initAliyunCaptcha、drive.go:21 `#aliyunCaptcha-window-popup`，01:282），验证码提供方/探针/token 字段（captchaVerifyParam vs cf-turnstile-response）均不同，收录会混淆问题域；但其「真实手势 CDP + gorilla/websocket 单依赖」是 challenge 浏览器底座的直接同源实现，作为 §3.4 选型旁证收录（01:261, 282）。mmxproxy/DeepAI 无 CF 证据不收录（01:281, 283） |
| 2 | 临时邮箱 | `tempmail` | 02 | 收录：skywork（多源骨架唯一生产验证方案）、flonno/runbios（HTTPSpec 声明式兜底）、grok-register（CF 双 token 流 + 域名三策略 + 35s 重发）、devin（CSS-aware 提取，防样式表颜色误判实证 02:110）、mmxproxy（providerChain 整号换源，02:238 补充采纳）、DeepAI（自适应轮询 + 接口分层）。**qw2api 收录**：temp.tf 客户端 306 行是全部项目里该源端点/坑位记录最完整的规格（provider 名静默过滤、池空 200+null 语义，02:14, 138-142），且是 skywork 提取层直系前代；但不作多源范本——engine 硬绑单源 `tempmail.New()`（02:142-143）。beeinbox 列可选 P2（Livewire 键序维护成本最高，02:316, 329）；**freecustom/mailsac 补收、tmaily 与 static/none 显式排除，逐源决策表见 02 §7（2026-11-07 修订补齐收录决策记录）** |
| 3 | 负载均衡/错误转移/无感切号 | `routing` | 03 §1 | 收录：宿主（ErrRules 差异化 + health EWMA + sticky 不推进游标 + 流中续写）、flonno（六枚举错误分类 + 失败状态机 + weighted 三因子）、bios（402→exhausted 语义）、qw（firstByteGate 20s + refreshRequestIDs + relaxed 豁免轮）、mmx（粘直到失败才轮转的反例形态）、devin（开关位最简形态）、DeepAI（两轮额度优先）。**qw2api 收录**且贡献三个关键机制：首帧闸判据、换号强制换 request/session id（防上游算同一次，03:67）、排除集豁免轮（03:122-126）。宿主「流中续写」为可选组件非必选（§5-A5） |
| 4 | 多 IP 并行注册 | `register` | 03 §2 | **以 DeepAI 为蓝本**：mihomo listeners 每节点常驻端口 + RunBatch 并发编排 + CapConcurrency 压并发 + 全局 pacing + 台账幂等补跑（03:248）；双闸（每日目标/尝试上限）采纳 qw nightly（03:394；邮箱池劣化防无限重试的明确设计动机见 qw 源 `internal/server/nightly.go:5-9`——勘误 2026-11-07：原稿误作「03:5-9」，按本文引用约定该处指向 doc 03 文档头，设计说明在 qw 源 nightly.go:3-9，与 03:394 原引一致）；flonno 双模式补号（阈值/窗口）合入 trigger；grok threads 1..10 夹紧收编为 threads_cap。**mmx `keep_available=3` 触发细节本会话未逐行核实**（03:483），只作需求存在性证据 |
| 5 | 出口代理 | `egress` | 03 §3 | 四模式全收（global_list / clash_selector / clash_pinned / exclusive_borrow）+ ClashClient 公约数；模式 C（pinned）优先于 C'（独占借用）——sky 常驻端口消除 devin 并发瓶颈，devin 自己也注明是 mihomo 单出口 selector 限制（03:374）。键名统一 `clash_api/clash_secret`（C2）；**`clash_mixed_port`、`resolve_candidates` 两键在全样本 grep 未找到，不设键**（03:312-313）。探测必须非破坏（/delay，03:375）、写运行配置必须幂等可还原（DeepAI 标记块+备份还原、sky 行级合并、mmx 假节点过滤三条经验合并，03:376） |
| 6 | 定时任务 | `schedule` | 03 §4 | **buddyhub 为唯一骨架**（slot+确定性抖动+(天,档)去重是样本中唯一成体系框架，03:33）；用户基调「凌晨 1 点与 23 点各全量执行、可配置」即 buddyhub `slot_hours=[1,23]` 每档触发全部任务（05:18）。吸收：grok（分钟 ticker+窗口+连败暂停——连败暂停定为 Job 内部策略非框架语义，03:462）、qw（双闸+day 变归零）、flonno（窗口内每日一次 = (天,档) 去重的单档特例）、sky/qw（refresh_interval 周期任务形态） |
| 7 | 账号额度/积分/余额/消耗展示 | `quota` | 04 | **13 插件全量盘点收录**（04 §2 逐家取数 API 均有代码证据）。统一模型四原则：单位显式化（qw2api 积分 vs runbios 美元分 vs devin 百分比，04:158）、0 与未知分离、窗口化、消耗与余额分离（mmx 签到获得≠调用消耗、xhx 估算≠结算必须分字段，04:162）。**vibex 全空收录为容错最差测试用例**（04:282）；devin 无绝对值→余量条走 Windows[].Percent 分支（04:281）；runbios 需存密码（余额端点认登录 token 不认 API key，04:275）；术语统一表见 05:191-224（存量叫积分/时段叫额度窗口/花掉叫消耗/货币加单位） |

---

## 3. 落地路线图（4 阶段）

| 阶段 | 内容 | 验收标准（一句） |
|------|------|------------------|
| **一：模板骨架** | 新建插件模板：provider.json 私有段 schema + 面板内嵌单文件 HTML（go:embed / SEA 注入，05:187）+ eventlog 落盘约定（05:164）+ `/v1/chat/completions` 契约 + console-mockup 13 视图导航骨架移植 | 用模板生成一个最小插件，出现在宿主聚合目录并能在「对话调试」视图完成一次真实请求（chat 视图出真数据） |
| **二：共享包 pluginsdk v0** | 按依赖与风险排序落地：routing → schedule → quota → tempmail → challenge → egress → register（纯逻辑包先行，浏览器/Clash 重的包在后）；同步落定 §1.3 十项裁决 | 每包表驱动单测 + 冒烟通过（ChainSolver 用 StubSolver、tempmail 用 HTTPSpec 假源、schedule 验证 jitter 确定性与 (天,档) 去重、routing 验证默认错误映射表），零新增三方依赖（对齐 01:261-267 轻依赖共识） |
| **三：试点插件迁移** | 双试点：① 注册全链路型——建议 runbiosproxy 或 skywork（单家即覆盖 challenge+tempmail+egress+register+quota 全部包）；② 纯调度额度型——建议 xhx 或 buddyhub（routing+schedule+quota） | 试点插件功能与旧版对齐：按各文档验收口径逐项回归（如 runbios token 一次性语义 01:89、skywork 四邮箱源 02:179、buddyhub (天,档) 去重 03:391），账号池/积分与余额视图出真数据 |
| **四：全量推广** | 13 插件全部迁到模板 + 13 视图接真数据或显式 N/A（判级依据 05 §1 矩阵：必 128 / 可 15 / N/A 26） | 全量插件构建通过；05 矩阵逐格复核每格有落点（真数据 / 能力裁剪后的降级展示 / 显式 N/A 文案）；各插件 provider.json `_hint` 与统一配置 schema 对齐（C2/C3 键名迁移完成） |

---

## 4. 与 console-mockup.html 视图模型的对应

视图清单出处：mockup:667-689 导航区 `data-view` 13 项（本会话 grep 实测）；各 section 行号本会话实读确认（732/860/909/973/1036/1125/1162/1224/1305/1376/1444/1497/1591）。

| 视图（mockup 行号） | 支撑包 | 数据落点 |
|---------------------|--------|----------|
| 仪表盘 dash（732） | `quota` + `routing` + `schedule` | KPI 卡取 quota 池计数与消耗、routing 池健康分档、schedule 任务最近执行（05:47：深度差异只在卡片数） |
| 账号池 accounts（860） | `routing` + `quota` | 表头 账号/状态/积分/余量/今日调用/累计消耗/最近使用/出口/操作（mockup:890-894 实读）逐一映射 AccountQuota 字段与池状态机，降级链见 04:254-262 |
| 积分与余额 credits（909） | `quota` + `schedule` | 三 KPI（池总积分+可用天数 / 今日消耗趋势 / 低余额数，mockup:921-937）、同步进度条（Refresher 并发回报，mmx syncRunning 先例 04:266）、逐账号余额表（待入账仅 mmx 真实可得，04:267） |
| 签到任务 tasks（973） | `schedule`（档位调度）+ `quota`（签到回填余额） | 用户基调 slot [1,23] 全量（05:18）；N/A 插件（devin/flonno/grok/qw/runbios/sky/vibex/xhx）隐藏该页（05:29 判级） |
| 对话调试 chat（1036） | 插件契约（无共享包）+ `routing`（命中账号）+ `quota`（扣分显示） | 统一走「面板 POST → 插件服务端代转」模式（05:101） |
| 调用日志 logs（1125） | eventlog 落盘约定（05:164），无专包 | 台账深度三档：已有 / 需补落盘（loomy、grok）/ 需从零建（vibex）（05:103-108） |
| 用量统计 usage（1162） | `quota` 消耗流水聚合 | 按天/按模型/按账号三维；lobsterai usageDay 90 天保留为现成模型（05:111） |
| 负载与切号 routing（1224） | `routing` + `quota` | 策略四选（粘性/轮询/加权/最少在途，mockup:1236-1254 实读）、错误转移规则 401/402/429/5xx（mockup:1259-1266 与 routing 默认表一一对应）、粘性与阈值四键（切号积分阈值/低余额告警/取号前实查/周期刷积分，mockup:1275-1283 = qw2api switch_threshold+preflight+refresh 语义）、切号记录时间线（Failover 事件 sink） |
| 链路与出口 egress（1305） | `egress` | 节点延迟表（非破坏探测）、当前出口卡（探测间隔/劣化阈值/连续劣化/切换冷却 + 自动切换开关默认关，mockup:1342-1354 与 sky clash 参数族同构）、按号出线绑定表（mockup:1358-1369 = ClashPinned Binding） |
| 自动注册 autoreg（1376） | `register` + `challenge` + `tempmail` + `egress` | 三卡：注册通道（challenge 浏览器优先+打码兜底，mockup:1388-1397）、夜间窗口（schedule 双闸 04:00-06:00 目标5/尝试10，mockup:1398-1401 = qw nightly 参数）、注册出口（每号轮换，mockup:1403-1408）；队列/实时日志/历史表（mockup:1411-1437）= register Runner + eventlog |
| 临时邮箱 mail（1444） | `tempmail` | 源优先级表（优先/来源/域名/状态/今日用量/取码成功率，mockup:1456-1467 = Registry + StatsSource）、最近收件取码（extract.go）、生成规则（默认来源/域名/dot/plus，mockup:1480-1490 = SourceConfig + DomainPolicy，加号默认禁用有依据 02:264——DeepAI/runbios 反向固定 plus=false——及 mockup:1486,1489 文案「禁用（部分站拒绝）」「加号别名常被注册风控识别，默认关闭」；勘误 2026-11-07：原稿引 02:148-152 落空，该处为 02 §3.8 skywork 包结构段，且 02:116 转述的 flonno sources.go:148-152 语义是 plus=true 取真 gmail 变体避注册页黑名单，属「保留 Plus 可配」的反向证据而非默认禁用依据） |
| 设置 settings（1497） | 各包 Config 汇总 | provider.json 私有配置 + 面板热改（05:184） |
| 设计系统 design（1591） | 无（纯 CSS/JS 资产） | 11 主题 + 动效 6 件套零后端依赖（05:187） |

---

## 5. 风险与开放问题

### A. 技术风险（实现期）

- **A1** MV2 指纹补丁扩展随 Chrome MV2 退场可能失效，需按 Chromium 实际行为回归（01:265, 298；推测级）。
- **A2** `cf_clearance`（5 秒盾）全部样本无成功实现，v1 只能 `ErrUnsupported`（01:296）；浏览器渲染取 cookie 的可行性是推测。
- **A3** 打码 API token 与浏览器会话/IP 绑定的边界未实测：runbios 实测空/假 token 恒拒（01:85），「API 真实 token 是否也被拒」无结论（01:297）——回退链设计不依赖该结论，但 APISolver 的实际成功率未知。
- **A4** challenge 浏览器通道需要桌面环境、单机并发受限（grok-register 建议 2-4 线程，01:21）；无头路线四项目证伪，不可作为并发扩展方向（01:65-70）。
- **A5** 流中续写（streamWithResume）仅宿主实现：三段计时器/triedAddrs/续写上限等机制下沉为可选组件的模板化成本高（03:113-121, 197-198），阶段二按「可选组件」排期而非首版必含。
- **A6** CF worker token 双形态（GET `/api/token?address=` vs POST `{address,password}`）对应不同 worker 版本（02:322，grep 实测本树 worker.js 0 命中 api/token）——providers/cloudflare.go 两种都要留并按部署探测。

### B. 证据缺口（迁移前需补查，均已登记未验证）

- **B1** doc 03 的 routing/egress/schedule 分析只覆盖 7 插件样本，**lobsterai / lobsterhub / loomy / vibex / xhx 五家未入样本**（03:24 覆盖度声明）——其中三家在 05 有判级证据（lobsterai Pick/冷却 pool.go:617、xhx switch_threshold 双阈值 04:38），但调度细节未逐行读，阶段二前需补查。
- **B2** mmx `keep_available` 自动补号触发/节流未逐行核实（03:483）。
- **B3** flonno 冷却时长是否按 FailureReason 区分取值无配置证据（03:96, 484）。
- **B4** sky `egress/probe.go`、`verify.go`、`manager.go` 探测自愈细节未逐行读（03:485）。
- **B5** runbios clash 守护实现深度待确认（05:230）；skywork 上游取数端点与 qw2api 同构的判断基于池结构同名，未读 skywork upstream 客户端（05:231）。
- **B6** grok 内核 stats→外壳调用日志页的聚合口径未验证（05:232）；宿主侧统一计数（账号池「今日调用」列的兜底来源）未核查（05:233, 04:258）。
- **B7** flonno 4 源真实入信链路未闭环（源码自述 intent 4705，02:323）；runbios emailmux 签名 secret 未在线复核（02:324）；devin tmaily 建箱受 Turnstile 未在线复测（02:325）；beeinbox reCAPTCHA v3 是否拦建箱 indeterminate（02:329）。
- **B8** loomy `points_alert_threshold` 配置存在但 1.9.0 无读取逻辑——模板做低水位告警需补实现而非透出配置（04:279-280）。

### C. 设计开放问题（本轮文档未覆盖，需在阶段一定稿）

- **C1**（编号续 §1.3）共享包分发形态：独立 Go module（pluginsdk）vs 宿主仓库 internal 子目录 + go.mod replace——影响版本策略与「单文件 exe 分发」共识（01:261）的构建方式，各文档未涉及。
- **C2** quota Refresher 的并发预算与进度回报协议（面板同步进度条需要 成功/失败/待处理/耗时 四计数，mockup:948-953；buddyhub 并发 3 与 mmx 进度字段是两个现成先例，04:68, 266）。
- **C3** 前端资产复用方式：13 视图 mockup 为静态 HTML，接真数据时统一「面板 POST → 插件服务端代转」（05:101）；公共 JS/主题抽到哪里（design 视图承载 or 模板资产目录）未定。
- **C4** `challenge` 与 `tempmail` 的供应商定制层：提取流派按目标站点选择（02:247-249）、盾位清单按端点探测（01:109），「目标站点配置」的 schema 归模板还是归各插件，待定。

---

## 附：本总纲写作时实际执行的检查

1. Read 通读五份文档全文：`01-cf-challenge.md`（298 行）、`02-temp-mail.md`（342 行）、`03-routing-scheduling.md`（487 行）、`04-quota-views.md`（283 行）、`05-capability-matrix.md`（234 行）。
2. `grep -n "data-view" docs/design/console-mockup.html` → 13 项导航（667-689 行），与 05:4 记载一致。
3. `grep -nE 'id="view-|<section'` → 13 个 section 行号清单。
4. Read 实读 mockup 六个 section：view-accounts（860-904，表头 890-894）、view-credits（909-968）、view-routing（1224-1300）、view-egress（1305-1371）、view-autoreg（1376-1439）、view-mail（1444-1492）。
5. 未执行任何构建/测试（本文档为纯设计归并，无实现代码）。
