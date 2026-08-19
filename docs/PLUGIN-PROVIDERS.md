# 插件式供应商（Plugin Providers）设计文档

> 状态：设计定稿（2026-08-19）。R 系列（供应商配置化 + 插件）的**唯一事实来源**；
> `docs/MASTER-PLAN.md` 仅存摘要与状态矩阵。
> 遵循 AGENTS.md 纪律：每阶段 = 功能开发 + 测试 + 验证；厂商特有信息进 `vendors/` 或配置，不写死在 core。

---

## 一、目标

把"供应商"做成**即插即用的插件**：供应商以**独立可执行文件 + 一个 `provider.json`** 的形式放入安装目录
`providers/<id>/` 下，主进程自动发现、拉起、注册进聚合器（模型目录 / failover / 统计 / 日志全部复用统一链路）。

用户只需：**复制供应商文件夹 → 填配置 → 使用**。全程不动主进程配置、不重编译、不重启网关。

### 背景动机

- 现有「自定义模型」页（`vendors/custom`）支持用户填 base_url + api_key 接入 OpenAI/Anthropic/Gemini/Responses
  兼容上游，但**无法表达上游的特殊鉴权/协议怪癖**（如 LOOMy 需要 `traceparent` 头、`/models` 只认 `token` 头、
  session 14 天过期需续期）。
- 需要一种方式让"懂的供应商"自带适配逻辑：供应商自己处理鉴权/协议/session，主进程只做透明桥接。

### 为什么是"侧车进程 + HTTP 契约"而不是 Go plugin

| | Go plugin (.so/.dll) | 侧车进程 + HTTP 契约 |
|---|---|---|
| Windows 支持 | ❌ 不支持 | ✅ 原生支持 |
| 供应商语言 | 必须 Go，且与宿主 Go 版本/依赖树完全一致 | **任意语言**（能起 HTTP 即可） |
| 故障隔离 | 崩溃拖死宿主 | 崩溃自重启，不影响网关 |
| 更新 | 重新编译对版本 | 替换 exe 即完成 |
| 项目先例 | 无 | sing-box.exe 外挂进程管理、网关实例子进程 |

## 二、目录与文件结构

```
<安装目录>/
├── opencode2api.exe          # 主网关
└── providers/                # ★ 供应商目录（主进程扫描）
    └── loomy/
        ├── provider.json     # 契约 + 私有配置（单文件 all-in-one）
        ├── loomy-provider.exe   # 独立编译的侧车（可执行文件）
        └── data/             # （可选）供应商运行数据目录，主进程不扫描不管理
```

- 主进程扫描 `providers/*/provider.json`，只认这个固定文件名。
- 目录名 = 供应商 id（`providers/loomy/` → id `loomy`）。
- `data/` 子目录为供应商自用运行数据（缓存/db），主进程不扫描、不进模型目录、删除时随目录一起删。

## 三、provider.json 契约

### 3.1 顶层结构

```json
{
  "id": "loomy",
  "name": "LOOMy 讯飞",
  "version": "1.4.0",
  "api_version": 1,
  "entry": "loomy-provider.exe",

  "provider_private_configs": {
    "session": "<14天有效的session token>",
    "xfyun_access_key_id": "...",
    "xfyun_access_key_secret": "...",
    "xfyun_app_id": "...",
    "points_alert_threshold": 1000
  }
}
```

### 3.2 字段规则

| 字段 | 谁读 | 说明 |
|---|---|---|
| `id` | 主进程 | 与目录名一致；模型目录前缀；**编辑页面只读** |
| `name` | 主进程 | 展示名 |
| `version` | 主进程 | 供应商版本号（展示） |
| `api_version` | 主进程 | 契约版本；主进程不兼容则拒绝加载并面板告警（不静默坏掉） |
| `entry` | 主进程 | 相对本目录的可执行文件名；**必须指向目录内实际存在的文件** |
| `provider_private_configs` | **仅供应商** | 供应商私有配置大对象，主进程整体不解析、不记录、不写日志 |

### 3.3 边界

- 主进程只读五个顶层保留键（`id`/`name`/`version`/`api_version`/`entry`）。
- `provider_private_configs` 内部结构**零校验**，供应商自行负责；主进程通过结构体解析天然忽略未知键（Go
  `json.Unmarshal` 默认行为），密钥从结构上就进不了主进程内存。
- 主进程**只读**此文件，**绝不回写**（读-改-写会冲掉供应商私有键）；写文件的只有供应商自己（自举模板、
  配置热更新）——唯一的例外是面板「编辑」保存（见 §六），由用户显式触发的全量写回。
