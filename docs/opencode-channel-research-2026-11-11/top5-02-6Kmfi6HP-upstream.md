# TOP5-02 — 上游 6Kmfi6HP/opencode2api 免费层现状与反向回灌可行性（2026-11-11）

> 只读调研。对象：`C:\Users\ASUS\AppData\Local\Temp\oc-refs-20261111\opencode2api`（GitHub **6Kmfi6HP/opencode2api**，HEAD `20021bd`，2026-11-10，v0.14.42）。
> 全程未修改该仓库（收尾 `git status --porcelain` 为空）；未修改 `opencode2api_enhance` 任何文件（除本文件）；未触碰生产 40000（pid 56240）/40080（pid 40176），两者全程在线。
> 实机：独立临时目录 `%TEMP%\oc2api-probe-40230`，端口 40230/40231，假 key，跑完即杀（`alive_after_stop=False`，40230–40239 现无监听）。

---

## 0. 一句话结论（先看这个）

上游**有活的免费层**、**代码里零 WARP**（WARP 只是它的一个可选 docker sidecar），并且它刚刚在**本机直连 + 中国大陆出口 + 不带任何 key** 的条件下被上游**正常受理**（返回的是额度 429，不是 403 地理拒绝）。这直接推翻了我此前 E2「直连 403 = 地理封锁」的解释：**上游认的是请求形态（session ID 反转时间戳 + tools manifest + system `<env>` + TLS/h1 指纹），不是出口国家**。出口 IP 影响的是「额度池」与「prompt 缓存分片」，不是「能不能进门」。

---

## 1. 它现在还有免费层（anonymous / free-tier）通道吗？—— 有，而且是它的主力路径

### 1.1 通道判定（`internal/app/auth.go`）

| 判据 | 位置 | 行为 |
|------|------|------|
| 无 `Authorization` / 无 `x-api-key` / `Bearer public` | `internal/app/auth.go:343-349` | `UpstreamAuth{Mode: AuthRoutePublic}` |
| key 前缀不合法（非 `sk-`/`oc_sk-`/`oc_sk_`，或长度不足，或 `sk-ant-`） | `internal/app/auth.go:369-380` | **回落 public**（不报错） |
| `auth.Token == ""` 时 `apiKey()` 恒返回字面量 `"public"` | `internal/app/auth.go:393-398` | 出站头即 `Authorization: Bearer public` |
| 是否免费模型的判定 | `internal/app/auth.go:417-423` | `*-free` 后缀 ∨ models.dev 目录零价（`internal/modelsdev/modelsdev.go:439`） |
| 付费模型名 → `-free` 变体降级 | `internal/app/auth.go:437-446`（`mapPublicToFreeModel`） | public/force_free 下 `gpt-4o-mini` → 若 `gpt-4o-mini-free` 在目录中则改写 |

**关键设计**（`internal/app/auth.go:352-353`）：管理面板密码被当作 `Bearer` 时**不校验下游 key**，而是强制 keypool 接管（`AuthRouteAdmin`，常量时间比对）——这是它「匿名入口也能吃多账号池」的暗门。

### 1.2 上游端点与模型目录

| 用途 | URL | 位置 |
|------|-----|------|
| 免费/zen 推理（主） | `https://opencode.ai/inference/{openai\|anthropic/}v1/{chat/completions\|messages\|responses}` | `internal/app/opencode.go:722-736` |
| Go 订阅推理 | `https://opencode.ai/zen/go/v1/<subpath>` | 同上 `:723-724` |
| 模型目录 | `GET <base>/inference/v1/models`（`Bearer public` + 全量指纹头） | `internal/app/opencode.go:455-499` |
| Go 目录 | `GET <base>/zen/go/v1/models` | `internal/app/opencode.go:501-519` |
| 默认 base | `["https://opencode.ai"]`，可配 `upstream_base_urls`（多反代域名） | `internal/app/config.go:382`、`internal/domain/types.go:173-177` |
| 目录刷新 | 10 分钟（zen/go）+ 1 小时（models.dev） | `internal/app/opencode.go:616-656` |

历史迁移值得注意：**免费层已从 `/zen/v1` 整体搬到 `/inference/v1`**（v0.14.30，`CHANGELOG.md:105-111`），`/zen/v1` 对匿名请求现在全 403。任何还按 `/zen/v1` 打匿名流量的实现（包括我们那个失修引擎）都已经过期。

### 1.3 出站鉴权头（免费层必须凑齐的「进门证据」）

`internal/app/opencode.go:679-792`（`buildOCRequestWithSubpathAndState`）一次请求发出：

```
Authorization: Bearer public            ← auth.authorizationHeader()  :744
User-Agent: opencode/latest/<ver>/cli   ← ocUserAgent()               :749  版本下限 2.0.0，默认 2.0.29，实际从 npm 拉（fetchOCVersion :266）
x-opencode-client: cli                  ←                             :750
x-opencode-project: <40hex>             ←                             :755  （70c7d55 的 projectID 修复点，见 §3.4）
x-opencode-session / x-session-id /
x-session-affinity / x-opencode-session-id :  四头同值双发            :781-784
x-opencode-request: msg_<升序ID>         ← 仅 go/v1 路径              :786-788
```

免费层**门禁不识别 tier**：只要解析后的上游模型是免费模型，带真 `sk-` key 也一样走这套改写（`internal/app/opencode.go:681-689` 注释 + `docs/labs/2026-09-18-fingerprint-ablation.md:159`）。这点很重要——判据是 `isFreeModel(resolved)`，不是 `auth.tier()`。

---

## 2. 它用 WARP 吗？—— **不用。Go 代码里一个 `warp` 字都没有。**

穷举验证（大小写不敏感，全仓）：

| 命中处 | 性质 |
|--------|------|
| `deploy/compose/compose.warp.yml:10,16-17,21-23,37,41` | **一个可选 docker sidecar**：`image: docker.io/caomingjun/warp:latest`，暴露 `warp:1080` |
| `deploy/compose/README.md:7,47-66,78`、`docs/DEPLOYMENT.md:5`、`README.md:343,350`、`README.zh-CN.md:343,350` | 文档 |
| `*.go` | **0 命中**（`git grep -in warp -- "*.go"` 空） |
| `go.mod` / `go.sum` | 0 命中（无 wireguard、无 cloudflare 注册 SDK） |
| `wireguard\|1.1.1.1\|ZeroTrust\|tun\|wg.exe` in `*.go` | **0 命中** |

它自己那套 compose 把 WARP 表达成什么？`deploy/compose/README.md:54-60`：

> 该模版使用 `caomingjun/warp` 镜像，并把首次生成的配置设为
> `"socks5_proxies": [{"name": "warp", "addr": "warp:1080"}], "active_socks5": "warp:1080"`

**即：在它的架构里，WARP 只是「一个恰好由第三方容器提供的 SOCKS5 代理」，与 Tor（`compose.tor.yml`，`tor:9050`）、自建代理、住宅代理池完全同权、可替换、默认不启用。** 没有 TUN、没有设备注册、没有 `/64` 出口账本、没有 24h 冷却——因为它根本不碰 WARP 的注册协议。

### 它的出口方案（三选一，优先级明确）

`internal/app/egress_proxy.go:20-31` + `internal/app/httpclient.go:677-686` 注释：

```
显式 active_socks5  >  环境变量 HTTPS_PROXY/HTTP_PROXY/NO_PROXY  >  直连
```

