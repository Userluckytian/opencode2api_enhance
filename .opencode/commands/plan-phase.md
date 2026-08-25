---
description: 起草阶段实施计划（Task/测试/验收表/交接提示词）并写入 docs 下 plans
agent: build
---

使用 @phase-planner 子代理，按 `docs/ai-framework/phased-plan-driven.md` 与 `AGENTS.md` 执行阶段规划。

要求：
1. 先读进度/总控文档与既有 `docs/ai-framework/plans/` 或 `docs/superpowers/plans/`，避免重复已完成阶段。
2. 产出完整阶段计划（含验收总表 + 文末可粘贴交接提示词五段结构）；可参考 `docs/ai-framework/phase-plan.template.md`。
3. 明确做/不做与红线；建议分支名；不要擅自 push。
4. 结束后给出计划路径；提示完成后用 `/accept-phase`，仅需提示词时用 `/handoff`。

人类补充的阶段目标与约束：
$ARGUMENTS
