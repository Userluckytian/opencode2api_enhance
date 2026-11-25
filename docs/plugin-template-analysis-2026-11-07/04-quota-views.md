# 04 · 账号额度/积分/余额/消耗展示（需求7）——盘点、统一模型与展示字段

> 日期：2026-11-07 · 性质：只读分析，未改任何源码
> 需求：盘点 13 个插件能从上游拿到的额度类数据（积分/余额/额度/今日·累计消耗/重置时间）、取数 API 与代码证据；产出统一 `AccountQuota` 数据模型与展示字段清单（对齐 `docs/design/console-mockup.html` 账号池页列）。
> 证据口径：本会话实际 grep/读取的源码，引用格式 `路径:行号`；未验证处标「推测」。

---

## 0. 基线与源码定位（含两处必须登记的偏差）

13 个插件部署实例的 `provider.json` 于本次会话逐一读取（`D:/Program Files/opencode2api/bin/providers/<id>/provider.json`），`_source.repo` 或同目录证据给出的源码路径如下，**全部实际存在**（逐一 `ls -d` 验证过）：

| 插件 | 部署版本 | 源码路径（本会话验证存在） | 备注 |
|---|---|---|---|
| buddyhub | 1.6.0 | `D:\AI_Projects\_plugin-work\phase5\buddyhub` | ⚠️ provider.json `_source.repo` 写的是 `D:\AI_Projects\buddyhub`，但该目录 `cmd/pluginbuddy/main.go:27` 仍为 `var version = "dev"`；phase5 副本同文件为 `var version = "1.6.0"` = 部署版。与 `docs/buddyhub-panel-survey-2026-11-05.md` §0 结论一致：**权威工作副本是 phase5** |
| devin | 1.2.1 | `D:\AI_Projects\Devin-all\devin-plugin` | 无 `_source`；以 VERSION=1.2.1 + 目录结构对齐部署实例 |
| flonno2api | 1.3.0 | `D:\AI_Projects\_plugin-work\phase5\flonno` | `_source.repo` 直指本路径 |
| grok | 0.5.0（内核 3.1.8） | 外壳 `D:\AI_Projects\_plugin-work\phase4\grok`；内核 `D:\AI_Projects\grok2api` | 外壳 `_source` 自述 0.5.0 同代构建树 |
| lobsterai | 1.2.1 | `D:\AI_Projects\Lobster\lobsterai2api` | `_source.repo` 直指本路径 |
| lobsterhub | 1.6.0 | `D:\AI_Projects\_plugin-work\phase5\lobsterhub` | `_source.repo` 直指本路径 |
| loomy | 1.9.0 | `D:\AI_Projects\_plugin-work\phase5\loomy` | ⚠️ `_source.repo` 写的是 `D:\AI_Projects\loomy2api`，但该目录 `plugin.go:24` 为 `pluginVersion = "1.5.1"`；phase5 副本同文件为 `"1.9.0"` = 部署版。与 `docs/loomy-panel-survey-2026-11-05.md` §0.1 结论一致 |
| mmxproxy | 1.4.0 | `D:\AI_Projects\_plugin-work\phase3\mmxproxy` | `_source.repo` 直指本路径（`_source.note` 提到的 `plugin/src/` 目录实际不存在，完整插件源码在仓库根——manifest 已记录该偏差） |
| qw2api | 0.7.0 | `D:\AI_Projects\_plugin-work\phase3\qw2api` | `_source.repo` 直指本路径 |
| runbiosproxy | 0.2.0 | `D:\AI_Projects\BIOS-all\runbios_proxy` | 无 `_source`；以 plugin-dist/runbiosproxy provider.json version=0.2.0 对齐 |
| skywork | 0.4.0 | `D:\AI_Projects\_plugin-work\phase5\skywork` | `_source.repo` 直指本路径 |
| vibex | 1.3.1 | `D:\AI_Projects\vibex2api\plugin` | provider.json 无 `_source`；以部署 README「VibeX = RunningHub 免费 LLM 额度」+ 目录结构（plugin/src/server.js）定位，仓库非 git |
| xhx | 1.5.1 | `D:\AI_Projects\_plugin-work\phase5\xhx` | `_source.repo` 直指本路径 |

---

## 1. 四条已知线索核实结论

