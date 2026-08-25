---
description: 按脚手架模板新建项目专属子代理（填空生成 .opencode/agents/<name>.md）
agent: build
---

使用 `docs/ai-framework/agent.template.md` 脚手架，为当前项目新建一个子代理。

需求：$ARGUMENTS

步骤：

1. 读脚手架 `docs/ai-framework/agent.template.md`；若不存在（旧版安装），退化为复制
   `.opencode/agents/` 下职责最接近的既有代理作底稿。
2. 需求不明确时先向用户确认：职责边界、只读还是可写、是否联网、是否固定模型。
3. 复制脚手架 `---8<---` 以后的内容到 `.opencode/agents/<kebab-case-name>.md`，
   逐项填空并删除〔〕指引与 `#` 注释指引；权限默认值遵照脚手架头部约定
   （审查类只读 `edit: deny` / `webfetch: deny`）。
4. 自查：description 含触发词；permission 与职责一致；输出格式固定；禁止事项非空。
5. 汇报写入路径、调用方式（`@name` 与触发词），并提示重载 OpenCode 后生效。
