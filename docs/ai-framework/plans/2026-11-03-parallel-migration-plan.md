# 并行迁移方案：把参考项目的节点/出口能力搬进 enhance（不动现有 opencode 逻辑）

- 日期：2026-11-03
- 分支：`feature/real-exit-probe`（已存在，HEAD `90f0bef`）
- 状态：**待确认，未实施**
- 前置：`docs/ai-framework/plans/2026-11-03-real-exit-probe.md`（T1~T8，只覆盖巡检打真出口）

## 0. 为什么是这个方案（三条已验证的事实）

1. **免费额度是 UTC 日窗口的全局桶，与出口 IP 无关。**
   实测：直连与本机代理（美国 GTT 机房 IP）同一分钟请求，`Retry-After` 分别 56330 / 56329，终点同为 `23:59:58 UTC`；换模型同样如此。
   → **「一个 IP 绑一个实例」在额度维度上不产生任何收益。**

2. **出口 IP 唯一还有用的地方是地域限制。**
   实测 `beam-free`：直连 `403 RegionError: not available in your country`，经美国代理 `429`（过了地域门禁，才撞到额度）。
   → 出口池的价值是「**绕地域封锁**」，不是「刷额度」。这决定了新菜单该展示什么指标。

3. **enhance 现在的两个巡检，一个真一个假。**
   - 节点池「一键探测」是真的（`core/manager/probe_node.go:51`，每节点独立 sing-box + 探针进程）。
   - 实例池「质量分」是假的（`core/manager/poolquality_socks.go:166` 打 `127.0.0.1:<port>/v1/models`，命中 `chat_handler.go:268` 的进程内 `modelsCache`；实测 0.0008s）。
   - 同文件里 `httpGetViaSocks`（`:129`）已写好但零调用点。

## 1. 设计原则

**并行，不替换。** 新增的能力全部走新的配置键、新的端点、新的页面；现有 `vendors/opencode`、`upstream.go`、`instances.json` 的行为在开关关闭时**逐字节不变**。

这样做的理由：你说要先跑一段时间确认没问题。只有「关掉开关 = 完全回到今天的行为」才能做这个确认；一旦改成原地替换，出问题时无法归因。

三条硬约束：

- **C1 正向白名单。** 任何透传给上游的 key，必须等于配置项里的值才放行。因为 `unifiedGatewayKey = "sk-unified-local"`（16 字符）和 `genSkKey()`（35 字符）都满足 `isValidOpenCodeKey`（`upstream_types.go:84`），白名单反向做（黑名单）会把实例密码漏出去。
- **C2 额度闸门只读不猜。** 只有上游原始 `FreeUsageLimitError` 才记账（现有 `isFreeUsageLimitBody` 已如此）。本地合成的 `rate_limit_exceeded` 不算，否则一次普通限流会把免费层锁 18 小时。
- **C3 不新增后台进程。** 新探测复用现有 sing-box + 探针二进程模型，不引入第三类常驻进程。

## 2. 分阶段任务

### P0 修 bug：Clash 外部节点读取用错端点（独立、可先做）

**问题**：`core/manager/clash_parse.go:236` 调 `GET /configs` 读 `payload.Proxies`；实测该端点不返回 `proxies` 字段（真实 Clash 9097 返回 44 个字段，无 `proxies`）。正确端点是 `GET /proxies`，返回 `{"proxies": {name: {...}}}`（对象，不是数组）。

**改法**：
- `listExternalNodes` 改调 `/proxies`，解析对象形态；对 `all` 字段非空的条目（策略组）跳过。
- 保留 `/configs` 调用仅用于探测「控制器是否在线」。
- `nodeFromJSONMap` 需要接受 `{"name","type","server","port",...}` 形态（Clash `/proxies` 的条目结构与 `/configs` 不同，需实测对齐）。

