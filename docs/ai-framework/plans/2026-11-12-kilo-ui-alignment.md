# 阶段 P：Kilo 页 UI 对齐优化（设计 skill 分析）

> **状态：** 计划已就绪，待批准后实施
> **For agentic workers:** 按 Task 顺序执行；每 Task 测完再进下一 Task。验收认证据，禁止「应该能过」。
> **元规范:** `docs/ai-framework/phased-plan-driven.md`；仓库约定 `AGENTS.md`
> **基线分支：** `main`（tip `e3554cb`，与 origin 同步，本地 2 分支）
> **设计依据：** `design` + `frontend-design` skill（已加载）；只读 UI 对比子代理 + 内置浏览器两张截图（生产 Kilo 改前 / 设置页基准）。

**Goal:** 对比其他菜单（设置 / OpenCode / 诊断 / 统计）的设计语言，把 Kilo 页的头部、卡片层级、状态区、端点区、表格列宽、底部引导对齐到既有面板体系；不改功能、不引入新 UI 依赖。

**用户附带事实澄清（重要）:** 用户「更新到了 2.0.6，但试跑按钮还在」—— 核实：阶段 O「去掉试跑」发生在 v2.0.6 发布**之后**（O 的 7 个提交只推 main，未打 tag/发 Release）。生产 `bin/dist` 的 js 是发版时旧构建（`试跑=True`、`通道分析=False`），仓库阶段 O 新构建才是 `试跑=False`、`通道分析=True`。**不是 bug，是版本差**；本阶段优化后是否发 v2.0.7 由用户另行决定，不自动发版。

**Architecture:** 只改 `src/pages/KiloPage.tsx`（必要时 `VirtualModelsCard.tsx` 的卡片 padding 对齐），纯前端，不动后端/API/配置。沿用现有 Tailwind class 体系（`rounded-2xl border border-zinc-200 bg-white`、`text-zinc-*`、`text-[12.5px]`）。

**Tech Stack:** React 19 + TS + Tailwind + lucide-react（现有）。

**实施档位：** 全能　**子代理：** 启用（独立代码审查 + 视觉验收）

---

## 前置阅读

| 优先级 | 文件 |
|---|---|
| P0 | 本文件、`docs/ai-framework/phased-plan-driven.md` |
| P0 | `src/pages/KiloPage.tsx`（全文）、`src/pages/SettingsPage.tsx`（标题区/卡片基准）、`src/pages/DiagPage.tsx`（卡片/状态条基准）、`src/App.tsx`（导航/布局） |
| P1 | `AGENTS.md`、`coding-standards.md`、`docs/issue-log/OPEN.md`、`src/index.css` |

---

## 独立 UI 对比子代理结论（11-12 回传，修正了初版臆测）

子代理逐行读了 9 个文件 + 交叉读 LogsPage/CustomModelsPage/kilo_gateway.go，给出 8 维对比与 12 条优化清单。**关键修正（写入实施约束）：**

1. `p-4` / `text-[12.5px]` **不是 Kilo 独有**（StatsPage:57、LogsPage:533、ChannelStatsCard 6 处、SettingsPage 3 处也用）；真正一致的差异是 **主卡 `p-5 shadow-sm`**（SettingsPage:179 / ChannelStatsCard:89）与 **主操作按钮 `text-[13px] px-3 bg-white`**（OpenCodePage:579、DiagPage:232、StatsPage:370）。
2. **状态徽章配色不要改**：`KiloPage.tsx:36-40` 的三分支已与 `OpenCodePage.tsx:97-102` 同令牌；**不要补 `starting` 分支** —— 后端 `kilo_gateway.go:84` 只有 `stopped|ready|failed` 三态，补了会造永不出现的徽章。只改形状（圆点 + `px-2.5 py-1 rounded-full text-xs`）与位置（移入页头）。
3. **7 个页面中唯一**缺 `p-6` + `<h1>` 的是 Kilo（App.tsx:290 的 main 不补内边距）。
4. 警示条是唯一「页顶 rose 通栏」且 `rounded-2xl` 与白卡同级 → 过重 + 错位；改 `rounded-xl`、降字号、移到 h1 下，**保持常驻不可关闭**。
5. 模型表四列均无 `w-*`、首列长 ID 不 truncate 是「列宽挤右侧」的代码成因（基准 OpenCodePage:851-859 钉 `w-16/w-20/w-24`）；`max-h-[420px]` + sticky 有据，**保留**。
6. `index.css:9` 已有 `--border`、`index.css:19` 有 `--accent: #0f766e`；`index.css:59-69` 不给 select 底色 → select 需补 `bg-white`；`accent-teal-600` 用于 checkbox。

