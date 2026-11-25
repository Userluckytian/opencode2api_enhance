# 03 — 负载均衡/错误转移/无感切号 · 多并发注册 · 出口代理 · 定时任务（需求 3+4+5+6）

> 轮次约定：本轮**只做分析与设计，不写实现代码**；接口与 schema 均为「设计草案」。
> 证据约定：所有结论标注 `文件:行号`（相对各项目根，见 §0.1 路径对照表）；本会话逐行读过的一手证据；「推测」= 无源码证据、由机制推断。
> 日期：2026-11-07

## 0. 范围、路径与结论速览

### 0.1 路径对照表（下文引用均用短路径）

| 短名 | 绝对路径 | 语言 | 定位依据 |
|------|----------|------|----------|
| **宿主** | `D:/AI_Projects/opencode2api_guide/opencode2api_enhance` | Go | 本仓库（manifest refs） |
| **DeepAI** | `D:/AI_Projects/refs/DeepAICode2API` | Go 1.24 纯标准库 | manifest refs |
| **flonno** | `D:/AI_Projects/_plugin-work/phase5/flonno` | Go | 部署 provider.json `_source.repo`（manifest，本会话未复读部署文件） |
| **mmx** | `D:/AI_Projects/_plugin-work/phase3/mmxproxy` | Go | 部署 `_source.repo`（manifest） |
| **qw** | `D:/AI_Projects/_plugin-work/phase3/qw2api` | Go | 部署 `_source.repo`（manifest） |
| **sky** | `D:/AI_Projects/_plugin-work/phase5/skywork` | Go | 部署 `_source.repo`（manifest） |
| **grok** | `D:/AI_Projects/_plugin-work/phase4/grok` | Go 外壳 | 部署 `_source.repo`（manifest；宿主仓库 `providers/grok` 为 0.1.0 旧代勿用） |
| **bios** | `D:/AI_Projects/BIOS-all/runbios_proxy` | Go | 部署 provider.json 无 `_source`；manifest 定位依据 = `plugin-dist/runbiosproxy/provider.json` v0.2.0 与部署实例一致（**本会话未复读部署文件，据 manifest 转述**） |
| **devin** | `D:/AI_Projects/Devin-all/devin-plugin` | Node .mjs 内核 + Go 壳 | 部署 provider.json 无 `_source`；manifest 依据 = VERSION 1.2.1 与部署一致；本会话验证：源码 `provider.json` 与部署 `D:/Program Files/opencode2api/bin/providers/devin/provider.json` 的 `"strategy": "round-robin"` 一致（grep 实测两处命中） |
| **buddy** | `D:/AI_Projects/_plugin-work/phase5/buddyhub`（源码基线 `D:/AI_Projects/buddyhub`，`internal/scheduler/scheduler.go` 两处同名同构） | Go | `docs/buddyhub-panel-survey-2026-11-05.md` 头部「源码基线：D:\AI_Projects\buddyhub」 |

**覆盖度声明**：统一模板的目标是覆盖 13 个插件供应商；本轮 manifest 只给了 7 个插件的路径（上表），其余 6 个未提供路径、未分析、不作任何论断。manifest 中 grok-register-cloudflare-temp-email（Python 注册机）只与本轮四项需求间接相关（grok 的 `register/` 外部进程），未逐行分析。

### 0.2 结论速览

| 需求 | 现状最大公约数 | 统一模板归宿 |
|------|----------------|--------------|
| 3 负载/切号 | 「策略 + 错误分类 + 冷却账本 + 尝试预算」四件套在 5 个 Go 插件与宿主中各自实现了 4~5 次 | `routing` 包：Strategy / ErrClass→Action 映射 / Cooldown 账本 / Failover 循环（含首帧闸） |
| 4 多并发注册 | DeepAI 有完整 worker-pool + 每任务独立出口；grok 有 threads 上限闸；其余为串行补号 | `register` 包：RunBatch 语义（并发、节奏、出口绑定、台账幂等补跑） |
| 5 出口代理 | 四种模式并存：全局列表 / Clash 组切换 / Clash 每号钉死端口 / 独占绑定 | `egress` 包：一个接口四种实现 + 最小 Clash 外控客户端 |
| 6 定时任务 | buddyhub 的 slot+确定性抖动+(天,档)去重是唯一成体系的框架；其余是 ad-hoc ticker | `schedule` 包：Spec（slot/jitter/window/interval）+ Tracker + Run 循环 |

---

## §1 需求 3：负载均衡 / 错误转移 / 无感切号

### 1.1 机制分层

各项目实际是三层叠加（宿主最完整）：

1. **厂商级 failover**（同模型多供应商候选，响应头提交前换）——宿主独有；
2. **账号级调度**（单供应商内多号：选号策略 + 错误分类 + 冷却）——所有插件都有；
3. **流内无感续写**（SSE 首帧已出后的换号/续写）——宿主 `streamWithResume` 独有；qw/bios 只做到「首帧前可换、首帧后报错」，devin 只做到「写头前可换」。

### 1.2 选号策略对比

