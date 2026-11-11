# Top-5 第二轮汇总 + 引擎升级 v1.4.7 可行性评估（2026-11-11 15:20）

> 触发：用户给出 GitHub 搜索 `opencode2api` 按 updated 排序的前 5 仓，要求 clone 到系统自动清理目录后分析。
> Clone 位置：`%TEMP%\oc-refs-20261111\`（Windows 临时目录，系统自动清理）
> 各仓详报：`top5-01-Lukejdjd-manager.md` / `top5-02-6Kmfi6HP-upstream.md` / `top5-04-dengyie-resilience.md` / `top5-05-IMROVOID-edge.md`（#3 是我们自己仓库的公开镜像，主会话直接审计）

---

## 0. 一句话结论

**升级 stock v1.4.7 = 值得做，且现在做风险极低**；它把我们的 L4 直接消掉（**24h 出口冷却其实是 wbi35772 分叉自己加的，stock 一直是 30min**），代价是失去一份**只在 WARP 模式生效、我们当前关着 WARP 所以本来就跑不到**的分叉补丁。**它不修 L3**（`usesWARP()` / `errWARPUnavailable` 空池 `break` 与 stock 逐行相同）——所以 P0 托管层修复照旧要做。

---

## 1. 血统钉死：我们的引擎 = jasonxu114514 v1.4.6 + wbi35772 分叉补丁

上午「引擎上游已死（wbi35772 仓库 404）」的结论**只对了三分之一**：404 的是分叉，正主活着。

```
jasonxu114514/opencode2api   1434★  v1.4.7 = 2026-11-05（活跃，windows_amd64.zip 在 release 里）
        └─ v1.4.6 ──────────► wbi35772/opencode2api-win（已 404）＝ 我们的 bin/opencode2api-win.exe
```

**blob 级证据**（`git ls-tree` 逐文件哈希比对，132 个我们的跟踪文件）：

| 与 v1.4.7 比 | 数量 |
|---|---|
| 逐字节相同 | 80 |
| 不同（含我们分叉改动） | 28 |
| 我们独有（分叉新增） | 24 |

**分叉点定位**（6 个关键文件逐 tag 求差异行数）：

| 对照 tag | v1.4.0 | v1.4.2 | v1.4.4 | v1.4.5 | **v1.4.6** | v1.4.7 |
|---|---|---|---|---|---|---|
| 差异行 | 24046 | 23334 | 2585 | 2428 | **2370 ← 最小** | 4469 |

⇒ 我们基线 = **v1.4.6**；v1.4.7 相对 v1.4.6 只多约 2.1k 行变动，不是大版本跳跃。

---

## 2. 那份补丁到底是什么（决定「升级会失去什么」）

wbi35772 提交标题自述：`v1.4.6 + session-affinity 429 fix`。实际内容（diffstat）：

| 文件 | 行 | 作用 |
|---|---|---|
| `internal/gateway/sessions.go` | +122（新） | `sessionGuard`：免费层 429 时按 (conversation, generation) 铸造新 `ses_`，因为**额度绑 session 不只绑出口** |
| `internal/warp/iface_windows.go` | +266（新） | Windows 下按网卡源 IPv6 绑定出口 |
| `internal/warp/pool.go` | +440 | 出口账本改造：**`egressCooldown` 30min → 24h**、duplicate /64 判定、preload 退避 |
| `internal/warp/exit.go` | +288 | 出口轮换/惩罚分类 |
| `internal/gateway/warp.go` | +154 | 429 归属判定（会话 or 出口）+ `sessionGuard` 接线 |
| `runtime.go` / `upstream.go` / `registrar.go` / `bind.go` / `config.go(+TUN)` | 各 59~169 | 配套 |
| `cmd/warpprobe/` | +83（新） | 出口探测小工具 |

**关键限定（实测 grep 定位调用点）**：`sessionGuard` 的三处调用全在 `warp.go:356 / 432 / 433` —— **只在 WARP 通道生效**。我们当前生产 `warp.enabled=false` ⇒ **这份补丁现在一行业务代码都跑不到**。

对照：stock v1.4.7 `git grep sessionGuard|sessions.Resolve|sessions.Rotate -- internal/gateway/` = **0 命中**（真没有）；但 session 相关能力在 **6Kmfi6HP 那条线**上有等价物（`120f60a replace per-key rotation with global session refresh on 429 exhaustion`）。

---

## 3. 升级买到了什么（三条都是我们今天的痛点）

### ① L4 被直接消掉 —— 而且原因出乎意料

```
stock v1.4.4 / v1.4.5 / v1.4.6 / v1.4.7 ： const egressCooldown = 30 * time.Minute
我们的基线（wbi35772 分叉）      ： const egressCooldown = 24 * time.Hour
                                     const maxEgressCooldown = 24 * time.Hour
