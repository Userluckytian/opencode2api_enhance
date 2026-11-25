# opencode2api 管理器

> ⚠️ **仅供学习参考，非授权禁止商用**
> 本项目仅用于个人学习、研究与技术交流。**允许**非商业目的的学习、修改与传播分享；**禁止**任何形式的商业用途（收费服务、盈利产品、打包转售、变相收费等）。详见 [LICENSE](LICENSE)。使用即视为同意许可条款。
## 致谢

感谢[Sujinxin123](https://github.com/Sujinxin123)——本项目 v1.0.0 的「统一网关、免费额度实测健康检查、代理池健康检查、配置热更新、模型必填、同模型重试、批量并行启停」等核心能力均移植自该项目，是本次合并的基石。

特别感谢[FYHC1](https://github.com/FYHC1)——v1.0.2 修复了两个关键问题：sing-box 生成配置含非法 `transport {type:tcp}` 导致扫描时 sing-box 启动即退出，以及统一网关进程工作目录错位导致统计界面读不到网关流量（含按节点拆分 token 统计明细）。这两项修复直接消除了日常使用中的两个「硬伤」：节点扫描后 sing-box 不再一启动就崩，统一网关的 token 消耗在统计界面实时可见、并能按节点下钻到用量明细——每个节点的流量消耗一目了然。

特别感谢[383827453-max](https://github.com/383827453-max)——发现了订阅/Clash 解析中 WS `Host` 头丢失导致部分节点 **403 无法连接**的深坑，并完整修复：VLESS 订阅 `host` 参数不再被误当 `path`，YAML / mihomo API / 订阅三种解析路径统一带出 Host 头，连老式扁平 `ws-headers` 写法也兼容。这直接解决了靠前置代理节点"看着能用、一连接就 403"的困扰，让大量节点真正可连。

- Go 代理核心源于 [`6Kmfi6HP/opencode2api`](https://github.com/6Kmfi6HP/opencode2api)
- 前端设计样式参考 Windsurf Account Manager



本地**三端口 LLM 代理管理器**，支持 Windows / Linux / macOS 桌面（Tauri 2 壳）与 **Docker / Headless Web**（同一 Go core）多端部署。40000 是管理面（不代理推理），40080 托管 OpenCode 免费层引擎，30080 托管自定义源与插件供应商；管理器负责启停、健康、配置热更新、调用日志与统计。

除内建免费源外，还支持**自定义模型源**：用你自己的 API Key 接入任意第三方供应商（GLM / DeepSeek / Kimi / OpenRouter / OpenAI / Anthropic / Gemini 等，四种上游协议），多 Key 轮询/错误转移，与免费源并列聚合在同一网关下——详见 [自定义模型源指南](docs/CUSTOM-MODELS.md)。

UI 参照 Windsurf Account Manager 的浅色官网风格：无边框窗口 + 自定义标题栏 + 侧边栏六页（**OpenCode** / 自定义模型 / 统计 / 日志 / 诊断 / 设置），关闭窗口最小化到托盘、服务继续运行；headless 端同一六页 UI 经浏览器访问。

> 本项目不是 OpenAI、Anthropic 或 OpenCode 的官方项目。请遵守上游服务条款，并只在你有权限的环境中使用。

## 效果图

![opencode2api 管理器界面](docs/images/screenshot.png)

## 引擎血统与上游（重要）

三个名字经常被混用，这里一次说清（以本仓库实际构建与分发为准，2026-11-25 核对）：

| 名字 | 与本项目的关系 | 许可证 | 是否随发行包分发 |
|------|----------------|--------|------------------|
| `wbi35772/opencode2api-win` | **现役运行时引擎**：CI 按固定提交 `1a208ac`（tag `v1.5.1-win.1`）构建 40080 引擎；该 fork 已合并上游 v1.5.1 全量，并保留 WARP / 会话亲和 / 免费通道增强 | GPL-3.0 | 是（独立进程 + 源码要约，见 `third_party/opencode2api-win/SOURCE.txt`） |
| `jasonxu114514/opencode2api` | 引擎的**上游**（血统祖先，GPL-3.0）；v1.5.1 已并入上面的 fork，本项目不再直接构建它 | GPL-3.0 | 间接（随 fork 源码包一并提供） |
| `6Kmfi6HP/opencode2api` | **本仓库**代码血脉的祖先（v0.11 前后，2026-06-04 首个提交）；现版本仅作机制调研参考 | **All rights reserved**（明确未授予复制/修改/分发/使用许可） | 否 |

结论：**不要把 40080 引擎换成 6Kmfi6HP**。原因有两条硬约束，任一条都足以否决：

1. **许可证**：它明确「未授予任何权利」，而本项目以安装包形式分发引擎二进制；GPL-3.0 至少允许随源码分发，换成它反而从「合规」变成「未授权分发」。只读它的代码/结论做调研可以，复制代码或内嵌产物不行。
2. **契约不兼容**：本项目的免费层面板与托管层依赖 v1.4.x/v1.5.x 契约——配置字段（`anonymous` / `warp` / `proxies` / `server_keys`）、`/healthz` 字段（`warp.active` / `models` / `proxies`）、匿名通道语义。6Kmfi6HP v0.15.x 的配置是另一套（`key_pool` / `model_alias` / `admin_password` / `stream_first_byte_timeout_ms`），且**没有 WARP 池**、没有上述 `/healthz` 字段。换过去不是「换上游」，而是重写 2,400 行免费层托管代码 + 1,380 行 OpenCode 页，并放弃 WARP 出口能力。

长期方向（若要摆脱 GPL 依赖）：自研一个薄免费层网关（只做协议转换 + 出口选择 + 额度账本），把外部引擎降级为可选后端；而不是换一个许可更严的第三方二进制。

> 为什么是两个 exe：管理面（40000）与免费层引擎（40080）是**两个独立程序**，分属不同许可证、不同配置契约。合并成一个二进制会把 GPL-3.0 传染到整个项目（与本仓库「保留所有权利 / 仅非商业」的许可冲突），因此保持「独立进程 + HTTP」的聚合分发方式。

## 功能

- **OpenCode 免费层（40080）**：托管 opencode2api 引擎（独立子进程 + 看门狗），面板含 WARP 出口池（活跃/冷却计数、一键重试）、WARP 注册节点池（导入 Clash 订阅 → 本地端口）、免费模型清单、接入信息（地址 + API Key 一键复制）、启停 / 重启（异步模式避免前端转圈 95 秒）。v2.0.9 起免费层只有匿名通道，不再需要 zen / go key
- **自定义模型源（30080）**：自带 API Key 接入第三方供应商（**OpenAI 兼容 / Anthropic / OpenAI Responses / Gemini** 四协议，多源并存），多 Key 池支持**轮询 / 错误转移**——429 自动冷却、401/403 自动禁用换 Key；保存即热生效、模型清单磁盘缓存；模型带 `源ID/` 前缀进入统一 `/v1/models`，详见 [指南](docs/CUSTOM-MODELS.md)
- **插件供应商**：`provider.json` 契约 + 子进程托管，统一启停、配置、日志与模型刷新，详见 [插件能力](docs/PLUGIN-CAPABILITIES.md)
- **调用日志**：供应商 JSONL 与引擎 stdout 事件合并，按天 / 供应商 / 关键词筛选；逐条显示 WARP / 匿名通道徽标、上游真实 model、错误大类
- **Token 统计**：按天 / 模型聚合用量，重置统计；诊断页另有「通道分析」（WARP 匿名 vs 其它通道的请求数、成功率与占比，以引擎记录为分母）
- **诊断**：三端口自检（管理面板 / 引擎 / 供应商服务）、引擎二进制与配置校验、通道分析、残留进程清理
- **设置**：端口与生效值、开机自启、日志保留上限、关于/退出
- **应用内更新**：桌面壳用 Tauri updater（安装包 minisign 验签）。下载前先问本机 `127.0.0.1:40099` 的清单中转，从 22 个 GitHub 公益加速源里挑实测最快的一个；探测失败或端口被占时自动回退 GitHub 直连。清单里的 `signature` 原样透传，镜像只影响下载字节，无法伪造内容。详见 `docs/UPDATE-MIRRORS.md`


## 用法

1. 启动 `opencode2api-manager.exe`（首次运行自动在 exe 旁生成 `bin/` 目录，内含 opencode2api 引擎）
2. **OpenCode** 页 → 确认 WARP 出口池（可选：导入 Clash 订阅做注册节点池）→ 「启动」→ 用 `http://127.0.0.1:40080/v1` 作为 API 地址
3. **自定义模型** 页（可选）→「添加模型源」→ 填 API 地址 / 协议 / Key（可多 Key）→「测试并获取模型」→ 保存即热生效 → 用 `http://127.0.0.1:30080/v1`
4. **统计 / 日志 / 诊断** 页观察用量、逐条调用与三端口自检

## Linux / Headless 部署

- **无头模式（Headless）**：同一 Go core 二进制以管理器方式运行即完整 Web 服务（默认监听 `:<port>` 全接口、托管前端 `dist/`、纯浏览器管理），两种启动途径：
  - **裸 core**：直接运行 `./opencode2api -port 40000 -config config.json`（服务器 / Docker 场景；管理鉴权默认关闭——需要开启时加 `-password <密码>`，管理 API 含启停实例等高权限操作，仅本机访问加 `-listen 127.0.0.1`，公网部署务必配合反向代理限制来源），见 [部署文档](docs/DEPLOYMENT.md)。
  - **桌面壳**：安装桌面版后 `opencode2api --headless [-port 40000] [-listen 127.0.0.1]`——壳释放内嵌组件并拉起 core，无窗口运行，适合 SSH / 无图形会话；退出自动清理网关与实例，见 [Linux 开发与构建指南](docx/LINUX-DEVELOPMENT.md)。
- **桌面（Linux）**：安装 .deb / AppImage 即可；桌面模式内置本地 HTTP 服务，前端经它取数，行为与 Windows 版一致。
- **deb 自动注册 systemd 服务**：`.deb` 安装后自动注册并启用 `opencode2api` 服务（`systemctl status opencode2api` 查看）。配置文件为 `/etc/opencode2api/manager.env`（WebUI 端口默认 `60000`、监听 `127.0.0.1`、数据目录 `/var/lib/opencode2api`、统一网关密钥默认 `sk-unified-local`）。**修改该文件后必须执行 `sudo systemctl daemon-reload && sudo systemctl restart opencode2api` 才生效**（安装 deb 时终端会输出此提示）；端口/网关密钥也可以在 WebUI「统一网关」卡片修改（写入 `config.json`，保存即生效，无需重启）。
- **Docker（服务器）**：仓库根目录 `docker compose up -d --build` 即起完整管理器（含六页前端与免费层引擎），管理面板 `http://127.0.0.1:40000`，见 [部署文档](docs/DEPLOYMENT.md)。
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

前置条件（脚本会自检并给出可操作报错）：

- `go` 在 PATH（编译 Go core）
- `cargo` + MSVC 工具链（编译 Rust 壳）。脚本会**自动初始化 MSVC 环境**：优先用 `VsDevCmd.bat`，若该 VS 安装缺 `vcvars` 脚本（此时 `VsDevCmd` 静默不设环境）则回退到**从磁盘探测** `VC\Tools\MSVC\<ver>` 与 Windows SDK 并自行拼 `PATH/INCLUDE/LIB`；两者都失败时报「install Desktop development with C++」
- `bin\opencode2api-win.exe`：免费层引擎，缺失时脚本自动调用 `scripts\fetch-free-tier-engine.ps1`（需 git + go ≥ 1.24，按固定 commit 源码构建）
- `bin\mihomo.exe`：WARP 注册节点池的内核（GPL-3.0，固定版本见 `third_party/mihomo/SOURCE.txt`）；缺失时脚本自动下载并校验 SHA256。不放也能编译，只是节点池会提示「未找到 mihomo」
- `bin\dist\`：先跑 `npm run build`

自动完成：编译最新 Go core（版本号取自 `package.json`，commit/branch 取自 git）→ Rust 壳（内嵌 core + 免费层引擎 + mihomo）→ 组装 `portable-out/`（exe + WebView2Loader.dll + portable.txt + 前端 dist）→ 校验。生成的 portable 包走**独立端口槽位与数据目录**，与正式版零冲突，适合日常测试（详见 [测试指南](docs/TESTING.md)）。

> 注意：运行过打包产物后，壳会在 `portable-out\bin\` 释放 `opencode2api.exe` / `opencode2api-win.exe` / `mihomo.exe`（各约数十 MB）。脚本重新打包前会清理整个输出目录，若提示文件被占用，先退出正在运行的 `opencode2api.exe`。

### Linux 构建（本地 / WSL）

Linux 桌面产物（.deb）需在 Linux 环境构建（Windows 可用 WSL），核心两步：

1. 交叉编译 Go core 并构建免费层引擎（无扩展名，见下文「内嵌二进制」）：`GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=v1.5.4" -o bin/opencode2api .`，`powershell -File scripts/fetch-free-tier-engine.ps1`（或在 Linux 上用同一 Go 源码按 SOURCE.txt 的 commit 构建）；节点池内核 `bin/mihomo` 由 `scripts/fetch-mihomo.ps1` 或 CI 准备（缺失只影响节点池）
2. WSL 内 `npx tauri build --bundles deb` 产出 `linux-out/opencode2api_<version>_amd64.deb`；Debian/Ubuntu 用 `sudo apt install ./opencode2api_<version>_amd64.deb` 安装（deb 自动注册 systemd 服务 `opencode2api` 并启用），`opencode2api --headless` 可无桌面运行

细节（平台二进制解析 / 可写目录回退 / 前端资源复制 / 组件内嵌选择 / 构建与验证 / 常见问题）见 [docx/LINUX-DEVELOPMENT.md](docx/LINUX-DEVELOPMENT.md)。

### 正式产物（GitHub Actions）

三平台正式产物由 CI 矩阵产出（Windows NSIS / Linux deb+AppImage / macOS dmg）：

- **push main 且提交信息含大写 `CI`** → 构建（产物为 Actions artifacts）
- **push `v*` tag**（如 `v1.3.2`）→ 构建 + **自动发布 GitHub Release**（含三平台附件与自动 Release Notes）

### 内嵌二进制（bin/）

`bin/` 被 `.gitignore` 忽略，`opencode2api.exe` / `opencode2api-win.exe` / `mihomo.exe` 均不入库，本地构建前需自行准备：

- `opencode2api`（Windows 为 `.exe`）：由本仓库 Go 源码构建（`go build -trimpath -ldflags "-s -w" -o bin/opencode2api .`）
- `opencode2api-win`（Windows 为 `.exe`）：免费层引擎（GPL-3.0 外部程序），由 `scripts/fetch-free-tier-engine.ps1` 按 `third_party/opencode2api-win/SOURCE.txt` 登记的 commit 源码构建；CI 由 workflow 自动准备
- `mihomo`（Windows 为 `.exe`）：WARP 注册节点池内核（GPL-3.0，v1.19.33 固定版本），由 `scripts/fetch-mihomo.ps1` 下载并校验 SHA256；CI 由 workflow 按平台自动准备。缺失不阻断编译，只是节点池不可用

远程构建无需手动准备：GitHub Actions 会自动构建 Go 核心、免费层引擎与 mihomo 并完成打包（见 `.github/workflows/build-release.yml`）。

开发热更：

```bash
npm install
npm run tauri:dev
```

> 💡 **构建脚本报 `resource path target\release\WebView2Loader.dll doesn't exist`**：
> 本地 `target/` 被清理后会出现。按 CI 同样方式补回即可：
> `mkdir -p src-tauri/target/release && cp src-tauri/WebView2Loader.dll src-tauri/target/release/`

> ⚠️ **重编 core 后壳不会自动重新内嵌**：`bin/opencode2api.exe` 是在 **cargo 编译时**
> 被 `include_bytes` 嵌入壳的（见 `src-tauri/build.rs` / `embed.rs`），而 `bin/` 不在
> cargo 的默认文件监听范围内——只重新 `go build` core 再跑 `tauri:dev`，壳里仍是旧
> core。触发重嵌任选其一：touch 任意 Rust 源文件（如 `touch src-tauri/src/main.rs`）、
> 改动 `src-tauri/` 下代码，或 `cargo clean`。前端改动无此问题（`tauri:dev` 走 vite
> 实时服务；core 服务的 web UI 读仓库根 `dist/`，`npm run build` 即可）。

## 数据目录

运行时数据（配置文件、实例清单、日志）存 `%APPDATA%\opencode2api-manager\`（正式版）：

| 路径               | 说明                                  |
| ---------------- | ----------------------------------- |
| `config.json`    | 应用配置（端口、引擎开关/通道顺序、日志保留等）          |
| `runtime\`       | 引擎与供应商服务的运行目录、配置与日志                |
| （exe 旁）`bin\`    | 释放的 opencode2api.exe / opencode2api-win.exe / mihomo.exe |

**多环境隔离**：正式版 / dev（tauri dev）/ 便携测试（exe 旁 `portable.txt`）/ Docker 各自独立数据目录与**端口槽位**（正式 40000 起、dev 44100 起、portable 48200 起、Docker 可映射）；可用 `OPCODE2API_DATA_DIR`、`OPCODE2API_MANAGER_PORT`、`OPCODE2API_INSTANCE_BASE_PORT` 环境变量进一步隔离。


## 架构

```
┌──────────────────────────────────────────────────────┐
│  Tauri 2 前端（React 19 + Tailwind 4）                │
│  OpenCode / 自定义模型 / 统计 / 日志 / 诊断 / 设置     │
└──────────────────┬───────────────────────────────────┘
                   │ http://127.0.0.1:<port>/api/admin/*
┌──────────────────▼───────────────────────────────────┐
│  Go core 管理器（main 包 + core/manager）              │
│  引擎托管 / 供应商服务 / 日志 / 统计 / 配置 / 诊断      │
└───────┬───────────────────────────────┬──────────────┘
        │ 子进程：40080                 │ 子进程：30080
┌───────▼──────────────┐      ┌─────────▼─────────────┐
│ opencode2api 引擎     │      │ 供应商服务             │
│ 免费层 → OpenCode 上游 │      │ 自定义源 + 插件供应商  │
└──────────────────────┘      └───────────────────────┘
```

- **Tauri 壳**：只做窗口/托盘/内嵌二进制释放/拉起 core 管理器（`src-tauri/src/lib.rs`），管理职责全部在 Go core；headless 端（Docker/Web）不依赖壳，同一 core 直接监听 HTTP 提供完整管理 UI
- **Go core**：一份实现服务所有端（桌面 exe / Web 浏览器 / Docker），经 `/api/admin/*` HTTP 暴露；协议转换（OpenAI/Anthropic/Responses）在 `core/protocol` 与 `core/contract`
- **三端口**：40000 控制面（不代理推理）、40080 免费层引擎（外部独立进程，GPL 隔离）、30080 供应商服务（自定义源 + 插件）
- **环境隔离**：正式版 / dev（tauri dev）/ 便携测试（portable.txt）/ Docker 各自独立数据目录与**端口槽位**（40000 起每环境一段），互不干扰
- **端口配置化**：来源优先级 环境变量 > config.json（gateway_port/provider_port/instance_base_port/probe_*_port）> 编译默认

## 目录结构

```
src/                      # React 前端（TitleBar + 侧边栏 + 六页）
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
  custom/                 # 自定义模型源（四协议 + 多 Key 池 + 目录缓存）
bin/                      # 内嵌子程序源（opencode2api.exe / opencode2api-win.exe / mihomo.exe）
docs/                     # 部署/自定义模型/性能模式/TESTING/FAQ 等文档
docx/                     # 平台开发专题文档（Linux 开发与构建指南等）
```
