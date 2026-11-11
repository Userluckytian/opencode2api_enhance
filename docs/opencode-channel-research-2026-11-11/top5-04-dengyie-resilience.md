# 横向调研 #4 — dengyie/opencode2api：韧性设计与「额度墙」治理

- 仓库：`C:\Users\ASUS\AppData\Local\Temp\oc-refs-20261111\dengyie-opencode2api`（只读，未修改任何文件；`git status` 保持 clean）
- 上游出处：`jasonxu114514/opencode2api` v1.4.6（commit `58f5667`）为基线导入（本仓库 commit `6b61de2`），此后全部为 dengyie 增强（v1.5.0 → v1.6.1）
- HEAD `c3383c4`（fix(gateway,tetherpool): event names that survive the log redactor），共 26 个提交，GPL-3.0，Go 1.24+
- 自述定位：`README.md:13-17`「production-hardening for free-tier operation: SSE stream keepalive, a post-commit idle watchdog, quota probes and a quota pre-check for cooled keys, a global inflight gate, and geo-block classification」
- **本次为静态分析 + 实机验证双轨**：实机跑通（端口 40220–40224、临时目录、全假 key），见 §7.5
- 与我们打同一上游（OpenCode Zen 免费层），因此它的韧性设计是可直接对照的工程样本

> **一句话结论**：它的招牌不是"换出口"，而是 **把「额度墙」建成了一个独立的、带探活与主动拒绝的一等状态**；而它对"额度绑什么"的回答是 **账号 + 出口 IP 双维**（§1.4/§3.4 原话），TetherPool 的 per-key binding 正面确认 **出口按 key 绑**。

> **引用约定**（行号按 LF 计数，与 `read`/`rg` 一致）：本文**裸文件名一律指 dengyie 仓库**，指我们仓库时总写全路径（`core/manager/...`、`vendors/custom/...`）。同名文件消歧——未加前缀的 `pool.go` / `rotation.go` / `client.go` / `exit.go` 分别指 `internal/gateway/pool.go`、`internal/gateway/rotation.go`、`internal/tetherpool/client.go`、`internal/warp/exit.go`；WARP 池一律写全 `internal/warp/pool.go`；`availability.go`（`internal/models/` 与 `internal/gateway/` 各一份）本文全部写全路径。

---

## 1. quota preflight（配额预检）—— 招牌特性拆到底

### 1.1 判据来源：**纯本地账本，零上游探测请求**

预检函数 `quotaWall()`：`internal/gateway/quotaprobe.go:326-359`
调用点：`internal/gateway/gateway.go:407-421`（在 `prepareRouteBodies` 之后、`doUpstream` 之前）

判据完全来自每个 key 节点上的两个 atomic 字段（`internal/gateway/pool.go:613-614`）：

| 字段 | 含义 |
|---|---|
| `quotaUntil` | 该 key 的额度窗口"预计何时重开"（UnixNano，0=无） |
| `probeNextAt` | 下次允许探活的时刻 |

写入点唯一：`MarkFailure`（`internal/gateway/pool.go:885-897`）

```
until := time.Now().Add(p.cooldownFor(node, resp, err))
node.cooldownUntil.Store(until.UnixNano())
if resp != nil && resp.StatusCode == http.StatusTooManyRequests {
    node.quotaUntil.Store(until.UnixNano())          // 429 ⇒ 同时标 quota 冷却
    if p.quotaProbeInterval > 0 {
        node.probeNextAt.Store(now + p.quotaProbeInterval)
    }
}
```

**注意语义分层**：`quotaUntil` 是 `cooldownUntil` 的语义超集（`pool.go:606-612` 注释原话）——每个 quota 冷却的 key 同时也是普通冷却，但反过来不成立。这让"等额度窗口"和"普通传输冷却"可以被区别对待。

**没有任何上游响应头可读**：`quotaprobe.go:14-23` 注释直说「upstream does not expose a quota endpoint, so the only honest way to learn whether the window reopened is one minimal real request through that key」。即 Zen 免费层**没有额度查询 API**，预检的唯一依据是"上一次真实 429 + Retry-After 外推"。

### 1.2 拒绝条件（全条件成立才拦）

`quotaWall()` 的判定顺序（`quotaprobe.go:326-359`）：

1. 匿名道**还能接单** → 不拦（`anonymousLaneAvailable() && !anonymousLaneWalled()` ⇒ return false，`:327`）
2. 一个 key 都没配 → 不拦（`:332`）
3. 配置了 zen 道 ⇒ `zenNodes.AllQuotaCooled()`，有一个 key 没在 quota 冷却就立刻 return false（`pool.go:950-966`）
4. 配置了 go 道 ⇒ 同理，取两道中**最早**到期时间
5. 全部命中 → 返回 `earliest`，请求以 **429 `insufficient_quota` + `Retry-After`（向上取整秒，最小 1）** 结束，`X-Request-Id` 照发，日志 `quota_precheck_blocked`，**0 次上游调用**

旁路：诊断/Playground 请求跳过（`gateway.go:407` `!isDiagnosticRequest(...)`）；管理面探活永远可达。

实测证据见 §7.5「quota preflight」行：墙期第 2 个请求起假上游命中数不再增长。

### 1.3 匿名道条件：v1.5.3 的"实测墙"信号（对我们最有价值的一段）

v1.5.2 及以前用 `route.Anonymous` 直接旁路预检 ⇒ 免费模型（匿名先行路由）**永远不走预检**，墙期每笔 live 请求要烧 3 次上游调用。v1.5.3 移除旁路，改为经验信号（`internal/gateway/gateway.go:112-113`，`quotaprobe.go:249-285`）：

