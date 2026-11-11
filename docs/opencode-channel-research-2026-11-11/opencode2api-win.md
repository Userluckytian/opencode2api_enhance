# opencode2api-win 引擎侧「无通道可用」机理调研（只读）

- 仓库：`D:\AI_Projects\refs\opencode2api-win`
- 基线提交：`9964407`（`99644074e00ead27bdbb931a59c9c0e5dac1a245`，2026-11-03 21:31:34 -0800，"baseline: opencode2api Windows/WARP gateway (v1.4.6 + session-affinity 429 fix)"）
- 调研时间：2026-11-11
- 约束遵守：未修改仓库任何文件，未启停进程，未触碰 `%APPDATA%` 运行配置。

> 结论速览：**503「无通道可用」不是 key 或 proxies 的问题，而是 `usesWARP()` 把「免费模型 + Zen key tier」一并锁死在 WARP 出口上；一旦 WARP 池 `no ready egress`，`doKeyUpstream` 直接 `break` 放弃整个 Zen tier，而那条 `warp_proxy_fallback` 日志只存在于匿名（anonymous）lane 的分支里，key lane 根本没有对应的 fallback。**

---

## Q1. `usesWARP()` 的判定逻辑：为什么开了 WARP 后带 key 的免费模型请求也必须走 WARP

判定函数本体 —— `internal/gateway/warp.go:38-40`：

```go
// usesWARP reports whether this request's egress must bypass proxies. Only
// free models on the anonymous lane and the Zen key tier qualify; the Go plan
// and paid models keep their proxy bindings.
func (g *Gateway) usesWARP(model string, tier config.Tier, anonymous bool) bool {
	return g.warp != nil && g.catalog.IsFreeModel(model) && (anonymous || tier == config.TierZen)
}
```

三个条件同时成立即为 true：

1. `g.warp != nil` —— 配置里启用了 WARP（池对象存在）。
2. `g.catalog.IsFreeModel(model)` —— 请求的模型是**免费模型**（判定与 lane/key 无关，只看 model id）。
3. `anonymous || tier == config.TierZen` —— **匿名 lane 或 Zen key tier 都命中**。

关键点：第 3 条里的 `tier == config.TierZen` **不区分 anonymous**。也就是说，只要「WARP 开着 + 免费模型 + Zen tier」，无论请求带不带 zen key，egress 都被强制为 WARP，`proxy` 绑定被完全绕过。注释本身说得很明白：`must bypass proxies`。

调用点（key lane 的两处，anonymous lane 一处，probe 一处）：

| 位置 | 场景 | 行为 |
|---|---|---|
| `internal/gateway/upstream.go:952` | `doKeyUpstream`（正式 key 轮换主循环） | `useWARP := g.usesWARP(route.ID, route.Tier, false)` —— **带 key 也走 WARP** |
| `internal/gateway/upstream.go:871` | `doSelectedKeyUpstream`（Playground 单 key 诊断） | 同上，`anonymous=false` 仍可能 true |
| `internal/gateway/upstream.go:351` | `doAnonymousUpstream` | `g.usesWARP(route.ID, config.TierZen, true)` |
| `internal/gateway/probe.go:152` | 探活 | `g.usesWARP(model, config.TierZen, false)` |

命中后的实际后果，`doKeyUpstream` 内部（`upstream.go:982-1004`）：

- `upstream.go:982-990`：**只有 `!useWARP` 时**才检查 / 使用 `proxy` 绑定（`if proxy == nil { lastErr = errors.New("upstream key has no proxy binding"); break }`）。`useWARP==true` 时这段整体跳过 —— 即 key 上配置的 proxy 完全不参与。
- `upstream.go:992-996`：`useWARP` 时 `setRequestCredential(..., nil)` —— 遥测里 proxy 字段被清空。
- `upstream.go:1000-1001`：`useWARP` 时改走 `g.doWARPAttempt(...)`，而非 `doInferenceAttempt(g.inferenceClient(proxy, ...))`。

而 `doWARPAttempt` 第一步就是取出口（`warp.go:230-236`）：

```go
lease, err := g.warp.AcquireFor(ctx, affinity)
if err != nil {
	if ctx.Err() != nil { return nil, "", ctx.Err() }
	return nil, "", fmt.Errorf("%w: %v", errWARPUnavailable, err)   // warp.go:235
}
```

池子没有 ready egress → 这里直接产出 `errWARPUnavailable`（外层日志文案 `WARP egress is unavailable: WARP pool has no ready egress` = `warp.go:28` 的 `errWARPUnavailable` + `warp.go:235` 的 `%v` 包装），**请求根本没发到上游**。

> 补充：`warpUsesBun`（`warp.go:45-48`）说明走 WARP 的免费流量还要伪装官方 Bun 运行时指纹，进一步印证「免费模型 + WARP」是一条独立且强制的传输通路。

---

## Q2. `errWARPUnavailable` 的处理：为什么它直接 `break` 放弃整个 key tier，而不回退到 proxies/直连

控制流在 `doKeyUpstream` —— `internal/gateway/upstream.go:1041-1045`：

```go
lastResponse = resp
if useWARP && errors.Is(err, errWARPUnavailable) {
	// No exit at all: another key would only wait on the same pool.
	lastErr = err
	break
}
```

逐条拆解这段为什么这样写、以及它的代价：

1. **`break` 跳出的是 key 轮换循环**（`upstream.go:953 for attempts < g.cfg.Retry.MaxAttempts`）。设计意图见注释：同一个 `g.warp` 池对象对所有 key 共享，「换一个 key 也只是在同一个空池上再等一次」，所以不做无谓的重试。这在「省时间」意义上正确，但代价是**一次 pool 空 = 整个 Zen tier 瞬间归零**，10 个 key 一个都没被真正使用过（日志里 503 均匀分布在全部 10 个 key 上，正是每个请求各自走到第一次 `errWARPUnavailable` 就 break 的分布特征，而不是 key 被轮换坏了）。
2. **`lastResponse` 此处为 `nil`**（`doWARPAttempt` 取不到出口时返回 `nil, label, err`，`warp.go:235` / `warp.go:240`），所以循环结束后走 `upstream.go:1057-1060`：
   ```go
   if lastResponse != nil { return lastResponse, nil, attempts }
   return nil, lastErr, attempts          // lastErr = errWARPUnavailable
   ```
   即向上返回**错误**，而不是一个可转发的 HTTP 响应。
