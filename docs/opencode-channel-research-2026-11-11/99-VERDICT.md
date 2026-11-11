# 结论：OpenCode「无通道可用」根因与方案选型（2026-11-11）

> 证据文件：本目录 `00-current-state-and-tests.md`（现场量化 + E1/E2/E4 实测）、`opencode2api-win.md`（引擎，455 行）、`Opencode2api.md`（656 行，含实机）、`9router.md`（816 行）。
> 计划：`docs/ai-framework/plans/2026-11-11-opencode-channel-stability.md`。

---

## ⚠️ 15:10 修订（GitHub 搜索 Top-5 第二轮，见 `top5-0*.md`）

本文档有两处结论**被第二轮推翻/削弱**，其余（根因 L1/L2、P0/P1 方向）不变：

1. **「引擎上游已死、永无官方修复」——作废**。wbi35772/opencode2api-win 只是分叉；**真上游 = `jasonxu114514/opencode2api`（1434★，活跃）**，blob 比对确认同源（108 共同路径、80 逐字节相同）。**v1.4.7（2026-11-05）比我们 v1.4.6 基线新**，且更新日志正修「免费层 403 FreeTierError / 429 误伤 / 标题请求注入工具集」等我们踩过的坑。⇒ 新增任务：v1.4.7 升级评估（先 diff `internal/gateway/warp.go`、`upstream.go`、`internal/warp/pool.go` 与我们 28 个差异文件的冲突面）。Lukejdjd 管理器（top5-01）管理的正是 jasonxu114514 二进制——**同一条链已被第三方生产使用**，升级可行性得到旁证。
2. **E2「直连 403 = 地理封锁」——降级为部分成立**。第二轮两份实测相反并存：6Kmfi6HP HEAD 二进制**本机 CN 直连、零 key** → `/v1/models` 200、推理 → **429 FreeUsageLimitError（非 403）**，其 labs 消融矩阵证明门禁认的是**请求形态**（session 反转时间戳/路径/tools manifest/`<env>` 块），UA/project/x-opencode-request 实测不参与；而 IMROVOID（跑在 Cloudflare Workers，TLS 由平台代打）同机直连拿到 403 `RegionError`，同时它也**否证了 TLS/JA4 指纹说**（Workers 改不了 ClientHello 却能工作）。⇒ 修正：**7890 海外代理继续用（换额度桶 + 缓存分片），但「地理」不写进架构假设；uTLS/curl-impersonate 不投入**。

第二轮新增进方案的机制（来源与行号见各 top5 报告）：

| 机制 | 来源 | 落到 |
|------|------|------|
| **quota preflight**（纯本地账本；全池墙 ⇒ 零上游调用、毫秒级 429+Retry-After；单 key 被墙不误伤全池） | dengyie `pool.go:885-897`、`quotaprobe.go:326-359`、`gateway.go:407-421`（实测 Retry-After 25→18 递减） | P1-1/P1-2 主件：`freetier_engine.go` + `vendors/custom/keypool.go` |
| **额度维度定案**：账号 × 出口 IP 双维、按请求数计 | dengyie PLAN-v1.6.0:13、CHANGELOG:59-60 | P1-2 账本 schema |
| **MarkListingSuccess ≠ MarkSuccess**（目录 list 恒 200 会抹掉额度状态——同款坑我们必踩） | dengyie `pool.go:869-877` | `freetier_models.go` |
| **pid 文件 {pid,binary} + kill 前 Win32 路径全等 + 「成功=观测到期望值」** | Lukejdjd `platform.ts:110-116/41-58/123-144/363-372` | **P0-1 直接照此实现**（它刻意 fail-closed 不领养孤儿，领养逻辑我们自己加 `listPortPids`） |
| **第二免费源 Kilo Free Gateway**（零账号，CN 直连实测 200/~1.4s/8 连发无限流） | IMROVOID `src/unlimited/kilo.ts:38-46` | P2-B 的 last-resort 候选（接之前必须：探活过滤 + 真实模型名回写 + 容量按 0 规划） |
| WARP 出口观测的**呈现框架**（counts/observations 双层、秒级倒计时、三种 unavailable 文案、免责声明） | Lukejdjd a70e27c（注意：其「固定 +30min」估法是假的，实测只认出 1/40 条账本） | P0-4 用我们 `WarpCooldown()` 真账本当主数据 |

