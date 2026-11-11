# IMROVOID/OpenCode2API — 边缘(failover)专项调研

- 仓库：`C:\Users\ASUS\AppData\Local\Temp\oc-refs-20261111\IMROVOID-OpenCode2API`（GitHub IMROVOID/OpenCode2API，HEAD `9fc9816`，2026-11-08，GPL-3.0，TypeScript）
- 调研日期：2026-11-11，只读；除本文件外不改动任何仓库
- 验证方式：**静态阅读 + 拷贝副本本地实跑**（`C:\Users\ASUS\AppData\Local\Temp\oc-verify-imrovoid`，未动原仓库；测试 daemon 用 40240/40241 端口，已杀掉；生产 40000/40080/50080 全程未碰，已复核仍在监听）
- 关键提交：`515d8f7 fix(unlimited): align OpenCode v2.2.5 wire fingerprint and add Kilo Free Gateway failover on 429`；前置 `9a3687d`（unlimited guard 引入）、`ca7652e`（worker.js 产物入库）、`92a37e8`（control panel）、`642f1ed`（CI 校验 Worker bundle）

---

## 1. 形态与运行前提

**双形态：同一套 `src/` 编译出两个入口 —— Cloudflare Worker（主推形态）和纯 Node 守护进程；两者都无需 CF 账号即可本地跑。**

- `wrangler.toml:1-4`：`main = "worker/worker.js"`（**入库的 esbuild 预打包产物**，`ca7652e` 起跟踪；`scripts/build.ts:24-34` 用 esbuild `platform: "neutral"` 从 `src/worker/index.ts` 打包，零 Node 内置模块，`scripts/verify-worker.ts:17-19` 在 CI 里断言这一点），`nodejs_compat` 仅兼容性声明。`wrangler.toml:6-19` 的 `[vars]` 就是 Worker 全部配置面：本地 key、匿名开关、WebUI 开关、`OPENCODE_ZEN_KEYS`/`OPENCODE_GO_KEYS`、三个上游 base URL。
- **`src/` vs `worker/` 分工**：`src/` 是唯一手写源（约 19,000 行 TS，含 `src/worker/` 与 `src/cli|server|proxy|unlimited`）；`worker/worker.js`（6,895 行）只是 `src/worker/index.ts` 的 bundle 产物，供直接粘进 CF 控制台 / `wrangler deploy`。`package-lock.json` 中**没有 wrangler 依赖** —— 仓库根本不假设你有 CF 工具链；部署由 GitHub Actions 产 release 附件（`.github/workflows/release.yml:141-175`）。
- **本地可跑性（实测）**：Node v22.23.1 下 `npm install`（仅 4 个 devDeps：tsx/typescript/esbuild/@types/node，2 秒装完）→ `npm test` **148/148 全绿**（13.8s）→ `tsc --noEmit` 干净 → `npm run build && npm run verify:worker` 全过 → `node dist/cli.js start` 在 127.0.0.1:40240 起服务，`/healthz` 返回目录统计（zen 85 / go 30），`/v1/unlimited/doctor` 返回完整体检。**结论：完全本地可跑、零运行时依赖、不需要 CF 账号**；Worker 形态的出口能力（`cf.resolveOverride`）在 Node 下自动退化为直连（`src/unlimited/lane.ts:128-148` 只是往 RequestInit 塞 `cf` 字段，undici 忽略它）。
- Node 守护进程功能反而是**超集**：JSON 配置文件（`config.example.json`）、双端口 WebUI（API + admin server）、目录定时刷新、多 key 池 + 冷却、rotation groups；Worker 是 keyless 简化版（env 配置、候选链转发）。注意 Node 的 WebUI 在测试中未起监听（40241 未 LISTEN，疑似 webui server 启动竞态或被我的启动方式触发），API 侧一切正常。

## 2. 429 → Kilo Free Gateway failover（`515d8f7` 核心）

### 2.1 候选顺序：代码写死的链 + env 可改 base URL

Worker 路径 `src/worker/unlimited.ts:97-159 collectCandidates()`（diff 见 `515d8f7`），顺序：

