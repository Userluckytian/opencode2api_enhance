# ai-framework-skill 通用性审计报告

> 归档说明（2026-11-07）：本文档原位于 `docs/plugin-template-analysis-2026-11-07/06-ai-framework-skill-audit.md`，因审计对象是另一仓库 `D:\AI_Projects\ai-framework-skill`、与「插件模板分析」系列（00-05）主题无关，修订时移出至本路径，避免被误读为该系列第 06 篇。

- **审计对象**：`D:\AI_Projects\ai-framework-skill`（通用 skill 仓库，安装「阶段化计划驱动 + 提交前审查」能力到任意项目）
- **审计日期**：2026-11-07
- **审计方式**：只读通读全部 48 个内容文件（不含 `.git`，约 3,408 行），并用 grep 交叉扫描硬编码线索（项目名 / 盘符 / 模型名 / localhost 端点 / 业务词 / 日期）
- **审计范围声明**：本次为纯静态通读 + grep，未实际执行 install 脚本验证安装产物；文中所有引用均有原文行号证据。

---

## 一、总评

### 1.1 冗余程度：低—中

- 仓库总量约 3,408 行（不含 `.git`），核心结构（两套能力分层、`{{PROJECT_NAME}}`/`{{CSS_PREFIX}}`/`{{VISION_MODEL}}` 参数化、openapi agents/commands）**本身是通用的、设计良好**。
- 非通用内容集中在约 15 处，绝大多数是**示例值/示例文件名级别**的残留，不影响框架机制本身。
- git 历史显示作者已做过一轮中性化（`f0d9cfc ♻️refactor(common): 中性化模板——移除碳卫星/Vue 前端专属内容`），本轮发现的残留主要是**其后从 opencode2api 系项目沉淀 phase 流（2026-08 前后）时带进来的例子**（Go 文件名、竞速业务词、本机模型端点）。

### 1.2 残留分类统计

| 类别 | 数量 | 代表 |
|------|------|------|
| 机器/环境专属默认值 | 1 个集群（8 个文件） | `oc-local/mimo-v2.5`、`http://127.0.0.1:18080/v1` |
| 业务项目文件名/特性名 | 3 处 | plan-layering 派工表、§3.1、§5 |
| 项目专属验证/红线词汇 | 3 处 | §6.1 Go 三平台、「厂商信息不进 core」、showGlobalError |
| 悬空引用/没改干净句子 | 5 处 | 「见历史映射」、superpowers `skills/`、AI-TESTING-GUIDE、海鸥.md 截断 |
| 格式/标题冗余 | 3 处 | 双 `---`、「原则四条」实列 7 条、install.ps1 死代码 |
| 双脚本行为分叉 | 2 处 | persona 仅 sh 有；`.mdc` 渲染面不一致 |

### 1.3 清理优先级排序

- **P0（影响每个安装产物的正确性）**
  1. VISION 模型/端点默认值集群——install 脚本会**静默**把 `{{VISION_MODEL}}` 渲染成 `oc-local/mimo-v2.5` 写进目标项目 `vision-analyst.md`，对任何其他安装者都是不存在的 provider，视觉子代理开箱即坏。
  2. plan-layering.md.template 的具体文件名（`chat.go`/`socks_perf.go`/`SettingsPage.tsx` 等）与业务词（「竞速预算」「动态副本」）——最扎眼的项目残留，随模板原样进入每个目标项目。
- **P1（观感与可信度受损/悬空引用）**
  3. phased-plan-driven §6.1 的 Go/三平台/macOS 真机验证示例；§4.2「厂商信息不进 core」。
  4. issue-log 模板引用 `docs/AI-TESTING-GUIDE.md`（目标项目必不存在的文档）与「海鸥」persona。
  5. CLAUDE.md.template 示例提交里的 `showGlobalError`；phase-planner 的 `汇总.md`、`docs/01*`。
  6. docs/project-paths.md 整节「与 Grok skill 路径的区别」；SKILL.md 的 `D:\AI_Projects` 盘符路径。
  7. 悬空引用三处：SKILL.md:110「见历史映射」、superpowers 切片的 `skills/` 目录（安装后不存在）、海鸥.md 文件截断。
- **P2（冗余与一致性）**
  8. 「原则四条」标题下实列 7 条；两处连续双 `---`；install.ps1 死代码。
  9. install.sh 两处功能性 bug（项目名尾随空格、persona 覆盖刚渲染的 CLAUDE.md）——非通用性问题，顺带记录。
  10. install.ps1 / install.sh 参数面分叉（persona 仅 sh 支持）；`docs/superpowers/plans/` 兼容说明在 11 个文件重复。