**许可红线（影响回灌候选）**：6Kmfi6HP 的 LICENSE 是 **All rights reserved**（明文禁止复制/修改/分发/使用）⇒ **「反向回灌上游代码」从候选移除**——抄机制（读它验证过的结论）可以，抄代码不行；GPL 引擎的独立进程用法反而是现有链路里法律上最干净的形态。dengyie/IMROVOID 均为 GPL-3.0，同样只能独立进程或重写进 Go。

**止血已完成（用户授权）**：15:08 杀孤儿 40176 → 看门狗 5s 内重启（新 pid 18788）→ 新进程 `anonymous=False`、无 warp 字段、3/3 真实请求 **200（741–1092ms）**，`/free-tier/status` 的 **pid 不再为 0**，`warp_fallback.enabled=false` 与实际一致。连续 21 小时的免费层全灭结束。

---

## 一句话结论

**今天不是上游把额度打光，是我们自己把通道焊死在一条已经废掉的 WARP 出口上。**
两处叠加即可完全解释 100% 的 503：

1. **引擎侧（GPL 独立程序；~~上游仓库已消失~~ 见顶部修订：真上游 jasonxu114514 活跃）**：只要 `warp.enabled=true`，**所有免费模型请求——包括带 key 的——都被强制走 WARP**；池空时直接放弃整个 key tier，连一次上游都不打（响应自述 `the upstream was not contacted`）。
2. **我们托管侧（本仓库，可改）**：引擎进程成为**孤儿**（管理器 `pid=0`），「保存配置」「重启引擎」全是**空操作**——11-11 00:25 就写好了「关 WARP」的配置，进程 11-06 起就没换过。看门狗又因为「配置说 WARP 已关」而拒绝自愈 → 死锁。

同一台机器、同一批 10 个 key，**只关掉 WARP 就 5/5 通过**（E1）。所以「更好的方式」不是换项目，而是**别让一条 lane 的生死决定整条链路的生死**，并且**让配置真的能落地**。

---

## 1. 三仓对比

| 维度 | 我们的 40080 引擎（opencode2api-win 9964407/v1.4.6） | Opencode2api（Nystic-Shadow，b9b81dc/v3.2.4） | 9router（decolua，本地 87a74e1b/v0.5.104，落后 99 提交） |
|------|------|------|------|
| 形态 | Go 单二进制，独立进程 | Node（需 Bun 指纹桥 + `@opencode/cli`） | Next.js16 + React19 + SQLite 全栈面板 |
| 免费层通道 | WARP 匿名出口 → zen key → go key | **纯代理出口池**（WARP 已整块删除 `2f12aa6`） | **没有 WARP**：代理池 round-robin（`connectionProxy.js:18-34`） |
| key 池 | 10 个 zen key（但 WARP 模式下被绕过） | **无 key 池**（只有 1 个 zen key，多样性全押出口 IP） | 137 provider / 20+ freeTier，key/OAuth/匿名/cookie 多形态 |
| 一条通道废了会怎样 | **全废**：`usesWARP()`（warp.go:39）+ 空池 `break` 整个 tier（upstream.go:1041-1045） | 出口逐个记账，全冷却时报 **429 + 真实 Retry-After**（proxy.js:469-485），死池 1–7ms 返回 | 「自救→换账号→换模型」三段降级（chatCore.js:437-482 → chat.js:246-263 → combo.js:340-419） |
| 冷却维度 | 出口按 /64、**固定 24h、无递增、持久化**（pool.go:29/516-527）⇒ 重启不自愈 | 出口 + model + (key×出口) 三层；同 host 兄弟端口共享冷却（egress.js:1450-1457） | **connection × model 二维**账本 `modelLock_<model>`（accountFallback.js:120-176），成功即清 |
| 失败语义 | 503 + 25 秒陪跑等待 | 429 + Retry-After + `/readyz` 逐出口账本 + `clear-quota`/`recheck` 运维端点 | 三分支语义（全 429→503+retry-after / 无凭据→404 / 试完→回吐真实码） |
| 上游活跃度 | ~~仓库 404 不可达~~ **修订**：wbi35772 是死分叉，真上游 `jasonxu114514/opencode2api` 活跃（v1.4.7，1434★，11-05）⇒ 升级评估进行中 | 114 提交 / 2.5 周 / 单作者 / 已 8 天无提交；本地比 origin 多 1 个提交 | **60 天 316 提交**，几乎每版都在修 OpenCode 指纹/端点搬家 |
| 许可 | GPL-3.0（我们以独立进程 mere aggregation 使用） | MIT | MIT |
| 适不适合换过去 | — | **不适合**：无 key 池，只能吃已耗尽的匿名桶 ⇒ 产能比现在低 | **不适合**：接管=接过「持续对抗上游指纹」的义务，且架构不匹配 |

