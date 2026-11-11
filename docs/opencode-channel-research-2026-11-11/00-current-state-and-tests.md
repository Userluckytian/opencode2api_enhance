# 现状取证与实机实验（2026-11-11）

> 本文件只放**证据**：命令输出、日志行、HTTP 响应。结论见计划文末 §结论。
> 生产实例：管理器 40000（pid 56240，起于 2026-11-09 11:34）、引擎 40080（pid 40176，起于 2026-11-06 16:40）。
> 全部实验实例跑在 40180–40184，独立工作目录 `%TEMP%\oc-research\{e1,e2,e3}`，不碰生产配置。

---

## 1. 症状量化：4252 条请求里 921 条 503

引擎日志 `%APPDATA%\opencode2api-manager\runtime\free-tier\stdout.log`：

| status | 次数 |
|--------|------|
| 200 | 3139 |
| **503** | **921（21.7%）** |
| 429 | 158 |
| 403 | 12 |
| 499 | 11 |
| 400 | 7 |
| 502 | 4 |

按天切片（503 占比单调恶化到 100%）：

| 日期 | 总请求 | 503 | 占比 |
|------|--------|-----|------|
| 11-05 | 103 | 10 | 10% |
| 11-06 | 2504 | 19 | 1% |
| 11-07 | 276 | 56 | 20% |
| 11-09 | 870 | 456 | 52% |
| 11-10 | 415 | 296 | 71% |
| 11-11 | 84 | 84 | **100%** |

503 归因（`request_failed` 的 `error` 字段去重）：**921 条全部**是
`WARP egress is unavailable: WARP pool has no ready egress`。

503 覆盖到全部 10 个 key（uatIq 79 / ICoux 66 / F0jAB 68 / DNAIc 57 / XY-RG 115 / OqCI7 73 / J_jXN 80 / viYdO 258 / i5h1l 66 / Pwrg3 60）——
即**不是 key 用尽，而是每个 key 的尝试都被 WARP 空池挡住**。

对照：164 条 429 集中在 11-06（当时是三 key 轮询各自 429，属旧的 TPM/会话限流形态），与当前故障不是同一类。

---

## 2. 配置漂移：磁盘上 WARP 已关，进程里 WARP 还活着

| 证据 | 值 |
|------|-----|
| 管理器配置 `engine_warp_enabled` | `false`（`%APPDATA%\opencode2api-manager\config.json`） |
| 管理器配置 `engine_channel_order` | `key_first` |
| 引擎配置文件 mtime | 2026-11-11 00:25:56 |
| 引擎配置内容 | `"anonymous": false`、`"warp": {"enabled": false, ...}` |
| 引擎进程启动时间 | **2026-11-06 16:40:58**（早于配置写入 4 天） |
| 引擎 `/healthz` | `warp.status=ready, active=1, standby=1, cooling=40` |
| 引擎日志 | 持续 `Discarding duplicate WARP exit ...: 2a09:bacX:…::/64`、`registration paused`、`Rotating ... (HTTP 403), cooling 24h` |

即：**管理器 11-11 00:25 写入了「关 WARP」的新配置，引擎进程从未加载它。**

管理器的状态接口自证它管不了这个进程：

```
GET http://127.0.0.1:40000/api/admin/free-tier/status
{"state":"ready",...,"pid":0,...,
 "warp_fallback":{"enabled":false,"auto_fallback":true,"applied":false,"strikes":0,"threshold":3}}
```

`pid: 0` —— 管理器重启过（11-09），内存里的 pid 丢了；于是：

- `core/manager/freetier_engine.go:421-441` `StopFreeTierEngine`：`pid := e.pid` → 0 → **不 kill 任何进程**，随后 `waitPortClosed` 5s 超时（错误被 `_ =` 吞掉），状态置 `stopped`；
- `freetier_engine.go:362-366` `StartFreeTierEngine`：`probePort` 发现端口在听 → 直接 `setState("ready") return nil` —— **不重启、不加载新配置**；
- 于是一次「保存配置 + 重启引擎」在面板上显示成功，实际是**空操作**。

自动降级同样失效（`core/manager/freetier_warp_fallback.go:165-173`）：

```go
cfg := m.loadConfig()
if !engineWarpEnabled(cfg) || !engineWarpAutoFallback(cfg) {
    return false   // 配置里 WARP 已经是 false → 永不进入降级分支
}
```

配置说「WARP 已关」，实际进程还在 WARP，看门狗却因为「配置说已关」而拒绝自愈 → **死锁状态**。

---

## 3. 复现（生产 40080）

```
POST http://127.0.0.1:40080/v1/responses
{"model":"beam-free","input":"say hi in three words","stream":false}

→ 26 393 ms，HTTP 503
{"error":{"message":"no WARP exit could carry this free-model request; the upstream was not contacted.
 Check the WARP switch and this host's UDP access to the Cloudflare endpoint.","type":"warp_unavailable"}}
```

