# Opencode2api 通道机制调研（2026-11-11）

> 调研对象：`D:\AI_Projects\refs\Opencode2api`（GitHub `Nystic-Shadow/Opencode2api`，本地 HEAD `b9b81dc`）
> 性质：**只读**。未改其代码、未停 40000/40080 生产进程；实验端口 40190–40193，已全部释放。
> 对照基准：`00-current-state-and-tests.md`（生产 503 全因 `WARP pool has no ready egress`，key 从未打到上游）。
> **实机已验证**（非纯静态）：用零依赖直接跑起，结果见 §Q5 与 §实验记录。

## 一句话结论

**它是「我们方案的 WARP 分支被删掉之后的版本」**：同一个上游、同一个匿名免费层、同样的地区限制，但它把 WARP 当作**失败设计整块拆除**（`2f12aa6 refactor(egress): remove WARP entirely`，删掉 1812 行 `server/warp.js`，此前有 40+ 个 WARP 提交踩的正是我们这几周的坑），换成「显式代理出口池 + 三层冷却账本 + 快速失败并回报真实 Retry-After」。**不值得整体替换**（JS 双进程 vs 我们 Go；且它没有 key 池，产能只会被打上 `Bearer public` 这个今天已耗尽的共享桶），**但它的 3 个机制应当立刻抄进 Go 引擎**（§Q7）。

**今晚最该做的一件事（不是抄机制）**：我们的 503 与它的 429 **不是同一个根因**——我们的是"上游根本没被问到"的自伤 bug（§实验记录 F）。E1 已证明 **key + 7890 路径此刻仍是通的**，所以先修 WARP 空池/pid 管理 bug 就能恢复产能。

---

## 仓库初览（文件规模即是架构线索）

`server/` 18 个文件：

| 文件 | 大小 | 职责 |
|------|------|------|
| `proxy.js` | 97 KB | 核心转发：通道选择、重试、降级 |
| `egress.js` | 54 KB | 出口池（egress route / 代理池 / 健康与冷却） |
| `proxyDiscovery.js` | 30 KB | 代理自动发现（爬虫 + 筛选） |
| `adminRoutes.js` | 25 KB | 管理面 |
| `index.js` | 20 KB | 入口/HTTP 服务 |
| `config.js` | 18 KB | 配置装载与迁移 |
| `bunBridge.js` | 18 KB | Bun 原生桥（Node 子进程） |
| `discovery.js` | 18 KB | 模型/能力发现 |
| `keys.js` | 11 KB | **入站**网关 API key 管理（**不是**上游 key 池） |
| `metrics.js` | 11 KB | 指标 |
| `bunProxy.js` | 9 KB | Bun FetchSession 代理执行侧 |
| `upstreamQuota.js` | 5 KB | 上游全局额度闸门（**见 §Q4 死代码发现**） |
| `requestQueue.js` | 4 KB | 请求排队（准入控制） |
| `upstreamContext.js` | 4 KB | 上游上下文（CLI `<env>` 块） |
| `identity.js` | 4 KB | 会话/项目 ID 铸造、上游头 |
| `updater.js` | 6 KB | CLI 版本号热更（只改配置，不装包） |
| `notifications.js` | 2 KB | 面板内通知（**无外发告警**） |

---

## Q1 通道模型：**纯代理出口池，WARP 已被彻底移除**

### 结论先行
它**没有** WARP/匿名出口池概念——不是"我们还没加"，而是**代码里曾经有、后来删干净了**：

```text
server/adminRoutes.js:406-409
  if (Object.keys(body).some(key => key.startsWith('warp_'))) {
    // WARP was removed. Accepting these fields silently would leave an
    return sendJson(res, 400, { error: 'WARP support has been removed. Automatic proxy
      discovery is built in; set PROXIES only to add your own routes.' });
```
```text
server/config.js:176-179   // 启动时删除旧配置里持久化的 warp_* 键
server/config.js:249       // 'warp_only' 等遗留 mode 一律 fail-closed 到 auto
```

全仓 `grep -i warp` 只剩这几处"清理遗留"的代码。

### 决定性证据：它的演进路线正是我们要走的那条

`git log --grep=warp` 有 **40+ 个 WARP 提交**，标题触目惊心——**和我们这几周踩的坑一字不差**：

```text
2a3f46d feat(warp): integrate Cloudflare WARP proxy with unauth auto-switching and key rotation
86e654a fix(warp): eliminate 429 streaks with 8-slot multi-port pool, anti-1015 registration backoff...
0d9007c fix(warp): throttle Cloudflare registrations to prevent error 1015 & add 10m pool cooldown rotation
be811a3 fix(warp): aggressive account burn-and-replenish on 429, zero-staleness socket destruction
2f62bc0 fix(warp): eliminate cascade pool burn, prevent IP flapping, and enforce dual-stack IPv6
c4335c4 fix: patch Bearer public bucket leak and implement IPv4/IPv6 dual-stack multi-endpoint WARP pool
8743edf fix: resolve warpRotatedInThisRequest ReferenceError ...
acd5ead fix(warp): eliminate port collisions with port mutex, enforce clean IPv4 WARP egress...
```

然后：

```text
2f12aa6 refactor(egress): remove WARP entirely          (2026-10-26)
        server/warp.js  | 1812 ------------------- 全文件删除
        scripts/install-warp.sh | 109 --- 删除
        总计 21 files, +241 / -3091
```

CHANGELOG 里给出的**删除理由**（原文）：

```text
## [Unreleased] - 2026-10-25 — Proxy-Only Egress, Bounded Admission, and Honest Quotas
> This release supersedes the quota-rotation and WARP-tunnel design described in the entries below.
> The gateway no longer rotates egress IPs to evade upstream limits, and WARP support has been removed entirely.

- **No quota evasion**: automatic identity rotation on 401/429 is removed. Upstream quota is returned as
  429 with the real Retry-After; the route stays in /readyz so a temporary quota window does not mark the
  deployment unhealthy.
- **Anonymous by default in Compose**: EGRESS_MODE=proxy_only and ALLOW_DIRECT_FALLBACK=false. The gateway
  returns 503 no_egress_route instead of silently leaking the host IP.
```

→ 两个立场值得记住：
> ① **"honest quotas"**：烧出口 IP 救不了额度，不如如实回报 429 + 真实 Retry-After。
> ② **"route stays in /readyz"**：出口临时冷却 ≠ 部署不健康（避免把 503 打成健康检查失败）。

### 通道 = EgressManager 管理的「出口路由」四类来源

核心类 `server/egress.js:220 class EgressManager`（1534 行），字段见 `egress.js:221-274`：

| 通道种类 | 来源 | 代码 |
|---------|------|------|
| **manual proxies** | `PROXIES` 逗号分隔 | `egress.js:143 parseProxyList`、`config.js:117` |
| **auto proxies** | 内置免费代理爬虫（`proxyDiscovery.js`） | `egress.js:508 setAutoProxies`（按 latency 排序，上限 `max_auto_pool`=100） |
| **rotating proxy** | 单个住宅代理网关，每次换 exit IP | `egress.js:372 _loadRotatingProxy`、`config.js:118-121` |
| **direct** | 本机直连（**默认允许！**） | `egress.js:1078`、`config.js:122 allow_direct_fallback` 默认 `true` |

合并 `egress.js:292 _mergeProxyLists`：manual 优先于 auto，同 id 去重，总量 `max_proxy_pool`=150。
爬虫源 `proxyDiscovery.js:26-99`：13 个公开列表（proxifly US/CA/GB、proxyscrape elite、spys.me、hideip.me 等），**只收 HTTP/HTTPS**（`egress.js`+README 明确 SOCKS5 与 Bun 桥不兼容），并强制 **每 host 唯一**（v3.2.3）。

### 关键差异：**它没有 key 池**
`config.js:116 upstream_zen_key` 是**单个**上游 key（`OPENCODE_ZEN_KEY`），注入 `identity.js:97,111-115` 的 `Authorization`；**没 key 就发 `Bearer public` 走匿名免费层**。
`server/keys.js` 名字有误导性——它是**入站**网关 key 管理器（签发/限流 `O2P-xxx` 客户端 key，`keys.js:21 class ApiKeyManager`），**与上游 zen key 无关**。

