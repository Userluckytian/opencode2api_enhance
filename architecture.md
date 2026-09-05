# opencode2api_enhance 架构说明

> 本地**多实例代理管理器**：把 OpenCode 上游转换成 OpenAI / Anthropic / Responses 兼容 API，通过「多实例 × 多代理节点」分散请求、绕过按 IP 的频率限制。同一份 Go core 服务桌面 / Web / Docker 多端。

## 一、技术栈

| 分类 | 技术 | 说明 |
|------|------|------|
| 后端核心 | Go 1.22 | **纯标准库、零第三方依赖**（go.mod 无任何 require） |
| 桌面壳 | Tauri 2（Rust） | 薄壳：窗口 / 托盘 / 内嵌二进制释放 / 拉起 core |
| 前端 | React 19 + Vite 8 + Tailwind 4 + TypeScript 6 | 七页 UI，经 HTTP 对接 core |
| 出口代理 | sing-box | trojan / vless / vmess / shadowsocks / ws |
| 上游协议 | OpenAI / Anthropic / Responses / Gemini | 四协议转换与厂商契约 |
| 部署 | 桌面(Win/Linux/macOS) + Docker + Headless Web | 同一 Go core 通吃 |

## 二、整体架构

```
┌──────────────────────────────────────────────────┐
│  Tauri 2 前端（React + Tailwind）                 │
│  独享 / 实例池 / 节点池 / 自定义模型 / 统计 / 日志 / 设置 │
└──────────────────┬───────────────────────────────┘
                   │ http://127.0.0.1:<port>/api/admin/*
┌──────────────────▼───────────────────────────────┐
│  Go core 管理器（core/manager + main 包）          │
│  实例 / 网关 / 节点 / 扫描 / 统计 / 日志 / 配置      │
│  协议转换（OpenAI/Anthropic/Responses）+ 厂商契约   │
└──────────────────┬───────────────────────────────┘
                   │ 子进程管理
┌──────────────────▼──────────────────────────┐
│  实例 = opencode2api.exe (Go) + sing-box.exe │
│  用户 → :端口/v1 → opencode2api → sing-box → 节点 → 上游 │
└─────────────────────────────────────────────┘
```

- **Tauri 壳**只做窗口/托盘/内嵌释放/拉起 core，管理职责全部在 Go core；headless 端不依赖壳，同一 core 直接监听 HTTP 提供完整管理 UI。
- **Go core** 一份实现服务所有端，经 `/api/admin/*` HTTP 暴露。

## 三、目录结构

```
src/                      # React 前端（TitleBar + 侧边栏 + 七页）
  pages/                  # 独享/实例池/节点池/自定义模型/统计/日志/设置
  components/             # TitleBar / TaskPanel / ResultModal
  lib/api.ts              # 统一 HTTP 对接层（/api/admin/*）
src-tauri/src/            # Tauri 薄壳（窗口/托盘/自启/内嵌释放）
  embed.rs                # 内嵌二进制释放（按内容哈希校验）
  job.rs                  # Windows Job Object（防孤儿进程）
  lib.rs                  # 入口 + 端口注入 + 拉起 core
core/                     # Go 核心层
  contract/               # 厂商契约（一厂商一契约）
  aggregator/             # 多厂商模型聚合
  router/                 # 模型到厂商分发 + failover
  manager/                # 管理域（实例/网关/节点/扫描/统计/日志/配置）
    pluginprovider/       # 插件型供应商子进程管理
vendors/                  # 厂商层（一厂商 = 一文件夹，新增供应商零 core 改动）
  opencode/               # 第一厂商（免费源）
  windsurf/               # 第二厂商（账号池型免费源）
  custom/                 # 自定义模型源（四协议 + 多 Key 池 + 目录缓存）
bin/                      # 内嵌子程序源（opencode2api / sing-box，不入库）
docs/                     # 部署/自定义模型/性能模式/TESTING/FAQ 等文档
```

## 四、核心模块

