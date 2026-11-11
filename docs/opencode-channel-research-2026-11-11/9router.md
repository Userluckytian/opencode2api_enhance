# 9router（decolua/9router）调研报告

- 仓库路径：`D:\AI_Projects\refs\9router`（本地 HEAD `87a74e1b`，`# v0.5.104 (2026-10-15)`）
- 调研性质：**只读**。未修改仓库任何文件；未启动/停止任何进程；未占用端口。
- 调研日期：2026-11-11
- 调研目的：判断它对「OpenCode 通道经常无通道可用」这个问题有没有参考价值
- 代码规模：约 1797 个非 `.git` 文件（JS 为主，Next.js App Router + 独立 `open-sse/` 引擎层）

> **结论先看（TL;DR）**：**不建议整体替换**，但有 6 个机制非常值得抄进我们的 Go 引擎 ——
> ① `modelLock_<model>` 按「连接 × 模型」二维记账的冷却账本；
> ② `ERROR_RULES` 表驱动错误分类 + 指数退避 + 上游 `resetAt` 三层优先级；
> ③ combo（同一逻辑模型多源候选 + 按能力分档重排 + 逐源降级）与 capacity-adapter 备用源池；
> ④ 「4xx 属于请求本身的问题时不冷却账号」这条防误伤规则（新版升级为 `requestScoped` 双问句）；
> ⑤ 「先刷 token 就地重放，再换账号，最后才换模型」的降级顺序；
> ⑥ 「HTTP 200 但零可用内容也算失败，继续降级」（SSE peek 版）。
>
> **两个必须优先看的意外发现**：
> - **§1.3 第 5 点**：上游 OpenCode 免费门**看的是 TLS ClientHello（JA4 密码套件哈希），不是请求头**（`origin/master:open-sse/services/opencodeTlsClient.js:1-9`，2026-10-26 实测：同进程同出口 IP，node fetch/https/http2 全 429，Bun/BoringSSL 200）。这可能重新解释我们「代理 5/5 通、直连 403」的实验结论。
> - **§6.1**：9router 是 **MIT**，而我们今天坏掉的那套免费 lane 编排在 **GPL-3.0 的 `opencode2api-win`（40080）** 里、且明确列为「GPL 隔离红线，不改引擎」。→ **9router 提供的是一条法律上干净的「在我们自己代码里重写编排」参照路径。**
>
> 详见各节行号。

---

## 1. Provider / 通道清单，以及 opencode（zen / go / free）怎么接的

### 1.1 通道总量与分类

Provider 定义全部集中在 `open-sse/providers/registry/*.js`，**共 137 个 provider 条目**（`index.js`、`REGISTRY_TEMPLATE.js` 除外），按 `category` 字段分五类：

| category | 数量 | 含义 |
|---|---|---|
| `apikey` | 85 | 填 API key 即用（含 OpenRouter / 火山 / SiliconFlow 等中转站） |
| `oauth` | 25 | OAuth 登录型（GitHub / Codex / gemini-cli 等） |
| `freeTier` | 20 | 有免费额度的正规 key 通道 |
| `free` | 5 | **完全匿名 / 无需 key**（`noAuth: true`） |
| `webCookie` | 2 | 用浏览器 Cookie 冒充（grok-web / perplexity-web） |

分类装配代码：`src/shared/constants/providers.js:43-48`
```js
const byCategory = (cat) => Object.fromEntries(
  REGISTRY.filter(r => r.category === cat).map(r => [r.id, buildProviderEntry(r)]));
export const FREE_PROVIDERS = byCategory("free");          // 第 47 行
export const FREE_TIER_PROVIDERS = byCategory("freeTier"); // 第 48 行
```

`free`（匿名）类中与本次课题相关的成员：

| provider | category | priority | noAuth |
|---|---|---|---|
| `opencode`（"OpenCode Free"） | free | 40 | **true** ← 匿名免费 lane |
| `mimo-free` | free | 50 | true |
| `devin-cli` | free | — | true |
| `kiro` | free | 10 | — |
| `gemini-cli` | free | 20 | — |

### 1.2 opencode 三条 lane 的实现分布（关键定位表）

9router 把 OpenCode 生态拆成 **3 个独立 provider**，而不是一个 provider 内部切 lane：

| provider id | 类别 | 鉴权 | 上游 base | 实现文件 |
|---|---|---|---|---|
| `opencode`（alias `oc`，显示名 "OpenCode Free"） | `free` / `noAuth:true` | **`Authorization: Bearer public`** 匿名 | `https://opencode.ai/inference/v1/*` | `open-sse/providers/registry/opencode.js`、`open-sse/executors/opencode.js` |
| `opencode-zen`（alias `ocz`，"OpenCode Zen" PAYG） | `apikey` | 真 `oc_sk_*` key | `https://opencode.ai/zen/v1/*` | `open-sse/providers/registry/opencode-zen.js`、`open-sse/executors/opencode-zen.js` |
| `opencode-go`（"OpenCode Go"） | 独立 provider | 独立鉴权 | 另一套 host | `open-sse/providers/registry/opencode-go.js`、`open-sse/executors/opencode-go.js` |

用量统计另有两份：`open-sse/services/usage/opencode-zen.js`、`open-sse/services/usage/opencode-go.js`。
另有 `opencode-zen` 的 registry 条目 `priority: 205`（`open-sse/providers/registry/opencode-zen.js:3`）、`opencode` 的 `priority: 40`（`opencode.js:3`）。

**注意：整个仓库搜不到 `warp` / `WARP` 关键字，一处都没有。** 9router 的匿名出口不是 Cloudflare WARP，而是**代理池（proxy pool）+ 可选 Vercel relay**：
- `open-sse/utils/proxyFetch.js` —— 代理抓取封装（425 行）
- `src/lib/network/connectionProxy.js` —— `resolveConnectionProxyConfig` / `pickProxyPoolId`
- 每条 connection 携带 `connectionProxyEnabled` / `connectionProxyUrl` / `connectionNoProxy` / `connectionProxyPoolId` / `vercelRelayUrl` / `strictProxy`，装配点 `open-sse/handlers/chatCore.js:346-355`

### 1.3 免费 lane 的「身份指纹」——它怎么让匿名请求不被 403

这是本次调研信息密度最高的一块，且和我们实测的「直连 → 上游 403 not available in your country」直接相关。匿名 lane 的通过条件不是「有没有 key」，而是**指纹四件套**：

1. **UA 版本门**（`open-sse/executors/opencode.js:18-32`）
   ```js
   const OPENCODE_UA = "opencode/latest/2.0.26/cli";
   // OpenCode v2 UA shape: opencode/<channel>/<version>/<client>. Upstream rejects
   // the v1 "opencode/1.18.x" form with 403 FreeTierError since 2026-10-14.
   ```
   → 上游在 **2026-10-14 换了指纹要求**，v1 形状 `opencode/1.18.x` 一律 403。zen lane 仍用旧形状（`opencode-zen.js:18` 的 `opencode/1.18.31` + `:25-31` 校验 `major>1 || (major===1 && minor>=17)`），因为付费 lane 门槛不同。
   对应修复提交：`5bdcd5e7 fix(opencode): move free tier to /inference/v1 with the v2 client fingerprint (#4842)`。

2. **session id 必须像 CLI 真生成的**（`opencode.js:52-71`）
   ```js
   // Session prefix = 12 hex of ~(timeMs * 0x1000 + counter), low 48 bits, like the CLI.
   // Upstream decodes it and rejects a non-small counter with 403 FreeTierError
   // (live probes: counter 0–16 pass, >=100 fail), so the counter must stay real.
   ```
   `descendingTimeHex()` = `~(timeMs*0x1000+counter) & (2^48-1)`，counter 必须 ≤16。

3. **fingerprint tools 四件套**（`opencode-zen.js:21-23`、`opencode.js:366-372`）
   ```js
   // Free-tier fingerprint (mirrors opencode CLI, PR #4132): upstream 403s
   // requests without the file-search quartet and without stream:true.
   const OPENCODE_FINGERPRINT_TOOLS = ["bash", "glob", "grep", "read"];
   ```
   缺任意一个 → 403；即使客户端自己带了 tools 数组也要补齐（`applyFingerprintTools(body, true)`）。

4. **必须 `stream:true`**（`opencode.js:339-341`、`opencode-zen.js:298-299`）
   ```js
   // Zen rejects non-streaming requests on free models with 403 FreeTierError;
   // always stream upstream and let the handler layer aggregate for non-stream clients.
   ```

5. **免费模型的 Authorization 强制覆盖**（`opencode-zen.js:285-292`）——这段注释直接说明「keyed lane 上的免费模型」行为：
   ```js
   if (isOpencodeFreeTierModel(model)) {
     // Free-tier models are gated behind the attested free-tier identity even
     // on the keyed lane: a real oc_sk_* key answers 403 FreeTierError, while
     // `Bearer public` + session/request ids succeed. Paid models keep the
     // connection key untouched.
     headers["Authorization"] = "Bearer public";
     headers["x-opencode-request"] = generateRequestId();
   }
   ```
   → **对我们极其重要**：免费模型用真 `oc_sk_*` key 反而 403，必须 `Bearer public`。我们手上那 10 个 zen 免费 key 如果以付费身份去请求免费模型，会全量 403，表现和「通道废了」一模一样。

匿名请求头完整形状（`opencode.js:401-414`，值得逐条比对我们的实现）：
```js
const headers = {
  "Content-Type": "application/json",
  "Authorization": "Bearer public",
  "User-Agent": isOpencodeDownstream ? downstreamUa : OPENCODE_UA,
  "x-opencode-client": lower["x-opencode-client"] || OPENCODE_CLIENT,   // "cli"
  "x-opencode-session": session,
  "x-opencode-session-id": session,      // v2 需要两个都在
  "x-session-affinity": session,
  "x-session-id": session,
  "x-opencode-project": lower["x-opencode-project"] || "global",
  "Accept": stream ? "text/event-stream" : "*/*",
};
// Mirrors the v2 CLI ... and rejects any x-opencode-request (v1 only) with 403.
```
注意 v1（zen）要 `x-opencode-request`（`opencode-zen.js:291`），v2（free）带这个反而 403 —— 两条 lane 的头集**不兼容**，不能共用一份模板。