```
const anonWallWindow = 10 * time.Minute      // quotaprobe.go:251
observeAnonymousWall(status):                 // :257-273
    2xx   → anonWallCount = 0（立刻解除）
    429   → 距上次 > 10min 则重新计 1，否则 +1
anonymousLaneWalled() → 窗口未过期 且 count >= 2
```

**≥2 次 429 / 10 分钟** 才算"实测在墙"。单次异常不算，避免误判。解除路径三条且**全部自愈**：匿名道任意 2xx、quota 探活成功（`quotaprobe.go:151` 直接把 count 清零）、窗口自然过期。设计意图（CHANGELOG.md:237-239）：墙期 live 请求从每笔 3 次上游调用降到 armed 之后 **0 次**，而前两次 walk 保留下来作为"墙是否解除"的天然探测器。

### 1.4 「额度按什么维度计」——它给了明确答案

这是我们在猜的问题，它两处写明：

1. **配额按出口网段计**：`internal/warp/exit.go:512-520`
   > The free tier treats a Cloudflare /64 as one quota scope: rotating inside the same /64 yields a new /128 but the same exhausted quota.
   IPv6 按 `/64`、IPv4 按 `/32` 去重并冷却（`README.md:266`）。
2. **配额是「账号 + 出口 IP」双维**：`docs/DEVELOPMENT-PLAN-v1.6.0.zh-CN.md:13`
   > 免费层配额按「账号 + 出口 IP」双维计，key 被墙后只能等 Retry-After 或 600s 探活，无法主动换出口 IP。
   以及 `CHANGELOG.md:59-60`「The free-tier wall is scoped per account AND per egress IP, so a rotation lifts only the IP-side component.」
3. **免费层按请求数计，不按 token**：`docs/DEVELOPMENT-PLAN-v1.5.0.zh-CN.md:244`
   > 探针成本对配额无影响（免费层按请求数计，非按 token）。

⇒ 三条合起来：换出口只解一半（IP 分量），账号分量必须等窗口自然流过。这是 §3 TetherPool 疗效预期的根据。

### 1.5 预检的账本不被污染（一个我们应当照抄的细节）

v1.5.0 修复（CHANGELOG.md:341-345）：模型目录 list **永远返回 200**，即使推理在墙上。v1.4.6 的刷新周期每 5 分钟调用 `MarkSuccess` 把 quota 状态抹掉 ⇒ 预检永远失效。修复：新增 `MarkListingSuccess`（`pool.go:869-877`）只清 `failures`/`cooldownUntil`，**刻意不动 `quotaUntil`/`probeNextAt`**；只有真实推理成功或 quota 探活成功才清额度状态。§7.5 实测已锁定该行为。

---

## 2. `report quota walls`（提交 `053133d`）：quota wall 的分类与处置

### 2.1 什么叫 wall

**quota wall = 上游 429 + 额度措辞的拒绝**。它分三个层次被识别（`pool.go:885-897`、`rotation.go:59-70`）：

```go
// rotation.go:59-70 —— 墙的字面判定
func quotaError(status int, body string) bool {
    if status == 429 { return true }                    // 任何 429 都算
    // 否则只认显式额度标记（普通 provider 错误必须保留重试预算）
    markers: insufficient_quota, quota_exceeded, "quota exceeded", "quota exhausted",
             rate_limit / "rate limit" / rate-limit, usage_limit / "usage limit",
             "insufficient balance", "credit balance is too low",
             额度不足, 配额已用尽
}
```

### 2.2 它把额度耗尽归成几类、各自怎么处置

| 类别 | 判据位置 | 处置 |
|---|---|---|
| **① 单 key 撞墙**（key lane 429） | `upstream.go:1075-1103` `observeKeyResult` | `MarkFailure` → `cooldownUntil`+`quotaUntil` 同置；日志 `key_quota_cooled`（含 `retry_after_seconds`）；若开 TetherPool 反哺则 `observeQuotaWall(node, tier, "live_429")`；**换下一个 key**（cursor 轮转） |
| **② 全池撞墙**（所有 key quota 冷却） | `pool.go:950-966` `AllQuotaCooled` | 预检拦下（§1.2），429+`Retry-After`，0 上游调用；**不摘 key**，交给探活循环 |
| **③ 出口被 IP 封**（免费层滥用 403） | `abuse.go:66-78` `isNetworkAbuseBlock`；`upstream.go:502-512` | 该出口 `markBlocked(now + free_tier_block_seconds)`（默认 **900s**，`config.go` `free_tier_block_seconds: 900`）→ 匿名 cursor 跳过它；**不算 key 的错**（`observeAnonymousResult:1120-1125` 显式 return，不计入熔断 strikes）；全部出口被封 → `ForceOpen` 熔断匿名道 |
| **④ 地区不可用（GEO 4xx）** | `abuse.go:155-180` `geoBlockMarkers`；`upstream.go:1033-1036` | **停止同出口换 key**（`errGeoBlocked`），映射 404 `model_not_available_in_region`；**不**标记模型全局不可用（GEO 是出口属性）；实测见 §7.5 |
| **⑤ 请求本身被拒（确定性 4xx）** | `abuse.go:115-137` `isDeterministicClientResponse` | 立即返回原始 4xx，**不跨 lane/tier 重试**（`upstream.go:520-527`，`deterministic_rejection`）；匿名道 `NoteNeutral()`（:539）——不算出口失败 |

**没有任何"永久摘除 key"**。墙只写时间戳，到期自动回池；提前回池靠探活。

### 2.3 分类函数与时长表（可直接抄的表）