| 机制 | 项目 | 配置项 | 证据 | 备注 |
|------|------|--------|------|------|
| 轮询 round_robin | 宿主 | `providers[].params.key_strategy`（缺省） | `vendors/custom/keypool.go:26-30,176-199`（`rr` 游标） | key 级；`tryAcquirePreferSticky` 命中 sticky 不推进游标（`keypool.go:139-144`） |
| | flonno | `strategy`（`rr` 别名） | `internal/scheduler/types.go:14,37-39`；`internal/scheduler/scheduler.go:22`（`rr int` 游标） | 账号级 |
| | bios | `settings.strategy=round` | `internal/scheduler.go:22-48`（`atomic.Value` 热更）；`internal/pool.go` | 账号级 |
| | devin | `provider.json strategy=round-robin` | `kernel/kernel.mjs:39,125-128`（`rr` 游标 `ordered.push(accounts[(rr+i)%n])`） | 部署实例同值（本会话 grep 两处 provider.json 实测） |
| | mmx | 无配置 | `store.go:411-478`（`schedPos` 游标） | **粘直到失败才轮转**：同号持续服务直到 429/401/网络错才 `advanceSched()`（`store.go:419-426`）——其余项目都是请求级轮换 |
| | DeepAI | 分组（key→group） | `internal/store/store.go:361-389`（`NextAccountGroup`）；`internal/server/common.go:440-455`（请求密钥绑定分组） | 两轮选择：先额度未知或>0，再全集兜底（`store.go:367-369` 注释）；轮询时偏向 `Failures` 最少（`store.go:407-414`） |
| 主备粘主 failover | 宿主 | `key_strategy="failover"` | `keypool.go:26-30,121`（「failover 恒从头找」） | |
| 健康分 health | 宿主 | `key_strategy="health"` | `keypool.go:201-237`（成功率 EWMA 降序→样本数→延迟升序）；`:45-46`（`healthAlpha=0.3`） | 借鉴 model-gateway（`keypool.go:7`）；流中失败回调拉低健康分 `vendors/custom/custom.go:615-620` |
| 最少在途 least_connections | flonno | `strategy=lc` | `types.go:15,40-42`；`scheduler.go:22`（`InFlight` 字段 `types.go:76`） | |
| | bios | `strategy` | `internal/scheduler.go:69-91`（Acquire/pick 双路径 least） | |
| | devin | `least-used` | `kernel.mjs:122-124`（按 `usageCount` 排序） | 用量为累计次数，非在途（推测：等价近似） |
| 加权随机 weighted | flonno | `strategy=weighted` | `types.go:16,43-45`；`scheduler.go:342-390`（余额比例因子、全集口径 maxBalance、等权洗牌、Top-N 截断、LRU 序号兜底）、`:392-447`（`weightedLottery` int64 定点 + 保底权重 ≥1，对齐 buddyhub） | 三因子：余额 + 闲置补偿（`types.go:16` 注释） |
| | bios | `weighted` | `internal/pool.go:180-184` | |
| 粘性会话 sticky | 宿主 | 内部（无键） | `keypool.go:135-144`（stickyIdx 优先、不推进游标）；`custom.go:538,585`（`withKeys(…, stickyIdx, …)`） | 续写同 key 防重复输出（`keypool.go:136-138` 注释） |
| | bios | `sticky` + `session StickyResolver` | `internal/scheduler.go:111` | 会话键解析器注入 |
| | devin | `sticky` + `stickyKey` | `kernel.mjs:68,118-121,394,407`（成功后置 stickyKey 并持久化 routing.json） | 全局单粘性位，非会话哈希 |
| | qw | 无 sticky，但换号重发强制换 request/session id | `internal/server/failover.go:158-161`（`refreshRequestIDs`，防上游把两次请求算同一次） | 反向约束，值得进模板 |
| 配额感知调度 | qw | `switch_threshold`(缺省 5.0)、`preflight_credits` | `internal/config/config.go:28,38,162-163`；`internal/pool/pool.go:135-137,418-426,476,644-649`（余额≤阈值跳过/降级、preflight 取号前实查） | 把「余额恢复的号重新激活」（manifest 备注） |
| | DeepAI | 账号字段 Theos 余额 | `store.go:426-429`（`hasQuota`：只有探测过且=0 才算耗尽，保守为准） | |
| | flonno | `Balance` 权重因子、`DayCalls/DayStamp` 每日上限 | `types.go:82-89`；`scheduler.go:352-357`（maxBalance 全集口径注释引用 buddyhub） | |

### 1.3 错误 → 动作映射（402/429/5xx）

| 项目 | 分类载体 | 映射表 | 证据 |
|------|----------|--------|------|
| 宿主 | `contract.ErrRules{Retryable/Switchable/BadPool}`（按厂商差异化声明，替代「401/402/429/503 写死」） | Switchable 状态码 → 换下一候选厂商 | `core/contract/contract.go:192-197`；`upstream.go:185-196`（`shouldSwitchVendor`） |
| 宿主（key 级） | HTTP 状态码 switch | 401/403→`authFail`（冷却或禁用）；429→`cool`（Retry-After，缺省 60s）；404 且并集目录有该模型→换 key；5xx/408→不标记换 key；**400 等请求级错误→立即返回** | `vendors/custom/custom.go:561-573`（非流式）；`:625-635`（流式）；`keypool.go:317-325`（authFail 分流） |
| 宿主（流中） | 文案+状态码弱判断 | 5xx/429/rate/quota/额度→切换续写；400 类→透传不切 | `gateway_timeout.go:667-675`（`isSwitchableStreamError`）；`:876-890`（SSE error 事件处理，含「上游中途发 bare JSON error 后关流」的识别 `:816-852`） |
| 宿主（统计口径） | `ErrorClass` 八类：rate_limit/quota/auth/upstream/transport/protocol/timeout/other（勘误 2026-11-07：原稿「七类」与所列 8 项自相矛盾；`fail_counter.go:22-29` 实读即 8 个常量） | 失败计数（厂商×原因），回退到状态码与文本匹配 | `fail_counter.go:21-30,39-81` |
| flonno | `FailureReason` 六枚举 + `Classify/ClassifyStatus` | 401→unauthorized、402→insufficient_quota、429→rate_limited、5xx→upstream_server_error，全部「值得换号」；context.Canceled→不换号（调用方自身问题） | `internal/scheduler/types.go:57-66,148-170`；`internal/scheduler/failover.go:55-90`（`Classify` 判定顺序） |
| bios | `failoverClass` 五类 | 401→credential、**402→balance（用户明确要求无感切号，402 后该号置 exhausted）**、429→rate_limit、408/425/5xx→transient；其余→fatal 直接回客户端 | `internal/dispatcher.go:239-263`；402→exhausted 见 `:242` 注释 |
| qw | 状态码 + 余额 | 401/402/403 建连失败→`ReportFailure` 标号并换；见底（2056 等）→阈值下跳过 | `internal/server/failover.go:182-187`；`internal/pool/pool.go:418-426` |
| devin | 开关位 | 401→`retry401`、429→`retry429`、≥500 或模型下架→`retry5xx`；不命中即停 | `kernel/kernel.mjs:420-424` |
| mmx | 上游错误码（如 2056 限流） | 429/401/网络错→游标前进；余额缓存不可靠，不做预防性切换（`store.go:441-447` 注释明确踩坑） | `store.go:419-426,441-455` |

**共性结论**：402=号额度耗尽（换号并标记）、429=限流（换号 + 按服务端 Retry-After 冷却）、401/403=凭据失效（冷却/禁用）、5xx=上游侧（换号但不惩罚号）、400 类=请求级（不换）、客户端断开=不惩罚不续写（宿主 `gateway_timeout.go:760` `clientGone`；flonno `failover.go:69-71`）。这套语义可直接固化为模板默认表。

### 1.4 冷却 / 耗尽标记 / 熔断