1. **SOCKS5 代理列表 + `active_socks5`**（`internal/domain/types.go:170-172, 223-228`；自研 socks5 客户端 `internal/app/httpclient.go:340-424`，含用户名密码认证）。`active_socks5` 三态：空=直连 / 具体 addr / `__round_robin__`=轮询（`:447`）。
2. **环境变量 HTTP(S) 代理**（`cf85257`，`internal/app/egress_proxy.go:38-157`）：标准 CONNECT 隧道，支持 `https://` 代理 + Basic 凭证 + 私有 CA（`OPENCODE2API_TLS_CA_FILE`）+ 跳过校验（`OPENCODE2API_TLS_INSECURE`）。写坏的代理值当「没有代理」处理并退回直连（`:84-91`），**一个笔误不该把整个出口打死**。
3. **直连**（我们的实测证明：中国大陆直连**可用**，见 §3.5）。

**是不是「第三个不用 WARP 的先例」？是。** 已知三个：上游本体（0 WARP 代码）、9router（主张 JA4 指纹门，也不靠 WARP）、现在再加上「直连 + 正确指纹」这条被我们 E2 误判为不可用的路。结论上，WARP 在我们架构里承担的职责（换一个海外出口）对免费层门禁而言**不是必要项**，它真正必要的角色是「换一个没烧完的额度桶 / 分散账号限额」——那个用代理池 + keypool 就能做到，成本低得多。

---

## 3. uTLS / HTTP2 指纹这条线在解决什么？—— 它把「上游按客户端形态判你」当一等公民，而且是**逐字节复刻**

### 3.1 六个提交的演进（按时间）

| 提交 | 日期 | 干了什么 | 关键文件 |
|------|------|----------|----------|
| `70c7d55` | 10-24 | 引入 uTLS + 自研 h2，并 **fix projectID**：inference 路径曾要求 `x-opencode-project: "global"`（40hex 触发 403），此提交把它改回 `state.projectID`（即上游又接受真 40hex 了；见 §3.4） | `internal/app/httpclient.go`, `opencode.go` |
| `6f19a7f` | 10-24 | **方向纠错**：抓包发现真实 CLI 全程 HTTP/1.1、根本不发 h2 → 删 `fp_h2.go`，改写 h1 客户端 | `fp_bun_hello.go`, `fp_h1.go`（新增） |
| `13501cf` | 10-24 | 默认档位切到 `bun`（`defaultTLSFingerprintProfile = "bun"`，`internal/app/httpclient.go:55`），未配置即生效 | `httpclient.go` |
| `19d7e8d` | 10-25 | system prompt 注入合规 `<env>` 块——**第 6 层隐蔽门禁** | `internal/app/opencode_fingerprint.go:243-265` |
| `988a2a1` | 11-09 | 前置 CLI harness preamble + 工具 manifest（旧的四件小写 stub 已被新门禁拒绝） | `internal/app/fingerprint_manifest.go`, `agent_tool_reply.go` |
| `cf85257` | 11-09 | 环境 HTTP(S) 代理出口 + TLS 信任配置 + 连接池过期 | `egress_proxy.go`, `fp_h1.go` |

### 3.2 指纹复刻到什么程度（`docs/labs/2026-10-24-bun-fingerprint-alignment.md`）

抓真实 `opencode CLI v2.1.4`（Bun/BoringSSL 产物）打到 `opencode.ai` 的流量，逐字节对齐：

- **TLS ClientHello**：17 cipher（BoringSSL 顺序，非 Chrome 顺序）、13 扩展固定顺序、32 字节 legacy session id、**MLKEM768 混合密钥共享排首位（1216B）**、带 `status_request`(OCSP)+SCT、**无 GREASE / 无 ALPS**、**ALPN 只 offer `http/1.1`**、BoringSSL 512 字节 padding 规则（`utls.BoringPaddingStyle`）。实测 1502B，**JA3 md5 `1523504b38f0fae0d881d4b6554aac1b` 与 CLI 全等**（`fp_bun_hello.go:20-22`、labs `:65-66`）。测试 `TestBunClientHelloSpec_MatchesCapture` 与抓包归一化后**逐字节相等**。
- **HTTP/1.1 请求字节**：自研 `fp_h1.go`（430 行）自己拼线上字节——用户头按**字节序**排序、固定尾块五连（`Connection: keep-alive` / `Accept: */*` / `Host` / `Accept-Encoding: gzip, deflate, br, zstd` / `Content-Length`）、`b3`/`traceparent` 宽度对齐（32/16/16）、头名大小写还原。因为 `net/http` 会把 `Host` 提到第一行后、把 `x-opencode-*` 规范化成 `X-Opencode-*`、头序随机——**三者都是明显特征**（labs `:119-122`）。
- 对照表（labs `:63-69`）给出量化差异：`off` 档（标准 Go crypto/tls）JA3 `725543c7…`、`chrome` 档 `db378ed1…`、CLI/`bun` 档 `1523504b…`。

它把「协议版本本身」也算作特征：labs `:17` 原话——「**CLI 的 ALPN 只有 http/1.1 —— 真实客户端全程 HTTP/1.1，不走 h2**」，`:20`——「HTTP 层自研 h2 客户端虽然去掉了 Go 特征，但**协议版本本身就是与真实客户端不一致的特征**」。

### 3.3 更重要的不是 TLS，是**请求体门禁**（这条我们的引擎多半完全没有）

labs `2026-10-13` 的消融矩阵（`:19-33`）把变量逐个剥离后，结论极其干净：

| 探针 | 变量 | 结果 |
|------|------|------|
| A | 旧基线（session 前 12 hex **纯随机**） | **403** |
| B–F | 新三段式 UA / +`x-opencode-session-id` / `project=global` / 去 `x-opencode-request` | 均 **403** |
| E/F/G | 无工具 / 全量 opencode 工具集 / 仅 bash | 均 **403**（当时 tools 不参与） |
| R/S | 旧式 UA + 降序 session / 40hex project + 降序 | 均 **200**（**UA、project 不参与门禁**） |
| **J/K** | **session 前 12 hex = `~(ts_ms*0x1000+counter)` 低 48 位（降序反转）** | **200** |
| **P** | 升序（非反转）session | **403** |

→ **新门禁唯一新增校验 = session ID 的反转时间戳语义**（`internal/app/opencode.go:83-111` `newOCShapeID`，对齐上游 `packages/schema/src/identifier.ts`；ses_ 降序、msg_ 升序、后 14 位 base62 随机；时效容忍 ≥7 天）。纯随机或升序**一律 403 `FreeTierError`**。

到 11-09（`988a2a1`）门禁又加两层（`internal/app/fingerprint_manifest.go:11-21`）：

- system 文本必须是 **CLI 自己的 harness preamble**（`fingerprint/agent-preamble.txt`，go:embed；截到 ~1.8KB 或换通用提示词都按 429 `FreeUsageLimitError` **伪装成额度耗尽**拒绝）；
- tools 必须携带 CLI 的**工具 manifest**（`fingerprint/agent-tools.json`），能映射回调用方工具的 6 件（read/edit/glob/grep/shell/write）排在调用方工具之前；回复侧再映射回客户端工具名并丢弃客户端无法执行的调用（`agent_tool_reply.go`）。

**「伪装」这个词是它自己的措辞**（`opencode_fingerprint.go:248-249`：「缺失时服务端**刻意**返回与配额耗尽相同的 429 `FreeUsageLimitError` 响应体和 retry-after 头」）。也就是说：**429 不一定是没额度，可能是请求形态不对。** 这条对我们排查有直接价值。

上游为什么这么干，labs `2026-10-13:15` 给了考证：`83f2b6f67a`（2026-10-11，"make Zen routes a thin inference proxy"）把 zen handler 从 1314 行砍成 62 行薄代理，**旧的 `error.ts` / `ipRateLimiter` / `stickyProviderTracker` 全部删除**，免费层决策下沉到**私有 inference 服务**，`FreeTierError` 字符串在公开仓库里已不存在——**新门禁不可审计**，只能靠黑盒消融推。