```
时长表（默认配置）
─────────────────────────────────────────────────────────────
普通失败冷却        failure_cooldown_seconds   15s
指数递增            cooldownFor() 1<<min(failures-1,3)   ⇒ 15/30/60/120s，封顶 8x
                    （pool.go:902-912）
上游 Retry-After    若更长 ⇒ 全额采纳（无上限）
                    ⇒ 实测见过 12564s（CHANGELOG.md:166）
quotaUntil          与 cooldownUntil 同一时刻（429 触发）
quota 探活          performance.quota_probe_seconds   600s（0=关）
                    ticker 周期 = interval/2，边界 [1s, 60s]（quotaprobe.go:30-42）
                    tick 循环超时 = 90s（quotaprobe.go:87）
出口 IP 封禁        performance.free_tier_block_seconds 900s（abuse.go:41 内置 15min）
WARP 出口冷却       egressCooldown 30min，按 /64（IPv4 按 /32）键
                    （internal/warp/pool.go:17-20, exit.go:508-520）★固定不递增★
匿名道熔断          anonymous_backoff{threshold, base_hours, max_hours}
                    未配置 ⇒ threshold=0 ⇒ 禁用熔断（gateway.go:188-197）
                    触发后 pause = base << min(strike-1, 30)，封顶 max（pool.go:264-274）
匿名道 Retry-After  上限钳制为 cooldown × 8（pool.go:380-410）——
                    key 道全额采纳、匿名道钳制，理由见 pool.go:380-388 注释
请求总预算          retry.timeout_seconds     300s
尝试次数            retry.max_attempts          3
─────────────────────────────────────────────────────────────
```

### 2.4 反哺（`053133d` 的 "report" 部分）

`internal/gateway/tetherpool.go:83-107` `observeQuotaWall` —— 三重门 + CAS 节流 + 非阻塞发送：

```
门 1: g.tpRebind != nil                     （集成开着）
门 2: cfg.TetherPool.RebindOnWall           （默认 false）
门 3: tier == config.TierZen                （只对 Zen 免费 key 反哺）
节流: now - node.rebindFiredAt >= rebind_min_interval_seconds（默认 600s）
      且 CAS 抢占成功（并发请求只发一份）
发送: select { case g.tpRebind <- req: default: }   // 满则丢弃，且保留时间戳
```

两个挂点：live 429（`upstream.go:1100`，reason=`live_429`）+ 探活仍墙（`quotaprobe.go:162`，reason=`probe_still_walled`）——注释明确后者是"全墙期唯一观察者"，因为那时预检把所有 live 请求都挡了。投递体见 §7.5 实测（`{"account_id":"oc2api_00002","event_type":"rate_limited_429","target_service":"opencode2api","force_rebind":true}`）。

---

## 3. TetherPool：出口 IP 池 + **per-key binding**（本次最关键发现）

### 3.1 它是什么

`internal/tetherpool/client.go:43-96` —— 一个 **外部 IP-Pool v2 控制面的 REST 客户端**（默认 `http://127.0.0.1:9100`，`docs/DEVELOPMENT-PLAN-v1.6.0.zh-CN.md:16-18` 说明部署位置：huawei-vps，经 EasyTier mesh 转发）。三个端点：`/v1/allocation/acquire`（按需租约，返回 `gateway_proxy_url`）、`/v1/battery/event`（业务反馈 + `force_rebind`）、`/health`。

绑定是**服务端权威 + 粘性 + 自动 re-home**（`client.go:17-31`）：网关只消费 `gateway_proxy_url`；节点死了由控制面在服务端迁移，**代理 URL 永不变**。因此这是"按需租约式出口 IP 池"。

### 3.2 per-key binding 的语义 = **每个 key 绑一个独立出口**（确认）

`internal/tetherpool/provision.go:16-25`：
```go
const AccountPrefix = "oc2api_"
func AccountID(key string) string { return AccountPrefix + config.KeyDisplayID(key) }
```
账号身份由 **key 本身**派生 ⇒ 绑定跨重启稳定、与 key 一一对应。

`provision.go:81-179` `Resolve`：
```
每个 zen key → POST /v1/allocation/acquire?service=..&account_id=oc2api_<key后5位>
               &egress_preference=warp_ipv6&region=<按 key 序号轮转 regions[i%len]>
entries = [按 zen_keys 顺序的 gateway_proxy_url ...] + cfg.RuntimeProxies()（静态备援）
```
它靠 `nodePool` 的位置绑定保证 1:1（`internal/gateway/pool.go:653-660`）：`proxyIndex := i % len(transports.items)`；把"每 key 一条 allocated URL"前置，就得到 key i ↔ 出口 i，静态代理自动退化为备援池。

**all-or-nothing**（`provision.go:160-172`）：任一键位取不到就整体放弃并回退静态列表——因为部分列表会让后面每个 key 的位置绑定全部错位。

**实机证据（§7.5）**：healthz 渲染两账号绑定，隧道嗅探显示 40223 出口只承载 key `00001`、40224 只承载 `00002`，6 个不同会话 100% 无交叉 ⇒ 出口按 key 绑定，与"会话/请求"无关。

### 3.3 冷却账本按什么维度记 —— **两层账本**

| 层 | 键 | 时长 | 递增 | 持久化 | 位置 |
|---|---|---|---|---|---|
| key 层 | `upstreamNode.quotaUntil`（**每 key 一份**） | Retry-After 或 15s→8x | 有 | **无（全 atomic，进程内存）** | `pool.go:606-614` |
| 出口层 | `Pool.cooling[egressKey]`，键 = **`/64`（IPv6）或 `/32`（IPv4）网段** | 固定 30min | 无 | 无 | `internal/warp/pool.go:17-20, 391-394`；`exit.go:508-520` |

出口层的 key 选择有明确理由（`exit.go:512-520`）：**同一 /64 内换地址是假轮换**（拿到的还是同一个已耗尽的额度域），所以池要求每个 active/cooling 出口的 /64 互不相同，重复注册直接丢弃（`pool.go:706-710`）。

### 3.4 它正面回答我们的核心疑问

**额度绑什么？答：账号(key) + 出口 IP **双维**，两者都要考虑。**