| 线索 | 结论 | 证据 |
|---|---|---|
| grok 的 QuotaInfo | ✅ 存在，且比线索更丰富：`QuotaInfo` + 多窗口 `QuotaWindow` 双结构，含重置/探活时间 | 外壳 `phase4/grok/admin.go:171`（`type QuotaInfo struct`）、`admin.go:160`（`QuotaWindow`）、`admin.go:209`（`Quota QuotaInfo json:"quota"`、`QuotaWindows []QuotaWindow`）；内核 `grok2api/backend/internal/application/account/service.go:1173`（`newQuotaView`）、`:2954-2985`（`HasQuotaWindows/DecrementQuota/ExhaustQuota`） |
| lobsterai 的 points | ✅ 存在三处：每调用积分（reqEntry.Points）、按天聚合（usageDay.Points，留 90 天）、每号余额（QuotaUsage 查询后写池） | `Lobster/lobsterai2api/internal/server/store.go:80`（reqEntry）、`store.go:324/335`（usageDay/usageModelAgg）、`store.go:391/410`（`d.Points += e.Points`）；`internal/upstream/client.go:491-516`（`QuotaUsage`）；`internal/scheduler/scheduler.go:224-243`（`refreshCredits`） |
| buddyhub 的 credits | ✅ 存在独立 `internal/credits` 包 + 池内 `credits/creditsExpiring` 双字段，含签到外逐次扣减回写（P1-A） | `phase5/buddyhub/internal/credits/credits.go:1-80`（包注释与 `Refresh`，并发 3）；`internal/pool/state.go:119-126`（`ReenableIfCredits`）、`state.go:193-210`（P1-A 逐次扣减 credits/creditsExpiring，钳 0） |
| xhx 的 switch_threshold/额度视图 | ✅ 存在：`switch_threshold`（默认 50，余额≤视为耗尽）+ `switch_floor_points`（默认 200，粘住号跌破即换人）；余额字段为 points/initialPoints/pointsSpent | `phase5/xhx/config.go:61-63`；`internal/pool/select_test.go:4-5`（阈值语义注释）；`internal/pool/pool.go:12-14`（「把 QwenWork credits 额度换成商汤 points 积分」的复用声明）、`pool.go:182-185`、`pool.go:272-313`（估算+真实差值双口径） |

---

## 2. 逐插件盘点：能从上游拿到什么、从哪个 API 拿

图例：✅=上游直接给；🧮=插件本地聚合/推算；❌=拿不到（上游无端点）；「—」=不适用。

### 2.1 汇总表

| 插件 | 积分/余额 | 额度窗口（百分比/时段） | 今日消耗 | 累计消耗 | 重置/结算时间 | 今日调用 | 最近使用 | 出口绑定 |
|---|---|---|---|---|---|---|---|---|
| buddyhub | ✅ 余额+快过期分桶 | ✅ 模型级限流台账含重置时刻 | 🧮 usage-log 聚合 | 🧮 usage-log（封顶5000） | ✅ 限流重置/积分到期（CycleEndTime 未透出） | ✅ `Status.today_calls` | ✅ `last_success` | ❌ 引擎无代理层 |
| devin | ❌ 无绝对值 | ✅ daily/weekly 剩余% + 重置 unix | ❌（仅百分比） | ❌ | ✅ `dailyQuotaResetAtUnix/weeklyQuotaResetAtUnix` | 🧮 `usageCount` Map | 🧮 调用记录 | ✅ 按号独占绑定节点 |
| flonno2api | ⚠️ 仅注册时快照 | ❌ | 🧮 调用日志 | ❌ | ❌ | ✅ `DayCalls` 每日计数 | ✅ `last_used_at` | ⚠️ 全局 clash 守护 |
| grok | ✅ Used/Limit/Remaining/UsagePercent | ✅ 多窗口+WindowSeconds | ✅ Used（窗口内） | ⚠️ 无终身累计 | ✅ `ResetAt/PeriodEnd/ExhaustedAt/NextProbeAt` | ⚠️ 经内核 stats | ⚠️ 经调用事件 | ✅ `EgressNodeID` 每号 |
| lobsterai | ✅ `totalCreditsRemaining`（含活动积分） | ❌ | 🧮 usageDay.Points | 🧮 usageDay 聚合（90天） | ❌（日奖励到账靠回读） | 🧮 usageDay.Calls | 🧮 调用记录 | ⚠️ 全局 clash |
| lobsterhub | ✅ Total/Available + CreditItems 分桶 | ❌ | 🧮 onCallDone 记账 | 🧮 记账 | ❌ | 🧮 CallRecord | 🧮 CallRecord | ⚠️ clash 绑定表 |
| loomy | ✅ balance/daily/available + 台账前后值 | ✅ 日额度（固定 1000 分/日） | 🧮 `usage.points_consumed` 按天 | 🧮 台账 17625 条可回溯 | ✅ 日额度语义（上游按自然日）❌具体时刻 | 🧮 store 按天 | 🧮 reqEntry | ❌ 国内直连 |
| mmxproxy | ✅ Remaining/CreditsTotal + 分桶（op_credit_summary） | ⚠️ 无百分比端点 | ✅ 签到 TodayPoints（获得口径）🧮消耗口径 | 🧮 usage.go 按天聚合 | ✅ `SettleAt` 次日 UTC 到账 | 🧮 gateway 计数器 | 🧮 调用记录 | ⚠️ clash_url 全局 |
| qw2api | ✅ Quota{Remaining,Total,Used} | ✅ `TotalUsagePercent/IsQuotaExceeded` | 🧮 creditsSpent 净下降 | ✅ `credits_spent` | ❌ 上游未给 | 🧮 池 reqCount | 🧮 池记录 | ⚠️ clash 守护（探测/自动切换开关） |
| runbiosproxy | ✅ balance_cents（美元分） | ❌ | ❌（无今日口径） | ✅ `total_spent_cents` + `month_spend` | ❌ | 🧮 billing/calllog | 🧮 calllog | ⚠️ clash_* settings |
| skywork | ✅ Credits/InitialCredits | ⚠️ 同 qw2api 族 | 🧮 creditsSpent 净下降 | ✅ `credits_spent` | ❌ 上游未给 | 🧮 池记录 | 🧮 池记录 | ✅ `clash_auto_switch` 自动换节点 |
| vibex | ❌ 全无 | ❌（仅 relay 项目槽位管理，非积分） | ❌ | ❌ | ❌ | ❌（现状无落盘） | ❌ | ❌（仅 UA 指纹伪装） |
| xhx | ✅ AvailablePoints（权威）+ 5 桶 + TotalChargeable | ⚠️ Daily/Monthly 桶（无重置时刻） | 🧮 余额净差值（applyBalance） | ✅ `points_spent` | ❌ 账单无时刻归因 | 🧮 池 MarkUsed | 🧮 池记录 | ❌ 无代理配置（国内直连） |