### 3.4 一处仍在震荡（移植时的雷）

`70c7d55` 把 `x-opencode-project` 从 `"global"` 改回 `state.projectID`（diff 见 `git show 70c7d55 -- internal/app/opencode.go`），而 v0.14.30 的 `0924584` 曾断言「40-hex project id 触发 403、必须 `global`」（`CHANGELOG.md:108-109`）。同一件事 10 天内两个相反结论，且 HEAD 采信「40hex 可用」。**我们的实机旁证**：HEAD 版（`state.projectID` 为 40hex）能拿到 `200 models loaded count=85` 与 `go catalog loaded count=30`（§3.5），说明**至少目录端点接受 40hex project**。→ 移植时这条**必须留成可一键切换的配置/env，不要焊死**。

### 3.5 实机证据（对 E2 判定的直接修正）

本机、**直连**（进程环境无 `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY`，逐一验证为 `<unset>`）、**假 key/无 key**、端口 40231、独立目录：

```
GET  /health                      → 200 OK
GET  /v1/models   (无 Authorization) → 200，12 个模型：big-pickle + 11 个 *-free 别名（mimo-v2.6-flash / deepseek-v4.1-flash / glm-5.3-flash / longcat-2.5 / iceman / inkling-2-small / beam / flint-40b / nemotron-3.5-* / minimax-m3.1-flash）
启动日志                          → "opencode session ready" version=2.2.7；"models loaded" count=85；"go catalog loaded" count=30；"models.dev catalog loaded" count=4228
POST /v1/chat/completions 无 key（big-pickle，免费）
   → 429，耗时 24.3s
   body: {"type":"error","error":{"type":"FreeUsageLimitError","message":"Free usage limit reached. Free models work in OpenCode (https://opencode.ai); subscribe to OpenCode Go to keep going: https://opencode.ai/go"}}
   rate_limit_reset = "in 17h44m46s (at 2026-11-12 00:27:03 UTC)"
POST /v1/chat/completions Bearer sk-probe-fake-key-0000000000（假 key）
   → 上游 401 {"type":"authentication_error","message":"Invalid credential"}，重试 3 次后透传 401
```

**读法**：

1. 从中国大陆**直连**、**完全匿名**，上游**没有任何一次 403**、没有「This model is not available in your country」、没有 `FreeTierError`（形态门）。**请求被正常受理并进了额度判定环节。**
2. 拿到的 429 是 `FreeUsageLimitError` + 一个明确的每日重置时刻（UTC 00:27，约北京时间 08:27）——这是**每日额度桶**，且是**本机这条出口 IP 的匿名共享桶已经被打满**（不是国家封锁、不是指纹门）。
3. 于是：**E2（直连 403）不是地理封锁的充分证据。** 最可能：我们那个失修引擎的**请求形态**已被新门禁拒绝，上游（或中间设备）把它表达成 403；也可能是 11-10 之后上游新增/调整过一次门。**同一条直连上，形态对的匿名请求 200/429，形态错的请求 403。**
4. **海外出口仍然有价值，但角色变了**：不是「进门门票」，而是「换一个没烧完的额度桶 + 保住 per-egress 的 prompt 缓存分片」（`internal/app/httpclient.go:484-490`：实测固定出口 99.8% 命中 vs 随机轮换 ~0%）。这件事 SOCKS5 代理池/本机 7890 就能做，不需要 WARP 设备注册。

**上游实现倾向哪一边？明确站在「指纹/形态门」这边**：全部 6 个 fingerprint 提交、430 行自研 h1、87 行逐字节 ClientHello、go:embed 的 preamble/manifest、`FreeTierError` 消融矩阵，都只在「让请求看起来像真 CLI」这一件事上投入；仓库里**没有一行**代码处理地理/国家/地区封锁语义（`git grep -i "country"` → **0 命中**）。

---

## 4. 429 / 额度耗尽的处理 —— **不会出现我们这种「一次 break 放弃整个 tier」**

### 4.1 热路径（`internal/app/opencode.go:837-1111`，`callOpenCodeEndpoint`）

单次请求的完整循环（`maxAttempts = max(3,3)`，池启用时取 `keypoolMaxAttempts()`，`:858-861`）：

| 环节 | 位置 | 行为 |
|------|------|------|
| 每 attempt 选 key | `:891` `selectPoolKey(...)` | public 免费流量**也**交由 keypool 接管（`:217-223`）；池整体黑名单级失败 → **回落直连，不用死 key 硬撞**（`:261-266` + `poolHardDead` `:315-335`） |
| 池全在冷却 | `:267-275` | **取最早过期的那把继续服务**，注释原话「never hard-fail」 |
| 429 退避 | `:1047-1061` + `upstream429Backoff` `:1115-1120` | `Retry-After` 优先（cap 5s），缺省 `(attempt+1)s` 递增（1/2/3s cap 5s），尊重 ctx 取消 |
| **429 常规重试耗尽 → 全局 session 换代** | `:1008-1020` 判定 + `:1062-1083` 执行 | `retryReason="session_refresh_429"`，`canRetry=true` |
| 换代内容 | `refreshOCSession()` `:291-306` | **新 sessionID + 新 projectID**（注释 `:1010-1011`：只换 sessionID 不够，上游按 sessionID+projectID 组合归因，projectID 不变则限流窗口仍在旧身份上）+ 清空下游映射表 |
| 换代预算 | `:1082` `maxAttempts++` | **每请求至多换代一次**（`sessionRotated` 标志 `:1015`），避免无限重建 |
| sticky 解绑时机 | `:1086-1093` | **首次 429 保住出口**（免费层 429 常为按出口 IP 的瞬时限流，同出口重试保 prompt 缓存亲和）；只有 `attempt>=1`（同一请求连续失败）才 `invalidateUpstreamTarget` 换出口。transport_error 才立即解绑（`:939-945`） |
| 失败落点 | `:1097-1110` | 日志 `upstream_result` + **原样透传最后一次上游状态/响应体**（不自己编造 503，不「放弃整个 tier」） |

实测印证（§3.5 那次 429）：`attempt 0/1` → `retry_reason=status_429`，`attempt 2` → **`retry_reason=session_refresh_429`** + `opencode session ready`（新 session `ses_e36a7dd9…` + 新 project `690d2aa8…`），`attempt 3` 用新身份再打一次，仍 429 → `retries=3 final_status=429` → 客户端收到 **429 + `rate_limit_reset`**。**全程没有任何「break 掉整个 tier」。**

### 4.2 与我们的对照（这就是今天 100% 503 的根因）

| | 我们（GPL 引擎） | 上游 v0.14.42 |
|---|---|---|
| 出口池空 | 第一次就 `break`，放弃整个 key tier → **503，`the upstream was not contacted`** | keypool 取最早过期项继续；池级死则回落直连；绝不放弃 tier |
| WARP 出口冷却 | 固定 **24h、无递增、持久化**，一个坏 `/64` 烧整池，重启不自愈 | 无 WARP；key 冷却 60s / 长冷却 15min，**纯内存不持久化**（`keypoolEntryState` `:39-42`），重启即清空 |
| 429 后动作 | 无身份换代概念 | sessionID+projectID 全局换代 + 映射表清空，每请求至多 1 次 |
| 客户端可见语义 | 503（谎称未联系上游） | 透传真上游状态（429/400/401）+ 恢复时刻 |
| 带 key 的免费请求 | `usesWARP()` 一律强制走 WARP | `socks5_paid_direct` 一个开关分离「付费是否直连」（`internal/domain/types.go:172`、`httpclient.go:891-896`），默认全走代理 |