`CHANGELOG.md:59-60`（v1.6.0 段落）：
> The free-tier wall is scoped per account AND per egress IP, so a rotation lifts only the IP-side component.

`docs/DEVELOPMENT-PLAN-v1.6.0.zh-CN.md:90` 语义边界：**换绑只解 IP 侧配额分量，账号侧滚动窗口仍需自然流过**；疗效靠线上 `request_routed` 观察，因此 `rebind_on_wall` 默认关。

对我们的直接推论（重要，别抄错）：**光换出口救不了已被墙 12564s 的 key**。"10 个 zen key + 7890 出口跑通"能成立，靠的是那 10 个账号自己的额度都没耗尽——与出口是同一 IP 无关。所以出口多样性的价值在**预防**（避免匿名/共享出口被 IP 侧限流），不在**恢复**。

---

## 4. idle watchdogs：不是进程存活检查，是**逐条已提交流的上游静默时钟**

`internal/protocol/keepalive.go`（221 行，整个特性就这个文件）。设计文档 §4.2（`docs/DEVELOPMENT-PLAN-v1.5.0.zh-CN.md:129-162`）。

### 4.1 监控什么

一个 `streamSupervisor`（`keepalive.go:42-56`）持有**两个独立时钟**：
- `lastWrite` —— 下游写时钟，用心跳
- `lastUpstream` —— **上游输出时钟**，用 idle watchdog

关键设计：`MarkData()`（`:191-193`）在**每次从上游读到字节**时更新，`markDataObserver`（`:209-222`）把它挂在 usage observer 外面 ⇒ **心跳帧写下游不会解除 idle 计时**（`:26-29` 注释直说），反之亦然。这就是能区分"模型在思考"和"链路已死"的根本原因。

### 4.2 判定死亡的条件

```go
run() 的 tick 周期：period = min(2s, Heartbeat/3, Idle/3)，下限 50ms   // :84-96
tick:
  if Idle > 0 && !idleFired && now-lastUpstream >= Idle { fireIdle(); return }   // :107-111
  if Heartbeat > 0 && now-lastWrite >= Heartbeat       { writeKeepalive() }      // :113-117
```
`fireIdle`（`:124-131`）顺序严格：
1. `idleFired=true` → 此后数据泵的下游写全被吞（`writeThrough` `:148-151`）
2. 按下游协议发 **in-band 错误帧**（`upstream_idle_timeout`；Chat/Responses 走 error 事件，Anthropic 走 `event: error`）
3. 调 `OnIdle(silence)` → 网关侧（`gateway.go:436-440`）打 WARN 日志、置 `idleAborted`、**`cancel()` 请求上下文**
4. goroutine 退出

### 4.3 恢复动作：**不重启进程、不清账本、不换出口**

只做一件事：取消上游请求 + 结束本回合。理由（`docs/DEVELOPMENT-PLAN-v1.5.0.zh-CN.md:131-136`）：wedge 期间连接挂着、token 挂着，等满 300s 总预算只会给客户端一个笼统 504。`idleAborted` 标志存在的意义（`gateway.go:428-431`）：idle abort 与"客户端自己走了"在 upstream read 上都表现为 `context.Canceled`，没有这个标志遥测就会冤枉客户端。默认 0（关），tebi 生产设 240s（低于 300s 预算）。

**实机证据（§7.5）**：wedge 流 8.05s 结束，序列 = 数据帧 → `: keepalive` ×2 → `upstream_idle_timeout` → `[DONE]`。

### 4.4 对我们「30s probePort + 降级」看门狗的建议（可落地）

我们的 watchdog 看的是 **TCP 端口**，只能发现"进程没了/没监听"，发现不了"进程活着但每条流都在空转"。它现在把两者混为一谈，降级到错误的出口。建议三层：

1. **保留 probePort，但降级为"存活"层**，与"可用性"层分离，不要用它判断上游是否可用。
2. **加"逐流上游静默时钟"**：每次从上游读到字节更新一个 `atomic lastUpstreamNano`；ticker 周期 `min(idle/3, 2s)`、下限 50ms；超阈值 → 发 in-band 错误 + cancel 该请求上下文 + 记 `upstream_idle_timeout`。**必须与 `idleAborted` 配合区分"看门狗杀的"与"客户端走的"**，否则遥测会冤枉客户端。
3. **加静默期心跳**（默认 15s）：SSE 注释行 `: keepalive\n\n`（Chat/Responses）或 `event: ping`（Anthropic）。这是零成本改动，解决"长 reasoning 被中间层/客户端 idle 掐断"。写路径必须与数据泵互斥（单帧 mutex，`:148-160`），否则帧会交错。

配置铁律（`docs/DEVELOPMENT-PLAN-v1.5.0.zh-CN.md:324-325`）：`idle_timeout < retry.timeout_seconds`，否则永远不先触发；另有 `config_attempt_timeout_covers_request` 启动告警（`gateway.go:170-184`，`attempt_timeout >= timeout_seconds` 时 WARN）——这条独立检查值得直接抄。

---

## 5. 失败与降级顺序：**lane 级熔断，别的 lane 照跑**（这正是我们缺的）

### 5.1 降级链

