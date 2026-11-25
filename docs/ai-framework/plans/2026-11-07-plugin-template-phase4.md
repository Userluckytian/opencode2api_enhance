# 阶段计划：PLT-4 全量推广（11 家迁移 + 面板真数据 + UI 对齐 + 统一真机/浏览器验收）

> 依据：勘察存档 `2026-11-07-plt4-survey.md`（11 家五域 files:line 级事实）+ phase3 已验收的迁移方法论（§12 映射表/adapter 优先/语义等价/用户在途快照先行）+ 05 能力矩阵（必 128/可 15/N/A 26）。
> **试点已验证的方法直接复用**：配置 schema 化+旧键迁移装载（xhx 模式）/整体换 routing+schedule 保旧测试（flonno 类）/单 exe 定案（runbios 模式）/面板 go:embed 字节一致抽取。

## 0. 元信息

| 项 | 值 |
|----|----|
| Goal | 其余 11 家插件全部迁到 pluginsdk+模板；面板按 05 矩阵接真数据/降级展示/显式 N/A；UI 对齐 console-mockup 令牌层 |
| 名单 | qw2api、mmxproxy、flonno2api、grok、skywork、lobsterai、lobsterhub、devin、loomy（9 家迁移）+ **vibex 冻结不迁移**（勘察裁决：号已封/五域全空/Node 异构，收益为零）+ loomy 降级为轻迁移 |
| 运行模式 | 目标模式（沿用 PLT-1/2/3 工作方式；worker/GLM-5.3-Flash） |
| 档位 | 全能 + 子代理审查/验收 |
| 验收策略 | **每批离线门即时跑；真机/浏览器统一测试放最后一批**（用户定）——其中面板/聚合/UI 层由主会话用内置浏览器执行，真实凭据类才移交用户 |
| 红线 | 沿用 PLT 计划 §1 + phase3 红线；新增：①用户在途改动先快照再动手（phase3 教训）；②树卫生与 `_source.note` 修复是各批 T0 前置；③live 测试分层（offline 必绿，live 标待人工/浏览器批次） |

## 1. 迁移分批（风险递增，批次内可并行）

| 批 | 家 | 形态 | 关键 Task |
|----|----|------|----------|
| **A 直接映射型** | qw2api | 平铺+30 键先例齐 | 配置 schema 化（§12 复用 switch_threshold 行）+ 裁 tray/独立入口 + routing（firstByteGate 先例回流模板）+ schedule credits_refresh + temp.tf/autoreg 链接 tempmail/register + 面板 embed |
| | mmxproxy | 平铺 | `_source.note` 修复（T0）+ providerChain 4 源→tempmail.Registry + 面板固定端口→sidecar 化 + schedule/register 段映射 |
| | flonno2api | 双模块 | 双模块构建定案（T0）+ 整体换 routing/schedule 保 24 测试绿 + captcha_*→challenge（turnstilePatch 已在包内）+ 运行残留清理 |
| **B 结构特殊型** | skywork | 15 包裁剪 | 裁剪面（删 cmd/qw2api 遗产/tray）+ **双键族分别映射**（clash_*→守护私有；egress_clash_*→egress 段）+ 76 测试 offline/live 分层 + switch_threshold→extras |
| | grok | 双层托管 | 外壳接 schedule（pool_* 13 键）/register/tempmail 段映射；**内核托管与外部注册机 exe 契约保留**（不动 grok2api）；配额跨进程聚合维持 |
| | lobsterai | 每号一线路 | egress ClashPinned 对接（env LB2A_CLASH_*→provider.json 键）+ keeper 单飞与冷却语义裁决 + OAuth 登录链保持插件私有 |
| | lobsterhub | Wails 双模式 | 定源剥离（同 buddyhub 方法）+ dailySlots=[60,1380]→schedule + 共享绑定表耦合处理（与 lobsterai 协调） |
| **C 轻型/异构** | loomy | 轻迁移 | 仅两项：30min 领奖 tick→schedule 包、points→quota 模型；架构/面板不动 |
| | devin | Node 异构 | **Go 移植不做**（勘察裁决）：provider.json schema 化（§12 devin 行）+ 面板对齐 tokens + kernel 内部 register/mail/egress.mjs 保留（参照资产地位写入 PLUGIN-CAPABILITIES 注记） |
| | vibex | 冻结 | 不迁移；provider.json 加 `_note` 冻结声明；从 05 矩阵落地名单划出 |

## 2. 每家通用 Task 模板（批次 Task 按此实例化）

```
T-n.0 树卫生与定源：_source.note 与现树对齐 / 遗产入口与 backup 目录清理（用户确认清单后执行）/ 用户在途快照先行
T-n.1 配置 schema 化：§12 映射逐键 + 旧形态兼容装载单测（xhx 模式）
T-n.2 能力包接入：按勘察五域「有则接、无则显式 N/A」（--without 裁剪生成或等价）
T-n.3 面板：常量字符串→go:embed 字节一致抽取 + tokens 注入 + 05 矩阵判级视图（真数据/降级/N/A）
T-n.4 离线门：build/vet/test（语义等价基线=既有测试）+ selftest.ps1 契约自测
```