→ 我们的「10 个 zen key 轮询」在它这里不存在。它的多样性全押在**出口 IP**，其自我认知写在 `upstreamQuota.js:1-12`：

```text
OpenCode's anonymous free tier ("Authorization: Bearer public") is metered
in one shared bucket, not per client IP. ... Rotating egress IPs therefore
cannot recover capacity.
```

### 三种 egress_mode
```text
config.js:121      egress_mode = EGRESS_MODE || 'auto'    // auto | proxy_only | direct
config.js:251-253  白名单校验，非法值收敛为 auto
config.js:256-262  自动迁移：没配 PROXIES 且没显式设 EGRESS_MODE ⇒ 强制 auto + allow_direct_fallback=true
egress.js:1068-1102 getCandidates()：rotating 独占优先；否则按 mode 组池并过滤 isAvailable
```

⚠️ **`egress.js:1097-1101` 有一处极易误读的语义反转**（阅读时必须注意）：
```text
return routes.filter(route => {
  if (!this.isAvailable(route)) return false;
  if (!includeQuota && this.getQuotaRetryAfter(route) > 0) return false;   // ← 名字反着
});
```
`includeQuota` 默认 `true` ⇒ **默认不过滤**正在 quota 冷却的路由；只有传 `includeQuota:false` 才排除。`getRoutableRoutes()` 传 `false`（选路要避开冷却），`getCandidates()` 裸调用传 `true`（就绪/统计要算上它们，对应 CHANGELOG 的 "route stays in /readyz"）。

---

## Q2 通道选择 / 重试 / 降级顺序 / 冷却账本

### 2.1 失败分类（一处集中定义，两个 handler 共用）
```text
egress.js:176-198 classifyUpstreamFailure(statusCode, body) -> 'quota' | 'retry' | 'fatal'
  :180  429                                   -> quota
  :181  403 + /freeusage|freetier|capacity|rate.?limit|quota|too many/ -> quota
  :184  403 + /regionerror|not available in your country/              -> retry   ★★★
  :187  0/408/425/500/502/503/504             -> retry
  :191  400/401/403/404/405/409/413/422       -> fatal（其余 403 一律 fatal）
  :194  ECONNRESET|ECONNREFUSED|ETIMEDOUT|ENOTFOUND|socket hang up|SOCKS5|CONNECT -> retry
```
**:184 直接命中我们 E2 实验里那句 403「This model is not available in your country」**：它把"地区拒绝"归为**可重试的出口问题**（换出口），而不是 fatal。**实机验证见 §实验记录 C**：只有 1 条出口且它是地区错时，它会把 3 次重试全部耗在同一出口上，最后改写成 502。

### 2.2 主循环的预算（不是简单的 maxRetries）
```text
proxy.js:460  maxRetries         = min(3, max_retries)                      默认 2
proxy.js:519  quotaFailoverLimit = min(20, proxy_quota_failovers)           默认 20
proxy.js:520  proxyFailoverLimit = min(15, proxy_transport_failovers)       默认 10
proxy.js:524  maxAttempts = maxRetries + 1 + quotaFailoverLimit + proxyFailoverLimit   ⇒ 最多 34 次
proxy.js:462  deadline = startTime + max(request_timeout_ms, request_deadline_ms=90000)
proxy.js:532-549  deadlineTimer + res.on('close')：客户端断开即销毁上游请求
proxy.js:598-600  代理路由单次 attempt 超时 proxy_attempt_timeout_ms=5000（直连不加此限制）
```
→ **90 秒总预算、最多 34 次尝试、代理单跳只给 5 秒**："短超时 + 大量换出口"，与我们的"长等待"相反。

⚠️ **实测踩坑**：`proxy_attempt_timeout_ms` / `rotating_proxy_timeout_ms` **不在 `DEFAULT_CONFIG` 里**（`config.js:85-125` 无此项），只能通过 `data/config.json` 落盘。默认 5 s 对**匿名免费层首包 >5 s 的模型是致命的**：§实验记录 A 显示上游明明返回 `200 OK`，Node 侧却报 `Proxy attempt timed out` ⇒ 假故障 + 冷却计数。**抄这个参数时，超时值必须按上游 P99 首字节时间设定，不能拍 5 秒。**

### 2.3 降级顺序（429/quota 分支，`proxy.js:1115-1179`）
```text
1) proxy.js:1116-1123  rotating 代理 ⇒ 直接 continue 换 exit IP，最多 quotaFailoverLimit 次
                       （不换 route，因为网关活着）
2) proxy.js:1125-1126  egress.recordFailure(route,'quota', retryAfterMs||60000)
                       + persistentSessions.rotate(keyId, routeKey)   ← 会话也换
3) proxy.js:1129-1137  getNextRoutableRoute(attemptedRoutes) ⇒ 池内换下一个代理，最多 20 次
4) proxy.js:1140-1146  canUseDirectFallback() ⇒ 全池试过 ⇒ 试直连（须未冷却）
5) proxy.js:1149-1164  smart_fallback===true 且 free 模型 ⇒ 换【模型】（triedModels 去重），
                       attemptedRoutes.clear() 重新洗一遍出口
6) proxy.js:1166-1179  全败 ⇒ 429 + Retry-After = max(retryAfterMs, 池内最早解除时间, 60s)
```
`smart_fallback` 判定是 **`=== true`**（严格开启，`proxy.js:422,1149`），默认 `config.js:101 SMART_FALLBACK === 'true'` ⇒ **默认关闭**，防"偷偷换模型"。

### 2.4 非 429 分支
```text
proxy.js:1182-1206  fatal ⇒ 原样透传上游状态码与 body（不重试、不换出口）——快速失败
proxy.js:1209-1213  代理传输失败 ⇒ recordFailure('proxy_down')，计数到 proxyFailoverLimit(≤15) 就 break
proxy.js:1214-1218  网络/5xx   ⇒ recordFailure('retry')，计数到 maxRetries+1(≤4) 就 break
proxy.js:1221-1230  取 nextRoute；取不到但 route 仍可用 ⇒ 指数退避 min(100*2^n, 600ms) 原地重试
proxy.js:1242-1246  循环耗尽 ⇒ 502 'All configured egress routes failed: <末次 body 前 300 字>'（504 单独）
proxy.js:1089-1113  bridgeFailure（Bun 桥挂）⇒ reportTransportFailure + ensureReady(2000) + 换出口
```

### 2.5 空池 / 无出口的处理（**对照我们 503 的直接答案**）
```text
proxy.js:465-468   selectRoute() 为空 ⇒ 若 allow_direct_fallback!==false 或 egress_mode==='auto' ⇒ 用 direct
proxy.js:469-485   仍无 route 且【存在 quota 冷却】⇒ 429 + Retry-After（不是 503！）
proxy.js:494-497   完全无 route ⇒ 503 'no_egress_route'，Retry-After: 5
```
→ **核心差别**：它在"池子被 quota 冷却填满"时报 **429 + 真实 Retry-After**（客户端可退避），只有"根本没配出口"才报 503。我们生产把"WARP 池空"一律报 503、不告知恢复时间，且**先烧 26 秒才发现没出口**（实测 503 用时 26 393 ms）。

### 2.6 冷却账本按什么维度记 —— 三层账本，互不混淆 ★★