3. **回到 `doUpstreamTiers`**（`upstream.go:276-312`）：对 Zen tier 而言 `resp==nil && err!=nil` → `lastErr = err`（`upstream.go:309-311`），继续尝试 `keyTiers` 里剩下的 tier。若 `route.KeyTiers` 只有 Zen（免费模型典型情况），循环结束，落到 `upstream.go:322-328`：
   ```go
   if lastResponse != nil { ... }
   if lastErr == nil { lastErr = errors.New("no usable upstream route") }
   return nil, effectiveRoute, attempts, lastErr
   ```
   `errWARPUnavailable` 原样冒泡。
4. **`gateway.go` 把它翻译成 503** —— `internal/gateway/gateway.go:365-368`：
   ```go
   if errors.Is(err, errWARPUnavailable) {
   	wire.WriteError(w, external, http.StatusServiceUnavailable, warpUnavailableMessage, "warp_unavailable", ids.Request)
   	return
   }
   ```
   消息本体 `warp.go:33`（`no WARP exit could carry this free-model request; the upstream was not contacted...`），遥测里 `errWARPUnavailable` 的第二处判定点在 `gateway.go:575`（同一族语义）。
5. **没有任何 key→proxy 的降级分支**：`doKeyUpstream` 里唯一使用 `proxy` 的分支被 `if !useWARP` 门控（`upstream.go:982`）；`useWARP` 分支（`upstream.go:1000-1001`）只可能进 WARP。`warp_proxy_fallback`（`upstream.go:364`）**只出现在 `doAnonymousUpstream`**（`upstream.go:350-367`），key lane 完全不调用它。
6. **旁证：`observeKeyResultWARP`（`warp.go:481-495`）刻意不因此冷却 key** —— `if err != nil && resp == nil { return }`，注释写明「the upstream never judged this key」。所以 key 状态看起来永远是「健康」的，监控不会报警，但请求全部 503 —— 这正是本次故障「503 均匀分布在全部 zen key、key 层无异常」的代码解释。
7. **probe 路径同样短路**：`internal/gateway/probe.go:249`（`if overWARP && errors.Is(err, errWARPUnavailable)`），探活也被同一个空池挡住，于是 `/healthz` 只会显示 `warp.active=1, cooling=40` 而 key 探活给不出可用信号。

> 一句话根因：**`errWARPUnavailable` 是「egress 层」错误，但引擎把它当作「整个 tier 不可用」来终结；对 key lane 而言 egress 层没有第二选项（proxy 被 `useWARP` 门控排除），所以 WARP 空 = 免费模型 key 请求 100% 503。**

---

## Q3. `WARP free lane has no usable exit; falling back to a configured proxy`（`upstream.go:364`）只在什么分支触发

日志行位置：`internal/gateway/upstream.go:364`。它所在函数 `doAnonymousUpstream`（`upstream.go:350-367`）：

```go
func (g *Gateway) doAnonymousUpstream(...) (*http.Response, error, int) {
	if !g.usesWARP(route.ID, config.TierZen, true) {
		return g.doAnonymousProxyUpstream(...)          // 351-353：WARP 关 → 纯 proxy
	}
	resp, err, attempts := g.doWarpAnonymousUpstream(...)  // 354：WARP 开 → 先走 WARP
	if !g.warpProxyFallbackAllowed(ctx, resp, err) {
		return resp, err, attempts                        // 355-357：不允许 → 原样返回（可能 503）
	}
	if resp != nil { httpx.DrainAndClose(resp.Body) }
	g.logger.Warn("WARP free lane has no usable exit; falling back to a configured proxy", ...)  // 364
	proxyResp, proxyErr, proxyAttempts := g.doAnonymousProxyUpstream(...)  // 365
	return proxyResp, proxyErr, attempts + proxyAttempts
}
```

**触发条件（全部满足）**：

1. 该请求走的是**匿名 lane**，即 `route.Anonymous == true`（由 `doUpstreamTiers` 的 `upstream.go:232` 分支进入）。带 key 的请求（`keyRoute.Anonymous = false`，`upstream.go:283`）**永远不会**经过这个函数。
2. `usesWARP(...)` 为 true，即 WARP 已启用 + 免费模型（匿名 lane 的 tier 硬编码 `config.TierZen`）。
3. `doWarpAnonymousUpstream` 返回后，`warpProxyFallbackAllowed` 为 true（`upstream.go:372-386`）：
   - `ctx.Err() == nil` 且 `!errors.Is(err, errDeterministicClientRejection)`（373-375）；
   - `g.anonymous.Len() != 0` —— **必须配置了至少一个可用 proxy**，且匿名 lane 断路器未开 `!g.anonymous.Open()`（376-380）；
   - `err != nil` 直接允许（381-383）；否则要求状态码是 403 / 429 / 5xx（384-385）。

`errWARPUnavailable` 属于 `err != nil`，所以在匿名 lane 上，**WARP 空池确实会自动 fallback 到 proxies** —— 但仅限匿名。

**能不能通过配置让「带 key 的免费模型请求」也走这条 fallback？答案是不能（不改代码的前提下）**：

- `doKeyUpstream`（`upstream.go:929-1061`）里没有任何对 `doAnonymousProxyUpstream` 或 proxy 的调用；`useWARP` 分支（`upstream.go:1000-1001`）唯一出口就是 `doWARPAttempt`。
- 想让 key 请求用 proxy，只能让 `usesWARP()` 返回 false，即三条件里断掉一个（`warp.go:39`）：
  - `g.warp == nil`：**关掉 WARP**（`warp.enabled=false`）—— 唯一现成、可配置的路径；
  - `!IsFreeModel(model)`：换非免费模型（不适用）；
  - `tier != TierZen`：让免费模型只走 Go tier / 其它 tier（取决于 `route.KeyTiers` 与配置是否允许，实测成本高于前者）。