| 机制 | 项目 | 证据 | 参数 |
|------|------|------|------|
| 429 冷却（Retry-After 优先） | 宿主 | `keypool.go:277-288`（`cool`）+ `:378-393`（`parseRetryAfter`：秒数或 HTTP 日期，兜底 60s） | 缺省 60s（`keypool.go:33`） |
| 401/403 冷却 vs 永久禁用 | 宿主 | `keypool.go:36-37,290-325`（`key_403_cooldown>0` 冷却到期自动回池；=0 旧行为永久禁用；authCool 顺带清掉限流/熔断状态防叠加 `:301-304`） | 缺省 600s |
| 连败熔断 | 宿主 | `keypool.go:39-43,265-275`（连续 3 次失败→熔断 30s，成功清零；三策略都受益） | 阈值 3 / 30s |
| 失败状态机 | flonno | `scheduler.go:502-526`（`ReportFailure`：MaxFailures 达→disabled；Cooldown>0→cooldown；否则 failed）；成功回 active 并清冷却（`:549-570`） | `MaxFailures/Cooldown` 启动期常量 |
| 冷却时长按原因区分 | flonno | `types.go:57-63` 六种原因 → 调度器按 reason 记账（`scheduler.go:511-512`）；**冷却时长如何按 reason 取值未在本会话读到配置证据**（推测：由 server 层按 reason 决定后调用 ReportFailure/Cooldown） | |
| 402 置 exhausted | bios | `dispatcher.go:242`（注释：402 后该账号置 exhausted，后续不再选它） | |
| 阈值降级 | qw | `pool.go:644-649`（Remaining>阈值→从 exhausted 恢复；≤阈值→标记）+ `refresh_interval_sec` 周期刷积分把恢复的号拉回来 | `cmd/qw2api-plugin/main.go:164-166,289-292`（热改间隔，0=关闭） |
| 连败暂停（注册侧） | grok | `keeper.go:264-293`（`ConsecutiveFailures≥3`→`PausedUntil=now+2h`；成功清零） | 写死 3 次/2h |
| 出口冷却账本（另一维度） | 宿主免费层 | `core/manager/freetier_cooldown.go:1-30`（WARP 出口 /64 前缀→解冻时刻，落盘 `config.json.warp-state.json`；坏文件显式区分于「0 条冷却」`warpCoolingState.Corrupt`） | 证明「冷却账本要落盘 + 坏态要显式」是已被踩出来的需求 |

### 1.5 粘性阈值与「无感切号」窗口

**未提交响应头前换号**（对客户端零感知）：

| 项目 | 判据 | 证据 |
|------|------|------|
| 宿主（厂商级） | `callOpenCodeAPIOnce` 在候选数组上循环，2xx 才返回；handler 拿到结果后才 `WriteHeader`（`chat_handler.go:154-157`） | `upstream.go:245-276`（非流式）、`:299-330`（流式单模型循环） |
| qw | 「只要还没向客户端吐出任何字节，就允许挑下一个账号重发同一次请求」；**判据 = 响应头没写 + SSE 首帧没出**；等待首帧的窗口 `firstByteGate=20s`（思考型模型首包慢，不能无限压着） | `internal/server/failover.go:1-12,25-31`；换号重试轮数 `failover_rounds` 缺省 3、1=关闭、上限夹紧 1..20（`failover.go:74-88`；配置键实测 `cmd/qw2api-plugin/main.go:457-462`） |
| bios | 非 2xx 先读走错误体、不立即回写；重试耗尽或不可换号错误才把最后一轮错误写给客户端（修「402 直接返回、换号成功也看不到」） | `internal/dispatcher.go:129-143,207-235` |
| devin | 全部尝试完成后才 `res.writeHead`；成功即写并带 `x-devin-account`/`x-devin-tried` 诊断头 | `kernel.mjs:370-418` |

**流中断后续写**（首帧已出，宿主独有能力）：

- TTFT/静默/预内容静默三段计时器（`gateway_timeout.go:62-88` 随机化、`:767-776` 选择逻辑——「已连接未吐内容」用更大的宽容窗避免把思考误杀成空回复）；
- 中断后把已吐内容做上下文续写重连，续写重试上限 = `min(maxRouteRetries,3)`（`:682-684`）；
- `triedAddrs`：流中断后标记冷却，重连强制换节点（`:696-718`）；
- EOF 无 [DONE] 且已吐内容 → 不再续写（防「请继续」诱导从零重答造成双回复混合，`:762-765,788-796`）；
- 续写换 key 时**不推进轮询游标**（`keypool.go:136-138`），同会话续写稳定落同 key；
- 换号后新节点重新加「🤖 节点 · 模型」前缀（每节点一次，`:732-733`、`:942`），token 统计跨节点累加修正（`:1023-1026`）。

**尝试预算与豁免**：

- qw：`rounds` 次内 `excluded` 排除集；「没有别的号可换」时，若排除集里只是临时故障的号，**清空排除集再给一轮**（`relaxed`，`failover.go:90-113`）——防临时抖动导致全池被排除；
- flonno：`MaxAttempts<=0` 时预算 = 当前可用账号数（`failover.go:157-164`）；逐号事件化（`failover.attempt`/`retry_next`/`failover.request.done`，`:170-208`）；
- devin：`CFG.maxSwitch` 上限（`kernel.mjs:370`）；
- 宿主：厂商循环上限 = 候选数；流内续写 ≤3。

### 1.6 统一 Go 设计草案：`routing` 包

目标：一个包覆盖「选号策略 + 错误语义 + 冷却账本 + 换号循环」，13 个供应商以配置差异复用。全部标准库，账号状态 JSON 落盘（对齐 flonno `pool.go:1-18` 的「调度器持运行态、池管名册与落盘」分界）。