### 2.2 逐插件取数 API 与证据

**buddyhub** — 上游 CodeBuddy 计量端点：
- `POST /billing/meter/get-user-resource`（global 首选）/ `POST /v2/billing/meter/get-user-resource`（CN 现状、global fallback）：`phase5/buddyhub/internal/upstream/client.go:825-827`
- `UserResourceDetailed` 按到期时间把余额拆 `CreditBuckets`：`client.go:1474`
- 刷新编排 `credits.Refresh`：并发 3、401 先 refresh 再重试一次、按余额解冻硬冷却：`internal/credits/credits.go:33-80`
- 触发时机：面板「刷新积分」端点 + 签到回填（`ReenableIfCredits`，`state.go:119`）；global 账号无签到体系，不主动查就是 0（`credits.go` 包注释）
- 调用扣分回写：上游 usage.credit 逐次扣减 credits/creditsExpiring、钳 0：`state.go:193-210`
- 可扩未透出：`creditsExpiring`、`CycleEndTime`（survey B2 结论）

**devin** — Devin 平台 SeatManagement：
- `POST /exa.seat_management_pb.SeatManagementService/GetUserStatus`：`devin-plugin/kernel/devin-core.mjs:2991-2993`（`userStatus`）
- 返回计划名/截止、`dailyQuotaRemainingPercent/dailyQuotaResetAtUnix`（1 天窗）、`weeklyQuotaRemainingPercent/weeklyQuotaResetAtUnix`（7 天窗）、`hideDailyQuota/hideWeeklyQuota`：`devin-core.mjs:3004-3037`
- 明确无签到：`kernel/tasks.mjs:4`「Devin has no such endpoint: there is no check-in API to call」；任务体系只有 quota refresh / pool heal
- 每调用 tokens（含 cached_tokens）：`kernel/kernel.mjs:397-404`；`/api/usage` 端点带缓存回源：`kernel.mjs:442-450`

**flonno2api** — 上游无余额查询端点（本次 grep 全 internal 无 `FetchBalance/QuotaUsage` 命中）：
- 池记录带 `Balance float64 / Quota int64`：`phase5/flonno/internal/pool/pool.go:42-43`
- 写入点只有两处：面板手工加号表单（`server/admin.go:53-54`，`balance/quota` 表单字段）与自动注册落池（`internal/autoreg/poolsink.go:27-28`）
- 调度器以池为「余额的唯一权威写者」：`internal/scheduler/scheduler.go:144`；无周期刷新（推测：上游 flonno.com 无对应接口，与 gap-analysis「积分与余额 ➕1 ❌3」一致）

**grok** — 三层里额度在内核账号域，外壳做聚合视图：
- 外壳 `QuotaWindow{Mode,Remaining,Total,UsagePercent,WindowSeconds,ResetAt,Source}` + `QuotaInfo{Type,Source,Confidence,Status,Unit,Used,Limit,Remaining,UsagePercent,LimitKnown,PeriodStart,PeriodEnd,ExhaustedAt,NextProbeAt}`：`phase4/grok/admin.go:160-193`
- 外壳经 admin `GET /accounts/summary` 拉账号列表：`admin.go:219-228`
- 内核配额引擎：`newQuotaView`（billing+observed+recovery 三源合成）：`grok2api/.../account/service.go:1173`；`DecrementQuota/ExhaustQuota/QuotaRefreshStats/QuotaRecoveryQueue`：`service.go:455-463,2954-2985`
- 耗尽语义：402 → `account.exhausted` 事件（`phase4/grok/failover_sync_test.go:146-163`）；provider.json `_hint`「额度耗尽只等待重置，不计入缺号」（`bin/providers/grok/provider.json`）
- 每号出口：`Account.EgressNodeID`：`admin.go:190`