- 另一个「间接可达」的口子：`route.KeyTiers` 顺序（`upstream.go:272-276`）+ `blocked` 跳过 Zen tier 的逻辑（`upstream.go:277-280`）——若匿名阶段先命中 `errFreeTierIPBlocked`，Zen key tier 会被整体跳过、只剩 Go tier，但这不是「fallback 到 proxy」，而是「绕开免费模型」，且会把免费额度彻底放弃。

> 因此运维侧唯一稳妥姿势：**要么保证 WARP 池始终有 ready egress，要么 `warp.enabled=false` 让 key 请求回到 proxy/direct 通道**（已实测 `warp.enabled=false + proxies=["http://127.0.0.1:7890"]` → 5/5 成功；`proxies=["direct"]` → 403 地域拒绝，说明回到 proxy 通道后上游判断的是出口 IP，与 key 无关）。

---

## Q4. WARP 出口冷却账本：时长、/64 duplicate 判定、403 轮换策略、为什么会「一个坏出口烧掉整池」

### 4.1 冷却时长常量表（`internal/warp/pool.go`）

| 常量 | 值 | 行号 | 用途 |
|---|---|---|---|
| `egressCooldown` | **24h** | `pool.go:29` | quota 型惩罚的固定黑名单时长（一个 /64 被上游拒绝 → 写死 24h） |
| `maxEgressCooldown` | **24h** | `pool.go:34` | Retry-After 的上限，`quotaCooldownFor` 取 `max(24h, Retry-After)` 再被封顶回 24h |
| `transportCooldown` | **90s** | `pool.go:40` | 隧道本地故障（handshake/首字节卡死）——地址本身未必坏 |
| `rateLimitCooldown` | **1min** | `pool.go:45` | 普通突发限流 |
| `maxRateLimitCooldown` | **5min** | `pool.go:49` | 普通限流的 Retry-After 上限 |
| `emptyPoolWait` | **25s** | `pool.go:87` | 池为空时 `Acquire` 最长阻塞 → 超时返回 `ErrNoExit` |
| `refillRetryDelay` | 5s | `pool.go:55` | 注册失败后的重试间隔 |
| `refillFailureThreshold` / `refillBackoffBase` / `refillBackoffMax` | 3 / 1min / **5min** | `pool.go:62-64` | 连续 duplicate 后 preload 退避 |
| `scaleDownDelay` | 60s | `pool.go:74` | 突发后保留热出口 |
| `DefaultPoolSize` / `maxPreload` | 4 / 8 | `pool.go:19` / `pool.go:24` | 池上限与预注册上限 |
| `maxAffinity` | 4096 | `pool.go:93` | 会话→出口 pin 表容量 |

`ErrNoExit = "WARP pool has no ready egress"`（`pool.go:83`）—— **就是日志里 503 的那句**；它在 `pool.go:441` 由 `AcquireFor` 的 `<-deadline` 分支产出，再被 `warp.go:235` 包成 `errWARPUnavailable`。

### 4.2 惩罚分类与写入路径

`penalize`（`pool.go:588-638`）是唯一的写账本入口：

- `pool.go:599` `if !bad.markPenalized(kind)` —— 一个 exit 只能被惩罚一次。
- `pool.go:604` `key := bad.egressKey()`；`pool.go:612-619` 写 `p.cooling[key] = max(现有, now+cooldown)`，且 `delete(p.reserved, key)`。
- `pool.go:620` 把 exit 从 `p.active` 环中摘除；`pool.go:630` 打印 `Rotating OpenCode WARP egress %s (%s), cooling %s` —— 对应观测到的 `Rotating ... (HTTP 403), cooling 24h0m0s`。
- `pool.go:634-637` 立刻后台 `replace()`。

`kind` 由 `classifyPenalty`（`pool.go:498-514`）决定：只有 429/403 参与分类；命中 quota 关键词（`quota`/`usage limit`/`free usage`/`insufficient_quota`/`billing`/`credit`/`额度`，`pool.go:503`）→ `penaltyQuota`；命中限流词（`rate_limit`/`rate limit`/`too many requests`/`try again`，`pool.go:508`）→ `penaltyRateLimit`；**默认（含未知 body）落 `penaltyQuota` → 24h**（`pool.go:513`，注释：`Unknown bodies default to quota`）。

触发来源：
- HTTP 403 → `exit.observe` 返回 `rotate=true, reason="HTTP 403"`（`exit.go:656-662`），经 `Pool.Report`（`pool.go:456-468`）→ 分类。**注意 403 也可能是 quota**（`classifyPenalty` 把 403 与 429 同等分类，`pool.go:499`），观测到的 `HTTP 403 → cooling 24h0m0s` 正说明 body 命中的是 quota 分支或走了默认分支。
- HTTP 429 → `exit.go:667-669`，理由注释明确「free-tier 429 scoped to the egress address」。
- 传输层失败 → `Pool.Fail`（`pool.go:551-559`）强制 `penaltyTransport` → 90s，且 `replace` 失败时**可以还原**（`pool.go:728-741`，`restorable := kind == penaltyTransport || kind == penaltyNone`）。quota/rate-limit 型**永不还原**（`pool.go:745-747` 打印 `stays out of rotation ... waiting for a fresh /64`）。

### 4.3 duplicate /64 判定