```go
package routing

// ErrClass 统一错误类别（勘误 2026-11-07：原稿称「宿主 fail_counter.go:21-30 与 flonno types.go:57-63 的并集」不成立——宿主八常量 fail_counter.go:22-29 含 timeout 无 client_gone，字面并集应为九类；本集实为语义重构：六类与宿主八常量对齐且 timeout 并入 transport（见 :143 注），flonno FailureReason 六枚举（internal/scheduler/types.go:57-66）逐项可映射，client_gone 自宿主流中断语义（gateway_timeout.go:760 clientGone；flonno failover.go:60,78 context.Canceled/DeadlineExceeded 不换号）提升为独立类）。
type ErrClass string
const (
    ClassAuth       ErrClass = "auth"        // 401/403：凭据失效
    ClassQuota      ErrClass = "quota"       // 402/余额见底：号耗尽
    ClassRateLimit  ErrClass = "rate_limit"  // 429：限流
    ClassUpstream   ErrClass = "upstream"    // 5xx：上游侧
    ClassTransport  ErrClass = "transport"   // 拨号/超时/断流
    ClassProtocol   ErrClass = "protocol"    // 非 SSE/解析失败
    ClassClientGone ErrClass = "client_gone" // 客户端断开：不惩罚不续写
    ClassOther      ErrClass = "other"
)

// Action 是一次失败后的动作（按 ErrClass 查默认表，可被配置覆盖）。
type Action int
const (
    ActSwitch  Action = iota // 换下一个号（不惩罚或按冷却惩罚）
    ActCool                  // 冷却 + 换号
    ActExhaust               // 标记耗尽 + 换号
    ActFatal                 // 直接把错误回给客户端（400 类）
)

// Failure 是 Classify 的输出。
type Failure struct {
    Class      ErrClass
    Action     Action
    RetryAfter time.Duration // 429 时取上游 Retry-After（宿主 keypool.go:378-393 同款解析）
    Err        error
}

// Classifier 把 error / 状态码 / 错误体映射为 Failure；供应商可用 ErrRules 覆盖状态码语义
// （对齐宿主 contract.go:192-197 的差异化 ErrRules 设计）。
type Classifier interface {
    Classify(err error, status int, body []byte) Failure
}

// Strategy 选号（P 对应 flonno 的 Account 快照：InFlight/Failures/CooldownUntil/State，
// types.go:96-116）。
type Strategy interface {
    Pick(exclude map[string]struct{}) (P, func(), error) // P=账号运行态快照（flonno types.go:73-117 的 Account：InFlight/Failures/CooldownUntil/State）；release 闭包（flonno scheduler.go:480-483 语义）
    ReportSuccess(id string)
    ReportFailure(id string, f Failure)                  // 内部按 f.Class/Action 做状态转移（flonno scheduler.go:502-526 语义）
}

// 内置实现（与现状一一对应，经配置选择）：
//   RoundRobin / StickyUntilFail(mmx) / Failover(粘主, 宿主) / LeastInFlight(flonno lc)
//   Weighted(余额+闲置, flonno) / Health(EWMA, 宿主) / TwoRoundQuota(DeepAI 两轮额度优先)
// 会话粘性作为装饰器：StickySession{Next Strategy, Key func(*http.Request) string}
//（宿主 stickyIdx 语义 + bios StickyResolver 语义合并）。

// Failover 循环（qw failover.go 语义为骨架）：
//   - attemptBudget：failover_rounds，<=0 取可用号数（flonno failover.go:157-164）
//   - excluded 排除集 + relaxed 豁免轮（qw failover.go:90-113）
//   - firstByteGate：首帧窗口 20s，窗口内无输出视为可换号（qw failover.go:25-31）
//   - consume 返回 outcome{Done, Written, Failover, ClientGone}（qw failover.go:47-52）
//   - 换号重发强制 refreshRequestIDs（qw failover.go:158-161）
type Failover struct{ /* budget, gate, classifier, strategy, sink(事件) */ }
func (f *Failover) Dispatch(ctx context.Context, call func(ctx context.Context, p P) error) error

// 流中续写（宿主 streamWithResume 能力下沉为可选组件）：
//   - Switchable(class) bool：quota/rate_limit/upstream 可续写切换；auth/fatal 透传
//   - maxResume 上限（宿主 gateway_timeout.go:682-684 的 ≤3）
//   - triedAddrs 强制换出口；EOF 无 [DONE] 已吐内容则终止（:762-765）
```

配置 schema 草案（provider.json 私有段 / settings 双层，沿用 sky/qw 的「私有配置 + 面板热改」模式，证据：`sky/cmd/skywork-provider/privateconfig.go:126-157`、`qw/cmd/qw2api-plugin/main.go:249-256`）：

```jsonc
{
  "routing": {
    "strategy": "round_robin",            // round_robin|sticky|failover|least_in_flight|weighted|health|two_round
    "weighted": { "top_n": 5, "min_pick_gap_ms": 100 },   // flonno scheduler.go:342-407
    "failover_rounds": 3,                 // qw failover.go:25
    "first_byte_gate_sec": 20,            // qw failover.go:31
    "refresh_request_ids": true,          // qw failover.go:158
    "cooldowns": {                        // 秒；0=禁用该项
      "rate_limit": 60,                   // 宿主 keypool.go:33；优先取上游 Retry-After
      "auth": 600,                        // 宿主 keypool.go:37；0=旧式永久禁用
      "circuit_after": 3, "circuit_cool_sec": 30,   // 宿主 keypool.go:39-43
      "exhaust_after_fails": 0            // 0=只按 402 判耗尽（bios dispatcher.go:242）
    },
    "sticky": { "session_header": "x-session-id" } // 空则退化为全局粘性（devin kernel.mjs:68）
  }
}
```

设计决策与出处：
1. **Action 表内置默认 + 允许覆盖**：四个 Go 插件的映射表只差细节（§1.3），默认表取并集，供应商特例走 ErrRules；
2. **冷却账本落盘 + Corrupt 显式态**：宿主 `freetier_cooldown.go:34-53` 的教训；
3. **策略热更走原子字段、不写回 config 结构体**：flonno `scheduler.go:22-27` 与 bios `scheduler.go:22-29` 的 `atomic.Value` 先例（并发读启动期常量 + 热改字段的分离）；
4. **粘性命中不推进轮询游标**：宿主 `keypool.go:136-144`，否则会话续写会串号。

---

## §2 需求 4：多并发注册（多节点/多端点并行 → 多 IP 并行注册）

### 2.1 现状对比