### 按优先级排序的 12 条优化清单（采纳，作为 Task 1~4 的实施明细）

| # | 位置 | 改成什么 | 为什么 |
|---|---|---|---|
| 1 P0 | `KiloPage.tsx:149` | `space-y-4` → `p-6 space-y-4` | 唯一无页内边距、贴边 |
| 2 P0 | `:151` 前新增页头 | `Cloud size=18 text-teal-700` + `<h1 text-[16px]>` + 状态徽章（圆点）+ 右侧按钮组；`:157` 徽章、`:165-198` 按钮搬入 | 唯一无 h1，警示占标题位 |
| 3 P0 | `:154,250`、`VirtualModelsCard:142` | `bg-white rounded-2xl border border-zinc-200 shadow-sm p-5` | 缺 shadow/p-5 |
| 4 P1 | `:160,252`、`VirtualModelsCard:145` | `<div text-[15px] text-zinc-800>` → `<h3 text-[15px] font-semibold text-zinc-900>` + `size=16 text-teal-600` 图标 + `mb-3` + 说明文字 | 全站每卡有语义标题，Kilo 0 个 heading |
| 5 P1 | `:157` | `px-2.5 py-0.5 text-[12px]` → `flex items-center gap-1.5 px-2.5 py-1 rounded-full text-xs` + `w-1.5 h-1.5` 圆点；移入页头。**配色三分支不动、不补 starting** | 形状/位置对齐 OpenCodePage:556 |
| 6 P1 | `:166-197,215-234`、`VirtualModelsCard` 按钮 | `text-[13px] px-3 flex gap-1.5 bg-white` + `size=14`；停用 `red-50/red-600`、启用 `teal-50/teal-700` | 对齐主操作按钮与 --accent；按钮组归位页头 |
| 7 P2 | `:208-246` | 端点两格补 `p-4` + `mt-1 text-[11px]` 副行；5 个元数据改 `grid grid-cols-2 sm:grid-cols-4 gap-3` 的 Stat 卡（`rounded-xl border border-zinc-100 bg-zinc-50/60 p-3`） | 状态卡一卡 4 层太满、两格太空 |
| 8 P2 | `:43-49` | `rounded-2xl`→`rounded-xl`、`bg-rose-50/80`→`bg-rose-50`、`text-[13.5px] font-medium`→`text-[12.5px]` 主句 + `text-[11px] text-rose-700/70` 副句；移至 h1 下，**常驻不可关闭** | 唯一页顶 rose 通栏，权重过重 |
| 9 P2 | `:266-318` | 容器 `border-zinc-100`；table `text-[12px]`；th/td `px-3 py-1.5`；去 `divide-y` 改每行 `border-t`；`th` 补 `w-16/w-20/w-20`；ID 列 `truncate max-w-[420px]` + `title`；阴影走 `var(--border)`；**保留** `max-h-[420px]` + sticky | 列宽缺失是挤压成因 |
| 10 P2 | `:283-288` 空态 | 移出 tbody → `rounded-lg bg-zinc-50 border px-3 py-2 text-[12px]` 或 Inbox 居中 | 表内一行在 420px 滚动容器里几乎看不见 |
| 11 P3 | `:254-262`、`VirtualModelsCard` checkbox/select | checkbox `accent-teal-600` + label 胶囊、移到页级工具条；select 补 `bg-white text-[13px] outline-none` | 裸 checkbox / select 露默认底色 |
| 12 P3 | `:328-331`、`VirtualModelsCard:231` | 日志引导上移到状态卡后、模型卡前（首屏可见）；字面量 ⚠ 换 `<AlertTriangle size=13>` | 底部引导太弱且几乎看不到 |