- `egressKey()`（`exit.go:742-754`）：IPv4 → `/32`；IPv6 → **`/64`**（`exit.go:753`，注释：`The free tier treats a Cloudflare /64 as one quota scope`）。
- `createExit`（`pool.go:979-1020`）：每次注册新出口后，先懒清扫过期 cooling（`pool.go:997-1001`），再判 `usable := key != "" && !p.reserved[key] && !p.cooling[key].After(now)`（`pool.go:1002`）。不可用 → `errDuplicateEgress`（`pool.go:1013`）+ 日志 `Discarding duplicate WARP exit %s: %v`（`pool.go:1015`）—— **正是观测到的 `Discarding duplicate WARP exit ...: 2a09:bac...::/64`**。同一 `createExit` 内重试 4 次（`pool.go:983`）。
- preload 层面：连续 `errDuplicateEgress` 累计到 3 次 → 指数退避 `1min<<shift`，封顶 5min，日志 `OpenCode WARP preload paused for %s after %d duplicate egresses`（`pool.go:831-851`）—— **对应观测到的 `preload paused for 5m0s after 6 duplicate egresses`**。
- 冷却持久化：`saveCooling`（`pool.go:677-713`）原子写 `statePath`；`warpStatePath(configPath)` 在 `internal/gateway/runtime.go:319-322`（与配置文件同目录）；启动时 `loadPenalties`（`pool.go:652-672`）读回，日志 `inherited %d spent egress block(s)`（`pool.go:305`）。

### 4.4 为什么一个坏出口会把整池烧掉（雪崩机制）

把 4.1–4.3 串起来即得死锁闭环：

1. **上游的惩罚粒度是 /64，引擎的惩罚粒度也是 /64**（`exit.go:753`）—— 对齐，本意正确。
2. 但 **Cloudflare 免费 WARP 的出口供给极小**：一个 colo 常只发极少数不同 /64，且**同一个 device 重注册极大概率拿到同一个 /64**（`pool.go:555-557` 注释 `Cloudflare usually hands the same /64 to the replacement`）。
3. 于是第一次命中 quota/403 → `cooling["2a09:bac…::/64"] = now+24h`（`pool.go:616`）→ `replace()` 注册新出口 → `createExit` 判定 duplicate（`pool.go:1002`）→ 4 次重试全 duplicate → `errDuplicateEgress`（`pool.go:1019`）。
4. `replace` 失败且 `kind==penaltyQuota` ⇒ **不可还原**（`pool.go:729-730`），旧出口被 `retire()`，新出口没有 → `p.active` 变空。
5. `refill` 继续撞 duplicate，累计到阈值后 `refillPaused` 最长 5min（`pool.go:836-851`）；期间 `AcquireFor` 面对 `len(p.active)==0` 只等 `emptyPoolWait=25s`（`pool.go:418/429-431`）→ `ErrNoExit`。
6. `ErrNoExit` → `warp.go:235` → `errWARPUnavailable` → `upstream.go:1041` `break` → `gateway.go:365` **503**。
7. 因为 24h 黑名单**跨重启保留**（`pool.go:677-713` + `loadPenalties`），且 `statePath` 在配置目录，所以「重启自愈」无效 —— 这与实测「独立实例关掉 WARP 才恢复」一致。

> 结论：**「一个坏出口烧掉整池」的充要条件是「出口供给 < 池规模」+「quota 型冷却固定 24h 且不可还原」。** 池子越大（`warp.size`/preload 越高）反而越容易触发 duplicate 退避（`refillFailureThreshold=3`，`pool.go:62`）。
>
> 补充：持久化文件 `<configPath>.warp-state.json`（`runtime.go:319-327`）。重启只会 `loadPenalties` 继承这些 24h 黑名单（`pool.go:290/652-672`，日志 `inherited N spent egress block(s)`），因此**重启不能自愈**；删掉该文件才会清空账本（属运行配置操作，本调研未执行）。

---

## Q5. 免费额度 / 429 的四个冷却维度（key / session / egress / model）与三个配置项的真实作用点

> ⚠️ 行号消歧：仓库里有**两个同名 `pool.go`** —— `internal/warp/pool.go`（1128 行，WARP 出口池，Q4 全篇）与 `internal/gateway/pool.go`（881 行，proxy/key 节点池，本节大量引用）。本节中标 `pool.go:` 且语义涉及 key/proxy/node/cursor/breaker 的，均指 **gateway** 那份；涉及 cooling 账本、egress、refill 的指 **warp** 那份（Q4 已逐条标出）。

### 5.1 维度总表

| 维度 | 账本位置 | 时长来源 | 是否受本次故障影响 |
|---|---|---|---|
| **egress（WARP /64）** | `internal/warp/pool.go:148` `p.cooling map[string]time.Time`（+ 持久化 `warpStatePath`） | 内置 24h/90s/1min（`pool.go:29/40/45`），**不可配置** | ✅ 就是它烧光 → 503 |
| **egress（静态 proxy）** | `internal/gateway/pool.go:32-38` `proxyTransport.blockedUntil` | `performance.free_tier_block_seconds`（默认内置 15min） | ❌ 未参与（key lane 不用 proxy） |
| **key** | `pool.go:605` `upstreamNode.cooldownUntil` | `performance.failure_cooldown_seconds`（默认 15s，×2^n，封顶 ×8） | ❌ WARP 模式下被刻意跳过（见 5.3） |
| **anonymous lane（熔断）** | `pool.go:81-84` `breakerUntil/breakerStrikes/consecutive` | `models.anonymous_backoff`（threshold 6 / base 1h / max 24h，×2） | 部分（`all_exits_blocked` 会 ForceOpen） |
| **session** | `internal/gateway/sessions.go:26-28` `gens map[string]int`（generation 计数） | **无时长**，只有代数（上限 4096 条，`sessions.go:33`） | 是（429 触发轮换） |
| **model（探活）** | `internal/models/availability.go:50-54` `items map[string]Availability` | 常量：成功/存疑 1h（`availability.go:13`）、确认不可用 24h（`availability.go:17`） | 是（探活被 WARP 保护逻辑跳过） |

### 5.2 `failure_cooldown_seconds`（默认 15）

