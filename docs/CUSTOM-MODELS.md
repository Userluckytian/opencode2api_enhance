# 自定义模型源使用指南

> 面板第七页「自定义模型」——用你自己的 API Key 接入任意第三方模型供应商，与内建的免费源（OpenCode / Devin-Windsurf）并列聚合在同一个网关下。

## 这是什么

很多工具（Claude Code、Cursor、Cline 等）只认一个 OpenAI 兼容端点。本功能让你像其他 API 路由器一样：**填 API 地址 + 协议 + Key，即可把第三方供应商（GLM、DeepSeek、Kimi、OpenRouter、OpenAI、Anthropic、Gemini……）的模型接入本项目的统一网关**——统一鉴权、统一 `/v1/models`、统一统计与调用日志，并复用节点池/多实例的出口能力。

核心特性：

- **可同时接入多个源**，每个源一个独立 ID；支持 **OpenAI 兼容 / Anthropic / OpenAI Responses / Gemini** 四种上游协议
- **多 Key 池**：一个源配多个 Key，可选 **轮询（round_robin）** 或 **错误转移（failover）** 调度；429 自动冷却（读 `Retry-After`）、401/403 自动禁用并换 Key
- **保存即热生效**：无需重启网关，也不用重启实例池/独享实例
- 模型清单**磁盘缓存**：重启后第一个请求即返回模型列表，不依赖上游可达
- 调用链路完整复用：token 统计、调用日志、失败切换（厂商级 failover）

## 快速开始

1. 面板左侧「**自定义模型**」→「**添加模型源**」
2. 填写：
   - **源 ID**：字母数字开头，可含 `-` `_`（它决定模型前缀，如 `myglm`）
   - **协议**：按供应商选择（见下表）
   - **API 地址**：填到版本根路径（示例见下表），不要带尾斜杠
   - **API Key**：一行一个，可贴多个
   - **Key 调度策略**：轮询 / 错误转移（单 Key 时无差别）
3. 点「**测试并获取模型**」——不落盘，逐 Key 真连上游拉模型清单预览
4. 「**保存**」——立即生效

之后在任意客户端：

```bash
# 模型列表：自定义模型带 {源ID}/ 前缀
curl http://127.0.0.1:40080/v1/models

# 对话（三种入站协议都行，OpenAI / Claude / Responses）
curl http://127.0.0.1:40080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"myglm/glm-4.7","messages":[{"role":"user","content":"你好"}]}'
```

## 支持的协议与地址示例

| 协议 | 适用供应商（示例） | base_url 示例 |
|---|---|---|
| **OpenAI 兼容**（默认） | GLM、DeepSeek、Kimi、OpenRouter、Groq、硅基流动、ollama 等 | `https://open.bigmodel.cn/api/paas/v4`、`http://127.0.0.1:11434/v1` |
| **Anthropic** | Anthropic 官方及兼容端 | `https://api.anthropic.com/v1` |
| **OpenAI Responses** | OpenAI 官方 Responses API | `https://api.openai.com/v1` |
| **Google Gemini** | Google AI Studio | `https://generativelanguage.googleapis.com/v1beta` |

无论上游是哪种协议，网关对外始终提供 OpenAI / Claude（`/v1/messages`）/ Responses（`/v1/responses`）三种入站端点，协议转换（含流式 SSE、工具调用、图片、思考内容）自动完成。

## 多 Key 与调度策略

一个源配 N 个 Key = N 倍限流额度。两种调度**只作用于本源**，与实例池的路由模式（smart/failover/round_robin）互不相干：

- **轮询**（默认）：请求在可用 Key 间均匀分摊
- **错误转移**：按你填写的顺序主 Key 优先，主 Key 冷却/失效才降级到备用

健康语义（列表页徽标实时展示，仅状态不涉用量统计）：

| 情形 | 处理 |
|---|---|
| 429 / 限流 | 该 Key **冷却**（优先读上游 `Retry-After`，缺省 60s），到期自动回池；同请求立即换下一个 Key |
| 401 / 403 | 该 Key **禁用**（运行期内不再使用）；同请求换下一个 Key |
| 5xx / 超时 | 不惩罚 Key，换下一个 Key 重试一次 |
| 全部 Key 耗尽 | 返回最后一次错误，交给外层厂商级失败切换（若其它源/厂商也提供同名模型则自动接手） |

同一请求每个 Key 最多尝试一次，不会在坏 Key 上打转。

## 暴露白名单（只放出部分模型）