**验证**：配 `clash_external_url=http://127.0.0.1:9097` + `clash_auth_token=1270019097`，`GET /api/admin/nodes` 应返回 9 个真实节点（当前 22 个全部来自本地 profile）。

**风险**：这是唯一改动既有行为的任务。若担心，可加 `clash_use_proxies_endpoint`（默认 true）+ 失败回落 `/configs`。

---

### P1 新增「出口池」菜单（对标参考项目的 Egress Proxy Pool）

**为什么单开一个菜单而不是塞进节点池**：节点池管的是「sing-box 能拨通的代理协议」（vmess/vless/trojan/hy2…，需要 sing-box 进程）；出口池管的是「HTTP/HTTPS/SOCKS5 隧道」（参考项目那套，直接 CONNECT，不需要 sing-box）。两者生命周期、探测方式、失败语义都不同，混在一起会让现有节点池页更复杂。

**新增文件**：
- `core/manager/egresspool.go` —— 池管理（增删、状态、按 host 合并冷却）
- `core/manager/egressprobe.go` —— 穿隧道探测（可复用 `socks5Connect`，补 HTTP CONNECT 实现）
- `core/manager/egress_http.go` —— `/api/admin/egress/*` 端点
- `src/pages/EgressPage.tsx` —— 新页面
- `src/lib/api.ts` 增 `egress*` 方法

**数据模型**（对齐参考项目 `egress.js` 的 `getProxyStatus` 返回，但去掉无效字段）：
```
route: { id, url, type(http|https|socks5), source(manual|auto), username, password }
health: { failures, openUntil, retryAfterUntil, lastError, lastSuccess }
check: { healthy, latency, passes, checks(5), lastCheck }
```

**探测语义**（关键差异，必须写进注释）：
- 隧道探测：`CONNECT opencode.ai:443` → TLS → `GET /zen/v1/models` → **只看能否拿到 HTTP 响应**，200 与 429 都算「隧道通」。
- 额度状态**不来自巡检**，来自请求路径的 429 回填（`FreeUsageLimitError` + `Retry-After`）。
- 地域状态来自 `403 RegionError`（新增分类，见 P2）。
- 页面上的「可用/额度/地域/故障」四态就是这四个来源，不能混。

**并发参数**：参考项目 `SCREEN_CONCURRENCY=200`、`VERIFY_CONCURRENCY=60`。Go 侧先取 `screen=50`、`verify=20` 保守值，可配。

**验收**：
- 手动加 3 条代理，状态列正确显示 隧道通/隧道断。
- 加一条已知被封的代理，显示「隧道断」而不是「额度」。
- 池为空时页面不报错，显示空态。

---

### P2 403 地域错误与 429 额度错误分流（改现有 chat 路径，但只加分类不改路由）

**现状**：`vendors/opencode/chat.go:453` 只判 `isFingerprintReject`（`FreeUsageLimitError`），其他 429/403 一律 `mark()`。`403 RegionError` 现在会被当成普通 403 打进节点冷却，把「地域不可用」误判成「节点坏了」。

**改法**：
- 新增 `isRegionReject(body)`（匹配 `RegionError` / `not available in your country`）。
- `403 RegionError` → 标记该「出口 × 模型」组合不可用（短冷却，按模型维度），不打节点坏池。
- 分类结果写入 call_log 的 `error_class`（新值 `region`），供出口池页统计。
- **不改路由决策**：仍然会重试，只是不再污染节点健康度。

**验收**：
- `beam-free` 直连时，日志显示 `error_class=region`，节点不进入坏池。
- 同一节点换模型正常请求不受影响。

---

### P3 配额闸门接入推理路径

**现状**：`freeGate` 只在 `admin_ops.go:315/368` 被调用。`vendors/opencode` 包不 import `core/manager`（已验证），所以不能直接调。

