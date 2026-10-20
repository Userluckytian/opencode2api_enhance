# 阶段 W：opencode 厂商出站协议可切 responses（prefer_responses）

> **状态：** 实施中  
> **For agentic workers:** 按 Task 顺序执行；每 Task 测完再进下一 Task。  
> **元规范:** `docs/ai-framework/phased-plan-driven.md`

**Goal:** 给 opencode 厂商加 `prefer_responses` 配置（默认 chat=现状不变），补齐 responses↔chat 翻译层三个已证缺口（图片、reasoning、tool_choice），使 opencode 上游出站可安全切到原生 Responses 协议。  
**Architecture:** 协议切换完全封在 `vendors/opencode` 内（muse 路径已是现成机制：`responsesEndpoint` 判定 → `buildResponsesRequest` 构造 → 响应侧双向翻译回 chat 契约）；core 三入口、failover/续写/竞速/日志零改动。custom/remote 的协议设置不动。  
**Tech Stack:** Go（vendor 层 + 表驱动单测）、node（上游直连探测脚本）、JSON 配置 params。

**实施档位：** 全能（默认）  
**子代理：** 启用（验收用 `@cat-jam` 独立复审；本会话实现者自测先行）

---

## 前置阅读（必须）

| 优先级 | 文件 |
|--------|------|
| P0 | `docs/ai-framework/phased-plan-driven.md` |
| P0 | 本文件 |
| P0 | `docs/issue-log/2026-10-20.md` #3/#4（四象限实测 + 三缺口定位，本计划的证据基座） |
| P1 | `AGENTS.md`、`vendors/opencode/responses.go` 头注释、`vendors/opencode/gate.go` 头注释 |

**仓库路径：** `D:\AI_Projects\opencode2api_guide\opencode2api_enhance`  
**基线分支：** 从 `main`（≥ `4af6501`，含日志误标修复与文档修正）拉 `feat/phase-w-opencode-responses`

---

## Global Constraints（冲突时以本节为准）

1. **默认行为逐字节不变**：`prefer_responses` 缺省 = `"chat"`，非 muse 模型走现有 chat 路径不受任何影响；muse 前缀继续强制 responses（现状）。
2. 只改 `vendors/opencode/`（+ registry params 声明 + 文档）；**不动** custom/remote/core 契约/三入口 handler。
3. 探测上游低频：每请求 ≤16 output tokens、间隔 ≥6s、轮换不同 SOCKS 出口；403 先怀疑出口风控（2026-10-13 教训），换出口复测再下结论。
4. 密钥不进 git；探测材料脚本不得含实例 password（复用 `%TEMP%/oc_mkprobe.mjs` 思路，脚本本体入库、密钥走命令行 env）。
5. **明确不做（本阶段）**
   - ❌ 翻默认路径为 responses（灰度由用户以配置驱动，数据后再议）
   - ❌ 前端面板字段（YAGNI：JSON params 已可配；面板接线留下阶段）
   - ❌ `previous_response_id` 直通透传（若 T1 证实上游支持，收益属入站直通路改造范畴，另立阶段）
   - ❌ custom/remote 协议联动
6. **Git：** 小步 commit（每 Task 一提交）；**默认不 push**。
7. **证据优先**：验收表每行有实际跑过的命令输出；探测原始 SSE 存 `docs/ai-framework/plans/probes/results/`（本地，不入库），摘要进 `probes/RESULTS.md`（入库）。

---

## 阶段开头：上阶段遗留

| 上阶段遗留项 | 来源 | 未通过原因 | 状态 |
|-------------|------|-----------|------|
| 无（v1.7.8-beta.6 验收全绿；今日两项日志修复已提交 `1fbf339` 待部署复验，不阻塞本阶段） | — | — | — |

---

## 与前后阶段

| 阶段 | 状态 | 交付 |
|------|------|------|
| 上阶段（beta.6） | ✅ | 第二轮埋点 schema v3 |
| **本阶段 W** | ⬜ | prefer_responses 开关 + 三缺口修复 + 上游行为矩阵文档 |
| 下阶段（若数据支持） | | 灰度切默认 / 入站 responses 直通路（OPEN.md 2026-09-09 项续） |

---

## File Structure（预期变更）

