# 阶段 v2.0.0：从「免费层聚合器」重构为「自定义供应商网关 + 托管免费层引擎」

> **状态：** 计划已就绪 / 待实施
> **For agentic workers:** 按 P0→P5 顺序执行；每阶段验收表全绿再进下一阶段。
> **交接提示词**见文末。
> **元规范：** `docs/ai-framework/phased-plan-driven.md`

**Goal:** 删除已被上游推翻的免费层自研路径（独享 / 实例池 / 节点池 / 40080 自研网关 / Bun sidecar），改为「40000 控制面 + 40080 托管 opencode2api-win 引擎 + 50080 自定义供应商」，并随包分发引擎二进制。
**Architecture:** 三进程架构，40000 只做控制面（不代理推理），40080/50080 各自是独立推理入口；管理器负责生成配置、启停、健康检查、崩溃拉起。
**Tech Stack:** Go 1.22（core）/ React 19 + Vite + Tailwind 4 / Tauri 2 / 外部 GPL 引擎 `opencode2api-win`（独立进程，HTTP 通信）。
**实施档位：** 全能（默认）
**子代理：** 启用（qwork-auto via 50080，用于实施 + 测试；验收由主代理独立执行）

---

## 前置阅读（必须）

| 优先级 | 文件 |
|--------|------|
| P0 | `docs/ai-framework/phased-plan-driven.md` |
| P0 | 本文件 |
| P0 | `docs/issue-log/OPEN.md` |
| P1 | `AGENTS.md`、`CODE_REVIEW.md` |
| P1 | `docs/issue-log/2026-11-04.md`（429 分析全过程） |

**仓库路径：** `D:\AI_Projects\opencode2api_guide\opencode2api_enhance`
**基线分支：** 从 `v1.9.0-free-tier-final`（= `97d4ecb`）拉 `refactor/custom-only`
**目标版本：** `package.json` 1.9.0 → **2.0.0**

---

## 决策记录（用户已确认）

| # | 决策 | 依据 |
|---|------|------|
| D1 | 独享 / 实例池 / 节点池 / 40080 自研网关 / Bun sidecar **全部删除** | 上游已改为按「(会话, 出口 IP)」计量 + 客户端形态校验，自研路径被推翻 |
| D2 | 40080 端口保留，改由 **opencode2api-win** 引擎提供 | 实测：6 串行 + 4 并发全 200；我们 sidecar 直连全 429 |
| D3 | 自定义供应商 + 插件供应商（50080）**完整保留** | 这是当前唯一正常工作的能力 |
| D4 | 40000 只做**控制面**，不做 `/v1` 统一入口 | 避免重写一套网关逻辑 |
| D5 | 代理能力保留，形态改为**静态代理列表 + 每供应商 proxy 字段** | 用户「有时必须走代理」，但不需要节点池管理系统 |
| D6 | 引擎 exe **随包分发**（方案 B） | 用户要求「装完即可用」 |
| D7 | 回滚点 tag：`v1.9.0-free-tier-final` | 已创建，指向 `97d4ecb` |

---

## Global Constraints（冲突时以本节为准）

1. **不引入 WARP 之外的新自研协议层。** 免费层协议（uTLS 指纹、`<env>` 注入、会话亲和、两段式 429）由外部引擎负责，我们**不实现**。
2. **密钥/凭据不进 git。** 引擎的 `config.json` 落在运行时目录（gitignore），不提交。
3. **GPL 隔离（硬约束）：**
   - 引擎以**独立进程**运行，通过命令行 + HTTP 通信，**不链接、不合并代码**（mere aggregation）；
   - 我们的代码**不得**复制 `refs/opencode2api-win` 的任何源码；
   - 发布包内必须包含 `third_party/opencode2api-win/LICENSE`（GPL-3.0 全文）与 `SOURCE.txt`（源码地址 + 对应 commit）；
   - 我们的 LICENSE（禁止商用）仅覆盖我们的部分，不得对引擎额外设限。
