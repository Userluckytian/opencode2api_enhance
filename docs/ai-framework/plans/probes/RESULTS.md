# T1 探测结论 — opencode 上游 responses 行为矩阵（2026-10-20）

探测方法：复刻生产形态（v2 身份：合规 ses_ 时间指纹 + UA `opencode/prod/2.0.29/cli` + 四会话头同值；
门禁 body：stream:true + 四工具真实 schema + instructions 占位说明），直连
`https://opencode.ai/inference/v1/responses`，SOCKS 出口轮换 42100-42105，间隔 ≥6s。
原始流：本目录 `raw_*.txt`（本地，不入库）。执行器 `../run.sh`，body 生成器 `../oc-gate-body.mjs`。

| # | 场景 | 模型 | HTTP | 结论 | 对 T2/T3 的约束 |
|---|------|------|------|------|----------------|
| P4 | 纯文本基线 | deepseek-v4.1-flash-free | 200 | 完整事件流 `response.created→…→completed`；响应 id 原生 `resp_…`（消息项 `msg_`） | 基线可用 |
| P1 | `reasoning:{effort:medium,summary:auto}` | deepseek | 200 | **明文摘要**：283 个 `response.reasoning_summary_text.delta`（非加密、非全文思考）；`reasoning_tokens` 明细在 usage | T3-2：把 summary delta 映射回 chat `delta.reasoning_content`；文档标注「responses 下思考流=摘要形态」 |
| P2a | `tool_choice:"none"` | deepseek | 200 | 上游**接受并尊重**（响应回显 `"tool_choice":"none"`，模型声明无工具可用） | 回落清单**不含** none（旧注释的 none 限制是 muse 专属） |
| P2b | `tool_choice:{type:function,name}` | deepseek | 200 | 上游**接受并产出** `function_call` 项（get_weather 被调用） | 回落清单**不含**命名函数 |
| P2c | `tool_choice:"required"` | deepseek | 200 | 上游**接受并产出** `function_call` 项 | 回落清单**不含** required |
| P3 | 1×1 红 PNG `input_image` | nemotron-3.5-super-vl | **400** | 报 `Model is unavailable` —— 对照 C1 证明是**模型级**错误（同模型纯文本也 400），非图片拒绝 | 转 C2 判定 |
| C1 | 纯文本对照 | nemotron-3.5-super-vl | 400 | 同上 → nemotron 在该免费通道不可用（与协议/图片无关） | 探测模型可用性 ≠ 协议结论，先做纯文本对照 |
| C2 | 同 P3 换模型 | deepseek-v4.1-flash-free | **200** | `input_image` 被处理：输入 token 从 ~60 涨到 998（图片计费特征）；模型对 1×1 小图颜色作答不稳定（pink vs red，且 max_tokens=20 截断在思考中途），**视觉效果未证实** | 图片不列入回落清单的「必然失败」，但 prefer=responses 时**含图片的请求默认回落 chat**（chat 是图片行为的已验证路径；视觉质量无法在 1px 探针上证实） |

## 关键翻案

1. **tool_choice 三态全部可用**（none/命名函数/required）——`responses.go:246-266` 记录的「muse 的 responses 端点仅支持 auto」限制是**模型级特例**，对 deepseek 类通用模型不成立。回落保护只需针对 muse 保留现有逻辑。
2. **reasoning 在 responses 下是「摘要流」形态**（`reasoning_summary_text.delta`），chat 下是全文 `reasoning_content` delta。翻译层可映射回 `delta.reasoning_content`，但**信息量不同**（摘要 ≠ 全文思考）→ 这是灰度对比时要盯的行为差异，写进配置文档。
3. **图片 400 的教训与 2026-10-13 同型**：模型级不可用会被误读为协议不支持——对照实验（换协议变量前先换内容变量）是必须步骤。

## 对 #4 评估的修正

issue-log 2026-10-20 #4 列的三个回归缺口中，**tool_choice 缺口基本不存在**（三态可用）；
**图片缺口以回落策略收口**（映射可做，但默认回落 chat 更稳）；
**reasoning 缺口真实存在但形态明确**（摘要 delta → reasoning_content，可做）。
