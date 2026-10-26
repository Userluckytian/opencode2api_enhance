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
| `chain_groups` | 需要链式的目标组列表（逗号分隔；空 = 所有组） |
| `front_group` | 前置候选组（如 `merged.yaml`） |
| `front_node` | 手动前置节点（优先于自动；空 = 自动） |
| `chain_auto_detect` | 自动检测（默认 true）：逐候选测试整条链 |

### 前置组策略

- **前置组 = 候选节点组**，不是目标组。候选条件：`group == front_group`、
  `kernelUsable`、有凭据、且不是目标节点本身。
- **自动检测必须测整条链**：临时启动 `front → target` 的 sing-box，
  经其 HTTP inbound 请求 `https://opencode.ai/zen/v1/models`；
  200 才算可用。只测前置自身健康不够（前置可达 ≠ 前置能到目标）。
- 结果按目标节点缓存 10 分钟，避免每次启动重复探测。
- 手动 `front_node` 优先；若未配置则按候选顺序探测（最多 4 个）。

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

- `StartInstance`：目标组命中 `chain_groups` 时解析前置，生成链式 sing-box 配置。
- 扫描探针：同一规则；扫描优先使用手动/缓存前置，避免 12s 预算内做完整探测。
- 网关无需改动（它只连实例 sing-box 的 SOCKS 端口，链已在实例内完成）。

### UI

设置页新增「链式代理」卡片：开关、目标组、前置组、手动前置节点、自动检测；
「检测链路」调用 `POST /api/admin/chain/check`，返回选中的前置与耗时。

## 验收

| 项 | 命令/证据 | 期望 |
|----|-----------|------|
| 配置生成 | 单测 | ✅ `TestBuildSingboxConfigWithFront`：front outbound + `detour:"front"` |
| 手动前置 | `front_node=node2` + `/chain/check` | ✅ front=node2，2097ms |
| 自动检测 | `/chain/check`（front_node 空） | ✅ 自动选中 node1，1683ms，缓存 10 分钟 |
| 实例端到端 | Falemon US G2 实例 chat | ✅ 200 "CHAIN"；singbox.json 含 front/detour |
| 扫描 | Falemon 组 | ✅ 2/3 200（front=node1）；1 个真实 429 额度耗尽 |
| 构建/单测 | `go build ./...` + 目标测试 | ✅ 通过 |