**lobsterai** — Lobster 上游：
- `GET /api/user/profile-summary` → `totalCreditsRemaining`（含 free+campaign；注意 `/api/user/quota` 只含 free 300，是坑）：`internal/upstream/client.go:488-492`（函数注释 + 实现 491-516）
- 每日登录奖励无独立签到接口，从 `GET /api/user/credits-earned` 明细回读「每日登录奖励/Daily Login Reward」：`client.go:518-527`（`dailyLoginRewardNames` + `CreditRecord`）
- 刷新编排：`refreshCredits`（`scheduler.go:224-243`，`ReenableIfCredits`，remain==0 置长冷却 `pool.go:804-807`）；`RunDailyRewards` 签到流水（活动→领取→回读→刷余额）：`scheduler.go:263-298`
- 消耗聚合：按天 `usageDay{Calls,TokensIn,TokensOut,Points,...}` 留 90 天：`internal/server/store.go:324-343`；每调用 `Points` 进 reqEntry：`store.go:80`

**lobsterhub** — 与 lobsterai 同上游（Lobster）：
- `FetchQuota`：`GET /api/user/profile-summary`（带 keyfrom query）→ `TotalCreditsRemaining` + `CreditItems[]{Label,CreditsRemaining}` 分桶：`phase5/lobsterhub/client.go:149-170`
- `QuotaInfo{TotalCredits,AvailableCredits,Nickname,Phone,Email}`：`client.go:140-147`
- 签到=每日登录奖励确认（三步：触发→回读 credits-earned→确认到账才报成功）：`checkin.go:10-28,168`
- 记账：`onCallDone(CallRecord)` 每次成功调用后「刷新积分+记账」：`gateway.go:26-27,596-605`
- 耗尽判词：`isQuotaError`（额度已用光/免费额度/余额不足/积分不足/quota/insufficient/no credits/exhausted）→ 标记耗尽换号：`gateway.go:274-280`

**loomy**（以 phase5 1.9.0 为权威）— 讯飞 LOOMy 上游：
- 余额三口径 `points{balance,daily,available}`（+台账 `balanceBefore/After`）：`docs/loomy-panel-survey-2026-11-05.md` §1.2/§2（线上实测）；1.9.0 取数走 `getPointsCached`→`fetchPointsPage`（token+Bearer+traceparent 三头，旧单头会 900000）：`phase5/loomy/main.go:216-219`
- 每调用积分：上游 SSE 末帧 `usage.points_consumed` → reqEntry.Points → 按天/按模型聚合：`phase5/loomy/main.go:77`、`store.go:170,182,226,241`
- 宠物打工：`/api/v1/pet-work`（tier_1/2/3=60/100/150 分）+ 新手任务 8 项 10000 分：survey §1.2/§2（实测）
- ⚠️ `points_alert_threshold` 配置存在但 1.9.0 仍无读取逻辑（`phase5/loomy/plugin.go:154,157` 仅出现在 hint 与默认值），低水位告警=未实现

**mmxproxy** — MiniMax 双端点族：
- 余额：`/backend/account/token_plan_credit`（balance sync 期间取）：`phase3/mmxproxy/minimax.go:40`；`BalanceInfo` 快照含 `CreditsTotal/Remaining/PendingCredit`（get_membership_info）/`CreditSummary/CreditNums`（op_credit_summary）/`TodayClaimed/TodayPoints/SigninDays`：`store.go:65-84`
- 签到：`GET /minimax-cloud/api/v1/signin/status` + `POST /minimax-cloud/api/v1/signin/claim`：`minimax.go:485-510`
- 结算：签到赠送积分在**账号创建次日 UTC 0:00-0:10** 批量到账，`RetentionDays/SettleAt/Settled` 三字段落盘跟踪：`store.go:81-84`
- 消耗聚合：`usage.go` 按天 Points（`usage.go:28,460,470`）+ 全局 gateway 计数器（`store.go:106-110`）
- 持久化：Balance 挂在 Account 条目 `json:"balance"` 随 accounts.json 落盘：`minimax.go:50-54`

**qw2api** — QwenWork 上游：
- `GET /api/v2/quota/usage`（`PathQuota`）：`phase3/qw2api/internal/upstream/client.go:41`
- 原始响应 `QuotaUsage{UserID,UserType,TotalUsagePercent,IsHighestTier,IsQuotaExceeded,UpgradeURL,IsPlanQuotaProrated}`：`client.go:791-800`
- 池视图 `Quota{Remaining,Total,Used,Exceeded,PlanName,UserType,UserName,UserEmail,FetchedAt}`：`client.go:822-831`
- 本地双字段：`credits`（剩余）+ `initialCredits`（首次观测基准）→ `creditsSpent` 由**真实余额净下降**在 applyQuota 累加（不按估算）：`internal/pool/pool.go:54-60,77-89,448-455`
- 防呆：`preflight_credits`（已知余额低于阈值先实查再发请求）+ `refresh_interval_sec`（默认 300s 周期刷，把余额恢复的号重新激活）：`cmd/qw2api-plugin/main.go:364`、provider.json `_hint`
- 换号：`switch_threshold`（积分低于即切换）