| 机制 | 项目 | 配置项 | 证据 | 可借鉴点 |
|------|------|--------|------|----------|
| **每节点固定端口 + 逐出口并行注册** | DeepAI | `egress plan/verify/run/register/disarm` 子命令 | `docs/multi-endpoints.md:9-21`（设计文档，manifest 指定来源）；实现 `internal/egress/`（clash.go/verge.go/runner.go/register.go 等，本会话读 clash.go:19-20 标记块、register.go:106-120 RunRegister） | ① mihomo `listeners`+`proxy` 每节点一个常驻端口（clash.go:347-364 `ListenerYAML/Snippet`）；② `run -register` 为**每个出口并行提交一个 count=1, concurrency=1 的注册任务**，body 用 `proxy_pool` 钉死出口（multi-endpoints.md:21）；③ 应用流程含备份→热重载→selector 快照恢复→verify→失败还原（multi-endpoints.md:28-29）；④ 验收口径：7 个出口 IP 互不相同、机器指纹互不相同（multi-endpoints.md:49-51） |
| 并发跑批 + 全局节奏 | DeepAI | `-count/-concurrency` | `internal/register/pipeline.go:436-476`（`RunBatch`：结果按索引回填、ctx 取消即收）；节奏 `waitTurn` 全局最小间隔缺省 800ms（`pipeline.go:627-646`） | worker-pool 模板；**全局 pacing 而非每 worker pacing** |
| 并发≤出口数 | DeepAI | 自动 | `internal/register/proxy.go:76-88`（`CapConcurrency`：并发高于出口数只会排队恶化 p95，压到出口数并提示） | 防呆：并发与出口数解耦校验 |
| 代理池轮询 | DeepAI | `proxy_pool`（多行/逗号分隔，http/socks5） | `proxy.go:16-99`（线程安全轮询池 + 协议白名单 + 去重）；`:153-197` `CheckProxyPool` 并发验活 | 轻依赖代理池 |
| 台账幂等补跑 | DeepAI | ledger 文件 | `pipeline.go:478-525`（`RunRecords`：已 `StatusEnrolled && APIKey!=""` 跳过；只有 key 的记录走 `resumeWithKey` 补验活） | 断点续跑 |
| 入池契约 | DeepAI | admin API | `internal/register/gateway.go:30-92`（`POST /admin/api/accounts action=enroll`，带 fingerprint/machine_id/proxy，`skip_probe` 免重复验活） | 注册器与网关解耦 |
| 线程上限闸 | grok | `register_threads`（1..10 夹紧） | `config.go:46`；`jobs.go:126,149`（`threads = maxInt(minInt(threads,10),1)`）；实际执行委托外部注册器 exe（jobs.go:186 `workerConfig["register_threads"]`） | 并发上限交给子进程参数 |
| 串行补号 + 时间窗 | flonno | `AutoRefill*` 面板设置 | `cmd/flonno2api/main.go:205-241`（每 2 分钟 tick；模式A：池内 < min 阈值补到 target；模式B：窗口内每日一次补 N 个，`lastWindowDay != today` 去重）；逐个 `BrowserRegister`（12 分钟/个超时） | 当前是**串行**注册；Clash 出口轮换已具备（见 §3） |
| 窗口内目标+尝试上限 | qw | `autoreg_nightly_*`（manifest） | `internal/server/nightly.go:22-41`（04:00-06:00、DailyTarget=5、MaxAttempts=10、IntervalSec=60）、`:97-124`（目标与尝试双闸）、`:204-206`（ticker 循环） | 「成功目标 + 尝试上限」两道闸防邮箱池劣化时无限重试（nightly.go:5-9 设计说明） |
| 自动补号阈值 | mmx | `keep_available=3`（manifest/provider.json） | `nightregister.go/regtask.go` 存在；本会话未逐行核实补号触发逻辑 → **细节未核实** | |
| 并发 usage 轮询（旁证） | devin | 无 | `kernel.mjs:148-166`（`refreshUsage`：worker pool 上限 8 并发查用量） | Node 侧同款 worker-pool 形态 |

**结论**：「多 IP 并行注册」直接以 DeepAI 的 egress+RunBatch 为蓝本即可成立——mihomo listeners 每节点常驻端口（或 skywork 的 Pin 方案，§3）解决「并行时出口互异」；RunBatch 解决「并行编排」；两道闸（每日上限/尝试上限）与全局 pacing 解决风控。

### 2.2 统一设计草案：`register` 包（编排器，不含具体站点逻辑）

```go
package register

// Job 是一次注册任务的抽象（站点差异由供应商实现）。
type Job interface {
    Run(ctx context.Context, eg egress.Lease) (Result, error) // eg：本次任务的出口租约（可为直连）
}

// Runner 语义对齐 DeepAI RunBatch + CapConcurrency + waitTurn：
//   - 并发 worker-pool，结果按索引回填（pipeline.go:436-476）
//   - concurrency 自动压到出口数（proxy.go:76-88），压缩时返回提示
//   - 全局最小间隔 pacing（pipeline.go:627-646）
//   - 每任务绑定独立出口（见 §3 egress.Lease）；任务结束归还
//   - 台账（ledger.json）逐条落盘；重启后 RunRecords 幂等补跑（pipeline.go:478-525）
type Runner struct{ /* concurrency, minInterval, ledger, egress */ }
func (r *Runner) RunBatch(ctx context.Context, n int) []Result
func (r *Runner) Resume(ctx context.Context) []Result   // 幂等补跑

// 与 schedule 包联动（§4）：补号任务 = schedule 的一个 Job；
// 触发条件两种（flonno main.go:224-234 语义）：
//   threshold：池内可用 < min → 补到 target（即时模式）
//   slot/window：时间窗内每日一次补 N（(天, 档) 去重交给 schedule.Tracker）
// 与 routing 联动：exhausted 计数上升可作为补号触发信号（推测：现状 grok keeper 的
// valid<min 判定即此思路，keeper.go:186-188）。
// 双闸：daily_target + max_attempts_per_day（qw nightly.go:22-41），日界重置。
```

配置 schema 草案：

```jsonc
{
  "register": {
    "concurrency": 4,                  // 自动 min(concurrency, 出口数)
    "min_interval_ms": 800,            // 全局节奏（DeepAI waitTurn）
    "per_task_timeout_sec": 720,       // flonno 12min/个（main.go:244）
    "daily_max": 10,                   // qw nightly MaxAttempts
    "daily_target": 5,                 // qw nightly DailyTarget
    "threads_cap": 10                  // grok jobs.go:149（外部注册器参数上限）
  }
}
```

---

## §3 需求 5：出口代理

### 3.1 现状：四种模式 + 配置键实况

