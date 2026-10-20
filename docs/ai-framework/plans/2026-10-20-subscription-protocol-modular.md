# 阶段 X：订阅协议模块化（格式解耦 + 内核能力过滤 + 刷新安全）

> **状态：** 实施中（v2：已并入 uniproxy 借鉴决策）  
> **For agentic workers:** 按 Task 顺序执行；每 Task 测完再进下一 Task。  
> **元规范:** `docs/ai-framework/phased-plan-driven.md`

**Goal:** 订阅层协议无关化：统一节点模型 + 能力矩阵单点，补齐 ssr:// 解析与 sing-box JSON 往返字段；sing-box 内核不支持的协议（SSR）在节点池可见可导出、实例生成时明示过滤；同时借 uniproxy 设计补上**节点指纹身份、节点级错误收集、刷新删除保护**三块安全地基。  
**Architecture:** 三段管线——格式适配器（Clash YAML / sing-box JSON / URI 列表平级）→ 协议无关 `SubscribeNode`（补 SSR/grpc/mux/UDP 字段 + 指纹）→ 能力矩阵 `caps.go`（protocol → {singbox, udp 三态, reason}，唯一数据源）。API 给节点带 `usable_by_kernel` 标记，前端只做映射展示不重算语义。  
**Tech Stack:** Go（core/manager 为主）、React（NodesPage 徽标/摘要）、零新依赖。

**实施档位：** 全能  
**子代理：** 启用（独立审查）

---

## uniproxy 借鉴决策记录（D:\AI_Projects\refs\uniproxy，只读调研 2026-10-20）

| # | 借鉴点 | uniproxy 出处 | 本阶段采纳形态 |
|---|--------|--------------|----------------|
| B1 | 指纹=节点身份（排除显示名，含协议/服务器/凭据/UDP） | `importer/parser.go:386-416` | SubscribeNode 加 `Fingerprint`；刷新 diff、实例身份匹配（现 `name+server:port`）改指纹 |
| B2 | ParseResult：errors≤128 带行号 + totalErrors + 坏条目不杀好节点 | `parser.go:50-60` `model/types.go:16-25` `errors_test.go` | `parseSubscription` 增返回 issues；URI 列表逐行收集（现状整行静默丢）；导入摘要的底层数据 |
| B3 | 刷新两道闸：解析有错→全留旧；删>50% 或空→需 force | `app/imports.go:264-266, 282-284` | `saveSubscriptionCacheGrouped` 前置闸（自动拉取路径 force=false；手动/预览路径可带 force） |
| B4 | UDP 能力三态（opt-in/强制/禁止） | `importer/native.go:53-67` + `core/compiler.go:402-410` | 并入 caps.go 表字段；池选点谓词一行 |
| B5 | canonical 协议别名表（单 switch 收拢 hy2→hysteria2 等） | `importer/values.go:233-249` | caps.go 内实现，替换散落 EqualFold 别名判断 |

**明确不采纳（含理由）**：
- ✗ 消费式 fields.finish() unknown-key 即报错（`values.go:176-190`）：它的输入是白盒 fixture，我们面对真实机场杂字段（cfw-*、自定义注释键），严格化会批量误杀现有可用订阅；错误可见性由 B2 达成。
- ✗ safehttp 全套（保留 CIDR 表/Dial-before-trust/逐跳重定向复验）：价值确认但改动面独立成阶段（候选阶段 Y）；本阶段只在 `fetchSubscriptionWithMeta` 加**最小一步**：重定向 ≤4 跳 + 拒 HTML 页当订阅（防登录门户误存）。
- ✗ fake-ip DoH 复核：uniproxy 也只用于订阅下载层；我们内核场景不同，不做。
- ✗ SSR 支持：uniproxy 同样拒绝 ssr（`security_test.go:18` 夹具证实）——它用 mihomo 才全协议。我们内核 sing-box，维持「解析/保存/导出支持、转发明示不支持」。

## 前置阅读（必须）

| 优先级 | 文件 |
|--------|------|
| P0 | 本文件、`docs/ai-framework/phased-plan-driven.md`、`AGENTS.md` |
| P0 | `docs/issue-log/2026-10-20.md` #6（YAML 方言修复——本分支基线 `fb62620`） |
| P1 | `core/manager/subscribe.go`（模型+URI 表+singbox JSON）、`core/manager/singbox.go`（outbound switch）、上面 B1-B5 出处的 uniproxy 对应文件 |

**仓库路径：** `D:\AI_Projects\opencode2api_guide\opencode2api_enhance`  
**基线分支：** `feat/phase-x-sub-modular` = main(`4af6501`) + `fb62620`

## 已核实的关键事实

