# 配置说明

默认配置文件是 `config.json`。首次运行可以从示例复制：

```bash
cp config.example.json config.json
```

## 字段

### `model_alias`

模型别名映射。键是客户端请求的模型名，值是实际传给上游的模型名。

```json
{
  "model_alias": {
    "deepseek-v4-flash": "deepseek-v4-flash-free",
    "mimo-v2.5": "mimo-v2.5-free",
    "ling-3.0-flash": "ling-3.0-flash-free",
    "nemotron-3-ultra": "nemotron-3-ultra-free",
    "north-mini-code": "north-mini-code-free",
    "laguna-s-2.1": "laguna-s-2.1-free"
  }
}
```

### `reasoning_effort_map`

把客户端传入的 `reasoning_effort` 映射到上游可接受的值。

```json
{
  "reasoning_effort_map": {
    "minimal": "low",
    "medium": "medium",
    "high": "high"
  }
}
```

### `force_disable_thinking`

设为 `true` 时，服务会尽量禁用 thinking/reasoning，并从返回中移除 reasoning 内容。

### `socks5_proxies`

SOCKS5 代理列表。

```json
{
  "socks5_proxies": [
    {
      "name": "local",
      "addr": "127.0.0.1:1080",
      "username": "",
      "password": ""
    }
  ]
}
```

### `active_socks5`

启用的代理。

- 空字符串：直连
- 某个 `addr`：固定使用该代理
- `__round_robin__`：在多个代理之间轮询

### `providers`（厂商注册）

每个厂商一个条目：`type`（对应厂商类型）、`id`、`name`、`enabled`、`params`（厂商自定义参数）。
**未配置 `providers` 时自动注册全部内建厂商**（扩增即生效；`custom` 除外——必须带条目参数）；
配置后按配置注册（`enabled: false` 可关闭某厂商）。

```json
{
  "providers": [
    {
      "id": "opencode",
      "type": "opencode",
      "name": "OpenCode",
      "enabled": true
    },
    {
      "id": "myglm",
      "type": "custom",
      "name": "智谱 GLM",
      "enabled": true,
      "params": {
        "base_url": "https://open.bigmodel.cn/api/paas/v4",
        "api_key": "sk-...",
        "protocol": "openai",
        "via_proxy": false
      }
    }
  ]
}
```

#### opencode 内建厂商参数（`params`）

| 参数 | 必填 | 说明 |
|---|---|---|
| `client_version` | — | 免费通道上报的客户端版本（UA `opencode/prod/<值>/cli`）；空 = 启动时自动探测 npm，失败回落内置值。上游抬高免费下限时改这里跟进，无需重编译 |
| `prefer_responses` | — | **出站协议偏好（v1.7.8+）**：`chat`（默认，含空/非法值）= 现状，仅 muse 系走上游 Responses；`responses` = 全部 opencode 模型优先走上游原生 `/responses` 端点（免费档 `/inference/v1/responses`，带 key `/zen/v1/responses`，go 面 `/zen/go/v1/responses`），响应在厂商内翻回 chat 契约，下游三入口/failover/续写/竞速不变。**带图片的请求自动回落 chat**（chat 为图片已验证路径）。配置热生效需重启网关（params 在厂商构造期读取） |

> `prefer_responses: "responses"` 灰度手册：上游行为矩阵实测见
> `docs/ai-framework/plans/probes/RESULTS.md`（2026-10-20）。与 chat 路径的两个已知行为差异：
> ① reasoning 为**明文摘要流**（映射到 `delta.reasoning_content`，非全文思考）；
> ② 非 muse 模型 `tool_choice` 三态（none/命名函数/required）上游均支持（muse 仅 auto，维持现状近似处理）。
> 放量观察：日志页「错误分析」对比两协议的失败率、`unexpected EOF` 中断率（「单节点反复中断」标签）与 TTFT；
> 回退：改回 `"chat"` 或不填并重启，**一键、无数据迁移**。

#### custom 自定义模型源参数（`params`）

用户自带 key 接入第三方供应商（管理面板「自定义模型」页可视化编辑，以下为配置等价形式）。

> 完整使用说明见 [自定义模型源指南](CUSTOM-MODELS.md)。
**一条 `type: "custom"` 条目 = 一个源，可配多条**；模型在 `/v1/models` 中带 `{id}/` 前缀
（如 `myglm/glm-4.7`），调用时网关自动剥前缀转发上游。