- 读取：`config.go:243`，默认值 `config.go:303`（`FailureCooldownSeconds: 15`），校验 `config.go:378`（≥1）。
- 注入：`gateway.go:117` → `cooldown` → `nodePool(...)`（`gateway.go:118/122`，`pool.go:624`）与 `anonymousPool(...)`（`gateway.go:136`，`pool.go:111`）。
- **key 维度作用点**：`upstreamNodes.MarkFailure`（`pool.go:855-868`）—— 只对 401/403/429/5xx 或 transport error 生效（`pool.go:856`），`delay = cooldown * 2^min(fail-1,3)`（`pool.go:860-861`），Retry-After 可全额突破上限（`pool.go:862-866`）。游标跳过冷却中的 key，但不返回 nil（`pool.go:802-843`，注释 `unlike anonymousCursor.Next it never returns nil`）。
- **anonymous 维度作用点**：`anonymousNodes.MarkFailure`（`pool.go:395-410`），`delay = p.cooldown * (1<<min(failures-1,3))`（`pool.go:403`），Retry-After 封顶 `cooldown*maxAnonymousRetryAfterMultiplier`（`pool.go:406`）。
- 清账：`MarkSuccess`（`pool.go:846-849` / `pool.go:348-354`）、`RestoreProxy`（`pool.go:651-674`）、`RebindProxy`（`pool.go:709+`）。

### 5.3 ⚠️ 本次故障下 key 冷却被整体旁路

`observeKeyResultWARP`（`internal/gateway/warp.go:481-502`）在 `useWARP` 时替代 `observeKeyResult`：

- `warp.go:490-494`：`if err != nil && resp == nil { return }` —— 取不到出口/拨号/首字节失败 **不冷却 key**（注释：`the upstream never judged this key`）。
- `warp.go:498-500`：429 与 network-abuse 403 **也不冷却 key**（`return`），因为惩罚属于出口。

结论：**WARP 开着时，`failure_cooldown_seconds` 对免费模型的 key 几乎不起作用**；这解释了监控里 10 个 zen key 全为「健康」、却 100% 503 的矛盾现象。

### 5.4 `free_tier_block_seconds`（默认 0 → 内置 15min）

- 定义：`config.go:252-255`；校验 `config.go:390-391`（不得为负，0 用内置）。
- 注入：`gateway.go:141` `freeTierBlock`；读取：`internal/gateway/abuse.go:44-49`（`g.freeTierBlockTTL()`，>0 用配置，否则 `freeTierBlockTTL = 15*time.Minute`，`abuse.go:41`）。
- **唯一作用点**：`upstream.go:521` `node.proxy.markBlocked(time.Now().Add(g.freeTierBlockTTL()))` —— 即**静态 proxy 出口**被免费层判定 IP 滥用（`peekNetworkAbuseBlock`，`abuse.go:96-101` / `isNetworkAbuseBlock` `abuse.go:66-78`）时，把该 proxy 踢出匿名游标（游标侧过滤 `pool.go:294-306` `isBlocked`；`proxyTransport.blocked()` `pool.go:50-58`）。
- 全出口都被封 → `blockedExits`（`pool.go:135-145`）→ `ForceOpen` 熔断整条匿名 lane（`upstream.go:562-566`）。
- **对 WARP 出口无效**：WARP 的 403/429 走 `Pool.Report` → `classifyPenalty` → `egressCooldown=24h`（`pool.go:498-514/516-527`），**完全不读 `free_tier_block_seconds`**。观测到的 `Rotating ... (HTTP 403), cooling 24h0m0s` 正是这条路径，改 `free_tier_block_seconds` 不会缩短它。

### 5.5 `anonymous_backoff`（threshold/base_hours/max_hours）

- 定义：`config.go:162-176`，默认 `threshold=6, base_hours=1, max_hours=24`（`config.go:183-191`），校验 `config.go:433`。
- 注入：`gateway.go:136` + `anonymousBackoffFromConfig`（`gateway.go:148-157`）—— **块缺失 → `threshold=0` → 熔断器完全关闭**（`pool.go:177` `if p.backoff.threshold < 1 return true`，`pool.go:239`、`pool.go:257` 同样短路）。
- 作用点（全部在匿名 lane）：
  - 取出口前先问闸门：`pool.go:176-188` `AllowAttempt()`（半开只放 1 个探针）——被 `warp.go:321`（WARP 匿名 lane）与 `upstream.go:403`（proxy 匿名 lane）调用。
  - 失败计数与触发：`pool.go:238-251 NoteFailure` → `pool.go:264-274 trip`，`pause = base << min(strike-1,30)` 且封顶 `max`（`pool.go:267-270`）。
  - 调用点：`warp.go:449-453`（非 leaseBlocked 且非 429 时）、`upstream.go:552-557`、`upstream.go:1105-1108`。
  - 强制触发：`pool.go:256-261 ForceOpen` ← `upstream.go:563`（`reason=all_exits_blocked`）。
  - 成功/中性复位：`pool.go:210-218 NoteSuccess`（`warp.go:399`、`upstream.go:504`）、`pool.go:225-232 NoteNeutral`（`upstream.go:545`，确定性 4xx 不算 lane 故障）。
- 客户端可见效果：`errAnonymousLanePaused` → 503 `free_tier_lane_paused`（`abuse.go:30-35`，`gateway.go:394-397`）。

### 5.6 session 维度（`sessions.go`，无时间冷却）

- 语义：`sessions.go:10-24` —— 免费层账本是 **(session, egress) 二元组**；会话耗尽时「换出口」会把整池烧 24h 而修不好请求，因此先换 session。
- 作用点：`warp.go:356` `ResolveIDs`（发请求前落到当前代数）、`warp.go:432-438 RotateIDs`（首个 429 → 换 session、**出口暂不上报** `defer429`）、`warp.go:428-431 + penalizeHeldEgress`（`warp.go:286-295`，第二次 429 才把持有的出口 24h 退役）。
- `Pool.AcquireFor(ctx, affinity)`（`pool.go:381-414`）按会话 pin 出口（`pool.go:388-399`），上限 4096 条（`pool.go:93/1053-1059`）；key lane 与 probe 传 `affinity=""`（`warp.go:230` 由 `doKeyUpstream` 传 `""`，见 `upstream.go:1001`）→ 保持 round-robin。
- 派生规则：`rotatedSessionID`（`sessions.go:117-122`，`base + "\x00session-rotation\x00" + gen` 的 canonical `ses_...`）。

### 5.7 model 维度（`availability`，1h / 24h）