**层一：出口健康账本（按 proxy id / host；不按 key、不按会话）**
```text
egress.js:983-987   _healthKey：type==='proxy' ? route.id : 'direct'      ← 维度 = 出口
egress.js:989-1008  _health 结构：failures / openUntil / retryAfterUntil /
                    checkFailed / checkFailures / proxyFailures / host / last*
egress.js:1420-1483 recordFailure(route, kind, retryAfterMs)：
   :1426-1441 kind 对 rotating 网关全部【降级为只记 lastError】——
              quota 不设 retryAfterUntil（:1427-1431）、proxy_down 不设 openUntil（:1433-1439）
              注释：One failing exit node must never disable the entire gateway
   :1444-1458 quota  ⇒ retryAfterUntil = max(旧, now + max(1000, retryAfterMs))
              ★ :1450-1457 同 host 兄弟端口【共享冷却】（415ce0f 加的）
   :1461-1470 proxy_down ⇒ 指数退避 openUntil = now + min(checkInterval, 30s * 2^min(fail-1,6))
   :1473-1480 retry      ⇒ failures++，满 3 次 ⇒ openUntil = now + 30s 并清零
egress.js:1010-1020 isAvailable = !checkFailed && openUntil <= now
egress.js:1030-1040 getQuotaRetryAfter：rotating 恒 0；否则 retryAfterUntil 剩余
egress.js:1046-1054 getEarliestQuotaRetryAfter：全池最早解除时间（⇒ 用作 Retry-After）
egress.js:1407-1418 recordSuccess(route)：一次成功即把该出口的 failures/openUntil/
                    retryAfterUntil/checkFailed/checkFailures/proxyFailures/lastError
                    【全部清零】（proxy.js:716,1675 调用）——注意这会连带洗掉额度冷却
egress.js:1485-1491 _recoverExpired 到期自动清除（无持久化）
```

**这些行为都有断言测试**（`test/reliability.js`，52 KB，可当我们 Go 单测的用例清单）：
```text
:230 distinguishes quota, retryable, and fatal failures
:270 quota cooldown does not make a route unavailable        ← 对应"冷却≠不可用"
:285 quota cooldown always has a minimum delay
:292 resetRoute clears stale quota and notifies listeners
:303 three retryable failures open the circuit and success closes it
:393 keeps a quota-limited route in the candidate list
:440 skips quota-cooling routes and reports the earliest retry  ← 对应 Retry-After 计算
:904 never shortens an existing reset with a shorter Retry-After
:926 defaults to a 15-minute block when Retry-After is missing
:1171 never poisons the quota gate from a direct Node 429      ← 但函数已无人调用（见层二）
```

**层二：全局上游额度闸门（按 model，跨所有出口）** —— `upstreamQuota.js`
```text
:16      FREE_LIMIT_PATTERN = /FreeUsageLimitError/i
:19      let state（全局）; modelStates = Map<modelId, state>
:50-86   recordFailure：仅当 429/403 【且 body 含 FreeUsageLimitError】才记账
         duration = min(7d, 上游 Retry-After || 15min)；:62-63 只延长不缩短
:109-135 isBlocked(model) ⇒ 先查该 model 账；无该 model 账 ⇒ 不阻塞
:27-32   消息直说"这是全局限额，换代理没用，请配 Go/Zen key 或等重置"
调用 proxy.js:420-446   请求进来先查闸门 ⇒ 直接 429 + freeQuotaHeaders，一个出口都不试
```
🔴 **重要缺陷（实机证实 + git 追因）**：**`upstreamQuota.recordFailure` 在当前 HEAD 的生产路径里没有任何调用点。**
`grep -rn "recordFailure" server/` 命中 15 处，全部是 `egress.recordFailure`；`proxy.js` 里含 `upstreamQuota` 的行共 **19 行** = `:13` require + **14 处 `clear()`（全在成功路径的"撤销记账"）** + **4 处 `isBlocked()`（读）** + **0 处 `recordFailure()`（写）**。

追因（这就是它为什么变成这样）：
```text
a80a6b9 fix(quota): fail fast on global free-tier exhaustion with exact reset   ← 接线
a35fe1d fix(egress): eliminate 429 cascades with pool-first proxy failover...   ← 【拆掉接线】
   -      const isDirectRoute = activeRoute && activeRoute.type === 'direct';
   -      const globalFreeQuota = isRequestedFree && !officialDirect && isDirectRoute
   -        ? upstreamQuota.recordFailure({ statusCode, body: bodyStr, retryAfterMs, model: currentModel })
   -        : null;
```
即：作者为了让"池内还有代理就不该放弃"（pool-first failover），把**全局额度记账的写入整个删掉，却保留了 16 处 clear() 与闸门读取** ⇒ 留下一套**只被清空、从不被填充**的账本。
连带 `bunBridge.js:64 isDirectOfficialAttempt`（配套函数，注释："Only requests actually carried by the Bun bridge may poison the global free-tier quota gate"）也只剩 `proxy.js:17` 导入与 `:2307` 再导出，**请求路径不再调用**——同一处拆除的残留。

实测印证（§实验记录 D）：`/readyz` 恒报 `upstream_quota:{blocked:false}`，而客户端已拿到 `Retry-After: 79907`；`/api/proxies/status` 的 `upstreamQuota` 字段同样恒 false。
⇒ 快速失败（1~2 ms）实际**全部来自层一（路由级 quota 冷却）**，不是宣传的全局闸门。
⇒ 真实后果：
1. **单出口/小池场景**碰巧正确（层一把唯一出口冷却了，`getCandidates()` 空 ⇒ `proxy.js:469-485` 报 429 + 最早解除时间）。
2. **多出口场景会退化**【**代码推演，未实测**——本次实验池里只有 1 条出口】：只要池里还剩一条没冷却的路由，它就会**继续逐条烧完整池**才失败（最多 20 次 quota failover），而 `FreeUsageLimitError` 本质是全局桶，这 20 次全部注定无效——正是 `upstreamQuota.js:8-11` 注释自己承诺要避免的事。（同 host 兄弟端口会因 `egress.js:1450-1457` 被一起冷却，所以"共享 IP 型"池子退化较慢；跨 ISP 的池子则会真烧。）
3. `egress.js:1407-1418 recordSuccess` 会把该出口的 `retryAfterUntil` **整体清零**（成功一次即解除其全部额度冷却）⇒ 在"按 IP 计的日额度"语境下，这条清零逻辑让额度冷却容易被一次成功意外洗掉。

⇒ **结论：这一层要抄的是"设计"（按 model 记全局额度、只延长不缩短、body 含 `FreeUsageLimitError` 才记账），而不是"现状"；照搬现状等于搬一套空转的账本。**

**层三：会话账本（按 入站 keyId × 出口 routeKey）**
```text
proxy.js:247-249  sessionKey(keyId, routeKey) = keyId（缺省 'anonymous'）+ NUL + routeKey
proxy.js:251-294  persistentSessions：5 分钟 或 10 次请求 到限即重造（:257）
proxy.js:283-293  rotate(keyId, routeKey) —— quota 失败后主动换会话
proxy.js:296      egress.onRouteReset ⇒ invalidateRoute(routeId)（出口被重置则该出口会话全废）
egress.js:276-290 resetRoute 删除 health 并广播监听者
```

### 2.7 选路算法（延迟分层 + 轮询）
```text
egress.js:1126-1144 selectRoute：实测 latency 升序 ⇒ 只在前 12 条里 routeCursor 轮转
                    注释 :1122-1125："one endpoint does not absorb every request (and every quota hit)"
egress.js:1146-1181 getNextRoutableRoute(attempted)：排除已试 ⇒ verified 前 4 名随机取一个
                    :1164-1178 verified 全试完 ⇒ 退到"未探测但可用"的前 4 名随机
                    :1179-1180 最后才 direct
egress.js:1108-1120 getRoutableRoutes：proxyChecks 里 healthy===true 者优先；一个都没验过则返回全池
egress.js:741-793   checkProxy：每条代理 5 次探测全过才算 healthy，单次 5 s 超时
egress.js:634-660   runProxyChecks：并发 5 的工作线程扫全池，扫完即 _pruneDeadProxies
egress.js:670-705   连续 2 次 5x 全败 ⇒ 摘除；manual 全灭时保留最后 1 条以便恢复；auto 可清零等补货
```

---

## Q3 会话亲和 + 三个指定提交