```
doUpstreamTiers  (internal/gateway/upstream.go:221-343)
  1. 诊断 key override ⇒ 单 key 短路
  2. route.Anonymous ⇒ doAnonymousUpstream
       · 熔断器 AllowAttempt() 为假 ⇒ errAnonymousLanePaused，**0 次上游调用**（:374-376）
       · 匿名 cursor（会话亲和）walk，limit=min(出口数, max_anonymous_exits=8)；
         有 #pool 时按 max_attempts 续抽（:393-396, :428-446）
       · 墙快速让位（v1.5.6/7）：实测墙 armed 且存在可用 key ⇒ limit=1、
         attemptTimeout=min(5s, attemptTimeout)（:397-408）
       · blocked / deterministic / geo 三个 halt 标志（:495-552）
       · 所有出口都被 IP 封 ⇒ anonymous.ForceOpen()（:553-560）
  3. 若 2 命中 errDeterministicClientRejection ⇒ 直接返回该 4xx，**不进任何 key tier**（:239-246）
  4. 若 2 命中 errFreeTierIPBlocked ⇒ blocked=true ⇒ **跳过 Zen key 道但保留 Go 道**（:247-254, :276-280）
     （Go 道有鉴权、不受免费层 IP 门 ⇒ 这是 lane 级保护的具体体现）
  5. ctx 已过期 ⇒ 立刻返回，**不进 key 道**（:259-270，注释：否则会对死上下文瞬时打空尝试并冤枉冷却健康的 key）
  6. 遍历 route.KeyTiers（prefer 序）⇒ doKeyUpstream
       · nodes 空 ⇒ 该道不可用，下一道（:936-938）
       · 节点 cursor（会话亲和）最多 max_attempts=3；node==nil 即 break（:959-962）
       · ctx 已过期 ⇒ 不入队 ⇒ 不冷却 key/proxy（:1003-1013，"冤枉健康 key"守卫）
       · WARP 下 errWARPUnavailable ⇒ break，"等同一个池的另一把钥匙没用"（:1046-1050）
       · GEO ⇒ return errGeoBlocked（:1029-1036）
       · 非重试 4xx ⇒ return（:1037-1044）
  7. errGeoBlocked ⇒ 404 model_not_available_in_region，**不再试其他 tier**（:294-299）
```

终态映射表（`gateway.go:469-514`）：

| 最终错误 | 状态 | code |
|---|---|---|
| `errProxyUnavailable` | 503 | `proxy_error` |
| `errWARPUnavailable` | 503 | `warp_unavailable` |
| `context.DeadlineExceeded` | 504（write 阶段 ⇒ **413** `payload_too_large`） | `upstream_timeout` |
| `errFreeTierIPBlocked` | 503 | `free_tier_ip_blocked` |
| `errGeoBlocked` | **404** | `invalid_request_error` / `model_not_available_in_region` |
| `errAnonymousLanePaused` | 503 | `free_tier_lane_paused` |
| 预检命中 | **429** | `insufficient_quota` + `Retry-After` |
| 并发闸满 | **429** | `rate_limit_error` / `gateway_busy` + `Retry-After: 5` |
| 客户端先走 | 499 | `client_canceled` |
| 其他 | 502 | `upstream_error` |

### 5.2 lane 级熔断保护 —— 实机确认

匿名道是**独立熔断器**（`anonymousNodes.breakerUntil`，`pool.go:176-274`）：
- `AllowAttempt`：开着全拒；窗口过 ⇒ 恰好一个 caller 赢半开探测（CAS）
- `NoteSuccess` 清零；`NoteFailure` 累计连续失败达 threshold ⇒ trip；`trip` 指数 `base << min(strike-1,30)` 封顶 max
- 匿名道被封不影响 key 道：实测 `anonymous_breaker_open paused_for=1h0m0s` 之后 key 道独立把请求跑成 200（§7.5）
- 反向也成立：`quotaWall()` 只在**所有** lane 都不行时才拦（§1.2），单 key 被墙时其余 key 照跑（§7.5 mixed 实测）

### 5.3 429 / 403 / 5xx / 超时 各自的重试与退避

| 情况 | 上游请求会不会重复发 | 退避/时长 | 代码 |
|---|---|---|---|
| 429 quota 墙（key） | 换 key（≤max_attempts），全墙则预检拦下 | `quotaUntil`=Retry-After 或 15s→8x | `pool.go:885-897` |
| 429 quota 墙（分组） | 换模型，当前模型按 Retry-After（无则 60s）暂停，**不烧普通失败预算** | 同上 | `rotation.go:155-176, 269-285` |
| 429（匿名道） | 换出口 | `cooldown×2^n`，Retry-After **钳到 8x 上限**（理由见 `pool.go:380-388`） | `pool.go:395-410` |
| 429/403（WARP 出口） | 换出口（立刻 retire + 后台重注册），**不冷却 key** | 出口冷却 **30min（固定、无递增）**，按 /64 | `exit.go:422-443`；`pool.go:376-412`；`warp.go:367-388` |
| 403 鉴权 | 换 key | 同 cooldown 指数表 | `pool.go:886` |
| 403 IP 滥用 | 换出口（不算 key 错） | `free_tier_block_seconds` 900s | `abuse.go:41`；`upstream.go:502-512` |
| 403/4xx GEO | **不换** ⇒ 直接 404 | — | `abuse.go:169-180`；`upstream.go:1033-1036` |
| 400/422 确定性 | **不换** ⇒ 原样回传 | — | `abuse.go:126-137`；`upstream.go:520-527` |
| 5xx / 401 | 换 key | 指数 cooldown，Retry-After 覆盖 | `pool.go:886` |
| 传输失败（拒绝/超时） | 换出口/换 key；`isProxyFailure` ⇒ 该 proxy 判死并 `RebindProxy`（least-load 迁移、清计数） | 同上 | `pool.go:583-595, 725-752` |
| 首事件超时 | 算 proxy 失败（但 `upstreamAccepted()` ⇒ 不算） | `first_event_timeout_seconds` | `first_event.go:63-69` |
| idle | 中止该请求，算一次失败；**记为 `upstream_idle_timeout` 而非 client_canceled** | `idle_timeout_seconds` | `keepalive.go:107-131` |

---

## 6. 路由 / 多 key 池 / 会话亲和