1. 每个 Go key → 每个 Zen key（`OPENCODE_GO_KEYS`/`OPENCODE_ZEN_KEYS` 逗号分隔）；
2. 若请求模型本身是 kilo 风格 id（含 `/` 或以 `:free` 结尾，`src/unlimited/kilo.ts:124-131`）→ 先插入整条 Kilo 链；
3. OpenCode v2 匿名 lane（`/inference`，key=字面量 `"public"`）——模型名先经 `OPENCODE_FREE_EQUIVALENTS` 映射（`kilo.ts:78-106`，把退役的 v1 免费模型名改写到 v2.2.5 现役名）；
4. **Kilo 链 7 项**（`kilo.ts:38-46 KILO_DEFAULT_CHAIN`：`reflection/beam-free → kilo-auto/free → nvidia/nemotron-3.5-lightning:free → nvidia/nemotron-3.5-super-vl:free → cohere/north-mini-code:free → stealth/crouton-alpha → openrouter/free`），非 kilo 请求先经 `KILO_EQUIVALENTS`（`kilo.ts:48-76`）把 OpenCode 模型名翻成 Kilo 等价模型，再附默认链兜底；
5. 匿名 Zen lane（仅当 `UPSTREAM_ANONYMOUS ≠ UPSTREAM_ZEN`）。

**第二个源 = Kilo Free Gateway**（`https://api.kilo.ai/api/gateway`，`kilo.ts:9`）：OpenAI `/v1/chat/completions` 协议、**零账号零密钥** —— `Authorization: Bearer anonymous` 字面量（`kilo.ts:10,180`）。模型映射表**硬编码在源码里，链内容不可配置**，只有 base URL 可用 `UPSTREAM_KILO` env 替换（`worker/unlimited.ts:35,106`；README:231/547 宣称的 `KILO_API_KEY`/`KILO_API_BASE` 在代码里**根本不存在**——文档漂移）。

**实测已腐坏的链项（2026-11-11）**：`reflection/beam-free` 与 `stealth/crouton-alpha` 直连 Kilo 均 **404 "model does not exist"**（当日 live 目录仅 4 个 `:free`：`kilo-auto/free`、`liquid/lfm-3-1.7b:free`、`nvidia/nemotron-3.5-super-vl:free`、`thinkingmachines/inkling-2-small:free`）。而链首恰好是死的 `reflection/beam-free` —— 见 2.3 的致命交互。

### 2.2 触发条件：不是"只看 429"，但分类只做了一半

外层是 `UnlimitedGuard.executeWithGuard`（`src/unlimited/guard.ts:163-181`）：首次尝试 + 最多 8 次 hop（`MAX_HOP_TRIES=8`，guard.ts:10），每次 hop 判据 `isQuotaWall`（guard.ts:34-48）：

- `status === 429`，**或** 响应体含 `quotaexceeded` / `free usage limit reached` / `freetiererror` / `rate_limit_exceeded`（含 200 + SSE error 帧的情形——单测 `tests/unlimited.test.ts:133` 专门覆盖）。

Worker 转发内再做一次归一（`src/worker/unlimited.ts:226-241`）：非 2xx 响应，若 `isQuotaWall` **或**（当前候选是匿名 key `"public"` 且状态 ∈ {400,401,403,404}）→ 状态改写成 429 交给 guard 触发 hop/换候选；**带 key 的候选吃到 4xx（包括我们的地理 403）则原样立即返回、不轮换**（我的 probe 实测：keyed 候选 403 country → 1 次尝试后 HTTP 403 透传）。超时/网络异常没有专门捕获（不像 Node 路径有 `outcomeFor` 的 `transport_error`，`src/proxy/attempt-helpers.ts:128-146`）。