4. **明确不做（本阶段）：**
   - ❌ 不实现 40000 的 `/v1` 反向代理 / 统一入口
   - ❌ 不实现节点扫描、订阅导入、Clash 解析、sing-box 多实例
   - ❌ 不实现出口 IP 池 / 坏池 / 质量分 / 竞速
   - ❌ 不改动 50080 的协议转换逻辑（`convert.go` / `responses.go` / `claude.go`）
   - ❌ 不做引擎的 WebUI（`webui.enabled=false`，管理统一在 40000）
5. **Git：** 小步 commit（gitmoji + 中文），**默认不 push**，除人类明确要求。
6. **YAGNI：** 不为「将来可能」保留死代码；删就删干净。

---

## 端口规划（目标态）

| 端口 | 归属 | 说明 |
|------|------|------|
| **40000** | 我们的管理器 | 管理面板 + `/api/admin/*`（控制面，不代理推理） |
| **40080** | opencode2api-win 引擎 | `/v1/chat/completions`、`/v1/responses`、`/v1/messages`、`/v1/models`、`/healthz` |
| **50080** | 我们的供应商服务 | 自定义源 + 插件供应商，OpenAI/Anthropic/Responses 三协议 |
| ~~39999~~ | — | Bun sidecar **删除** |
| ~~40100+~~ | — | 实例 API **删除** |
| ~~42100+~~ | — | sing-box **删除** |

---

## 删除清单（P2/P3 执行）

### 根包（Go）

| 文件 | 理由 |
|------|------|
| `socks.go` / `socks_perf.go` / `socks_state.go` | SOCKS5 节点池 |
| `pool_health.go` | 节点健康 / 坏池 |
| `upstream.go` | `rootTransport`（池 + sidecar 桥接） |
| `gateway_timeout.go` | 自研网关超时体系 |
| `upstream_key_pool.go` | 单/多 key 池（引擎自带 `zen_keys`/`go_keys`） |
| `free_sidecar_client.go` | Bun sidecar 客户端 |
| `auto_model.go`（若仅服务免费层） | 视引用情况，先保留再评估 |

### core/

| 路径 | 理由 |
|------|------|
| `core/freesidecar/`（整包） | Bun sidecar |
| `core/manager/instance.go`、`singbox.go`、`gateway.go` | 实例池 + 自研网关 |
| `core/manager/poolquality*.go`、`probe*.go` | 节点探测 / 质量分 |
| `core/manager/subscribe.go`、`subscriptions.go`、`subscription_*.go` | 订阅 |
| `core/manager/clash.go`、`clash_parse.go`、`yaml.go` | Clash 解析 |
| `core/manager/chain.go`、`localprofile.go`、`batch.go` | 链路 / 本地配置 / 批量 |
| `core/manager/orphan*.go`、`health.go`、`tcp.go` | 残留进程 / 健康 / TCP |
| `core/manager/free_sidecar.go`、`opencodecfg.go`、`custom_propagate.go`、`automodel.go` | 免费层配置生成/传播 |
| `core/manager/freetier_gate.go`、`freetier_http.go`（未提交 WIP） | 429 闸门 |

### vendors/

| 路径 | 理由 |
|------|------|
| `vendors/opencode/`（整包） | 免费层厂商（`<env>` / 指纹 / 429 阶梯） |

### 前端

| 文件 | 理由 |
|------|------|
| `src/pages/InstancesPage.tsx` | 独享 |
| `src/pages/PoolPage.tsx` | 实例池 |
| `src/pages/NodesPage.tsx` | 节点池 |
| `src/components/TestBadge.tsx`（若仅实例测试用） | 随实例页删除 |

### 其它

- `sidecar/free-sidecar.mjs`、`sidecar/free-sidecar.test.mjs`
- `scripts/build-free-sidecar.ps1` / `.sh`
- `bin/free-sidecar.exe`、`bin/sing-box.exe`、`bin/opencode2api.exe`（旧的会被新构建覆盖）

### 配置项（`ConfigView` / `AppConfig` 删除）