- key 池：`zen_keys` 与 `go_keys` 两个**独立** `upstreamNodes`（`gateway.go:137-144`）；数量不设上限，示例 `config.example.json:5-6` 为空数组（免费层匿名 + 外部托管 key）。
- 选择策略：**轮询 + 健康分层偏好**（不是健康分、也不是加权）
  - `Cursor()`：`next.Add(1)-1` 取模 ⇒ 每个并发请求不同起点（`pool.go:792-797`）
  - `CursorFor(affinity)`：FNV-64a(session) 取模 ⇒ **会话粘性到 key**（`pool.go:799-810`）
  - `Next()` 四级优先（`pool.go:818-860`）：① 健康 proxy + 未冷却 ② 健康 proxy 中最早冷却 ③ 不健康 proxy + 未冷却 ④ 全局最早冷却。**非空池永不返回 nil（冷却只是降级偏好）**——`docs/DEVELOPMENT-PLAN-v1.5.0.zh-CN.md:247-248` 点明：所以预检是唯一能在"全墙"时拦住上游调用的闸。
- key↔出口绑定：`proxyIndex := i % len(transports)`（`pool.go:653-660`），key 少于 proxy 时多余 proxy 故意闲置，作为故障接管备援（`pool.go:636-639` 注释）；proxy 故障 ⇒ `RebindProxy` 全量迁移到 least-load，恢复 ⇒ `RestoreProxy` 迁回并清计数（`pool.go:663-690`）。
- 会话与 prompt cache 亲和：`internal/identity/request.go:27-49, 75-79`
  - session id 派生：`x-opencode-session`/`x-session-affinity`/`x-session-id`/`session_id`/`metadata.user_id`/（system prompt + 首个 user turn）
  - **作用域到调用方 server key 指纹** ⇒ 不同本地 key 永不共享 upstream session / 首选 key / 匿名 proxy（`README.md:300`）
  - 亲和的**故意不复用**：stale reasoning 引用重放保留 client session，"新 mint session 会落到不同节点、prompt cache 变冷、且把回复与 client 仍持有的链断开"（`upstream.go:120-126`）——这条是我们该直接照搬的语义。
- 分组轮换（sota/sweet）：`rotation.go:93-192`，每模型最多 3 次单调用尝试；quota 失败 ⇒ 按 Retry-After 暂停该模型；普通失败第 3 次 ⇒ 暂停 15s；3 个模型普通失败即结束 ⇒ 503 `rotation_unavailable`（预算尽则 504）。

---

## 7. 对我们最有用的机制（落到文件）

前提：**它自己有 WARP（且默认关）**，所以"能不能直接治我们的 503"要分两层回答。

### 机制 A：quota 墙一等公民 + 零调用预检 —— 抄，但只能当**设计参照**

落点 `core/manager/freetier_engine.go`、`core/manager/freetier_models.go`。

需要抄的语义：
1. `cooldownUntil` 与 `quotaUntil` **分字段**；429 ⇒ 两者同置，传输失败 ⇒ 只置前者。
2. `MarkFailure` 按 `resp.StatusCode==429` 触发 quota 标记；时长 = max(15s×2^n 封顶 8x, Retry-After 全额)。
3. 预检 = `AllQuotaCooled()`（全部 key 在 quota 冷却，返回最早到期）+ 其他道都不可用。
4. 响应 `429` + `Retry-After=<最早到期秒数>` + `insufficient_quota`。
5. **list 成功只 `MarkListingSuccess`（不清 quota）**——这条对我们同样致命：我们如果有任何"目录刷新成功 ⇒ 认为 key 可用"的代码路径，预检就会失效。
6. 匿名道旁路改成经验信号（≥2×429 / 10min，2xx 即解除）。

**能不能直接治 503？—— 能，且立竿见影。** 我们现在 100% 503 且每笔请求还在真实打上游；加预检之后：第一笔（或第二笔）真实撞墙、把 `quotaUntil` 写进账本，之后的请求毫秒级返回 429+`Retry-After` ⇒ 上层立刻降级到付费道，而不是每笔都在死墙上重试烧尝试。这是纯逻辑改动，不需要 WARP，也不需要新出口。

落点 `vendors/custom/keypool.go`：它已有 429→600s 冷却、403→600s/永久禁用、30s 熔断（`vendors/custom/keypool.go:27-73`）。缺的正是"全池墙 ⇒ 零调用拒绝"。

### 机制 B：墙期反哺 —— **只抄形状，不抄实现**

落点 `core/manager/freetier_warp_fallback.go`。它反哺的是**外部控制面**（`POST /v1/battery/event`），我们只有 7890 一个共享海外出口，没有等价控制面。真正可抄的两件事：
1. 节流器：per-node `rebindFiredAt` + CAS + 非阻塞有界 channel（满则丢，保留时间戳）。我们做"换出口/重连/清池"这类动作时都该是这个形状。
2. 挂点选择：**探活的 `still_walled` 分支是墙期唯一观察者**，任何"墙期反馈"必须同时挂 live 路径与探活路径——否则全墙时反馈永不发。
3. 边界条件：`rebind_on_wall` 无 `enabled` ⇒ **加载期直接拒绝**（`CHANGELOG.md:82-84`）。

### 机制 C：逐流 idle 静默时钟 + 心跳 —— 抄，落点明确

落点 `core/router/router.go`（流转发处）+ `freetier_engine.go`。见 §4.4。

### 机制 D：`egressKey = /64`（IPv6）或 `/32`（IPv4）

落点 `freetier_warp_fallback.go`。这是"一个坏 /64 烧整池"的**根因描述**：出口去重键必须是**上游实际计费的粒度**。我们如果继续用 WARP，冷却账本的键必须是 /64；若换普通代理池，则每条代理各自独立冷却 + **必须递增/可探测**。

### 机制 E：出口冷却**必须带主动探活**，不能靠固定时长