**runbiosproxy** — RunBios 计费（契约全为 2026-11-03 浏览器实测，注释自带）：
- 链路：`POST /api/auth/login` → `GET /api/organizations` → `GET /api/workspaces` → `GET /api/billing/wallet`（`available_balance_cents/balance_cents/balance_dollars/total_spent_cents`）+ `GET /api/serverless/keys`（`month_spend_microcents`）：`BIOS-all/runbios_proxy/internal/accountinfo.go:1-22`
- 两个硬前提：wallet/keys 必须带 `X-Org-ID/X-Workspace-ID`；认的是**密码登录换的 access_token，不是 sk-bios API key**——池条目必须保存 password：`accountinfo.go:17-22`
- 快照落盘：`TotalSpent/MonthSpend/BalanceCheckedAt` 挂账号条目：`internal/admin.go:180-183`
- 低余额联动：一键停用（先刷余额再按阈值停）+ `low_balance_threshold/auto_disable_low_balance` 配置：`admin.go:700-731,928-929`
- 日限口径：`DailyLimitExhausted/DayCounterOf`（billing 包按自然日计数）：`internal/billing/account.go:58-67`
- 调用台账：`internal/billing/calllog.go` + `UsageSummary`（Calls/Tokens/Cost）：`admin.go:234-244`

**skywork** — 与 qw2api 同族的池结构（qw2api 机制蓝本复刻）：
- `Credits/initialCredits/creditsSpent/PreflightCredits` 全套同名字段：`phase5/skywork/internal/pool/pool.go:37,55,83-89,166-167`
- `switch_threshold/failover_rounds/refresh_interval_sec=300/clash_auto_switch`：部署 provider.json（`bin/providers/skywork/provider.json`）
- 上游取数同 qw2api 族 `FetchQuota`（本次未逐行读 skywork upstream 客户端，标记：**与 qw2api 同构，路径未逐一核实**）

**vibex** — RunningHub/VibeX，**无任何积分/余额能力**：
- 全文 grep `points|credit|balance`（server.js + panel.html）无余额类命中；仅有 SSE 末帧 `usage{input_tokens,output_tokens}` 透传：`vibex2api/plugin/src/server.js:692,758,974,2396-2399`
- `quota_token_multiplier`（server.js:1992）与「删除远端 app 释放 project quota slot」（server.js:1066,1279,1328）是 **relay 应用槽位管理**，不是账号积分
- `inbox_port`（provider.json=11519）是面板/cookie 同步inbox（`/panel/ping|cookie|account`，server.js:2750-2756），**不是临时邮箱**
- 部署实例名自述「号已封」（`bin/providers/vibex/provider.json` name 字段）——单 cookie 账号，无池

**xhx** — 商汤小浣熊：
- 余额：`GET /api/web/points/v1/balance`（`PathBalance`）；账单：`GET /api/web/points/v2/bills`；用户：`/api/web/auth/v1/user_info`：`phase5/xhx/internal/xhx/client.go:34-39`
- `Balance{AvailablePoints（权威余额）, DailyPoints, MonthlyPoints, RewardPoints, TopupPoints, EnterpriseTopupPoints, TotalChargeablePoints}`：`internal/xhx/types.go:240-253`；账单流水条目带 `Points`：`types.go:301`
- 归因难题：账单**没有模型名和 token 数** → 本地双路径：`Estimate`（内置单价：输入 1 分/千 token、输出 4、缓存 0.1）+ `Store`（EWMA 学习「余额净下降÷token」反推每模型真实速率）：`internal/points/pricing.go:1-40`
- 双口径记账：`MarkUsed` 存估算、`applyBalance` 用真实余额差值结算 `pointsSpent`：`internal/pool/pool.go:272-281`
- 短信/扫码会话无 refresh_token（`browserlogin.go` 注释实测结论）；无签到/邮箱通道

---

## 3. 统一 AccountQuota 数据模型

### 3.1 设计原则