| 文件 | 动作 | 职责 |
|------|------|------|
| `docs/ai-framework/plans/probes/oc-gate-body.mjs` | 新建 | 探测请求体生成器（门禁合规 body，支持 reasoning/tool_choice/image/prev_id 场景） |
| `docs/ai-framework/plans/probes/RESULTS.md` | 新建 | T1 探测结论表（reasoning 载荷形态 / tool_choice 矩阵 / input_image 支持 / 上游响应 id 形态） |
| `vendors/opencode/opencode.go` | 修改 | `Config.PreferResponses` 字段 |
| `vendors/opencode/registry.go` | 修改 | `ParamPreferResponses = "prefer_responses"`（非 `_` 前缀 → JSON 可配，同 `client_version` 范式）+ 装配读取 |
| `vendors/opencode/responses.go` | 修改 | 端点选择与 muse 解耦；`responsesInputFromMessages` 补 `input_image`；SSE 翻译补 reasoning 事件；回落判定 |
| `vendors/opencode/chat.go` | 修改 | `useResponses` 决策接入 `prefer_responses` + 回落保护（按请求特征） |
| `vendors/opencode/responses_test.go` 等 | 修改/新建 | 决策表、图片映射、reasoning 翻译、回落保护单测 |
| `docs/PLUGIN-PROVIDERS.md` | 修改 | opencode 行补「可配置 prefer_responses」 |

---

## 配置契约

```json
{ "providers": [ { "id": "opencode", "type": "opencode",
  "params": { "prefer_responses": "chat" } } ] }
```

| 值 | 语义 |
|----|------|
| `"chat"`（缺省） | 现状：仅 muse 前缀走 responses |
| `"responses"` | opencode 全部模型优先 responses 出站；命中回落特征（见 T1 结论钉死：至少含 tool_choice required/命名函数，视探测结果含图片、reasoning 需求）自动走 chat 兜底 |
| 非法值 | registry 装配期 warn + 按 `chat` 处理（不炸网关） |

---

## Task 1：上游行为探测（reasoning / tool_choice / input_image / 响应 id）

**Files:** `probes/oc-gate-body.mjs`、`probes/RESULTS.md`

**行为:** 复刻生产形态（v2 身份 + 门禁 body + SOCKS 轮换出口）对 `inference/v1/responses` 直测 6 场景，回答三缺口与收益边界：
- P1 reasoning 载荷：deepseek 带 `reasoning:{effort:medium,summary:auto}` → 事件流里 reasoning 是明文 delta、摘要、还是 encrypted？（决定 T3-2 翻译策略）
- P2a/P2b/P2c tool_choice：`"none"` / 命名函数对象 / `required` 三态各一次 → 200 还是 400？（钉死回落保护清单）
- P3 input_image：1×1 红像素 data URI + 「图是什么颜色」→ 答 red=支持；400=不支持→回落
- P4 响应 id 形态：流中 `response.completed` 的 id 前缀（`resp_` 原生 vs 网关 `chatcmpl-` 假 id 只在翻译层出现）——顺带确认 `store` 字段行为（不做 previous_response_id 请求，超范围）

**Steps:**
1. 写 body 生成器（扩展现有 `oc_mkprobe.mjs`：入参场景名，出 body 文件 + 合规 ses_）
2. 跑 6 探测（curl --socks5-hostname 轮换 42100-42105，间隔 ≥6s），原始流存 `probes/results/`
3. 结论写 `probes/RESULTS.md`（每场景：请求摘要 / HTTP 码 / 关键证据片段 / 对 T2/T3 的约束）
4. Commit：`🔧 chore(probe): opencode 上游 responses 行为矩阵探测`

---

## Task 2：prefer_responses 配置开关

**Files:** `opencode.go`、`registry.go`、`responses.go`（端点选择解耦）、`chat.go`（决策接入）、单测

**行为:** `cfg.PreferResponses`：muse 恒 responses（不变）；`"responses"` 时全模型 responses 出站，端点按 auth/surface 三档（inference/zen/zen-go）选择；回落保护按 T1 结论生效；默认/非法值 = chat 现状。

**Steps:**
1. 单测先行：决策表用例（prefer × muse × authmode × 回落特征 → chat/responses + URL 断言），仿 `responses_test.go` 现有两个端点用例
2. 实现；跑 `go test ./vendors/opencode/ -run Responses -count=1` → 全绿
3. `go build ./...` → exit 0；Commit：`✨ feat(opencode): prefer_responses 出站协议开关（默认 chat 不变）`

---

## Task 3：缺口修复（按 T1 结论实施）

**Files:** `responses.go`、单测