「这个源服务不了这个请求 vs 这个源整体挂了」：**部分区分**。匿名候选的 404 会被当作"此路不通继续换源"（归一成 429）；但 **Kilo 候选的 404 不会**——`"anonymous" !== "public"`，条件 `key === ANONYMOUS_TOKEN` 不成立（worker/unlimited.ts:229），404 直达 guard，`isQuotaWall(404)`=false ⇒ **链在第一个死 Kilo 模型上终止**。这是设计缺口而非实现笔误（Node 路径同样：404 分类为 `request_error`，attempt-helpers.ts:145）。

### 2.3 重放方式、上限、全灭语义（probe 实测）

用 mock fetch 驱动 `src/worker/index.ts` 全链，令所有上游恒 429：

```
### keyless + model=glm-5.3 => HTTP 429, attempts=9
  #1 opencode.ai/inference/openai/v1/chat/completions  model=glm-5.3-flash-free  auth=Bearer public   cf={"resolveOverride":"2606:4700:10::ac42:ad95"}
  #2 api.kilo.ai/.../v1/chat/completions               model=reflection/beam-free auth=Bearer anonymous
  #3..#7 Kilo 链其余 5 项 …  #8 Kilo openrouter/free  #9 opencode.ai/zen anonymous
```

- **整请求重放**：每次 attempt 重新 `prepareCandidateRequest`（worker/unlimited.ts:195-201），body 全量重发，模型名按候选重写；attempt>1 时**换成 hop 新生成的身份**（`activeIDs = attempt === 1 ? initialIDs : lane.ids`，worker/unlimited.ts:188——会话亲和在重试瞬间被牺牲，quota 维度优先）。
- 候选按 `(attempt - 1) % candidates.length` 取（worker/unlimited.ts:189-193）⇒ **重试上限 = 1 + 8 = 9 次**，与候选数解耦：候选多于 9 时后面的永远轮不到（实测 keys+12 候选只打了前 9），少于 9 时回绕重打。
- **全灭语义**：最后一个候选的响应原样透传 —— 我的 probe 里是 `HTTP 429` + 上游错误体 `{"error":"QuotaExceeded: Free usage limit reached"}` + 诊断头 `x-opencode-hops: 8`、`x-opencode-edge`、`x-request-id`（worker/unlimited.ts:245-253）。**不合成 Retry-After、不指数退避、无跨请求记忆**（下一次请求照常再走 9 连）。Node 路径语义不同：`completion-handler.ts:98-110` 全败返回 **502 `all upstream attempts failed`**（匿名阶段最后一条非 2xx 有时被 key tier 的空池吞掉——probe2 实测 kilo 404 在 Node 下最终变成 502 而非 404）。
- 一个真实的自伤 bug：**每次 hop 都会 `blacklistAdd` 当前 /64**（guard.ts:174 `this.hop(this.pool, true)`），即使这次 429 是 **Kilo** 给的——把没试过的 OpenCode 出口当烧掉处理；链上 7 个 Kilo 候选 429 一轮就烧 7 个前缀。

### 2.4 能否落到我们的 Go 侧

形状高度同构，可平移：

| 它的概念 | 我们的对应物 | 落点 |
|---|---|---|
| `collectCandidates` 有序候选链 | `core/router/router.go:87 Candidates()`（已有厂商级 failover 骨架） | 在免费 lane 后**追加 Kilo 型兜底厂商**（`vendors/custom/custom.go` 已是中转站池，加一个 keyless vendor 即可复用） |
| `isQuotaWall` 状态+文案双判据 | `core/manager/freetier_engine.go` 目前空池第一次就 break | 把「429/配额文案」与「此源服务不了该请求（404/未知模型）」统一为 *continue-chain*，只有一致性客户端错误才终止 |
| `(attempt-1) % len` 全链重放 ≤9 次 | `auto_model.go` 模型别名轮转 | 免费 lane 503（`the upstream was not contacted` ≡ 它的 transport_error）直接作为链进位条件，而非终态 |
| `outcomeFor`/attempt record 失败归因 | `freetier_warp_fallback.go` 的 strikes 观测 | 每 attempt 记 `(source, model, status, outcome)`，面板展示链上每跳 |