**「试跑还在」澄清（子代理未涉及，主会话核实）**：v2.0.6 发版早于阶段 O，属版本差非 bug；本阶段不改此事实，是否发 v2.0.7 由用户决定。

---

## 设计分析（8 维对比结论，主会话初版，已被上面子代理报告修正）

| 维度 | 基准（其他页） | Kilo 页现状 | 差距 |
|---|---|---|---|
| 页面头部 | 设置/诊断有 icon + `<h1>` 标题区 | 无标题，玫瑰横幅开场 | 缺身份锚点、警示反客为主 |
| 区块标题 | `h2 text-lg` / `h3 text-[15px]` | 「可用模型」`text-[15px]` | 层级偏小 |
| 卡片 | `p-5 shadow-sm` | `p-4` 无 shadow | 内边距/阴影不一 |
| 状态区 | 诊断卡标题独立一行 + 工具条 | 状态徽章/标题/说明/按钮全挤一行 | 窄屏拥挤 |
| 端点区 | 设置端口表紧凑行 | 两个半宽大卡放一行 URL/Key | 留白过多 |
| 表格列宽 | sticky + 限高（Kilo 已有） | 长模型 ID 把后三列挤到最右 | 列宽失控 |
| 开关位置 | Stats/Logs 筛选工具条 | 「只显示免费」在模型卡右上 | 可接受但可对齐 |
| 底部引导 | 诊断结论文案条 | 「日志」引导在长表格之后 | 几乎看不到 |

**设计取舍（刻意不做）：** 不换配色/字体（会破坏与其余 6 页一致性）、不做大动效、不把警示横幅改成可关闭（需求本体常驻）、不引入 shadcn/ui 新组件。`frontend-design` skill 强调「distinctive」，但本 brief 的明确目标是「对比其他菜单做对齐」，**一致性优先于个性**，故采用保守精炼方向。

---

## Global Constraints

1. **纯前端、不改功能**：不增删 API 调用、不改状态逻辑、不动 `VirtualModelsCard` 的业务逻辑（仅可选 padding 对齐）。
2. **警示不可绕过**：训练警示横幅常驻、无关闭入口、文案保持「数据可能被上游训练」一句（用户 2-1 意见）；只调整视觉权重（降为细条 + 位置），不改变「存在 + 不可关闭」。
3. **不改变信息**：端点/密钥/端口/目录数/更新时间全部保留，只改排布。
4. **不引入新依赖 / 不改全局样式**：只用现有 Tailwind class + 现有 lucide 图标；不动 `src/index.css`。
5. **响应式**：窄屏下头部按钮组不重叠、表格横向不溢出（沿用 `overflow-x-hidden` + truncate）。
6. **不替换生产 exe/dist**：验证走 dev（`npm run tauri:dev` 44100 段）+ 内置浏览器；前端改动需同步 `dist/` → `src-tauri/target/debug/bin/dist/`。用户自行更新生产。
7. **Git**：小步 commit、规范 `<gitmoji><type>(<scope>): 中文描述`；**默认不 push**（是否发版由用户定）。

---

## 阶段开头：上阶段遗留（如实带入）

| 上阶段遗留项 | 状态 |
|---|---|
| 阶段 O 代码上生产需用户自行部署（前后端同批更新） | ⬜ 用户自行更新 |
| `ComputeChannelStats` 大日志限流 / `ChannelStatsCard` 代次守卫 | ⬜ P2 遗留 |
| V-6b / B5 / 4 条时间炸弹 / 累计统计 vendor 维度 / SSE 流式 / `race` 代价 | ⬜ 各阶段遗留 |
| v2.0.5 旧 Release / 已发布 Release 正文披露 | ⬜ 外发修改，用户未要求 |

---

## File Structure

| 文件 | 动作 | 职责 |
|---|---|---|
| `src/pages/KiloPage.tsx` | 修改 | 标题区、状态卡重构、端点区压缩、卡片统一、表格列宽、引导上移 |
| `src/pages/VirtualModelsCard.tsx` | 可选修改 | 卡片 `p-4 → p-5` / 标题层级对齐（若审查指出不一致） |
| `docs/issue-log/2026-11-12.md`、`OPEN.md` | 修改 | 阶段 P 记录 + 「试跑还在」澄清 |
| `docs/ai-framework/plans/2026-11-12-kilo-ui-alignment.md` | 新建 | 本计划 |
| 截图 | 产出 | `docs/design/screenshots-20261111/kilo-ui-*.png`（改前/改后，本地保留） |