### 3.1 会话亲和实现在哪、解决什么
```text
身份头铸造    identity.js:21 generateCanonicalSessionId —— 逆向官方 CLI v2.0.32 的
              Identifier.descending()：(ts*0x1000+seq) 取反 ⇒ 48bit hex + 14 位 Base62，
              形如 ses_<12hex><14base62>；identity.js:86 generateProjectId（sha256→40hex）
上游头装配    identity.js:95-118 buildUpstreamHeaders：
              User-Agent: opencode/latest/<client_version>/cli、x-opencode-client: cli、
              x-opencode-version、x-opencode-session、x-opencode-project
会话缓存      proxy.js:242-296 persistentSessions（§2.6 层三）
```
**解决两件事**：
1. **反爬伪装**——上游按 CLI 客户端身份放行。`proxy.js:450-454`：官方闸门要求 system 消息含 CLI 的 `<env>` 块，缺失就用 `upstreamContext.ensureUpstreamEnv` 补上。**这条我们已在生产验证有效**（否则直接 403/fatal）。
2. **prompt cache 命中**——同一 (入站key, 出口) 稳定复用同一 session/project。注释 `proxy.js:243-245`："leverage OpenCode's session affinity and prompt caching. Only rotated on quota/route failure."

⚠️ **两条谨慎证据**：
- 提交 `0181e06`（v3.2.1，10-31）标题含 **"strip session affinity headers"**——这机制**曾被删过**，后来又加回来。作者自己未定论。
- §实验记录 A/B：实机日志每次都是 `(session: global)`。已坐实原因：
  ```text
  BUN_BE_BUN=1 opencode run <probe.js> → BUN=1.4.2  FetchSession=undefined
  ```
  即 CLI v2.0.32 内嵌的 **Bun 1.4.2 根本没有 `Bun.FetchSession` 这个 API** ⇒ `bunProxy.js` 的 `typeof Bun.FetchSession === 'function'` 判断为假，**静默退回全局 `fetch`**（连它自己 `b55c939` 加的 fallback 错误日志都不会打印）。v3.2.2/v3.2.4 宣传的"隔离连接池、零连接复用、强制新出口 IP"在真实运行中**完全没兑现**。
  ⇒ 这是一条**可直接抄的反面教训**：一个"只在某个新 runtime 版本才存在的 API"被当成分支条件，且降级路径**无任何告警**——与 §3.3 "禁止静默降级"的原则自相矛盾。若我们做类似判断，降级必须**打 warning 并计入指标**。
  （本次结论不受影响：出口是 7890，出口 IP 由 7890 自己轮换，不依赖 FetchSession 隔离。）
⇒ 抄之前必须自测"稳定 session 是否真提高命中/降低 429"，别当既成事实。

### 3.2 `b9b81dc` fix(proxy): eliminate TDZ ReferenceError that emptied all streaming responses
（2026-11-03，tianzhuzhu，`server/proxy.js` +10/−4）

**修了什么**：`attachStreamWatchdog()` 在声明之前读了 `failStream`，而 `failStream` 原本是块内 `const` 箭头函数 ⇒ 命中 TDZ ⇒ `ReferenceError: Cannot access 'failStream' before initialization`。

**为什么严重**（提交正文原话）：抛错发生在 **200 + SSE 头已经写出之后**，`server/index.js` 的全局 `uncaughtException` 把它吞了 ⇒ **客户端收到 HTTP 200、content-type 是 text/event-stream、body 零字节**。所有流式请求静默失败，非流式完全正常。

改动即把 `const failStream = (err) => {...}` 改成 `function failStream(err) {...}`（函数声明提升），两处：`proxy.js:759`（/v1/chat/completions）、`proxy.js:1736`（/v1/messages）。

**对我们的价值（与通道无关，但是直接可抄的教训）**：我们日志里 200/499 并存；**"200 + 空 SSE"是最难发现的一类故障**——状态码健康、指标全绿。落点：SSE 转发路径里 **watchdog/回调必须在被引用的绑定初始化之后注册**；`uncaughtException` 兜底不得掩盖请求级错误；流式响应要显式校验"首包是否写出"，零字节 200 判失败并计入指标。

### 3.3 `b55c939` fix(bun): pass proxy to Bun.FetchSession constructor…（v3.2.4，11-01）
（11 文件 +364/−149，实质在 `server/bunProxy.js`、`server/bunBridge.js`）

1. **代理根本没生效 / 静默漏本机 IP**。原代码 `new Bun.FetchSession()` 建的隔离会话**没把 proxy 传进构造器**，proxy 只挂在 fetch 选项上 ⇒ 隔离连接池仍可能走宿主直连。
   现：`session = new Bun.FetchSession({ proxy: { url: proxy, respectNoProxy: false } })`，失败退 `{ proxy }`，全失败才退全局 `fetch`；并新增日志 `Outbound ... via <maskedProxy> (session: isolated|global)`、`Upstream response: <status> via <maskedProxy>`；`bunBridge.js:453+` 把子进程 stdout/stderr 逐行转发到宿主日志（`[BunProxy]` / `[BunProxy:err]`）。
2. **SOCKS5 不被 Bun 支持时必须拒绝，不能放行**：
   ```text
   if (rawProxyHeader && !proxy) return 502 { type:'proxy_transport_error',
     message:'Proxy protocol not supported by Bun TLS bridge. Use HTTP or HTTPS proxies.' }
   ```
   注释原话：**"instead of silently leaking out the VPS host IP!"**

**对我们的价值**：我们 E2 证明直连必被地区拒绝。这类 bug 表现为"配了代理但流量从本机 IP 出去"，症状是**间歇性 403 region error**而非明确报错。任何出口适配层都应：① 每请求日志"实际用了哪个出口"，② 无法应用指定出口时**硬失败**，禁止静默降级为直连。**（§实验记录 D 证明：它自己的日志做到了这点，而我们生产没做——否则 WARP 空池早该暴露。）**

### 3.4 `415ce0f` feat(core): v3.2.3 …（10-31，16 文件 +351/−199）
1. **strict model routing**：`smart_fallback` 从 `!== false`（默认开）改成 **`=== true`（默认关）**（`proxy.js:422,1149,1386,2202`），且换模型前先要求还有可用出口 `nextFallbackRoute`。CHANGELOG：消除"8 个模型连环 failover 卡 40 秒"。→ **默认不偷换用户指定的模型**。
2. **direct quota cooldown protection**：新增 `canUseDirectFallback()`（`proxy.js:106-126`），所有"兜底走直连"的分支统一改调它；它检查 `egress.getQuotaRetryAfter(direct) === 0`（`:120-122`）——起因是宿主 IP 正在 11.5 小时限流（`Retry-After: 41350s`）却被反复兜底命中。→ **"兜底通道"必须查自己的冷却账本**。这条对我们**一模一样适用**：`freetier_warp_fallback.go` 的兜底不该只看配置开关，要看出口账本。
3. **universal proxy parsing**：`normalizeProxyString`（`egress.js:63-89`）支持 `host:port:user:pass`（Webshare/Smartproxy/IPRoyal/BrightData 格式）；`parseProxyUrl` 先过它。加 `-sessid-`/`__sessid__` 令牌识别；`applyRotatingSession(user, mode, forceRotate)`（`egress.js:34-62`）在 auto+forceRotate 时**强制注入新 `-session-<rand>`** ⇒ 逼网关发新 exit IP；`proxy.js:616,1571` 传 `quotaFailovers > 0` ⇒ **429 之后的重试一定换出口 IP**。
4. **shared IP pool optimization**：`egress.js:1450-1457` 同 host 兄弟端口共享 quota 冷却；`proxyDiscovery.js` 自动发现加**每 host 最多 1 条**唯一性约束。→ 承认**上游按 IP 计限流**，不按连接/端口。

---

## Q4 「所有通道都挂」时的兜底