### 1.4 与目标项目（opencode2api_enhance）规范文档的重复关系

- `templates/common/` 正是本仓库根目录 `AGENTS.md`、`CODE_REVIEW.md`、`coding-standards.md`、`docs/ai-framework/*`、`docs/issue-log/*` 的**参数化上游**：对照可见本仓库 `AGENTS.md` 与 `templates/common/AGENTS.md.template` 逐段同源（差异仅为 `{{PROJECT_NAME}}` 实例化为本项目名）。两仓库间的正文重复属**刻意的「模板↔实例」复用关系，不需要消重**；skill 侧清理后，本仓库作为实例可选择性回填中性化后的措辞。
- 本机专属值（如 `@vision-analyst` 固定 `oc-local/mimo-v2.5`、端点 18080）**在实例项目里是合理的**——问题只在于 skill 侧把它当成了**默认值**。清理方向是「skill 侧中性、实例侧具体」，单向同步，不冲突。
- Git 提交规范（gitmoji 表）在 AGENTS / coding-standards / CLAUDE 三个模板中各出现一次，属**有意冗余**（各渲染产物需自包含），保留；`docs/superpowers/plans/` 兼容说明重复 11 处属**过度冗余**，建议收敛到 2–3 处。

---

## 二、Findings 明细（按重要性排序，与提交的 findings 一致）

### F1【P0】VISION 模型/端点默认值是本机专属配置，随安装静默写入目标项目
- **涉及文件**（8 处）：`SKILL.md:58,106`、`README.md:44`、`scripts/install.ps1:34`、`scripts/install.sh:9`、`templates/opencode/agents/vision-analyst.md:57-58`、`templates/opencode/scripts/vision-bridge.py:13,15,54,56`
- **what**：默认视觉模型 `oc-local/mimo-v2.5` 与默认端点 `http://127.0.0.1:18080/v1` 是作者本机 OpenCode 代理 + 小米 MiMo 模型的配置。任何其他用户安装后：脚本把 `{{VISION_MODEL}}` 渲染成 `oc-local/mimo-v2.5` 写入 `.opencode/agents/vision-analyst.md` 的 `model:` 行，该 provider 在其环境中不存在；桥脚本默认指向 18080 端口也仅在本机成立。
- **action**：泛化——脚本与文档默认值改为中性占位（如 `vision/local-model`）或留空并在安装交互中改为必填/可选说明；`127.0.0.1:18080` 保留为「本机示例」措辞。
- **evidence**：SKILL.md:58「`VISION_MODEL`（可选）……默认 `oc-local/mimo-v2.5`；若目标环境 provider 名不同（如 `oc-local-18080/mimo-v2.5`）」；install.sh:9 `VISION_MODEL="oc-local/mimo-v2.5"`；vision-bridge.py:13「`VISION_API_BASE  端点，默认 http://127.0.0.1:18080/v1`」。

### F2【P0】plan-layering 派工表与示例直接使用某具体项目的文件名与业务特性名
- **文件**：`templates/common/docs/plan-layering.md.template:31-32,59-62,75`
- **what**：派工表示例列出 `chat.go`、`models_source.go`、`socks_perf.go`、`types_config.go`、`opencodecfg.go`、`SettingsPage.tsx`、`calllog.go`、`LogsPage.tsx`（Go + Vue 混合项目的真实文件）；§3.1 用「竞速预算」「自适应竞速」「代理出口」、§5 用「动态副本需要预算机制兜底」等具体业务特性名举例。
- **action**：泛化——文件名改占位（如 `service-a/x.go`、`web/B.tsx`），特性名改「功能 A/功能 B」类中性示例。
- **evidence**：line 59「`| A | Stage-1 → Stage-2（串行） | chat.go/models_source.go/socks_perf.go | 主链 | — |`」；line 31「如「竞速预算」是「自适应竞速」的依赖」；line 75「如「动态副本需要预算机制兜底」」。

