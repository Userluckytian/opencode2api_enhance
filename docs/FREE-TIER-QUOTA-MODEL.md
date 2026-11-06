# OpenCode 免费额度的记账维度与「模拟无限」的理论边界

> **性质**：机制分析文档，基于上游 `opencode2api-win`（commit `9964407`）源码逐条核对。
> 目的只有一个：**把原理搞清楚** —— 免费额度到底按什么维度计、为什么"无限 IP ≠ 无限 token"、
> 现有实现里哪些环节已经把某个维度做成了可再生的。
>
> **边界声明**：本文只描述上游已实现的机制与理论上的约束条件，不构成、也不用于
> 绕过上游风控或滥用免费额度的操作指南。上游同时存在身份形状校验、出口黑名单、
> 请求级确定性拒绝等反制，实际可用额度远小于理论上限。

---

## 一、先纠正一个常见前提：额度不是「按 IP」计的

早期流传的说法是「每个 IP 给 1 亿 token，有无限个 IP 就有无限 token」。
**当前上游不是这个模型。** 源码里明确写的是按 **(session, egress)** 二元组记账：

| 证据 | 原文要点 |
|------|----------|
| `internal/warp/pool.go:375-380` | "The free tier ledgers its quota against the **pair (session, egress)**, so a conversation that hops exits looks spent to every fresh one" |
| `internal/gateway/sessions.go:14-18` | "The free tier ties its quota and risk bookkeeping to the **session id, not only to the egress address**: a conversation that has exhausted its free allowance keeps answering 429 ... on **every fresh WARP exit**, while a **new session on the same exit succeeds**" |
| `internal/identity/request.go:80-87` | 额度还挂在 **project id** 上；客户端没带 project 时，引擎**每个请求随机生成一个**（`RandomProjectID()`），注释写明「one shared id is one shared bucket」 |

所以现在的额度桶至少是三维：

```
额度桶 = f(egress /64, session id, project id)
```

推论：**纯换 IP 已经不够了** —— 一个烧干的 session 换到全新出口，上游照样回 429
（`sessions.go:16-18`）。上游把这一个坑写进了注释，并据此改了引擎的轮换策略。

---

## 二、引擎自己踩过这个坑，才改成「先转身份、再转出口」

这段最能说明"哪个维度是可再生的"。`internal/gateway/warp.go:336-341`：

> A 429 on the free tier can belong to the exit or to the session. The first 429
> of a request is validated against the session: the exit is not reported to the
> pool, the session id is rotated, and the request is retried. Only a second 429
> retires the exit. **Rotating exits first ... spends the whole warm pool for 24
> hours without fixing the request.**

对应的两条机制：

**1. 会话钉在单个出口上（affinity）** —— `pool.go:375-399` `AcquireFor(ctx, affinity)`
优先复用该会话已绑定的出口。因为"一个会话在多个出口间跳"会让每个新出口都把它当
已烧（`pool.go:376-378`）。绑定表上限 `maxAffinity = 4096`（`pool.go:89-93`）。

**2. 429 时先换身份** —— `warp.go:422-439`：

```
第一次 429 → 不报给出口池，RotateIDs(baseIDs) 派生新 session id
           → 在【同一个出口】上重试
           → 若 2xx：证明出口是好的、是这个会话烧了
           → 若第二次 429：才把这个出口判定为烧掉并淘汰
```

`internal/gateway/sessions.go:20-24` 说清了身份怎么再生：generation 递增 →
派生一个新的 `ses_...` id → 该会话后续请求继续用这个新身份 → 直到它也烧掉。

**这就是"模拟无限"理论上的那条缝**：额度按 (身份, 出口) 记账，而身份是客户端可
自造的字符串。只要"烧一个换一个"能被上游接受，就能在**同一批 IP** 上持续拿额度，
不依赖 IP 数量。

---

## 三、为什么那条缝现在还不是无限：四个约束

### 约束 1 —— 上游在校验身份的合法性（最主要的反制）

`internal/identity/request.go:187-239` 记录了上游已经加上的形状校验：

