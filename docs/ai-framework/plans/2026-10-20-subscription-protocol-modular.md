# 阶段 X：订阅协议模块化（格式解耦 + 内核能力过滤）

> **状态：** 实施中  
> **For agentic workers:** 按 Task 顺序执行；每 Task 测完再进下一 Task。  
> **元规范:** `docs/ai-framework/phased-plan-driven.md`

**Goal:** 订阅层协议无关化：统一节点模型 + 能力矩阵单点，补齐 ssr:// 解析与 sing-box JSON 往返字段；sing-box 内核不支持的协议（SSR）在节点池可见可导出、在实例生成时明示过滤，不再「导入后启动才炸、报错含糊」。  
**Architecture:** 三段管线——格式适配器（Clash YAML / sing-box JSON / URI 列表平级）→ 协议无关 `SubscribeNode`（补 SSR/grpc/mux 字段）→ 内核能力矩阵 `caps.go`（唯一数据源，替代 singboxOutbound default 与 parseSingboxJSON 静默丢弃的知识）。API 给节点带 `usable_by_kernel` 标记，前端只做「映射展示」不重算语义（对齐 route_verdict 既有原则）。  
**Tech Stack:** Go（core/manager 为主）、React（NodesPage 徽标/摘要）、零新依赖。

**实施档位：** 全能  
**子代理：** 启用（独立审查）

---

## 前置阅读（必须）

| 优先级 | 文件 |
|--------|------|
| P0 | 本文件、`docs/ai-framework/phased-plan-driven.md`、`AGENTS.md` |
| P0 | `docs/issue-log/2026-10-20.md` #6（YAML 方言修复——本分支基线 `fb62620`） |
| P1 | `core/manager/subscribe.go`（模型+URI 表+singbox JSON 解析）、`core/manager/singbox.go`（outbound switch） |

**仓库路径：** `D:\AI_Projects\opencode2api_guide\opencode2api_enhance`  
**基线分支：** `feat/phase-x-sub-modular` = main(`4af6501`) + `fb62620`（YAML 方言修复）

## 已核实的关键事实（防接手 AI 重走弯路）

1. **「不支持的类型 ssr」源头**：`core/manager/singbox.go:356-357`（Go 权威实现）；Rust `src-tauri/src/singbox.rs:301` 是孪生死代码（lib.rs:206 invoke_handler 只剩 4 个壳命令，实例链全走 Go HTTP）。
2. **SSR 转发是内核硬约束**：官方 sing-box（钉 1.13.16，fetch-singbox.sh）无 shadowsocksr outbound，上游 PR #1828 关闭不实现。方案=解析/保存/导出全支持，仅实例出口过滤（用户已确认）。
3. **解析层已认识 ssr 类型**（Clash YAML 190 节点=86 vmess+104 ssr 全部入池），缺的是：`ssr://` URI scheme、sing-box JSON 的 `ssr` 静默丢、grpc service_name/multiplex 往返字段。
4. 实例链调用点：`core/manager/instance.go:102` `sf.BuildSingbox(node, port)`（接缝 main.go 装配）。

## Global Constraints

1. 节点池数据向后兼容：新字段全 `omitempty`，旧订阅缓存 JSON 无迁移读入。
2. 能力判定唯一数据源 = `caps.go`；singboxOutbound default、parseSingboxJSON default、前端展示必须同源（对拍测试锁定）。
3. 行为兼容：已 usable 类型的 outbound 生成逐字节不变（黄金测试锁）；SSR 节点**不得**被导入环节丢弃。
4. 真实订阅文件（含 token 的链接）**不进 testdata**；黄金用例用合成小文档。
5. 密钥不进 git；默认不 push；小步 commit（每 Task 一提交）。

## File Structure

| 文件 | 动作 | 职责 |
|------|------|------|
| `core/manager/caps.go` | 新建 | 能力矩阵：`KernelOf(nodeType) (usable bool, reason string)`（当前唯一内核 sing-box） |
| `core/manager/subscribe.go` | 修改 | SubscribeNode 补 SSRProtocol/SSRObfs/SSRObfsParam/SSRProtoParam/GrpcService/Mux*/UDPOverTCP；`ssr://` URI；parseSingboxJSON 补字段+ssr 入池 |
| `core/manager/subscribe_ssr_test.go` 等 | 新建（本地） | 黄金/矩阵/URI/往返测试（`*_test.go` 不入库策略） |
| `core/manager/singbox.go` | 修改 | default 错误文案带原因+引导；grpc transport 补 service_name；mux 透传 |
| `core/manager/clash.go` | 修改 | ClashNode→SSR 专用字段承载（protocol/obfs/obfs-param/proto-param 已有 key，核对映射） |
| `core/manager/subscriptions.go` + admin handler | 修改 | 导入响应加 `usable_count/unsupported:[{type,count,reason}]`；nodes API 每节点 `usable_by_kernel`/`kernel_reason` |
| `src/lib/api.ts` + `src/pages/NodesPage.tsx` | 修改 | toast 摘要明示；节点池不支持协议置灰+tooltip；创建实例的节点选择过滤 usable |
| `src-tauri/src/singbox.rs`、`instance.rs` | 修改（注释） | 文件头标 legacy 镜像声明 |

## 契约