- 间隔常量：`models/availability.go:13`（`FreeModelCheckInterval = 1h`）、`:17`（`FailedModelCheckInterval = 24h`）；`Record` 只有 `reason=="model_unavailable"` 才真正禁用模型（`availability.go:191-192`，间隔切换 `:214-218`）。
- 调度与 **WARP 保护**：`internal/gateway/availability.go:68-85`（每分钟 tick）、`:109-155 checkFreeModels`：
  - `availability.go(gateway):125-127` `if snapshot.Active <= 1 { return }` —— 池里只剩 1 个出口时**停止探活**，避免探针把最后一个出口 429 掉。
  - `availability.go(gateway):150-154` 若本次探针使 `Cooling` 增长，立刻放弃本轮剩余探针。
  - 运维可 `RestoreModel`/`SetManualModel`（`availability.go:33-63`）手动解禁（`models/availability.go:112-151`）。
- 因此本次故障期间「模型可用性」页大概率显示未探/待定，而不是明确 unavailable —— 是 WARP 保护短路，不是模型真死。

---

## Q6. 配置热加载：`RuntimeController.Reload()` 与 `POST /api/config/reload` 能热应用什么、必须重启什么

### 6.1 调用链与「热」的机制

```
POST /api/config/reload   → internal/admin/server.go:62（路由）
  → handleReload          → internal/admin/server.go:267-274
    → manager.Reload()    → internal/gateway/runtime.go:580-591（config.Load 重读文件 → Apply(cfg, false)）
      → Apply             → runtime.go:474-478（取 updateMu）
        → applyLocked     → runtime.go:496-575（Normalize → build → 校验 → 可选落盘 → Swap 新 runtime）
```

关键的「热」证据：

- `runtime.go:425-435 Handler()`：每个请求取 `m.current.Load()`，**配置一被 apply，后续请求立刻用新 runtime**，无需重启。
- `runtime.go:554 m.current.Swap(next)` + `runtime.go:559 previous.cancel()`（只停旧 runtime 的后台 loop，在途请求继续用旧池）。
- 另一条热应用通道：`PUT /api/config`（`admin/server.go:61` → `handlePutConfig` `server.go:217-263` → `manager.Apply(candidate, true)`，`server.go:256`，会落盘）。
- 还有 `Update()`（`runtime.go:482-490`，读-改-写 + 必存盘）与 `retryWARP`（`runtime.go:366-407`，后台重试 WARP，间隔 15s/30s/1m/2m/5m，`runtime.go:352-358`）。

### 6.2 `restart_required` 的**完整**判定（只有 3 个字段）

`runtime.go:536-545`：

```go
result := ApplyResult{Applied: true}
if normalized.Listen != m.effective.API { ... "listen" }
if normalized.WebUI.Listen != m.effective.WebUI { ... "webui.listen" }
if normalized.WebUI.Enabled != m.effective.WebUIEnabled { ... "webui.enabled" }
result.RestartRequired = len(result.RestartFields) > 0
```

同一份判定也暴露在 `runtime.go:452-468 RestartStatus()`；`effective` 在启动时一次性固化（`runtime.go:118`，`EffectiveListeners{API, WebUI, WebUIEnabled}`）。注释亦见 `runtime.go:101-107` 与 `config.go:218-219`（`Changes to Enabled and Listen take effect only after a restart`）。

**⇒ 需要重启的只有：`listen`、`webui.listen`、`webui.enabled`（监听地址/UI 开关）。其余字段全部可热应用。**

### 6.3 四个目标字段逐一归类

| 字段 | 类别 | 证据与注意事项 |
|---|---|---|
| `warp.enabled` | **可热应用（但有两个坑）** | `selectWARP`（`runtime.go:277-317`）：`if !cfg.Warp.Enabled { return nil, "", false, nil }`（`runtime.go:278-280`）→ 关掉后新 runtime 的 `g.warp=nil`（`gateway.go:137`），`usesWARP()` 立刻恒 false（`warp.go:39`）。<br>坑 1：**开启**是 fail-closed 同步注册，`applyLocked` 用 `warpApplyDeadline=45s`（`runtime.go:61`，`runtime.go:512`），失败即整个 apply 报错（`runtime.go:313-314` → `runtime.go:513-515`），配置不会生效。<br>坑 2：旧池延迟关闭 `grace = retry.timeout_seconds`，封顶 5min（`runtime.go:561-571`），期间旧出口仍占资源。 |
| `anonymous` | **可热应用** | 经 `New()` 的 `anonymousPool(cfg.Anonymous, ...)`（`gateway.go:136`）重建。关掉后 `anonymous.Len()==0` → `warpProxyFallbackAllowed` 直接 false（`upstream.go:376-380`），匿名 lane 不再参与路由（`doUpstreamTiers` 的 `route.Anonymous` 分支由 catalog 依据 `g.cfg.Anonymous` 决定，`health.go:164` / `runtime.go:709`）。 |
| `proxies` | **可热应用** | `gateway.go:113 transportPool(cfg.RuntimeProxies(), ...)` 随新 runtime 重建；`nodePool` 重新做 key→proxy 轮转绑定（`pool.go:624-645`）。注意 `registryProxyURL`（`runtime.go:333-346`）用「第一个非 direct proxy」做 WARP 设备注册，改 proxies 会连带改变注册出口。 |
| `zen_keys` | **可热应用** | `gateway.go:118 nodePool(cfg.ZenKeys, ...)`；`admin/server.go:229-233 resolveSecrets` 支持「省略即保留原值」。<br>注意：热 swap 会新建 `sessionGuard`（`gateway.go:142`），**session 轮换代数清零**；WARP 出口池本身不重建（见 6.4），故 key 变化不影响已烧掉的 /64 账本。 |

### 6.4 唯一「部分冻结」的例外：WARP 池复用 key

`runtime.go:281`：

```go
key := fmt.Sprintf("%d|%s|%s|%t", cfg.Warp.PoolSize, cfg.Warp.Stack, cfg.Warp.SourceIPv6, cfg.Warp.TUN)
if m.warpPool != nil && m.warpKey == key { return m.warpPool, key, false, nil }
```