`upstream_zen_key`、`upstream_zen_keys`、`free_sidecar_port`、`free_sidecar_enabled`、
`instance_base_port`、`probe_api_port`、`probe_socks_port`、`gateway_port`、
`subscribe_url`、`subscribe_interval_min`、`pool_*`、`race_*`、`rate_limit_*`、
`fingerprint_bad_cooldown_sec`、`scan_concurrency`、`stop_scan_concurrency`、
`chain_*`、`front_*`、`opencode_client_version`、`upstream_proxy`、
`test_chat_enabled`、`test_min_interval_ms`、`show_node_prefix`

---

## 保留清单

| 模块 | 文件 | 备注 |
|------|------|------|
| 自定义供应商 | `vendors/custom/`、`custom_providers.go`、`CustomModelsPage.tsx` | `via_proxy:bool` → `proxy:string` |
| 插件供应商 | `core/manager/pluginprovider/`、`plugin_vendors.go` | 不动 |
| 供应商服务 | `core/manager/providerservice.go` | 不动 |
| 协议转换 | `convert.go`、`responses.go`、`claude.go` | 不动 |
| 鉴权 | `auth.go`、`login.go` | 不动 |
| 调用日志 | `core/manager/calllog.go` | 删掉实例/网关源，保留 50080 |
| 统计 | `core/manager/stats.go` | 重写：只留模型 / Token / 日维度 |
| 设置页 | `SettingsPage.tsx` | 大幅瘦身 + 新增「免费层引擎」卡 |
| UI 组件 | `src/components/Modal.tsx` | **保留**（本次 WIP 成果） |
| 插件能力文档 | `docs/PLUGIN-CAPABILITIES.md` | **保留**（本次 WIP 成果） |

**WIP 抢救：** 47 个未提交改动整体丢弃，仅保留
`src/components/Modal.tsx`、`src/pages/CustomModelsPage.tsx`（UX 修复）、`docs/PLUGIN-CAPABILITIES.md`。
已备份到 `D:\AI_Projects\opencode2api_guide\_salvage_20261104\`。

---

## 引擎集成设计（P4 核心）

### 运行时目录

```
<AppData>/opencode2api-manager/
├── config.json                    # 我们的配置（custom providers / plugins / engine 设置）
├── custom_providers.json
├── call_log.jsonl
└── runtime/
    └── free-tier/                 # 引擎工作目录（可写）
        ├── config.json            # 我们生成
        ├── config.json.models.catalog.json   # 引擎自建
        ├── config.json.rotation.json         # 引擎自建
        ├── config.json.*.availability.json   # 引擎自建
        └── logs/
            ├── stdout.log
            └── stderr.log
```

> 实测：引擎会在 `config.json` 同目录创建 `config.json.models.catalog.json` 等状态文件，
> 因此**必须**给它一个独立可写目录，不能放 Program Files。

### 我们生成的引擎配置（模板）

```jsonc
{
  "listen": "127.0.0.1:40080",
  "server_keys": ["<我们的 gateway_key>"],
  "zen_keys": [],                      // 面板可填（可选）
  "go_keys": [],
  "anonymous": true,
  "warp": {
    "enabled": true,
    "pool_size": 4,                    // 面板可调 1~32
    "stack": "ipv6",
    "source_ipv6": ""                  // 用户可填 /64；空 = 不轮换
  },
  "proxies": ["direct"],               // 来自 outbound_proxies
  "proxyfile": "",
  "upstream": {
    "zen": "https://opencode.ai/zen",
    "go": "https://opencode.ai/zen/go",
    "anonymous": "https://opencode.ai/inference"
  },
  "retry": { "max_attempts": 3, "timeout_seconds": 300 },
  "models": { "refresh_seconds": 300, "protocols": {}, "free_only": false },
  "performance": {
    "max_idle_conns": 2048,
    "max_idle_conns_per_host": 256,
    "max_conns_per_host": 0,
    "idle_conn_timeout_seconds": 120,
    "connect_timeout_seconds": 5,
    "failure_cooldown_seconds": 15,
    "attempt_timeout_seconds": 0,
    "first_event_timeout_seconds": 0,
    "max_anonymous_exits": 8,
    "free_tier_block_seconds": 900
  },
  "logging": { "level": "info", "ring_size": 2000, "dump_request_bodies": false },
  "webui": { "enabled": false, "listen": "127.0.0.1:0", "username": "", "password": "", "session_ttl_minutes": 60 },
  "prefer": "go",
  "reasoning": { "effort": "", "effort_by_model": {} }
}
```

> **实测确认：** `webui.enabled=false` 时不监听 WebUI 端口；`-config <path>` 可指定配置路径；
> WARP 在独立目录下正常起 `size=2 / active=2`；`/healthz` 返回 `ready:true`。

### 管理器新增：`core/manager/freetier_engine.go`

```go
type FreeTierEngine struct {
    m *Manager
    // 状态：stopped / starting / ready / failed
}