```jsonc
// POST /api/admin/subscriptions/import 响应（新增字段，向后兼容）
{ "status":"ok", "imported":190, "target":"节点池",
  "usable": 86, "unsupported": [{"type":"ssr","count":104,"reason":"sing-box 内核不支持 SSR"}] }
// GET /api/admin/nodes 每项新增
{ "name":"...", "node_type":"ssr", "usable_by_kernel": false, "kernel_reason":"sing-box 内核不支持 SSR（节点可导出到第三方客户端使用）" }
```

## Task 0：黄金回归测试

合成三方言文档（flow 多行/裸 dash/JSON 化节点/`- {…}`/reality/grpc）+ 期望计数与关键字段；锁定现有 usable 类型的 outbound JSON（singboxOutbound 输出快照）。跑 `go test ./core/manager/ -run Golden` 全绿。**先于一切重构。**

## Task 1：统一模型 + caps.go

字段补全（omitempty）；caps.go 建立协议→内核能力唯一表（vmess/vless/trojan/ss 系/hy2/hy1/tuic/anytls/wg/socks/http usable；ssr/snell/v2ray 插件类 unsupported）；singboxOutbound default 改查 caps；对拍测试：switch case 清单 == caps 支持集（表驱动互证，漂移即红）。

## Task 2：URI 解析补全

`ssr://` 完整解析（base64url `host:port:protocol:cipher:obfs:base64(pass)` + 查询参 obfsparam/protoparam/tls）；`splitFragment` 复核所有 scheme 对 Shadowrocket 风格 `#名&flag=1` 容忍；vmess v2rayNG 扩展字段核对；测试进本地 `_test.go`。

## Task 3：sing-box JSON 往返

parseSingboxJSON：`ssr` 类型入池（映射进 SSR 字段）；补 grpc `service_name`、`multiplex`、`udp_over_tcp`、ss `plugin`→备注保留；未知类型不再 `continue` 静默——计数进 skipped 列表随响应上报。

## Task 4：能力过滤与明示

nodes/import API 契约落地（上表）；NodesPage：导入 toast「已入池 190（可转发 86；ssr×104 内核不支持，保留可导出）」、节点行置灰+tooltip；创建实例选点处过滤 usable（后端 StartInstance 已有兜底错误）；singboxOutbound 错误文案改「节点类型 ssr：sing-box 内核不支持，请在节点池导出到第三方客户端」。

## Task 5：Rust legacy 标注

singbox.rs / instance.rs 头注释：`DEAD CODE 镜像 — 生产链路在 core/manager（Go），保留仅作行为参考；勿在此实现新功能`。

## Task 6：全套件 + dev 端到端

`go test ./...`（flaky 判例同 OPEN.md）；`npx tsc -b && npm run build`；重编 `bin/opencode2api.exe` → 重启 tauri:dev → 飞机云订阅重导：usable 86 / ssr 104 置灰可见；实例选单不含 ssr；实例启动无「不支持的类型」。

## 验收标准总表

| # | 标准 | 通过条件 | 责任人 | 结果 |
|---|------|----------|--------|------|
| 1 | 黄金测试先行且全绿 | T0 用例存在并通过（含 outbound 快照） | 自动化 | ⬜ |
| 2 | 能力单源 | caps↔switch 对拍测试红绿；三处消费同源 | 自动化 | ⬜ |
| 3 | ssr:// 解析 | 单测：标准/带 tls/带中文 param 三形态 | 自动化 | ⬜ |
| 4 | JSON 往返 | ssr 入池、grpc/mux 字段不丢、未知类型计数上报 | 自动化 | ⬜ |
| 5 | 明示过滤 | 契约字段+UI 置灰/摘要文案（tsc/build 绿） | 自动化+人工浏览 | ⬜ |
| 6 | 行为兼容 | 既有订阅测试+全套件绿；usable 类型 outbound 快照未变 | 自动化 | ⬜ |
| 7 | 独立审查 | cat-jam 结论 ✅/⚠️（问题登记） | 独立角色 | ⬜ |
| 8 | dev 端到端 | 验收 T6 场景全过 | 端到端（会话内可测） | ⬜ |

## 风险与降级

| 风险 | 缓解 |
|---|---|
| 字段扩张破坏旧缓存 | 全 omitempty + 黄金测试反序列化旧形态 |
| caps 与 switch 漂移 | 对拍表驱动测试（红门禁） |
| 前端置灰误导（用户以为节点坏了） | tooltip 明示「可导出到第三方客户端使用」；toast 给计数 |
| grpc/mux 生成侧没配套 | T3 只做解析保留，T4 生成侧透传，往返测试锁定 |

## 明确不做（本阶段）

❌ mihomo 双内核（caps 接口预留）；❌ SSR 转发；❌ subconverter 外部转换；❌ 面板级格式配置

## 给接手 AI 的完整提示词

（本会话直接执行；留档供中断续接）

你是负责 **opencode2api_enhance 阶段 X** 的实现代理。仓库 `D:\AI_Projects\opencode2api_guide\opencode2api_enhance`，分支 `feat/phase-x-sub-modular`（已含 YAML 方言修复 fb62620）。唯一实施计划：本文件。按 T0→T6 顺序，T0 黄金测试先于一切修改；`*_test.go` 不入库（既有策略）；默认不 push；简体中文汇报；每 Task 测试后 commit。

## 残留手工验收清单

1. 用户肉眼过 NodesPage 置灰样式与 toast 文案
2. 用真实 ssr:// 链接（如有）在批量链接导入页验证
