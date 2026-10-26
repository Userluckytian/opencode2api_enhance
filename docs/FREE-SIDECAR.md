# OpenCode 免费层 Bun sidecar

> 状态：已实现（2026-10-26）。适用版本：v1.7.10-beta.2 之后。

## 为什么需要它

OpenCode 免费层（`Authorization: Bearer public`）会在传输层校验客户端 TLS 指纹：

| 客户端 | 结果 |
|--------|------|
| 官方 `opencode` CLI（Bun/BoringSSL） | 200 |
| Bun `fetch`（同一 body/headers） | 200 |
| Go `crypto/tls` / curl / Node fetch | **429 `FreeUsageLimitError`** |

因此 Go 网关不能直接发免费层 chat 请求；sidecar 只负责“用 Bun 发请求”，
节点选择、重试、健康、统计仍由 Go 网关负责。

## 架构

```
opencode CLI / 客户端
        │
        ▼
Go 网关 / 实例（rootTransport）
        │  TierFree（仅 opencode 厂商）→ sidecar client
        ▼
Bun sidecar（127.0.0.1:40200）
        │  X-Oc2a-Proxy: http://127.0.0.1:<SOCKS+2000>
        ▼
sing-box（同一进程：SOCKS + HTTP inbound）
        │
        ▼
opencode.ai
```

- **1 个 sidecar 进程服务所有节点/实例**，不随节点数增长。
- sing-box 配置同时提供 SOCKS（`listenPort`）与 HTTP（`listenPort+2000`）inbound；
  sidecar 用 HTTP inbound（Bun fetch 不支持 SOCKS）。
- Go → sidecar 用 `POST /proxy`，带 `X-Oc2a-Target-URL` / `X-Oc2a-Proxy`；
  sidecar 把上游状态码/头/流式 body 原样透传。
- sidecar 子进程环境只允许回环绕过系统代理（`NO_PROXY=127.0.0.1,localhost`），
  并清空 `HTTP(S)_PROXY`。**不要设 `NO_PROXY=*`**：Bun fetch 会因此忽略
  `X-Oc2a-Proxy`，导致 sidecar 直连上游、节点池失效（表现为全部 429）。
- 只有 `chat/completions` / `responses` 的 POST 走 sidecar；模型目录/版本探测等
  GET **优先走节点 HTTP inbound**（保持实例池出口），无节点时才直连。
- **只有 opencode 厂商**使用 sidecar；custom `via_proxy` / remote / plugin 源
  继续走原传输，Authorization 与 body 不会被改写。

## 构建

### Windows

```powershell
powershell -ExecutionPolicy Bypass -File scripts/build-free-sidecar.ps1
# 产物：bin/free-sidecar.exe（约 110MB，不入库）
```

脚本优先使用 PATH 上的 `bun`；否则下载 Bun 1.4.4 到 `.scratch/bun` 并编译。

### Linux / macOS

```bash
# 需要 bun（https://bun.sh）
./scripts/build-free-sidecar.sh
# 产物：bin/free-sidecar
```

### 安装包/便携包

- `scripts/prepare-portable.ps1` 在 Rust 壳构建前自动调用 sidecar 构建。
- GitHub Actions `build-release.yml` 在 `npx tauri build` 前构建 sidecar。
- `src-tauri/build.rs` 检测 `bin/free-sidecar(.exe)`：存在则 `embed_free_sidecar`，
  Rust 壳启动时释放到 `bin/`；不存在则只告警，不阻断编译。
- 安装包体积会增加约 110MB。

### 构建失败策略

| 场景 | 策略 |
|------|------|
| Release / portable / CI | **硬失败**：没有 sidecar 的发布包不应放出 |
| `build-win.bat`（本地开发） | 软告警：可继续构建，运行时会回退旧免费层路径 |
| 手动跳过 | 把 `bin/free-sidecar.exe` 放好，或设置 `free_sidecar_enabled=false` |

## 配置

`config.json`（应用级）：

```json
{
  "free_sidecar_port": 40200,
  "free_sidecar_enabled": true
}
```

实例/网关生成的 `opencode2api.json` 会自动带：

```json
{
  "free_sidecar_url": "http://127.0.0.1:40200"
}
```

- `free_sidecar_url` 为空 → 回退旧直连/代理池行为。
- `free_sidecar_enabled=false` → 不启动 sidecar。
- sidecar 缺失/启动失败 → 只告警，应用继续运行（免费层回退旧路径）。