6. **⭐ 第 5 道门是 TLS ClientHello，不看头部**（本地 HEAD 无此文件；`origin/master:open-sse/services/opencodeTlsClient.js:1-9`，实测日期 2026-10-26）
   ```js
   // TLS-impersonating HTTP client for opencode.ai's free tier.
   // The free gate keys on the TLS ClientHello, not on headers: a request that
   // passes from the OpenCode CLI is refused with 429 FreeUsageLimitError when the
   // same body/headers ride Node's OpenSSL fingerprint, and passes again once the
   // cipher list matches. Measured 2026-10-26 on one process, one egress IP, three
   // transports: node fetch/https/http2 all 429, Bun's BoringSSL fetch 200.
   // JA4's cipher hash identifies the list (Bun: 5b57614c22b0), so the match is on
   // the cipher-list hash and nothing else — reordering the same list still passes.
   ```
   - 它把 **BoringSSL/Chrome 家族的密码套件列表逐字抄下来**（`:18-37`，17 项，注释「Only the list's hash is compared, so this must stay verbatim」），用可选依赖 `wreq-js` 的 `createSession({ emulation: { tlsOptions: { cipherList } } })` 发请求（`:52-63`）；`wreq-js` 不在时**抛 `OpencodeTlsUnavailableError` 而不是失败请求**（`:70` 注释 + `:81-88`：caller keeps native fetch and「stays subject to the gate rather than failing the request here」= fail-open）。
   - 响应体以 live ReadableStream 交还（`:74-76`「Streaming is preserved … never buffered」），下游 `.body/.text()/.json()` 无需改。
   - 单例 session 缓存在模块级 promise（`:44 loadSession()`），失败时重置 `sessionPromise = null` 允许下次重试（`:59-62`），另有 `resetOpencodeTlsSession()` 主动丢弃（`:66-71`）。
   - Docker 侧为此把 `wreq-js`/`playwright-core` **预打进 runner 镜像**，注释「so OpenCode Free resolves without runtime downloads」（v0.5.112 CHANGELOG，#5245）。

   **⭐ 这一条直接命中我们的实测数据，必须记下**：我们测的是「关 WARP + key 走海外代理 127.0.0.1:7890 → 5/5 成功；直连 → 上游 403 not available in your country」。9router 的结论是**上游区分的是 TLS 指纹，不是来源 IP**。我们的引擎是 Go，`crypto/tls` 的 ClientHello 既不是 Node/OpenSSL 也不是 Bun/BoringSSL —— 于是我们的「代理成功」有两种可能解释，目前无法区分：
   - (a) 确实只是**地理封锁**，代理换了出口就过；
   - (b) 代理链路（如 mihomo/sing-box 的 TLS 终结与重建）**顺带换掉了 ClientHello**，让上游认成了别的客户端。
   → 若是 (b)，那「开 WARP 就全废」可能**不只是出口池烧光的问题，还叠加了 TLS 门**：WARP 出口直连 opencode.ai 时保留 Go 原生指纹 → 429/403；而 7890 那条路径恰好改写了指纹 → 通过。**这是一个可用 30 分钟验证的实验**（见 §8.2 附表），且能解释「为什么同一个 key 走 A 路通、走 B 路全废」这种看起来矛盾的现象。Go 侧对应手段是 `refraction-networking/utls`（我们仓库现在**完全没有**任何 ClientHello/指纹相关代码：`grep utls|ClientHello|CipherSuites|JA3|JA4` → 无匹配）。

### 1.4 session 维度 = 上游配额维度（我们目前缺的认知）

`open-sse/executors/opencode.js:109-154` —— 这段注释是本次调研对我们痛点最直接的洞察：

```js
// Upstream free-tier quota is accounted per session. Minting a fresh
// x-opencode-session on every request burns through it and surfaces as
// 429 FreeUsageLimitError with growing reset-after delays, while the real
// CLI reuses one long-lived canonical session per conversation. Mirror
// that: one stable canonical session per downstream identity, evicted
// after MEMORY_CONFIG.sessionTtlMs like the other session stores.
const stableOpencodeSessions = new Map();
const MAX_STABLE_SESSIONS = 1000;
```

- **上游免费配额是按 `x-opencode-session` 记账的**，不是按 IP、不是按 key。每请求换新 session id = 每次都在给一个全新身份申请免费额度 → 快速打满 → 429 且 `reset after` 越来越长。
- 对应修复提交：`0c6ab4f9 fix(opencode): reuse one stable upstream session per identity to stop 429s` —— **说明上游确实因为「session 乱生成」造成过 429 雪崩，他们专门修过一次**。
- 身份键 `identityKey()`（`:127-137`）优先级：`connectionId` → 下游 `Authorization`/`x-api-key` 的 sha256 前 32 位 → `"opencode:default"`。**即「我们该按什么维度分桶」它给了可抄的答案**。
- 会话 hint 探测 `bodyHasSessionHints()`（`:156-186`）：`session_id` / `conversation_id` / `prompt_cache_key` / `metadata.user_id` / `request.sessionId`，或历史中 assistant 文本累计 ≥50 字符 → 视为同一会话，复用 session。
- 决策顺序 `resolveOpencodeSession()`（`:202-234`）：客户端原生合法 session → 客户端 session 做确定性翻译 → sessionManager 解析 → 最后才 `stableSessionId()`。
- 确定性翻译 `translateSessionId()`（`:73-89`）：任意客户端会话 id → sha256 → 合法形状 `ses_...`，**同一输入永远同一 id**（会话可复现，且不违反 counter 约束）。
- LRU + TTL 淘汰（`:117-125`、`:149-151`）：`MAX_STABLE_SESSIONS = 1000` 上限，超出淘汰最旧；后台 `setInterval` 按 `MEMORY_CONFIG.sessionTtlMs` 清理，`unref()` 不阻止进程退出。

### 1.5 免费模型名单从上游实时拉取

`open-sse/providers/registry/opencode.js:48`
```js
modelsFetcher: { url: "https://opencode.ai/zen/v1/models", type: "opencode-free" },
passthroughModels: true,
```
zen registry 同样有（`opencode-zen.js:129`），且注释记了「live /zen/v1/models, 2026-09-18: 71 ids」（`:35-36`）。
→ 免费模型名单**不硬编码、走实时抓取**，上游改名/下线不需要发版。registry 里同时保留一份静态清单（`opencode-zen.js:37-120`，含免费档 `*-free` 后缀 9 个：`deepseek-v4-flash-free`、`mimo-v2.6-flash-free`、`mimo-v2.5-free`、`ling-3.0-flash-fin-free`、`nemotron-3-ultra-free`、`nemotron-3.5-lightning-free`、`muse-spark-1.3-contributor-free`、`muse-spark-1.2-contributor-free`、`jev-1.13-free`）作为实时抓取失败时的兜底。

### 1.6 每个模型声明自己支持的 wire format（避免打错 endpoint）

`opencode-zen.js:30-34` 一个 provider 挂 3 条 transports：
```js
transports: [
  { format: "openai",            baseUrl: ".../zen/v1/chat/completions", auth: {...} },
  { format: "claude",            baseUrl: ".../zen/v1/messages",         auth: {...} },
  { format: "openai-responses",  baseUrl: ".../zen/v1/responses",        auth: {...} },
],
```
配合每个模型的 `supportedFormats`（`:39`、`:52`、`:60`），选路时按「客户端 wire format ∩ 模型支持」挑 transport（决策逻辑 `open-sse/handlers/chatCore.js:89-110`）。
→ 免费模型清单变化时，**「哪个模型能走哪个 endpoint」是数据而非代码**：`opencode.js:34-38` 用显式 `targetFormat` 标注例外（`muse-spark-*-contributor-free` → responses、`union-alpha` → claude），`:30-31` 用 `quirks.forceAutoToolChoiceModels` 标注「只有这个模型必须 `tool_choice:"auto"`」（`opencode.js:344-347` 执行）。

---

## 2. 通道选择与降级顺序（provider → account/key → proxy → model）

### 2.1 完整调用链（从 HTTP 入口到一次上游请求）

```
POST /v1/chat/completions
  → src/sse/handlers/chat.js:35            handleChat()
      :105  getComboModels(modelStr)        ← 是不是 combo（多源候选）
      :114  comboStrategy === "fusion"      ← 扇出+裁判
      :136  handleComboChat(...)            ← 逐源降级循环
      :152  augmentModelsWithCapacityAdapter(...)  ← 能力不足时插入备用源
      :169  handleSingleModelChat(...)      ← 单模型：进入账号级降级
  → src/sse/handlers/chat.js:246           while(true) 账号轮转循环
      :247  getProviderCredentials(provider, excludeSet, model, ...)
  → src/sse/services/auth.js:28            getProviderCredentials()  ← 通道选择核心
      :47   FREE_PROVIDERS[pid].noAuth → 合成虚拟 connection "noauth"
      :87   filter 掉 modelLock / exclude / codex enabledModels / Antigravity 配额耗尽
      :115  全被过滤 → { allRateLimited:true, retryAfter }
      :146  strategy: round-robin | fill-first（默认）
      :202  resolveConnectionProxyConfig()  ← 代理在这一层绑定，不是独立降级维度
  → open-sse/handlers/chatCore.js:63       handleChatCore()  ← 翻译 + 执行 + 401/403 刷新
  → open-sse/executors/<provider>.js       真正发请求
```

### 2.2 一条通道出错后，下一步走哪里（**这是最该抄的部分**）

