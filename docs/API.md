# API 兼容说明

服务默认监听 `http://127.0.0.1:8000`，客户端不需要传入真实 OpenAI 或 Anthropic API key，但可以通过 `Authorization` 指定 OpenCode 上游模式。

## 鉴权与上游选择

- 无 `Authorization`，或 `Bearer public`
  - 走 public Zen 免费模型。
  - `/v1/models` 只返回 `-free` 模型和免费别名。
- `Bearer <opencode-api-key>`
  - 默认走 Zen。
  - 如果请求的是仅存在于 Go 目录中的模型，代理会自动切到 Go。
- `Bearer zen:<opencode-api-key>`
  - 强制走 Zen。
- `Bearer go:<opencode-api-key>`
  - 优先走 Go 订阅目录。
  - 对同时存在于 Zen 和 Go 的模型，也会按 Go 路径请求。

## 路由

| 路由 | 方法 | 说明 |
| --- | --- | --- |
| `/v1/models` | `GET` | 返回权限范围内的模型；已配置别名会替换对应上游模型 ID |
| `/v1/chat/completions` | `POST` | OpenAI Chat Completions 兼容入口 |
| `/v1/responses` | `POST` | OpenAI Responses 兼容入口 |
| `/v1/messages` | `POST` | Anthropic Messages 兼容入口 |
| `/health` | `GET` | 健康检查 |
| `/api/admin/*` | `GET`/`POST` | 管理面板后台接口（会话鉴权：配置/统计/实例/节点/网关/调用日志/自定义模型源等，路由注册见 `main.go`，handler 见 `core/manager/admin_http.go`） |
| `/api/reset-stats` | `DELETE` | 实例统计复位（管理器 ↔ 实例/网关子进程的内部契约，非前端使用） |

> 旧版 `/api/config`、`/api/stats`、`/api/reload` 等内嵌管理路由已于 2026-08-12 移除，
> 现行管理面统一为 `/api/admin/*`（前端由 dist/ 七页 UI 提供）。

## 节点导入（管理）

节点池的节点来源共四类：① Clash 外部控制 API（`clash_external_url`）② Clash Verge 默认 profiles 目录
③ 本管理器的订阅缓存 ④ **本地代理文件**（用户显式指定路径，见下）。合并顺序与去重规则见
`core/manager/clash_parse.go:ListNodesWithGroup`（外部 API 优先，其余来源补缺，最后按 `(group, name)` 排序）。

### 批量粘贴代理链接

`POST /api/admin/subscriptions/import-links`

```json
{
  "text": "vless://uuid@host:443?security=tls&type=ws&path=%2Fws#香港\nhttp://user:pass@1.2.3.4:8080#新加坡\nsocks5://1.2.3.4:1080#日本",
  "group": "我的手动节点"
}
```

响应：

```json
{ "status": "ok", "imported": 3, "group": "我的手动节点", "skipped": 0, "errors": ["第 4 行：不支持的协议 ftp://"] }
```

- `text` 支持多行明文链接、整段 Clash YAML、base64 订阅文本、sing-box JSON（自动识别）。
- 支持的协议：`http` / `https` / `socks` / `socks5` / `socks5h` / `socks4` / `vless` / `vmess` / `trojan` /
  `ss` / `hysteria2`(`hy2`) / `hysteria`(`hy1`) / `tuic` / `anytls` / `wireguard`(`wg`)。
- `group` 留空 → 自动 `手动导入N`（避开已有分组重名）。
- 解析采用「先整体、失败再逐行」：整体解析失败时逐行兜底，坏行以 `第 N 行：原因` 记入 `errors`，
  好行照常导入；若一条都没成功则返回 400。
- 导入的分组会同时登记为一条订阅源（占位 URL `manual://<分组名>`），因此在「订阅管理」列表里可见、可删除
  （删除会同步清理该分组节点与使用中的实例）。

### 本地代理文件 / 目录

| 路由 | 方法 | 说明 |
| --- | --- | --- |
| `/api/admin/local-profiles` | `GET` | 已登记的本地源列表 `{sources:[{path,group,kind,node_count,last_import,last_error}]}` |
| `/api/admin/local-profiles/add` | `POST` | `{path, group?}` 导入（首次登记）→ `{status,path,group,imported,files}` |
| `/api/admin/local-profiles/reload` | `POST` | `{path?}` 重载单个源；`path` 留空 = 重载全部 |
| `/api/admin/local-profiles/remove` | `POST` | `{path}` 移除源并清理该分组节点 → `{status,removed,removed_nodes}` |

- `path` 可为单文件（`.yaml` / `.yml` / `.txt`）或目录（取其下 **1 层**配置文件，不递归）。
- 解析优先按 Clash YAML 的 `proxies`；无节点时回退到订阅文本解析（明文链接 / base64），
  因此把链接列表存成 `.txt` 也能导入。
