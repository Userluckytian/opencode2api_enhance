# 子代理脚手架（agent.template）

> **用途：** 为 opencode2api_enhance 新增项目专属子代理。把下方 `---8<---` 以后的内容复制到
> `.opencode/agents/<agent-name>.md`（kebab-case 文件名），按 `〔〕` 指引填空，填完删除指引。
> **更快的方式：** 直接对 AI 说 `/new-agent <需求>`，由 AI 按本模板生成。
> **权限默认值约定（来自既有 7 个子代理的实践）：** 审查/分析类只读（`edit: deny`、
> `webfetch: deny`）；规划/测试等需要落盘产出的才开 `edit: allow`；联网查资料才开 `webfetch: allow`。

---8<--- 复制从此行之后开始（本行不要复制）---

---
description: 〔一句话职责 + 触发词。主代理靠这段决定何时调你，把「图片」「依赖」「测试」这类触发词写进去，参考 vision-analyst 的 description 写法〕
mode: subagent
# model: 〔仅当需要固定到特定模型时保留本行并填值（如 oc-local/mimo-v2.5），否则整行删除〕
permission:
  edit: deny      # 〔只读审查类保持 deny；规划/测试等需落盘产出才改 allow〕
  bash: allow
  read: allow
  grep: allow
  glob: allow
  webfetch: deny  # 〔需要联网查资料才改 allow〕
  task: deny
---

## 职责

〔你是谁、专管什么、何时被主代理调用。2–3 句。示例开头：你是 XX 审查员，专门负责 opencode2api_enhance 的 XX。〕

## 输入

〔写清楚吃什么，主代理才知道调用时传什么：

- **必填**：文件路径 / diff 范围 / 目标模块 / ……
- **可选**：关注点、期望输出格式……连同默认值一起写明〕

## 工作步骤

〔1. 2. 3.，每步可验证。必须包含失败路径：输入不存在 / 命令失败时报告具体错误，不编造。〕

## 输出格式（强制）

〔固定结构，主代理要能稳定消费你的结果。常见骨架：

1. **结论**：一句话
2. **明细**：文件、行号、问题、建议（表格或列表）
3. **统计**：严重程度分级 / 合规率〕

## 禁止

〔红线，按适用保留并补充项目专属项：

- 编造看不到的内容或未执行的验证结果
- 越权：做超出 permission 声明的操作
- 〔项目专属红线〕〕