供应商动辄上百个模型（如 NVIDIA 有 100+），全部混进 `/v1/models` 会淹没你的主用模型。编辑弹层底部「**暴露模型**」：

- 默认「**全部暴露**」
- 取消勾选后逐个勾选要暴露的模型——未勾选的**不出现在 `/v1/models`，也无法经网关调用**
- 点「测试并获取模型」会刷新全量清单（已有勾选保留，新模型默认不勾）
- 白名单存于配置 `allowed_models`，热生效；全量清单有磁盘缓存，重启后勾选界面依然可列出全部模型

## 活性探测

与独享/实例池的测试能力对齐，自定义源提供两个层次的活性探测：

- **手动**：源卡片上的活性徽标（`活跃 HH:MM` / `异常`）即可点击——真实拉一次上游 `/models`（不走缓存），立即刷新健康并提示延迟
- **后台**：网关每 5 分钟自动对所有已启用源探测一轮，无流量时健康状态也保持新鲜；异常信息显示在卡片与源详情

健康状态同时反映真实请求的成败（探测 + 业务流量共用一份健康）。

## 模型命名与路由

- 自定义模型在 `/v1/models` 中**恒带 `{源ID}/` 前缀**（如 `myglm/glm-4.7`），与内建源的同名模型天然隔离；调用时网关自动剥前缀转发上游
- 客户端无需携带该源的 Key——Key 由网关持有，任何客户端 Key（或无 Key）都能调用
- 想用不带前缀的短名？在 `config.json` 的 `model_alias` 里加 `"glm47": "myglm/glm-4.7"` 即可
- 想强制某个模型走某个源？用 `routing.model_provider_map`（见 [配置文档](CONFIGURATION.md)）

## 生效机制（为什么不用重启）

- **保存**：写入核心配置 → 厂商集合**原地重建**（opencode 会话、windsurf 账号池状态保留）→ 模型目录立即刷新
- **子进程传播**：自定义源会同步补写进所有已存在实例与统一网关的 runtime 配置；**运行中的子进程**靠 1 秒配置监视热重载——它们的 `/v1/models` 不重启即出现自定义模型；**停着的实例**下次启动自动带上
- **重启后**：模型清单磁盘缓存（`<数据目录>/custom_models/<源ID>.json`，原子写）保证首请求即返回，后台再向上游刷新修正

## 配置参考

面板保存等价于在 `config.json` 写入（手动编辑亦可，同样热生效）：

```json
{
  "providers": [
    { "id": "opencode", "type": "opencode", "enabled": true },
    { "id": "windsurf", "type": "windsurf", "enabled": true },
    {
      "id": "myglm",
      "type": "custom",
      "name": "智谱 GLM",
      "enabled": true,
      "params": {
        "base_url": "https://open.bigmodel.cn/api/paas/v4",
        "api_keys": ["sk-xxx1", "sk-xxx2"],
        "key_strategy": "round_robin",
        "protocol": "openai",
        "via_proxy": false
      }
    }
  ]
}
```

| 参数 | 必填 | 说明 |
|---|---|---|
| `base_url` | ✅ | 上游 API 根地址（含版本路径） |
| `protocol` | — | `openai`（默认）/ `anthropic` / `responses` / `gemini` |
| `api_keys` | — | 多 key 数组；`api_key` 单 key 为兼容字段，两者合并去重 |
| `key_strategy` | — | `round_robin`（默认）/ `failover`，仅作用于本源 |
| `via_proxy` | — | `true` 时出站走该实例的节点池代理（应对有地区限制的供应商）；默认直连 |
| `allowed_models` | — | 暴露白名单（上游模型 ID 数组；空 = 全部暴露）。目录缓存保存全量，仅对外过滤 |

> 以 `_` 开头的 params 键保留给 core 运行时注入，配置中同名键会被忽略。

## 管理 API

面板「自定义模型」页背后的接口（`requireAuth` 鉴权）：

| 路由 | 方法 | 说明 |
|---|---|---|
| `/api/admin/custom-providers` | `GET` | 源列表（含 keys 明文回填编辑表单、模型数、key 健康计数、最近错误） |
| `/api/admin/custom-providers/save` | `POST` | 整表保存（增/改/删一次到位），保存即热生效并传播到子进程 |
| `/api/admin/custom-providers/test` | `POST` | 连通测试（不落盘、不走缓存），逐 key 返回模型数与延迟，附首个成功 key 的模型清单 |
| `/api/admin/custom-providers/probe` | `POST` | 活性探测：`{"id":"源ID"}`，真实拉一次目录刷新健康，返回 ok/延迟/上次成功时间 |