### F3【P1】元规范 §6.1 验证分工表混入特定项目的验证项（Go/三平台/macOS/挂起节点）
- **文件**：`templates/common/docs/phased-plan-driven.md.template:204-206`
- **what**：自动化层示例「`go test` 全绿、`-race` 关键包」、端到端层示例「挂起节点、并发压测、真机 GUI、CI 三平台产物、docker run、macOS 真机」——「挂起节点」「三平台」「macOS 真机」是某 Go 桌面/服务项目的具体验证清单，其他技术栈项目照抄会误导。
- **action**：泛化——改为通用示例（单测/集成/构建；真机/部署环境验证）并注明「按项目替换」。
- **evidence**：line 205「单测、集成测试、构建、`go test` 全绿、`-race` 关键包、代码走查」；line 206「挂起节点、并发压测、真机 GUI、CI 三平台产物、docker run、macOS 真机」。

### F4【P1】issue-log 模板两处源项目残留：引用不存在的文档 + 作者个人 persona/环境
- **文件**：`templates/common/docs/issue-log-README.md.template:4,113`
- **what**：①「适用」行列举「海鸥」「Grok」——海鸥是作者个人 persona、Grok 是作者本机环境，目标项目通常两者皆无；②「本地研发过程笔记」注释里引用 `docs/AI-TESTING-GUIDE.md`——该文档不属于本框架、也不会存在于目标项目。
- **action**：泛化——①改为「OpenCode / Codex / Claude 等本项目在用的 AI 与协作者」；②删除 AI-TESTING-GUIDE 括号引用。
- **evidence**：line 4「`> **适用:** 本项目所有 AI(OpenCode / Grok / Codex / Claude / 海鸥)与协作者`」；line 113「(和 `docs/AI-TESTING-GUIDE.md` 等本地文档同策略)」。

### F5【P1】CLAUDE.md 模板示例提交带特定项目业务开关 showGlobalError
- **文件**：`templates/common/CLAUDE.md.template:14,23`
- **what**：示例提交「♻️refactor(api): 请求拦截器支持 showGlobalError 开关」中的 `showGlobalError` 是某前端项目的具体业务开关；line 14 scope 示例 `auth / api / login / layout / theme / i18n` 也偏前端 admin 项目口味（可接受但可更中性）。
- **action**：泛化——示例提交换通用写法（如「♻️refactor(api): 请求拦截器支持错误提示开关」）；scope 示例可保留。
- **evidence**：line 23「`♻️refactor(api): 请求拦截器支持 showGlobalError 开关`」。

### F6【P1】phase-planner 参考文档路径含源项目命名习惯
- **文件**：`templates/opencode/agents/phase-planner.md:31`
- **what**：进度/总控文档示例「`docs/*STATUS*`、`汇总.md`、`docs/01*`」——`汇总.md`、`docs/01*` 是源项目的文档命名约定，其他项目没有这类文件，示例指向性差。
- **action**：泛化——改为「项目进度/汇总类文档（按项目实际命名）」。
- **evidence**：line 31「`| P0 | 进度/总控：README、docs/*STATUS*、汇总.md、docs/01* 等（若有） |`」。

### F7【P1】审查面示例「厂商信息不进 core」是特定项目的架构红线
- **文件**：`templates/common/docs/phased-plan-driven.md.template:103`
- **what**：「架构红线（厂商信息不进 core 等）」——「厂商信息不进 core」是某具体项目的分层红线，放在通用元规范里对其他项目无意义。
- **action**：泛化——改为「架构红线（按项目自定义，如分层依赖方向）」。
- **evidence**：line 103「`2. **审查面**（按项目补）：…… 架构红线（厂商信息不进 core 等）/ 安全（密钥、注入、越权）……`」。

### F8【P1】docs/project-paths.md 整节讲「与 Grok skill 路径的区别」，属作者本机历史
- **文件**：`docs/project-paths.md:27-34`
- **what**：整节对照「安装器 skill 本体曾在 `~/.grok/skills/ai-framework/`」，是 skill 早期作为 Grok skill 开发的历史背景，与「目标项目落点」主题无关，安装到任何项目都不需要这段。
- **action**：删除整节；若想保留提醒，压缩为一句「安装器本体只存在于 skill 根目录，与目标项目路径无关」。
- **evidence**：line 27「`## 与 Grok skill 路径的区别`」；line 31「`| ~/.grok/skills/ai-framework/ 或 ~/.agents/skills/ai-framework/ | 仅「安装器 skill」本体 |`」；line 34「不要把 harness 配置写到 `~/.grok`」。