| 参数 | 必填 | 说明 |
|---|---|---|
| `base_url` | ✅ | 上游 API 根地址（含版本路径，如 `https://api.openai.com/v1`、`https://api.anthropic.com/v1`、`https://generativelanguage.googleapis.com/v1beta`；尾斜杠容忍） |
| `protocol` | — | 出站协议：`openai`（默认，OpenAI 兼容）/ `anthropic` / `responses`（OpenAI Responses API）/ `gemini` |
| `api_key` | — | 单 key（兼容字段，与 `api_keys` 合并去重） |
| `api_keys` | — | **多 key**（数组）：429 冷却（读 `Retry-After`，缺省 60s）、401/403 禁用后自动换 key，同请求每 key 至多试一次，全部耗尽才交外层厂商级切换 |
| `key_strategy` | `round_robin` | key 调度：`round_robin` 轮询 / `failover` 错误转移（按数组顺序主 key 优先）。**仅作用于本自定义源，与实例池 `route_mode` 互不影响** |
| `via_proxy` | — | `true` 时出站走节点池代理（应对地区限制供应商）；默认 `false` 直连 |
| `allowed_models` | — | 暴露白名单（上游模型 ID 数组；空 = 全部暴露），热生效 |

> 模型清单缓存在 `<数据目录>/custom_models/<id>.json`（成功拉取时原子写；拉取失败时兜底返回，
> 重启后无需等上游即出模型）。连通测试（面板「测试并获取模型」）不走缓存、强制真实触达。
>
> 注意：以 `_` 开头的 params 键保留给 core 运行时注入（Transport 等），配置中的同名键会被忽略。

### `routing`（模型路由）

```json
{
  "routing": {
    "model_provider_map": {},
    "default_provider": "opencode"
  }
}
```

- `model_provider_map`：强制指定某模型走某厂商（同名多厂商时的优先选择）
- `default_provider`：兜底厂商
- 未命中映射时，按聚合器倒排索引找"提供该模型的厂商"，再无则走默认厂商

### `auto_model`（auto 虚拟模型）

> 完整行为说明见 [ROUTING.md 第七节](ROUTING.md)。配置入口：实例池页 → 右上齿轮 → 「auto 模型 · 智能选路」；默认关闭。

```json
{
  "auto_model": {
    "enabled": true,
    "strategy": "balanced",
    "weights": { "deepseek-v4-flash": 9, "big-pickle": 3 },
    "context_windows": { "deepseek-v4-flash": 200000, "big-pickle": 1000000 }
  }
}
```

| 字段 | 默认 | 说明 |
|---|---|---|
| `enabled` | `false` | 开启后 `/v1/models` 顶部出现虚拟模型 `auto`，客户端填 `model:"auto"` 即智能选路；关闭即消失 |
| `strategy` | `balanced` | `balanced` 均衡（SWRR 按权重分流）/ `speed` 速度优先（权重≥5 中选实测最快）/ `quality` 能力优先（按权重锁定，失败才降） |
| `weights` | 缺省 5 | 模型展示名（`/v1/models` 可见名）→ 权重 0~10；**0 = 永不参与**；按模型粒度，同模型跨实例同权重 |
| `context_windows` | 保守 128k | 模型展示名 → 上下文上限 token；超限请求自动避开该模型（est≤上限×0.9），上游报上下文错误时系统还会自动学习收紧 |

管理端 `GET /api/admin/auto-model` 读取、`POST /api/admin/auto-model` 保存（保存即传播子进程，3s 热重载生效）。

### 订阅导入边界（2026-08-16 拍板）

订阅导入（节点池页「订阅导入」及后台按间隔自动拉取）**一律只更新节点池**：
拉取订阅节点写入订阅缓存，**不创建任何实例**（不再区分独享/进池/仅节点池导入目标）。
需要使用时，请在节点池勾选节点后手动【设为独享】或【入池】添加实例。
删除订阅时，该订阅分组的节点池节点会一并清除。

## 管理面板

打开 `http://127.0.0.1:8000/` 可进入管理面板。面板可以修改配置、刷新模型和查看 token 统计。

管理鉴权**默认关闭**（默认空密码，打开页面/接口无需登录）。如需开启：

```bash
./opencode2api -password "your-strong-password"
```

设置后 `/api/admin/*` 与 `/` 将 302 到 `/login` 登录页（该密码同时作为 `/v1` API 密钥）。