---

## Task 1：加页面标题区 + 警示降权

**Files:** `KiloPage.tsx`

**行为:**
- 顶部新增与其他页同款标题区：`Cloud` 图标（沿用 App.tsx 的 Kilo 导航图标）+ `<h1 class="text-[16px] font-semibold">Kilo</h1>` + 一句说明「独立网关 · 直通 api.kilo.ai，不经过 OpenCode / 自定义源」。
- 警示横幅移到标题区**下方**，保持常驻不可关闭、文案一句；视觉降权：`py-2` 细条、`bg-rose-50/60`、图标 `h-3.5 w-3.5`、文字 `text-[12.5px]`（仍是唯一玫瑰元素，不会被忽略）。
- **Commit:** `🎨 style(kilo): 页面标题区与警示层级对齐`

---

## Task 2：状态卡重构（标题/工具条/元信息）

**Files:** `KiloPage.tsx`

**行为:**
- 卡片统一 `p-5 shadow-sm`。
- 第一行：图标 + `Kilo 独立网关`（`text-lg font-semibold`，对齐设置页 `h2`）+ 状态徽章；说明文字独立第二行（`text-[12px] text-zinc-500`）。
- 按钮组（刷新目录 / 启用停用）独立成右侧工具条，`flex-wrap gap-2`，窄屏自动换行不与标题重叠。
- 「端点与密钥」两个半宽大卡 → 紧凑条目：一行 `地址 + 复制`、`密钥 + 复制`（保留 `break-all font-mono`），或 2 列小卡但 `px-3 py-2` 减小留白。
- 元信息（端口 / 上游目录数 / 当前展示 / 出站代理 / 目录更新）做成一行 `flex flex-wrap gap-x-5 text-[11.5px] text-zinc-400`，带轻分隔。
- `last_error` 条保留但降为 `mt-2` 细条。
- **Commit:** `🎨 style(kilo): 状态卡信息层级与工具条重构`

---

## Task 3：模型卡 + 虚拟模型卡对齐

**Files:** `KiloPage.tsx`、（可选）`VirtualModelsCard.tsx`

**行为:**
- 模型卡 `p-5 shadow-sm`；标题「可用模型」改 `text-lg font-semibold`（加 `Cloud`/`List` 图标与其他页一致）；「只显示免费模型」开关保留右上但 `text-[12px]` + 统一 checkbox 样式。
- 表格列宽：模型 ID 列 `w-1/2 sm:w-[55%]` + `truncate`，档位/工具调用/上下文三列 `w-[15%]` 左右固定宽度 + `whitespace-nowrap`，避免长 ID 挤压；sticky 表头与 420px 限高保留。
- 底部说明 `text-[11.5px] text-zinc-400` 保留。
- `VirtualModelsCard` 卡片 `p-4 → p-5 shadow-sm`、标题层级对齐（若与其余卡不一致）。
- **Commit:** `🎨 style(kilo): 模型卡与虚拟模型卡样式对齐`

---

## Task 4：底部引导上移

**Files:** `KiloPage.tsx`

**行为:** 「调用记录见【日志】页」引导从最底部（长表格 + 虚拟模型之后）移到**状态卡之后、模型卡之前**，做成细条 `bg-zinc-50/60` + 「查看日志 →」文字（纯提示，不加路由跳转以免改动 App 状态）；明确「持久化、重启不丢、按供应商 Kilo 筛选」。
**Commit:** `🎨 style(kilo): 日志引导移到首屏可见位置`

---

## Task 5：构建 + 内置浏览器视觉验收