这条是它自己没做好的地方（见 §9），但它的 key 道给了正确范式：`probeNextAt` + 最小真实请求 + 成功即清冷却。**我们的 WARP 出口"固定 24h、不递增、持久化、重启不自愈"应当改成：固定短冷却（30min 量级）+ 每 N 分钟一条真实小请求探活 + 探活成功立即回池**——重启不自愈的问题随之消失（账本不落盘反而成了优点）。

### 关于"它有没有 WARP"——**有，且默认关**

`internal/warp/`（`pool.go`/`exit.go`/`registrar.go`/`bind.go`/`freebind_linux.go`）是**完整的用户态 Cloudflare WARP**（WireGuard + gVisor netstack），不需要 TUN、不需要 root、不需要 NET_ADMIN。`config.example.json:8` `"enabled": false`；`warp.pool_size` 1–32 默认 2；`warp.stack` 默认 `ipv6`；`warp.source_ipv6` 留空 ⇒ 用宿主默认源地址。

⇒ **不能作为"第三个否决 WARP 的先例"**。但它给了三个**支持我们决策**的硬证据：
1. `README.md:266` 自陈**病态退化**："On networks whose WARP egress pool is saturated every fresh exit can still answer 429; the gateway then churns exits without ever surfacing a 429 before the attempt budget is spent." —— 与我们 40080 的症状同构。
2. **它把出口失败与 key 失败严格分账**（这正是我们的 40080 犯的错）：`observeKeyResultWARP`（`internal/gateway/warp.go:367-388`）对「无响应 + 传输错误」（`:376-380`，租约/拨号/首事件失败 ⇒ `return`，不冷却 key）、以及 **429 与 IP-abuse 403**（`:381-386`，"A 429 or a network-abuse 403 is scoped to the WARP exit, which the pool has just retired; holding it against the key would stop a single-key rotation after the first attempt instead of retrying on a fresh exit" ⇒ `return`）三类出口症状**一律不写入 key 账本**。出口问题只让出口自己承担（retire + 后台重注册）。
3. **诚实的错误出口**：无出口 ⇒ `errWARPUnavailable`（`warp.go:25-33`），终态映射 **503 + `code:"warp_unavailable"`**，消息直接点名"上游未被联系，请检查 WARP 开关与到 Cloudflare 端点的 UDP"（`upstream.go`→`gateway.go:473-476`）；匿名道被熔断 ⇒ **503 + `free_tier_lane_paused`**（`gateway.go:510-513`，消息 `abuse.go:32-35` 给出三条出路：配鉴权 key / 等窗口 / 换出口后重启）。readiness 层：WARP 开着但零活跃出口 ⇒ `warp_unavailable` 阻塞（503），而 `no_healthy_proxies` **只在 WARP 关闭或存在 Go key 时**才阻塞（`health.go:143-152`）⇒ 免费道出口坏了不会把整个网关判死。
4. **但要说清它并没有解决我们那个结构性问题**：`usesWARP()`（`warp.go:38-40`）= `warp != nil && IsFreeModel && (anonymous || tier==zen)` ⇒ **带 Zen key 的免费请求同样只走 WARP、不回落到 proxy**（`upstream.go:949, 997-1001`），且 WARP 无出口时 `break` 整个 key 循环（`upstream.go:1046-1050`，理由"其他 key 只会等同一个池"）。**结构与我们的 40080 同形**。差别只在两处：(a) 它不因此污染 key 账本（第 2 条）；(b) `warp.enabled` 默认 false（`config.example.json:8`），关掉之后 key 的免费流量**照走 proxies**（`README.md:266`："Turning WARP off restores proxy routing for the free lanes"）——这与我们实测"关 WARP + 10 key 走 7890 ⇒ 5/5 成功"完全一致，等于第三方样本对"key 道不应被出口开关劫持"的再次确认。

---

## 8. 许可与可用性

**GPL-3.0**（`LICENSE`，35149 B）。与我们现有引擎同源许可 ⇒ **只能独立进程使用，不能链接进我们的 Go 二进制**。

---

## 8.5 构建/运行需求（已实测编译通过）

- Go 1.24+（本机 1.26.5 直接 `go build ./cmd/opencode2api` 成功，`GOPROXY=off` 走模块缓存）
- 依赖仅 5 个直接依赖：brotli / klauspost compress / utls / x/crypto / **golang.zx2c4.com/wireguard**（后者拖进 gVisor netstack + wintun，即 WARP 实现）
- 无数据库、无 Node 运行时（WebUI 内嵌于可执行文件）
- WARP 才需要出站 UDP 2408；TetherPool 才需要外部 IP-Pool v2 控制面（`base_url`，可选 Bearer token）

**TetherPool 是否要第三方账号/付费？—— 需要第三方服务，不需要账号；本验证用自写假控制面跑通全部 provision + battery 路径。**

---

## 9. 它没做好的部分（**直接印证我们的判断**）

| 它的做法 | 问题 | 我们的对照 |
|---|---|---|
| WARP 出口冷却**固定 30min、无递增**，按 /64 键（`warp/pool.go:17-20`） | 若上游对同一 /64 反复给 429，会持续 churn 注册（`README.md:266` 自陈） | 我们"固定 24h"是同一 bug 的更糟版本——它至少短且带主动重注册 |
| 出口死亡靠**重新注册新设备**救（`pool.go:414-476` `replace`），失败时才把旧出口放回去（:427-435） | WARP 池饱和时"每次新出口都还是 429"（它自己写的） | ⇒ 印证"WARP 池饱和不可靠"，支持走 7890/稳定代理 |
| `rebind_on_wall` 默认关，且**换绑只解 IP 分量**（`CHANGELOG.md:59-60`） | 账号侧仍需等窗口 | ⇒ 印证"配额主要绑账号"，多 key 的边际价值取决于账号余量 |
| 探活请求**真实烧额度**（`quotaprobe.go:14-22`，尺寸 ≤8 token、全局并发 1、间隔 ≥600s） | 无法避免（上游无额度 API） | 我们同样只能这么干；它给的约束尺寸可直接用 |
| `usesWARP()`（`warp.go:38-40`）确实把 Zen key 的免费流量也锁进 WARP | 无出口时靠 `errWARPUnavailable` + 不冷却 key 缓解，但**仍不会回落到 proxy** | 我们的引擎在此处 `break` 放弃整道；两者都需"带 key 的免费请求可回落 proxy"才真正健壮 |

