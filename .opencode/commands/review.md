---
description: 提交前综合审查（代码风格+测试+依赖；非整阶段验收）
agent: build
---
执行**提交前**综合审查（单次 diff），按以下步骤进行：

1. 首先调用 @code-stylespector 检查本次变更涉及的文件代码风格
2. 然后调用 @test-engineer 运行相关测试
3. 如有 package.json 变更，调用 @dependencies-checker 检查依赖
4. 汇总审查结果，给出是否可以提交的建议

注意：
- 仅检查本次 git diff 中涉及的文件，避免全量扫描。
- 若人类要做**整阶段**对照计划验收，改用 `/accept-phase`（见 `docs/ai-framework/phased-plan-driven.md`），不要与本命令混淆。

$ARGUMENTS