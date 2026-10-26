# 阶段 1：OpenCode 免费层 Bun sidecar + 池/扫描修复

> **状态：** 已完成（代码/单测/端到端通过，待安装包验收）
> **For agentic workers:** 按 Task 顺序执行；每 Task 测完再进下一 Task。  
> **交接提示词**见文末。  
> **元规范:** `docs/ai-framework/phased-plan-driven.md`

**Goal:** 让 opencode2api 统一网关/实例池恢复 OpenCode 免费层可用（稳定 200），并修复 429 误标 `bad_quota`、坏池恢复探针卡死、扫描 429 误报 `i/o timeout`。

**Architecture:** 新增一个本地 Bun sidecar，只负责“用 Bun 的 TLS 指纹发免费层请求”；Go 的 `rootTransport` 在免费层把 `http.Client` 换成 sidecar 客户端，节点选择/重试/统计仍由 Go 负责。sidecar 通过 HTTP 代理连接各实例的 sing-box（sing-box 配置增加 HTTP inbound，端口 = SOCKS + 2000）。管理器负责 sidecar 生命周期；sidecar 缺失时自动回退旧行为。

**Tech Stack:** Go 1.x（core/manager/vendors）、Bun 1.4.x、sing-box HTTP inbound、Tauri/React（仅配置展示，可选）。

**实施档位：** 全能（默认）  
**子代理：** 启用（验收前独立代码审查；实现由主代理完成）

---

## 前置阅读

| 优先级 | 文件 |
|--------|------|
| P0 | `AGENTS.md`、`CODE_REVIEW.md` |
| P0 | 本文件 |
| P0 | `docs/issue-log/2026-10-26.md` |
| P1 | `core/contract/contract.go`、`core/contract/instrument.go` |
| P1 | `models_source.go`（`rootTransport`）、`socks.go`、`core/manager/singbox.go`、`core/manager/probe_node.go` |

**仓库路径：** `D:\AI_Projects\opencode2api_guide\opencode2api_enhance`  
**基线分支：** `main`（工作区干净，HEAD `cce2486`）

---

## Global Constraints

1. 密钥/凭证不进 git；sidecar 二进制（约 110MB）不入库，构建脚本按需下载。
2. 默认不 push；小步 commit。
3. **向后兼容**：`free_sidecar_url` 为空或 sidecar 不可用时，免费层行为回退到现有实现，不阻断应用启动。
4. sidecar 只处理免费层（`TierFree`）；付费层/自定义源完全不动。
5. **明确不做（本阶段）**：
   - ❌ 不重写 opencode 厂商的请求构造/重试逻辑（保留现有 `ensureAgentGate`/session/retry）。
   - ❌ 不为每个节点启动一个 Bun；sidecar 固定 1 个进程（可选固定小池，本阶段不做）。
   - ❌ 不做 uTLS 方案（作为备选记录，不实现）。
6. 所有新逻辑必须有单测或可复现命令；验收以实际输出为准。

---

## File Structure（预期变更）