1. **单位显式化**：同为「余额」，qw2api/skywork 是积分、runbios 是美元分（`balance_cents`）、devin/grok 上游给百分比——统一结构必须带 `Unit`，展示层负责换算/加后缀。
2. **「0」与「未知」分离**：lobsterai 上游只给 free 300 的旧端点是坑（client.go:490 注释）、flonno 注册前余额未知、xhx 首次估算——用 `Source/Confidence` 表达可信度，不用哨兵值。
3. **窗口化**：grok 多窗口（五小时/日/周）、devin 日+周百分比、loomy 日额度——用 `Windows[]` 承载，单窗口插件退化为一元素。
4. **分桶**：buddyhub 快过期、lobsterhub CreditItems、xhx 五桶、mmx 待入账——用 `Buckets[]` 承载。
5. **消耗与余额分离**：余额是上游快照（可刷新），消耗是本地流水（只增），两者口径在 mmxproxy（签到获得≠调用消耗）和 xhx（估算≠结算）上必须分开字段。

### 3.2 字段定义（Go 参考定义 + 可选性）

```go
type Unit string // "points" | "credits" | "usd_cents" | "tokens" | "percent" | "unknown"

type Source string // "upstream-api"（上游端点直查）| "deduction"（余额净下降反推）
                   // | "local-estimate"（本地估算/学习）| "registration"（注册快照）| "none"

type Confidence string // "exact" | "estimated" | "stale" | "unknown"

type QuotaBucket struct { // 可选：积分分桶
    Label     string `json:"label"`               // 必须：展示名（"7日内到期"/"每日积分"/"待入账"）
    Remaining float64 `json:"remaining"`           // 必须：桶内剩余
    ExpiresAt string `json:"expiresAt,omitempty"` // 可选：到期时刻（buddyhub CycleEndTime 类）
}

type QuotaWindow struct { // 可选：时段配额窗口（grok/devin/loomy日额度）
    Kind      string  `json:"kind"`                  // 必须："daily" | "weekly" | "five_hour" | "custom"
    Percent   float64 `json:"percent,omitempty"`     // 可选：剩余百分比（devin/grok）
    Remaining float64 `json:"remaining,omitempty"`   // 可选：剩余绝对值（grok）
    Total     float64 `json:"total,omitempty"`       // 可选：窗口总额（grok）
    ResetAt   string  `json:"resetAt,omitempty"`     // 可选：RFC3339 重置时刻（devin unix→转）
    Hide      bool    `json:"hide,omitempty"`        // 可选：上游要求隐藏（devin hideDailyQuota）
}

type AccountQuota struct {
    // ── 余额（积分/余额口径）──
    Unit       Unit          `json:"unit"`                    // 必须；Source==none 时为 "unknown"
    Remaining  float64       `json:"remaining,omitempty"`     // 尽力提供；无来源时 0 且 Source=="none"
    Total      float64       `json:"total,omitempty"`         // 可选：上游给总额才填（qw2api/xhx/grok）
    Buckets    []QuotaBucket `json:"buckets,omitempty"`       // 可选
    // ── 消耗（本地流水口径）──
    UsedToday     float64 `json:"usedToday,omitempty"`      // 可选：今日消耗（积分口径；无则由调用日志聚合）
    CallsToday    int64   `json:"callsToday,omitempty"`     // 可选：今日调用次数
    SpentTotal    float64 `json:"spentTotal,omitempty"`     // 可选：累计消耗（qw2api credits_spent / xhx points_spent / runbios total_spent）
    SpentMonth    float64 `json:"spentMonth,omitempty"`     // 可选：本月消耗（runbios month_spend）
    // ── 窗口 ──
    Windows    []QuotaWindow `json:"windows,omitempty"`       // 可选：devin/grok/loomy
    // ── 状态与元数据 ──
    Exhausted  bool    `json:"exhausted,omitempty"`          // 可选：上游明确告知耗尽（qw2api IsQuotaExceeded / grok Status / mmx isQuotaError 族判词）
    PendingCredit float64 `json:"pendingCredit,omitempty"`   // 可选：待入账（mmx PendingCredit）
    CheckedAt  string  `json:"checkedAt"`                    // 必须：最近一次余额同步时刻（runbios balance_checked_at 同义）
    NextSyncAt string  `json:"nextSyncAt,omitempty"`         // 可选：下次自动同步（qw2api/skywork refresh_interval_sec 推导）
    Source     Source  `json:"source"`                       // 必须
    Confidence Confidence `json:"confidence"`                // 必须
}
```

### 3.3 字段 × 插件可用来源映射