**它有没有可能一次放弃整个 tier？** 没有对应代码结构：热路径上只有两处 `break`（`:947` transport 重试用尽、`:1042` `!canRetry`），两者都**先经过 `rotateSessionOn429` / `pool_failover` / `sessionRotated` 三个放行门**，break 之后也只是把最后一次上游响应透传出去，不做「tier 级」整体放弃。

### 4.3 `0944925 feat(ratelimit)` 用什么字段（`internal/app/ratelimit_reset.go`）

三级提取，全兼容：

1. **响应头**（`:26-42`）：`Retry-After`、`X-Ratelimit-Reset`、`Ratelimit-Reset`、`X-Rate-Limit-Reset`、`X-Ratelimit-Reset-Tokens`、`X-Ratelimit-Reset-Requests`；
2. **JSON 字段**（`:112-133`）：顶层与 `error.*` 下的 `resets_at` / `reset_at` / `reset_time` / `retry_after` / `reset_after`；
3. **自然语言正则兜底**（`:16-18`、`:167-185`）：`try again in / retry after / resets in` + `20s|5 minutes|2h30m`，`resets at <时间戳|日期>`。

值归一化（`formatResetTimeValue` `:56-97`）同时支持 Unix 秒 / Unix 毫秒 / 相对秒 / HTTP-Date / Go duration。落点：**日志** `rate_limit_reset` 字段（`opencode.go:982,1031,1053`）+ **客户端响应体注入 `rate_limit_reset`**（`injectRateLimitResetWithHeader`，调用点 `chat.go:793`、`claude.go:1278`、`responses.go:1826`、`stream_retry.go:312/315`、`opencode.go:1270-1297`）。

我们实测拿到的就是这条链路产物：`"in 17h44m46s (at 2026-11-12 00:27:03 UTC)"`。**这是免费层排查最缺的一块信息，直接抄即可。**

---

## 5. `{{session-id}}` SOCKS5 模板 + pool expiry + credential-keyed pool —— 是「每会话换出口」的廉价方案，但**不能替代海外出口，也不该用来替代**

### 5.1 实现位置与配置写法

| 组件 | 位置 | 说明 |
|------|------|------|
| `isTemplateProxy` | `internal/app/httpclient.go:622-627` | username/password 含 `{{session-id}}` 即为模板代理 |
| `expandProxyCredentials` | `:629-639` | 替换为 `ocScope` = `normalizedTransportScope(ocSession)` = **sha256(sessionID) 前 8 字节 hex（16 字符）**（`opencode.go:173-180`）——定长、已脱敏，可直接嵌进用户名 |
| 不池化 | `:816-823` | 模板代理 `entry.client = nil`，每次按 `ocScope` 展开后 `buildProxyClient` 新建（`:733-736`），**避免每会话独立 Transport 无限增长**由 sticky 表有界 256 兜住 |
| credential-keyed pool | `:616-620` `proxyClientKey` | 单例池 key = `addr + \0 + username + \0 + password`，同地址不同账号不共用 Client（防认证串号） |
| TTL 快路径 | `:749-762` | 读锁命中只原子刷 `lastUsed`；过期直接落慢路径重建（`1bf7c7c` review 修复项） |
| 连接池过期 | `cf85257` → `fp_h1.go` | 按 host 单池、空闲 90s 超龄关闭重建；失效池化连接上幂等方法（GET/HEAD/OPTIONS/TRACE）自动换连接重放一次，**POST 不重放** |

**配置写法**（`docs/CONFIGURATION.md:176-232` + `internal/domain/types.go:223-228`）：

```json
{
  "socks5_proxies": [
    { "name": "isp-a", "addr": "proxy.example:1080",
      "username": "user-{{session-id}}", "password": "pass-{{session-id}}" }
  ],
  "active_socks5": "__round_robin__",
  "socks5_sticky": true
}
```

commit message（`887927f`）自己把用途说清楚了：

> This lets proxy pools that use per-session usernames (**e.g. rotating residential proxies**) route traffic for the same session through the same exit IP, **preserving upstream prompt-cache affinity** without needing a static proxy assignment.

### 5.2 那它到底解决了什么、没解决什么

- ✅ **解决**：一个「按用户名分出口」的代理（住宅代理/企业网关常用这种语义）不需要为每个会话静态分配一条代理，就能做到**同会话同出口**（保住 per-egress prompt 缓存，实测 99.8% vs ~0%，`httpclient.go:484-490`）+ **跨会话不同出口**（分摊出口 IP 级的 output-token 限速）。
- ✅ **另一条更便宜的同效果路径**：`ocSessionMap`（下游会话 → 专属上游 `ses_`，`opencode.go:316-437`，256 有界 + 2h 滑动 TTL + LRU）。因为上游限流按 `stickyId = sessionId ?: workspaceID ?: ip` 归因（`CHANGELOG.md:133`），**换一个 session 身份 ≈ 换一个额度桶，比换 IP 更廉价**；`{{session-id}}` 模板则把「session 身份」和「出口身份」绑成同一个维度，二者叠加才最省。
- ❌ **不能「替代 WARP 来撑匿名免费额度」**（严格讲）：模板**只展开字符串**，不生产 IP。前提是你已经有一个「按用户名切出口」的代理端点。而 §3.5 已经证明**中国大陆直连不被门禁拦**，所以今天真正的瓶颈是**每日额度桶**而不是出口稀缺——这个用 keypool（多账号，`keypool.go:185-192` public 流量交池分摊）+ `ocSessionMap`（多 session 身份）+ 海外代理池三件事就能撑，**WARP 不是必要件**。

**推荐组合（对应我们的痛点）**：`HTTPS_PROXY=http://127.0.0.1:7890`（我们已实测 10 个 zen key 走它 5/5 成功、0.7–1.6s）+ `key_pool`（多 key 分摊）+ `ocSessionMap`（多身份分摊）+ `socks5_sticky`/`{{session-id}}`（有代理池时才需要）。

---

## 6. `force_free` 模式（`18cb381` / #50）—— 语义与对我们「免费 lane 降级」的启发

### 6.1 语义（三处生效，一个 env 硬覆盖）

`internal/domain/types.go:160-166` + `CHANGELOG.md:74-77`：

> 开启后，**所有鉴权路径（包括 keypool）只使用 `-free` 变体模型**；`/v1/models` 只展示免费模型（剥 `-free` 后缀）；上游请求模型名自动加 `-free`；**行为与 public auth 完全一致，但 key 鉴权路径不变**。

| 生效点 | 位置 |
|--------|------|
| 模型名降级（`mapPublicToFreeModel`） | `internal/app/auth.go:439`（`(auth.Mode != AuthRoutePublic && !forceFreeEnabled())` ⇒ 不降级；force_free 时照样降级） |
| `/v1/models` 目录过滤 + 剥后缀 | `internal/app/chat.go:1153`、`:1170` |
| 开关读取 | `internal/app/keypool.go:194-203`：`OPENCODE2API_FORCE_FREE=1/true/yes/on`（部署期硬覆盖）> `config.ForceFree`（管理面板在线改 + 持久化） |
| 热切换 | `internal/app/admin.go:88/112/177`（`config set force_free=true` 立即生效，无需重启） |

即「**付费 key 当免费额度用**」的运维开关：key 照旧鉴权、照旧进池分摊限额，但**上游模型一律锁死在免费档**。

### 6.2 对我们「免费 lane 挂了自动降级」的启发（正向 + 反向各一条）