- 插件读写自身 `provider.json` 时应**容忍 UTF-8 BOM**（Windows 记事本/PowerShell 5.1 写入常带 BOM，
  Go `json` 包不认会解析失败；若按"损坏即重建模板"处理会冲掉用户配置）——读侧先剥 BOM。
- 「就绪」的最小前置由供应商自定（如 loomy 只要求 `provider_private_configs.session` 非空），
  模板中可一并列出其余可选字段供填写。

### 3.4 已知约束：插件 id 与自定义源 id 撞名

插件 id（`providers/` 目录名）与 `config.json` 中 `providers: [{id: ..., type: "custom"}]`
自定义源的 id 属于**同一命名空间**——两者最终都注册进聚合器的 vendor 集合，模型前缀
（`{id}/` 前缀 + 模型目录 + failover + 路由别名）全链路共享同一 id 空间。

若用户同时配置 `providers: [{id:"loomy",type:"custom"}]` 与 running 插件 `loomy`，
聚合器会出现**两个同 ID vendor**：模型前缀同为 `loomy/`，failover/别名等路由语义落到哪个
vendor 不确定，属**用户配置错误**。文档明确禁止撞名——插件接入后，请勿再用相同 id 配置
自定义源（或反之）。插件管理器与自定义源配置是两条独立装配链路，主进程不做跨链路重名
校验（以免误伤其它合法部署），错误后果由用户自行承担。

## 四、线协议（主进程 ⇄ 供应商子进程）

### 4.1 启动契约

主进程 spawn 供应商 exe：

```
cwd          = providers/<id>/
env PROVIDER_DIR    = providers/<id>/ 绝对路径
env PROVIDER_CONFIG = providers/<id>/provider.json 绝对路径
argv         = --provider-serve --port 0   （port 0 = OS 分配随机端口）
```

子进程启动 HTTP 服务（仅绑定 127.0.0.1），并在 **stdout 打一行 JSON 就绪消息**：

```
{"state":"ready","port":54321,"auth":"<一次性令牌>","id":"loomy","version":"1.4.0"}
{"state":"need_config","hint":"请填写 provider_private_configs 中的讯飞凭据"}
{"state":"fatal","error":"..."}
```

- `auth` 为一次性令牌（主进程生成，经 argv/env 传入，子进程原样打印回来用于校验），防其它本地进程冒充。
- `need_config` 状态：子进程自举生成默认配置模板后报告，主进程面板标记「待配置」，不注册厂商。
- **`need_config` 后子进程不退出**——它周期重读配置（3s），配置齐后**同一 stdout 后续会补打 `ready` 行**。
  宿主必须持续消费 stdout 行流（按行解析 JSON，勿按文本匹配字段顺序），否则会漏掉转 ready 的就绪行。
- 主进程对就绪行有超时（如 15s），超时按启动失败处理（指数退避重启）。

### 4.2 HTTP 契约（OpenAI 兼容子集）

| 端点 | 方法 | 说明 |
|---|---|---|
| `/v1/models` | GET | 模型目录（OpenAI 格式 `{"data":[{"id":...}]}`） |
| `/v1/chat/completions` | POST | 对话；`stream:true` 返回 SSE |

- 请求带 `Authorization: Bearer <auth 令牌>`（主进程注入），子进程校验。
- 响应/SSE 原样透传（对齐 `vendors/custom` 的 openaiProto 语义）。
- 上游特殊需求（traceparent、token 头、session 续期等）**全部由子进程自行处理**，主进程不感知。

### 4.3 生命周期

| 事件 | 主进程行为 |
|---|---|
| 发现新供应商 | spawn → 等就绪行 → 注册厂商（走 `rebuildVendors()` 热重建） |
| 子进程崩溃 | 指数退避重启（1s/2s/4s…封顶 60s），面板显示异常 + 最近错误；**崩溃即从聚合目录移出该插件模型，重启就绪后自动回到目录**（running 集合变化经 OnChange → rebuildVendors 单点合并） |
| `enabled=false` | 停进程 + 注销厂商（模型移出 /v1/models），**不删文件** |
| 删除 | 停进程 + 整目录删除（前端二次确认） |
| 主进程退出 | 统一 kill 全部子进程（复用 orphan/process 管理逻辑） |
| 配置文件变更 | 供应商自 watch 自己的 `provider.json`（3s ticker，仿 `startConfigWatcher`），自行重载；entry/api_version 变更才由主进程重启子进程 |

## 五、配置自举（首次安装体验）

```
用户复制供应商文件夹进 providers/
   → 主进程发现清单 → spawn exe
   → exe 发现 provider_private_configs 缺关键字段
   → 自举写入默认模板（含字段说明）→ stdout: {"state":"need_config"}
   → 面板显示「待配置」，不注册厂商
   → 用户编辑 provider.json → 子进程 watch 到变更 → 重载 → ready → 注册厂商
```