## 架构与二次开发

实现集中在 `vendors/custom/`（一源一实例，实现 `core/contract` 的 `Vendor` 接口）：

```
vendors/custom/
  custom.go          # 契约实现 + key 池编排（withKeys/withKeysStream）
  keypool.go         # 多 key 调度与健康状态（冷却/禁用）
  registry.go        # contract.Register("custom", 工厂)：providers 条目 → 实例
  cache.go           # 模型清单磁盘缓存（stale-while-revalidate）
  proto_openai.go    # OpenAI 兼容：近透传
  proto_anthropic.go # Anthropic messages 双向转换 + SSE 流转换
  proto_responses.go # OpenAI Responses 双向转换 + SSE 流转换
  proto_gemini.go    # Gemini generateContent 双向转换 + SSE 流转换
  sse.go             # 通用 SSE 转换管道（原生事件 → OpenAI chunk）
```

关键设计：

- **契约驱动**：统一 OpenAI Chat 形态进出（`contract.Message` / `Reply` / `Stream`），core 的协议转换、路由、聚合、统计零改动
- **新增上游协议** = 新增一个 `chatProto` 实现（`listModels / chat / chatStream` 三方法，key 由池调度传入）+ `New()` 一个分支 + UI 一个选项，约 300 行
- 请求出站统一走注入的 `contract.Transport`（直连或节点池），统计/日志/失败切换由 core 既有链路自动覆盖

## 插件式供应商（providers/ 目录）

> 与「用户自定义供应商」并列的第二种接入方式：**自带适配逻辑的即插即用供应商插件**。
> 设计定稿见 [PLUGIN-PROVIDERS.md](PLUGIN-PROVIDERS.md)（唯一事实来源），本节是用户侧操作指南。

### 它和「自定义模型源」有什么不同

| | 用户自定义供应商（上文） | 插件式供应商（本节） |
|---|---|---|
| 适配逻辑 | 主进程内置通用协议转换（OpenAI/Anthropic/Gemini/Responses） | **插件自带**（供应商特殊鉴权/协议怪癖/Session 续期等由插件自己处理） |
| 接入方式 | 面板填 base_url + Key | 把供应商文件夹复制进 `providers/` 即用 |
| 配置载体 | `config.json` 的 `providers` 条目 | 目录内 `provider.json`（含私有的 `provider_private_configs`） |
| 升级/修复 | 等主进程版本 | 替换目录内 exe 即完成 |

一句话：自定义源 = 用通用协议接「兼容型」上游；插件 = 供应商（或你）自己写的专用适配器，
主进程只做透明桥接 + 生命周期管理。

### providers/ 目录结构

```
<安装目录>/                      # Windows: exe 旁；Linux deb 版: 数据目录 bin/ 下（OPCODE2API_DATA_DIR）
├── opencode2api.exe            # 主网关（Linux 版无 .exe 后缀）
└── providers/                  # ★ 主进程自动扫描此目录
    └── loomy/                  # 目录名 = 插件 id（决定模型前缀 loomy/）
        ├── provider.json       # 契约 + 私有配置（单文件 all-in-one）
        ├── loomy-provider.exe  # 供应商侧车程序（Windows 形态；Linux/macOS 为无后缀 ELF/Mach-O）
        └── data/               # （可选）插件自己的运行数据，主进程不扫描不管理
```

主进程只认固定文件名 `provider.json`，只读不写；扫描到即自动拉起，无需重启网关。

> **插件是可执行文件，必须与宿主平台匹配**（Windows 的 `.exe` 不能用于 Linux）：
> Linux 版需交叉编译出无扩展名的 ELF 文件（`GOOS=linux CGO_ENABLED=0 go build`），并把
> `provider.json` 的 `entry` 改成对应文件名（无 `.exe` 后缀）再部署——详见
> [PLUGIN-PROVIDERS.md](PLUGIN-PROVIDERS.md) §2.1 平台形态。

### 快速接入三步

拿到一个插件包（`providers/<id>/` 文件夹，含 `provider.json` + 供应商 exe）后的完整接入流程：