- **正向可抄**：我们的降级目前只做「WARP 池空 → 关 WARP 重启引擎」（v2.0.4）。上游这套给出三个更好的杠杆：
  1. **降级应是配置动作，不是进程重启动作**：`applyConfig`（`internal/app/config.go:148-250`）在保存后**全量热应用**——代理列表变了才清 sticky + 重建 client 池（`:212-235` 深比较 `proxiesEqual`），`tls_fingerprint` 切换连池自动重建，**无需重启**。对照我们「引擎 reload 被自废」，这是可以直接对齐的行为基线。
  2. **降级方向应该还有第二档**：上游有「免费档全灭 → 回落直连 / 回落 public」的显式路径（`keypool.go:261-266`），而我们现在只有「开/关 WARP」。`force_free` 反过来用（key 挂了全锁免费、免费挂了 key 优先）是现成的语义模板。
  3. **降级要带恢复时刻**：`rate_limit_reset` 透传（§4.3），面板/日志能显示「17h44m 后重置」，而不是让人猜。
- **反向（重要）**：**上游完全没有「lane 挂了自动降级到别的 lane」这回事**（`upstream_result` 恒 `fallback_used=false`，`opencode.go:971/1102`；`claude_no_chat_fallback_test.go` 是显式禁止 chat 回退的回归测试）。它的哲学是**诚实透传上游状态**（429 就 429、401 就 401），不编造、不悄悄降级。**我们「免费 lane 挂了自动降级」的隐式行为，代价是把故障语义藏起来**——v2.0.4 的 503 + `the upstream was not contacted` 正是这种遮蔽的产物。建议抄它的「宁可透传真错误 + 带恢复时刻」，把降级决策权交还给上层（面板/用户），而不是在热路径里默默改路由。

---

## 7. 失败语义与可观测性

### 7.1 `/health`（**注意：不是 `/healthz`，也没有富信息**）

`internal/app/server.go:180-183`：`/health` 恒 `200 "OK"`，两字节，**不含任何 warp/session/额度字段**。另有 `opencode2api -healthcheck` 自探测模式（`server.go:81,84-90`，Docker healthcheck 用它，镜像已切 scratch 无 shell，`CHANGELOG.md:15`）。
→ **对照我们引擎 `/healthz` 暴露 `warp.active`**：可观测性我们反而更强；上游靠结构化日志与 admin API。

### 7.2 结构化日志（这是它真正的账本）

| 事件 | 位置 | 关键字段 |
|------|------|----------|
| `request_plan` | `internal/app/chat.go:731-746` | `auth_mode`/`auth_source`/`has_key`/`protocol`/`model_in`→`model_resolved`/`upstream_surface`/`tools_count`/`text_only_model`/`max_tokens_cap`/`keep_reasoning`… |
| `upstream_attempt` | `opencode.go:937/965/1036` | `try_model`/`base_url`/`surface`/`status`/`duration_ms`/`attempt_index`/**`retry_reason`**/`rate_limit_reset`/`key_id`(池接管时) |
| `upstream_result` | `opencode.go:966-972/1097-1103` | `models_tried`/`base_url`/`retries`/`final_status`/`fallback_used` |
| `request_done` | `request_plan` 之后 | `request_id`/`method`/`path`/`status`/`duration_ms`/`bytes_out` |
| `upstream 429 backoff` / `429 global session refresh` | `opencode.go:1049-1055/1064-1067` | `old_session`→`new_session` + `new_project_id` |
| `upstream_error_debug` | `logging` + `-log-bodies` | 非 2xx / transport 错时带上游请求体+响应体摘要（`CHANGELOG.md:92`） |

**实测样本**（§3.5，直连 429 那次）完整给出了 `attempt_index 0→3`、`retry_reason` 三次变化、两次 session 换代、每次 `duration_ms`、`rate_limit_reset` 逐次递减——**一次请求把整条决策链打在 INFO 级**。这正是我们今天排查 503 时最缺的东西。

### 7.3 管理 API 与热应用（`internal/app/server.go:186-203`，仅 `adminAPIEnabled()` 时注册）

| 路由 | 方法 | 能改/看什么 |
|------|------|-------------|
| `/api/config` | GET | 运行时全量快照（`admin.go:108-131`）：`model_alias`/`reasoning_effort_map`/`force_disable_thinking`/**`force_free`**/`max_tokens_cap(_per_model)`/`socks5_proxies`/`active_socks5`/`socks5_paid_direct`/`upstream_base_urls`/`prompt_cache_retention`/`cache_control_breakpoints`/`socks5_sticky`/`text_only_models`/`protocol_rules`/`key_pool`/**`key_pool_status`**（含每把 key 的 `consecutive_fails`/`in_cooldown`/`cooldown_remaining_secs`，`keypool.go:395-431`）/**`oc_session_map_size`**/`log_level`/`log_bodies` |
| `/api/config` | POST | 写盘 + `applyConfig` **热应用、不重启**（`:161-165`）；`protocol_rules`/`key_pool` 严格校验，任一条非法 **400 且不落盘不生效**（`:143-160`） |
| `/api/key_status` | GET | 池快照 + toggle/strategy（`admin_keys_status.go:11-26`，**不需要 CSRF**，只读） |
| `/api/key_parse` | POST | 批量 key 文本 → 结构化（语法 `key \| note \| group \| weight`，`:47-`），面板与 CLI 共用一份实现 |
| `/api/stats` | GET/DELETE | 按 key 的 token/缓存/错误统计（读文件，跨进程共享，`admin.go:191-224`） |
| `/api/reload` | POST | **全局 session 换代 + 重拉两个目录**，singleflight 合并并发（`admin.go:23-72`、`opencode.go:291-306`），返回 `{status, session, free, go}` |

鉴权已加固（v0.14.31，`CHANGELOG.md:97-104`）：常量时间密码比对、24h 滑动 TTL session、`HttpOnly`/`SameSite=Strict`/条件 `Secure`、双重提交 Cookie CSRF、安全响应头。

**oc2api-admin skill**（`98bee29`，`.claude/skills/oc2api-admin/{SKILL.md,oc2api-admin.py}`，459 行）子命令 `probe | config | reload | stats`（+ 镜像更新走 `scripts/update-remote.sh`），`config get` / `config set <KEY>=<VALUE>`。
→ **对我们「引擎 reload 被自废」最有价值的一条**（`SKILL.md:56-59`）：

> ⚠️ **服务端 `POST /api/config` 是整体覆盖**：它会用提交内容覆盖整个配置文件并全量应用，**未提交的字段一律取零值**——`active_socks5` 变空字符串等于改为直连，`key_pool`/`protocol_rules` 等会直接从磁盘消失，重启即丢。
> 客户端已内置保护：`config set` 会先 `GET` 全量运行时快照、合并本次变更后再整份 `POST`。（直接 `curl` 必须自己完成同样的合并。）

**这几乎就是「reload 被自废」这类事故的机器可执行解释**：写配置方若发增量，缺省字段被清零 → 下次 reload 把关键开关「自己关掉」。上游把这条写进 skill 并在客户端强制 merge（`5c45f64`/`0a4a9df` 就是修这个）。**我们应照抄：服务端改增量合并，或至少客户端强制 GET-merge-POST。**

### 7.4 出口/会话/额度账本可视化 —— **部分有，且不在 `/health`**

- 有：keypool 冷却账本（`key_pool_status`）、下游会话映射规模（`oc_session_map_size`）、按 key 的 token/缓存统计（`/api/stats`）、每 attempt 的上游决策链（INFO 日志）。
- 无：**出口 IP 维度账本**（没有「哪个 sticky 出口绑给哪个会话、失败过几次」的持久化表）。它的出口状态是**纯内存 + 15min TTL**（`stickyEntryTTL` `httpclient.go:508`，有界 256），重启即清空、不写盘。→ 对我们的意义：**不持久化出口冷却恰好是优点**（我们的 24h 持久化冷却才是「重启不自愈」的病根），但排查历史故障要靠日志而不是面板。

---