即 **`warp.size/stack/source_ipv6/tun` 四项不变时，reload 直接复用同一个 `*warp.Pool` 对象（连同其 `cooling` 24h 黑名单与 `refillPaused` 退避）**。想通过 reload 拿到「全新出口账本」，必须：

- 改动这四项之一（触发新建池，但新池仍会 `loadPenalties` 读 `<config>.warp-state.json` 继承黑名单，`pool.go:290`）；或
- `warp.enabled=false` 后再 `=true`（先 `return nil`，`m.warpPool` 被 `runtime.go:555` 覆盖为 nil，并延迟关闭旧池 `runtime.go:561-571`）—— 但冷却文件仍在磁盘，重启/重建仍会继承；
- 真正的清零只能删除 `<configPath>.warp-state.json`（运行配置操作，本调研**未执行**，仅指出位置 `runtime.go:319-327`）。

### 6.5 一句话答案

**`warp.enabled`、`anonymous`、`proxies`、`zen_keys` 全部属于「可热加载」类，`POST /api/config/reload` 即可生效；`restart_required` 只覆盖 `listen` / `webui.listen` / `webui.enabled` 三项。** 唯一要留意的不是「要不要重启」，而是「reload 不重建 WARP 池、也不清 24h 出口黑名单」。

### 6.6 ⚠️ 仓库自带文档与本结论的两处冲突（以代码为准）

| 位置 | 文档说法 | 代码事实 |
|---|---|---|
| `doc/05-故障排查.md:38` | 「想彻底不用 WARP，把 `warp.enabled` 改成 `false`，**重启**」 | **不需要重启**：`selectWARP`（`runtime.go:278-280`）+ `m.current.Swap`（`runtime.go:554`）已证明 reload 即生效。该文档成文时可能尚未接入 RuntimeController 热切换，或作者不确定 reload 对 `warp.*` 的效果 |
| `doc/03-网络-WARP-与出口.md:235` | 「新版把额度命中按 **30m→2h→8h→24h 递增**并持久化」 | 当前基线是**固定 24h、无递增**：`quotaCooldownFor` 注释写明 `Fixed 24h blacklist ... No escalation`（`pool.go:516-527`），`noteSuccess` 是空函数（`pool.go:544-546`），`coolingState` 只存 `cooling` 不存 strikes（`pool.go:643-646`） |

⇒ 给运维的两个结论：**(1) 关掉 WARP 走 reload 就够，不用重启；(2) 一次 quota 命中就是 24h，别指望它按小时逐步恢复。**

---

## Q7. 比「WARP 空→放弃」更稳的现成开关组合（含建议配置片段）

### 7.1 先明确「哪些组合根本无效」

| 想法 | 判定 | 代码依据 |
|---|---|---|
| 保持 `warp.enabled=true`，指望 key 请求自动回退到 `proxies` | ❌ **不可能** | key lane 的 WARP 分支（`upstream.go:1000-1001`）没有任何 proxy 路径；`warp_proxy_fallback` 只在 `doAnonymousUpstream`（`upstream.go:350-367`）。仓库文档同口径：`WARP 和 proxies 在免费模型上是互斥的：开了 WARP 就不会用代理`（`doc/02-配置参考.md:67`） |
| 调大 `retry.max_attempts` 多试几个 key | ❌ 无用 | `errWARPUnavailable` 在**第一次**尝试就 `break`（`upstream.go:1041-1045`），与 max_attempts 无关 |
| 调 `performance.free_tier_block_seconds` 缩短 WARP 冷却 | ❌ 无用 | 该值只作用于**静态 proxy** 的 blocked（`upstream.go:521` → `abuse.go:44-49`）；WARP 冷却是硬编码 24h（`warp/pool.go:29/516-527`），完全不读它 |
| 重启进程清掉坏出口 | ❌ 无用（甚至更糟） | 24h 黑名单持久化在 `<config>.warp-state.json`（`runtime.go:319-327`），重启 `loadPenalties` 原样继承（`pool.go:290/652-672`），日志 `inherited N spent egress block(s)` |
| 指望 `warp.source_ipv6` 轮换出新 /64 | ⚠️ Windows 上不成立 | `freebind_other.go:1-9`（非 Linux 空操作），文档明写「Windows 上填了也不会生效」（`doc/02-配置参考.md:65`）→ 这正是本机坏 /64 **无法靠重注册绕开**的根因之一（`pool.go:555-557`：Cloudflare 通常把同一 /64 再发给替代品） |

### 7.2 有效组合 A（推荐，已实测 5/5）：彻底不用 WARP，key + 代理

原理：`warp.enabled=false` → `g.warp=nil`（`runtime.go:278-280` → `gateway.go:137`）→ `usesWARP()` 恒 false（`warp.go:39`）→ key lane 走 `doInferenceAttempt(inferenceClient(proxy,...))`（`upstream.go:1003`），proxy 绑定恢复生效（`upstream.go:982-990`）。

```jsonc
{
  "anonymous": false,                  // 不进匿名免费 lane，请求只用带 key 的 tier
  "warp": { "enabled": false },        // usesWARP() 恒 false，key 请求回到 proxies
  "proxies": ["http://127.0.0.1:7890"],// 必须是非 direct 的可用地域出口
  "prefer": "zen",                     // 只有 zen_keys 时 zen 优先（默认其实是 "go"）
  "zen_keys": [ "……10 个 key……" ],
  "go_keys": [],
  "retry":     { "max_attempts": 3, "timeout_seconds": 300 },
  "performance": {
    "failure_cooldown_seconds": 15,    // key/proxy 失败冷却基数（pool.go:855-868）
    "free_tier_block_seconds": 900,    // 仅作用于静态 proxy 的 IP 封禁（upstream.go:521）
    "max_anonymous_exits": 8
  }
}
```

要点与注意：

- 出口必须是**非受限地域**的代理。`proxies:["direct"]` 会命中 `403 This model is not available in your country`（已实测）——那是 `isNetworkAbuseBlock` 之外的地域拒绝，仍会消耗出口健康度。
- `anonymous:false` 之后 `proxies` 的唯一职责就是给 key 做出口，行为最可预测。
- 代价：放弃匿名免费额度池，全部按 key 计额度。
- 这四步全是**可热应用**字段（Q6 §6.3），改完 `POST /api/config/reload` 即生效，不用重启。