func (e *FreeTierEngine) Status() EngineStatus        // 读 /healthz + 进程状态
func (e *FreeTierEngine) Start(runner Runner) error   // 生成配置 → spawn → 等 ready
func (e *FreeTierEngine) Stop(runner Runner) error
func (e *FreeTierEngine) Restart(runner Runner) error
func (e *FreeTierEngine) Watchdog(runner Runner)      // 30s：进程死 / healthz 失败 → 重启
func (e *FreeTierEngine) WriteConfig() error          // 面板设置 → config.json
func (e *FreeTierEngine) Reload() error               // POST /api/config/reload
```

### 面板新增：设置页「免费层引擎」卡

| 项 | 控件 |
|---|---|
| 引擎状态 | 徽章：未安装 / 已停止 / 运行中（40080）/ 异常 |
| 版本 | 读 `bin/opencode2api-win.exe` 的 `-version` 或文件哈希 |
| WARP 出口池 | 开关 + `pool_size` 数字（1~32）+ `source_ipv6` 输入 |
| 出口代理 | 多行文本（`direct` / `socks5://...` / `http://...`） |
| Zen / Go keys | 可选多行（空 = 纯匿名） |
| 模型可用性 | 表格：模型 / 可用 / 原因 / 下次探测 |
| 操作 | 启动 / 停止 / 重启 / 重新生成配置 |

### 打包（P5）

`src-tauri/src/embed.rs` 增加：

```rust
// 免费层引擎（GPL-3.0，独立进程，mere aggregation）
#[cfg(all(embed_free_tier_engine, embed_unix_bin))]
pub const FREE_TIER_ENGINE: Option<&[u8]> = Some(include_bytes!("../../bin/opencode2api-win"));
#[cfg(all(embed_free_tier_engine, not(embed_unix_bin)))]
pub const FREE_TIER_ENGINE: Option<&[u8]> = Some(include_bytes!("../../bin/opencode2api-win.exe"));
#[cfg(not(embed_free_tier_engine))]
pub const FREE_TIER_ENGINE: Option<&[u8]> = None;
```

`build.rs` 检测 `bin/opencode2api-win.exe` 是否存在 → 设 `embed_free_tier_engine`。

发布包增加：

```
third_party/opencode2api-win/
├── LICENSE          # GPL-3.0 全文
├── SOURCE.txt       # 仓库地址 + 对应 commit + 构建命令
└── README.md        # 说明这是独立进程、非本项目代码
```

---

## 代理能力（D5）

### 我们的配置新增

```jsonc
{
  "outbound_proxies": ["direct", "socks5://127.0.0.1:7897"],
  "outbound_mode": "auto"        // auto | direct | proxy
}
```

- `auto`：原样传给引擎的 `proxies`
- `direct`：写 `["direct"]`
- `proxy`：只写非 direct 条目

### 自定义供应商字段变更

```
- via_proxy: boolean          // 删除
+ proxy: string               // "" = 直连；"socks5://..." = 走该代理
```

`vendors/custom/` 的 `Transport` 注入改为：

```go
// core/contract 新增
type StaticProxyTransport struct{ ProxyURL string }
func (t StaticProxyTransport) Client(...) (*http.Client, string)  // 按 proxy URL 建 client
func (t StaticProxyTransport) Mark(...)                            // no-op
```