| 阶段 | 位置 | 行为 |
|---|---|---|
| ① 上游 401/403 | `open-sse/handlers/chatCore.js:437-482` | **先不换账号**，先 `refreshWithRetry(..., 3, log)` 刷 token，刷成功就地重放同一请求（`:458-469`），成功则直接用重放结果。即「一次免费的自我修复机会」优先于降级。 |
| ② 仍失败 / 其它非 2xx | `chatCore.js:485-507` | `parseUpstreamError()` 抽出 `{statusCode, message, resetsAtMs}`，`createErrorResult(statusCode, errMsg, resetsAtMs, upstreamResponseHeaders(...))` 返回给上层；**注意 `resetsAtMs` 一路透传**（`:506`），精确冷却时间不被丢弃。 |
| ③ 记账 + 决定是否换账号 | `src/sse/handlers/chat.js:340-351` | `markAccountUnavailable(connectionId, status, error, provider, model, resetsAtMs)` → 返回 `shouldFallback`。true 则 `excludeConnectionIds.add(...)`、记住 `lastError/lastStatus/lastHeaders`、`continue` 回 `:247` 重新选下一个账号。 |
| ④ 账号用尽 | `chat.js:250-263` | 见 §4 兜底语义。 |
| ⑤ 模型层降级 | `open-sse/services/combo.js:340-419` | 每个 combo 成员都是一次完整「账号级降级」调用（`handleSingleModel` 内部又跑一遍 ②③④）。全成员失败 → 503。 |
| ⑥ 代理层 | `open-sse/utils/proxyFetch.js:377-410` | 代理请求异常 → 若 `strictProxy===true` **抛错，绝不静默直连**（`:382-384`、`:400-406`）；否则 warn 后降级直连（`:385-386`）。 |

关键点：**它的降级顺序是「先自救（刷 token）→ 再换账号 → 再换模型 → 代理只在最外层决定成败」**，而我们目前遇到的是「WARP 出口不可用」直接短路成 503，中间一层都没有。

### 2.3 账号选择策略（`src/sse/services/auth.js`）

- **`fill-first`（默认，`:197-200`）**：`connection = availableConnections[0]`，依赖 `getProviderConnections` 已按 `priority` 升序排好。→ 优先把高优通道打满，而不是均摊。
- **`round-robin` + 粘性（`:158-196`）**：先按 `lastUsedAt` 找当前候选，若 `consecutiveUseCount < stickyRoundRobinLimit`（默认 3，`:159`）就继续用它；否则切到「最久未用」的那个。计数每次 `updateProviderConnection` **持久化到 DB**（`:176-179`、`:192-195`），不是进程内存 —— 重启不丢轮转位。
- **`preferredConnectionId` 钉住（`:150-155`）**：会话可粘在同一连接（`chat.js:247` 传参）。
- **并发保护（`auth.js:10`、`:35-38`、`:232-234`）**：一个 `selectionMutex` Promise 链，把整个「读连接表→筛选→写 lastUsedAt」串行化，防两个请求同时选中同一账号。这是多路复用选路里最容易被忽略的一步。

### 2.4 provider 层的 `priority` 是静态字段

每个 registry 条目带 `priority`（如 `opencode` = 40、`opencode-zen` = 205、`openrouter` = 10、`kiro` = 10），装配进 UI 常量：`src/shared/constants/providers.js:27`。**注意它是「展示/排序用的静态优先级」，不是动态健康分** —— 9router 里没有「按健康分实时排序 provider」的机制（这条对 Q5 的判断很重要）。

### 2.5 我们最该对齐的一条：noAuth 免费 lane 也要走同一套 filter

`auth.js:47-72` —— 匿名 free provider 走的是**提前 return 一个虚拟 connection**，`id: "noauth"`，并且：
- 照样给它绑代理池（`:52-57` `pickProxyPoolId(poolIds, strategy, providerId)`，策略 `round-robin`/`random`/`none`，见 `src/lib/network/connectionProxy.js:18-34`）
- 但 **`markAccountUnavailable` 对 `connectionId === "noauth"` 直接短路返回 `shouldFallback:false`**（`auth.js:248`），`clearAccountError` 同样短路（`:307`）。

→ 也就是说：**9router 的匿名 lane 自己也没有冷却账本**（它把记账放在「连接」维度，而匿名 lane 只有一个虚拟连接，没有可记账的东西）。它靠的是「上游 429 时用 combo 换模型 + 用 proxyPool 换出口」来兜，而不是给 `noauth` 记冷却。
**这是我们直接能受益的结论**：我们 WARP 池的处境和它一样——出口池不是「账号」，所以照搬它的账本还不够，必须**把「出口/代理」升格成一个可冷却的键**（它只在 `connectionProxyPoolId` 这一层记，池内不做逐个健康记账，`connectionProxy.js:22-27` 纯 round-robin，不 skip 坏的）。这是它比我们强的地方之外的**它自己的短板**。

---

## 3. 冷却 / 重试账本：记在什么维度，时长与恢复

### 3.1 记账维度 = **connection × model 二维**（不是 key、不是 IP）

`open-sse/services/accountFallback.js:120-129`
```js
/** Prefix for model lock flat fields on connection record */
export const MODEL_LOCK_PREFIX = "modelLock_";
/** Special key used when no model is known (account-level lock) */
export const MODEL_LOCK_ALL = `${MODEL_LOCK_PREFIX}__all`;
/** Build the flat field key for a model lock */
export function getModelLockKey(model) {
  return model ? `${MODEL_LOCK_PREFIX}${model}` : MODEL_LOCK_ALL;
}
```
落库为 connection 记录上的**扁平字段** `modelLock_<modelId>` → ISO 到期时间（`buildModelLockUpdate()` `:162-165`），读取用 `isModelLockActive(connection, model)`（`:135-140`，同时兜 `modelLock___all` 账号级锁）。

设计取舍（注释写在 `auth.js:238-239`）：
> All errors (429, 401, 5xx, etc.) lock per model, not per account.

→ **一个模型 429 只锁「这个连接的这个模型」，同一连接别的模型照常可用**。这正是「一条通道废了就全废」的反面。

配套：
- `getEarliestModelLockUntil(connection)`（`:146-157`）扫所有 `modelLock_*` 取最早到期，供 UI 显示冷却剩余。
- `buildClearModelLocksUpdate(connection)`（`:171-176`）批量清空。
- 检查点：`auth.js:89` `if (isModelLockActive(c, model)) return false;` —— 选路时就地过滤，成本一次日期比较。

### 3.2 冷却时长怎么算：表驱动规则 + 指数退避

`open-sse/config/errorConfig.js:32-36`
```js
export const BACKOFF_CONFIG = { base: 2000, max: 5 * 60 * 1000, maxLevel: 15 };
```
`open-sse/services/accountFallback.js:9-13`
```js
export function getQuotaCooldown(backoffLevel = 0) {
  const level = Math.max(0, backoffLevel - 1);
  const cooldown = BACKOFF_CONFIG.base * Math.pow(2, level);
  return Math.min(cooldown, BACKOFF_CONFIG.max);
}
```
→ 2s、4s、8s …… 封顶 5 分钟；`backoffLevel` 存在 connection 上（`auth.js:251`、`:283`），**成功即归零**（`auth.js:334-341`）。

规则表（`errorConfig.js:59-77`，命中顺序 = 优先级，文本规则先于状态码规则）：
```js
export const ERROR_RULES = [
  { provider: "codex", text: "model is not supported when using codex with a chatgpt account", cooldownMs: MAX_RATE_LIMIT_COOLDOWN_MS },
  { text: "no credentials",            cooldownMs: COOLDOWN.long },   // 2 min
  { text: "request not allowed",       cooldownMs: COOLDOWN.short },  // 5 s
  { text: "improperly formed request", cooldownMs: COOLDOWN.long },
  { text: "rate limit",               backoff: true },
  { text: "too many requests",        backoff: true },
  { text: "quota exceeded",           backoff: true },
  { text: "capacity",                 backoff: true },
  { text: "overloaded",               backoff: true },
  { status: 401, cooldownMs: COOLDOWN.long },
  { status: 402, cooldownMs: COOLDOWN.long },
  { status: 403, cooldownMs: COOLDOWN.long },
  { status: 404, cooldownMs: COOLDOWN.long },
  { status: 429, backoff: true },
];
```
其它常量：`TRANSIENT_COOLDOWN_MS = 30_000`（`:39`，未匹配任何规则的兜底冷却）、`MAX_RATE_LIMIT_COOLDOWN_MS = 30 * 60_000`（`:42`，把上游报的 5-6 小时硬砍到 30 分钟）。

匹配器 `checkFallbackError(status, errorText, backoffLevel, provider)`（`accountFallback.js:23-65`）——纯函数，规则表可整体搬走。

### 3.3 上游报了精确 resetAt 时，以它为准

`src/sse/services/auth.js:253-271`
```js
if (githubResetAtMs) {                      // GitHub 月度上限：算到下一个 UTC 月 1 号
  cooldownMs = githubResetAtMs - Date.now(); newBackoffLevel = 0;
} else if (resetsAtMs && resetsAtMs > Date.now()) {
  // Antigravity quota API provides exact per-model resetAt. Do not truncate it.
  cooldownMs = resolveProviderId(provider) === "antigravity"
    ? resetsAtMs - Date.now()
    : Math.min(resetsAtMs - Date.now(), MAX_RATE_LIMIT_COOLDOWN_MS);
  newBackoffLevel = 0;
} else {
  ({ shouldFallback, cooldownMs, newBackoffLevel } = checkFallbackError(...));
}
```
`resetsAtMs` 来源：`parseUpstreamError` 让 executor 自己解析（`open-sse/utils/error.js:68-76`），provider 专属字段（codex `usage_limit_reached.reset_at`、codebuddy `6004`、antigravity 配额 API）都能抽出来。→ **三层优先级：上游精确 resetAt > 表驱动规则 > 30s 兜底。**

### 3.4 恢复策略：成功即清 + 惰性清扫（无定时器）

`src/sse/services/auth.js:306-345` `clearAccountError(connectionId, currentConnection, model)`：
```js
const keysToClear = allLockKeys.filter(k => {
  if (model && k === `modelLock_${model}`) return true; // succeeded model
  if (model && k === "modelLock___all") return true;    // account-level lock
  const expiry = conn[k];
  return expiry && new Date(expiry).getTime() <= now;   // expired  ← 顺手清
});
// Only reset error state if no active locks remain
if (remainingActiveLocks.length === 0) {
  Object.assign(clearObj, { testStatus:"active", lastError:null, errorCode:null, lastErrorAt:null, backoffLevel:0 });
}
```
挂钩点：`src/sse/handlers/chat.js:318-322` `onRequestSuccess` → `clearAccountError(...)` + `clearAntigravityStrikes(...)`。
→ **没有后台清扫协程**：过期锁靠「下次成功时」和「下次扫到时」被动回收。这点对我们很实用——省掉一个 goroutine 和一堆并发问题。