### F9【P1】SKILL.md 写死作者本机盘符路径
- **文件**：`SKILL.md:30`
- **what**：「本地开发路径示例：`D:\AI_Projects\ai-framework-skill`」——盘符绝对路径是作者本机布局，对其他用户是噪音；SKILL.md 会被作为 skill 说明加载，无需本机示例。
- **action**：删除该行，或改为 `<本地检出路径>`。
- **evidence**：line 30「`- 本地开发路径示例：D:\AI_Projects\ai-framework-skill`」。

### F10【P1】SKILL.md 手动映射一节悬空引用「见历史映射」
- **文件**：`SKILL.md:110`
- **what**：Codex/Claude/Others 的手动映射只有一句「同原说明：见历史映射」——仓库内不存在任何「历史映射」文档，读者无从查起；这是一句没改干净的指引。
- **action**：补上实际映射（对照 `docs/project-paths.md` 的落点表）或删除该句改为「见 `docs/project-paths.md`」。
- **evidence**：line 110「`同原说明：见历史映射；Claude 渲染 CLAUDE.md.template（已含阶段协作短指引）。`」。

### F11【P1】superpowers 切片元数据与 hook 引用仓库内从未安装的 `skills/` 目录
- **文件**：`templates/claude/hooks/session-start:11`、`templates/others/gemini/GEMINI.md:1-2`、`templates/others/kimi/plugin.json:21-24`、`templates/others/cursor/plugin.json:21`
- **what**：session-start hook 读取 `${PLUGIN_ROOT}/skills/using-superpowers/SKILL.md`，GEMINI.md `@./skills/...`，kimi/cursor 的 plugin.json 声明 `"skills": "./skills/"`、`sessionStart: using-superpowers`——但 `templates/` 下没有任何 `skills/` 目录，install 脚本也从不安装它；项目级安装后 hook 必然走到「Error reading using-superpowers skill」分支。切片是从 superpowers 上游摘出来的不完整拷贝。
- **action**：泛化/说明——要么把 `using-superpowers` 切片补全进模板，要么在 claude/others 的 INSTALL.md 与 hook 注释中明确「这些元数据/hook 仅为参考形态，完整能力需按官方方式安装 superpowers 插件」（后者与现有 INSTALL.md 的表态一致，改动最小）。
- **evidence**：session-start:11「`using_superpowers_content=$(cat "${PLUGIN_ROOT}/skills/using-superpowers/SKILL.md" 2>&1 || echo "Error reading using-superpowers skill")`」；GEMINI.md:1「`@./skills/using-superpowers/SKILL.md`」。

### F12【P1】海鸥.md persona 文件截断，且引用仓库不含的 `$seagull-*` 技能
- **文件**：`templates/common/prompts/海鸥.md:20-31,258-259`
- **what**：①文件在 `<!-- module: 48-protocol-reverse.md -->` + 标题「Protocol Reverse Routing」处戛然而止（共 259 行，模块 48 无正文）——从外部仓库复制时没拷全；②其 Skill Routing 引用 `$seagull-reverse`/`$seagull-pentest`/`$seagull-memory`/`$seagull-lab` 等技能，均不在本仓库、也不会被安装。README 已注明来源与「默认不安装」，作为可选 persona 可保留。
- **action**：保留（opt-in persona，README 有出处声明），但补全或删除截断的模块 48 头部，并加一行说明「需配套 seagull skills，本仓库不含」。
- **evidence**：文件末两行为「`<!-- module: 48-protocol-reverse.md -->` / `# Protocol Reverse Routing`」后即结束（`tail` 验证）；line 24「`- $seagull-reverse: binaries, pseudocode, disassembly, ...`」。

### F13【P2】元规范 §10 标题「原则四条」下实列 7 条
- **文件**：`templates/common/docs/phased-plan-driven.md.template:240-248`
- **what**：标题仍是「原则四条」，列表却是 1–7 共七条（5–7 为后来追加的「下放/分工/实事求是」），标题没跟着改。AGENTS.md.template 里的同名「原则四条」仍是四条，两处一旦被对照阅读会显得不一致。
- **action**：改标题为「原则（七条）」或「阶段工作原则」。
- **evidence**：line 240「`## 10. 原则四条`」，line 246-248 为第 5、6、7 条（「验证不通过必下放」「验证分工两层」「实事求是」）。

### F14【P2】install.ps1 SkillRoot 默认值死代码
- **文件**：`scripts/install.ps1:42-46`
- **what**：第一行赋值 `$SkillRoot = Split-Path (Split-Path $PSScriptRoot -Parent) -Parent` 立即被下一行覆盖，注释「scripts/ is directly under repo root」与首行语义矛盾——是改目录结构时没删干净的残留。
- **action**：删除第一行赋值。
- **evidence**：line 43-45「`$SkillRoot = Split-Path (Split-Path $PSScriptRoot -Parent) -Parent` / `# scripts/ is directly under repo root` / `$SkillRoot = Split-Path $PSScriptRoot -Parent`」。