---

## 阶段任务

### P0 — 回滚与基线（0.5h）

| # | 任务 | 验收命令 | 期望 |
|---|------|---------|------|
| 0.1 | 确认 tag 存在 | `git tag -n1 v1.9.0-free-tier-final` | 指向 `97d4ecb` |
| 0.2 | 丢弃 47 个 WIP | `git checkout -- . ; git clean -fd`（保留 `_salvage_20261104`） | `git status --short` 空 |
| 0.3 | 建分支 | `git checkout -b refactor/custom-only v1.9.0-free-tier-final` | 当前分支正确 |
| 0.4 | 备份数据目录 | 复制 `%APPDATA%\opencode2api-manager` → `_backup_20261104` | 备份存在且非空 |
| 0.5 | 基线构建 | `go build ./...` / `npx tsc --noEmit` | 均 exit 0 |

### P1 — 解耦共享契约（2h）

> 目标：让 `vendors/custom` / `vendors/remote` 不再依赖 `vendors/opencode` 的常量。

| # | 任务 | 验收 |
|---|------|------|
| 1.1 | `ParamTransport` 等常量迁到 `core/contract` | `grep -r "opencode.Param" vendors/` 无结果 |
| 1.2 | 新增 `contract.StaticProxyTransport` | 单测：`Client()` 返回带 proxy 的 client |
| 1.3 | `vendors/custom` 改用 `contract.ParamTransport` | `go build ./...` 通过 |
| 1.4 | `vendors/remote` 同上 | `go build ./...` 通过 |

**验收表：**

| 检查 | 命令 | 期望 |
|---|---|---|
| 编译 | `go build ./...` | exit 0 |
| vet | `go vet ./vendors/... ./core/contract/` | exit 0 |
| 单测 | `go test ./vendors/custom/ ./core/contract/` | PASS |
| 依赖检查 | `go list -deps ./vendors/custom \| grep opencode` | 无输出 |

### P2 — 删除免费层后端（4h）

| # | 任务 | 验收 |
|---|------|------|
| 2.1 | 删根包 8 个文件 | 见删除清单 |
| 2.2 | 删 `core/freesidecar/`、`core/manager/` 下 30 个文件 | 见删除清单 |
| 2.3 | 删 `vendors/opencode/` | `ls vendors/` 只剩 `custom`、`remote` |
| 2.4 | 重写 `models_source.go`：只留 `newAggregator` + `appendOtherFreeModels` + 目录刷新 | 50080 模型列表正确 |
| 2.5 | 重写 `stats.go`：只留模型 / Token / 日维度 | `/api/admin/stats` 返回新结构 |
| 2.6 | 重写 `diag.go` / `postmortem.go`：删节点维度 | 编译通过 |
| 2.7 | 清理 `ConfigView` / `AppConfig` 中 24 个免费层配置项 | `ConfigGet` 不再返回这些 key |
| 2.8 | `main.go` 移除实例/网关/sidecar 装配 | 启动日志无相关字样 |

**验收表：**

| 检查 | 命令 | 期望 |
|---|---|---|
| 编译 | `go build ./...` | exit 0 |
| vet | `go vet ./...` | exit 0 |
| 残留引用 | `grep -rn "free-sidecar\|sing-box\|instances/\|nodes/\|subscribe" --include=*.go .` | 仅剩 `freetier_engine.go` 相关 |
| 启动 | `bin\opencode2api.exe -provider-service -port 50080` | `/v1/models` 返回自定义源模型 |
| 推理 | `curl 50080/v1/chat/completions -d '{"model":"qw2api/qwork-auto"...}'` | 200 + 正确回复 |

### P3 — 删除免费层前端（3h）