### 3.5 防误伤：4xx 不该冷却一个健康连接

`accountFallback.js:49-61`（整段注释就是事故复盘，直接可读）
```js
// Request-scoped client errors that matched no rule above: a 400 caused by the
// request itself (context overflow, malformed body, unsupported parameter) says
// nothing about the credential, so cooling the account down only removes a
// healthy connection from rotation. With a single connection it is worse: every
// later request in the window fails with a copy of this very error
// ("all 1 accounts locked for <model> | lastError=[400]: ..."), which hides the
// real cause from the caller and makes unrelated sessions look like they hit the
// same limit. Hand the upstream error back for this request instead.
if (status >= 400 && status < 500 && status !== 401 && status !== 402 && status !== 403 && status !== 429) {
  return { shouldFallback: false, cooldownMs: 0 };
}
```
对应回归测试：`tests/unit/account-fallback-4xx.test.js`（`:11-33`，断言 400 无规则命中 → `shouldFallback:false`；400 + "rate limit reached" → true；503 → true/30s）。
另有一条 provider 专属反例：`tests/unit/codex-extended-context.mjs:17-18` 断言 codex 的特定 400 文案 **要** 触发降级 —— 所以例外走 `ERROR_RULES` 里的 `provider:` 字段（`errorConfig.js:61`），而不是把通用逻辑写松。

---

## 4. 「所有通道都不可用」时的兜底与错误语义

### 4.1 三种「没通道了」的语义被明确区分开

`src/sse/handlers/chat.js:246-263` —— 这是我们 503 那种情况的完整语义化版本：

```js
while (true) {
  const credentials = await getProviderCredentials(provider, excludeConnectionIds, model, {...});   // :247

  if (!credentials || credentials.allRateLimited) {
    if (credentials?.allRateLimited) {                     // (A) 有账号，但全在冷却里
      const errorMsg = lastError || credentials.lastError || "Unavailable";
      const status = HTTP_STATUS.SERVICE_UNAVAILABLE;      // 503
      log.warn("CHAT", `[${provider}/${model}] ${errorMsg} (${credentials.retryAfterHuman})`);
      return unavailableResponse(status, `[${provider}/${model}] ${errorMsg}`,
                                 credentials.retryAfter, credentials.retryAfterHuman, lastHeaders);
    }
    if (excludeConnectionIds.size === 0) {                 // (B) 压根没配这个 provider 的账号
      log.warn("AUTH", `No active credentials for provider: ${provider}`);
      return errorResponse(HTTP_STATUS.NOT_FOUND, `No active credentials for provider: ${provider}`);  // 404
    }
    log.warn("CHAT", "No more accounts available", { provider });   // (C) 试完了所有排除项
    return errorResponse(lastStatus || HTTP_STATUS.SERVICE_UNAVAILABLE, lastError || "All accounts unavailable", lastHeaders);
  }
  ...
}
```

三条分支的取舍非常讲究（值得逐条对照我们的 503）：

- **(A) `allRateLimited`**：来自 `auth.js:115-138` —— 全在锁里时**回传最早到期时间** `retryAfter` + 人话 `retryAfterHuman`（"reset after 2m 30s"，格式化在 `accountFallback.js:105-118`）+ `lastError` + `lastErrorCode`。客户端拿到 **`retry-after` 秒数头**（`open-sse/utils/error.js:117-132`，故意小写 `retry-after` 防重复头，`:118` 用 `Math.max(ceil, 1)` 保证 ≥1s）。→ 语义 = 「可重试，且告诉你什么时候重试」。
- **(B) 没配凭据 → 404**，明确「这不是临时故障，是你没配」。
- **(C) 试完了 → 原样回吐最后一次真实 status/error**，而不是自造 503。→ **保留上游真实状态码**，客户端能区分 429 与 403。

**对照我们的问题**：`WARP egress is unavailable: WARP pool has no ready egress` 是 503，但既没带 `retry-after`，也没区分「出口池空了（可恢复、等一会儿就有）」和「免费额度真烧光了（要等到重置点）」。这两者在客户端重试策略上完全不同，现在被压成同一个 503。

combo 层同理（`open-sse/services/combo.js:421-439`）：

```js
// All models failed
// Use 503 (Service Unavailable) rather than 406 (Not Acceptable) — 406 implies
// the request itself is invalid, but here the providers are simply unavailable
// or have no active credentials. 503 is more accurate and retryable by clients.
const allDisabled = lastError && lastError.toLowerCase().includes("no credentials");
const status = allDisabled ? 503 : (lastStatus || 503);
if (earliestRetryAfter) {                                  // :429-433 跨所有候选取最早 reset
  const retryHuman = formatRetryAfter(earliestRetryAfter);
  return unavailableResponse(status, msg, earliestRetryAfter, retryHuman);
}
```

→ `earliestRetryAfter` 是**跨所有 combo 成员收集的最小值**（`:382-385`），所以最终 503 会告诉客户端「最早 X 秒后有通道回来」，而不是笼统一句 unavailable。**这一段几乎可以直译到我们的 Go 引擎。**

对应修复提交：`15687d19 fix(chat,docker): return 503 for rate-limited providers and bundle node-machine-id`。

### 4.2 主动健康探测：四类，各有各的节流

| 机制 | 位置 | 触发 | 节流与要点 |
|---|---|---|---|
| **凭据有效性后台刷新** | `src/sse/services/backgroundTokenRefresh.js:90-139`（tick）、`:146-170`（启动） | `setInterval` 默认 5 分钟（`:10`），启动后 10 秒先跑一次（`:11`、`:163`） | 提前量 `max(BACKGROUND_REFRESH_LEAD_MS=30min, provider 自身 lead)`（`:9`、`:62-68`）；`tickRunning` 互斥（`:91-92`）；逐连接串行 + 抖动延时（`:103-130`：Google 敏感 provider 12s+0~4s jitter，其它 1.5s+0.2s）；**全 fail-open**（`:115-121`、`:132-138`，单个失败/整个 tick 失败都不会杀死定时器） |
| **连接逐个测试** | `src/app/api/providers/[id]/test/route.js`、UI `src/app/(dashboard)/dashboard/providers/[id]/page.js:700-790`（"Test Connection One-by-One"）、`/api/providers/test-batch` | 人工点 / 按组批量 | 结果写 `testStatus`，UI 实时汇总 passed/failed（`page.js:1652-1665`） |
| **代理池连通性测试** | `src/app/api/proxy-pools/[id]/test/route.js:37-69` → `src/lib/network/proxyTest.js:28-91` | 人工点 / 批量 | **`HEAD https://google.com/` + `ProxyAgent` + 默认 8s 超时**（`:3-4`），并在 `finally` 强制 `dispatcher.close()`（`:84-89`）防 socket 泄漏；relay 型走 `x-relay-target` 探法（`route.js:6-34`）。测完 `updateProxyPool({ testStatus, lastTestedAt, lastError, isActive: result.ok })`（`route.js:51-56`）—— **坏出口自动 `isActive:false` 被摘除** |
| **配额预热 auto-ping** | `src/shared/services/quotaAutoPing.js`（345 行）+ 常量 `src/shared/constants/config.js:65-86` | 「配额重置后立即打一发极小请求」+ 固定时刻表 | `wasPingedRecently()`（`:73-77`）按 `lastPingAt` 去抖；`normalizeResetKey` 分钟对齐（`:41-45`）；`resetAtDriftMs` 判 reset 点漂移（`:88-90`） |

> auto-ping 的用途细节（`config.js:86` 注释）：`gpt-5.6-terra` 被指定为「给 Codex 5 小时窗口 auto-ping 的轻量模型」——**主动用一个极便宜的模型保持配额窗口「在计时」**。对「免费 lane 长期空闲被上游降权」这类问题是个可借鉴的招。

### 4.3 自动摘除：三级熔断，其中 strike breaker 最值得抄

1. **被动摘除** = `modelLock_*`（§3.1），到期自愈。
2. **Antigravity 实时配额预过滤**（`src/sse/services/auth.js:83-101`）：选路阶段就查 RAM 配额缓存，`remainingPercentage <= 0 && resetAt > now` → 直接跳过该连接，并打日志 `AG_QUOTA ... CACHE_BLOCK <model> — skip upstream until <resetAt>`（`:98`）。→ **根本不发请求就知道它不行**，省一整轮 RTT，也避免把重试压力打到上游。
3. **Strike breaker（连续失败熔断）**（`src/sse/services/antigravityQuota.js:20-29`、`:253-284`）：
   ```js
   // Google's quota API can report remaining quota while generation endpoints keep
   // returning 429 (sprint/weekly dual-pool mismatch). After STRIKE_THRESHOLD 429s
   // within the window for the same connection+model, treat the optimistic quota
   // reading as untrusted and cache-block that pair instead of retry-storming upstream.
   const STRIKE_WINDOW_MS = 60_000; const STRIKE_THRESHOLD = 3; const STRIKE_BLOCK_MS = 15 * 60_000;
   const strikeCounts = new Map(); // "connectionId|model" → { count, windowStart }
   const strikeBlocks = new Map(); // "connectionId|model" → blockedUntil ms
   ```
   - **窗口锚定在第一次 strike**（`:264-266` 注释明确：三次 429 必须都落在「第一次之后的 60s 内」，而不是彼此间隔 60s 内 → 防滑动窗口被无限续期）。
   - 达阈值 → 合成一条 `{remainingPercentage: 0, resetAt: blockedUntil}` **写进同一份 quotaCache**（`:277-280`），让预过滤器用**同一条代码路径**跳过它；`:31-50` `applyActiveStrikeBlocks` 保证每次上游配额刷新后 strike 块不被「乐观读数」复活（`:229-231` 注释就是这个意思）。
   - 恢复：`:152-175` `clearAntigravityStrikes` —— 成功一次即清计数，且**只删自己合成的那条缓存项**（`resetAt === 我们的 block deadline` 才删），真上游 0% 读数保留（`:165-173`）。
   - 对应提交：`ac98dd9d fix(antigravity): strike-break optimistic quota readings that keep 429ing`、`1a3db1ef feat(antigravity): quota-aware routing with reset-aware fallback`、`5798b308 fix(antigravity): drop requestType "agent" to avoid false 429 RESOURCE_EXHAUSTED`。