**选型结论：两个参考实现都「不替换、只抄机制」。** 最有价值的不是它们的代码，而是它们**用行动给出的同一个判据**：Opencode2api 走过 WARP 这条路 40+ 个提交后**整块删掉了 WARP**（`2f12aa6`），9router **从来没上 WARP**。我们押注的这条 lane，是被两个先行者各自否决过的方案。

---

## 2. 实测数据（本次亲测，非引用）

| 实验 | 条件 | 结果 |
|------|------|------|
| 现场量化 | 生产 40080，4252 条 `request_routed` | 200:3139 / **503:921（21.7%）**；按天 10%→1%→20%→52%→71%→**今日 100%**；921 条 503 **全部**为 `WARP pool has no ready egress`，且均匀分布在**全部 10 个 key** 上（⇒ 不是 key 用尽） |
| 复现 | `POST 40080/v1/responses` beam-free | **26 393ms → 503** `warp_unavailable`，message 含 `the upstream was not contacted` |
| **E1** | 独立实例 40180：同 10 个 key + `warp.enabled=false` + `anonymous=false` + `proxies=[7890]` | **5/5 = 200**（696–1633ms）；SSE 首包 906ms、38 事件、1.08s 完成；日志 `tier=zen channel=key anonymous=false` |
| **E2** | 同上但 `proxies=[direct]` | **403** `This model is not available in your country` ⇒ 海外出口是**必要项**，不是可选项 |
| **E4** | 起在 WARP 模式（healthz 有 warp / active=1）→ 改盘关 WARP → `POST /api/config/reload` | **`applied=true, restart_required=false`**；reload 后 warp 字段消失、`keys.anonymous=false`；随后 **3/3 = 200**（913–1549ms）⇒ **热切成立，不需要重启窗口** |
| 热加载白名单 | `runtime.go:536-545` | 仅 `listen` / `webui.listen` / `webui.enabled` 需重启；`warp.enabled`、`anonymous`、`proxies`、`zen_keys` **全部可热应用** |
| **R1 repro** | `go test -tags research ./core/manager/ -run TestDriftRepro`（隔离 dataDir + 40187 + 假 key） | **PASS**，逐条复现：① `FreeTierEngineStatus().PID=0`（管理器不掌握进程）② 点重启后引擎配置**一字未改** ③ 进程内生效 key 数仍是旧的 1 ④ `engine_warp_enabled=false` 时 `maybeAutoFallbackWarp` **直接 return false**（死锁另一半）⑤ 对照 A：端口空出后 `WriteEngineConfig` 立刻写出新配置 ⇒ 故障来自 `Start` 的 `probePort` 短路 ⑥ 对照 B：新进程按 2-key 启动 → `/healthz keys.zen=2` ⇒ 证明「无通道」= 孤儿 + pid 未跟踪，**与 key/额度无关** |