1. **复制**：把整个插件文件夹放进 `<安装目录>/providers/` 下
   - Windows：
     ```powershell
     Copy-Item "D:\tmp\loomy" "D:\Program Files\opencode2api\providers\" -Recurse
     ```
   - Linux（deb 安装版，数据目录见 `manager.env` 的 `OPCODE2API_DATA_DIR`）：
     ```bash
     sudo mkdir -p /var/lib/opencode2api/bin/providers
     sudo tar -xzf loomy-provider-linux-amd64.tar.gz -C /var/lib/opencode2api/bin/providers/
     sudo chmod +x /var/lib/opencode2api/bin/providers/loomy/loomy-provider
     sudo chown -R opencode2api:opencode2api /var/lib/opencode2api/bin/providers/loomy
     ```
2. **启动/扫描**：双击主程序（或已运行时点面板「插件式供应商」标签的「重新扫描」），
   主进程 3 秒内自动发现并拉起子进程（无需重启网关）
3. **验证闭环**（面板第七页「自定义模型」→「插件式供应商」标签）：
   - 列表出现该插件 → 状态徽标
     - **待配置**：缺私有配置，点「编辑」填好 `provider_private_configs` 后自动转运行中
     - **运行中**：就绪，模型已注册
     - **异常**：鼠标悬停看最近错误，或点「重新扫描」重试
   - 「运行中」后调 `/v1/models` 确认模型出现：`GET http://127.0.0.1:<网关端口>/v1/models`
     应包含 `{id}/` 前缀的模型（如 `loomy/qwen3.8-max`）
   - 任选一个模型发一条对话（可流式）验证链路通

> 不依赖固定端口/固定目录名：插件 id 由目录名决定，复制后即生效；删除 = 面板删除（二次确认）或手动移走整个文件夹。

### provider.json 契约（顶层保留键）

```json
{
  "id": "loomy",
  "name": "LOOMy 讯飞",
  "version": "1.4.0",
  "api_version": 1,
  "entry": "loomy-provider.exe",
  "provider_private_configs": {
    "session": "<14天有效的session token>",
    "xfyun_access_key_id": "..."
  }
}
```

| 字段 | 说明 |
|---|---|
| `id` | 与目录名一致；模型前缀来源；编辑页面只读 |
| `name` / `version` | 展示名 / 版本号 |
| `api_version` | 契约版本；主进程不兼容则拒绝加载并在面板告警 |
| `entry` | 相对本目录的可执行文件名；必须指向目录内实际存在的文件。**平台相关**：Windows 为 `xx.exe`，Linux/macOS 为无后缀 ELF/Mach-O——跨平台时需用对应平台编译产物并改此字段，否则拒绝加载 |
| `provider_private_configs` | **仅供应商自己读**的私有配置大对象，主进程整体不解析、不记录、不写日志 |

### 复制即用（同快速接入第一步）

把供应商整个文件夹（含 `provider.json` 与 exe）复制进 `<安装目录>/providers/` 即完成接入；
余下流程见上方「快速接入三步」。

### 待配置状态

插件缺私有配置时（如 loomy 缺 `session`）会报告「待配置」：状态徽标显示 **待配置**，模型
不注册进聚合器。此时**编辑 provider.json 填好 `provider_private_configs`** 即可——插件自己
监视文件变更（3 秒），配置齐后自动转「运行中」，模型自动进目录。

### 操作：编辑 / 启停 / 删除

- **编辑**：弹层展示 `provider.json` 全文 JSON，保存原子写回；`id` / `entry` 只读保护，
  **JSON 非法拒绝保存**，私有配置内部零校验
- **启停**：开关停用 = 停进程 + 模型移出目录（**不删文件**）；再启用 = 拉起重登目录
- **删除**：二次确认后**停进程 + 删除整个 `providers/<id>/` 目录**（不可恢复）

> 插件崩溃会自动指数退避重启（1s→60s 封顶），崩溃期间模型暂时移出目录，恢复后自动回来；
> 主进程被强杀（如 taskkill /F）后残留的插件子进程，可在设置页「残留进程清理」中回收。

### 安全提醒

复制供应商可执行程序（exe / ELF / Mach-O）进 `providers/` 等价于允许该程序以你的用户权限运行任意代码（同安装软件）。
仅从可信渠道获取插件；插件仅监听 127.0.0.1 并用一次性令牌鉴权，`provider.json` 中的密钥
属本机敏感文件。

### 插件合规要求（供应商必须满足）

拿到一个插件时，可对照以下要点判断它是否合规（完整版见
[PLUGIN-PROVIDERS.md](PLUGIN-PROVIDERS.md) §十「插件合规约束规范」，含插件作者自检清单）：