> **「上游读数不可信时用 strike 熔断，并把熔断状态伪装成正常读数写回同一个缓存，让预过滤器零改动地复用」**——我认为这是整份调研里对我们第二有价值的单一机制（第一是 §3.1 的二维账本）。我们的 WARP 池正是「乐观读数」的典型：池子报 `ready`，实际一发包就废；只信池子自己的读数就会 100% 503。

### 4.4 可用性面板：一个只读 API + UI 倒计时 + 手动解锁

- **`GET /api/models/availability`**（`src/app/api/models/availability/route.js:22-64`）：扫全部 connection 的 `modelLock_*`，输出 `{provider, model, status:"cooldown"|"unavailable", until, connectionId, connectionName, lastError}` 列表 + `unavailableCount`（`:55`）。
- **`POST /api/models/availability`**（`:66-103`）：`action:"clearCooldown"` → 按 provider+model 批量清锁，并在没有其他锁时把 `testStatus` 复位 `active`、`backoffLevel` 归零（`:81-92`）。→ **运维手动干预入口**：冷却判断失误时不用干等，也不用重启。
- UI 状态显示逻辑（多处一致）：`src/app/(dashboard)/dashboard/providers/components/ConnectionsCard.js:88`、`providers/[id]/ConnectionRow.js:161`
  ```js
  const effectiveStatus = connection.testStatus === "unavailable" && !isCooldown ? "active" : connection.testStatus;
  ```
  → **`testStatus=unavailable` 但已经没有活着的锁 → 显示 active**，防「状态字段没清干净导致面板长期红着」。`ConnectionsCard.js:121` / `ConnectionRow.js:224` 挂 `<CooldownTimer until={modelLockUntil} />` 显示倒计时。
- 另有极简存活探针 `src/app/api/health/route.js`（只返回 `{ok:true}`，**不聚合通道健康**）。→ 我们若想要「一个 URL 看出还有几条 lane 可用」，该抄的是 `/api/models/availability` 而不是 `/api/health`。

### 4.5 它没有的东西（避免我们高估）

- **没有「按健康分实时排序 provider」**：`priority` 是静态字段（§2.4）；combo 的排序依据是「能力匹配」（§5）+ 人工顺序，不是成功率 EWMA / 滑动窗口打分。
- **没有出口级（IP / 代理）健康账本**：代理池只有 `isActive` + `testStatus`，靠人工或批量 `testProxyUrl` 打标；池内轮换 `pickProxyPoolId`（`src/lib/network/connectionProxy.js:18-34`）是纯 round-robin / random，**不会跳过刚失败的出口**，也不记「这个出口刚被 403」。→ 我们 WARP 池的核心痛点，9router **同样没有现成答案**，只能自己补（见 §5.4）。
- **没有匿名 lane 的冷却账本**：`connectionId === "noauth"` 时 `markAccountUnavailable` / `clearAccountError` 双双短路（`auth.js:248`、`:307`）——匿名通道没有可记账的实体身份。

---

## 5. 多 provider 聚合思路能否套到我们身上（对照 `core/router/router.go`）

### 5.1 它的「同一模型多源候选」= combo

combo 就是**用户自定义的「一个逻辑模型名 → N 个 provider/model 候选」**，存在 SQLite（`src/lib/db/repos/combosRepo.js`，API `src/app/api/combos/route.js`）。请求里 model 名不带 `/` 且命中 combo 名即触发：

- 识别：`src/sse/services/model.js:85-94` `getComboModels()`；`chat.js:105` 入口分流
- 执行：`open-sse/services/combo.js:320-440` `handleComboChat()`

降级循环核心（`combo.js:340-419`），逐条值得看：
```js
for (let i = 0; i < rotatedModels.length; i++) {
  const result = await handleSingleModel(body, modelStr);      // :345 每个候选内部又跑一遍账号级降级
  if (result.ok) {
    const isLastModel = i === rotatedModels.length - 1;
    if (!isLastModel) {                                        // :349-351
      const peekBody = await result.clone().json();
      if (isZeroContentResponse(peekBody)) {                   // :355 ← 200 但空内容也继续降级
        log.warn("COMBO", `Model ${modelStr} returned HTTP 200 with zero usable content, falling back to next model`);
        lastError = "Zero usable content"; lastStatus = 200; continue;
      }
    }
    return result;
  }
  ...
  const { shouldFallback, cooldownMs } = checkFallbackError(result.status, errorText);   // :393 与账号层同一张规则表
  if (!shouldFallback) return result;                          // :395-398 请求本身的问题 → 不换源
  if (cooldownMs && cooldownMs > 0 && cooldownMs <= 5000 &&
      (result.status === 503 || result.status === 502 || result.status === 504)) {       // :403-407
    log.info("COMBO", `Model ${modelStr} transient ${result.status}, waiting ${cooldownMs}ms before next`);
    await new Promise(r => setTimeout(r, cooldownMs));         // ← 瞬时 5xx 先等一会儿再降级，别秒判死刑
  }
} catch (error) { lastError = error.message; ... }             // :413-418 异常也继续降级
```

三个细节是我们没有的：
1. **`isZeroContentResponse()`（`combo.js:273-306`）**：HTTP 200 但 `choices` 空 / 无 content+tool_calls+reasoning 且 `finish_reason` 是 `in_progress`/`incomplete` → **判定为不可用，继续降级**。Anthropic 形状同样判（`:297-303`）。对应提交 `8f4e3800 fix(combo): fallback when model returns HTTP 200 with zero usable content (#4750)`。→ 对「上游返回空响应骗过 failover」这类故障是刚需。
2. **瞬时 5xx 先等 ≤5s 再换源**（`:403-407`）：避免一次抖动把整条链烧穿。
3. **降级判据和账号层共用一张 `ERROR_RULES` 表**（`:393`）：不会出现「账号层认为该换、模型层认为不该换」的策略分裂。

### 5.2 「排序」用的不是健康分，而是**能力分档**

`combo.js:63-82` `reorderByCapabilities(models, required)`：
```js
// Tier 0: satisfies all hard + all soft. Tier 1: all hard only. Tier 2: rest.
const tierOf = (m) => {
  const caps = getCapabilitiesForModel(provider, model);
  if (!hard.every((c) => caps[c] === true)) return 2;
  return soft.every((c) => caps[c] === true) ? 0 : 1;
};
// Stable sort by tier (Array.prototype.sort is stable in modern engines).
return models.map((m, i) => ({ m, i, t: tierOf(m) })).sort((a, b) => a.t - b.t || a.i - b.i).map(x => x.m);
```
- `HARD_CAPS = vision/pdf/audioInput/videoInput`（`:12`）——**缺硬能力会丢请求数据**，必须优先；软能力（search）只降级功能。
- 注释明确「**never drops a model (fallback intact)**」（`:61`）：只重排，不删除候选。
- 稳定排序保序 → 用户手设顺序仍是 tie-break。
- 能力来源：`open-sse/providers/capabilities.js` + `visionPatterns.js`（数据驱动）。

### 5.3 capacity adapter：**按能力配置的「全局备用源池」**（这条我们完全没有）

`open-sse/services/capacityAdapter.js`（174 行，注释即设计说明）：
```js
// The pool models are appended as extra fallback candidates behind whatever models
// were already going to be tried ... reorderByCapabilities then floats a capable
// pool model to the front only when none of the original models can handle the request
// so this never overrides a combo that already has a member covering the capability.
```
- `:15` **默认兜底源就是 OpenCode 免费模型**：`const DEFAULT_FALLBACK_MODEL = "oc/mimo-v2.6-flash-free";` —— 我们的免费 lane，在它眼里是「全系统最后一道保险」。反过来说：我们应该把「多中转站」配置成这样一个池。
- `:93-101` `augmentModelsWithCapacityAdapter`：只有**原候选全都不满足硬能力时**才把池模型**插到最前**（`[...pool, ...models]`，原候选仍作降级尾部）。
- `:118-157` `stripHistoryForContext`：切到上下文更小的备用源时，**保留 system + 前 6 条 + 当前用户轮，砍中间**（`HEAD_KEEP=6` `:104`，预算 = 上下文窗口 × 0.8，`:141`）→ 「换个小模型」不至于超窗直接失败。
- `:161-174` `withCapacityAdapterStripping`：把这个裁剪包装进降级回调，只对池内模型生效。
- 接线点：`src/sse/handlers/chat.js:111`（combo 路径）、`:152-167`（**单模型路径也会走 adapter** —— 即使用户没配 combo）。
- 配置形态：`settings.capacityAdapter[cap] = { enabled, roundRobin, models[] }`，兼容旧的裸数组（`:20-32`）；开关即生效，`models` 为空时自动填默认值（`:36-42`「the toggle is never a no-op」）。
- 对应提交：`6886915f feat(capacity-adapter): default vision fallback to mimo-v2.6-flash-free`、`8e59093d feat(combos): default-enable vision/audio adapter with mimo fallback`。

### 5.4 与我们现状的精确对照