### 有什么
| 兜底手段 | 有无 | 代码/实测 |
|---------|------|----------|
| **如实回报 + 可退避** | ✅ **最强项** | `proxy.js:1166-1179` 429 + `Retry-After`=真实值；实测 `Retry-After: 79907`（≈22.2 h，即 UTC 午夜重置）；`proxy.js:494-497` 无出口 ⇒ `503 no_egress_route` + `Retry-After: 5`，**1~7 ms 内返回** |
| **就绪探针分离** | ✅ | `index.js:231-242` `/healthz` 恒 200（进程活着）；`index.js:245-270` `/readyz` 无出口 ⇒ **503**，且带 `egress.ready_routes[]`、`proxies_configured/automatic/manual`、`allow_direct`、`upstream_quota`、`queue`、`bun_bridge` 全量诊断 |
| **请求排队** | ✅ | `requestQueue.js` 全局 8 / 每 key 4 / 队列 100 / 等待 15 s；满 ⇒ `429 queue_full`（`proxy.js:567-581`）；注释明确"admission control, **not** a rate-limit bypass"。槽位在**整个流期间**持有（`proxy.js:715 upstreamRes.once('close', releaseSlot)`），失败路径每处都 release（`:1078,1667,2132`） |
| **探测后摘除 + 自动补货** | ✅ | `egress.js:741-793` 5 次探测全过才 healthy；`egress.js:670-705` 连续 2 次全败 ⇒ 摘除 + 面板告警通知；`proxyDiscovery` 后台持续补货 |
| **运维手动干预** | ✅ **实测可用** | `POST /api/proxies/clear-quota`（清额度冷却，`adminRoutes.js:458-469`）、`/api/proxies/recheck`（立刻重扫）、`/api/proxies/prune`、`/api/proxies/scrape-now`；`GET /api/proxies/status` 逐条列出 `state/quotaRetryAfterMs/openUntil/checkFailures/lastError`（实测输出见 §实验记录 E） |
| **换模型兜底** | ⚠️ 有但默认关 | `discovery.js:431-472` 硬编码优先级 `big-pickle→mimo→nemotron→beam→flint→glm→deepseek→space-bunny`，逐个跳过 `isBlocked` 的；`smart_fallback` 默认 `false` |
| **多厂商上游** | ❌ **没有** | 上游只有 `opencode.ai` 一个域：`config.js:20 DEFAULT_UPSTREAM_INFERENCE_URL`、`:123-124`（zen models / metadata）、`bunProxy.js:14 UPSTREAM_ORIGIN`、`bunBridge.js:25 OFFICIAL_HOST`。**没有第二家 LLM 供应商** |
| **付费/备用 key 兜底** | ❌ **没有** | 只有单个 `OPENCODE_ZEN_KEY`（`config.js:116`）；`proxy.js:405-415` 请求付费模型但没 key ⇒ **直接 401 `zen_key_required`**。不会自动切 key、没有 key 池 |
| **自动降级到直连** | ⚠️ 有但被冷却门控 | `canUseDirectFallback()`；生产 compose 显式 `ALLOW_DIRECT_FALLBACK=false`（CHANGELOG："instead of silently leaking the host IP"）——**对我们是必须的**（E2 已证直连必 403） |
| **外发告警（webhook/IM）** | ❌ 没有 | `grep webhook\|telegram\|discord\|slack\|smtp` 在 `server/` **零命中**；只有 `notifications.js` 写 `data/notifications.json` 给面板看，上限 100 条 |

### 结论：它的兜底哲学是「**不掩盖，快失败**」
它**不试图**在全部通道挂掉时"再抢救一下"（没有第二厂商、没有 key 池、不偷换模型），而是把**"为什么挂、什么时候好"精确交给客户端和运维**：真实 `Retry-After` + 结构化 `/readyz` + 逐出口账本可视化 + 一个手动清冷却按钮。

**这恰好是我们方案的反面**：生产 503 的响应体写着 `the upstream was not contacted`，却仍花 26 秒、返回不带恢复时间的 503、且把"WARP 空池"和"额度耗尽"混成同一个错误。

---

## Q5 运行要求（**已实机验证**）

### 静态事实
```text
package.json  name/version = opencode2api 3.2.4；main = server/index.js
              scripts.start = "node server/index.js"
              ⚠️ 无 dependencies / devDependencies 字段 ⇒ 服务端【零 npm 依赖】，纯 node 内置模块
              engines 未声明（README 徽章写 Node.js 20+）
Dockerfile    基础镜像 node:20-bookworm-slim；两阶段（client React 19 面板 → dist）
              关键行 23-28：
                # The official inference gate rejects Node's TLS fingerprint. The gateway
                # reuses the Bun runtime embedded in the OpenCode CLI ...
                RUN npm install -g "@opencode/cli@2.1.4"
              EXPOSE 8080；HEALTHCHECK 用 /healthz（注释：image must stay healthy even when all proxies are unavailable）
entrypoint.sh 仅 mkdir -p /app/data && exec "$@"
.env.example  只需 3 项：PORT=8080 / DASHBOARD_USER=admin / DASHBOARD_PASS=（compose 必需，
              <12 字符或 admin123 等已知默认会被忽略并随机生成，写入 data/admin-password.txt）
              另可选 ROTATING_PROXY_*
README.md     Prereq: Node v20+；npm install -g @opencode/cli@2.1.4；
              启动 $env:DASHBOARD_PASS="762920"; npm start；面板 http://127.0.0.1:8080/；API base /v1
默认端口      8080（config.js:85）；管理面板同端口；上游 Bearer 见 identity.js:111-115
```

### 运行拓扑（两个进程）
```text
node server/index.js  :40190          ← 网关（Node）
   └─ spawn opencode.exe run server/bunProxy.js  :127.0.0.1:<随机>   ← Bun 桥
      env: BUN_BE_BUN=1, BUN_BRIDGE_PORT/TOKEN/PARENT_PID/UPSTREAM, OPENCODE_DISABLE_AUTOUPDATE=1
      并【删除继承的代理环境变量】(bunBridge.js:429-431)：注释——inherited proxy env
      "would silently add a second, unaccounted hop"（值得抄）
```
- **Bun 不是直接依赖**，而是**藏在 `@opencode/cli` 里的单文件二进制**（本机 `opencode.exe`，213 MB）。
- 桥只在上游是官方域时必需：`bunBridge.js:232-235 isRequired()` = `isOfficialUpstream(upstream_inference_url)`；自定义上游不启动 Bun。
- CLI 探测路径 `bunBridge.js:120-158`（Windows 扫 `%APPDATA%\npm\...`、PATH 等）——**本机装在 `D:\Programs\nodejs\` 不在其扫描表内**，须显式 `BUN_BRIDGE_CLI_PATH`（该仓库 `.env` 里前人已留同样的注释与解法）。
- `updater.js:50-94`：只 `config.update({client_version})` + 重新发现模型，**不安装包、不执行外部命令**（安全）。

### 实机验证：**跑得起来，30 秒内**
本机已有 node v22.23.1 + `@opencode/cli` ⇒ 零 `npm install`。复制到 `%TEMP%\o2p-research\run`（**不动原仓库**，`data/` 与 `.env` 都落在临时目录），`node server/index.js` 即起。完整记录见下一节 §实验记录。

### 运行版本对照（两个值得记的错位）
```text
本机 CLI 实际版本   opencode v2.0.32（内嵌 Bun 1.4.2）
仓库 pin 的版本     Dockerfile:26 OPENCODE_CLI_VERSION=2.1.4；config.js:88 client_version 默认 '2.1.4'
运行时 updater 还把它改成 2.2.7（README 徽章/pin 都落后于上游发布）
```
⇒ 尽管 `x-opencode-version` 头（2.1.4→2.2.7）与实际二进制（2.0.32）**长期不一致**，§实验记录 B 仍然 7/7 成功 ⇒ 说明**上游对这个版本号头目前不敏感**，真正的门槛是 Bun 的 TLS 指纹 + `<env>` 系统消息块（`proxy.js:450-454`）。
⚠️ 反向风险：`updater.js` 会**自动把版本号头升到最新**（无需确认），一旦上游开始校验"版本头必须与真实 CLI 匹配"，这类"只改头不改二进制"的做法会突然全线失败。我们 Go 侧若要抄，**版本号必须与实际指纹同源**，别学它分两处。

---

## 实验记录（实机，全部只读、已清理）

> 环境：本机 node v22.23.1；出口复用 7890（Clash）；`@opencode/cli` 走 `BUN_BRIDGE_CLI_PATH`。
> 所有实例：`EGRESS_MODE=proxy_only`、`AUTO_PROXY_DISCOVERY=false`（不去爬公网列表，避免污染与耗时）、`ALLOW_DIRECT_FALLBACK` 按需。
> 未配置任何上游 zen key ⇒ 全程 `Authorization: Bearer public`（匿名免费层）。

### A. 首次运行（40190，`PROXIES=http://127.0.0.1:7890`，默认 `proxy_attempt_timeout_ms=5000`）
`/readyz` → `status:ready`，`egress.mode:proxy_only`，`ready_routes:[{id:"http://127.0.0.1:7890",type:"proxy"}]`，`bun_bridge:{required:true,ready:true,cli_path:"...@opencode\cli\bin\opencode.exe"}`，**响应里没有任何 warp 字段**。
首个请求 5 秒后被 Node 侧判失败：
```text
[BunProxy] [bun-bridge] Outbound POST /inference/openai/v1/chat/completions via http://127.0.0.1:7890 (session: global)
[BunProxy] [bun-bridge] Upstream response: 200 OK via http://127.0.0.1:7890
[Proxy] Proxy transport failure on proxy:http://127.0.0.1:7890 for beam: Proxy attempt timed out (1/10)
→ 客户端 HTTP 504（5 080 ms）
```
⇒ **上游 200，网关自己超时**。定性见 §2.2 ⚠️。