| 模式 | 项目 | 配置键（实测） | 证据 | 取舍 |
|------|------|----------------|------|------|
| A. 全局代理列表 | 宿主 | 设置页 `engine_proxies`（列表）+ 自定义源 `proxy` / `use_proxy` 布尔 / `use_local_proxy` | `models_source.go:92-112`（`globalEgressProxy` 读列表取首个可用，改全局无需重存各源）；`core/manager/config.go:569-582`（`FirstEgressProxy` 跳过空项与 direct）；`custom_providers.go:285-292,303-318`（`use_local_proxy` 走 `http.ProxyFromEnvironment`，仅测试链路）；`models_source.go:120-131`（装配时注入 `StaticProxyTransport`） | 简单；无按号分流 |
| B. Clash 组切换（轮换/自愈） | mmx | `clash_url`(缺省 127.0.0.1:9097)、`clash_token`、`proxy_url`、`clash_group`（空=自动解析 MiniMax→*节点选择*→GLOBAL） | `net.go:37-46`（NetSettings）、`:107-140`（clashReq，Bearer）、`:222-231`（clashSwitch=PUT /proxies/<group>）、`:233-259`（/configs + /version）、`:263-320`（netProbe：400=OK，3xx=地理分流识别）、`:325-403`（ensureNetwork：探针→换节点逐个复探≤6 次/25s→失败还原原节点；过滤「官网/套餐/流量」假节点 `:375-381`） | 一个出口全局共享；注册/请求前编排 |
| | flonno | 面板 `ClashEnabled/ClashAPI/ClashSecret/ClashGroup/ClashPort` | `internal/clash/clash.go:1-6`（包注释：**组切换方案**，注册完还原；与 lobsterai2api 的持久端口绑定方案对照）、`:88-168`（Nodes/GroupCurrent/SelectorGroups/SwitchNode）、`:168+`（ProbeEgress）；`internal/panel/browserregister.go:107-192`（每次注册尝试切下一节点，全程结束恢复原节点） | |
| | sky / qw（链路守护） | `clash_api/clash_secret/clash_group` + `clash_auto_switch`（缺省 false）+ `clash_interval_sec/clash_bad_delay_ms/clash_bad_streak/clash_cooldown_sec` | `sky/cmd/skywork-provider/privateconfig.go:142-157`；`sky/internal/upstream/clash.go:1-12`（**非破坏性 delay 探测 `/proxies/<node>/delay`；自动切换默认关；永不碰全局 mode**）；qw 同款 `internal/upstream/clash.go:1-12` | 只做「保证稳定」的最小外控 |
| C. Clash 每号钉死端口（按号出线） | sky | `egress_on` + `egress_clash_api/egress_clash_secret/egress_clash_group/egress_config_path/egress_port_base/egress_probe_timeout_ms` | `privateconfig.go:167-177`；`internal/egress/pin.go:1-52`（**Pin=节点↔端口永久绑定**，「端口漂移=出口 IP 无感换节点，正是本功能要消除的」pin.go:33-36；Binding=账号↔端口，注册期 email→入池后 uid）；`internal/egress/listeners.go:30-52,232-249`（行级合并 listeners 块 `sw-<port>` 进运行配置，幂等、保用户已有条目） | 防按 IP 聚类封号（pin.go:3-5）；需要写 Clash 配置并热重载 |
| C'. 独占借用出口 | devin | `clash_*` 经 provider.json 注入环境变量 | `kernel/kernel.mjs:131-136`；`kernel/egress.mjs:1-15`（**诚实标注限制**：mihomo 一个 selector 同时只有一个出口，per-account 绑定是独占借用，请求期间持锁该节点）；`:29-35` DEFAULTS（api 9097、mixedPort 7890）；undici `setGlobalDispatcher` 热切换出口（`egress.mjs:21-26`，运行期改环境变量无效——实测注释） | 无需写配置，但并发账号不能各持不同节点 |
| D. 直连/系统代理兜底 | 宿主 | `use_local_proxy` | `custom_providers.go:296-318` | |

**配置键核对（如实标注）**：
- `clash_url`/`clash_token`：mmx 有（`net.go:39-40`）；sky/qw 用的是 `clash_api`/`clash_secret`（`privateconfig.go:142-143`）——**两套命名并存**，模板需统一；
- `clash_mixed_port`：**作为配置键在 mmx/sky/qw/grok/flonno/devin 源码中均未找到**（grep `mixed_port` 无命中）。最接近的实况：devin `egress.mjs:34` 常量 `mixedPort: 7890`；mmx `net.go:41,349` 注释写明「Clash 混合端口 7890 / 专属端口 7891」由 `proxy_url` 承载；flonno 面板键名为 `ClashPort`（`browserregister.go:33`，注册浏览器 `--proxy-server` 指向的混合端口）。
- `resolve_candidates`：**未找到**（全量 grep 无命中）。最接近：sky `resolve_host`/`resolve_ip` → `Resolve map[string]string`（`privateconfig.go:121-125`，把指定域名钉到指定 IP 的静态解析）。语义是否等价于「候选解析列表」**无法确认 → 推测**：若原意是「出口候选探测」，sky 已由 `egress/probe.go`+`verify.go` 承担（文件存在，本会话读 pin.go/listeners.go，未逐行读 probe/verify → 细节未核实）。
- `proxy_url` / 跟随系统代理：mmx `proxy_url`（`net.go:41`）；宿主 `use_local_proxy`（`custom_providers.go:290-292`）；devin 用 undici 热切换而非环境变量（`egress.mjs:21-26`）。

### 3.2 统一设计草案：`egress` 包

```go
package egress

// Lease 是一次请求/一个注册任务拿到的出口租约。nil *Lease = 直连。
type Lease interface {
    ProxyURL() string            // http://127.0.0.1:<port> 或上游代理 URL
    Release()                    // 模式C' 独占借用时归还（devin egress.mjs 语义）
}

// Provider 是出口策略实现。模板内置四种（对应 §3.1 A/B/C/C'）。
type Provider interface {
    Acquire(ctx context.Context, hint AccountHint) (Lease, error)
}

// 1) GlobalList：设置页维护的代理列表取首个可用（宿主 FirstEgressProxy 语义）。
// 2) ClashSelector：外控组切换 + 探针自愈（mmx ensureNetwork 语义：探测→轮换≤N 次/
//    总预算→失败还原；flonno browserregister 语义：每次尝试下一节点、结束还原）。
// 3) ClashPinned：listeners 每号常驻端口（sky Pin/Binding 语义 + DeepAI verge 应用流程）。
//    - 挑节点：按账号数最少 + 实测出口 IP 去重（pin.go:16-19 注释）
//    - 端口永不复用、绑定持久化；行级合并进运行配置（listeners.go:30-52），备份→热重载→
//      selector 快照恢复→verify→失败还原（DeepAI multi-endpoints.md:28-29）
// 4) ExclusiveBorrow：selector 独占借用（devin 语义），并发受限的降级模式。

// ClashClient 最小外控面（mmx/flonno/sky/qw/devin 五处实现的公约数）：
//   GET /proxies（列组/节点）、PUT /proxies/<group>（切换）、GET /proxies/<node>/delay
//   （非破坏探测，sky clash.go）、GET /configs、GET /version（mmx net.go:233-259）。
//   鉴权 Bearer secret；超时 4-10s；永不改全局 mode（sky clash.go:12）。
type ClashClient struct{ Base, Secret string }

// 账号绑定持久化（JSON 落盘）：email/uid → port；校验 ProxyPort 反解对齐
//（sky pin.go:54-80：非本机地址返回 0，不当 listener 处理）。
```

配置 schema 草案：

```jsonc
{
  "egress": {
    "mode": "global_list",             // global_list|clash_selector|clash_pinned|exclusive_borrow|direct
    "global_list": ["http://...", "socks5://..."],   // 宿主 engine_proxies 语义
    "clash": {
      "api": "http://127.0.0.1:9097", "secret": "",    // 统一 clash_api/clash_secret 命名
      "group": "",                     // 空=自动解析（mmx net.go:188-216 顺序：专用组→*节点选择*→任一 Selector）
      "auto_switch": false,            // sky：默认关，切换影响用户其它应用出网（clash.go:10-11）
      "watch": { "interval_sec": 0, "bad_delay_ms": 0, "bad_streak": 0, "cooldown_sec": 0 }, // sky privateconfig.go:146-157
      "ensure": { "max_tries": 6, "budget_sec": 25, "restore_on_fail": true }   // mmx net.go:335-403
    },
    "pinned": { "config_path": "", "port_base": 18400, "probe_timeout_ms": 0, "probe_url": "https://api.ipify.org?format=json" }, // sky egress_* + devin testUrl
    "proxy_url": "",                   // mmx 语义：混合端口/专属端口/任意 http(s)/socks5
    "follow_system_proxy": false       // 宿主 use_local_proxy 语义（默认关）
  }
}
```