顺带纠正参考实现里两条与实测冲突的说法：
- 9router 说「真 `oc_sk_*` key 打 `-free` 模型会 403、必须 `Bearer public`」（opencode-zen.js:285-292）——**E1 直接反驳**：`channel=key tier=zen` 5/5 200。
- 它的「TLS/JA4 指纹门」假说——**E1/E2 给出更简单的解释**：差异变量就是有没有海外出口（直连 403「your country」），不必引入指纹假设。

---

## 3. 根因分层（别混为一谈）

| 层 | 事实 | 证据 | 谁能修 |
|----|------|------|--------|
| **L1 托管层（本仓库，P0）** | 引擎进程成孤儿：`Stop` 只 kill 内存 pid（重启后为 0）→ 谁都不杀；`Start` 见端口在听就 `return nil` → 永不写盘/重启；面板显示「重启成功」实为空操作 | `freetier_engine.go:421-441`、`:362-366`；实测 `pid=0`；R1 repro ①②③ | **我们自己，代码可改** |
| **L2 自愈层（本仓库，P0）** | 自动降级以**配置**为准而非**观测**为准：配置 `engine_warp_enabled=false` 时直接 `return false`，而进程里 WARP 还活着 → 拒自愈 | `freetier_warp_fallback.go:165-173`；R1 repro ④ | **我们自己** |
| **L3 引擎层（GPL 外部程序，不可改源码）** | `usesWARP()` 第 3 个条件 `tier==TierZen` 不区分有无 key ⇒ 带 key 的免费请求也被锁进 WARP，proxy 分支被 `if !useWARP` 整体门控；空池 ⇒ 第一次就 `break` 放弃整个 tier | `warp.go:39`、`upstream.go:982/1041-1045`、`gateway.go:365` | **只能用配置绕**：`warp.enabled=false`（E1/E4 证明有效） |
| **L4 出口账本** | 一个坏出口烧整池：`egressCooldown` 硬编码 **24h、无递增、无 strikes**，按 /64 记账；Windows 上 `source_ipv6` 是空操作 ⇒ 重注册撞同一 /64 → `Discarding duplicate` → 池长期 0；黑名单**持久化**在 `config.json.warp-state.json` ⇒ **重启也不自愈** | `pool.go:29/513/516-527/1002/1015`、`runtime.go:319-327`；现场 warp-state 里 40+ 个 /64 排到 11-12 | ~~只能弃用 WARP~~ **修订**：24h 是 wbi35772 分叉自加（stock 恒 30min），升 v1.4.7 即消掉；`break` 整个 tier 的结构性问题 stock 同样存在，仍靠关 WARP 规避 |
| **L5 架构层（本仓库）** | **免费 lane（40080）与付费/自定义 lane（50080）是两个互不相干的入口**，40000 根本不代理推理（`main.go:9`）⇒ 免费层全挂时**没有任何自动兜底**，客户端只能自己失败 | `main.go:6-9`、`models_source.go:4`、`upstream.go:4` | 需要设计决策（见方案 P2） |

> 引擎自带中文文档有两处**与代码不符**，别再照它操作：`doc/05-故障排查.md:38` 说「关 WARP 要重启」（reload 即可，E4 实测）；`doc/03…md:235` 说冷却「30m→2h→8h→24h 递增」（当前基线固定 24h）。

---

## 4. 推荐方案

### 立即止血（0 代码，1 条命令，需你授权——会重启生产 40080）

```powershell
Stop-Process -Id 40176 -Force   # 杀掉孤儿引擎；30s 内看门狗 probePort 失败 → RestartFreeTierEngine → 端口已空 → 正常写盘 + 启动新进程（warp 关）
```

