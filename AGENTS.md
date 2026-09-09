# AGENTS — opencode2api_enhance

本仓库 AI 代理与协作者的**入口说明**。

## 两套互补能力

| 能力 | 用途 | 入口 |
|------|------|------|
| **阶段化计划驱动** | 跨会话：定阶段 → 计划 → 交接执行 → 独立验收 | `docs/ai-framework/phased-plan-driven.md` |
| **提交前审查** | 单次 diff：风格 / 测试 / 依赖 | `CODE_REVIEW.md`、`/review` |

详细阶段工作流见：`docs/ai-framework/phased-plan-driven.md`（含**运行模式**：交互式默认 / **目标模式**自主连续推进，见 §12。对 AI 说「**进入目标模式**」即开启，或开工前由框架询问）。  
空白计划骨架：`docs/ai-framework/phase-plan.template.md`。  
阶段实例目录：`docs/ai-framework/plans/`（若已有 `docs/superpowers/plans/` 可继续用）。

## 强制遵循

1. **大块工作**先写阶段计划（含验收表与交接提示词），再实现；不要无计划的大范围编码。开工前按用户选择**实施档位**（默认全能）与**是否启用子代理**；低调档位在验收表如实登记「跳过项」，不作全绿。  
2. **验收认证据**：测试/构建命令实际跑过；禁止「应该能过」。  
3. **密钥不进 git**；破坏性操作与 push 远程需人类明确授权。  
4. **Git 提交**见下文规范；默认不 push。  
5. 编码约定见 `coding-standards.md`（若存在）。  
6. **按天问题日志**：处理任何问题/需求时，同步维护 `docs/issue-log/YYYY-MM-DD.md`（描述 / 分析 / 修改结果 / 状态）；修复后更新状态为「已关闭」并附验证证据；未关闭项次日自动带过。**开工先读 `docs/issue-log/OPEN.md`（开放事项索引，只读它即可掌握全部未完成项，无需翻历史日志）**，再按需读对应日期文件。约定详见 `docs/issue-log/README.md`。

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