**行为:**
- T3-1 图片：若 P3 支持 → `responsesInputFromMessages` 把 `image_url` part 映射 `input_image`（data URI 直传）；不支持 → 带图请求强制回落 chat 并在 RESULTS.md 记录
- T3-2 reasoning：按 P1 实际形态补 SSE 翻译（reasoning 事件 → chat `delta.reasoning_content`），不可译形态（加密）→ 记录并保证不误吞正文
- T3-3 tool_choice：按 P2 矩阵把不可表达值加入回落清单

**Steps:**
1. 每项先加失败用例再修（TDD）
2. `go test ./vendors/opencode/ -count=1` 全绿；`go build ./...` exit 0
3. Commit：`🐛 fix(opencode): responses 翻译层补图片/reasoning/tool_choice 缺口`

---

## Task 4：全套回归 + 文档

**Steps:**
1. `go test ./... -count=1`（core/manager 已知 flaky：`TestHTTPRequestRaw` 等负载敏感，红了单包复跑判定，见 OPEN.md 2026-10-13）
2. `docs/PLUGIN-PROVIDERS.md` opencode 行 + `docs/CONFIGURATION.md` params 表补 `prefer_responses`
3. 探测/灰度手册段：如何开 `"responses"`、日志页看哪些指标对比（失败率/「上下文超限」/「单节点反复中断」/TTFT）、如何回退
4. Commit：`📝 docs(opencode): prefer_responses 配置与灰度手册`

---

## 验收标准总表

| # | 标准 | 通过条件 | 验证责任人 | 结果 |
|---|------|----------|-----------|------|
| 1 | T1 行为矩阵有真实数据 | 6 场景原始流落盘 + RESULTS.md 每场景有 HTTP 码与证据片段 | 自动化（执行方） | ⬜ |
| 2 | 默认零变化 | 单测：prefer 缺省时非 muse 决策 = chat（构造与 URL 逐字节路径不变） | 自动化 | ⬜ |
| 3 | 开关生效 | 单测：prefer=responses → 三档端点选择正确 + muse 不受 prefer 影响 + 回落特征命中 chat | 自动化 | ⬜ |
| 4 | 缺口修复 | T3 各失败用例转绿；图片/reasoning 结果按 T1 约束（不可支持项以回落/文档收口即算过） | 自动化 | ⬜ |
| 5 | 全套件 | `go test ./...` 绿（flaky 按 OPEN.md 判例单包复跑） | 自动化 | ⬜ |
| 6 | 独立代码审查 | `@cat-jam` 审查结论 ✅/⚠️（问题登记进本表） | 独立角色 | ⬜ |
| 7 | 密钥红线 | `git ls-files` 无凭据；probes/ 无实例 password | 自动化 | ⬜ |
| 8 | 真实灰度冒烟 | 部署机：prefer=responses + 网关 `/v1/chat/completions` 实调 deepseek，日志显示成功且无图片/reasoning 回归 | 端到端（需用户/部署机） | ⬜ |

---

## 风险与降级

| 风险 | 缓解 |
|------|------|
| 上游对非 muse 的 responses 行为与 muse 不同（reasoning 加密、tool 限制更宽/更严） | T1 先行，T2/T3 决策表按实测钉死；本计划所有"假设"均标⬜待 T1 校准 |
| 探测触发出口风控 | 低频 + 轮换出口 + 403 换出口复测规则（Global Constraints 3） |
| 翻译层改动影响 muse 现网 | muse 用例（端点选择 2 个既有单测）保持红绿门禁 |
| 灰度后上游 responses 稳定性未知（EOF 行为是否优于 chat） | 默认 chat；灰度手册给对比指标与一键回退（改回 params） |

---

## 给接手 AI 的完整提示词

（本阶段由当前会话直接执行；如下留档供中断续接）

你是负责 **opencode2api_enhance 阶段 W** 的实现代理。仓库 `D:\AI_Projects\opencode2api_guide\opencode2api_enhance`，分支 `feat/phase-w-opencode-responses`（不存在则从 main 创建）。唯一实施计划：本文件。必读：`docs/ai-framework/phased-plan-driven.md`、`AGENTS.md`、`docs/issue-log/2026-10-20.md` #3/#4。严格按 Task 1→4 顺序；探测走 SOCKS 出口轮换（127.0.0.1:42100-42105）与 ≥6s 间隔；403 先换出口复测再下结论；每 Task 测完 commit；默认不 push；简体中文汇报。现在开始。

---

## 残留手工验收清单

1. 部署机开 `"prefer_responses": "responses"`，跑半天真实流量，日志页对比失败率/EOF/上下文超限分布（验收 #8）
2. 面板配置字段（下阶段候选）
