# 阶段：前置节点链式代理（chain front node）

> 状态：已完成（2026-10-26）。目标：让本地直连被阻断的节点（如 Falemon trojan 组）
> 经一个可达的“前置组”节点中转后仍可正常出网，出口保持目标节点。

## 背景

- 部分订阅节点（Falemon trojan）本地 TCP 直连超时，但经 merged hysteria2 节点可达。
- 临时 sing-box 已验证：`front = merged node1` + `proxy = Falemon US G2 (detour: front)`
  → `/zen/v1/models` 200、免费 chat 200。
- 应用当前只生成单个 outbound，无 `detour` 支持。

## 设计

### 配置（应用级）

| 键 | 含义 |
|----|------|
| `chain_enabled` | 总开关（默认 false） |
| `chain_groups` | 需要链式的目标组列表（逗号分隔） |
| `chain_nodes` | 需要链式的具体节点（一键探测规划写入；优先于组） |
| `front_group` | 前置候选组（如 `merged.yaml`） |
| `front_node` | 手动前置节点（优先于自动；空 = 自动） |
| `chain_auto_detect` | 自动检测（默认 true）：逐候选测试整条链 |

> `chain_nodes` 非空时只认白名单；`chain_nodes` 与 `chain_groups` 都为空才表示“所有组”。

### 前置组策略

- **前置组 = 候选节点组**，不是目标组。候选条件：`group == front_group`、
  `kernelUsable`、有凭据、且不是目标节点本身。
- **自动检测必须测整条链**：临时启动 `front → target` 的 sing-box，
  经其 HTTP inbound 请求 `https://opencode.ai/zen/v1/models`；
  200 才算可用。只测前置自身健康不够（前置可达 ≠ 前置能到目标）。
- 结果按目标节点缓存 10 分钟，避免每次启动重复探测。
- 手动 `front_node` 优先；若未配置则按候选顺序探测（最多 4 个）。
- **一键探测/规划**（扫描 `chain=auto`）：先直连；直连成功或真实 429 不套链；
  连接类失败自动经前置重试。结果汇总为「直连可用 / 链式可用 / 额度 429 / 不可用」，
  「应用计划」只把链式可用节点写入 `chain_nodes`，直连可用节点保持直连。

### sing-box 配置

```json
{
  "outbounds": [
    { "type": "hysteria2", "tag": "front", ... },
    { "type": "trojan", "tag": "proxy", ..., "detour": "front" },
    { "type": "direct", "tag": "direct" }
  ],
  "route": { "final": "proxy" }
}
```

### 接入点

- `StartInstance`：节点命中 `chain_nodes` 或 `chain_groups` 时解析前置，生成链式 sing-box 配置。
- 扫描探针：默认按配置；`chain=auto` 时先直连，连接失败后经前置重试（一键探测）。
- 网关无需改动（它只连实例 sing-box 的 SOCKS 端口，链已在实例内完成）。

### UI

- 「链式代理（前置节点）」放在**节点池 → 设置**弹窗：目标组/目标节点、前置组、
  手动前置、自动检测、一键探测（全部节点）、应用计划、检测链路。
- 「OpenCode 免费层 Sidecar」并入**设置 → 关于**卡片，与 `opencode2api.exe` /
  `sing-box.exe` 子程序状态放在一起。

## 验收

| 项 | 命令/证据 | 期望 |
|----|-----------|------|
| 配置生成 | 单测 | ✅ `TestBuildSingboxConfigWithFront`：front outbound + `detour:"front"` |
| 手动前置 | `front_node=node2` + `/chain/check` | ✅ front=node2，2097ms |
| 自动检测 | `/chain/check`（front_node 空） | ✅ 自动选中 node1，1683ms，缓存 10 分钟 |
| 实例端到端 | Falemon US G2 实例 chat | ✅ 200 "CHAIN"；singbox.json 含 front/detour |
| 扫描 | Falemon 组 | ✅ 2/3 200（front=node1）；1 个真实 429 额度耗尽 |
| 一键探测 | `scan chain=auto` | ✅ 直连可用不套链；直连失败经前置；429 不套链 |
| 节点白名单 | `chain_nodes` + `/chain/check` | ✅ chain_groups 为空时仍按节点名走链 |
| UI 归位 | 设置页 / 节点池设置 | ✅ Sidecar 并入「关于」；链式代理移入「节点池设置」 |
| 构建/单测 | `go build ./...` + `npm run build` + 目标测试 | ✅ 通过 |