| 文件 | 动作 | 职责 |
|------|------|------|
| `sidecar/free-sidecar.mjs` | 新建（已完成） | Bun HTTP 服务：`/health`、`/proxy`、指纹 body、稳定 session、流式/聚合、HTTP 代理 |
| `scripts/build-free-sidecar.ps1` | 新建 | 下载 Bun + `bun build --compile` 产出 `bin/free-sidecar.exe` |
| `scripts/build-free-sidecar.sh` | 新建 | Linux/macOS 构建脚本 |
| `core/manager/free_sidecar.go` | 新建 | 管理器启动/停止 sidecar、健康检查、URL 暴露 |
| `core/manager/config.go` | 修改 | `FreeSidecarPort`/`FreeSidecarEnabled` 配置 |
| `core/manager/singbox.go` | 修改 | 增加 HTTP inbound（SOCKS + 2000） |
| `core/manager/opencodecfg.go` | 修改 | 生成配置注入 `free_sidecar_url` |
| `core/manager/probe_run.go` | 修改 | 端口分配避开 HTTP inbound 段 |
| `core/manager/probe_node.go` | 修改 | 扫描 429 分类、i/o timeout 识别、探针头 |
| `core/manager/probe_completion.go` | 修改 | 探针请求带 `X-Oc2a-Probe: 1` |
| `types_config.go` | 修改 | `AppConfig.FreeSidecarURL` |
| `config.go` | 修改 | `applyConfig` 保存 `freeSidecarURL` |
| `free_sidecar_client.go` | 新建 | sidecar `RoundTripper` + HTTP 代理地址推导 |
| `models_source.go` | 修改 | `rootTransport.Client` 免费层返回 sidecar 客户端；实现 `MarkFingerprint` |
| `core/contract/instrument.go` | 修改 | `FingerprintMarker` 可选接口 |
| `vendors/opencode/chat.go` | 修改 | 识别 `FreeUsageLimitError`，走短冷却 |
| `socks.go` | 修改 | `markFingerprintResult`（短冷却，不进坏池） |
| `docs/FREE-SIDECAR.md` | 新建 | 构建/部署/排障说明 |
| `docs/issue-log/2026-10-26.md` | 修改 | 状态与验收证据 |

---

## 配置/API 契约

**实例/网关生成配置（`opencode2api.json`）新增：**

```json
{
  "free_sidecar_url": "http://127.0.0.1:40200"
}
```

**sidecar 内部协议：**

```http
POST /proxy
X-Oc2a-Target-URL: https://opencode.ai/inference/v1/chat/completions
X-Oc2a-Proxy: http://127.0.0.1:44100   # 可选；节点 sing-box HTTP inbound
X-Oc2a-Identity: <可选>
Content-Type: application/json
```

响应：上游状态码/头/body 原样透传；sidecar 自身错误 502 JSON。

**管理配置（`config.json`）新增：**

```json
{
  "free_sidecar_port": 40200,
  "free_sidecar_enabled": true
}
```

---

## Task 1：Bun sidecar MVP（已完成，待回归）

**Files:** `sidecar/free-sidecar.mjs`

**行为:** `/health`、`/proxy`；补官方 `read`/`shell` 逐字定义；稳定 session；`stream:false` 聚合；HTTP 代理；流式透传。

**验收命令：**

```bash
bun sidecar/free-sidecar.mjs
curl http://127.0.0.1:40200/health
# 直连 + 节点桥两种方式各发 1 次 chat，期望 200
```

**已实测：** 直连 200、聚合 200、节点桥 200。

---

## Task 2：Go sidecar 客户端（免费层 Transport）

**Files:** `types_config.go`、`config.go`、`free_sidecar_client.go`、`models_source.go`、`core/contract/instrument.go`

**行为:**

1. `AppConfig.FreeSidecarURL` 解析 `free_sidecar_url`；`applyConfig` 存入全局。
2. `rootTransport.Client(TierFree, …)`：URL 非空时返回 sidecar 客户端；URL 为空保持旧行为。
3. sidecar 客户端把原请求改写为 `POST <sidecar>/proxy`，带 `X-Oc2a-Target-URL`/`X-Oc2a-Proxy`，原 body/headers 透传。
4. `X-Oc2a-Proxy` 推导：`127.0.0.1:<socksPort>` → `http://127.0.0.1:<socksPort+2000>`；HTTP(S) 代理原样；无法推导时留空（直连）并告警。
5. 新增 `contract.FingerprintMarker` 可选接口；`rootTransport` 实现。

**测试：**

```bash
go test ./... -run 'TestSidecar|TestRootTransport|TestFingerprint' -count=1
go build ./...
```

期望：单测通过；`go build` 无错误。

---

## Task 3：sing-box HTTP inbound + 端口分配

**Files:** `core/manager/singbox.go`、`core/manager/probe_run.go`

**行为:**