### B. 放宽 attempt 超时后：**7/7 全成功**（同 40190）
`data/config.json` 设 `proxy_attempt_timeout_ms=45000`，重启。`/v1/models` → 11 个模型（`big-pickle, mimo-v2.6-flash, deepseek-v4.1-flash, glm-5.3-flash, longcat-2.5, iceman, inkling-2-small, beam, flint-40b, nemotron-3.5-super-vl, nemotron-3.5-lightning`）。
```text
nonstream#1 {"status":200,"totalMs":648,"firstByteMs":647,"bytes":906,"content":"ok 1"}
nonstream#2 {"status":200,"totalMs":603,"firstByteMs":603,"bytes":906,"content":"ok 2"}
nonstream#3 {"status":200,"totalMs":536,"firstByteMs":536,"bytes":908,"content":"ok 3"}
nonstream#4 {"status":200,"totalMs":628,"firstByteMs":628,"bytes":907,"content":"ok 4"}
nonstream#5 {"status":200,"totalMs":786,"firstByteMs":786,"bytes":899,"content":"ok 5"}
stream#1    {"status":200,"totalMs":600,"firstByteMs":476,"bytes":2095,"sseLines":9}
stream#2    {"status":200,"totalMs":714,"firstByteMs":586,"bytes":2090,"sseLines":9}
```
⇒ **无 key、无 WARP、无爬虫池，只靠「本机 residential IP + 一个海外 HTTP 代理」= 非流式 5/5 + SSE 2/2，536~786 ms**。与我们 E1（10 个 zen key + 7890 代理 5/5）**同等成功率的另一半**：匿名层也能直接吃 7890。

### C. 地区拒绝对照（40191，`EGRESS_MODE=direct`）
```text
req1 {"status":502,"ms":8398,"err":"upstream_unavailable | All configured egress routes failed:
      {\"error\":{\"type\":\"RegionError\",\"message\":\"This model is not available in your country\"}}"}
req2 {"status":502,"ms":1051,...}   req3 {"status":502,"ms":334,...}
日志：[Proxy] Upstream 403 on direct for beam: {...RegionError...} (1/3)(2/3)(3/3)
```
⇒ 复现我们 E2 的 403，但暴露两个**它的**问题：① 只有一条地区受限出口时，`classifyUpstreamFailure` 的 `retry` 分类让它把 3 次重试全砸在同一出口（`egress.js:184` 的假设在"池里只剩 direct"时不成立）；② **403 语义被改写成 502**（`proxy.js:1242-1246`），运维看不出是地区问题还是上游故障。**我们抄时应当：地区错只重试 N 个"不同出口"，出口用尽即停，并保留 403。**

### D. 匿名额度烧穿 + 快速失败（40193，单出口 7890）
```text
[BunProxy] Upstream response: 429 Too Many Requests via http://127.0.0.1:7890
[Proxy] Upstream 429 error body for beam (proxy:http://127.0.0.1:7890):
        {"error":{"type":"FreeUsageLimitError","message":"Free usage limit reached...subscribe to OpenCode Go..."}}
[Proxy] Upstream quota for beam; all routable routes and models exhausted (Retry-After 79907s).

req1 {"status":429,"ms":924,"retryAfter":"79907","err":"free_tier_rate_limit | ..."}
req2 {"status":429,"ms":  2,"retryAfter":"79907",...}
req3 {"status":429,"ms":  1,"retryAfter":"79907",...}
/readyz → status:"ready"  upstream_quota:{"blocked":false}      ← 全局闸门恒 false，见 §2.6 层二
日志会话标记：(session: global)                                  ← FetchSession 不存在，见 §3.1
```
⇒ **三条硬结论**：
1. `Retry-After: 79907 s ≈ 22.2 h`（≈ UTC 午夜重置）与 `FreeUsageLimitError` 的每日全局桶一致 ⇒ **今天上游对匿名层已经烧光**，与我们生产的 503 同一天、同一根因方向。**换 IP 救不了它**——它自己 B 段成功、D 段被拒，同一出口，差别只在累计量。
2. 第 2 个请求起，**同一出口被路由级冷却（`egress.js:1444`）排除 ⇒ 1~2 ms 快速失败**、`Retry-After` 原样透传。**这正是我们 503 该变成的样子**（我们现在是 26 秒 + 无恢复时间）。
3. `/readyz` 仍 `ready`（"route stays in /readyz"），但 `upstream_quota.blocked=false` **是假绿**——该账本无写入点（§2.6 层二）。

### E. 空池 / 死出口对照（40192，`PROXIES=` 两个不存在的端口，`ALLOW_DIRECT_FALLBACK=false`）
```text
/readyz → HTTP 503, status:"not_ready", mode:"proxy_only", routes:[]
req1 {"status":503,"ms":7,"retryAfter":"5","err":"no_egress_route | No egress route is available. Configure
       PROXIES and wait for a healthy proxy, or set ALLOW_DIRECT_FALLBACK=true for local development."}
req2 {"status":503,"ms":1,...}   req3 {"status":503,"ms":1,...}
```
`GET /api/proxies/status`（登录 token 后）逐出口账本：
```text
{"id":"http://127.0.0.1:59911","source":"manual","available":false,"routable":false,
 "state":"failed","checkFailures":1,"checkFailed":true,"openUntil":0,
 "lastError":"Proxy health check failed (0/5): connect ECONNREFUSED 127.0.0.1:59911",
 "passes":0,"checks":5,"latency":11,"healthy":false}
```
⇒ 死出口**不进池、不占请求时间**（7 ms → 1 ms），且 `POST /api/proxies/recheck`、`/api/proxies/clear-quota` 手动干预实测 200 可用。
⇒ **对照我们**：`WARP pool has no ready egress` 是同类场景，但我们**花 26 秒**、不给 `Retry-After`、**没有逐出口账本接口**、也没有"清冷却"按钮。

### F. 关键时间线与**两条不同的故障**（务必别混淆）
各实例日志落盘时刻（同一台机器、同一出口 7890）。
⚠️ **诚实标注精度**：记录 A/B 复用了同名日志文件（`stdout.log`/`stderr.log` 与 `stdout2.log`/`stderr2.log`），且我第一次的 kill 过滤器失效导致两个实例短时间并存，**A/B 的行归属不能 100% 分离**。但结论不依赖这一点：
```text
09:37–10:07  他人实验 E1（前次评估留下的 %TEMP%\oc-research\1，40180）：10 个 zen key + 7890 → 5/5 成功
10:14:41     stdout2.log 末次写入（内含 6 次上游 200 + 第 7 次 429）
10:15:53     stdout.log/stderr.log 末次写入，内含：
             [BunProxy] Upstream response: 429 Too Many Requests via http://127.0.0.1:7890
             [Proxy] Upstream quota for beam; ... exhausted (Retry-After 80385s)
             ⇒ 匿名额度在 10:15 前已被烧穿（B 的 7/7 成功发生在 10:12–10:14，早于它）
10:18:55     记录 C（direct）：RegionError 403
10:19:19     记录 E（死池）：503 no_egress_route
10:23:52     记录 D（匿名，独立可归属）：FreeUsageLimitError 429，Retry-After 79907 s（已不可逆）
```
⇒ **匿名层是在本次调研进行中被烧完的**（10:12–10:14 还能 7/7，10:15 起全部 429）。这不是它的问题，是上游日额度：`Retry-After` 两次分别 80 385 s / 79 907 s，均指向 **UTC 午夜重置**（约北京时间 11-12 08:00）。
⇒ **两件事同时成立，不能合并成一句"上游挂了"**：
1. **我们生产 503 与它的上游 429 不是同一个根因。**
   - 生产 503 = `WARP pool has no ready egress` + 响应体自证 `the upstream was not contacted` ⇒ **纯自伤**（配置/pid 管理 bug，见 `00-current-state-and-tests.md` §2），上游根本没被问过。
   - 它今天的 429 = **匿名层额度真的耗尽**（`FreeUsageLimitError`，Reset≈UTC 午夜，`79907 s`≈22.2 h）。
   ⇒ **修好 WARP 空池 bug，不会让匿名层可用**（今天 10:23 之后匿名层已 429）；
   ⇒ 反之，**匿名层 429 也不解释我们那 921 条 503**。两条修复各自独立，**先修 WARP bug（能立刻把流量导回 10 个 zen key，E1 已证明 key 路径此刻仍通）**。