| # | 任务 | 验收 |
|---|------|------|
| 3.1 | 删 `InstancesPage.tsx` / `PoolPage.tsx` / `NodesPage.tsx` | 文件不存在 |
| 3.2 | `App.tsx` 导航 7 → 4 项（自定义模型 / 统计 / 日志 / 设置） | UI 只有 4 个 tab |
| 3.3 | `api.ts` 删除 instance/node/pool/scan/subscribe/gateway 类型与函数 | `tsc` 通过 |
| 3.4 | `SettingsPage.tsx` 瘦身：删节点池/订阅/竞速/扫描配置 | 页面无这些区块 |
| 3.5 | `StatsPage.tsx` 瘦身：删节点/实例维度 | 页面只显示模型维度 |
| 3.6 | 移植 `Modal.tsx` + `CustomModelsPage.tsx` UX 修复 | 弹窗固定标题、保存 <200ms、开关乐观 |
| 3.7 | 新增「免费层引擎」卡（P4 对接前先做静态 UI） | 卡片渲染正常 |

**验收表：**

| 检查 | 命令 | 期望 |
|---|---|---|
| 类型 | `npx tsc --noEmit` | exit 0 |
| 构建 | `npm run build` | exit 0，dist 生成 |
| 导航 | 浏览器打开 40000 | 4 个 tab，无 404 |
| 弹窗 | 点「添加模型源」滚到底 | 标题 `y` 不变 |
| 保存 | 保存自定义源 | <200ms 关闭 |

### P4 — 引擎集成（4h）

| # | 任务 | 验收 |
|---|------|------|
| 4.1 | 新建 `core/manager/freetier_engine.go` | 实现 Status/Start/Stop/Restart/Watchdog/WriteConfig/Reload |
| 4.2 | `main.go` 注册 `/api/admin/free-tier/{status,start,stop,restart,config}` | 端点可达 |
| 4.3 | 设置页「免费层引擎」卡对接真实 API | 显示运行中 / WARP 出口数 / 模型可用性 |
| 4.4 | 首次启动自动生成配置并拉起引擎 | 40080 `/healthz` ready |
| 4.5 | 30s 看门狗：进程死 → 自动重启 | 手动 kill 后 30s 内恢复 |
| 4.6 | 端口表更新：只显示 40000 / 40080 / 50080 | 设置页无 39999/40100/42100 |
| 4.7 | `outbound_proxies` 配置 + 面板编辑 | 改后写入引擎 config 并 reload |
| 4.8 | 自定义供应商 `via_proxy` → `proxy` 字段迁移 | 旧数据自动转换 |

**验收表：**

| 检查 | 命令 | 期望 |
|---|---|---|
| 编译 | `go build ./...` | exit 0 |
| 引擎起 | 启动管理器 | `Get-NetTCPConnection -LocalPort 40080` 有监听 |
| 健康 | `curl 40080/healthz` | `ready:true`，`warp.active > 0` |
| 推理 | `curl 40080/v1/chat/completions -H "Authorization: Bearer <key>"` | 200 |
| 看门狗 | `Stop-Process <engine pid>` | 30s 内自动恢复 |
| 50080 | `curl 50080/v1/models` | 自定义源模型列表 |
| 端口表 | 设置页截图 | 仅 3 行 |

### P5 — 打包与发布（3h）

| # | 任务 | 验收 |
|---|------|------|
| 5.1 | `bin/opencode2api-win.exe` 放入（v1.4.6+win 基线） | 文件存在，~19MB |
| 5.2 | `embed.rs` + `build.rs` 增加引擎内嵌 | `cargo build` 通过 |
| 5.3 | `prepare-portable.ps1` 复制 `third_party/opencode2api-win/` | 发布包含 LICENSE + SOURCE.txt |
| 5.4 | `package.json` 1.9.0 → 2.0.0 | 版本正确 |
| 5.5 | 文档：README 重写（三端口架构 + GPL 声明） | 文档无免费层旧描述 |
| 5.6 | `docs/issue-log/2026-11-04.md` 记录全过程 | 有证据链接 |
| 5.7 | 打 tag `v2.0.0` | tag 存在 |

**验收表：**

| 检查 | 命令 | 期望 |
|---|---|---|
| 构建 | `cargo build --release` | exit 0 |
| 打包 | `.\scripts\prepare-portable.ps1` | `portable-out/` 生成 |
| 包内容 | `ls portable-out/third_party/opencode2api-win/` | LICENSE + SOURCE.txt |
| 冷启动 | 删除 AppData 后启动 | 自动释放引擎、生成配置、40080 ready |
| 端到端 | 40080 + 50080 各发一次请求 | 均 200 |