## 六、UI 设计（复用第七页「自定义模型」）

- **共用同一页面**（`src/pages/CustomModelsPage.tsx`），顶部双标签：
  - **插件式供应商**（`providers/` 目录扫描发现）
  - **用户自定义供应商**（现有 `vendors/custom` 逻辑，行为不变）
- 插件式 tab：状态筛选下拉（全部 / 运行中 / 待配置 / 已停用 / 异常）+ 「重新扫描」按钮。
- 列表项：名称 / 状态徽标 / 版本 / 模型数 / provider.json 路径 / 活跃时间 + 启停开关 + 编辑 + 删除。
- **编辑弹层**：展示 provider.json 完整内容（JSON 编辑），保存 = 原子写回文件；`id`/`entry` 只读保护，
  JSON 非法拒绝保存；`provider_private_configs` 内部零校验。
- **删除二次确认**：明确告知将停止进程并删除整个 `providers/<id>/` 目录（不可恢复）。
- UI 纪律：与七页 UI 一致，不另起风格。

## 七、管理 API

| 端点 | 方法 | 说明 |
|---|---|---|
| `/api/admin/plugins` | GET | 扫描结果列表（id/name/version/状态/模型数/路径/provider.json 全文） |
| `/api/admin/plugins/{id}/config` | POST | 保存编辑后的 provider.json（校验后原子写盘） |
| `/api/admin/plugins/{id}/toggle` | POST | 启停（停进程 or 拉起+注册） |
| `/api/admin/plugins/{id}` | DELETE | 停进程 + 整目录删除 |
| `/api/admin/plugins/rescan` | POST | 手动重扫 `providers/` |

统一走 `requireAuth` 鉴权（与现有 `/api/admin/custom-providers/*` 同款）。

## 八、实施拆分（R1→R5，每阶段测绿 + 审查）

| 阶段 | 内容 | 验收 |
|---|---|---|
| **R1** | 后端插件管理器：providers/ 扫描、spawn、生命周期、API 端点 | 单测（httptest + 伪子进程）：发现/就绪/need_config/崩溃重启/启停/删除 |
| **R2** | `vendors/remote` 桥接厂商（contract.Vendor 实现） | 单测：ListModels/Chat/ChatStream 桥接到子进程，复用聚合器 |
| **R3** | loomy2api `--provider-serve` 模式（首个真实插件） | 端到端：loomy2api 以插件形态接入，模型目录/对话/流式可用 |
| **R4** | 前端页面改造（双标签 + 筛选 + 编辑弹层 + 删除确认） | UI 视觉自查（.ui-shots.mjs）+ 行为验证 |
| **R5** | 端到端联调 + 全套验证收尾 | 全量 go test + 面板全流程 + README/CUSTOM-MODELS 文档同步 |

> 每阶段完成后跑 `go test -count=1 ./...` 全绿才提交（AGENTS.md 纪律）；审查不通过项写入下一阶段开头修复。

## 九、安全边界

- 信任模型：复制供应商 exe 进去 = 让该程序以用户权限运行任意代码（等价于安装软件），文档中明示。
- 子进程仅绑定 127.0.0.1，一次性令牌鉴权，防本地其它进程冒充/调用。
- 主进程不解析 `provider_private_configs`，不写日志、不统计其内容。
- `provider.json` 含密钥，属用户本机敏感文件；面板回显全文是单用户/内网定位下的既有取舍（与
  `/api/admin/custom-providers` api_keys 明文回显同款）。

## 十、插件合规约束规范（Checklist）

> 本节回答「一个合规插件**必须**提供什么、**不得**做什么、**可**依赖什么」。
> 宿主在加载/运行哪些点校验、违反的后果，均与 R1 实现（`core/manager/pluginprovider/`
> `manifest.go` + `manager.go`）逐条对齐。插件作者按本节自检，宿主按本节兜底。

### 10.1 provider.json —— 必填与校验点

| 键 | 必填 | 宿主校验（违反后果） |
|---|---|---|
| `id` | ✅ | 与目录名一致；字母数字开头、可含 `- _`、≤32 字符（不一致/非法 → **拒绝加载**） |
| `name` | 建议 | 空则回退用 id 展示 |
| `version` | 建议 | 空则展示 `-` |
| `api_version` | ✅ | 必须 = `1`（当前契约版本；不兼容 → **拒绝加载**，面板告警，不静默坏掉） |
| `entry` | ✅ | 相对本目录、不得目录穿越（`safeJoin`）；**必须指向目录内实际存在的文件**（否则拒绝加载） |
| `provider_private_configs` | 可选 | 宿主不解析、不校验、不写日志，整体不透明；插件自行负责默认值与健壮性 |