| 维度 | 我们（`core/router/router.go` + `auto_model.go` + `upstream.go`） | 9router | 差评 |
|---|---|---|---|
| 同模型多源候选 | ✅ `Router.candidates()`（`router.go:54-84`）：modelMap 精确映射 → 聚合器倒排 → 前缀直连 → defaultID | ✅ combo + capacity adapter | **我们已有，且厂商发现更自动**（倒排索引），这点不输它 |
| 候选排序 | ✅ **健康分**：`autoCand.Score = (Weight/10) × SR`（`auto_model.go:101`），SR 来自滑动窗口实测 `modelFeedbackStats()`（`:526-561`，键 = `model\x1f实例addr`，窗口修剪 + 每键封顶） | ❌ 只有静态 `priority` + 能力分档 | **我们更强**。它没有成功率 EWMA；我们别为了抄它把自己的打分扔掉 |
| 降级触发判据 | ⚠️ `shouldSwitchVendor()`（`upstream.go:185-196`）：**只看 `v.ErrSemantics().Switchable` 里的状态码整数**，无文案匹配、无 provider 例外、无「未命中规则时默认怎么办」 | ✅ `checkFallbackError(status, errorText, backoffLevel, provider)` 一张表 + 兜底（`accountFallback.js:23-64`） | **它明显更强**：403 文案 `not available in your country` 这种，我们只能整码放行或整码禁用，它可按文案精确处置 |
| 空响应 200 | ⚠️ 协议层有 `finish_reason` 处理（`responses.go:140-149` 空 content 标记、`gateway_timeout.go:761/900` `sawFinish`、`core/protocol/*`），但**免费 lane 的 failover 循环 `callOpenCodeAPIStreamOnce`（`upstream.go:300-330`）只按 2xx 判成败**，没有「200 但零可用内容 → 换源」 | ✅ `isZeroContentResponse`（JSON）+ 新版 `emptyFailover.js` SSE peek | **缺在 failover 决策处**：协议转换层懂 finish_reason，降级层不懂，等于白查 |
| 失败后的冷却 | ⚠️ key/出口有冷却概念，但 WARP 池空是**直接 `break` 放弃整个 key tier**（`core/manager/freetier_warp_fallback.go:16-24` 引用的 `internal/gateway/upstream.go:1041-1045`） | ✅ 二维 `modelLock_<model>` + 三层冷却时长 | **缺**：一条 lane 废 → 整 tier 废，正是题目说的「一条通道废了就全废」 |
| 出口维度健康账本 | ❌ `usesWARP()` 只看「池对象是否存在」不看池里有无可用出口（同上 `:10-12`） | ❌ 同样没有（§4.5） | **两边都没有 → 必须自研**，但可以借它「strike breaker 把熔断伪装成正常读数写回同一缓存」的形状（§4.3） |
| 全废时的错误语义 | ⚠️ 503 + `Retry-After` 在白名单里能透传（`upstream.go:337`），但自造的 503 不带 retry-after；`httpStatusOr()` 把 0 码归一 502（`:358-362`） | ✅ 三分支语义 + 跨候选最早 reset 点 | **缺**，抄 §4.1 |
| 手动摘除/恢复 | ❓ 面板有「重试 WARP」（`freetier_warp_fallback.go:44`） | ✅ `POST /api/models/availability {action:"clearCooldown"}` 按 provider+model 批量解锁 | 值得加一个等价接口 |

### 5.5 明确判断

- **值得抄的（按性价比排序）**：
  1. `ERROR_RULES` + `checkFallbackError` 表驱动判据（替换 `shouldSwitchVendor` 的纯状态码白名单）——改动小、直接解决「403 地区封锁 vs 403 配额耗尽」分不开的问题。
  2. `modelLock_<model>` 二维账本 + 惰性清扫 + `clearAccountError` 的「只在无活锁时复位」——解决「一条 lane 废了整 tier 废」。
  3. `unavailableResponse` / combo 汇聚的「跨全部候选取最早 resetAt，503 带 `retry-after` + 人话」——客户端体验立刻变好。
  4. `isZeroContentResponse`（200 空内容也降级）。
  5. capacity adapter 那种「独立于 combo 的全局备用源池 + 只在原候选全不满足时插到最前 + 换小源时按上下文砍中间」——直接可映射成我们「OpenCode 免费模型不可用 → 切 vendors/custom 中转站」。
  6. strike breaker（乐观读数不可信时的熔断，且熔断状态复用正常读数的数据结构）。
- **不该抄 / 不该换的**：我们的健康分打分（`auto_model.go:94-102`）比它的静态 priority + 能力分档更先进，**保留**；我们的厂商倒排索引发现机制也更自动，**保留**。
- **它对我们最痛的那个问题（WARP 出口池没有健康账本）没有答案** —— 不要指望「换用 9router」就能解决，那部分必须自研。

### 5.6 ⚠️ 上游 master 已把这套逻辑再演进一层（本地 HEAD 看不到）

本地 HEAD 是 `87a74e1b / v0.5.104 (2026-10-15)`，`origin/master` 已到 `1339107e / v0.5.116 (2026-11-08)`（**本地落后 99 个提交**）。新版里 `checkFallbackError` 多了一个**独立于 `shouldFallback` 的返回字段**，这正是我们该抄的最终形态：

`origin/master:open-sse/services/accountFallback.js:16-23`（新版函数 doc）
```js
 * `shouldFallback` answers "is the CREDENTIAL at fault?" (account rotation).
 * `requestScoped`  answers "is the REQUEST itself what this member rejected?" (combo rotation).
 * They are different questions: a context overflow or a validation 400 means the
 * credential is healthy but this member cannot serve this request, so combo
 * rotation must still try the next member.
```
`:57-64` 未分类 4xx 现在返回 `{ shouldFallback: false, cooldownMs: 0, requestScoped: true }`（本地版没有 `requestScoped`）。

combo 侧（`origin/master:open-sse/services/combo.js`）：
- `:469` `let requestScopedResult = null;`
- `:580` `const { shouldFallback, cooldownMs, requestScoped } = checkFallbackError(result.status, errorText, 0, provider)` —— **注意它现在把 `provider` 也传进去**（本地版 `:393` 没传，provider 级例外规则在 combo 层失效，属于 bug 修复）
- `:582` `if (!shouldFallback && !requestScoped) return result;` —— 两者都为假才终止整条链
- `:594-599` `if (requestScoped) { log "rejected the request, trying next"; ...; if (!requestScopedResult) requestScopedResult = result; continue; }` —— **无冷却、无等待**（注释：`there is nothing to recover`）
- `:631-633` 全链跑完后：
  ```js
  if (requestScopedResult) {
    log.warn("COMBO", `No model served the request; returning the first request-scoped failure (${requestScopedResult.status})`);
    return requestScopedResult;   // 原样回吐第一个「请求级失败」
  }
  ```
  配套注释（`:625-630`）说明为什么必须这样：合成的 503 信封「**reaches the client as a 200 carrying an error object, which reads as an empty success, and it buries the member that named the real cause**」——即宁可回吐某个成员的真实 4xx，也不给一个聚合后的模糊 503。

  → 这一层区分（**「凭据坏了」 vs 「这个源服务不了这个请求」**）是我们 `shouldSwitchVendor()` 完全没有的维度，也是「一次 context overflow 把整条通道打进坏池」这类误伤的正解。相关 CHANGELOG 条目：
  - `Combos: advance past request-scoped 4xx rejections (validation, unsupported parameter, context overflow) instead of terminating combo (#5057)`
  - `CodeBuddy: align the #5024 backoff tests with the current request-scoped contract — checkFallbackError reports requestScoped: true for unclassified 4xx`

同理，200 空内容检测在新版从「只查 JSON」升级到「**先偷看 SSE 流再决定**」：`origin/master:open-sse/services/combo.js:10` 引入 `open-sse/services/emptyFailover.js` 的 `peekStreamForContent` / `reconstructStream` / `isNoRetryFinish`，`:481-546` 用 peek 结果决定是否换源；注释直接点名场景：
```js
// Success (2xx). A 2xx is necessary but NOT sufficient: Gemini aborts (e.g.
// MALFORMED_FUNCTION_CALL) still return 200 and stream empty content, which would
// otherwise be delivered as a successful empty answer with no failover.
```
并且 `:534-538` 有 `isNoRetryFinish` 白名单（某些 finish_reason 属于「重试也没用」类，直接原样返回）与「最后一个候选不再降级」的边界处理。→ 我们流式路径要抄的话，应抄这个**偷看+重建流**的形状（`emptyFailover.js`），而不是简单判空 JSON。

---

## 6. 运行与许可成本

### 6.1 许可证：**MIT**（对我们的 GPL 传染顾虑无障碍 —— 而且这条结论比想象中更重要）

`LICENSE`（21 行）第一行即 `MIT License`，`Copyright (c) 2024-2026 decolua and contributors`；`README.md:1558-1560` 亦写「MIT License - see LICENSE」。`package.json` 无 `license` 字段（该包 `"private": true`，名为 `9router-app`，npm 上发布的是 CLI 包 `9router`）。

**关键背景（本次调研顺带查实，直接影响「从哪儿抄」的决策）**：

| 组件 | 许可证 | 位置 / 证据 |
|---|---|---|
| 我们的 40080 免费层引擎 `opencode2api-win` | **GPL-3.0-only** | `third_party/opencode2api-win/LICENSE` + `SOURCE.txt`（上游 `github.com/wbi35772/opencode2api-win`，内嵌二进制基线 commit `9964407`「v1.4.6 + session-affinity 429 fix」）；`CHANGELOG.md:95` 亦记为「外部程序（GPL-3.0），随包内嵌、独立进程运行」 |
| 我们自己的这个仓库 | **All rights reserved（专有）** | 根 `LICENSE`；无开源许可证 |
| 9router | **MIT** | `LICENSE` |

推论（三条都很硬）：