2. **同一出口的匿名桶是被共享烧掉的**：本机同时有多个实验实例（40180–40186 + 40190–40193）都在打同一个上游出口。**这是可复现的资源竞争**，也解释了为什么记录 B（10:14）成功、记录 D（10:23）失败。
   ⇒ 对我们方案的直接含义：**若继续走匿名层，多个进程/多个出口共享同一 IP 桶会互相烧额度**；它代码里 `upstreamQuota` 想解决的就是这个，偏偏那本账没人写（§2.6 层二）。**这条比任何机制都值得我们先落地：把"全局额度耗尽"作为一等状态记进引擎并对外回报 `Retry-After`。**

### 清理与合规核验
```text
killed node 30712 / 59348 / 28116 / 48828 / 35524（本次实验自建实例，命令行匹配 *server/index.js*）
40190 40191 40192 40193 → free
40000 LISTENING pid=56240 OK（生产，未动）
40080 LISTENING pid=40176 OK（生产，未动）
opencode.exe pid=46032（属 Loomy，11-03 创建，父进程 29716）→ 判定为【非我启动】，未触碰
D:\AI_Projects\refs\Opencode2api: git status --porcelain 空；HEAD 仍 b9b81dc；data/* mtime 停在 11-05（未写入）
```

---

## Q6 许可证与维护活跃度

### 许可证
`LICENSE` = **MIT**（Copyright (c) 2026 OpenCode2API Contributors），`package.json:24 "license": "MIT"`。
⇒ **机制可自由移植进我们 Go 仓库**，无传染性义务（仍建议保留出处注释）。
⚠️ 合规提醒：README:5 与文档强调"educational disclaimer"，其行为本质是绕过上游的匿名免费层限制，**法律/TOS 风险不由许可证决定**，与我们现有方案同级。

### 活跃度
```text
git fetch --all --prune  → 成功（exit 0，远程可达）
remote origin            → https://github.com/Nystic-Shadow/Opencode2api
git log HEAD..origin/main → 【空】（不落后）
git log origin/main..HEAD → b9b81dc  ← 注意：本地 HEAD 比 origin/main【多 1 个提交】
  即 TDZ 流式修复目前【只存在于本地】，上游仓库最新仍是 b55c939（v3.2.4, 11-01）
总提交数                   → 114
首末                       → 2026-10-17 → 2026-11-03（约 2.5 周）
作者                       → Shadow-Black-YT 113 / tianzhuzhu 1（= 近乎单人项目）
活跃日                     → 10-17(4) 10-18(7) 10-19(22) 10-23(11) 10-24(23)
                            10-25(16) 10-26(23) 10-31(6) 11-01(1) 11-03(1)
tags                       → v1.0 … v3.2.4（24 个；10-26 v3.0.0，10-31 一天发 v3.1.0→v3.2.3）
```
⇒ **节奏判断**：10-17~10-26 高强度冲刺（一天 20+ 提交），之后断崖（11-01、11-03 各 1 个）。**距调研日已 8 天无提交**；对一个"追着上游改协议跑"的项目，2.5 周生命周期 + 单作者 + 爆发式开发 = **高腐烂风险**。CHANGELOG 里 v2.5.x 的 WARP 方案、v3.1 的 scraper 都被后续版本推翻——**它的代码是"活文档"，但也是"随时会推翻自己"的代码**。
⇒ **不要把它当上游依赖长期跟版**；把机制抄进我们自己的 Go 实现，然后自己维护。

---

## Q7 结论：只抄机制，不整体替换

### 为什么不整体替换（4 条硬理由）
1. **它今天也不行，但"不行"的原因和我们的 503【不是同一个】**（详见 §实验记录 F，此处早期结论已被实测推翻并修正）：
   - 我们的 503 = `WARP pool has no ready egress` + 响应体自证 `the upstream was not contacted` ⇒ **纯自伤**，上游根本没被问到。**这是管理器的 pid/配置 bug**（`00-current-state-and-tests.md` §2）。
   - 它的 429 = **匿名层额度真被烧光**（`FreeUsageLimitError`，`Retry-After` 80 385 s / 79 907 s 两次实测，均指向 UTC 午夜重置）。
   ⇒ **推论一**：修好 WARP 空池 bug **不会**让匿名免费层可用（10:23 之后它自己也是 429）；
   ⇒ **推论二**：但 E1（10 个 zen key + 7890，5/5）与记录 B/C/D（匿名层 429）并列说明 **key 路径此刻仍是通的** ⇒ **修 WARP bug 能立刻恢复我们自己的产能**，这是今晚最该做的事，与是否抄它的机制无关。
   ⇒ **推论三**：换成它这套引擎，**产能反而更低**（它没有 key 池，只能吃 `Bearer public` 这一个已被烧光的共享桶）。
2. **它没有我们已有的东西**：**没有 key 池**（只有 1 个 `OPENCODE_ZEN_KEY`，`config.js:116`）、**没有多厂商**、**没有第二上游**、**没有付费兜底**、**没有外发告警**。我们 10 个 zen key 的多样性是它的超集。
3. **技术栈不同**：JS 双进程（Node 网关 + Bun 桥 + 依赖 `@opencode/cli` 单文件二进制 213 MB），我们是 Go 单二进制。移植成本 ≈ 重写，收益只有机制。
4. **维护风险**：单作者、2.5 周、8 天无提交、方案两次自我推翻（WARP 整块删除 + 同日爬虫反转即证）。

### 最值得抄的 3 个机制（含 Go 落点）

#### ★ 机制 1：三层分离的冷却账本 + **真实 Retry-After 回传**（最高性价比）
**问题对应**：我们 503 的 `WARP pool has no ready egress` 是**把"出口不可用"当成唯一真相**，且**花 26 秒**、不给恢复时间，客户端无法退避。
**它的做法**：出口账本（`egress.js:1420-1483`，按 proxy id / **同 host 共享**）、额度账本（`upstreamQuota.js`，按 model，跨出口）、会话账本（`proxy.js:247-294`，按 key × 出口）**各自独立**；`getEarliestQuotaRetryAfter()`（`egress.js:1046-1054`）算出"池内最早解除时间"直接当 `Retry-After`（`proxy.js:1166`）；池被冷却填满时报 **429 而不是 503**（`proxy.js:469-485`）。实测效果：`1 ms 返回 + Retry-After: 79907`。

**Go 落点**：
- `core/manager/freetier_engine.go`：给 503 分支（当前 `warp_unavailable`）加"**若存在冷却中的出口/key，则改报 429 + Retry-After = 最早解除时间**"；把"WARP 池空"从 503 降级为可退避的 429/503-with-header。同时**把等待时长从 26 s 压到毫秒级**——账本查询不该阻塞。
- `vendors/custom/keypool.go`：把现在的"key 可用性"从布尔升级为 `struct{ openUntil, quotaRetryAfterUntil, host, failures, lastError }`，并在 429 时**按出口 IP/宿主维度共享冷却**（`egress.js:1450-1457` 的思路：同 IP 多 key/多端口一起冷却，避免浪费尝试）。
- `core/router/router.go`：新增 `/readyz`（结构照 `index.js:245-270`：`egress.ready_routes[]` + 计数 + `upstream_quota` + `queue` + 逐账本），与 `/healthz` 分离（"route 冷却 ≠ 部署不健康"）。