需要避开的它的坑：重试上限与候选数解耦、非配额 404 不换位、hop 误烧无关出口、无跨请求熔断（它对同一坏链每次都烧 9 发）。

## 3. "unlimited" 是什么

不是配置模式，是 `src/unlimited/` 模块族 + `/v1/unlimited/doctor|hop` 端点的总称（引入于 `9a3687d`），语义 = **把 OpenCode 免费档做成"无限额度"**，三条腿：

1. **身份轮换**：每次 hop 铸新 `ses_`（v2.2.5 编码格式，见 §4）、`msg_`、40-hex `project`、32-hex `machineId`（`src/unlimited/lane.ts:107-126`）——上游按设备/会话身份计额，换身份=换额度桶；
2. **出口轮换**：DoH（1.1.1.1）解 `opencode.ai` 的 AAAA 得 Cloudflare 边缘池（lane.ts:60-103，兜底 3 个写死的 `2606:4700:10::*`），`cf.resolveOverride` 把 Worker 子请求钉到指定边缘（lane.ts:128-148）；烧掉的 /64 前进 200 条黑名单，全黑时回收最老前缀（注释称"额度桶每日午夜重置"，guard.ts:141-154）；
3. **换供应商**：Kilo Free 兜底（§2）。

即"多出口 + 多 session + 多网关"绕过免费限额，**额度冷却维度是 (device/session 身份 × 客户端 /64 × key)** 三个独立维度各自有池；`COOLDOWN_SECONDS=75` 只在 doctor 报告里出现、运行路径未消费（死常量）。参考实现自 ElectronBreach/opencode-unlimited-usage（README:647），README:638-641 自己承认违反上游 ToS 的风险自负。

## 4. wire fingerprint（v2.2.5 对齐）——纯 HTTP 层

`515d8f7` 改的全部是 HTTP 报文层，**零 TLS 代码**（全仓 grep `tls|JA3|cipher|impersonat` 只命中密钥指纹 `fingerprintSecret` 与一处 "TLS fingerprint" 注释）：

- `User-Agent: opencode/latest/2.2.5/cli`（`src/protocol/lanes.ts:7`，从 2.0.32 升级）；v1 lane 则伪装 `opencode/1.19.3 ai-sdk/provider-utils/<ver> runtime/bun/1.3.14`（lanes.ts:36-38）；
- **session id 编码格式**：新铸的 `ses_<12位反转时间戳hex><14位base62>` 精确复刻官方 CLI 的 `~((now_ms<<12)|seq)` 降序时间编码（`src/protocol/identity.ts:87-103 mintFreshSessionID`，替换了旧的哈希派生 `canonicalSessionID`）——上游显然校验 id 里的时间戳形态而不只是长度；
- 头集合：`x-opencode-session`/`x-session-affinity`/`X-Session-Id` 三头同值、`x-opencode-client: cli`、`x-opencode-project` = 裸 40-hex（**注释明言：v2 gate 会对 `x-opencode-request` 和 `prj_*` 形态回 403 FreeTierError，所以这两个绝不发送**，`src/protocol/upstream-headers.ts:66-89`）；匿名 lane 路径改为 `/inference/openai|anthropic/...`（lanes.ts:49-56）；同时**删除**了 `x-opencode-machine-id` 头（diff attempt-helpers）。
- **请求体指纹**：v2 免费 gate 会比对五个核心工具（`shell/edit/glob/grep/read`）的 description+schema 与官方 CLI 是否一致 ⇒ 仓库直接内置官方工具定义 `src/protocol/tools/opencode_tools.json`（16KB）并在 `ensureV2Tools` 里强制补齐/覆写（`src/protocol/tools.ts:264-316`）；chat 协议还把 `max_tokens` 改名 `max_completion_tokens`（缺省 32000）、删 `tool_choice:"none"`、function 工具加 `strict:false`（同文件 269-284 与 diff 75-79）。

### 对「地理封锁 vs TLS 指纹」争议的判定

**证据链支持：判定=IP 地理 + HTTP 报文形状两层，TLS 客户端 hello 不参与 accept/reject。**