- session 必须是 `ses_` + 12 位 hex 时间戳 + 14 位 Base62
  （`canonicalSessionPattern`）；官方形状之外的 id 一律 403 `FreeTierError`
- 时间戳部分用官方 `Identifier.descending()` 的取反时间戳，上游只接受
  counter 1–63 的窗口（`descendingTimePartPattern`）
- 时间戳必须"合理"：偏离当前时间约一年，上游回 **429 "Free usage limit reached"**
  （`request.go:234-239`）—— 注意这和"IP 真的烧了"是**同一条消息**，靠消息体分不出来
- 因此引擎用 `bootNonce`（每进程一次）把下游身份重编码成合法形状，
  且**重启进程会换一批 session id**（`request.go:204-232`）

也就是说：身份维度不是"随便造一个字符串就行"，上游已经把可造的空间收窄了。
引擎做的是"生成合法形状的身份"，不是"伪造无限身份"。

### 约束 2 —— 引擎自己的表有上限

| 上限 | 源码 | 值 | 满了会怎样 |
|------|------|-----|-----------|
| session generation 表 | `sessions.go:33` `sessionGuardMaxEntries` | 4096 | 随机丢一条，该会话退回 generation 0 |
| 会话→出口 affinity | `pool.go:93` `maxAffinity` | 4096 | 随机丢一条，重新走轮询 |
| 出口池 | `pool.go:191` `Size` | `pool_size`（面板默认 2~4） | 不扩容 |
| 每进程身份盐 | `request.go:209` `bootNonce` | 1 个 | 重启即换一批 |

### 约束 3 —— 出口维度握在 Cloudflare 手里

`pool.go:57-60` 明说："A pool can only hold as many exits as **Cloudflare hands out
distinct egress blocks**. When preload is larger than that supply, every extra
registration is a duplicate." 连续 3 次重复注册就退避（`refillFailureThreshold = 3`）。

而且额度按 **/64 块**算，不是按单个 IP（`exit.go:742-753`）：

> The free tier treats a Cloudflare /64 as one quota scope: rotating inside the
> same /64 yields a new /128 but the **same exhausted quota**.

所以"出口很多"≠"额度很多"，有意义的是**不同的 /64 数量**，供给方是 Cloudflare。
每个被判定为 quota 429 的 /64 还要封 **24h**（`pool.go:29` `egressCooldown`、
`classifyPenalty` 默认按 quota 归类 → `pool.go:494-513`）。

### 约束 4 —— 请求级确定性拒绝根本不换 lane

`internal/gateway/abuse.go` 的 `isDeterministicClientResponse`（400/422 +
`invalid_request_error`）会让引擎**直接把 4xx 返回客户端，不进任何降级/轮换**。
例如单请求估算 token 超组织 tpm 上限的 `request_exceeds_token_rate_limit`
（400），重试、换出口、换身份都无意义。这条和额度维度无关，但它是"无限"最硬的
天花板之一：单请求本身就有上限。

---

## 四、四条"理论无限"路径的可行性排序

纯理论推演，按「是否握在我们手里」排序：

| 路径 | 谁供给这个维度 | 能否自我再生 | 结论 |
|------|---------------|-------------|------|
| **① 身份（session / project）** | 客户端自造字符串 | **能**（引擎已实现 `warp.go:432` 的 session rotation + `request.go:86` 每请求随机 project） | **最接近的一条缝**。前提是上游继续只按 (身份, 出口) 记账、不引入跨身份的全局风控，且去掉引擎自己的 4096 上限。但约束 1 的形状校验说明上游已在收窄这个空间 |
| ② 出口 IP（WARP /64） | Cloudflare | 不能 | 上限由 Cloudflare 给的不同 /64 数决定；烧掉的 /64 封 24h；需要本机有可路由 `source_ipv6` 才能换源（`pool.go:195-199`、`bind.go:124-139`）。**不是我们能刷的** |
| ③ key（账号） | 上游签发 | 不能 | 每个 `oc_sk_` 是独立账号额度，但 key 造不出来，属于充钱/注册范畴 |
| ④ 时间（额度窗口） | 上游重置 | 周期性 | `model_quotas.go` 的 `ResetsAt`、24h 冷却。等就行，是"续杯"不是"无限" |
| ⑤ 代理/节点池 IP | 你的节点池 | 能（关 WARP 时） | **只在回退链路上有效**：WARP 关闭或池全挂时免费通道走代理列表，那时切节点=换 IP。但 WireGuard 数据面走 UDP，代理不承载（`runtime.go:329-333`），WARP 模式下碰不到出口 IP |