#### ★ 机制 2：集中式失败分类 —— 把 **403 RegionError 归为「换出口重试」**，把 **403 FreeUsageLimit 归为「额度」**
**问题对应**：我们 E2 看到直连 403「not available in your country」，但生产里 `403` 只有 12 条、且不区分类型；WARP 空池 921 条 503 里，**有多少其实是地区问题被误判成额度、有多少是额度被误判成出口**——我们不知道，因为没有分类。
**它的做法**：`egress.js:176-198` 单函数返回 `quota|retry|fatal`，两条正则承担全部语义（`:181` 额度型 403 / `:184` 地区型 403）；`classifyUpstreamFailure` 被导出可测（`egress.js:1521,1529`），并在 `test/reliability.js` 里有断言。
**抄时要修掉它的两个 bug**（§实验记录 C）：地区错只应重试**不同出口**，出口用尽即停；最终响应保留 403 语义，不要改写成 502。

**Go 落点**：
- `core/manager/freetier_engine.go`：把散在各处的 `if resp.StatusCode == 403` 收敛成一个 `classifyUpstreamFailure(status, body) → Quota | RetryEgress | Fatal` 纯函数（便于单测），并在日志里**按类别计数**（今天 503 归因困难正是因为缺这一层）。
- `core/manager/freetier_warp_fallback.go`：兜底判断改为消费这个分类，而不是消费"WARP 是否可用"这个单一开关。

#### ★ 机制 3：**兜底通道必须查自己的冷却账本 + 禁止静默降级为直连 + 每请求记录实际出口**
**问题对应**：这是**我们当前故障的准确病灶**。`freetier_warp_fallback.go:165-173` 因为"配置说 WARP 已关"而**拒绝进入降级分支**，而进程实际还在 WARP ⇒ 看门狗与进程互相以对方的配置为前提，形成死锁（见 `00-current-state-and-tests.md` §2）。它的答案是 `canUseDirectFallback()`：不看开关，**看 direct 这个出口自己的账本**（`proxy.js:120-122`），并且 CHANGELOG 把"静默漏宿主 IP"当作必须消灭的缺陷（`503 no_egress_route` instead of silently leaking the host IP；`b55c939` "instead of silently leaking out the VPS host IP!"）。
**它的做法（三件套）**：
- `proxy.js:106-126` 唯一入口判定兜底可用性（配额 + 是否已试过 + 是否允许）；
- `bunBridge.js:429-431` **删除继承的代理环境变量**，注释"inherited proxy env would silently add a second, unaccounted hop"；
- `bunProxy.js`（`b55c939`）每请求打印 `Outbound ... via <maskedProxy> (session: …)` 与 `Upstream response: <status> via <maskedProxy>` —— **出口去向可审计**。

**Go 落点**：
- `core/manager/freetier_warp_fallback.go`：**删掉"配置开关决定是否自愈"这个前提**，改为"探测进程真实出口状态 + 查该兜底通道自己的冷却账本"决定是否走 direct/key-first。这正是 §2 里那个死锁的直接解药。
- `core/manager/freetier_engine.go`：请求级日志加"实际出口 = 哪个 key / 哪个 egress / 是否直连"（对应 `maskedProxy` 那两行），并把"无法应用指定出口"改成**硬失败**而非静默降级。
- `core/router/router.go`：`/readyz` 输出"当前 ready 出口数 / 冷却中数 / 各自 lastError + 解除时间"（对应 `getProxyStatus()` 的逐条明细，实测见 §实验记录 E），并配一个 **`POST /api/clear-quota`** 手动清冷却端点（`adminRoutes.js:458-469`，实测可用）。

### 次优先（值得抄但不是当务之急）
| 机制 | 出处 | 一句话 |
|------|------|--------|
| 排队/准入而非无限重试 | `requestQueue.js`、`proxy.js:567-581` | 全局 8 / 每 key 4 / 队列 100 / 等 15 s，满 ⇒ `429 queue_full`；槽位贯穿整个流；注释明说"不是限速绕过" |
| 延迟分层轮询 | `egress.js:1126-1144` | 只在最快 12 条里轮转，注释："one endpoint does not absorb every request (and every quota hit)" |
| 探测后摘除 + 全灭保留 1 条 | `egress.js:670-705` | 5 次探测全过才 healthy；连续 2 轮全败摘除；**manual 全灭时保留最后 1 条以便恢复**（避免自锁死） |
| 上游 200 但空 body 的自检 | `b9b81dc` 教训 | 流式必须校验"首包已写出"，零字节 200 判失败 |
| attempt 超时的可配置性 | `proxy.js:598-600` + §2.2 ⚠️ | 5 s 对免费层是假故障源；改 45 s 后 7/7 成功 |

### 明确**不要**抄的
1. **公网免费代理爬虫**（`proxyDiscovery.js` 13 个 GitHub/列表源）：质量不可控、把第三方出口 IP 引入生产链路（含 SSRF / 日志泄漏 / 合规面）。**更要紧的是作者自己 10 小时内就自我推翻**：
   ```text
   2f12aa6  10-26 01:15  remove WARP entirely
            CHANGELOG: "No public proxy scraping in the request path:
                        public proxy lists are disabled by default and never used for egress."
   50079d1  10-26 11:34  feat(egress): manage proxies automatically with zero-config discovery
            ⇒ 爬虫当日回归，并默认开启（config.js:106 AUTO_PROXY_DISCOVERY !== 'false'）
   ```
   ⇒ 同一天的两份自述互相矛盾，说明这条路径**连作者都没定论**（`/api/proxies/status` 里 `autoPruneDead:false, autoCount:0` 也显示我们实测时它是关的）。我们已有 7890 这个稳定海外出口，不需要它。
2. **`upstreamQuota` 全局闸门**：设计好，**但现状写入点被 `a35fe1d` 拆除、账本恒空**（§2.6 层二）。抄的是设计（按 model 记全局额度、只延长不缩短、`FreeUsageLimitError` 才记账 + **成功不注销记账**），并自己补上写入调用；照搬现状等于搬一套空转的账本。
3. **硬编码模型优先级换模型**（`discovery.js:441-450`）：把上游模型名写死在源码里，上游改名即失效；且它默认关闭（`smart_fallback=false`）是对的。

---

## 附：它与我们的差异一览表

| 维度 | 我们生产（Go 40080） | Opencode2api（b9b81dc） |
|------|---------------------|------------------------|
| 海外出口 | Cloudflare WARP 匿名池（已烧光） | 显式 HTTP/HTTPS 代理池（**WARP 已整块删除**） |
| 上游凭证 | 10 个 zen key 轮询 | **1 个** key 或 `Bearer public` 匿名 |
| 出口多样性 | 池 = 10（40 cooling） | 池 = ≤150（100 自动 + manual） |
| 全部不可用时 | **26 s → 503**，无恢复时间 | **1~7 ms → 429 + Retry-After 真实值**（或 503 `no_egress_route` + `Retry-After:5`） |
| 失败分类 | 分散、按状态码粗判 | `egress.js:176-198` 单函数三类 |
| 冷却维度 | key + 出口（耦合） | 出口(含同 host 共享) / model / (key,出口) 名义三层分离【model 层**写入点缺失**，见 §2.6 层二】 |
| 可观测性 | `/healthz` 有 warp 计数 | `/healthz` + `/readyz` + 逐出口账本 + clear-quota/recheck 端点 |
| 配置生效 | **热加载通道被自己关死**（`freetier_engine.go:334`） | `config.onChange` → `egress.reload()`（`egress.js:265-273`）+ 环境变量恒胜磁盘 |
| 兜底死锁 | **有**（fallback 看配置、进程看现实） | 无同类设计：兜底看**账本**不看开关 |
| 多厂商/付费兜底 | 无 | 无 |
| 许可 | — | MIT |