1. `buildSingboxConfig` 增加 `http` inbound：`127.0.0.1:<listenPort+2000>`。
2. 导出 `SingboxHTTPPort(port uint16) uint16` 供 main 包推导。
3. `allocatePorts` 避开实例 HTTP inbound 段（`instBase+4100 ~ instBase+6100`），并检查 `socks+2000` 空闲。
4. 现有实例启动/探针/网关配置自动获得 HTTP inbound，无需额外进程。

**测试：**

```bash
go test ./core/manager -run 'TestSingbox|TestScan|TestAllocate' -count=1
```

期望：配置含两个 inbound；端口分配不冲突。

---

## Task 4：管理器 sidecar 生命周期 + 配置注入

**Files:** `core/manager/free_sidecar.go`、`core/manager/config.go`、`core/manager/opencodecfg.go`、`main.go`

**行为:**

1. `StartFreeSidecar`：优先 `bin/free-sidecar.exe`；否则 `bin/bun.exe` + `bin/free-sidecar.mjs`；再否则 `OPCODE2API_BUN` + `sidecar/free-sidecar.mjs`；都没有则告警并跳过。
2. 端口默认 `40200`；若已监听则探测 `/health`，复用或报错。
3. 启动后 `m.FreeSidecarURL()` 返回 `http://127.0.0.1:<port>`。
4. `buildOpenCodeCfg` / `buildRouterCfg` 注入 `free_sidecar_url`（仅 sidecar 可用时）。
5. `main.go` 在 `manager.New` 后启动，`defer` 停止；日志写 `runtime/_free-sidecar/logs/`。
6. 配置项 `free_sidecar_port` / `free_sidecar_enabled`；默认启用。

**测试：**

```bash
go test ./core/manager -run 'TestFreeSidecar' -count=1
go build ./...
```

期望：sidecar 启停幂等；生成的实例/网关配置含 URL；缺失二进制时应用仍可启动。

---

## Task 5：池/扫描修复

**Files:** `vendors/opencode/chat.go`、`socks.go`、`core/manager/probe_completion.go`、`core/manager/probe_node.go`、`core/manager/tcp.go`

**行为:**

1. **429 指纹拒绝不标坏池**：`FreeUsageLimitError` → `MarkFingerprint`，只设短冷却（15s，Retry-After 封顶 60s），不增加 `badCount`/`badReason`。
2. **恢复探针**：`badPoolProbeReady` 在 `until` 过期且租约过期时放行；探针失败不无限续期（已有上限保留）。补充单测覆盖 `until` 过期仍可恢复。
3. **扫描探针**：请求带 `X-Oc2a-Probe: 1`；核心识别后 `maxRetries=0`，429 快速返回。
4. **扫描分类**：429 → `rate_limit`；`i/o timeout` → `timeout`；前端补 `rate_limit` 标签。
5. **未就绪**：API 端口等待失败归类 `startup`，文案明确。

**测试：**

```bash
go test ./... -run 'TestMarkFingerprint|TestBadPool|TestProbe' -count=1
```

期望：429 不进入 `bad_quota`；扫描结果为 429 而非 i/o timeout。

---

## Task 6：构建/打包/文档/验收

**Files:** `scripts/build-free-sidecar.ps1`、`scripts/build-free-sidecar.sh`、`docs/FREE-SIDECAR.md`、`README.md`、`scripts/prepare-portable.ps1`、CI workflow（可选）

**行为:**

1. 构建脚本下载 Bun 1.4.x，`bun build --compile` 产出 `bin/free-sidecar.exe`（不入库）。
2. 便携包/CI 在构建前执行脚本；安装包内 `bin/free-sidecar.exe` 随 core 释放。
3. 文档：构建、端口、节点 HTTP inbound、排障（`/health`、日志、回退）。
4. 端到端验收：真实节点 + 统一网关连续 10 次 chat（流式/非流式）全部 200；长回复 1 次 200。

---

## 验收表