### F15【P2·附带 bug】install.sh 默认项目名带尾随空格
- **文件**：`scripts/install.sh:65`
- **what**：`PROJECT_NAME="${PROJECT_NAME:-$(basename "$TARGET_ROOT") }"` 引号内括号后有一个空格，未传 `--project-name` 且无 package.json 时，`{{PROJECT_NAME}}` 会被渲染成「目录名 」（尾随空格）写进各模板标题。属功能 bug 而非通用性问题，顺带登记。
- **action**：删除引号内空格（同类问题请在修复时跑一次安装冒烟验证）。
- **evidence**：`cat -A` 验证 line 65 为「`PROJECT_NAME="${PROJECT_NAME:-$(basename "$TARGET_ROOT") }"$`」——`)` 与 `}` 之间有空格。

### F16【P2·附带 bug】install.sh persona 会整个覆盖刚渲染的 CLAUDE.md
- **文件**：`scripts/install.sh:241-252`
- **what**：`install_claude` 先用模板渲染 CLAUDE.md，随后若选择 persona，`copy_file "$psrc" "$TARGET_ROOT/CLAUDE.md"` 会把刚渲染的 CLAUDE.md 备份成 `.bak` 并用 persona 正文**整文件替换**——框架指引（阶段协作、Git 规范、问题日志条款）全部丢失，与 usage 注释「write into the target CLAUDE.md」的意图不符。
- **action**：改为把 persona 追加到渲染后的 CLAUDE.md 末尾（或写入独立文件并在 CLAUDE.md 中引用）。
- **evidence**：line 241「`render_file "$TEMPLATES/common/CLAUDE.md.template" "$TARGET_ROOT/CLAUDE.md"`」→ line 247「`copy_file "$psrc" "$TARGET_ROOT/CLAUDE.md"`」+ line 248「`log "Installed persona: $PERSONA -> $TARGET_ROOT/CLAUDE.md"`」。

### F17【P2】install.ps1 与 install.sh 参数面/行为分叉
- **文件**：`scripts/install.sh:22,144-276` vs `scripts/install.ps1`（无 persona）；`install.sh:114` vs `install.ps1:141`；`SKILL.md:68,72`
- **what**：①`--persona`/`--no-persona` 交互选择只有 sh 版实现，ps1 完全没有，SKILL.md 的两段调用示例也都不提 persona；②渲染扩展名 sh 为 `.md|template|jsonc`，ps1 多渲染 `.mdc`；③ps1 渲染判断里还排除 `run-hook.cmd`/`session-start`，sh 无对应排除（靠扩展名规则天然排除）。同一 skill 两条安装路径行为不一致，属双份维护未同步。
- **action**：二选一对齐——给 ps1 补 persona 参数（或在 SKILL.md 注明 persona 仅 sh 支持），统一渲染扩展名集合。
- **evidence**：install.sh:22「`[--persona FILENAME] [--no-persona]`」；install.sh:114「`if [[ "$render" == "1" && "$f" =~ \.(md|template|jsonc)$ ]]`」；install.ps1:141「`$render = $RenderMarkdown -and ($_.Extension -match '\.(md|template|jsonc|mdc)$' -or $_.Name -like '*.template')`」。

### F18【P2】两处连续双水平线 `---`（格式冗余）
- **文件**：`templates/common/docs/phased-plan-driven.md.template:29-31`、`templates/common/docs/phase-plan.template.md:104-106`
- **what**：连续两个 `---` 渲染出两条紧邻的水平线，是编辑/合并时残留的重复分隔符。
- **action**：删除多余的一个。
- **evidence**：phased-plan-driven.md.template:29-31 依次为「`---` / （空行） / `---`」；phase-plan.template.md:104-106 同构（`cat -A` 验证）。