配置里 `engine_warp_enabled=false`、`engine_channel_order=key_first` 已经写好了，杀掉孤儿后新进程就会按 E1 那份配置启动。预期：503 归零，回到 E1 的亚秒级 200。**风险**：约 30–60s 免费层不可用；WARP 一旦关闭就不再有匿名免费额度（key 桶承担全部流量），若后续 key 桶打不出额度，再按 P2 讨论是否重新引入 WARP。

### P0 · 本仓库代码修复（根因，必做）

| # | 落点 | 改法 | 验收 |
|---|------|------|------|
| P0-1 | `core/manager/freetier_engine.go` `Stop`/`Start` | **停止依赖内存 pid**：`pid==0` 时用已有 `listPortPids(port)`（`netstat_windows.go`）认领端口持有者并按**进程名 = `opencode2api-win`** 校验后才 kill；`Start` 的 `probePort` 短路改为「端口活着 **且** 引擎 `/healthz` 有效配置 == 期望配置」才跳过；否则写盘 + 应用 | 把 repro 测试转正（去 `research` tag）：Stop+Start 后引擎配置**必须**变化、`/healthz` key 数 == 面板值 |
| P0-2 | `core/manager/freetier_warp_fallback.go` `maybeAutoFallbackWarp` | 判据从「配置开关」改成「**观测**」：`health.Present`（进程里 WARP 真的在跑）就计入 strikes，不论配置怎么说；并在「配置关 / 进程开」时直接判定为漂移 → 触发应用 | 新增用例：`engine_warp_enabled=false` + `health{Present:true,Active:0}` → **必须**返回 true |
| P0-3 | `core/manager/freetier_engine.go` 引擎配置 | 打开引擎自带管理口（`webui.enabled=true`、`listen=127.0.0.1:<引擎管理端口>`、随机口令写入本地、仅监听回环），配置变更后**先 `POST /api/config/reload` 再决定是否重启**（E4 证明 reload 可热应用 4 项关键字段） | 改 `zen_keys`/`warp.enabled` 后不重启进程即生效；面板不再出现「配置已保存但未生效」 |
| P0-4 | `core/manager/freetier_engine.go` 状态语义 | 面板/`/healthz` 增「**配置指纹 vs 生效指纹**」比对，不一致直接显示「漂移」并给一键应用；WARP 关闭时 `warp_cooling` 不再恒 0（现有 `WarpCooldown()` 已能读盘，补进状态契约） | 面板能看到「40 个 /64 冷却到 11-12」这种真实账本 |

### P1 · 抄参考实现的三个机制（提升韧性，本仓库自研 Go）

| # | 机制 | 来源（证据） | 落点 |
|---|------|--------------|------|
| P1-1 | **失败语义诚实化**：lane 判定为「不可用」时**立刻**返回 429/503 + 真实 `Retry-After`，不做无意义的 25s 陪跑；死池 1–7ms 返回 | Opencode2api `proxy.js:469-485`、`egress.js:1046`→`proxy.js:1166`；对照现状 26 393ms | `core/manager/freetier_models.go` / 面板错误透传 + `gateway_timeout.go` 超时预算 |
| P1-2 | **二维冷却账本（凭据 × 模型）**：冷却记在「这条凭据服务不了这个模型」，而不是把整条 lane 一起打死；成功即清 + 惰性扫 | 9router `accountFallback.js:120-176`、`auth.js:238-295/306-345`；`errorConfig.js:32-77` 表驱动 + 指数退避 | `vendors/custom/keypool.go`（现有 401/403 冷却升级为 `openUntil`/`quotaRetryAfterUntil` + host 维度共享，参考 `egress.js:1450-1457`） |
| P1-3 | **兜底通道查账本、不查开关**：能否直连由「直连出口自己的冷却/失败记录」决定 | Opencode2api `proxy.js:106-126`（`:120-122`）—— 正是我们 L2 死锁的反例 | `core/manager/freetier_warp_fallback.go`（与 P0-2 合并实现） |