## 3. UI 对齐与真数据（随 T-n.3 落地，不单独立阶段）

- 令牌层：11 套主题 tokens.css 注入各面板（沿用 T3 产物 scaffold/panel/assets）；每家按 05 矩阵/已定调色板选主题
- 真数据：面板数据端点统一「面板 POST → 插件服务端代转」模式（05:101）；账号池/积分列对齐 AccountQuota 字段（04 §4）；N/A 视图显示统一空态文案
- eventlog 缺口：loomy（无）/vibex（冻结）不补；其余已有或有 eventlog 的保持

## 4. 统一验收批次（最后执行，用户定的顺序）

| 层 | 执行人 | 内容 |
|----|--------|------|
| **浏览器可测（主会话执行）** | 主会话+内置浏览器 | portable 48200 起多实例 → 逐家：聚合目录出模型、面板 13 视图导航、05 判级逐格核对（真数据/降级/N/A 文案）、主题切换、契约 selftest；截图留档 docs/design/screenshots-plt4/ |
| **必须用户（待人工）** | 用户 | 真实凭据类：真实注册/真实过盾/真实收码/真实 clash 切换——沿用 handoff-manual 口径逐家列清单 |

## 5. 验收标准总表

| # | 通过条件 | 层 |
|---|---------|---|
| C1 | 批 A 三家：离线门全绿 + 契约自测 PASS + §12 映射核对 | 自动 |
| C2 | 批 B 四家：离线门全绿（skywork offline 子集）+ 裁剪面/定源裁决落盘 + 双键族纪律核对 | 自动 |
| C3 | 批 C：loomy 轻迁移两项落地；devin schema 化+面板对齐；vibex 冻结声明 | 自动 |
| C4 | 面板层：11 家 tokens 注入 + 05 矩阵逐格有落点（真数据/降级/N/A） | 自动+浏览器 |
| C5 | 每家独立审查（worker），发现项登记 findings 文件并处置 | 审查 |
| C6 | pluginsdk 若有改动全仓 -race 全绿；§12/§12.1 沉淀同步 | 自动 |
| C7 | 浏览器验收批次完成，截图留档 | 浏览器 |
| C8 | 独立终验（对照 C1-C7 复跑+四段结论） | 验收 |
| C9 | 待人工清单逐家列明（真实凭据类） | 待人工 |

> **验收结论（2026-11-07）**：C1-C6/C9 全 ✅（四组独立审查逐仓复跑 build/vet/test + selftest 全绿：批 A 三家「通过」3 medium 当场修复、skywork「通过」5 遗留登记、批 B 两家「通过」3 微瑕、批 C「通过」2 观察项——全部登记 `plugin-template/docs/plt4-review-findings.md`）；**C7 ⬜ 浏览器统一验收批次待执行**（步骤已列 handoff-manual ⑧-2，主会话内置浏览器执行）；C8 以四组独立审查逐仓复跑等效达成。**总体（2026-11-08 更新）：C7 浏览器统一验收批次已完成**——隔离实例（核心 48280+provider-service 50180）拉起 9 家迁移插件，聚合目录 64 模型；8 个面板经内置浏览器逐家验收通过（devin need_config 待凭据），截图存档 docs/design/screenshots-plt4/；验收中发现并修复 loomy 面板鉴权 L1（分段鉴权+6 子用例）。发现项全部登记 plugin-template/docs/plt4-review-findings.md。**总体：有条件通过——离线层+浏览器层全绿，仅剩真实凭据类（handoff ⑧-3）待用户执行。**

## 6. 风险与降级

| 风险 | 降级 |
|------|------|
| flonno 双模块/lobsterhub Wails 定源争论不决 | 模板先支持「入口 exe + 本地 replace」现状形态，构建统一化推迟到 PLT-5 |
| skywork 76 测试改造量超预期 | 按 internal 包分四个子批落地，每子批独立可回退 |
| grok 外部注册机契约与模板 register 语义冲突 | 注册机托管形态原样保留，仅映射配置键（外壳层薄接入） |
| devin/loomy 异构两家拖慢整体 | 排最后，允许随批 C 单独延期不影响 C1/C2 |
| 浏览器验收发现面板回归 | 每家 T-n.3 自带截图比对，问题当批修复不跨批 |

## 7. 交接提示词（零上下文，给执行方）

> 你在执行 PLT-4 全量推广（9 家迁移+1 家冻结）。必读：本计划 + 勘察存档 `2026-11-07-plt4-survey.md` + `plugin-template/docs/schema.md` §12/§12.1 + phase3 计划附录 A（T0 结论）。做：批 A→B→C，每家按 §2 模板五步，每 Task 一条 `<gitmoji><type>(PLT4):` 提交。不做：碰已部署 exe 与运行实例；重写浏览器真实流程（browser/ 域）；合并 skywork 双键族；对 vibex 做迁移。工作方式：语义等价（旧测试是行为基准）；用户在途改动先快照；live 测试不 offline 跑；证据优先。交卷：各仓 commit 链+本计划 §5 自评+测试输出+残留风险。回复语言中文。