### F19【P2】`docs/superpowers/plans/` 兼容说明在 11 个文件重复同义句
- **文件**：`templates/common/AGENTS.md.template:14`、`templates/common/CLAUDE.md.template:6`、`templates/common/docs/phased-plan-driven.md.template:5,161,258`、`templates/common/docs/plans-README.md.template:13`、`docs/project-paths.md:38-41`、`templates/opencode/agents/phase-planner.md:30`、`phase-acceptor.md:28`、`commands/plan-phase.md:11`、`handoff.md:8`、`accept-phase.md:9`、`README.md:56`
- **what**：「若已有 docs/superpowers/plans/ 可继续用」同一兼容说明以近逐字形式出现 11+ 次。各渲染文件自包含可解释 AGENTS/CLAUDE/元规范三处，但 agents/commands 里也各自重复，密度过高。
- **action**：收敛——保留 AGENTS.md、phased-plan-driven.md 头部、plans-README 三处，agents/commands 内的重复删去（定位逻辑仍可查两个目录，只是不必每次复述兼容政策）。
- **evidence**：grep 统计 `superpowers/plans` 命中上述 11 个文件；例：phase-acceptor.md:28「`（在 docs/ai-framework/plans/ 与 docs/superpowers/plans/ 中查找）`」。

### F20【保留】jiejian/CDPS 出处说明：建议原样保留
- **文件**：`THIRD_PARTY_NOTICES.md:8-9`、`docs/design.md:58`
- **what**：提到 OpenCode agents/commands「来自 CDPS / jiejian frontend framework lineage」「提交审查流来自既有 jiejian/OpenCode 实践」——虽是作者内部项目名，但位于**出处声明/设计动机**位置，是诚实溯源而非模板正文，不会安装进目标项目。
- **action**：保留（可顺手把措辞改成「作者既有项目实践」，非必须）。
- **evidence**：THIRD_PARTY_NOTICES.md:9「`additions from the CDPS / jiejian frontend framework lineage, generalized for reuse.`」；design.md:58「`提交审查流来自既有 jiejian/OpenCode 实践。`」。

---

## 三、附：已核对但判为通用的项（免复查）

- `templates/common/CODE_REVIEW.md`（41 行）与 `coding-standards.md.template`（103 行）：纯通用工程约定，含「项目专属约定」留空填写区，无需处理。
- `templates/opencode/agents/` 其余 5 个 agent（test-engineer / code-stylespector / dependencies-checker / project-auditor）与全部 9 个 command：无项目专属词汇（`npm test`/`pytest` 等为通用示例）。
- `templates/claude/hooks/run-hook.cmd`、`session-start` 的跨平台 polyglot 机制：来自 superpowers 上游的通用工程实现。
- gitmoji 提交规范表在 AGENTS / coding-standards / CLAUDE 三模板重复：有意冗余（渲染产物自包含），保留。
- `templates/common/docs/phase-plan.template.md` 主体、`agent.template.md`、`plans-README.md.template`、`issue-log-OPEN.md.template`：通用。
- `LICENSE`（Apache-2.0，自有文件）与 superpowers 切片 MIT 归属：授权关系在 README/THIRD_PARTY_NOTICES 已声明，清晰。
- 海鸥.md 的存在本身：README「Persona / Role templates」节已声明默认不安装、要求注明来源，定位明确（但见 F12 的截断问题）。

## 四、审计方法与可复现命令

```bash
# 文件清单与规模
find /d/AI_Projects/ai-framework-skill -type f -not -path "*/.git/*" -exec wc -l {} +

# 硬编码线索扫描（本次全部跑过，命中即上表 evidence）
grep -rn -E "opencode2api|jiejian|CDPS|buddyhub|loomy|devin" /d/AI_Projects/ai-framework-skill --exclude-dir=.git
grep -rn -E "D:\\\\|C:\\\\|~/.grok|Grok|127\.0\.0\.1|oc-local|mimo" /d/AI_Projects/ai-framework-skill --exclude-dir=.git -I
grep -rn -E "竞速|代理出口|动态副本|showGlobalError|socks_perf|opencodecfg|SettingsPage|LogsPage|calllog|models_source|types_config" /d/AI_Projects/ai-framework-skill --exclude-dir=.git -I
grep -rn -E "AI-TESTING-GUIDE|厂商|汇总\.md|docs/01|海鸥|2026-08" /d/AI_Projects/ai-framework-skill --exclude-dir=.git -I

# 海鸥.md 截断验证
tail -c 400 "/d/AI_Projects/ai-framework-skill/templates/common/prompts/海鸥.md" | cat -A
```

未执行项：未实际运行 `install.ps1`/`install.sh` 做安装冒烟（审计要求只读不改，且 F15/F16 的影响是从代码路径推得，建议修复后补跑一次安装冒烟验证）。
