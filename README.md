# opencode2api 管理器

> ⚠️ **仅供学习参考，非授权禁止商用**
> 本项目仅用于个人学习、研究与技术交流。**允许**非商业目的的学习、修改与传播分享；**禁止**任何形式的商业用途（收费服务、盈利产品、打包转售、变相收费等）。详见 [LICENSE](LICENSE)。使用即视为同意许可条款。
## 致谢

感谢[Sujinxin123](https://github.com/Sujinxin123)——本项目 v1.0.0 的「统一网关、免费额度实测健康检查、代理池健康检查、配置热更新、模型必填、同模型重试、批量并行启停」等核心能力均移植自该项目，是本次合并的基石。

特别感谢[FYHC1](https://github.com/FYHC1)——v1.0.2 修复了两个关键问题：sing-box 生成配置含非法 `transport {type:tcp}` 导致扫描时 sing-box 启动即退出，以及统一网关进程工作目录错位导致统计界面读不到网关流量（含按节点拆分 token 统计明细）。这两项修复直接消除了日常使用中的两个「硬伤」：节点扫描后 sing-box 不再一启动就崩，统一网关的 token 消耗在统计界面实时可见、并能按节点下钻到用量明细——每个节点的流量消耗一目了然。

特别感谢[383827453-max](https://github.com/383827453-max)——发现了订阅/Clash 解析中 WS `Host` 头丢失导致部分节点 **403 无法连接**的深坑，并完整修复：VLESS 订阅 `host` 参数不再被误当 `path`，YAML / mihomo API / 订阅三种解析路径统一带出 Host 头，连老式扁平 `ws-headers` 写法也兼容。这直接解决了靠前置代理节点"看着能用、一连接就 403"的困扰，让大量节点真正可连。

- Go 代理核心源于 [`6Kmfi6HP/opencode2api`](https://github.com/6Kmfi6HP/opencode2api)
- 前端设计样式参考 Windsurf Account Manager



本地**多实例代理管理器**，支持 Windows / Linux / macOS 桌面（Tauri 2 壳）与 **Docker / Headless Web**（同一 Go core）多端部署。每个"实例" = 一个 opencode2api 代理进程 + 一个 sing-box 出口，绑定不同代理节点，把 OpenAI / Anthropic / Responses 风格的请求转发到 OpenCode 上游，并可通过多实例 × 多节点分散请求、绕过按 IP 的频率限制。

除内建免费源外，还支持**自定义模型源**：用你自己的 API Key 接入任意第三方供应商（GLM / DeepSeek / Kimi / OpenRouter / OpenAI / Anthropic / Gemini 等，四种上游协议），多 Key 轮询/错误转移，与免费源并列聚合在同一网关下——详见 [自定义模型源指南](docs/CUSTOM-MODELS.md)。

UI 参照 Windsurf Account Manager 的浅色官网风格：无边框窗口 + 自定义标题栏 + 侧边栏七页（独享 / 实例池 / 节点池 / **自定义模型** / 统计 / 日志 / 设置），关闭窗口最小化到托盘、实例继续运行；headless 端同一七页 UI 经浏览器访问。

> 本项目不是 OpenAI、Anthropic 或 OpenCode 的官方项目。请遵守上游服务条款，并只在你有权限的环境中使用。

## 效果图

![opencode2api 管理器界面](docs/images/screenshot.png)

## 功能

- **独享实例**：增/删/启/停/测试，批量操作（启动/停止/释放，按勾选执行）；API 地址一键复制；链路质量列 + 探测开关
- **实例池**：入池实例聚合到统一网关（路由模式 smart·failover·round_robin），网关地址与密钥一键复制（密钥立即生效）；批量启动/停止/测试/释放（勾选驱动）；一键重启仅作用于运行中实例
- **节点池**：一键扫描全部节点（经 Clash 外部控制 + 本地 Verge profiles），按分组展示，结果分类（ok / config / socks / tls / upstream / timeout / other）与延迟；勾选节点可直接【入池】或【设为独享】批量添加（节点行点击选中、分组行点击展开收起）
- **多代理节点**：每实例自动生成 sing-box 配置走所选节点（trojan / vless / vmess / shadowsocks / ws），opencode2api 的 SOCKS5 指向 sing-box
- **Clash 集成**：配置 Clash 外部控制地址与密钥即可拉取节点；也可读取 Clash Verge 本地 profiles 目录（仅桌面端展示，headless 端隐藏）
- **订阅管理**：支持添加多条订阅源，每条独立配置自动拉取间隔与导入目标（独享/进池/仅节点池）；自动识别 Clash YAML / V2Ray base64 / 明文链接三种格式，容错解码（URL-safe 变体/缺 padding/含换行均可）、节点名 percent-decode（中文/emoji）、公告伪节点过滤、重名去重、IPv6 主机，解析能力对齐 mihomo/v2rayN 等主流客户端
- **性能模式**：链路级主动探活（质量分 0~100 / healthy·degraded·flaky·down）+ 质量加权路由 + 熔断自动恢复 + 请求级竞速（并行发最优 2 节点取快）——坏节点自动剔除、恢复自动回归，全程无感；单节点时自动退化直连。因对话卡顿而生，一篇想说清楚的[性能模式说明](docs/PERFORMANCE-MODE.md)
- **自定义模型源**：自带 API Key 接入第三方供应商（**OpenAI 兼容 / Anthropic / OpenAI Responses / Gemini** 四协议，多源并存），多 Key 池支持**轮询 / 错误转移**调度——429 自动冷却、401/403 自动禁用换 Key；保存即热生效（含已运行实例，无需重启）、模型清单磁盘缓存（重启即得）；模型带 `源ID/` 前缀进入统一 `/v1/models`，统计/日志/失败切换全链路复用，详见 [指南](docs/CUSTOM-MODELS.md)
- **Token 统计**：按实例聚合用量，支持按节点下钻明细与**按天查看**；重置统计（可清除已删除节点历史）
- **调用日志**：全流程日志（成功/失败/切换/超时），按天筛选、时段/节点分析、一键清空；直连请求显示「直连」
- **残留进程清理**：一键探测/勾选/清除占进程的孤儿节点与探针残留（运行中的实例/网关自动跳过）
- **触摸保活**：系统托盘常驻，关闭窗口实例继续代理
- **设置**：Clash 外部控制（桌面端）、开机自启、残留进程清理、关于/退出；实例池/节点池各自页面内带「设置」齿轮（性能模式参数、并发、网关超时切换、订阅刷新）

## 用法

1. 启动 `opencode2api-manager.exe`（首次运行自动在 exe 旁生成 `bin/` 目录，内含 opencode2api 与 sing-box 子程序）
2. **节点池**页 →「订阅导入」添加订阅源（或设置 Clash 外部控制后扫描）→ 拉取节点 → 勾选可用 →【入池】聚合到统一网关 或【设为独享】
3. **自定义模型**页（可选）→「添加模型源」→ 填 API 地址 / 协议 / Key（可多 Key）→「测试并获取模型」→ 保存即热生效
4. **独享 / 实例池**页 →「启动」→ 用 `http://127.0.0.1:{实例端口}/v1`（独享）或统一网关地址（入池）作为 API 地址

## Linux / Headless 部署

- **无头模式（Headless）**：同一 Go core 二进制以管理器方式运行即完整 Web 服务（默认监听 `:<port>` 全接口、托管前端 `dist/`、纯浏览器管理），两种启动途径：
  - **裸 core**：直接运行 `./opencode2api -port 40000 -config config.json`（服务器 / Docker 场景；管理鉴权默认关闭——需要开启时加 `-password <密码>`，管理 API 含启停实例等高权限操作，仅本机访问加 `-listen 127.0.0.1`，公网部署务必配合反向代理限制来源），见 [部署文档](docs/DEPLOYMENT.md)。
  - **桌面壳**：安装桌面版后 `opencode2api --headless [-port 40000] [-listen 127.0.0.1]`——壳释放内嵌组件并拉起 core，无窗口运行，适合 SSH / 无图形会话；退出自动清理网关与实例，见 [Linux 开发与构建指南](docx/LINUX-DEVELOPMENT.md)。
- **桌面（Linux）**：安装 .deb / AppImage 即可；桌面模式内置本地 HTTP 服务，前端经它取数，行为与 Windows 版一致。
- **deb 自动注册 systemd 服务**：`.deb` 安装后自动注册并启用 `opencode2api` 服务（`systemctl status opencode2api` 查看）。配置文件为 `/etc/opencode2api/manager.env`（WebUI 端口默认 `60000`、监听 `127.0.0.1`、数据目录 `/var/lib/opencode2api`、统一网关密钥默认 `sk-unified-local`）。**修改该文件后必须执行 `sudo systemctl daemon-reload && sudo systemctl restart opencode2api` 才生效**（安装 deb 时终端会输出此提示）；端口/网关密钥也可以在 WebUI「统一网关」卡片修改（写入 `config.json`，保存即生效，无需重启）。
- **Docker（服务器）**：仓库根目录 `docker compose up -d --build` 即起完整管理器（含七页前端与 sing-box 出口），管理面板 `http://127.0.0.1:40000`，见 [部署文档](docs/DEPLOYMENT.md)。
- 数据目录与配置：`OPCODE2API_DATA_DIR` 隔离数据；`OPCODE2API_MANAGER_PORT` 覆盖管理端口；`config.json` 支持网关端口/密钥（统一网关密钥默认 `sk-unified-local`，也可用 `OPCODE2API_GATEWAY_KEY` 环境变量覆盖）、订阅、日志过滤等配置项。

## macOS 部署

- 安装包：CI 产出 `.dmg`（内含 `.app`）。
- **Gatekeeper**：当前未签名/未公证，首次打开需在访达右键 App →「打开」，或在系统设置 →「隐私与安全性」点「仍要打开」。
- 开机自启走 LaunchAgent（`~/Library/LaunchAgents/opencode2api-<环境>.plist`），端口清理用 `lsof`，行为与 Windows 版一致。
- 签名/公证接入属后续迭代（需 Apple Developer 账号），不影响本地使用。

## 轻量化原则

本项目保持轻量、克制的设计取向：

- **少依赖**：不引入图表库、状态管理库等重组件；分析/可视化用纯 CSS 实现，新功能优先用现有技术栈（Tauri command + React + Tailwind）完成。
- **按需添加**：只加有实际使用价值的功能，不为「看起来丰富」堆功能；功能取舍以使用场景为准。
- **体积敏感**：UI 与运行时保持精简，避免无意义的依赖膨胀拖慢启动、增大打包体积。

## 常见问题

使用中遇到问题？先看 [常见问题（FAQ）](docs/FAQ.md) —— 包含 `max_tokens` 超限报错、Token 统计疑问等。

## 构建与打包

依赖：Node.js ≥ 18、Rust（stable-x86\_64-pc-windows-msvc）、Windows 需要 MSVC Build Tools + Windows SDK。`bin/` 下的两个内嵌 exe 不入库，本地构建前需自行准备，见下文「内嵌二进制（bin/）」。

### 便携测试包（一条命令，Windows）

```powershell
powershell -ExecutionPolicy Bypass -File scripts/prepare-portable.ps1
```

自动完成：编译最新 Go core → Rust 壳（内嵌 core + sing-box）→ 组装 `portable-out/`（exe + WebView2Loader.dll + portable.txt + 前端 dist）→ 校验。生成的 portable 包走**独立端口槽位与数据目录**，与正式版零冲突，适合日常测试（详见 [测试指南](docs/TESTING.md)）。

### Linux 构建（本地 / WSL）

Linux 桌面产物（.deb）需在 Linux 环境构建（Windows 可用 WSL），核心两步：

1. 交叉编译 Go core 与下载 sing-box（无扩展名，见下文「内嵌二进制」）：`GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=v1.5.3" -o bin/opencode2api .`，`./scripts/fetch-singbox.sh linux-amd64`
2. WSL 内 `npx tauri build --bundles deb` 产出 `linux-out/opencode2api_<version>_amd64.deb`；Debian/Ubuntu 用 `sudo apt install ./opencode2api_<version>_amd64.deb` 安装（deb 自动注册 systemd 服务 `opencode2api` 并启用），`opencode2api --headless` 可无桌面运行

细节（平台二进制解析 / 可写目录回退 / 前端资源复制 / 组件内嵌选择 / 构建与验证 / 常见问题）见 [docx/LINUX-DEVELOPMENT.md](docx/LINUX-DEVELOPMENT.md)。

### 正式产物（GitHub Actions）

三平台正式产物由 CI 矩阵产出（Windows NSIS / Linux deb+AppImage / macOS dmg）：

- **push main 且提交信息含大写 `CI`** → 构建（产物为 Actions artifacts）
- **push `v*` tag**（如 `v1.3.2`）→ 构建 + **自动发布 GitHub Release**（含三平台附件与自动 Release Notes）

### 内嵌二进制（bin/）

`bin/` 被 `.gitignore` 忽略，`opencode2api.exe` 与 `sing-box.exe` 均不入库，本地构建前需自行准备：

- `opencode2api`（Windows 为 `.exe`）：由本仓库 Go 源码构建（`go build -trimpath -ldflags "-s -w" -o bin/opencode2api .`）
- `sing-box`（Windows 为 `.exe`）：一键脚本 `./scripts/fetch-singbox.sh` 从 [sing-box 官方 Release](https://github.com/SagerNet/sing-box/releases) 下载（默认 v1.13.16，`SINGBOX_VERSION` 可覆盖），自动按宿主平台放到 `bin/`；CI 由 workflow 自动准备

远程构建无需手动准备：GitHub Actions 会自动构建 Go 核心、下载 sing-box 并完成打包（见 `.github/workflows/build-release.yml`）。

开发热更：

```bash
npm install
npm run tauri:dev
```

## 数据目录

运行时数据（配置文件、实例清单、日志）存 `%APPDATA%\opencode2api-manager\`（正式版）：

| 路径               | 说明                                  |
| ---------------- | ----------------------------------- |
| `config.json`    | 应用配置（Clash 外部控制、统一网关端口/密钥）           |
| `instances.json` | 实例清单                                |
| `runtime\`       | 各实例的运行目录与日志                         |
| （exe 旁）`bin\`    | 释放的 opencode2api.exe / sing-box.exe |

**多环境隔离**：正式版 / dev（tauri dev）/ 便携测试（exe 旁 `portable.txt`）/ Docker 各自独立数据目录与**端口槽位**（正式 40000 起、dev 44100 起、portable 48200 起、Docker 可映射）；可用 `OPCODE2API_DATA_DIR`、`OPCODE2API_MANAGER_PORT`、`OPCODE2API_INSTANCE_BASE_PORT` 环境变量进一步隔离。


## 架构

```
┌──────────────────────────────────────────────────┐
│  Tauri 2 前端（React + Tailwind）                 │
│  独享/实例池/节点池/自定义模型/统计/日志/设置       │
└──────────────────┬───────────────────────────────┘
                   │ http://127.0.0.1:<port>/api/admin/*
┌──────────────────▼───────────────────────────────┐
│  Go core 管理器（core/manager + main 包）          │
│  实例/网关/节点/扫描/统计/日志/配置 + 协议转换       │
│  vendors/opencode · windsurf · custom（多厂商）    │
└──────────────────┬───────────────────────────────┘
                   │ 子进程管理
┌──────────────────▼──────────────────────────┐
│  实例 = opencode2api.exe (Go) + sing-box.exe │
│  用户 → :端口/v1 → opencode2api → sing-box → 节点│
└─────────────────────────────────────────────┘
```

- **Tauri 壳**：只做窗口/托盘/内嵌二进制释放/拉起 core 管理器（`src-tauri/src/lib.rs`），管理职责全部在 Go core；headless 端（Docker/Web）不依赖壳，同一 core 直接监听 HTTP 提供完整管理 UI
- **Go core**：一份实现服务所有端（桌面 exe / Web 浏览器 / Docker），经 `/api/admin/*` HTTP 暴露；协议转换（OpenAI/Anthropic/Responses）与厂商契约在 main 包 + `core/contract`
- **多厂商**：`vendors/opencode`（第一厂商）、`vendors/windsurf`（账号池型：无号自动注册/额度预注册/24h 冷却/无感换号）、`vendors/custom`（自定义模型源：自带 Key 接入第三方供应商，四协议 + 多 Key 池）
- **环境隔离**：正式版 / dev（tauri dev）/ 便携测试（portable.txt）/ Docker 各自独立数据目录与**端口槽位**
  （40000 起每环境一段；sing-box = 实例端口 +2000 紧挨），互不干扰，新开环境无需手动配端口
- **端口配置化**：来源优先级 环境变量 > config.json（gateway_port/instance_base_port/probe_*_port）> 编译默认

## 目录结构

```
src/                      # React 前端（TitleBar + 侧边栏 + 七页）
src/lib/api.ts            # 统一 HTTP 对接层（/api/admin/*）
src-tauri/src/            # Tauri 薄壳（窗口/托盘/自启/内嵌释放）
  embed.rs                # 内嵌二进制释放（按内容哈希校验）
  job.rs                  # Windows Job Object（防孤儿进程）
  lib.rs                  # 入口 + 端口注入 + 拉起 core
core/                     # Go 核心层
  contract/               # 厂商契约
  aggregator/             # 多厂商模型聚合
  router/                 # 模型到厂商分发 + failover
  manager/                # 管理域（实例/网关/节点/扫描/统计/日志/配置）
vendors/                  # 厂商层（一厂商 = 一文件夹，新增供应商零 core 改动）
  opencode/               # 第一厂商（免费源）
  windsurf/               # 第二厂商（账号池型免费源）
  custom/                 # 自定义模型源（四协议 + 多 Key 池 + 目录缓存）
bin/                      # 内嵌子程序源（opencode2api.exe / sing-box.exe）
docs/                     # 部署/自定义模型/性能模式/TESTING/FAQ 等文档
docx/                     # 平台开发专题文档（Linux 开发与构建指南等）
```
