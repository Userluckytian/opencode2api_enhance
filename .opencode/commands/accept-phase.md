---
description: 对照阶段计划做独立验收（证据优先，输出四段结论）
agent: build
---

使用 @phase-acceptor 子代理，按 `docs/ai-framework/phased-plan-driven.md` 验收四段结构做独立验收。

要求：
1. 定位阶段计划（参数中的路径或阶段名；查 `docs/ai-framework/plans/` 与 `docs/superpowers/plans/`）。
2. 复跑计划要求的测试/构建；核对红线与密钥是否误入库。
3. 输出强制四段：基线、对照表、结论（通过/有条件通过/不通过）、下一步。
4. 默认不修改业务代码；缺陷列出并建议写入下一阶段 Task 1。

阶段计划路径或阶段代号及执行方自述：
$ARGUMENTS