1. **今天坏掉的那套「免费 lane 路由/冷却」逻辑，主体在 GPL-3.0 的 `opencode2api-win` 里**（`usesWARP()` 缺陷、key tier `break`、WARP 出口池与冷却账本，见 `core/manager/freetier_warp_fallback.go:8-24` 引用与 `CHANGELOG.md:1080` 记录的「不改引擎（GPL 隔离红线）」）。我们既有**法律约束**（不改引擎）也有**技术约束**（拿不到源码）去精修它。
2. **9router 是 MIT**：允许商用、允许闭源再分发、允许修改，唯一义务是保留版权声明（而且我们是「照其算法思路自研 Go 实现」，连衍生作品都算不上，风险更低）。→ **把 §8.2 那 8 项做在我们自己的 MIT-clean 路径上（`core/` + `upstream.go` + 管理器），法律上是安全的；反过来若去逆向/复刻 GPL 引擎内部实现则不安全。** 这实际上是本次调研对我们最大的合规价值：**9router 提供了一个「干净的重写参照」**。
3. **若长期目标是把「免费 lane 编排」从 GPL 引擎收回到我们手里**，9router 恰好证明这条路可走：它用 MIT 代码独立实现了我们依赖 GPL 引擎才有的全部能力（137 provider、多账号轮转、冷却、代理池、指纹伪装、可用性面板）。这是一个战略选项，不是本次必须做的事。

⚠️ 另一层风险不在许可证，而在**服务条款**：9router 的很多 lane 依赖逆向/伪装上游客户端（`Bearer public`、伪造 UA、fingerprint tools、TLS ClientHello 模仿、MITM）。这不是 license 问题，而是**任何一条 lane 都可能因上游改指纹一夜失效**（§7.2/§7.3 就是实录）。

### 6.2 运行时依赖：Next.js 全家桶 + 本地 SQLite，**不算轻**

`package.json`（68 行）：
- **框架**：`next ^16.1.6` + `react 19.2.4` + `react-dom` + `tailwindcss ^4` + `monaco-editor` + `recharts` + `@xyflow/react` + `@dnd-kit/*` + `@monaco-editor/react` + `material-symbols` → **完整带 UI dashboard 的现代前端栈**
- **服务端**：`express ^5.2.1` + `http-proxy-middleware` + `undici ^7.19.2` + `socks-proxy-agent` + `jose` + `bcryptjs` + `@node-saml/node-saml`（企业 SSO）+ `node-machine-id` + `selfsigned`
- **数据库**：`better-sqlite3 ^12.6.2`（optional）→ `sql.js ^1.14.1`（纯 JS 兜底）。驱动选择链见 `src/lib/db/driver.js:7-57`：**bun:sqlite → better-sqlite3 → node:sqlite（Node≥22.5）→ sql.js** 四级降级；注释还记录「Node ≥24 时 better-sqlite3 原生 addon 加载即 SIGSEGV，故直接跳过」（`:23-25`）——这类细节是踩出来的。
- **没有外部服务依赖**：不需要 Redis / Postgres / Docker sidecar（compose 里的 `headroom` 是可选的上下文压缩代理，`docker-compose.yml:16-26`）。
- **要求 Node.js 20+**（`README.md:1483`、`gitbook/content/*/getting-started/quick-start.md:15`）。
- 构建吃内存：README 生产模式段建议**先挂 2G 临时 swapfile 再 `npm run build`**（`README.md:112-130`），并设 `MAKEFLAGS="-j1"`、`CFLAGS="-mno-avx"`（为 better-sqlite3 原生编译）。

**单机可跑性**：可以。三种官方路径 —— ① `npm i -g 9router && 9router`（默认开 `http://localhost:20128`，`README.md:76-83`）；② 源码 `npm install && npm run dev`（`:104-108`）；③ Docker `decolua/9router`（`Dockerfile` 84 行，多阶段 node:22-alpine，`EXPOSE 20128`，非 root `su-exec node`，数据卷 `/app/data`）。

### 6.3 若把它当「替代方案」部署，我们的实际成本

| 项 | 评估 |
|---|---|
| 部署形态 | 它是个 **有 Web 面板 + SQLite 的 Next.js 全栈应用**（默认 20128），不是我们能直接嵌进 Go 进程的东西 |
| 与生产共存 | 我们 40000（管理器）+ 40080（引擎）已是稳定链路；再引入一个 Node 全栈服务意味着多一套运行时、多一个数据库文件、多一套升级面 |
| 它的价值定位 | **参考实现 / 算法样本库**，而不是替代品 |
| 逆向 lane 的维护负担 | 它的 137 provider 里大量「伪装客户端」lane 需要**持续跟上游改指纹**。这从 CHANGELOG 密度可见（见 §7）。我们若整体接管，等于接过这份持续维护义务 |

**明确判断：不整体替换。** 我们的正确用法是把 §5.5 的 6 条机制按行号抄成 Go 实现。若只是想立刻验证某个机制的行为，可以本地 `npm i -g 9router` 跑起来当"活文档"用（本次调研**没有**安装、**没有**启动、**没有**占端口）。

---

## 7. 活跃度与近期修复方向

### 7.1 远程可达性：`git fetch --all --prune` **成功**

```
$ cd D:\AI_Projects\refs\9router; $env:GIT_TERMINAL_PROMPT='0'; git fetch --all --prune
（无输出，exit code 0，数秒内完成）
$ git status -sb
## master...origin/master [behind 99]
$ git remote -v
origin  https://github.com/decolua/9router (fetch)
```
- **远程可达**，未超时、未重试。
- 本地 HEAD `87a74e1b` @ **2026-10-15**；`origin/master` 顶 `1339107e` @ **2026-11-08**（v0.5.116）→ **仓库在本机 HEAD 之后 3 周又发了 12 个版本号**。
- 提交量：`git log --since='60 days ago' --oneline`（本地 HEAD 起算）= **217 条**；同一区间对 `origin/master` 起算 = **316 条**。**极其活跃**（日均 5+ 条）。
- `gh` CLI 存在（`C:\Program Files\GitHub CLI\gh.exe`）但**未认证**（`gh auth login` 未做），无法查 PR/issue 列表。改用一次 web_fetch 验证了单个 issue，见 §7.3。

### 7.2 近期修过 429 / fallback / cooldown / no-channel 类问题（**答案是「一直在修」**）

`origin/master` 近 120 天里命中关键字的提交（按主题归类，摘最相关的）：

**OpenCode lane 本身（对我们最直接）**
| 提交 | 说明 |
|---|---|
| `5bdcd5e7 fix(opencode): move free tier to /inference/v1 with the v2 client fingerprint (#4842)` | **2026-10-14 上游换指纹，免费 lane 一夜全废，他们搬家 + 换指纹救回** |
| `0c6ab4f9 fix(opencode): reuse one stable upstream session per identity to stop 429s` | **session 乱生成导致 429 雪崩**（§1.4） |
| `95dafae3 fix(opencode): alias declared tool names containing "terminal" on the free tier` | **工具名里含 "terminal" 就 429**（issue #5319） |
| `e964e482 fix(opencode): inject reasoning_content placeholder for Zen multi-turn chats (#4805)` | Zen 多轮 400 |
| CHANGELOG（v0.5.112）`OpenCode Free: restore the free tier by sending the TLS ClientHello the upstream accepts (via optional wreq-js, fail-open to native fetch)` | **上游开始做 TLS 指纹(JA3)识别**，他们引入 `wreq-js` 改 ClientHello；Docker 里为此把 `wreq-js`/`playwright-core` 预打进镜像（#5245） |
| CHANGELOG（v0.5.116）`OpenCode Go: use current inference endpoints (/zen/go/v1 retired)` | `/zen/go/v1` **端点整体下线** |
| CHANGELOG（v0.5.116）`Fetch: stop reusing dead keep-alive sockets silently dropped by upstream edges`（`963787f7`） | 上游边缘节点静默杀 keep-alive |

**冷却 / 熔断 / 降级语义**
| 提交 | 说明 |
|---|---|
| `ac98dd9d fix(antigravity): strike-break optimistic quota readings that keep 429ing` | §4.3 熔断 |
| `1a3db1ef feat(antigravity): quota-aware routing with reset-aware fallback` | 配额感知选路 |
| `15687d19 fix(chat,docker): return 503 for rate-limited providers` | §4.1 错误语义 |
| `8f4e3800 fix(combo): fallback when model returns HTTP 200 with zero usable content (#4750)` | §5.1 ① |
| `e706e4f2 feat(codebuddy): parse 6004 rate limit error and extract resetsAtMs` | §3.3 精确 resetAt |
| `ec669280 fix(search): scope failure locks so search cannot take chat offline` | **一条 lane 的锁把主链路全打死**（与我们 WARP 事故同型） |
| `67d9182e fix(executor): handle CommandCode in-stream errors for combo and account fallback` | 流内错误也要能触发降级 |
| `3b137aa6 fix(codebuddy): back off blocked international connections` | **地区封锁（"blocked international"）单独退避** —— 与我们「not available in your country」同类 |
| `cd4003bc feat(usage): dedup + cache Claude quota calls to avoid 429` | 探测本身会烧配额，故去重+缓存（§4.2 节流思想） |
| `7fee56ba fix(providers): clear stale locks after validation (#3830)` | 测试通过后清残留锁（§4.3 自愈） |

**近期 CHANGELOG 版本线（v0.5.99 → v0.5.116）**：几乎每版都有 OpenCode / combo / cooldown 相关条目；v0.5.116（2026-11-08）仍在修 `OpenCode Free` 429 与 `OpenCode Go` 端点下线。**结论：这不是一个"已经稳定"的方案，而是一份"上游持续打压、他们持续对抗"的实时记录。**

### 7.3 issue #5319（web_fetch 验证，单点）

标题：**「[Bug] OpenCode free tier 429s whenever a declared tool name contains "terminal" — 9Router forwards tool names untouched」**。修复即上面的 `95dafae3`（把含 "terminal" 的声明工具名做别名）。
→ 这条给我们的教训和 §1.3 同源：**OpenCode 免费 lane 的「可用性」取决于请求体细节到工具名字符串的程度**。我们若把免费 lane 当主力通道，就必须接受「某天上游改一个关键词判定，整条 lane 归零」这个现实 —— 所以架构目标应该是**「多源快速降级」而不是「把一条 lane 伺候得永不失败」**。（未认证状态下只验证了这一条 issue，未系统遍历 PR/issue。）

### 7.4 活跃度结论