1. 本仓库主体跑在 Cloudflare Worker 上——**Worker 的对外 TLS 握手由 CF 平台代打，代码层面不可能定制 TLS hello**，但它对齐 HTTP 特征后匿名免费请求 200（其 CI/测试全部 mock 上游，但 Node 路径是真网络可达 Kilo；OpenCode 侧 200 由 README/提交说明佐证）；
2. 我在本机直连实测（probe2，中国大陆出口 IP）：同一套已对齐 v2.2.5 指纹的请求打 `opencode.ai/inference/openai/v1/chat/completions` → **`403 {"type":"RegionError","message":"This model is not available in your country"}`** —— 错误体自报家门是**地理判定**，不是指纹拒绝（拒绝伪装客户端它们用的是 `FreeTierError`，两个 type 不同，upstream-headers.ts:69-70 注释也印证 403 有两种语义）；
3. 同一时刻同一台机器，打 api.kilo.ai 直连 200 —— 网络通路本身没问题，被 Geo 的只是 opencode.ai。

结论：我们此前「直连 403 = 地理封锁」的解释**被上游错误类型直接证实**；「按 TLS 指纹判客户端」一派在本目标上没有证据支撑——但保留一个精确化：CF 出口能过可能同时吃到 **colocation 红利**（Worker→`resolveOverride`→CF 自家边缘→CF 源站，握手全程在 Cloudflare 网内，WAF bot 评分天然宽松）。这对我们的操作含义是：**换海外 IP 出口 + 对齐 HTTP 头/体即可，不需要往 curl-impersonate/TLS 伪装方向投入**。

## 5. 会话与出口

- **会话派生**：`deriveRequestIDs`（`src/protocol/request-identity.ts:189-243`）从客户端头（`x-opencode-session`/`x-session-affinity`/`conversation_id`/Claude Code 的 `metadata.user_id`…）或首条 user 消息内容哈希**确定性**派生 session；`msg_` id 有 6h TTL 的会话内消息存储（113-171）。
- **key 亲和**：`KeyPool.cursorFor(session)` FNV-1a 哈希把同一会话钉在同一 key（`src/auth/key-pool.ts:66-75`）；但 guard 一 hop 就整体换身份（§2.3），亲和仅在无重试时成立。
- **多 key 池**：zen/go 两组逗号分隔 env / 配置文件数组；失败冷却 = 基值指数 ×2 封顶 ×8，Retry-After 全额尊重（key-pool.ts:83-111）。可疑点：runner 传给 `markFailure` 的冷却基值是 `timeout_seconds`（300s）而非 `performance.failure_cooldown_seconds`（15s）（attempt-runner.ts:156-161 vs config）——后者全仓无人消费，疑似 bug；`proxies`/`proxyfile` 配置字段在请求路径**完全未接线**（只有脱敏和视图引用），Node 形态实际不支持出口代理。
- **出口（CF 上怎么换）**：不是真换源 IP —— Worker 出网恒是 CF 机房；靠 `cf.resolveOverride` 换的是**回源落点边缘**+ 身份层（§3）。额度冷却维度 = 上游侧按 session/device 身份与 /64 前缀记账，其对策是黑名单+每日重置假设（guard.ts:141-146）。

## 6. 内置 control WebUI

单文件内嵌 HTML（`src/webui/*`，灰阶暗色），API：`/api/auth/login|session|logout`、`/api/monitor`、`/api/debug/models`、`/api/config`、`/api/api-keys`、`/api/model-quotas`、`/api/rotation`（`src/worker/index.ts:87-113`、`src/server/admin/*`）。可观测面：