设计决策与出处：
1. **默认 `auto_switch=false`**：sky 明确写「切节点会影响用户其它应用的出网」（`clash.go:10-11`）；mmx 只在注册/签到前编排（`net.go:337-339`）——模板把「切换时机」交给调用方（请求前/注册前/守护循环）而不是包内自发；
2. **模式 C 优先于 C'**：sky 的常驻端口绑定消除了 devin 独占借用的并发瓶颈；devin egress.mjs:9-15 自己也说明这是 mihomo 单出口 selector 的限制；
3. **探测必须非破坏**：sky 用 `/proxies/<node>/delay` 而非切换后实测（`clash.go:7-8`）；
4. **写运行配置必须幂等 + 可还原**：DeepAI 标记块 + 备份/还原/disarm（`multi-endpoints.md:27-29`）、sky 行级合并保留用户条目（`listeners.go:36-52`）、mmx 过滤假节点（`net.go:375-381`）三条经验合并。

---

## §4 需求 6：定时任务

### 4.1 现状对比（buddyhub 为参照系）

| 机制 | 项目 | 配置项 | 证据 | 备注 |
|------|------|--------|------|------|
| **档位 slot + 确定性抖动** | buddy | `SlotHours`（缺省 [1,23]）、`SlotJitter`（缺省 ±5min） | `internal/scheduler/scheduler.go:33-40,95-101` | 两个档位到点派发全部启用任务族（checkin/travel/activity/keepalive/blackcat，`:200-203,283-303`） |
| 抖动 = (自然日,档位) 纯函数 | buddy | — | `scheduler.go:223-251`（FNV 变体混淆 y/m/d/hour/yday → [-max,+max] 秒级偏移；**纯函数是硬约束**：Run 主循环每次醒来重算 nextWake，随机抖动会让 timer 被无限推迟、任务永不触发 `:225-227`） | 种子跨天变化防上游识别固定节律（`:229-230`） |
| 最近档位计算 | buddy | — | `scheduler.go:253-281`（`slotAt`/`nextSlot`：逐日往后找 ≤2 天，覆盖「抖动为负且当天档位已过」与跨天） | |
| 主循环 + 热重载 | buddy | 面板改配置 | `scheduler.go:352-389`（timer + `wake` channel：配置热更丢弃当前 timer 立即重排；全禁用时空转等 wake 不 busy-loop） | |
| 迟到唤醒宽限 | buddy | 零配置 | `scheduler.go:329-350`（睡眠跨过槽位 → 先等 5s 让 DNS/网络栈就绪再补跑，issue #152 实测；>1s 才算迟到，毫秒级 timer 抖动不误判） | Modern Standby 场景 |
| **(天, 档) 去重** | buddy | — | `scheduler.go:410-433`（tracker 去重粒度=自然日+档位：01:00 跑过不影响 23:00；`slot=-1`=手动触发不占档位）；`tracker.go:127-142`（`DoneInSlot`：Day!=today 或 Status=skipped 视为未做；`Start` 清旧状态防「跑一半重启」误判已完成） | 与 ask 描述「去重粒度=天+档」一致 |
| 档内并行派发 | buddy | — | `scheduler.go:391-404`（每任务族一个 goroutine，慢族不阻塞同槽其它族，批内等待收尾） | |
| 分钟 ticker + 窗口 + 间隔 | grok | `pool_keeper_enabled`、`pool_window_start/end`（跨午夜支持）、`pool_check_minutes`、`pool_min_interval_minutes`、`pool_jitter_seconds`、`pool_min_valid/target_valid/max_per_run/max_per_day` | `config.go:54-64`；`keeper.go:100-108`（1 分钟 ticker + 20s 启动延迟）、`:111-134`（窗口 + 检查间隔 + running 防重入）、`:350-361`（`inPoolWindow` 跨午夜 `[s,e)`）、`:264-293`（`NextAllowedAt` = min_interval + 随机抖动；连败 3 次暂停 2h）、`:177-181`（日界重置 RegisteredToday） | 触发条件 = `valid < pool_min_valid`（`keeper.go:186-188`） |
| 窗口 + 目标 + 尝试双闸 | qw | `autoreg_nightly_*`（manifest 记载键名前缀） | `internal/server/nightly.go:22-41`（04:00-06:00 / target 5 / max 10 / 60s 巡检）、`:97-124`（窗内且未达目标且未达尝试上限才发起，want=min(target-succeeded, limit-attempted)）、`:204-206`（ticker + Apply 热重启循环）、`:43-44`（`nightlyState`：`day` 变则全部归零） | 明确的设计动机：邮箱池质量不稳定，成功数单闸会无限重试（`:5-9`） |
| 阈值 + 窗口双模式补号 | flonno | 面板 `AutoRefillEnabled/Min/Target` + `AutoRefillWindowEnabled/Start/End/Count` | `cmd/flonno2api/main.go:205-241`（2 分钟 tick；阈值模式即时补；窗口模式按 `lastWindowDay != today` 每日一次）；`internal/server/refill_window_test.go:10-30`（RefillInWindow 跨天窗口判定表驱动测试：边界含开始不含结束、起止相同无效） | 去重粒度=天（无档位概念） |
| 周期任务（非窗口） | sky/qw | `refresh_interval_sec`（0=关闭，热改） | `sky/privateconfig.go:132-134`；`qw/cmd/qw2api-plugin/main.go:164-166,289-292`（Apply 语义：改间隔即生效） | 周期刷积分恢复号 |
| 任务执行状态回显 | buddy | tracker | `tracker.go:127-142` + `scheduler.go:426-432`（Start/FinishOK 记录耗时与结果，checkin 由任务自身写更精确分桶） | |

### 4.2 统一设计草案：`schedule` 包

以 buddyhub 为骨架，吸收 grok/qw/flonno 的触发条件与双闸，收敛为「一种 Spec + 一个 Tracker + 一个 Run 循环 + Job 注册表」：