### 7.3 有效组合 B（保留 WARP 的折中）：让免费模型优先落 Go tier，把 Zen tier 留作兜底

原理：`usesWARP` 只在 `tier == TierZen` 时命中（`warp.go:39`）——**Go tier 永远走 proxy**。`prefer:"go"` 让 `keyTierOrderLocked` 把 Go 排在 Zen 前（`models/catalog.go:337-352`，`order[0],order[1] = order[1],order[0]`），于是免费模型请求先在 Go tier 用 proxy 成功，根本不碰 WARP。

```jsonc
{
  "anonymous": true,                   // 保留匿名 lane：它有 warp_proxy_fallback 兜底（upstream.go:364）
  "warp": { "enabled": true, "pool_size": 3, "stack": "ipv6" },  // pool_size 建议 3–6（doc/03:235）
  "proxies": ["http://127.0.0.1:7890", "direct"],   // 也给 WARP 注册当 registry 出口（runtime.go:333-346）
  "prefer": "go",                      // ★ 关键：Go tier 先行，避开 usesWARP()
  "go_keys":  [ "……Go 订阅 key……" ],   // ★ 前提：必须有 go_keys，否则 keyTiers=[zen] 仍被 WARP 锁死
  "zen_keys": [ "……10 个 key……" ],     // 留作兜底
  "models":   { "anonymous_backoff": { "threshold": 6, "base_hours": 1, "max_hours": 24 } }
}
```

- **只有 `go_keys` 非空且该模型在 Go 目录里可用时才成立**（`catalog.go:343-344` 的 `available()` 判定）。若 `go_keys` 为空，`keyTiers` 退化为 `[zen]`（`catalog.go:337-360`），WARP 一开就又回到本次故障形态。
- `pool_size` 不宜大：`/64` 供给有限，池越大越容易撞 duplicate → `refillPaused` 5min（`pool.go:61-64/831-851`），与 `doc/03-网络-WARP-与出口.md:235` 的「建议 3–6」一致。

### 7.4 一句话建议

**当前这台 40080 实例（已知：有 10 个 zen key、跑在 Windows；`go_keys` 是否配置本次未核实，不碰运行配置）→ 若 `go_keys` 为空则只能用组合 A**：`warp.enabled=false` + `anonymous=false` + `proxies=[可用代理]`，四字段皆可热加载（Q6），改完 `POST /api/config/reload` 即恢复。`go_keys` 非空时可选组合 B（`prefer:"go"` + 保留 WARP 作兜底）；否则 Zen key 的免费模型流量永远被 `usesWARP()`（`warp.go:39`）单点绑死在 WARP 出口上。

---

## Q8. 上游是否有新版本 / 修复（WARP、429、no channel）

**远程不可达，无法判定是否有更新。** 按指令执行了一次 `git fetch --all --prune`（60s 超时，未重试），结果：

```
remote: Repository not found.
fatal: repository 'https://github.com/wbi35772/opencode2api-win.git/' not found
```

本地可见事实：

| 检查项 | 结果 |
|---|---|
| `git remote -v` | 仅 `origin https://github.com/wbi35772/opencode2api-win.git`（fetch/push 同址，**404 / 私有 / 已删除**） |
| `git branch -a` | `main` + `remotes/origin/HEAD -> origin/main`、`remotes/origin/main`（本地缓存的 ref，非最新） |
| `git log --oneline -20` | **仅 1 条**：`9964407 baseline: opencode2api Windows/WARP gateway (v1.4.6 + session-affinity 429 fix)` |
| `git log --since='60 days ago' --oneline` | 同上，仅 `9964407` |
| `git tag` | 空（无任何 tag） |

**结论**：该仓库是**单提交（squashed baseline）镜像**，本地历史里不存在任何可比对的新版本，也没有 WARP / 429 / no channel 相关的后续修复提交可 cherry-pick。基线提交信息本身已含 `session-affinity 429 fix` —— 即 **Q5.6 的 session↔egress 二元组修复已经在跑的这份代码里**，本次故障不是「缺该修复」，而是「修复生效但 WARP 出口供给本身枯竭」。

因此：**任何「等上游修复」的路线都不可行，只能走配置侧（见 Q7）。**

---

## 附录. 排障时容易误判的两个次级机理

1. **`/healthz` 的 `active=1` 与「no ready egress」并不矛盾。** `ErrNoExit` 只在 `len(p.active)==0` 时产出（`pool.go:418` 的 `empty` 判定 + `pool.go:426-432/441`），而 `Snapshot().Active` 是采样瞬时值（`pool.go:565-586`，`health.go:77-90`）。池在「空 → 后台 replace 出一个 → 立刻又被 403/429 罚掉」之间来回抖动，所以 `/healthz` 常看到 `active=1, cooling=40`，同时 503 仍在刷。
2. **只剩 1 个出口时不是 503，而是挂到请求超时。** 单出口并发上限 `inFlight >= 3` 即 `addRef` 失败（`exit.go:602-611`）；此时 `empty==false` → `AcquireFor` 不设 deadline（`pool.go:426-432`），请求**阻塞到 `retry.timeout_seconds`（300s）才因 ctx 超时**，`warp.go:232-233` 返回 `ctx.Err()` 而非 `errWARPUnavailable`。这解释了日志里「503 与长时间挂起/流中断」两种形态并存（另有 `warpStreamIdle=2min` 看门狗，`warp.go:127-132/155-177`）。
3. **`key 全部显示健康` 是设计使然，不是监控坏了。** `observeKeyResultWARP` 在 `resp==nil` 时直接 return（`warp.go:490-494`），`/api/monitor` 的 `KeyStatus.CooldownUntil` 因此为空（`runtime.go:634-641/796-806`）。排障应看 `Rotating OpenCode WARP egress`、`Discarding duplicate WARP exit`、`stays out of rotation`、`preload paused` 这四类事件（与 `doc/07-已知问题与限制.md:45/84-90` 一致）。