---

## 10. 一页纸结论

1. **quota preflight = 本地账本 + 零探测**：`MarkFailure` 按 **status==429** 写 `quotaUntil`（时长取 Retry-After 全额）；`quotaWall()` 要求"每条 key 道都 AllQuotaCooled" **且**"匿名道不可用（关闭 ∥ 熔断 ∥ 实测墙：10 分钟内 ≥2 次 429）"才拒绝；拒绝时 429 + `insufficient_quota` + `Retry-After`，**0 次上游调用**。上游没有额度 API，所以账本必须靠真实请求撞墙来建立——它用探活循环把"下一次撞墙"从客户请求提前到网关后台。
2. **免费额度的维度它写死了**：`docs/DEVELOPMENT-PLAN-v1.6.0.zh-CN.md:13` + `CHANGELOG.md:59-60` ⇒ **账号(key) + 出口 IP 双维**；换出口只解 IP 侧；免费层**按请求数**计（`DEVELOPMENT-PLAN-v1.5.0.zh-CN.md:244`）。
3. **TetherPool per-key binding = 1 key ↔ 1 粘性出口（实机证实）**：account_id 由 key 后 5 位派生；allocated 列表按 `zen_keys` 序前置 + `i % N` 位置绑定 ⇒ key/出口 1:1；all-or-nothing，失败整体回退静态并 5min 自愈重试。冷却账本两层：key 层 per-node（有递增、不持久化）、出口层按 **/64(/32)** 固定 30min。
4. **idle watchdog 是"逐条已提交流的上游静默时钟"**：独立于下游写时钟、ticker `min(2s, idle/3)` 下限 50ms、只 cancel 该请求上下文、结果标 `upstream_idle_timeout`。→ 对我们：保留 probePort 做存活层，另加 (a) 15s SSE 心跳、(b) 逐流上游静默 abort、(c) `attempt_timeout < retry.timeout` 启动自检。
5. **它的降级链有 lane 级熔断**：匿名道独立 breaker（threshold + 指数 base→max + 半开单 caller），`errFreeTierIPBlocked` ⇒ 跳 Zen key 道但**保留 Go 道**，GEO/确定性 4xx ⇒ **立即停止跨 lane 重试**（实机确认：匿名道熔断 1h 期间 key 道照常 200）。但**要如实指出**：在 WARP 开着且无出口时，它的 key 循环同样 `break` 放弃整道（`upstream.go:1046-1050`），与我们的 40080 同形——它的改进点是「出口失败不污染 key 账本」+「默认不开 WARP」，而不是「出口坏了 key 照走 proxy」。
6. **最值得抄的三件事**：(A) 429 写账本 + 零调用预检（治"每笔请求都在死墙上重试"）；(B) `MarkListingSuccess` 与 `MarkSuccess` 分离（否则预检会被目录刷新每 5 分钟清空，v1.5.0 实修过的 bug）；(C) 探活主动清冷却（把"等 Retry-After"变成"提前恢复"）。
7. **它有没有 WARP？有，且是完整的用户态实现。** 所以"第三个否决 WARP 的先例"这条**不成立**。但它的 `README.md:266` 自陈 WARP 池饱和时"churn exits without ever surfacing a 429"、它把 WARP 默认关、它的 `observeKeyResultWARP` 明确"不因为出口问题冷却 key"——这三条仍**强烈支持**"免费层走 WARP 是脆弱方案"的判断。
8. **在它没有 WARP（`warp.enabled=false`）的前提下能不能直接治我们的 503？**
   **能，而且是立刻见效的那一半。** 预检让 100% 503 变毫秒级 429 并携带真实 `Retry-After`，上层立即降级到付费道，不再每笔都撞。恢复那一半：机制 E（出口短冷却 + 主动探活）+ 带 key 的免费请求允许回落 proxy（不再 break）。**但"墙本身"它治不了** —— 它的答案是"用 429 + Retry-After 让上层换渠道"。这正是我们该采纳的产品语义。

---

## 附：本次引用文件清单

`CHANGELOG.md`、`README.md:13-17, 258-266, 418-433`、`config.example.json`、`go.mod`、
`docs/DEVELOPMENT-PLAN-v1.5.0.zh-CN.md`、`docs/DEVELOPMENT-PLAN-v1.6.0.zh-CN.md`、`docs/management.zh-CN.md:35`、
`internal/gateway/quotaprobe.go`、`internal/gateway/pool.go`、`internal/gateway/upstream.go`、
`internal/gateway/warp.go`、`internal/gateway/abuse.go`、`internal/gateway/gateway.go`、
`internal/gateway/health.go`、`internal/gateway/availability.go`、`internal/gateway/rotation.go`、
`internal/gateway/tetherpool.go`、`internal/gateway/refresh.go`、`internal/gateway/first_event.go`、
`internal/tetherpool/client.go`、`internal/tetherpool/provision.go`、
`internal/warp/pool.go`、`internal/warp/exit.go`、`internal/warp/registrar.go`、
`internal/protocol/keepalive.go`、`internal/identity/request.go`、`internal/models/discovery.go`、`internal/admin/model_quotas.go`