```go
package schedule

// Window 是可选时间窗（独立 type）。勘误 2026-11-07：原稿误写成下方 Spec 内嵌匿名
// struct、与 *Window 字段重复定义（Go 无法编译），按总纲 00 §1.3-C10 裁决改为独立类型。
type Window struct{ Start, End string `json:",omitempty"` } // "01:00"；跨午夜支持（grok keeper.go:350-361 [s,e)），起止相同无效（flonno refill_window_test.go:10-30）

// Spec 是一个定时任务的排程声明（JSON 友好，全部字段可选）。
type Spec struct {
    // 触发方式三选一（同时配置时 slot 优先于 interval）：
    Slots []int  `json:"slots"`         // 每天触发小时档，如 [1,23]（buddy scheduler.go:33-35）
    IntervalSec int `json:"interval_sec"` // 周期任务，0=不周期（sky/qw refresh_interval_sec 语义）
    Window *Window `json:"window"`      // 窗口限制（可与上两者叠加；grok/qw/flonno 同款）
    JitterSec int `json:"jitter_sec"`    // 档位抖动上限（缺省 300s=±5min）；确定性纯函数（buddy scheduler.go:223-251）
    // 每日闸（注册/补号类任务用，qw nightly.go:22-41）：
    DailyTarget int `json:"daily_target"`          // 成功数达标即停
    DailyMaxAttempts int `json:"daily_max_attempts"` // 尝试数上限（含失败）即停
    MinGapSec int `json:"min_gap_sec"`             // 两次运行最小间隔（grok pool_min_interval_minutes）
    GraceSec int `json:"grace_sec"`                // 迟到唤醒网络宽限（buddy 零配置 5s 的可配置化）
}

// Job 由供应商实现；context 取消必须快速收尾（buddy sleepCtx 口径）。
type Job interface {
    Name() string                                    // tracker key（buddy taskKind.trackerKey 语义）
    Run(ctx context.Context, slot int) error          // slot=-1 手动触发（buddy scheduler.go:452-455）
}

// Tracker 去重粒度 = (自然日, 档位)；手动触发 slot=-1 不占档（buddy scheduler.go:410-433、
// tracker.go:127-142）。落盘 JSON；Start 清旧状态防「跑一半重启」误判已完成。
type Tracker struct{ /* mu, byKey map[string]TaskRunState{Day,Slot,Status,StartedAt} */ }
func (t *Tracker) DoneInSlot(key string, slot int) bool
func (t *Tracker) Start(key string, slot int)
func (t *Tracker) FinishOK(key string, msg string, d time.Duration, slot int)

// Scheduler 主循环（buddy Run 语义）：nextWake 重算 + timer + wake channel 热重载 +
// 迟到宽限 + 档内并行派发。确定性 jitter 保证「重算出的下一时点收敛」。
type Scheduler struct{ /* specs, jobs, tracker, wake chan struct{}, nowFn */ }
func (s *Scheduler) Run(ctx context.Context)
func (s *Scheduler) Apply(specs []Spec)         // 热更：close(wake) 触发重排（buddy scheduler.go:369-373）
func (s *Scheduler) RunNow(name string) error   // 手动触发 slot=-1
```

配置 schema 草案（provider.json 私有段）：

```jsonc
{
  "schedule": [
    { "job": "pool_refill",
      "slots": [1, 23], "jitter_sec": 300,
      "daily_target": 5, "daily_max_attempts": 10,
      "min_gap_sec": 1800, "grace_sec": 5,
      "trigger": { "when_pool_below": 10, "refill_to": 15 } },   // 阈值条件叠加窗口（flonno 双模式合一）
    { "job": "credits_refresh", "interval_sec": 1800 }           // 周期任务（sky/qw refresh 语义）
  ]
}
```

设计决策与出处：
1. **jitter 必须是 (天, 档) 的确定性纯函数**：这是 buddy 踩出来的硬约束（`scheduler.go:225-227`），不是优化项；实现直接采用其 FNV 变体（年积日参与混淆，跨天变化）；
2. **去重键 = (job, 天, 档)**，`slot=-1` 手动不占档：buddy `scheduler.go:410-433`；flonno 的「窗口内每日一次」是该粒度的特例（只有一个隐式档）；
3. **双闸分开**：`daily_target`（成功）与 `daily_max_attempts`（尝试）必须同时生效，否则邮箱质量差时无限重试（qw `nightly.go:5-9`）；连败暂停（grok `keeper.go:279-284`）作为 Job 内部策略而非框架语义（失败原因只有 Job 自己知道）；
4. **热重载用 wake channel 丢弃重排**，而不是重启 goroutine：buddy `scheduler.go:369-373`；全禁用时空转等 wake 防 busy-loop（`:356-364`）；
5. **迟到宽限默认 5s、可配置**：睡眠唤醒后 DNS 未就绪，零宽限会把唯一一次补跑机会打在注定失败的窗口（buddy `scheduler.go:329-335` issue #152）。

---

## §5 四能力集成点（跨包接线，均为证据支持的关系）

1. **routing.Exhaust → register 触发补号**：耗尽/不可用计数上涨 → 池内可用低于阈值 → schedule 的 `pool_refill` Job（grok keeper 的 `valid<min` 判定即此闭环，`keeper.go:146-188`；flonno 阈值模式同源，`main.go:224-226`）。
2. **register → egress**：每个注册任务持有独立出口租约，注册完归还/还原（DeepAI `multi-endpoints.md:21`；flonno `browserregister.go:107-192`）。
3. **routing → egress**：请求级出口 = 账号绑定端口（sky Binding）或全局列表（宿主）；routing 的 429 冷却若由出口 IP 引起（同 IP 多号被限）应联动 egress 换出口（**推测**：现状无插件实现该联动，sky 的 Pin 是预防而非响应）。
4. **schedule → 全部**：credits_refresh（sky/qw）、pool_refill（flonno/grok/qw/mmx keep_available）、failover 审计同步（grok `failover_sync.go:8-19`，60s 周期只读同步内核审计为 failover.* 事件）都是同一框架的 Job 实例。

## 附 A：本会话核实清单（命令级）

- `grep -o '"strategy"[^,]*'` 两处 devin provider.json → 均为 `"round-robin"`；
- `grep -rn "clash_mixed_port\|resolve_candidates"` 于 mmx/sky/qw/grok 源码 → 零命中（如实标注为未找到）；
- 其余全部结论来自上表所引文件的直接阅读（Read/grep -n），未跑任何构建或测试（本轮为纯分析，无实现代码，无需构建验证）。

## 附 B：遗留不确定项（下轮可补）

1. mmx 自动补号（`keep_available=3`）的触发与节流实现细节（`nightregister.go/regtask.go` 存在但未逐行读）；
2. flonno 冷却时长是否按 FailureReason 区分取值（类型与状态机已核实，取值来源未读到配置证据 → §1.4 已标推测）；
3. sky `egress/probe.go`、`verify.go`、`manager.go` 的探测与自愈细节（包注释与 pin.go 已足够支撑设计，未逐行读）；
4. bios 部署 provider.json 的 `_source=None` 与版本一致性：**据 manifest 转述**，本会话未复读 `D:/Program Files/opencode2api/bin/providers/runbiosproxy/provider.json`。