关键定语：**the upstream was not contacted** —— 25 秒全花在等一个不存在的 WARP 出口上，10 个 key 一次都没打到上游。

---

## 4. 对照实验 E1：关 WARP + key 直连（独立实例 40180）

配置：同一批 10 个 zen key、`anonymous=false`、`warp.enabled=false`、`proxies=["http://127.0.0.1:7890"]`、`free_only=true`。

`/healthz`：`ok`、`keys.zen=10`、`proxies.healthy=1`、**无 warp 字段**（证明 warp 未启用）。

| # | 结果 |
|---|------|
| 1 | 200 / 1050 ms |
| 2 | 200 / 848 ms |
| 3 | 200 / 1633 ms |
| 4 | 200 / 761 ms |
| 5 | 200 / 696 ms |

流式（SSE）抽查：HTTP 200，906 ms 首包，38 行事件，1.08 s 收完。

→ **5/5 成功、亚秒级**。同一批 key、同一台机器，唯一差别是没走 WARP。

---

## 5. 对照实验 E2：无代理直连（独立实例 40181）

`proxies=["direct"]`：

```
403  {"message":"This model is not available in your country","type":"upstream_error"}
```

→ 本机直连被上游按地区拒绝，**出口代理是必要项**（不是可选项）。E1 的 5/5 正是「key + 7890 代理」组合。

---

## 6. 引擎具备热加载能力，但管理器把它关掉了

- `internal/admin/server.go:62`：`POST /api/config/reload` 存在；
- `internal/gateway/runtime.go:580` `Reload()` → `applyLocked`，实测：

```
POST /api/auth/login {"username":"admin","password":"..."} → csrf_token
改 config.json：models.refresh_seconds 300→600、retry.max_attempts 3→2
POST /api/config/reload  → {"applied":true,"restart_required":false}
GET  /api/config         → refresh_seconds=600 max_attempts=2
```

- `runtime.go:527-545`：只有 `listen` / `webui.listen` / `webui.enabled` 变更需要重启，**warp.enabled 属于可热应用字段**；
- 但 `core/manager/freetier_engine.go:334` 把引擎 WebUI 写成 `Enabled:false`、`Listen:"127.0.0.1:0"` → 管理端口根本没起来，热加载通道被我们自己关死。

---

## 7. 实验环境清理

实例 pid 与目录：E1 pid 12224（40180）、E2 pid 56896（40181）、E3 pid 43572（40183 + webui 40184）。
实验结束即杀，目录 `%TEMP%\oc-research\` 可整体删除。

---

## 8. 下午复检（14:30）：配置漂移的直接对拍 + 故障已持续 21 小时

同一台机器、同一时刻，把「磁盘配置 / 运行进程 / 管理器认知」三边并排打印：

| 来源 | anonymous | warp | 时间戳 |
|------|-----------|------|--------|
| **磁盘** `runtime/free-tier/config.json` | `false` | `enabled=false` | 写入 **11-11 00:25:56** |
| **运行进程** 40080 `/healthz` | **`keys.anonymous=true`** | **warp 字段存在，status=ready, active=1, cooling=40, standby=1** | 进程启动 **11-06 16:40:58** |
| **管理器认知** `/api/admin/free-tier/status` → `warp_fallback` | — | `enabled=false, auto_fallback=true, applied=false, strikes=0` | 实时 |

三边互不相容，且**没有任何一方在报错**（管理器仍报 `state=ready healthy=true`）。这就是 §2 的死锁全貌：
配置说「WARP 关」，进程里「WARP 开着且匿名桶在跑」，看门狗因为信配置而 `strikes=0` 从不起计。

### 故障持续性（不是偶发）

```
今日 10:00–14:59：21 条请求 = 20×503 + 1×403   （0 条成功）
最后一条 200：11-10 17:05:15   ← 免费层已连续不可用约 21 小时
最后一条 503：11-11 12:55:21
```

`keys.zen=10` 全部健康、E1 证明同一批 key 此刻仍能 5/5 成功 ⇒
**损失完全由「孤儿进程 + 配置漂移」造成，与上游额度无关。**

> 注：`/healthz` 的 `keys.anonymous=true` 是引擎**匿名 lane 开关**的运行时值，与磁盘的 `false` 不一致 —— 这是**第二个**独立漂移字段（除 warp 外，`anonymous` 同样从未生效）。
> 至于「上游对免费模型只接受 `Bearer public`、真 key 会 403」的说法，反驳依据是 §4/E1 的直接观测（`channel=key tier=zen` 5/5 成功），不是本行的 healthz 字段。