**行为:**
- `npx tsc -b`=0；`npm run build`=0；`gofmt -l`（无 Go 改动，跳过）。
- 重编 dist 并同步到 dev `src-tauri/target/debug/bin/dist/`；起 `npm run tauri:dev`（`OPCODE2API_KILO_PORT=30082`）。
- 内置浏览器 44100 → Kilo 页截图（改后）；与改前截图、设置页基准对比：标题区存在、警示仍常驻、状态卡不挤、端点信息齐全、表格列宽正常、日志引导首屏可见、虚拟模型卡正常。
- 窄屏（视口 900px）复核按钮组不重叠、表格不横向溢出。
- 截图存 `docs/design/screenshots-20261111/`（该目录被 .gitignore 忽略，本地保留）。
- **Commit:** `✅ test(kilo): UI 优化浏览器视觉验收`

---

## Task 6：文档与日志

- `docs/issue-log/2026-11-12.md` 追加阶段 P：设计分析结论、改了什么、截图路径、**「试跑还在」澄清（v2.0.6 发版早于阶段 O，属版本差非 bug）**。
- `OPEN.md` 更新（如用户后续决定发 v2.0.7 再登记）。
- 本计划：验收表 / 审查表 / 状态回填。

---

## 代码审查（阶段级环节）

**审查方：** 独立角色子代理（非实施者）

| 审查项 | 结论 | 问题清单 |
|---|---|---|
| 功能零回归（API 调用/状态逻辑/轮询未变） | | |
| 警示常驻不可关闭（需求本体） | | |
| 信息完整（端点/密钥/端口/目录数/更新时间全保留） | | |
| 与其他页设计语言一致（标题/卡片/按钮/表格） | | |
| 响应式（窄屏不重叠/不溢出） | | |
| 风格与规范（无新依赖/无全局样式改动） | | |

**结论：** ⬜

---

## 验收标准总表

| # | 标准 | 通过条件 | 责任人 |
|---|---|---|---|
| P-1 | 构建 | `npx tsc -b`=0；`npm run build`=0；`git diff --check` 干净 | 自动化 |
| P-2 | 功能零回归 | `grep` 核对 `api.kiloStatus/kiloModels/kiloRefresh/kiloToggle` 调用与 5s 轮询全部保留；无新增/删除 API | 主会话 |
| P-3 | 警示本体 | 横幅常驻、无 close handler、文案「数据可能被上游训练」一句保留 | 代码 + 浏览器 |
| P-4 | 信息完整 | 端点/密钥/端口/目录数/代理/更新时间/错误条全部仍渲染 | 浏览器 |
| P-5 | 设计对齐 | 有标题区；卡片 `p-5 shadow-sm`；区块标题 `text-lg`；按钮组独立工具条 | 浏览器 + 截图 |
| P-6 | 表格 | sticky + 420px 限高保留；长 ID 不挤压后三列 | 浏览器 |
| P-7 | 引导可见 | 「日志」引导位于状态卡后、模型卡前（首屏可见） | 浏览器 |
| P-8 | 响应式 | 900px 视口下按钮组不重叠、表格不横向溢出 | 浏览器 |
| P-9 | 红线 | 生产 exe/dist 未替换；无密钥入库；工作区干净 | 主会话 |
| P-10 | 文档 | issue-log / OPEN.md / 计划回填 + 截图路径 | 主会话 |
| P-11 | 独立审查 + 视觉验收 | 6 面结论 + 改前/改后截图对比 | 子代理 + 主会话 |

---

## 风险与降级

| 风险 | 缓解 |
|---|---|
| 警示降权后被忽略 | 保持唯一玫瑰元素 + 图标 + 不可关闭；只减 padding/字号，不改成灰条 |
| 端点区压缩后复制按钮不好点 | 保留独立复制按钮 `min 28px` 点击区；浏览器实测 |
| 表格列宽固定后超长 ID 截断 | `truncate` + `title` 属性保留完整 ID（hover 可见） |
| dev 前端不同步 | `npm run build` 后同步 `dist/` → `src-tauri/target/debug/bin/dist/`（阶段 K 踩过） |
| 「试跑还在」被误当 bug 反复报 | 阶段 P 日志明确写清版本时间线；是否发 v2.0.7 交用户决定 |

---

## 残留手工验收清单

1. 本阶段纯前端改动上生产需用户自行部署（用户明确「我自己更新」）；不自动打 tag/发 Release。若要让「试跑消失 + 通道分析 + 新 UI」进安装包，需另发 v2.0.7。
2. 阶段 K/L/M/N/O 遗留按各计划「阶段开头遗留」带入。