- **Node 形态（真数据）**：lifetime/last-hour 请求数、成功率、输入/输出/缓存/reasoning token（按 model/tier/session/server-key 维度）、每分钟序列、**每条上游 attempt 记录**：`{time, model, tier, attempt, key_id, channel(key|anonymous), proxy_node(edge IP|kilo), status, duration_ms, outcome(success|rejected|quota_or_rate_limit|upstream_error|transport_error|request_error)}`（`src/telemetry/metric-types.ts:70-105`）——即**每跳失败的机器可读归因**，正是我们面板缺的；目录 `stale` 标记（cacheSource=disk 或超时未刷新，`src/proxy/model-catalog.ts:286-290`）；quota 页有真提示："OpenCode 返回的是共享订阅额度，多 key 不能相加"（`src/server/admin/views.ts:163-165`）。
- **Worker 形态（空壳）**：`/api/monitor` 指标全为写死占位（lifetime total=0、示例请求一条），`/api/api-keys`、`/api/model-quotas`、`/api/rotation` 返回空数组（`src/worker/admin.ts:160-232`、`src/worker/index.ts:105-113`）——README 宣传与实现有落差。
- **「配置未生效/漂移」提示**：WebUI 会渲染 `restart_required_fields` 警告徽章（`src/webui/script-settings.ts:132-134`），但后端永远返回 `applied:true, restart_required:false, restart_fields:[]`（`src/server/admin/config-routes.ts:93`）——**特性是半截的**；`/api/config/reload` 同样恒报成功（admin/routes.ts:87-88）。
- 另有运维端点：`GET /v1/unlimited/doctor`（egress IP/prefix、当前 edge/session/machineId、hops、黑名单计数、池大小；本机跑实测返回 `status:"degraded"` 因 egress 非 Cloudflare —— 即「出口不在 WARP/CF」的漂移自检）与 `POST /v1/unlimited/hop`（手动跳出口）。

## 7. 测试与质量

- **规模**：TS 源码 ~19,000 行（src/protocol 7.4k、server 2.4k、proxy 1.9k、unlimited 0.7k、worker 0.9k、webui 0.9k、telemetry 1k、config 0.7k）+ tests 2,089 行 6 文件 + 入库 bundle 6.9k 行。CI（.github/workflows/test.yml，Node 22）跑 typecheck+test+build+「bundle 无 node: 内置」+ worker 全局域沙箱验证。
- **覆盖与 failover 用例**：`tests/unlimited.test.ts` —— /64 归一、DoH 池解析/回退、身份格式断言、`isQuotaWall` 四态（含 200+SSE 错误帧）、黑名单 cap 200、最老块回收、**auto-hop 至 8 次后成功**（174-209）、**Worker 429→换 edge+换身份→成功** 集成（264-302，断言第二次 `Bearer anonymous`）、SSE→JSON 折叠、control panel 登录/监控。缺口：**Node 的 kilo failover（attemptKiloAnonymous/runAnonymous）零测试**、全链耗尽语义无断言、kilo 404 行为未测、协议转换（responses/anthropic↔chat）与流式覆盖尚可（protocol/stream 各 ~150 行）。
- **本地可跑（已实测）**：见 §1 —— 装依赖、148/148 通过、构建+沙箱验证通过、daemon 起停正常。`wrangler dev` 未跑（不需要）。
- **可信度**：代码风格统一、注释密集且多为"为什么"级、有真实上游逆向痕迹（工具表内置、session 编码复刻、FreeTierError/RegionError 语义区分）——逆向做工是真的；但腐化同样是真的：**Kilo 链 2/7 已死、README 的 KILO_API_KEY 不存在、failure_cooldown_seconds/proxies 死配置、rotation-stream.ts 是孤儿模块（全仓无人 import）、restart_required 半截实现、Worker monitor 假数据**。1 star / 创建 2026-10-20 / 单人作者 —— 作为**设计参考**可信度高，作为**生产依赖**不可信（模型清单类硬编码以天为单位过期）。

## 8. 结论

### 最值得抄的一条

**「候选链全量重放 + 三类失败归因」的转发核** —— `src/worker/unlimited.ts:187-253`（`executeWithGuard` 包 `(attempt-1) % candidates.length` 的整请求重放，≤9 跳）+ `src/unlimited/guard.ts:34-48`（`isQuotaWall`：状态码与错误体文案双通道判额）+ `src/worker/unlimited.ts:226-241`（把匿名源的 400/401/403/404 归一成 429 驱动换位，而带 key 源的 4xx 立即终止）。这三段合起来就是我们 P2 决策的最小完整答案：**免费 lane 失败不是终态，而是一个带分类的链进位信号**。