- **只读不写**：不会修改用户磁盘上的配置文件；改文件后点「重载」即可刷新节点。
- 节点写入订阅缓存（与订阅节点同源），因此可直接扫描、入池、按分组删除。
- 移除源时若该分组仍被其它源/订阅共用，只删源记录、不清节点。
- 单文件读取上限 32MB；目录内单文件失败不阻断其他文件（错误合并返回）。

`GET /v1/models` 的返回会随鉴权模式变化：

- `public` 只显示免费 Zen 模型。
- 默认或 `zen:` 模式显示 Zen 目录。
- `go:` 模式显示 Go 目录，并附带 public 可用的免费模型。
- 管理面板「自定义模型」页接入的自定义源模型**恒展示**，ID 带 `{源ID}/` 前缀（如 `myglm/glm-4.7`），
  各入站协议均可直接调用（网关剥前缀转发对应上游，协议自动转换；出站支持 openai / anthropic / responses / gemini 四种上游协议）。

## 自定义模型源（管理）

管理面板「自定义模型」页使用的后台接口（`requireAuth` 鉴权）：

| 路由 | 方法 | 说明 |
| --- | --- | --- |
| `/api/admin/custom-providers` | `GET` | 自定义源列表（**api_keys 明文回显**——编辑表单回填用，单用户/内网定位所致；含模型数与最近错误） |
| `/api/admin/custom-providers/save` | `POST` | 整表保存（增/改/删一次到位），保存即热生效 |
| `/api/admin/custom-providers/test` | `POST` | 连通测试（不落盘）：逐 key 拉取上游模型目录返回结果与延迟 |
| `/api/admin/custom-providers/probe` | `POST` | 活性探测：真实拉一次目录刷新健康，返回 ok/延迟/上次成功 |
| `/api/admin/custom-providers/clear` | `POST` | 清空全部自定义源（热生效，不触碰其它配置） |

## Chat Completions

### 准确支持

- `model`
- `messages`
- `stream`
- `temperature`
- `max_tokens`
- `top_p`
- `thinking`
- `reasoning_effort`
- `extra_body`
- `tools`
- `tool_choice`

流式响应会原样保留合法的 usage-only 尾块（`choices: []`）以及完整 usage details。

### Best-effort

- 上游 Anthropic 响应会转换 stop reason、usage、reasoning、refusal 和工具调用。
- 不同上游模型对 `thinking` / `reasoning_effort` 的支持可能不同。

### 不支持

- 本项目未声明支持的 Chat Completions beta 字段不会被合成或伪造。

`model` 会先经过 `model_alias` 解析。`reasoning_effort` 会按 `reasoning_effort_map` 转换。

## Responses API

### 准确支持

- 字符串 `input`；含 `input_text` / `input_image` 的 message item；函数及内置工具的 call/output item
- `instructions`、`messages`、`previous_response_id`
- 显式零值的 `temperature`、`top_p`、`frequency_penalty`、`presence_penalty`
- `max_output_tokens`、`stop`、`user`、`parallel_tool_calls`、`stream_options`、`store`
- 函数工具、项目已有的内置工具、`tool_choice`、`reasoning`、`metadata`
- 正常终态 `response.completed`；长度截断终态 `response.incomplete`，reason 为 `max_output_tokens`

### Best-effort

- Responses 会通过 Chat Completions 上游实现；内置工具被编码为函数工具后再还原。
- 仅在上游实际返回 reasoning 时生成 reasoning output item。

### 不支持

- `input_file` 等未在准确支持列表中的 input item 类型。
- `include` 及未在上面列出的可选 Responses 字段；这些字段不会用占位值伪装成已支持。

示例：

```bash
curl http://127.0.0.1:8000/v1/responses \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o-mini",
    "input": "Write one short sentence.",
    "stream": false
  }'
```

## Anthropic Messages

### 准确支持

- `system`、`stop_sequences`、`temperature` / `top_p` / `top_k`（包括显式零值）和 `metadata.user_id`
- 文本、base64/URL image、`tool_use`、`tool_result`（包括 `is_error`）；合法的 tool result 在普通用户内容之前的顺序会被保留
- `tool_choice` 的 `auto`、`any`、`tool`、`none`
- JSON Schema 约束字段（包括 `additionalProperties`、`format`）
- stop reason、usage 以及流式 content block 配对

### Best-effort

- thinking 会在没有 signature 时继续输出，以提高客户端兼容性。代理不会伪造 signature 或发送假的 `signature_delta`。
- `redacted_thinking.data` 是不可解释的加密数据，无法无损转成 reasoning；代理忽略该内容且不记录其数据。

### 不支持

- Anthropic beta、server tools、fallback 等本项目未承诺的扩展。

示例：

```bash
curl http://127.0.0.1:8000/v1/messages \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o-mini",
    "max_tokens": 256,
    "messages": [{"role": "user", "content": "hello"}]
  }'
```

## 流式响应

`stream: true` 时服务会使用 SSE 返回，并在内部清理空 delta、空 finish reason 和不需要的 reasoning 字段。Responses 和 Anthropic 流式接口会把上游 Chat Completions chunk 转换成对应事件。