**必须提供**：
- `provider.json` 五个顶层保留键齐全：`id`（与目录名一致）/ `name` / `version` /
  `api_version`（当前为 1）/ `entry`（指向目录内真实存在的可执行文件——Windows 为 `.exe`，Linux/macOS 为无后缀 ELF/Mach-O）
- 两个 OpenAI 兼容端点：`GET /v1/models`（拉模型目录）与 `POST /v1/chat/completions`
  （对话，`stream:true` 支持 SSE 流式）
- 启动时按宿主契约工作：识别 `--provider-serve` 参数与 `PROVIDER_DIR` / `PROVIDER_CONFIG` /
  `PLUGIN_AUTH_TOKEN` 三个环境变量；stdout 只输出一行行 JSON 状态消息（ready / need_config /
  fatal），不打印其它横幅
- 就绪行 `ready` 必须**原样回显**宿主注入的令牌（`auth` 字段），宿主校验不一致会拒绝

**必须不做**：
- 不监听除 127.0.0.1 以外的地址
- 不在插件目录之外写文件（运行数据放自己的 `data/` 子目录）
- 不自起守护进程（崩溃由主进程自动指数退避重启，1s→60s 封顶）

**违反后果**：`id` 与目录名不一致、`api_version` 不兼容、`entry` 缺失 → 主进程**拒绝加载**并在
面板显示「异常 + 具体错误」，不会影响其它插件的运行。

### 插件式供应商 · 常见问题

**插件一直显示「待配置」，填了配置也不转运行中？**
插件自己监视 `provider.json` 变更（3 秒轮询），配置齐后自动补打就绪行转运行中——点列表上的
「重新扫描」或稍等 3 秒。仍不转：确认 `provider_private_configs` 里的关键字段（如 `session`）
非空且正确；看「异常」状态的最近错误文案。

**『运行中』但 `/v1/models` 里没有「loomy/」前缀模型？**
等 1~2 秒让聚合目录刷新；确认状态徽标仍是运行中（若中途崩溃会自动退避重启、模型暂时移出，
恢复后自动回来）；网关用管理端口的独立网关地址（48280 等），不是管理页端口本身。

**provider.json 用记事本编辑后插件报解析错误？**
Windows 记事本/PowerShell 5.1 保存会写 UTF-8 BOM，Go 的 JSON 解析不认。插件读侧已做 BOM
容错（自动剥除）；若仍异常，用 VS Code 等编辑器「另存为 UTF-8 无 BOM」重存一次即可。

**插件和「用户自定义供应商」重名（同 id）会怎样？**
两者共用同一模型前缀命名空间，同 id 会出现两个同 ID vendor、路由语义不确定——属配置错误，
文档明确禁止撞名（见 [PLUGIN-PROVIDERS.md](PLUGIN-PROVIDERS.md) §3.4）。

## FAQ

**Key 安全吗？**
Key 明文存于本机 `config.json`，仅回传给已鉴权的管理面板（单用户/内网定位）；调用方（你的 Claude Code 等）不需要也无法读到 Key。

**为什么我的实例 `/v1/models` 看不到自定义模型？**
三种可能：① 实例进程是旧版 exe 启动的（升级后需重启一次实例）；② 刚保存完，等 1~2 秒让子进程热重载；③ 源被停用（列表页灰色「已停用」徽标）。

**编辑时 Key 框是空的吗？**
不是。编辑会回填全部已存 Key（密码掩码显示），直接点「测试并获取模型」即可；整体留空保存 = 保留原 Key。

**什么时候开「走节点池代理」？**（完整出站链路说明见 [ROUTING.md 出站链路一节](ROUTING.md)——注意：请求需打网关或实例端口才会真正走节点，管理端口无池恒直连）
上游对地区有限制（如部分供应商拒绝中国大陆直连）时开启，出站改走该实例的代理节点；默认直连（延迟更低）。开启前确认对应出口链路本身可达——指向一个没在监听的本地代理会得到 `socks5 connect ... refused`（502 upstream error）。

**接入 NVIDIA（integrate.api.nvidia.com）注意什么？**
① 协议选 **OpenAI 兼容**（NVIDIA 不支持 Responses 端点）；② 模型 ID 含斜杠（如 `deepseek-ai/deepseek-v4-flash-0731`），配上源前缀即 `nvidia/deepseek-ai/...`，属正常现象；③ 建议用「暴露白名单」只放出需要的几个。

**自定义模型会计入统计和日志吗？**
会。token 用量按模型（带前缀）统计、调用日志完整记录，与内建源一致。