## 8. 反向回灌可行性（最重要）—— 结论：**两条路都别走「整体替换」；推荐「抄上游的免费层形态层」而非移植整个上游，且第一步不是写代码，是重跑直连实验**

### 8.1 共同祖先在哪 —— 查清了，而且结果反直觉

| 事实 | 证据 |
|------|------|
| 我们 `main` 的历史**始于上游 `Initial opencode2api project`** | `f9a2754` 2026-06-04，作者 `6Kmfi6HP`（在**我们**仓库里，不是「近似」，是真的同一 commit） |
| 上游 PR 合并点 | `bfbdc62` = Merge PR **#3** from `6Kmfi6HP/fix/protocol-compat-and-model-aliases`，**2026-07-28** |
| 桌面化改造的分叉提交 | `a3c15e4` 2026-08-02 「chore: 移除 Docker/CI 设施，准备桌面化改造」；`faaeed5` 2026-08-09 `release: v1.3.0 —— 多平台 CI 与发布准备` |
| 上游同期版本节点 | `CHANGELOG.md` v0.11.2/v0.11.1/v0.11.0（`:309/322/334`）→ **v0.12.0**（`:289`，`protocol_rules`，对标 sub2api）→ **v0.13.0**（`:271`，三协议转换硬化）→ **v0.14.0**（`:266`，muse-spark 免费层） |
| 时间对齐 | v0.11.x ≈ PR#3（2026-07-28）前后；v0.12.0/v0.13.0 落在 2026-08（我们 v1.3.0 桌面化那一周）；v0.14.0 在 2026-09（我们 `test-20260901`/v1.7.3） |
| 我们删除免费层的时点 | tag `v1.9.0-free-tier-final` = `97d4ecb` 2026-11-03（「并行迁移方案——出口池/配额闸门/付费key/巡检修正」），随后 `v2.0.0` `1c5b3a8` 2026-11-05 起改为托管外部 GPL 引擎（`core/manager/freetier_engine.go:5,28,31`：`github.com/wbi35772/opencode2api-win`，端口 40080） |

→ **共同祖先大约在 2026-07-28（上游 v0.11.x）～ 2026-08-09（上游 v0.12.0/v0.13.0）之间**；我们带走的是「Go 协议转换网关」，上游此后一路演进到 **v0.14.42**，**差约 30+ 个 minor、3 个半月**。
→ **代码形状差得更远**：上游现为 `module github.com/6Kmfi6HP/opencode2api` + **`internal/{app,config,domain,logging,modelsdev,random,stats,util}` + 161 个 .go**；我们是**同一 module 名**但 **根包扁平 38 个 .go + `core/` 64 个 .go**、`go 1.22`、**go.mod 里 require 全空**。

### 8.2 致命前提：**上游的 git 历史无法与我们的历史建立 merge-base**

`%TEMP%\oc-refs-20261111\opencode2api\.git\shallow` 存在，内容 = `11bb0f3`（2026-10-14 23:13，v0.14.29 之后）；`git log --reverse` 的**最早 commit 就是它**，全仓**只有 51 个 commit**（2026-10 43 个、2026-11 8 个，**2026-06～09 一个都没有**）。
更关键：**我们 `opencode2api_enhance` 仓库里也有这批上游 commit 对象**（`git cat-file -t 70c7d55`/`20021bd`/`18cb381` 全部 = `commit`），但同样在 `11bb0f3` 处被截断：

```
$ git rev-list --count 20021bd   (在我们仓库里)
error: Could not read c40fb98b31c9d76cb2496d2a4c351b9a9862589d
fatal: Failed to traverse parents of commit 11bb0f32aa927502115d9c53ad9668655cd2c2e5
$ git merge-base 20021bd main    → 同一处报错，算不出
```

→ **这是一次浅 fetch 留下的孤儿对象图**（只取到 10-14 之后的 51 个 commit，父链断裂）。所以：
- 我们**读得到**上游最新代码（本文件全部结论据此）；
- 但**拿不到上游 2026-10-14 之前的历史**，`git merge-base` 不存在 → **任何形式的 upstream re-sync（merge / cherry-pick 到祖先）在当前本地状态下都不可行**，必须先补一个**完整（非浅）的上游 clone**（`git fetch --unshallow` 或重新 `git clone`）。
- 同时这也**独立证明**：「把上游提交直接 merge 进来」这条路从一开始就不成立——上游 v0.14.42 的 161 文件 `internal/` 结构，与我们的 `core/` + 根包结构**从未有过共同 blob 树**。

### 8.3 路线 A：把它的免费层移植回我们的 Go 代码

| 维度 | 评估 |
|------|------|
| **代码量** | 移植面 ≈ **6400 行**非测试代码：`opencode.go` 1282（session/ID/请求构造/热路径重试）+ `httpclient.go` 811（sticky 出口 + socks5 + uTLS 路由）+ `agent_tool_reply.go` 618 + `instructions_stable.go` 580 + `opencode_fingerprint.go` 528 + `modelsdev.go` 454 + `fp_h1.go` 430 + `keypool.go` 405 + `auth.go` 405 + `fingerprint_manifest.go` 318 + `ratelimit_reset.go` 231 + `egress_proxy.go` 154 + `fp_bun_transport.go` 107 + `fp_bun_hello.go` 87。**再加 2 个 go:embed 资产**（`fingerprint/agent-preamble.txt`、`agent-tools.json`）——它们是门禁本体，会随上游再变 |
| **第三方依赖** | 必需 `github.com/refraction-networking/utls v1.8.2`（ClientHello 复刻）、`golang.org/x/net`（`http/httpproxy` + `http2`）、`golang.org/x/sync`（singleflight）、`x/sys`、`andybalholm/brotli` + `klauspost/compress`（h1 响应 br/zstd 解压）、`lumberjack.v2`（日志轮换）。**我们目前一个外部 Go 依赖都没有**（go.mod require 全空、无 go.sum） |
| **构建门槛** | 上游 `go 1.26.0`（本机 go1.26.5 实测 `go build ./...` **通过，12s**）；我们声明 `go 1.22` → 引入 x/net v0.61/x/sys v0.49 需要抬 go directive 并新增 go.sum（离线/代理环境下首次 module 下载） |
| **架构摩擦** | 上游免费层与它的 `internal/config` 快照、`internal/stats`、`internal/logging`（`request_plan`/`upstream_attempt` 字段）、`modelsdev` 目录判定**互相咬合**；只搬 `buildOCRequest` 而不搬 session map / sticky / keypool / modelsdev，门禁与限额分摊都会缺件 |
| **工作量估计** | **12–25 人日**：形态层（指纹+session ID+manifest+`<env>`+embed）1–3 天；热路径重试/session 换代/ratelimit 2–4 天；sticky 出口 + socks5 + `{{session-id}}` + 环境代理 3–5 天；uTLS/h1 传输层接线 2–4 天；modelsdev 目录 1 天；回归测试 3–8 天（上游光 `free_tier_fingerprint_test.go` 就 20.9KB、`fp_h1_test.go` 14.8KB、`oc_session_map_test.go` 18.6KB，测试与实现同量级） |
| **最大风险** | **不是工作量，是「跟不上的门禁」**。上游自己 10 月内被门禁打断 4 次：v0.13.0（issue #19，2026-09-18 加 tools+stream 门）→ v0.14.29（2026-10-11 session 反转时间戳门）→ v0.14.30（`/zen/v1`→`/inference/v1` 路径门）→ v0.14.40（第 6 层 `<env>` 门，伪装成 429）→ v0.14.41（preamble + tool manifest 门，把上一版的四件小写 stub 判死）。labs `2026-09-18:198` 明确写着「免费层策略可能按时段/负载滚动收紧」。**门禁不可审计（上游已把它移进私有服务），唯一能持续追平的人是原作者**；我们移植后等于自任维护者，而下一次上游收紧时，只有上游仓库会 3 天内出修复——**这正是我们当初删掉本地免费层、换 GPL 引擎的那个理由，不能反过来又走回去**。次级风险：`x-opencode-project` global/40hex 两处相反断言（§3.4）、h1 自研传输层无 h2（上游若强制 h2 需重做）、抬 go directive + 引入外部依赖动了我们的构建/发布链 |