## 设置页：环境检查 + 重启

设置页新增 **「OpenCode 免费层 Sidecar」** 卡片：

- **刷新**：`GET /api/admin/free-sidecar/status`
- **检查环境**：`GET /api/admin/free-sidecar/check`
  - `enabled` / `binary` / `health` / `port` / `url` 五项逐项 OK/FAIL
- **重启 Sidecar**：`POST /api/admin/free-sidecar/restart`
  - 停止旧进程 → 重新启动 → 健康检查 → 回写根配置

状态包含：`enabled / port / url / running / healthy / pid / bin / script / error`。
修改端口或开关后，点击「重启 Sidecar」生效；运行中的实例/网关需在实例池页重启后
才会拿到新地址。

## 排障

| 现象 | 检查 |
|------|------|
| 免费层仍 429 | 设置页「检查环境」；或 `curl http://127.0.0.1:40200/health` |
| 全部 429 且节点池像没生效 | sidecar 进程环境不能有 `NO_PROXY=*`：Bun fetch 会因此忽略 `X-Oc2a-Proxy`，实际直连上游。应为 `NO_PROXY=127.0.0.1,localhost`（代码已内置，见 `freeSidecarEnv`） |
| 503 `sidecar_unavailable` | sidecar 被误杀/未启动：设置页点「重启 Sidecar」 |
| 503 `no_healthy_node` | 实例 `/api/admin/pool/health`；节点是否被 429 打进 `bad_quota` |
| sidecar 启动失败 | `bin/free-sidecar.exe` 是否存在；端口是否被占用；`OPCODE2API_BUN`/`OPCODE2API_FREE_SIDECAR_SCRIPT` |
| 节点请求失败 | sing-box 是否同时监听 SOCKS 与 `SOCKS+2000`；`X-Oc2a-Proxy` 是否为 HTTP |
| 远程 SOCKS 上游 | Bun fetch 不支持 SOCKS；远程 SOCKS 会告警（按地址去重）并直连 |

> 快速自检代理是否生效：给 sidecar 一个不存在的代理端口，
> `POST /proxy` 应返回 **502**；若仍返回上游 429，说明代理被 `NO_PROXY` 绕过。

日志：

- 管理器：`runtime/_free-sidecar/logs/free-sidecar.out.log` / `.err.log`
- sidecar：`[free-sidecar] v1.0.0 listening on http://127.0.0.1:40200`

## 测试

```bash
# sidecar 纯函数/聚合/session
bun test sidecar/free-sidecar.test.mjs

# Go：客户端改写、哨兵错误、代理推导、探针头、配置注入、HTTP inbound
go test ./core/freesidecar ./core/contract ./vendors/opencode -count=1
go test ./core/manager -run 'TestFreeSidecar|TestSingboxConfigHasHTTPInbound|TestProbeRequestHeader' -count=1
go build ./...
npm run build
```

## 资源与安全

- 1 个 sidecar 约 50–100MB 内存；固定 1 个进程，可选固定 2–4 个做隔离，
  **不按节点数扩**。
- 只监听 `127.0.0.1`；不记录请求 body/密钥。
- 当前未加本机 token：仅做本机桥接；如需更强隔离，可后续加一次性 token。
- sidecar 只处理 opencode 免费层；付费层/自定义源完全不动。

## 实测（2026-10-26）

| 场景 | 结果 |
|------|------|
| 直连/系统代理 35 次短请求 | 35/35 200 |
| 节点 42100 经 HTTP→SOCKS 桥 11 次 | 11/11 200 |
| 长回复 3 次 | 3/3 200 + usage |
| Go core 端到端（SOCKS 42110 + HTTP 44110） | 5/5 非流式 200 + 流式 200 + `/v1/models` 200 |
| 管理器自动启动 sidecar（40201/40203） | 启动成功 + chat 200 |
| 配置不写 `free_sidecar_url`，启动后回写 | 根进程 chat 200（"P11"） |
| 设置页 status/check/restart | 5/5 检查 OK；重启后 chat 200（"RESTART"） |
| 池健康 | `available=1, bad_quota=0` |
| `NO_PROXY=*` 回归（2026-10-26 11:40） | 修复前 sidecar 直连、全部 429；改为回环 bypass 后，无效代理返回 502，实例非流式 200、流式 200、单节点扫描 `ok=true/models=10` |