1. 「不支持的类型 ssr」源头：`core/manager/singbox.go:356-357`；Rust `singbox.rs:301` 孪生死代码（invoke_handler 仅 4 壳命令）。
2. SSR 转发是内核硬约束：官方 sing-box（1.13.16）无 shadowsocksr outbound，上游 PR #1828 关闭。
3. 解析层已认识 ssr（Clash YAML 190 节点全入池），缺：`ssr://` scheme、sing-box JSON `ssr` 静默丢、grpc `service_name`/mux 往返。
4. 实例链：`core/manager/instance.go:102` `sf.BuildSingbox`；订阅刷新覆盖写：`saveSubscriptionCacheGrouped`（subscribe.go:1391）。
5. reality 往返字段现状已有（黄金测试证实），真正缺口是 grpc/mux/ssr。

## Global Constraints

1. 向后兼容：新字段全 `omitempty`，旧订阅缓存 JSON 无迁移读入；已 usable 类型的 outbound 生成逐字节不变（黄金快照锁）。
2. 能力判定唯一数据源 = `caps.go`；singboxOutbound default、parseSingboxJSON default、前端展示同源（对拍测试红门禁）。
3. SSR 节点**不得**被导入环节丢弃（B2/B3 之后仍如此：可解析→可入库→可导出，只是不打上实例）。
4. 真实订阅文件（含机场 token）不进 testdata；黄金用例合成。
5. 密钥不进 git；默认不 push；每 Task 测后 commit。

## File Structure

| 文件 | 动作 | 职责 |
|------|------|------|
| `core/manager/caps.go` | 新建 | B4/B5：协议别名 canonical 表 + protocol→{singbox, udp 三态, reason} 能力矩阵；消费点统一从这里取 |
| `core/manager/subscribe.go` | 修改 | SubscribeNode 补 SSR*/GrpcService/Mux*/UDP 字段 + Fingerprint（B1）；`ssr://` URI；parseSingboxJSON 补字段、ssr 入池、未知类型计 issues（B2）；导入摘要带 usable/unsupported |
| `core/manager/subscription_diff.go` | 新建 | B1+B3：指纹计算、refresh 两道闸、旧 name 身份→指纹的迁移匹配 |
| `core/manager/singbox.go` | 修改 | default 查 caps（错误文案含原因+引导）；grpc service_name、mux 透传 |
| `core/manager/subscriptions.go` + handlers | 修改 | import/links 响应 usable_count + unsupported[]；nodes API 每节点 usable_by_kernel/kernel_reason；刷新路径接两道闸 |
| `src/lib/api.ts` + `src/pages/NodesPage.tsx` | 修改 | toast 摘要；不支持协议置灰+tooltip（文案含「可导出到第三方客户端」） |
| `core/manager/zz_*_test.go`（本地） | 新建 | 黄金（T0 已有）、caps 对拍、指纹、两道闸、ssr URI、JSON 往返 |
| `src-tauri/src/singbox.rs`、`instance.rs` | 注释 | legacy 镜像声明 |

## 契约

```jsonc
// POST /api/admin/subscriptions/import（新增字段，向后兼容）
{ "status":"ok", "imported":190, "target":"节点池",
  "usable":86, "unsupported":[{"type":"ssr","count":104,"reason":"sing-box 内核不支持 SSR（节点可导出到第三方客户端）"}],
  "issues":[{"line":7,"reason":"第 7 行：未知协议 unknown://"}], "issues_truncated":false }
// GET /api/admin/nodes 每项新增
{ "name":"...", "node_type":"ssr", "usable_by_kernel":false, "kernel_reason":"..." , "fingerprint":"<12hex 短指纹>" }
// 刷新保护（自动拉取路径）
{ "status":"refresh_rejected", "reason":"subscription_shrink", "message":"解析后节点数比现有少 62%，已保留原节点；确认无误可手动强制刷新" }
```

## Task 0：黄金回归 ✅ 已完成

`zz_golden_x_test.go`：三方言（Clash YAML flow/裸dash/JSON化节点、URI 四 scheme、sing-box JSON）+ usable 类型 outbound 快照 + ssr 必报错事实锁。全绿。附带发现：reality 往返已存在（原计划的 T3 范围据此收窄）。

## Task 1：caps.go 能力矩阵（B4+B5）

canonical 别名表 + 能力表（singbox/udp 三态/reason）；singboxOutbound default 与 subscribeFromClash 强制 TLS 名单、parseSingboxJSON default、authlessProxyTypes 改读 caps；对拍表驱动测试锁漂移。行为等价迁移（对拍红=任何一处名单漂移）。

## Task 2：模型补全 + ssr:// URI（B1 一半 + T2 原案）