### 8.4 路线 B：直接把它当引擎（我们只托管进程）

| 好在哪 | 坏在哪 |
|--------|--------|
| 上游**活着**：v0.14.41 是 11-09、v0.14.42 是 11-10，我们最后一次同步（PR#3）是 07-28 → **3.5 个月的门禁/协议修复白拿**，且下次上游收紧时上游会先出修复 | 🔴 **许可证硬伤**（见下） |
| 免费层**已在 HEAD 可用且形态正确**（§3.5 实测：直连匿名 → 上游正常受理进额度判定，无 403 形态门），而我们的 GPL 引擎**永无官方修复** | 🔴 **不是 drop-in**：它是「完整独立网关」（自带 keypool/池/sticky/admin/面板/统计），与我们 v2.0 的「三进程（40000 面板 / 40080 免费引擎 / 50080 供应商服务）+ core/manager 契约 + 桌面化（Tauri 自释放二进制）」数据面**重叠冲突**；`warp.active` 这类我们面板依赖的字段它**根本没有** |
| 出口策略可配且更合身：**默认不用 WARP**，`active_socks5` / `__round_robin__` / `{{session-id}}` / `HTTPS_PROXY` 任选；`socks5_paid_direct` 把「带 key 要不要走代理」变成开关；`OPENCODE2API_FORCE_FREE` / `OPENCODE2API_POOL_PUBLIC` / `OPENCODE2API_OC_SESSION_MAP` / `OPENCODE2API_STICKY_BY_BODY` 全 env 可硬覆盖 → **正好绕开我们「WARP 自废」那一类失败模式** | 🟡 要抬 Go 版本 + 引入外部依赖 + 维护上游构建链；Docker/CI 设施与我们桌面化路线不一致（我们 08-02 主动删过） |
| 可观测性更强（`upstream_attempt`/`retry_reason`/`rate_limit_reset`/`key_pool_status`/`oc_session_map_size`），`/api/config` **热应用不重启**（对照我们「reload 被自废」） | 🟡 行为差异：它**不做 lane 自动降级**、坚持诚实透传真错误；我们面板目前假定「降级/自愈」 |

**🔴 许可证必须逐字看（GitHub 显示 `Other/NOASSERTION` 不是含糊话）**——`LICENSE` 原文（8 行，全文）：

> `Copyright (c) 2026 6Kmfi6HP`
> `All rights reserved.`
> **`No license is granted to copy, modify, distribute, sublicense, or use this software except as permitted by the copyright holder in writing.`** `Replace this file with an open-source license such as MIT or Apache-2.0 if you decide to publish the project under an open-source license.`

`README.md:399-401` 同口径重申。这不是「未选许可证所以工具报 NOASSERTION」，而是**作者明确写了「未授予任何权利」**。逐条对照我们的现状：

| 动作 | 我们已做的 | 上游许可证允许吗 |
|------|-----------|-----------------|
| 在本地**只读** clone 并做代码调研 | 是（本次任务） | 严格讲连 copy 都未授权；本地个人研究实务上风险极低 |
| 以**二进制形式托管并转发生产流量**（= 未授权「use」，若分发还构成「distribute」） | 路线 B 的核心 | ❌ **明确未授权**，且比 GPL 引擎更糟（GPL 至少明文允许，条件是要开源） |
| 把上游代码**移植进我们仓库**（copy + modify） | 路线 A 的核心 | ❌ **明确未授权** |
| 再分发/发布含其代码的产物 | 我们发 exe/便携包 | ❌ **明确未授权**，且我们 LICENSE 是非商业自定义许可证，两者不可能兼容 |
| git 历史里已有它的 commit | `f9a2754`…`bfbdc62`（PR#3，07-28） | 属既有事实；继续**增加**上游代码进入仓库才是新的未授权行为 |

→ **两条路线都卡在同一个「作者书面许可」前置条件上，而路线 A（抄代码）在许可证上不比路线 B（跑二进制）更安全，反而更不安全**——它把别人的代码永久放进我们要分发的产物，形成持续、可分发、可举证的侵权物；路线 B 至少是「托管一个独立进程」，可以随时停止、不留代码在我们仓库里。

### 8.5 明确推荐（分层，按性价比排序）

**推荐：先做零风险的验证实验，再走「只抄思路、不抄代码」的路线 A-lite；路线 B 只在拿到书面许可后作为可选升级。**

1. **第 0 步（今天就能做，1 小时，最高价值）：用正确形态重跑直连。** 我们已实测「直连 → 403 country」，但那次发的是**我们引擎的形态**。本次已证明上游 HEAD 的形态在**同一网络、同一直连**下不被 403 拦。→ 拿上游 HEAD 编一个临时二进制（本文件已给出构建产物路径），在 40230–40239 上分别测：①直连 ②`HTTPS_PROXY=127.0.0.1:7890` ③带真 zen key + `socks5_paid_direct`。若直连在**额度重置时刻（约北京 08:27）之后**能拿到 200，**那么 WARP 在我们架构里可以整体退役**，改用 7890 海外代理（已实测 5/5、0.7–1.6s）+ keypool 分摊即可。这一步不改任何代码，直接决定后面所有工程投入的方向。
2. **第 1 步（1–3 天，纯自研、零许可风险）：把三条「免费的」上游结论抄进我们的引擎/面板。** 它们不是代码，是**事实与语义**，不受许可证约束：
   - `rate_limit_reset`（429 必须带恢复时刻，`ratelimit_reset.go` 那套三级提取的**语义**，自己实现）；
   - **出口冷却不持久化**（内存 + 短 TTL，重启即清）——直接治我们「24h 烧池不自愈」；
   - **写配置要 GET-merge-POST**（`SKILL.md:56-59` 的教训）——直接治我们「reload 被自废」。
3. **第 2 步（视第 0 步结果）：路线 A-lite，只自研实现「门禁形态层」。** 免费层能不能进门，取决于一小组**可外部观察的事实**（session ID 反转时间戳编码、`/inference/{openai|anthropic}v1/*` 路径、四头同值双发、`tools` manifest、system `<env>` 块、UA `opencode/latest/<ver>/cli`）。这些是**协议兼容性信息**，用我们自己的 Go 代码重新实现（不抄上游字节）法律与工程风险都低得多，量级 **3–5 人日**，且**不必**引入 uTLS（§3.5 直连 200/429 说明上游目前并未在 TLS 层设卡；uTLS 是「更稳」而非「必需」，可留作后备）。
4. **不要做**：8.3 的「6400 行全量移植」，以及无书面许可的「直接把它当引擎跑生产」。
5. **值得花 30 分钟做**：给上游作者开 issue / 邮件请求**书面开源或商用许可**（作者 2026-11-10 还在提交，156 star，活跃）。若答应改 MIT/Apache-2.0（`LICENSE` 第 7-8 行就是给他自己留的台阶），路线 B 立刻从「不可行」变成「最省心的选项」——这是整件事投入产出比最高的一步。

### 8.6 最大风险（一句话）

**我们可能为了「不再依赖失修引擎」而把同一份维护负担从「一个消失的 GPL 仓库」换成「一个 All-rights-reserved 但极其活跃的仓库 + 一份私有不可审计、按周变动的免费层门禁」——届时我们仍是孤立的下游 fork，只是这次连合法同步的许可证都没有。因此第 0 步（先用直连把 WARP 的必要证伪）比第 1、2 步都重要：如果海外代理 + 正确形态就够用，我们根本不需要上游那 6400 行。**