---

## 风险与回退

| 风险 | 概率 | 影响 | 缓解 |
|---|---|---|---|
| 引擎上游改协议导致 429 复发 | 中 | 高 | 跟上游发版更新 exe；面板显示版本 |
| GPL 合规争议 | 低 | 中 | 独立进程 + 附 LICENSE/SOURCE；不分发改动版 |
| WARP 注册受限（Cloudflare 限流） | 中 | 中 | `pool_size` 可调；失败回落 `proxies` |
| 用户机器无法访问 `api.cloudflareclient.com` | 低 | 中 | 面板显示 WARP 失败原因 + 建议用代理 |
| 删除代码遗漏引用 | 中 | 低 | P2 验收表有 grep 检查 |
| 数据迁移丢自定义供应商 | 低 | 高 | P0.4 备份 + 迁移脚本 dry-run |

**回退路径：**

```powershell
git reset --hard v1.9.0-free-tier-final
# 数据：还原 _backup_20261104
```

---

## 给接手 AI 的完整提示词

```
你在 D:\AI_Projects\opencode2api_guide\opencode2api_enhance 工作。
这是一个 Go + React/Tauri 的 opencode2api 管理器，正在做 v2.0.0 大重构。

先读（按顺序）：
1. docs/ai-framework/plans/2026-11-04-custom-only-gateway.md  ← 本计划，权威
2. docs/ai-framework/phased-plan-driven.md
3. AGENTS.md
4. docs/issue-log/OPEN.md

任务：按计划 P0→P5 顺序执行。每阶段跑完验收表全部命令，把实际输出贴出来。
禁止「应该能过」——必须真实执行。

硬约束：
- 不复制 refs/opencode2api-win 的任何源码（GPL-3.0）
- 引擎以独立进程运行，HTTP 通信
- 密钥不进 git
- 默认不 push
- 每阶段结束 commit（gitmoji + 中文）

已确认的决策（不要重新讨论）：
- 独享/实例池/节点池/40080自研网关/Bun sidecar 全删
- 40080 改由 opencode2api-win 引擎提供，随包分发
- 50080 自定义供应商 + 插件供应商完整保留
- 40000 只做控制面
- 代理改为静态列表 + 每供应商 proxy 字段

回滚点：git tag v1.9.0-free-tier-final

现在从 P0 开始。每个任务完成后报告：做了什么 / 命令 / 实际输出 / 是否通过。
```

---

## 变更历史

| 日期 | 变更 |
|------|------|
| 2026-11-04 | 初版：基于 429 实测结论 + 用户决策 D1–D7 制定 |

---

## 执行记录（2026-11-04 晚）

| 阶段 | 结果 | 证据 |
|---|---|---|
| P0 | ✅ 分支 `refactor/custom-only`，tag `v1.9.0-free-tier-final` | `git log` |
| P1 | ✅ `contract.ParamTransport` + `StaticProxyTransport` | `go test ./core/contract` PASS |
| P2 | ✅ 免费层后端删除；`freetier_engine.go` | `go build ./...` / `go vet` |
| P3 | ✅ OpenCode 独立页；导航 5 项；设置/统计瘦身 | `tsc` / `npm run build` |
| P4 | ✅ 40080 启停/看门狗/healthz + 前端接入 | 隔离端口冒烟：engine `ready:true` |
| P5 | ✅ 内嵌引擎 + GPL 三件套 + 三平台 CI + v2.0.0 | `cargo check` / YAML / PS parser / 交叉编译 |
| P6 | ⏳ 桌面壳遗留模块清理 + 安装包冒烟 + tag | 见 issue-log 同日「P5 遗留」 |

**决策变更：** P5 完成后**暂不打 v2.0.0 tag**——桌面壳仍含 instance/probe/gateway 遗留模块，需 P6 清理并真机冒烟后再发版。