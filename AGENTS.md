# AGENTS — opencode2api_enhance

本仓库 AI 代理与协作者的**入口说明**。

> **开工前必读（本仓库专属纪律，优先级高于通用条款）：**
> 1. **`docs/AI-TESTING-GUIDE.md`**（⭐ 必读；本地文档，未入库）—— 端口与环境隔离避让指南。本机可能同时运行正式版（`D:\Program Files\opencode2api\`，生产服务）、dev、便携测试包等多个环境。**任何真实服务启动/测试前必须执行 §3 的端口与进程检查，并按 §5 模板做三件套环境隔离（`OPCODE2API_DATA_DIR` / `OPCODE2API_GATEWAY_PORT` / `OPCODE2API_INSTANCE_BASE_PORT`），禁止占用其它环境端口、禁止 kill 非自己启动的 opencode2api/sing-box 进程。** 单元测试与自动化 E2E（`go test`）用 httptest 随机端口，安全，随时可跑。
> 2. **`docs/ARCHITECTURE-V2-PLAN.md`**（本地文档，未入库）—— 架构 V2 改造计划（唯一事实来源），含阶段/验收表/决策记录。改架构前必读。
> 3. **UI 纪律（⭐ 全平台唯一界面）**：**七页 UI（独享 / 实例池 / 节点池 / 自定义模型 / 统计 / 日志 / 设置）是本项目全平台唯一事实界面**。不管哪个终端设备、什么语言/技术栈，界面都必须与 exe 这七个菜单**一致**——复用 `src/` 同一套前端，或按该 UI 逐一对应实现；**禁止另起一套不同风格的界面**。新增页面/菜单需先与七页对齐，改动七页布局需显式确认。
> 4. **测试纪律**：每步改动后 `go test -count=1 ./...` 全绿才提交；不触网的 mock/httptest 为准。

## 两套互补能力

| 能力 | 用途 | 入口 |
|------|------|------|
| **阶段化计划驱动** | 跨会话：定阶段 → 计划 → 交接执行 → 独立验收 | `docs/ai-framework/phased-plan-driven.md` |
| **提交前审查** | 单次 diff：风格 / 测试 / 依赖 | `CODE_REVIEW.md`、`/review` |

详细阶段工作流见：`docs/ai-framework/phased-plan-driven.md`。  
空白计划骨架：`docs/ai-framework/phase-plan.template.md`。  
阶段实例目录：`docs/ai-framework/plans/`（若已有 `docs/superpowers/plans/` 可继续用）。

## 强制遵循

1. **大块工作**先写阶段计划（含验收表与交接提示词），再实现；不要无计划的大范围编码。  
2. **验收认证据**：测试/构建命令实际跑过；禁止「应该能过」。  
3. **密钥不进 git**；破坏性操作与 push 远程需人类明确授权。  
4. **Git 提交**见下文规范；默认不 push。  
5. 编码与架构约定见 `coding-standards.md`、`architecture.md`（若存在）。  
6. **按天问题日志**：处理任何问题/需求时，同步维护 `docs/issue-log/YYYY-MM-DD.md`（描述 / 分析 / 修改结果 / 状态）；修复后更新状态为「已关闭」并附验证证据；未关闭项次日自动带过。约定详见 `docs/issue-log/README.md`。

## OpenCode 命令

### 阶段协作

| 命令 | 子代理 | 用途 |
|------|--------|------|
| `/plan-phase` | `@phase-planner` | 起草阶段计划 + 文末交接提示词 |
| `/accept-phase` | `@phase-acceptor` | 对照计划独立验收（四段结论） |
| `/handoff` | — | 从已有计划生成可粘贴执行提示词 |

### 提交前审查

| 命令 | 子代理 | 用途 |
|------|--------|------|
| `/test` | `@test-engineer` | 测试编写与运行 |
| `/audit` | `@project-auditor` | 项目自检与升级建议 |
| `/deps` | `@dependencies-checker` | 依赖检查 |
| `/style` | `@code-stylespector` | 代码风格 |
| `/review` | 综合 | 依次风格 + 测试 +（如有）依赖 |

### 视觉分析

| 子代理 | 用途 |
|--------|------|
| `@vision-analyst` | 图片/截图/UI 图识别与描述（主力模型无视觉能力时的看图通道） |

**遇到图片相关任务**（用户贴图、项目里的截图/设计图/流程图、需要 OCR 或 UI 描述的请求）：优先调用 `@vision-analyst`，把图片路径和分析目标传给它，**不要自行猜测图片内容**。

## Git 提交规范

```
<gitmoji><type>(<scope>): <中文描述>
```

| type | 说明 | gitmoji |
|------|------|---------|
| feat | 新功能 | ✨ |
| fix | 修复 bug | 🐛 |
| docs | 文档更新 | 📝 |
| style | 代码格式（不影响功能） | 🎨 |
| refactor | 重构 | ♻️ |
| test | 测试 | ✅ |
| chore | 构建/工具 | 🔧 |
| perf | 性能 | ⚡ |
| ci | CI/CD | 🐳 |
| revert | 回滚 | ⏪ |

- 描述使用**中文**，祈使语气，结尾不加句号  
- 首行尽量不超过 50 字符  

### 分支命名（建议）

```
feat/<topic> | fix/<topic> | chore/<topic>
```

阶段工作常用：`feat/phase-x-<slug>`。

### 提交前

在 `git commit` 前可询问是否 `/review` 或按 `CODE_REVIEW.md` 检查。  
**整阶段交付**另用 `/accept-phase`，与单次 review 不互相替代。

## 原则四条（阶段工作）

1. 边界先于功能  
2. 计划必须可交接（零上下文提示词）  
3. 任务必须可验证（命令 + 期望）  
4. 验收独立且认证据（缺陷显式带入下阶段）