| 指标 | 值 | 判读 |
|---|---|---|
| 60 天内提交（origin/master） | **316** | 极高活跃 |
| 距本机 HEAD 落后 | 99 提交 / 12 个版本号 / 3 周 | 更新极快，**读它的本地 checkout 会错过最新演进**（§5.6 就是） |
| 我们关心的课题是否在被持续修 | 是（§7.2 十几条） | **它的代码正处在「和上游打压对抗」的状态**，抄之前应优先读 `origin/master` 版而不是本地 HEAD |
| 社区 | `gh` 未认证，无法列 PR/issue；README 有 10+ 语言翻译与多个第三方视频指南 | 有真实用户群 |

---

## 8. 最终判断与落地建议

### 8.1 三句话判断

1. **不整体替换**：它是 Next.js + SQLite + 137 provider 的全栈应用，我们要的是 Go 引擎里一层薄薄的选路/冷却逻辑。引入它等于换一个运行时并接过「持续跟上游指纹对抗」的维护义务（§6.3、§7.2）。
2. **但它是目前能拿到的最好的一份「同类问题的成熟解法样本」**，MIT 许可，算法层与 UI 层分离干净（`open-sse/` 目录不依赖 React 即可读），抄起来成本很低。
3. **对我们最痛的那一点（WARP 出口池无健康账本 → 一条 lane 废则全废）它没有现成答案**：它的匿名 lane 同样不记冷却（§2.5、§4.5）。这部分必须自研，但可借它的**形状**（§4.3 strike breaker）。

### 8.2 建议落地顺序（按性价比，均可独立验证）

| # | 抄什么 | 源码位置 | 落点（我们） | 预期效果 |
|---|---|---|---|---|
| 1 | `ERROR_RULES` 表驱动 + `checkFallbackError`（含 `requestScoped` 新版） | `origin/master:open-sse/config/errorConfig.js:59-77`、`accountFallback.js:16-64` | `upstream.go:185-196 shouldSwitchVendor()` | 「凭据坏了」与「这个源服务不了这个请求」分开；403 地区封锁 vs 403 配额耗尽可按文案区分处置 |
| 2 | 二维冷却账本 + 惰性清扫 | `open-sse/services/accountFallback.js:120-176`；`src/sse/services/auth.js:238-345` | 新 `core/cooldown` 包（键 = lane/key × model） | 直接消灭「一条通道废了整 tier 废」 |
| 3 | 全废时的错误语义（三分支 + 跨候选最早 resetAt + `retry-after` + 人话） | `src/sse/handlers/chat.js:246-263`；`open-sse/utils/error.js:117-132`；`combo.js:421-439` | 503 生成路径 | 客户端能区分「等 30 秒再来」和「等到重置点」；排障不再盲 |
| 4 | 「先刷 token 就地重放，再换账号」 | `open-sse/handlers/chatCore.js:437-482` | `freetier_warp_fallback.go` 那条 `break`（见 `core/manager/freetier_warp_fallback.go:16-24`） | WARP 池空时不再 break 整个 tier，而是继续跑 key 链 / 代理链 |
| 5 | 200 空响应也降级（SSE peek 版） | `origin/master:open-sse/services/emptyFailover.js` + `combo.js:481-546` | `callOpenCodeAPIStreamOnce` `upstream.go:300-330` | 上游"假成功"不再穿透 |
| 6 | capacity-adapter 式全局备用源池 | `open-sse/services/capacityAdapter.js:15,93-101,118-157,161-174` | 我们的 `vendors/custom` 多中转站挂到一个「按能力/按 lane 的池」上 | OpenCode 免费模型不可用 → 自动切中转站；换小源时按上下文砍中间 |
| 7 | 手动解锁接口 + 可用性面板 | `src/app/api/models/availability/route.js:22-103` | 管理器加 `GET/POST /cooldown` | 冷却误判时不必干等；面板能答「现在还剩几条通道」 |
| 8 | 出口级健康账本（**它没有，需自研**） | 借 `src/sse/services/antigravityQuota.js:20-50,253-284` 的 strike + 「熔断伪装成正常读数写回同一缓存」形状 | WARP 池 / 代理池 | `usesWARP()` 不再只看池对象是否存在（`core/manager/freetier_warp_fallback.go:10-12` 记的原缺陷），而是看池内**有无健康出口** |

### 8.3 附：先做这两个 30 分钟实验，再决定抄哪几项

抄代码之前，有两个「认知级」问题会改变优先级排序，且成本极低（**注意：只能在 40195 以上端口做实验，做完必须杀；不要碰 40000/40080**）：

| 实验 | 做法 | 判读 | 影响 |
|---|---|---|---|
| **E1 TLS 指纹 vs 地理封锁** | 用同一个 zen key、同一出口 IP，分别在 ① Go 原生 `crypto/tls` 直连 ② Go 经 7890 ③ Bun/BoringSSL 经同一出口 三种 ClientHello 下打一次免费模型请求 | 若 ② 通而 ①③ 废 → 真的只是地理；若 ③ 通而 ① 废（同 IP 同头） → **确认 §1.3 第 5 点的 TLS 门也在生效** | 若 TLS 门成立，`refraction-networking/utls` 仿 BoringSSL 套件列表（9router 已把 17 项清单逐字抄好在 `opencodeTlsClient.js:18-37`）会比任何路由改造都更直接地恢复免费 lane |
| **E2 免费模型用真 key 是不是必 403** | 拿一个 `oc_sk_*` key，对 `-free` 后缀模型分别用 `Bearer <key>` 与 `Bearer public` + 合法 session 各打一次 | 9router 断言（`opencode-zen.js:285-292`）：真 key 打免费模型 → 403 FreeTierError，`Bearer public` → 通过 | 若成立，我们「10 个 zen 免费 key 一次都没被使用」就**多了一个非 WARP 的解释**：key 身份和免费模型不匹配，跟出口池无关 |

### 8.4 关于「整体替换」的明确判断

**不替换**，理由三条（按权重）：

1. **合规与工程边界**：我们的编排层（`core/`、`upstream.go`、管理器）是专有代码（根 `LICENSE` = All rights reserved），引擎是 GPL-3.0 外部进程。9router 是 MIT 全栈 Next.js + SQLite + Monaco + React 19 + Tailwind 4 应用，**把它塞进我们的链路等于换运行时、换数据层、换 UI，并接过高强度 lane 对抗的持续维护义务**（§6.2/§6.3、§7.2）。
2. **它并不比我们强在关键处**：「同一模型多源候选 + 按健康分排序」我们**已经有了且更好**（`auto_model.go:94-102,526-561` 的滑动窗口实测成功率 vs 它的静态 `priority` + 能力分档，§5.4）。它的匿名 lane 同样没有出口级健康账本（§4.5），也就是**我们最痛的那一刀它也没解**。
3. **正确用法是当「已验证的解法样本库」**：每个机制旁边都挂着 issue 号 / 实测记录 / 回归测试（`tests/unit/account-fallback-4xx.test.js`、`tests/unit/opencode-session.test.js`、`tests/unit/codex-extended-context.mjs` 等），照它的判据写成 Go 实现，能让我们少走半年弯路。MIT 让这条路干净（§6.1）。

**唯一值得重新评估「替换」的场景**：如果 E1 证实上游对抗已升级到大范围 TLS/attestation 门（我们自己的 Go 栈要长期维持伪装成本很高），那么「把 40080 那层换成一个 MIT 许可、社区活跃（60 天 316 提交）、有 137 provider 现成对抗代码的服务」就从「过度工程」变成「合理外包」。这个判断需要 E1 结果才能下。

### 8.3 一条「免费 lane 脆」的元结论

§7.2/§7.3 摆在一起看很清楚：OpenCode 免费 lane 在 2026-10-14 之后**每一层都可能被上游单方面改判定**（端点搬家、UA 形状、TLS ClientHello、工具名集合、session 形状、工具名里含 "terminal" 这种字符串级规则）。**因此「把一条 lane 伺候到永不失败」不是可达目标**；正确目标是：
- 降级链足够长（多源候选）→ §5.1/§5.3
- 降级判据足够准（不冷却无辜 lane，也不放过假成功）→ §3.2/§3.5/§5.6
- 恢复足够快且不重复踩坑（精确 resetAt + 熔断 + 惰性清扫）→ §3.3/§3.4/§4.3
- 失败时对客户端诚实（503 + retry-after + 真实原因）→ §4.1

这四点，9router 每一条都有可直接移植的实现，且都是**在别人已经踩过的坑上迭代出来的**（每个函数旁边都有对应 issue 号或 live probe 说明）。这是它对我们最大的价值。

---

## 附：本次调研涉及的 9router 文件清单（只读）

**引擎层（`open-sse/`）**
`services/accountFallback.js`(230) · `services/combo.js`(683) · `services/capacityAdapter.js`(174) · `config/errorConfig.js`(86) · `handlers/chatCore.js`(537) · `executors/opencode.js`(416) · `executors/opencode-zen.js`(330) · `providers/registry/opencode.js`(50) · `providers/registry/opencode-zen.js`(135) · `utils/error.js`(150) · `utils/proxyFetch.js`(425)

**服务端（`src/`）**
`sse/handlers/chat.js`(355) · `sse/services/auth.js`(372) · `sse/services/antigravityQuota.js`(300) · `sse/services/backgroundTokenRefresh.js`(184) · `sse/services/model.js`(94) · `lib/network/connectionProxy.js`(199) · `lib/network/proxyTest.js`(91) · `lib/db/driver.js`(89) · `shared/constants/providers.js`(168) · `shared/services/quotaAutoPing.js`(345) · `app/api/models/availability/route.js`(103) · `app/api/proxy-pools/[id]/test/route.js`(70) · `app/api/health/route.js`(15)

**元信息**
`package.json`(68) · `LICENSE`(21, MIT) · `Dockerfile`(84) · `docker-compose.yml`(30) · `README.md`(1566) · `CHANGELOG.md`(845，本地版；origin/master 已更新到 v0.5.116) · `docs/ARCHITECTURE.md`(557) · `tests/unit/account-fallback-4xx.test.js` · `tests/unit/opencode-session.test.js` · `tests/unit/opencode-zen-free-tier.test.js`

**origin/master 新增（本地 HEAD 无）**
`open-sse/services/emptyFailover.js`（SSE peek + 流重建）