（**不抄**：Opencode2api 的公网代理爬虫——作者本人 10-26 同日自述矛盾；它的 `upstreamQuota.recordFailure` 在生产路径**零调用点**，抄设计不抄现状；9router 的静态 priority 排序——我们的滑动窗口 SR 健康分 `auto_model.go:94-102` 更强，别丢。）

### P2 · 架构决策（需要你先拍板，本阶段不做）

真正能消灭「无通道可用」的只有一件事：**同一模型多源候选 + 免费 lane 失败自动落到付费/自定义源**。但当前 40080 与 50080 是两个独立入口、40000 不代理推理（L5），所以这需要先决定形态：

- **A. 引擎内自治**（不推荐）：受 GPL 上游限制，我们改不了它的 tier 放弃逻辑，只能在外面兜。
- **B. 管理器增设统一推理入口**：40000（或新端口）做一层薄代理，按「候选列表 + 健康分」在 `[引擎 40080]` 与 `[vendors/custom 各源]` 之间 failover，复用 `core/router/router.go` 的 `Candidates()` 与 `auto_model.go` 的健康分。工作量最大（≈1 阶段），但这是唯一把「无通道」变成「换通道」的位置。
- **C. 只做可观测 + 半自动**（最省）：保留双入口，面板在免费 lane 判定不可用时明确提示「已降级/请切 X」，并把 P0/P1 的账本可视化。等于承认「不自动兜底」，但杜绝了「说不清为啥挂了」。

我的建议顺序：**立即止血 → P0（本阶段后另立实现阶段）→ P1 → 再评估 P2 选 B 还是 C**。

---

## 5. 为什么不建议换掉引擎

1. **产能会更低**：Opencode2api 无 key 池，只剩已烧光的匿名桶（今日 `Retry-After ≈ 80385/79907s`，指向 UTC 午夜）；我们有 10 个 key 且实测可用（E1）。
2. **不是它更稳**：它自己也在被上游掐（匿名桶整体烧光），只是**如实返回 429** 而不是伪装成 503。
3. **义务不对等**：9router 60 天 316 个提交里大量是在追 OpenCode 的端点搬家与请求指纹（`5bdcd5e7` 端点、`95dafae3` 工具名含 "terminal" 就 403）。接管等于接过这份持续对抗义务，而我们真正需要的是「一条 lane 死了别带着全链路死」。
4. **合规更清爽的是自研**：40080 引擎是 **GPL-3.0** 独立进程；两个参考实现都是 **MIT**，照它们**自己写 Go** 是法律上最干净的参照（比逆向我们的 GPL 引擎更安全）。所以「参考实现」的正确用法是**样本库**，不是替代品。

---

## 6. 残留与风险

| 项 | 说明 |
|----|------|
| 生产仍是坏的 | 未经授权未止血；40080 今天 100% 503 |
| key 桶容量未知 | 关 WARP 后 10 个 zen key 独扛全部免费流量，可能转向真 429（E1 只有 5 条）。止血后需按天看 `request_routed` 的 429/`legs` |
| 出口依赖 | E2 证明必须走海外代理（7890）；代理挂了免费层即挂——这是新的单点，P1-2/P1-3 之外应加代理健康度 |
| ~~引擎不可升级~~ **改：升级评估已完成，建议执行 R1** | 详见 `top5-99-consolidation-and-upgrade.md`：我们基线 = jasonxu114514 **v1.4.6 + wbi35772 分叉补丁**；stock **v1.4.7 已实测**吃我们现有配置（SHA256 校验、healthz 12/44 一致、推理 3/3 200）。**L4 的 24h 出口冷却是分叉自加的，stock 恒 30min** ⇒ 升级直接消掉它；代价仅是失去只跑在 WARP lane 的 sessionGuard（我们 WARP 关着，当前跑不到） |
| repro 文件 | `freetier_drift_repro_test.go` 带 `//go:build research`，不进常规 `go test ./...`；P0-1 完成后应转正为回归锁 |
| 实验实例 | 40180–40189 已全部杀掉，端口 free；生产 40000/40080 未被触碰（已复核） |