**加载期校验触发点**：目录扫描发现 / 面板编辑保存（`validateSave` 校验 id 一致 + entry 存在）/
清单修复后重读。任一处违反 → 插件不进聚合器，面板显示「异常 + 具体错误」。修复 provider.json
后自动重试（无需重启）。

### 10.2 启动契约 —— 必须遵守

1. **argv**：宿主固定按 `--provider-serve --port 0` 拉起（`--port 0` = 宿主提示绑定随机端口；
   插件实际监听端口以自身 `net.Listen("127.0.0.1:0")` 结果为准，**必须打印真实端口**）。
2. **环境变量**：`PROVIDER_DIR`（插件目录绝对路径）/ `PROVIDER_CONFIG`（provider.json 绝对路径）/
   `PLUGIN_AUTH_TOKEN`（一次性令牌）。三者缺失 → 立即 stdout 打 `{"state":"fatal",...}` 并以非 0
   退出码退出（宿主按启动失败处理，指数退避重启）。
3. **stdout 纪律**：stdout **只允许状态行**（ready/need_config/fatal 一行 JSON）；不得打印
   横幅/日志/进度（否则混淆就绪行解析）。stderr 随意（宿主丢弃，错误经状态行上报）。
4. **就绪时限**：首次状态行必须在宿主超时窗口内输出（默认 15s）；超时按启动失败处理。

### 10.3 状态机与就绪行 —— 必须遵守

| 状态 | 触发 | 必含字段 | 宿主行为 |
|---|---|---|---|
| `need_config` | 首次缺私有配置 | `hint`（中文说明缺什么） | 标记「待配置」，不注册厂商；**子进程存活等待** |
| `ready` | 配置齐 / 配置转齐 | `port`（真实监听端口）/`auth`（**原样回显** `PLUGIN_AUTH_TOKEN`）/`id`/`version` | 校验 auth 一致 + id 与目录一致 + port 合法 → 注册厂商 |
| `fatal` | 致命错误 | `error` | 停进程，指数退避重启 |

**关键约束**：
- `need_config` 后**不得退出**——插件周期重读配置（建议 3s），配置齐后**同一 stdout 补打
  `ready` 行**（宿主持续消费 stdout 行流，见 §4.1）。
- `auth` 必须**原样回显宿主注入的令牌**（伪造/篡改 → 宿主拒绝，防本地进程冒充）。
- 状态行是**行流**：宿主按行解析 JSON，字段顺序无约定，勿做文本匹配。

### 10.4 HTTP 面 —— 必须提供

| 端点 | 方法 | 要求 |
|---|---|---|
| `/v1/models` | GET | OpenAI 格式 `{"data":[{"id","..."}...]}`；空列表按失败处理（宿主视为目录不可用） |
| `/v1/chat/completions` | POST | OpenAI 格式；`stream:true` 必须返回 **SSE**（`text/event-stream`，逐块 flush） |

- **入站鉴权**：所有请求必须校验 `Authorization: Bearer <宿主令牌>`；不匹配返回 401。
- 响应/SSE 由宿主原样透传，插件负责自己上游的协议适配（traceparent/token 头/session 续期等）。
- 错误体建议用 OpenAI 错误格式 `{"error":{"message":...}}`（面板/客户端可读）。

### 10.5 生命周期与边界 —— 必须遵守

| 事项 | 约束 |
|---|---|
| 监听地址 | **仅 127.0.0.1**；绑定其它地址视为违规（信任模型失效） |
| 崩溃 | 由宿主自动指数退避重启（1s→60s 封顶）；**插件不得自起守护/自杀式循环**（避免与宿主重启叠加成抖动） |
| 配置热更新 | 仅 `provider_private_configs` 变更 → 插件自己 3s 轮询重载，**不依赖宿主重启** |
| entry/api_version 变更 | 宿主负责重启子进程（插件无需处理） |
| 运行数据 | 写插件目录内 `data/`（宿主不扫描、删除时随目录一起删）；**不得写宿主目录/系统目录** |
| 退出 | 被宿主 kill 即终止；不要拦截信号做长清理（可能被强杀） |

### 10.6 插件作者自检清单（提交前逐项打勾）

- [ ] provider.json 五键齐全、id 与目录名一致、entry 文件存在
- [ ] 能解析 `--provider-serve` + 三个环境变量
- [ ] stdout 只打状态行；首行在 15s 内；need_config 后不退出、能补打 ready
- [ ] auth 原样回显；/v1/models + /v1/chat/completions（SSE）实现且带 Bearer 校验
- [ ] 仅监听 127.0.0.1；运行数据只写 data/
- [ ] UTF-8 无 BOM 读写 provider.json（或读侧剥 BOM）
- [ ] 私有配置缺失时提供默认模板（`_hint` 字段说明）