```

**上午判定「L4 引擎缺陷：一个坏出口烧整池 24h」需要修正**：24h **不是上游行为，是分叉作者自己升级的**（注释理由：「免费层把额度绑在这个块上，同一块里新建隧道只会得到同样答案」）。stock 的 30min 意味着：**同样的事故在 stock 上最多 30 分钟自愈，而我们实际烧了 40+ 个 /64、挂了 21 小时**。

⇒ 顺带修正 `opencode2api-win.md` 里「`pool.go:29` 硬编码 24h 不可改」的表述：不可改是事实，但它是**分叉的选择**，不是协议约束。我们自己的 `freetier_engine.go:188` 把匿名熔断 `max_hours` 对齐成 24h 的整段推理，前提也要重估。

### ② v1.4.7 正在修的就是我们踩的坑

release 正文（v1.4.7）三条正中要害：
- 免费层工具调用整形：官方工具名 ↔ 客户端声明名互映射，Claude Code 不再报 `No such tool available: shell`；清除历史里的幽灵调用
- **OpenCode 会话标题请求不再注入官方工具集 → 不再触发 `403 FreeTierError` / `429 FreeUsageLimitError`**
- Chat 串流不再泄漏拆分的工具名

（标题请求 403/429 这条与 dengyie 报告里「目录 list 恒 200 会抹掉额度状态」是同一类污染问题，两个独立项目都撞上了。）

### ③ 恢复「有官方修复可拿」的通道
404 的分叉让我们处于永久冻结；stock 有 release feed + 二进制 + sha256（本次实测下载通过）。

---

## 4. 实测：stock v1.4.7 吃我们现在的配置（40250 隔离实例）

流程与结果：

1. 下载 `opencode2api_v1.4.7_windows_amd64.zip` + `.sha256`，**SHA256 校验通过**
   `075c226cc78e5ae79678729db35aaf46aed6873d4a6487abd93f7263a74f71e2`
2. 直接复用**生产那份 config.json**（只改 `listen` → 127.0.0.1:40250），未增删字段
3. 引擎是 `DisallowUnknownFields` 严格解析（历史上写错一个类型就起不来）——**启动成功，无解析错误**

| 检查 | 结果 |
|---|---|
| `/healthz` | `status=ok ver=v1.4.7 keys.zen=10 anonymous=false warp=无字段 models=12/44`（与生产逐项一致） |
| `POST /v1/responses` ×3（beam-free） | **3/3 = 200**，790 / 863 / 1018 ms |
| `POST /v1/chat/completions` ×1（glm-5.3-flash-free） | **200**（10.7s，冷 session 首包） |
| 清理 | 进程已杀、40250 free；**生产 40000/40080 全程未动**（复核 pid=18788 健康） |

**没有观测到的兼容性问题**：配置字段零报错、模型目录一致（12/44）、免费层推理正常。

唯一需要留意的字段：我们分叉在 `config.go` 加了 `TUN bool json:"tun,omitempty"`。它是 `omitempty` 且我们的 `WriteEngineConfig` **从不写它** ⇒ 不构成障碍（实测已证）。

---

## 5. 升级方案（推荐 R1，待批准）

**R1 · 二进制替换，保留回退（成本≈10 分钟，可秒级回滚）**

| 步 | 动作 | 验证 |
|---|---|---|
| 1 | 新文件放 `bin/opencode2api-win-v147.exe`（**不覆盖**现有 exe，避开「运行中被占用」历史问题） | 哈希对上 |
| 2 | `core/manager/freetier_engine.go:28` `freeTierEngineBinName` 走**可配置**（新增 `engine_bin_name`，默认仍 `opencode2api-win`） | `go build ./...` + free-tier 测试 |
| 3 | 面板切到 v147 → 重启引擎 → 跑 §4 同一组实测（healthz 12/44 + 3 条推理 + 1 条 chat） | 与 R0 数据逐项对齐 |
| 4 | 观察 24h 真实流量的 429/503（重点看 key 通道是否出现「session 被墙但不轮换」的新形态） | `request_routed` 分布对比 |
| 5 | 任一项不符 → 把 `engine_bin_name` 改回旧值，重启即回退 | — |

**要不要顺手把 `freetier_engine.go` 里那三个对齐 24h 的常量改短？** —— **本轮不改**。理由：匿名熔断 `max_hours=24` 只在 WARP 模式起作用，我们 WARP 关着；等 R1 观察期结束、确定是否重新引入 WARP 再一起决策，避免一次改两个变量。

**被否决的替代**：
- **R2 从源码构建 v1.4.7 再把 sessions.go 摘回来**：等于我们自己维护一个 Go 分叉（stock 要 Go 1.24+、我们生产链是二进制分发），把「等官方修」换成「自己长期背一个 fork」。不值 —— 那份补丁当前跑不到。
- **R3 换成 6Kmfi6HP 上游**：见 `top5-02` —— 它是**我们主仓库的父项目**，`All rights reserved`（明文禁止复制/修改/分发/使用），且与我们已分家 833 个提交；只能抄结论，不能拿代码，也不能当引擎跑在分发产物里。

---

## 6. 修订上午的结论（哪些作废 / 哪些不变）

| 上午结论 | 现在 |
|---|---|
| 「引擎上游 404 已死，永无官方修复」 | ❌ **作废**：分叉死了，正主活跃（v1.4.7，11-05） |
| 「L4 = 引擎缺陷，出口冷却固定 24h 不可改」 | ⚠️ **修正**：24h 是 wbi35772 的自加分支，stock 恒为 30min；L4 的**结构性**问题（`errWARPUnavailable` → break 整个 tier）stock 同样存在，仍然成立 |
| 「E2 直连 403 = 地理封锁」 | ⚠️ **削弱为部分成立**：见 `99-VERDICT.md` 顶部修订（同一台机器上，6Kmfi6HP 直连拿到 **429 FreeUsageLimitError**、IMROVOID 直连拿到 **403 RegionError**；TLS/JA4 指纹说被 IMROVOID 的 Workers 形态否证） |
| 「L1 孤儿 pid + L2 自愈看配置」死锁 = 首要根因 | ✅ **不变**，且已被止血动作反向证实（杀孤儿 → 5s 自愈 → 3/3 200） |
| P0 四项 / P1 三机制 / P2 决策 | ✅ **不变**，新增可抄件见 VERDICT 顶部修订表 |

---

## 7. 止血结果（用户已授权，15:08 执行）

| 项 | 值 |
|---|---|
| 动作 | 先校验 `ExecutablePath` 确为 `D:\Program Files\opencode2api\bin\opencode2api-win.exe`，再 `Stop-Process -Id 40176` |
| 恢复 | **5 秒**后 40080 重新监听，新 pid **18788**（看门狗自动重启，**首次**加载 11-11 00:25 那份配置） |
| 新进程配置 | `keys.zen=10 anonymous=false`、**无 warp 字段**、`proxies healthy=1/1`、`models 12/44` |
| 真实推理 | **3/3 = 200**（741 / 804 / 1092 ms）；上一条 200 还是 11-10 17:05 ⇒ **连续 21 小时的全灭结束** |
| 管理器状态 | `state=ready healthy=true pid=18788` —— **pid 不再为 0**，L1 的表现当场消失（但代码没修，重启管理器仍会复发 ⇒ P0-1 照做） |
| `warp_fallback` | `enabled=false applied=false strikes=0` —— 现在与实况一致（WARP 真关了），不再自相矛盾 |

---

## 8. Kilo Free Gateway 复核（主会话亲自实测，修正 top5-05 的乐观表述）

子代理报「CN 直连 200、零账号」，我自己重跑一遍确认，但**发现四条它没充分说明的限制，足以改变结论**。

**认证：确实零账号**（不是绕过，是它的设计）
- `kilo.ts:10` `KILO_ANONYMOUS_TOKEN = "anonymous"` → `:180` `Authorization: Bearer anonymous`，注释原文 "**keyless** Kilo Free Gateway"；
- 进一步：**连 Authorization 都不发也 200**（实测 A 组）；
- README 宣传的 `KILO_API_KEY` 在 `src/**/*.ts` 里**零命中**（grep 确认）⇒ 文档漂移，密钥路径根本不存在。所以「无账号」是稳定设计，不是我们蹭到的漏洞。

**可用性：真的通**（CN 直连、无代理）

| 模型 | 结果 |
|---|---|
| `nvidia/nemotron-3.5-lightning:free` | 200，`content=[KILO-OK]`，`cost=0`、`is_byok=false`，provider=Nvidia |
| 连发 8 次 | **8/8 = 200**（2.0–5.6s），无任何 rate-limit 响应头 |
| SSE 流式 | 200，116 个 `data:` chunk 正常 |
| `/models` 免密可列 | 385 个模型，其中 11 个 free |

**但四条限制是硬伤（比「目录漂移」更关键）**

1. 🔴 **11/11 个免费模型 `mayTrainOnYourPrompts = true`**，且 NVIDIA 免费端点 ToS 原文写进 description：**"Trial use only — do not submit personal or confidential data. Your use is logged for security purposes and to improve NVIDIA products and services."** ⇒ 我们的流量是 **coding agent 流量（含用户私有代码）**，接这条等于把代码送给第三方训练。**这一条单独就能否决默认接入。**
2. 🟠 **它自己的默认链今天已 2/7 死**：`reflection/beam-free` 与 `stealth/crouton-alpha` 实测 **404**（`kilo.ts:38-46` 硬编码），且 404 不在它的归一化分支里（`worker/unlimited.ts:226-241` 只归一 `key==="public"` 的 4xx）⇒ 链首即卡死。**抄它的链 = 抄一份会过期的清单**，我们必须自己探活过滤。
3. 🟠 **静默换模型**：`kilo-auto/free` 实际返回 `nvidia/nemotron-3.5-super-vl:free`，`openrouter/free` 实际返回 `recursive/flint-v1.0:free`（实测响应 `model` 字段）⇒ 客户端指定什么不保证拿到什么，**必须把真实模型名回写响应与日志**，否则「模型名≠实际能力」直接污染调用分析。
4. 🟡 **`max_tokens` 给小会看到假失败**：`max_tokens=16/64` 时 `content: null` + `finish_reason=length`，token 全烧在 `reasoning` 上（这批多是推理模型，实测 reasoning_tokens=201）。我们诊断页/probe 那种极小 max_tokens 的打法在这条 lane 上会**全判失败**——探测预算至少 400。

**修正后的判断**：子代理给的「last-resort 值得接一条」我**下调为「默认不接，只做可选开关且限定非敏感流量」**。零账号 + 零限流头（无从得知自己的配额地位）+ 全量训练授权 + 静默换模型，组合起来不适合当编码通道的兜底。真要接，前置条件三条缺一不可：① 启动期 + 定期 `/models` 探活过滤死模型 ② 真实模型名回写 ③ 面板上对该 lane 的请求**显式标注「提示词可能被上游用于训练」**并默认关闭。

> **16:20 用户决策覆盖本条**：仍要挂（作为与 OpenCode 并列的免费供应商），但**必须在页面标明数据可能被训练**。已按此执行，实现与验证记录见 `top5-98-kilo-mounted.md`；上述三条前置里的 ② 与「auto 候选隔离」仍在该文档 §6 作为未完成缺口登记。

---

## 9. 一句话给决策

升级 v1.4.7 = 低风险高收益，建议做（R1，待批准）；Kilo = 能通但不该默认用（隐私），P2-B 的兜底价值要重估。