### 一句话结论

> 在当前这套里，**唯一不依赖上游供给、理论上能自我再生的维度是「身份」**
> （session / project id）。引擎已经靠 `warp.go:432` 的 session rotation 与
> `identity/request.go:86` 的随机 project id 摸到了这条缝：额度按 (身份, 出口)
> 记账，身份烧了就换一个新的，同一批 IP 继续用。
>
> 而**纯靠换 IP 已经不成立**：session 烧干后换 IP 照样 429，且不同 /64 的供给方
> 是 Cloudflare、每条还封 24h。
>
> 真要"模拟无限"，需要同时满足：上游持续只按二元组记账 + 无跨身份全局风控 +
> 绕过身份形状校验 + 去掉引擎的 4096 上限。这四条里前三条都不在我们手里，
> 且属于对抗风控 —— 本文只做机制说明，不提供实现方式。

---

## 五、与本项目配置项的对应（便于对照）

| 面板/配置项 | 影响哪个维度 | 说明 |
|-------------|-------------|------|
| 「池大小上限」`engine_warp_pool_size` | 出口数量 | 上限仍受 Cloudflare 供给约束 |
| 「源 IPv6 前缀」`engine_warp_source_ipv6` | 出口的**源地址** | 需要本机可路由的 /64；没配时换出口常拿回同一个 /64（白换） |
| 「出口代理」`engine_proxies` | 回退链路的出口 | 仅 WARP 关闭/池挂时生效；WireGuard UDP 不走它 |
| 「通道顺序」`engine_channel_order` | 谁先被消耗 | WARP 优先时 key 只在 WARP 不通时兜底，省的是 **key 额度** |
| `models.anonymous_backoff`（管理器写入 6/1/24） | 匿名通道熔断 | 连续失败 6 次暂停 1h，上限 24h（≥ 出口 24h 冷却期） |
| 「重置出口冷却」 | 本地出口账本 | 只清 `config.json.warp-state.json`，**不重置上游额度** |

> 关于"重置出口冷却为什么不能重置额度"：额度在服务端按账号/session 记账，
> 重启、换出口、换 key 都改不了它。清账本只是让引擎愿意重新注册出口。

---

## 六、源码索引（便于复核）

均指上游 `opencode2api-win` commit `9964407`：

| 主题 | 文件 |
|------|------|
| 二元组记账 / affinity | `internal/warp/pool.go:375-399`、`:89-93`、`:57-60`、`:191` |
| /64 作为额度作用域 | `internal/warp/exit.go:742-753` |
| session 烧干后换 IP 仍 429 | `internal/gateway/sessions.go:10-24`、`:33` |
| 429 先转身份再转出口 | `internal/gateway/warp.go:336-341`、`:422-439` |
| 身份形状校验 / 时间戳风险窗口 | `internal/identity/request.go:187-239` |
| project id 每请求随机 | `internal/identity/request.go:80-87`、`:312-322` |
| 24h 冷却 / 429 归类 | `internal/warp/pool.go:26-51`、`:494-518` |
| 请求级确定性拒绝 | `internal/gateway/abuse.go:126`（`isDeterministicClientResponse`） |
| 代理只用于注册、不承载 UDP | `internal/gateway/runtime.go:329-345` |
| WARP 出口源地址绑定 | `internal/warp/bind.go:124-154` |
| 额度窗口重置 | `internal/admin/model_quotas.go:19-42` |