SubscribeNode/ClashNode 补 SSR 四字段（protocol/obfs/obfs-param/proto-param）+ GrpcService + Mux 开关 + Fingerprint（SHA256 前 12 hex，输入排除 name）；`ssr://` 完整解析（base64url 六段 + query 中文 param percent 容错）；parseSingboxJSON `ssr` 入池 + grpc service_name + mux；vmess/vless/trojan/ss 指纹覆盖。

## Task 3：B2 错误收集 + B3 刷新两道闸

`parseSubscription` 返回 issues（≤128+total 计数，坏行不杀好节点；URI 逐行、JSON 逐 outbound、YAML 计 skipped）；`subscription_diff.go`：refresh 按指纹 diff；两道闸接进 `saveSubscriptionCacheGrouped` 自动路径（手动 import 端点默认也走闸，带 `force:true` 可越——force 不豁免解析错误）。

## Task 4：明示过滤与前端

契约字段落地（API + 节点行徽标/置灰 + toast「已入池 190：可转发 86；ssr×104 内核不支持，保留可查可导出」）；实例选点入口过滤 usable；错误文案「节点类型 ssr：sing-box 内核不支持，请在节点池导出到第三方客户端」。

## Task 5：最小下载加固 + Rust legacy 标注

`fetchSubscriptionWithMeta`：重定向显式 ≤4 跳、HTML 页识别拒收（uniproxy looksLikeHTML 思路，20 行内）；singbox.rs/instance.rs 头注释 DEAD CODE 镜像声明。

## Task 6：全套件 + dev 端到端

`go test ./...`（flaky 判例 OPEN.md）；`npx tsc -b && npm run build`；重编 bin → tauri:dev：飞机云重导（usable 86/ssr 104 置灰/摘要文案）；改订阅名节点身份不碎（指纹）；模拟机场返回半截→旧缓存保留（两道闸）；实例选单无 ssr、启动无「不支持的类型」。

## 验收标准总表

| # | 标准 | 通过条件 | 责任人 | 结果 |
|---|------|----------|--------|------|
| 1 | 黄金测试先行全绿 | T0 存在且通过 | 自动化 | ✅ (fb62620+T0) |
| 2 | 能力单源 | caps↔全部消费点对拍绿 | 自动化 | ⬜ |
| 3 | ssr:// 与 JSON 往返 | 单测：标准/中文param/reality/grpc service_name | 自动化 | ⬜ |
| 4 | 指纹身份 | 改名 diff=0 删除；旧 name 身份迁移匹配测试 | 自动化 | ⬜ |
| 5 | 错误收集 | 坏行夹好节点存活（uniproxy errors_test 同款不变量） | 自动化 | ⬜ |
| 6 | 两道闸 | shrink>50%/解析错→旧缓存保留；force 语义 | 自动化 | ⬜ |
| 7 | 明示过滤 | 契约字段 + tsc/build 绿 + UI 置灰 | 自动化+人工 | ⬜ |
| 8 | 行为兼容 | usable outbound 快照未变；全套件绿 | 自动化 | ⬜ |
| 9 | 独立审查 | cat-jam ✅/⚠️（问题登记） | 独立角色 | ⬜ |
| 10 | dev 端到端 | T6 场景全过 | 会话内端到端 | ⬜ |

## 风险与降级

| 风险 | 缓解 |
|---|---|
| 指纹上线后旧缓存身份断裂 | 首次 diff 回退 name 匹配（迁移期双轨），迁移测试锁 |
| 两道闸误伤正常换机场 | 手动刷新带 force；toast 明示拒绝原因与操作指引 |
| caps 与 switch 漂移 | 对拍红门禁（表驱动互证） |
| ssr:// 形态多样（旧版无 proto-param） | 解析器分级容错：缺段进 issues 不猜默认 |

## 明确不做（本阶段）

❌ mihomo 双内核（caps 预留 kernels 维度扩展）；❌ SSR 转发；❌ unknown-key 严格模式（见决策记录）；❌ safehttp 全套（阶段 Y 候选）；❌ 面板级格式配置

## 给接手 AI 的完整提示词

（本会话直接执行；留档续接）

你是负责 **opencode2api_enhance 阶段 X** 的实现代理。仓库根目录即当前工作目录，分支 `feat/phase-x-sub-modular`（含 T0 黄金测试）。唯一计划：本文件（重点读「uniproxy 借鉴决策记录」与「明确不采纳」，不要重新发明被拒方案）。按 T1→T6 顺序；T0 黄金先跑绿再动任何代码；`*_test.go` 不入库（既有策略）；默认不 push；简体中文汇报。

## 残留手工验收清单

1. 用户过目 NodesPage 置灰与 toast 文案
2. 真实 ssr:// 链接（如有）批量导入验证
3. 「换机场被闸拦截」场景的用户话术确认