| 模块 | 位置 | 职责 |
|------|------|------|
| 协议转换 | 根 main 包（`convert.go` / `responses.go` / `claude.go` / `chat_handler.go`） | OpenAI / Anthropic / Responses 三协议互转与流式处理 |
| 统一网关 | `gateway_timeout.go` + `core/manager/gateway.go` | 多实例聚合到统一入口，路由模式 smart / failover / round_robin，超时与熔断 |
| 厂商契约 | `core/contract` | 定义厂商能力接口，注册表分发 |
| 模型聚合 | `core/aggregator` | 聚合各厂商模型清单，lastGood 逐厂商保留最后成功目录 |
| 路由分发 | `core/router` | 模型 → 厂商分发 + failover |
| 管理域 | `core/manager` | 实例生命周期、节点扫描、订阅解析、SOCKS/sing-box 配置、统计、调用日志、配置热更新、残留进程清理 |
| 厂商实现 | `vendors/*` | opencode / windsurf / custom，新增供应商零 core 改动 |

## 五、请求链路

```
客户端(OpenAI/Anthropic SDK)
  → 统一网关 :<gateway_port>/v1  或  独享实例 :<instance_port>/v1
  → opencode2api(Go) 协议转换 + 路由选择实例
  → 实例内 opencode2api → SOCKS5 → sing-box 出口
  → 所选代理节点 → OpenCode 上游
```

- **独享实例**：一个 opencode2api 进程 + 一个 sing-box 出口，绑定不同节点，直接暴露 `/v1`。
- **实例池**：多实例聚合到统一网关，按路由模式分散请求；坏节点自动剔除、恢复自动回归（性能模式）。

## 六、协议转换

对外同时兼容三种风格，内部统一转换到上游：

- **OpenAI Chat Completions**（`/v1/chat/completions`）
- **Anthropic Messages**（`/v1/messages`）
- **OpenAI Responses**（`/v1/responses`，含 previous_response 状态）

转换逻辑集中在根 main 包，`core/protocol` 提供 anthropic / claude / openai / responses 的协议适配与互转。

## 七、多厂商与自定义模型源

- **免费源**：opencode（第一厂商）、windsurf（账号池型：无号自动注册 / 额度预注册 / 24h 冷却 / 无感换号）。
- **自定义模型源**（custom）：用自带 API Key 接入任意第三方供应商（GLM / DeepSeek / Kimi / OpenRouter / OpenAI / Anthropic / Gemini），支持四协议、多 Key 池轮询 / 错误转移 / 健康优先，429 冷却、401/403 冷却可配置自动恢复、连续失败熔断；保存即热生效（含已运行实例）。模型带 `源ID/` 前缀进入统一 `/v1/models`。

## 八、部署形态

| 形态 | 说明 |
|------|------|
| 桌面（Win/Linux/macOS） | Tauri 壳释放内嵌 core + sing-box，拉起管理器；关窗最小化托盘继续运行 |
| Headless Web | 同一 core 二进制以 `-port` 监听全接口，托管前端 `dist/`，纯浏览器管理 |
| Docker | `docker compose up -d --build`，含七页前端与 sing-box 出口 |
| systemd | `.deb` 安装自动注册 `opencode2api` 服务，配置在 `/etc/opencode2api/manager.env` |

## 九、环境隔离与端口

正式版 / dev（tauri dev）/ 便携测试（portable.txt）/ Docker 各自独立数据目录与**端口槽位**，互不干扰：

| 环境 | 起始端口 |
|------|---------|
| 正式版 | 40000 |
| dev | 44100 |
| portable | 48200 |
| Docker | 可映射 |

- sing-box 端口 = 实例端口 + 2000（紧挨）。
- 端口来源优先级：环境变量 > `config.json`（gateway_port / instance_base_port / probe_*_port）> 编译默认。
- 隔离变量：`OPCODE2API_DATA_DIR`、`OPCODE2API_MANAGER_PORT`、`OPCODE2API_INSTANCE_BASE_PORT`、`OPCODE2API_GATEWAY_KEY`。

## 十、数据目录

运行时数据存 `%APPDATA%\opencode2api-manager\`（正式版）：`config.json`（应用配置）、`instances.json`（实例清单）、`runtime\`（各实例运行目录与日志）、exe 旁 `bin\`（释放的子程序）。

## 十一、设计原则

- **零依赖**：Go 侧只用标准库，不引入第三方包；前端不引入图表 / 状态管理库，分析可视化用纯 CSS。
- **一份 core 多端复用**：桌面 / Web / Docker 共享同一 Go core，避免重复实现。
- **厂商可插拔**：一厂商一文件夹，新增供应商零 core 改动。
- **环境安全隔离**：多环境端口/数据目录互不冲突，测试用 httptest 随机端口，禁止占用真实服务端口。