落到我们的文件：
- `core/router/router.go` `Candidates()`（99 行已经就位）——把「免费引擎厂商」的失败返回值从终态错误改为链进位；
- `core/manager/freetier_engine.go`——照 `isQuotaWall` 写一个 `classifyUpstreamFailure(status, body) → {quota_wall|source_cannot_serve|source_down|client_error}`，空池 break 与前三种统一走 continue；
- `core/manager/freetier_warp_fallback.go`——它的 strikes 观测量（现只盯 WARP 池）应扩成每 attempt 的 `(source, status, outcome)` 记录（抄它的 `attempt record` 字段，`src/proxy/attempt-helpers.ts:174-209`）；
- 反面教材同样值钱：**别学它**的非配额 404 终止、hop 误烧无关出口、重试上限与候选数解耦、无跨请求熔断。

### Kilo 这类第二免费源值不值得接

**值得当"活着比优质好"的兜底接一条，但绝不当容量层。** 实测证据（本机 2026-11-11，中国大陆直连，零代理）：`Bearer anonymous` 直打 `api.kilo.ai/api/gateway/v1/chat/completions` **200、~1.4s、连续 8 发无限流迹象**，且**不受 opencode.ai 地理封锁影响**（不同资产方）——作为 40080/40000 全灭时的 last-resort，它今天恰好补上我们"免费 lane 挂=全挂"的洞。风险清单（须如实写进验收）：① 目录漂移快——它的默认链 7 项里 2 项当日已 404，接入方必须**启动期+定期用 `/v1/models` 探活过滤链项**（它自己没做，直接死在链首）；② 免费 `:free` 模型普遍 `mayTrainOnYourPrompts: true`（提示词被上游留存用于训练）——兜底语义要与用户约定一致，不能默默把生产流量降级过去；③ 匿名档意味着共享配额，被 Kilo 自己限流的概率随口碑上升，容量当 0 规划；④ 静默换模型 = 质量断崖（它用等价映射表硬凑，我们至少要把降级后的真实模型名回写响应/日志）；⑤ GPL-3.0 只约束抄码不约束接 API，无法务增量。

### 一句话给 P2

它的存在证明了三件事：纯 HTTP 对齐 + 换地理出口就能吃免费档（TLS 派无票）；**免费层跨供应商 failover 在工程上今天就成立**（Kilo 匿名可用，200/8 burst）；以及——failover 链的失败分类（配额墙 vs 源不认此请求）才是这套东西真正的难点，它做了一半，剩下的另一半（404 换位、探活过滤、跨请求熔断）正是我们要做扎实的地方。

---

### 附：本次实测记录（可复现）

| 实验 | 结果 |
|---|---|
| 副本 `npm install / tsc / npm test / build / verify:worker` | 8 包 2s；typecheck 净；**148/148 通过**；bundle 209KB 沙箱过 |
| `node dist/cli.js start` @40240（已杀，40241 未监听） | /healthz 目录 98 模型；/v1/models 匿名暴露 0（需 keys）；doctor=degraded（egress 非 CF） |
| daemon 免费请求 model=big-pickle（CN 直连） | 链：anon /inference 429 → anon /zen 429 → kilo 404（链首死）→ guard 不换位 → **502 吞因** |
| 同请求另一轮 | /inference 403 **RegionError**（=地理封锁实锤），kilo 同请求 404 model-not-exist |
| Kilo 直连探测（`--noproxy`，CN IP） | `kilo-auto/free`、`nvidia/*:free`、`cohere/north-mini-code:free`、`openrouter/free` 200；`reflection/beam-free`、`stealth/crouton-alpha` **404**；8 连发全 200 |
| mock 全链 probe（候选顺序/全灭语义） | 顺序与上限如 §2.3 所记；全灭=429+上游原错误体+hops 头，无 Retry-After |