| AccountQuota 字段 | buddyhub | devin | flonno | grok | lobsterai | lobsterhub | loomy | mmxproxy | qw2api | runbios | skywork | vibex | xhx |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| Unit | points | percent | points | percent/窗口 | points | points | points | points | credits | usd_cents | credits | — | points |
| Remaining | ✅ upstream-api | ❌（仅窗口%） | ⚠️ registration | ✅ upstream-api | ✅ upstream-api | ✅ upstream-api | ✅ upstream-api | ✅ upstream-api | ✅ upstream-api | ✅ upstream-api | ✅ upstream-api | ❌ none | ✅ upstream-api |
| Total | ❌ | ❌ | ⚠️ | ✅ Limit | ❌ | ✅ TotalCredits | ⚠️（日额度1000） | ⚠️ CreditsTotal | ✅ Total | ❌ | ⚠️ | ❌ | ✅ TotalChargeablePoints |
| Buckets | ✅ Expiring | ❌ | ❌ | ✅ QuotaWindows | ❌ | ✅ CreditItems | ✅ daily/available | ✅ Pending+summary | ❌ | ❌ | ❌ | ❌ | ✅ 五桶 |
| UsedToday | 🧮 聚合 | ❌ | 🧮 聚合 | ✅ Used | 🧮 usageDay | 🧮 记账 | 🧮 points_consumed | 🧮 usage.go | 🧮 净下降 | ❌ | 🧮 净下降 | ❌ | 🧮 净差值 |
| CallsToday | ✅ today_calls | 🧮 usageCount | ✅ DayCalls | 🧮 stats | 🧮 usageDay | 🧮 CallRecord | 🧮 store | 🧮 计数器 | 🧮 reqCount | 🧮 calllog | 🧮 池 | ❌ | 🧮 MarkUsed |
| SpentTotal | 🧮 usage-log | ❌ | ❌ | ❌ | 🧮 聚合 | 🧮 记账 | 🧮 台账 | 🧮 聚合 | ✅ credits_spent | ✅ total_spent | ✅ credits_spent | ❌ | ✅ points_spent |
| SpentMonth | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ month_spend | ❌ | ❌ | ❌ |
| Windows | ⚠️（限流台账可入此） | ✅ 日+周 | ❌ | ✅ 多窗口 | ❌ | ❌ | ✅ 日额度 | ❌ | ✅ TotalUsagePercent | ❌ | ⚠️ 同 qw2api | ❌ | ⚠️ Daily/Monthly 桶可入此 |
| Exhausted | ✅（余额=0 硬冷却） | ✅（429+判词） | ✅（状态机） | ✅ ExhaustedAt | ✅ CoolHard | ✅ isQuotaError | ❌ | ✅（未限流/未死亡口径） | ✅ IsQuotaExceeded | ✅ DailyLimitExhausted | ✅ | ❌ | ✅（threshold 判耗尽） |
| PendingCredit | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |
| CheckedAt | ✅ | ✅（quota task 时刻） | ⚠️ 注册时刻 | ✅ NextProbeAt | ✅ | ✅ | ✅ | ✅ UpdatedAt | ✅ FetchedAt | ✅ balance_checked_at | ✅ | ❌ | ✅ |
| Source | upstream-api | upstream-api | registration | upstream-api | upstream-api | upstream-api | upstream-api | upstream-api | upstream-api | upstream-api | upstream-api | none | upstream-api+local-estimate |

> 图例：✅ 直接可得；⚠️ 部分可得/口径需换算；🧮 本地聚合/推算；❌ 拿不到；「—」不适用。

### 3.4 刷新节奏（现网实际配置）

| 插件 | 周期刷新 | 手动刷新 | 备注 |
|---|---|---|---|
| qw2api / skywork | `refresh_interval_sec` 默认 300s（写 0 关闭），单号预算 `refresh_account_timeout_sec` + 整轮预算 `refresh_total_budget_sec`（夹 60s~10min） | 面板「刷新积分」 | qw2api provider.json `_hint`：刷新负责把「余额恢复的号」重新激活，否则全池 503 |
| loomy | pointsCache 60s 缓存（防面板轮询打爆上游） | — | survey §1.3 |
| buddyhub | 无周期；签到时回填 + 面板「刷新积分」（并发 3） | ✅ | credits.go 注释：global 账号不签到不查就是 0 |
| devin | 任务体系 quota task（周期+手动） | ✅ | tasks.mjs:8「quota refresh: GetUserStatus per account」 |
| grok | 内核 QuotaRefreshCoordinator/NextProbeAt 自调度 | ✅ 内核 admin | service.go:459-463 |
| lobsterai | 每日奖励流程顺带全量刷 + 周期扫描（MarkCreditPass 防重复） | RefreshOne（新号登录即刷） | scheduler.go:224,286-289 |
| runbios | 无周期；停用前先刷 | ✅（含批量） | admin.go:700-731 |
| mmxproxy | balance sync 后台循环 + 每日签到循环 | ✅ | minimax.go:40,523-542 |
| xhx | `refresh_interval_seconds` 默认 300s | ✅ | 部署 provider.json |

---

## 4. 展示字段清单（对齐 mockup 账号池页）

mockup 账号池表头（`docs/design/console-mockup.html` 账号池视图，表格 thead）：**账号 / 状态 / 积分 / 余量(110px 余量条) / 今日调用 / 累计消耗 / 最近使用 / 出口 / 操作**（另有全选框与批量操作：刷新积分/批量签到/批量停用）。