**改法**：把闸门提到 `core/contract` 层，用接口注入（与 `Transport` 同款模式）。
- `core/contract/gate.go` 新增 `QuotaGate` 接口：`Blocked(now) (bool, time.Duration)` / `Observe(status int, body []byte, retryAfter time.Duration, model string)`。
- `core/manager/freetier_gate.go` 实现它，通过 `opencode.Config` 注入（与 `Transport` 同一个字段注入点）。
- `chat.go` 在 `call()` 入口检查，命中则**立即返回 429 + 真实 `Retry-After`**，不打上游。
- `chat.go` 在收到 429 且 `isFingerprintReject` 时调用 `Observe`。

**收益（可量化）**：额度耗尽时一次请求从 3 次上游调用 + 3s 退避，降到 0 次上游调用 + 立即返回。

**验收**：
- 手动把闸门置为封锁，发请求 → 上游 0 次调用，响应带 `Retry-After`。
- 闸门到期后自动放行，无需重启。
- 单元测试覆盖：`FreeUsageLimitError` 记账、本地合成 429 不记账、短 `Retry-After` 不缩短已有封锁。

---

### P4 付费 key 通道（唯一真正消除 429 的改动）

**现状**：`upstream.go:210` / `:238` 无条件 `auth = UpstreamAuth{Mode: AuthRoutePublic}`；`zenChatURL`/`zenGoChatURL` 存在但无配置入口。

**改法**：
1. `Config` 新增 `upstream_zen_key`（string，omitempty）。
2. `upstream.go` 那两处改为：**只有**客户端 key 等于 `upstream_zen_key` 时才放行为 `AuthRouteZen`，其余一律 `AuthRoutePublic`（C1 正向白名单）。
3. `SettingsPage` 新增「上游 Zen 密钥」输入框（密码型，只回显掩码 `sk-xxxx••••xxxx`，与参考项目 `adminRoutes.js:318` 同款）。
4. `/api/admin/config` 返回时掩码，`has_zen_key` 布尔。

**注意**：`zen:` 前缀路径（`upstream_types.go:74`）现在被 `upstream.go` 覆盖，P4 之后仍然覆盖——**只认配置项**，不接受客户端自带 key。这是安全要求，不是偷懒。

**验收**：
- 不配 key：行为与今天完全一致（429 全局桶）。
- 配 key：请求走 `/zen/v1/chat/completions`，`Authorization: Bearer <key>`，不再出现 `FreeUsageLimitError`。
- 用 `unifiedGatewayKey`（`sk-unified-local`）当 key 请求，必须**不透传**（走 public）。

---

### P5 实例池巡检改打真出口（即原 T1~T8，收窄范围）

原计划 `2026-11-03-real-exit-probe.md` 的 T1~T8 保留，但**收窄**：既然 `/v1/models` 在额度耗尽时返回 200，巡检就不能靠它判「可用」。

**新语义**：
- 浅探（默认，每 45s）：`GET /zen/v1/models` **经实例自己的 SOCKS 出口**，判隧道 + 地域。
- 深探（节流 ≥5min，或用户手动触发）：走一次真实 chat（`max_tokens=1`），判额度。深探结果进闸门。
- 质量分只用浅探延迟；额度状态单独展示，不混进质量分（否则额度耗尽会把所有实例打成 0 分，掩盖真实的链路差异）。

**实现**：启用已存在的 `httpGetViaSocks`（`poolquality_socks.go:129`），目标 URL 从 `127.0.0.1:<port>/v1/models` 改为 `https://opencode.ai/zen/v1/models`，socks 端口用 `inst.SingboxPort`。

**验收**：
- 实例在跑时，质量分随出口延迟变化（不再恒为 0.0008s）。
- 停掉 sing-box，质量分立刻掉到 down。
- 额度耗尽时质量分不变，但「额度」列显示剩余时间。

---

### P6 一键启动/停止（你明确要的简化）

`PoolPage.tsx` 已有「一键重启池」（`:285`）。你想要的「实例池页面一个一键启动就够了」——现状是批量按钮需先勾选（`:719` 勾选驱动）。