---

## 9. 实机验证记录（问题 9）

| 项 | 结果 |
|----|------|
| `go build ./...`（上游，`cmd/opencode2api`） | ✅ **通过**。`go1.26.5 windows/amd64`；`GOCACHE=%TEMP%\oc2api-gocache`、产物 `%TEMP%\oc2api-build-20261111\oc2api-probe.exe`（17.5 MB），**未污染上游仓库**（`git status --porcelain` 空）；耗时 ≈12s，需联网拉 8 个 module（utls/x-net/x-sync/x-sys/x-crypto/x-text/brotli/compress/lumberjack），无 cgo、无 Node |
| 起服务（假 key，独立目录） | ✅ 40230（PID 35300）与 40231（PID 10464），`-config %TEMP%\oc2api-probe-40230\config.json`，**未写任何真实 key**（`config.json` 只有 `admin_password: "probe-admin"` + 空 key_pool） |
| `/health` | ✅ `200 OK`（两字节，无富字段；见 §7.1） |
| `/v1/models` 匿名 | ✅ `200`，12 项（`big-pickle` + 11 个免费别名），`owned_by` 区分 `opencode`/`alias` |
| 启动自检 | ✅ `opencode session ready version=2.2.7`（npm 实拉）、`models loaded count=85`、`go catalog loaded count=30`、`models.dev catalog loaded count=4228` |
| 免费层真实请求 | ⚠️ `429 FreeUsageLimitError` + `rate_limit_reset="in 17h44m46s (at 2026-11-12 00:27:03 UTC)"`，**直连、无 key、无 403**（见 §3.5 —— 本次调研最有价值的产出） |
| 假 key | ✅ 上游 `401 authentication_error "Invalid credential"`，本地重试 3 次后**透传 401**（不掩盖、不编造） |
| `/api/config` 未鉴权 | ⚠️ 实测 **302 → `/login` 页**（日志 `status=302`；`Invoke-WebRequest -MaximumRedirection 0` 才能看到跳转本身）。**这同时纠正了 SKILL.md「POST 全量覆盖」的表述语境：GET 走登录重定向，`config get` 需 `admin_password` 鉴权** |
| 清理 | ✅ 两个进程均 `Stop-Process` 终止；40230–40239 现无监听；生产 40000（56240）/40080（40176）全程在线未受影响 |

**未实机验证项（诚实登记）**：WARP/SOCKS5 海外出口的对比（本机无代理凭据，按约束不写真实配置，只测了直连）；`uTLS` hello 的实际线上抓包复核（信任上游 `TestBunClientHelloSpec_MatchesCapture` + labs 数据）；`force_free` / `{{session-id}}` / `key_pool` 的端到端行为（只做了代码级确认）。

---

## 10. 关键文件索引（`文件:行号`，供主代理直接跳）

| 主题 | 位置 |
|------|------|
| public/匿名鉴权 | `internal/app/auth.go:329-398`（提取 + tier + `Bearer public`） |
| 免费模型判定 / `-free` 降级 | `internal/app/auth.go:417-446`；`internal/modelsdev/modelsdev.go:439` |
| 上游 URL（inference vs go） | `internal/app/opencode.go:722-736`；base 默认 `config.go:382` |
| 出站指纹头 | `internal/app/opencode.go:744-788` |
| session ID 反转时间戳（进门钥匙） | `internal/app/opencode.go:49-120`（`newOCShapeID`） |
| 下游会话 → 上游 session 映射 | `internal/app/opencode.go:316-437`（256/TTL2h/LRU） |
| `<env>` 门禁（第 6 层） | `internal/app/opencode_fingerprint.go:243-330` |
| preamble + tool manifest（第 7 层） | `internal/app/fingerprint_manifest.go:1-340` + `fingerprint/{agent-preamble.txt,agent-tools.json}` + `agent_tool_reply.go` |
| Bun/BoringSSL ClientHello | `internal/app/fp_bun_hello.go:8-91`；组装 `fp_bun_transport.go`；h1 `fp_h1.go` |
| 指纹档位默认/env | `internal/app/httpclient.go:50-115`；`docs/CONFIGURATION.md:295-` |
| 环境 HTTP(S) 代理 + 坏值退回直连 | `internal/app/egress_proxy.go:20-157` |
| sticky 出口（per-egress 缓存） | `internal/app/httpclient.go:482-601, 694-848` |
| `{{session-id}}` 模板代理 | `internal/app/httpclient.go:616-639, 733-736, 816-823`；测试 `template_proxy_test.go` |
| credential-keyed 代理 client 池 | `internal/app/httpclient.go:603-675` |
| keypool（public 交池 / 池死回落直连） | `internal/app/keypool.go:185-335`；冷却 `:337-393`；状态 `:395-431` |
| **429 热路径** | `internal/app/opencode.go:890-1111`（退避 `:1047`、换代 `:1062-1083`、sticky 保/切 `:1086-1093`） |
| 429 退避计算 | `internal/app/opencode.go:1113-1138` |
| `rate_limit_reset` 提取/注入 | `internal/app/ratelimit_reset.go`（全 252 行）；调用点 `chat.go:793`/`claude.go:1278`/`responses.go:1826`/`stream_retry.go:312` |
| 不可重试（billing）判定 | `internal/app/opencode.go:589-614` |
| `force_free` | `internal/domain/types.go:160-166`；`keypool.go:194-203`；生效点 `auth.go:439`、`chat.go:1153/1170`；admin `admin.go:88/112/177` |
| `/health` | `internal/app/server.go:180-183`；`-healthcheck` `:81,84-90` |
| 管理路由（含热应用） | `internal/app/server.go:186-203`；`admin.go:23-189`；`admin_keys_status.go` |
| `applyConfig` 热应用（代理变更才清 sticky） | `internal/app/config.go:148-250` |
| oc2api-admin skill + 全量覆盖警告 | `.claude/skills/oc2api-admin/SKILL.md:41,56-59,131-133`；`oc2api-admin.py`（329 行） |
| 指纹消融实验（403 归因证据） | `docs/labs/2026-09-18-fingerprint-ablation.md`（尤其 `:56,91,103,159,179,198`）、`docs/labs/2026-10-13-free-tier-fingerprint-refresh.md:19-36`、`docs/labs/2026-10-24-bun-fingerprint-alignment.md:63-69,156-162` |
| daemon 泄漏坑（抓包注意） | `docs/labs/2026-10-24-…:51-59`（`opencode serve --service` 继承 proxy env，抓包后须 kill） |
| WARP 仅是可选 sidecar | `deploy/compose/compose.warp.yml:21-41`；`deploy/compose/README.md:47-61` |
| 许可证原文 | `LICENSE:1-8`；`README.md:399-401` |

---

## 附：本次实验可复现命令

```powershell
# 构建（不污染上游仓库）
$env:GOCACHE="$env:TEMP\oc2api-gocache"; $env:GOPATH="$env:TEMP\oc2api-gopath"
cd C:\Users\ASUS\AppData\Local\Temp\oc-refs-20261111\opencode2api
go build -o "$env:TEMP\oc2api-build-20261111\oc2api-probe.exe" ./cmd/opencode2api

# 起（独立目录、假 key、40230）——脚本见 %TEMP%\oc2api-probe-40230\run-probe.ps1 / run-probe2.ps1
# 关键：直连（进程环境确认无 HTTP(S)_PROXY），POST /v1/chat/completions 无 Authorization，model=big-pickle
# 预期：429 FreeUsageLimitError + rate_limit_reset（约北京 08:27 重置），全程无 403
```