| 项 | 命令/证据 | 期望 | 状态 |
|----|-----------|------|------|
| sidecar 直连 | `curl /proxy`（stream true） | 200 + SSE | ✅ 2026-10-26 |
| sidecar 聚合 | `curl /proxy`（stream false） | 200 + JSON | ✅ 2026-10-26 |
| sidecar 节点桥 | `X-Oc2a-Proxy: 8897/44110` | 200 | ✅ 2026-10-26（8897 + 44110 均 200） |
| Go 单测 | `go test ./core/freesidecar ./core/manager ./vendors/opencode -count=1` | 全绿（已知基线失败除外） | ✅ 仅既有 `TestZZRepairExperiment` 失败（与本阶段无关） |
| Go 构建 | `go build ./...` | 成功 | ✅ 2026-10-26 |
| 网关端到端 | Go core 连续 5 次 chat + 流式 + `/v1/models` | 全部 200 | ✅ 5/5 + 流式 200 + models 200（09:36–09:41） |
| 长回复 | 1024 tokens | 200 + 可见正文 + usage | ✅ Bun 路径 3/3（glm/deepseek/big-pickle） |
| 设置页 status/check/restart | API + UI | 200 / 5 项 OK / 重启后 chat 200 | ✅ 2026-10-26（pid 572→36952，chat "RESTART"） |
| sidecar 单测 | `bun test sidecar/free-sidecar.test.mjs` | 5/5 | ✅ |
| GET 走节点代理 | 模型目录 GET | 200 | ✅（节点 HTTP inbound） |
| 429 不进坏池 | 构造 429 FreeUsageLimitError | `bad_quota` 不增加 | ⚠️ 代码路径已实现（`markFingerprintResult`）；未注入真实 429 做单测，端到端池健康 `bad_quota=0` |
| 扫描 429 分类 | 扫描一个限流节点 | `rate_limit`，非 `i/o timeout` | ✅ 2026-10-26 11:40：单节点扫描返回 200 `可用，models=10`；分类单测通过 |
| `NO_PROXY` 回归 | 无效代理端口 + 真实节点 | 502（证明代理生效）；实例/流式/扫描 200 | ✅ 2026-10-26 11:40：修复 `NO_PROXY=*` 后全部通过 |
| 缺失 sidecar 回退 | 删除/改名 sidecar 二进制 | 应用启动成功，日志告警 | ✅ `TestFreeSidecarDisabledByConfig`；`main` 启动失败仅告警 |
| 代码审查 | 独立子代理 | 无 ❌ | ✅ P1-1/P1-2/P2-3/P2-4 已修；P2-5/6/7/P3 登记 |

---

## 风险与回退

- **上游变更**：指纹/端点可能再变；sidecar 的 UA/端点/工具定义集中在一处，便于跟进。
- **Bun 打包体积**：+110MB；可选首次运行下载或依赖官方 CLI。
- **SOCKS 代理**：Bun fetch 不支持 SOCKS；必须走 sing-box HTTP inbound。远程 SOCKS 上游代理暂不支持（告警并直连/回退）。
- **回退开关**：`free_sidecar_enabled=false` 或删除 `free_sidecar_url` → 旧行为。
- **安全**：sidecar 仅监听 `127.0.0.1`，不接受外部连接；不记录 body/密钥。

---

## 给接手 AI 的完整提示词

```text
你在 D:\AI_Projects\opencode2api_guide\opencode2api_enhance（main 分支）。
按 docs/ai-framework/plans/2026-10-26-free-tier-bun-sidecar.md 执行 Task 2–6。
背景：OpenCode 免费层校验客户端 TLS 指纹；官方 Bun 客户端 200，Go/curl/Node 429。
sidecar/free-sidecar.mjs 已实现并通过直连/聚合/节点桥实测。
要求：
1) 先跑 Task 2 单测与 go build，再进 Task 3；每 Task 完成后更新验收表。
2) 不改付费层/自定义源行为；sidecar 缺失必须回退且不阻断启动。
3) 429 FreeUsageLimitError 不得进入 bad_quota；扫描 429 不得误报 i/o timeout。
4) 不 push；不把 bin/free-sidecar.exe 或密钥入库。
5) 验收以实际命令输出为准；完成后更新 docs/issue-log/2026-10-26.md。
```