**改法**：在 `InstancesPage` 顶部加两个常驻按钮「全部启动」「全部停止」，不依赖勾选，作用于全部实例。后端复用现有 `batchStart`/`batchStop`，只是传全量名单。

**风险**：40+ 实例全启会瞬间拉起 80+ 进程。需加确认框 + 分批（复用 `batch_concurrency`）。

---

## 3. 实施顺序与工作量

| 阶段 | 内容 | 依赖 | 估时 |
|---|---|---|---|
| P0 | Clash `/proxies` 端点修复 | 无 | 0.5 天 |
| P2 | 403 RegionError 分流 | 无 | 0.5 天 |
| P5 | 实例池巡检打真出口 | 无 | 1.5 天 |
| P3 | 配额闸门接入推理 | 无 | 1 天 |
| P4 | 付费 key 通道 | P3（共用注入点） | 1 天 |
| P1 | 出口池菜单（前后端） | P2（复用分类） | 3 天 |
| P6 | 一键启停 | 无 | 0.5 天 |

**建议顺序**：P0 → P2 → P5 → P3 → P4 → P6 → P1。
理由：P0/P2/P5 都是修正确性问题，改动小、可独立验收；P1 最大且最不影响现有行为，放最后。

## 4. 验收总表

| 编号 | 验收项 | 判定方式 |
|---|---|---|
| A1 | 关闭全部新开关时，现有行为零变化 | `git diff` 只含新增文件；旧测试全绿 |
| A2 | Clash 外部节点能读到 | `GET /api/admin/nodes` 含 9097 的 9 个节点 |
| A3 | 实例池质量分反映真实链路 | 停 sing-box 后分数掉到 down |
| A4 | 额度耗尽不烧上游 | 一次请求 0 次上游调用 |
| A5 | 付费 key 消除 429 | 配 key 后无 `FreeUsageLimitError` |
| A6 | 实例密码不泄露上游 | `sk-unified-local` 请求走 public |
| A7 | 403 地域不污染节点健康 | `error_class=region`，节点不进坏池 |
| A8 | 一键启停不依赖勾选 | 未勾选任何行也能全部启停 |

## 5. 明确不做（避免误解）

- **不做「一 IP 一实例」的额度分配**。已验证额度与 IP 无关，这个设计不产生收益。出口池只服务「绕地域限制」。
- **不替换现有 `vendors/opencode` 的请求构造与门禁逻辑**。那是经过 2026-10-14 上游收紧后调试出来的（`gate.go` 的 v2 契约、`applyFreeTierIdentity` 的四会话头），重写风险远大于收益。
- **不引入 Node 侧车**。已验证 Go 原生 TLS 与 Bun bridge 拿到的结果完全一致（同 body 同状态码），侧车是纯负担。
- **不承诺「多节点 = 更多额度」**。任何节点组合都共享同一个日桶；能提升的只有「绕地域」和「故障转移」。

## 6. 已知风险

1. **P0 改的是既有行为**。若你的 Clash 版本 `/proxies` 结构有差异，需实测对齐。建议先只读验证，不落盘。
2. **P1 的免费代理池质量极低**。参考项目 44 个自动发现节点里只有 2 个 healthy、33 个 quota、9 个 failed。搬过来不等于能用；出口池的真实价值在于**手工填你自己买的节点**。
3. **P4 依赖你持有 OpenCode Zen/Go 订阅**。没有真 key，这条通道无法验证，只能验证「不配 key 时行为不变」。
4. **P3 与 P5 深探共用闸门**，若深探误判（例如把 `400 unsupported_parameter` 当成额度），会误锁免费层。测试必须覆盖非额度类错误。
5. **基线不干净**：enhance `go test ./...` 现有 7 个失败 + 2 个编译失败，全部来自未纳入版本管理的 `*_test.go`（`.gitignore:55`）。验证必须按包跑，不能用全量。