| 列 | 数据来源（AccountQuota/池字段） | 各插件口径 | 缺数据时的降级 |
|---|---|---|---|
| 账号 | uid/邮箱/昵称 | 各池均有（devin email、buddyhub 昵称、xhx 手机号） | — |
| 状态 | 池状态机 + `Exhausted` | active/cooling(限流)/exhausted(耗尽)/dead(凭据死)/disabled(手动)——buddyhub 已有三档计数健康度（survey A1）；devin health.quotaScore（tasks.mjs:34-47） | 无池的 loomy/vibex 退化为单卡「登录态」 |
| 积分 | `Remaining` + `Unit` 后缀 | qw2api/skywork「积分」、runbios「$x.xx」、xhx「分」、devin 无绝对值→显示窗口% | vibex/flonno(未刷) →「—」 |
| 余量条 | `Remaining/Total`；退化链：`Windows[].Percent`（devin/grok）→ 阈值相对刻度（xhx 以 `switch_floor_points` 为红线） | grok `UsagePercent`、qw2api `TotalUsagePercent` 直接可用 | Total 未知→条形图隐藏只留数字 |
| 今日调用 | `CallsToday` | buddyhub `today_calls`（引擎已有面板未取）、flonno `DayCalls`（重启恢复：scheduler.go:134-135）、devin `usageCount` | 无本地计数的（grok/vibex）→ 宿主网关中继侧计数（**推测**：宿主统计链路未在本次核查范围，接通方式待宿主侧确认） |
| 累计消耗 | `SpentTotal`（+`SpentMonth` 副标） | qw2api/skywork `credits_spent`、xhx `points_spent`、runbios `total_spent_cents`、其余走本地聚合 | 聚合类→「近90天」标注（lobsterai usageKeepDays=90）；devin/vibex →「—」 |
| 最近使用 | 池 `LastUsedAt` 类字段 | flonno `last_used_at`（pool.go:45）、buddyhub `last_success` | 无则取调用日志末条时刻；再无→「—」 |
| 出口 | 池/账号出站配置 | grok `EgressNodeID`（admin.go:190）、devin 独占节点绑定（egress.mjs:11-17）、mmx/qw2api/skywork/runbios/lobsterhub clash 组名、lobsterai/flonno 全局 clash、其余「直连」 | 无代理层（buddyhub/xhx）→「直连」固定文案 |
| 操作 | 单号：刷新积分/停用/启用/删除/签到 | 各池均有对应端点或等价操作 | — |

积分与余额页（mockup `view-credits`）另需三块，均已被插件现状支撑：
- 三 KPI：池总积分+预估可用天数（可用天数=余额÷近3日均耗，survey §4 已判可算）、今日消耗+较昨日趋势、低余额账号数（阈值：runbios `low_balance_threshold`、xhx `switch_floor_points`、loomy `points_alert_threshold`（⚠️ 未实现读取））
- 同步进度条：并发数/成功/失败/待处理/耗时——buddyhub `refreshCredits` 现为同步阻塞返回（survey C3），mmxproxy 已有 `syncRunning/syncDone/syncTotal`（store.go:108-110）
- 逐账号余额表：账号/剩余/总额/待入账/结算/同步于——「待入账」仅 mmxproxy `PendingCredit` 真实可得，其余一律「—」（buddyhub survey §4 已判上游不提供）

---

## 5. 已知坑位（模板实现必须吸收）

1. **lobsterai 旧端点陷阱**：`/api/user/quota` 只回 free 300 不含活动积分，必须用 `/api/user/profile-summary`（client.go:490 注释）。
2. **loomy 上游鉴权三头**：points 查询必须 token+Bearer+traceparent 组合，缺 traceparent 上游挂死（main.go:216-218、survey §5.4）。
3. **runbios 需存密码**：余额端点认登录 token 不认 API key，且必须带 org/workspace 头（accountinfo.go:17-22）——统一模板的凭据模型要允许「密码型账号」。
4. **xhx 账单无归因**：账单流水没有模型名/token，按模型成本只能本地学习（pricing.go）——统一消耗台账要接受「估算+结算」双口径。
5. **mmx 结算时差**：签到积分次日 UTC 才到账，`SettleAt/Settled` 防止「显示签到成功但余额没动」（store.go:81-84）。
6. **buddyhub 多进程一致**：data/state.json 多子进程共享，面板读到的 credits 可能滞后——文案按「最近一次同步」（survey §3.3），与 mockup「上次同步 14:22」一致。
7. **flonno 无刷新**：Balance 是注册快照，无上游查询端点——额度列在该插件只有注册时刻的意义。
8. **loomy 告警未实现**：`points_alert_threshold` 配置存在但无读取逻辑（plugin.go:154,157）——模板若做低水位告警，需补实现而非仅透出配置。
9. **devin 无绝对值**：百分比窗口即全部——「积分」列对 devin 无意义，余量条必须走 `Windows[].Percent` 分支。
10. **vibex 全空**：无余额/无池/无落盘，额度视图整列「—」；它是「统一模板必须容错最差情况」的测试用例。
