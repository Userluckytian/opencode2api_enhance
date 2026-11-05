# Grok 插件 · grok2api 内核版本与升级指引

> 适用：`D:\Program Files\opencode2api\bin\providers\grok`
> 最后核对：2026-11-05 16:0x（本地仓库 + GitHub 官方 release 双向确认）

## 1. 一句话结论

**当前就是最新版，不用更新。** 但"以后怎么升级"比想象中麻烦一点——因为上游**只发 Docker 镜像，不发二进制**，插件面板的"检查更新"按钮默认是失效的。

## 2. 现状核对（2026-11-05）

| 检查项 | 值 |
|---|---|
| 本地参考仓库 | `D:\AI_Projects\grok2api` |
| HEAD | `e2fd7ab4` Merge PR #1088 from chenyme/fix_CVE |
| `git fetch` 后落后 `origin/main` | **0 个提交** |
| 本地最高 tag | `v3.1.8` |
| GitHub 官方 latest release | **v3.1.8**（2026-10-22 发布，无更新） |
| 插件内置二进制 | `runtime\grok2api-3.1.8\grok2api.exe`，89,580,544 字节 |
| 与仓库 `grok2api.exe` 的 SHA256 | **完全相同**（`F1C6882620FAA3C73B8326A8901D23C1AFF5BD3FFBBB6639F60E57F9C1EDA30E`） |
| `runtime\grok2api-3.1.8\VERSION` | `v3.1.8` |
| 运行中实例 | PID 15116，用的就是这份内置二进制 |

## 3. 三个必须知道的架构事实

### 3.1 插件只是外壳，账号池在内核里

`grok-provider.exe` 本身不存账号、不做轮询、不查额度。这些都发生在 `grok2api.exe` 内部。插件通过 HTTP 调它的 admin API：

```
/api/admin/v1/auth/login          # 登录拿 token
/api/admin/v1/accounts            # 账号池（增删改查）
/api/admin/v1/client-keys         # 客户端 key
/api/admin/v1/egress-nodes        # 出口节点
/api/admin/v1/egress-operations   # 出口自动分配
/healthz                          # 健康探针
```

**这层接口是插件与内核的契约。** 内核升级后如果 API 变了，插件就会坏——2026-11-05 修的 `external_base_url=auto` 那个 bug 就是这层出的问题（不是内核的问题，是插件外壳把解析逻辑改丢了）。

**内核升级后的必做验证**（比"看起来能跑"更重要）：

```powershell
# 登录取 token
$body = '{"username":"admin","password":"<config.yaml 里的 bootstrapAdmin 密码>"}'
$tok = (Invoke-WebRequest 'http://127.0.0.1:<port>/api/admin/v1/auth/login' `
  -Method POST -Body $body -ContentType 'application/json' -UseBasicParsing |
  ConvertFrom-Json).data.tokens.accessToken
$h = @{ Authorization = "Bearer $tok" }

# 逐个打插件实际用到的端点，必须全 200
foreach ($p in '/api/admin/v1/accounts','/api/admin/v1/client-keys',
               '/api/admin/v1/egress-nodes','/api/admin/v1/egress-operations') {
  try { $r = Invoke-WebRequest "http://127.0.0.1:<port>$p" -Headers $h -TimeoutSec 10 -UseBasicParsing
        "$p -> $($r.StatusCode)" } catch { "$p -> ERR" }
}
```

2026-11-05 实测（v3.1.8）：四个端点全 `200`，`/healthz` 返回 `{"ok":true}`。

### 3.2 当前是 `external` 模式，"下载新内核"对它不生效

`provider.json` 里：

```json
"runtime_mode": "external",
"external_base_url": "auto"
```

含义是：**插件不自己拉起内核**，而是去读 `data\grok2api-port.json` 找到已经在跑的实例连上去。

这个实例由独立脚本 `start-grok2api-auto.ps1` 守护（带 mutex 单实例，进程退出后 2 秒自动重启）。

**由此产生两个反直觉的行为：**

- 从面板下载新版本内核 → 只是把文件放进 `runtime\grok2api-<ver>\`，**当前 external 实例还在跑旧的那个**，不重启就还是旧内核。
- 停插件 → **内核不会跟着停**；启插件 → 也不会重启内核。两者生命周期是解耦的。

### 3.3 上游只发 Docker 镜像，不发二进制

这是"能不能一键升级"的根因。插件面板第 315 行自己写着：

> 上游只发布 Docker 镜像，没有 GitHub 二进制资产。自建发布源 manifest 后即可在面板检查/下载；不配置时请使用本地二进制或 external 模式。

`https://api.github.com/repos/chenyme/grok2api/releases/latest` 的 `assets` 字段是**空数组**——确认无二进制资产。

所以插件提供了 `runtime_manifest_url` 这个"自建发布源"机制作为补偿，见 §4.3。

## 4. 以后怎么升级

### 4.0 一条命令（推荐）：`deploy-grok2api-kernel.ps1`

插件目录下已提供封装脚本，把 §5 的六步全部自动化，**这是日常唯一需要记的入口**。

> **面板里也能看到这条命令**（插件 `0.4.2` 起）：
> 「运行时」页 →「升级内核（推荐方式）」卡片，带「复制」按钮。
> 卡片里的路径**来自后端实时下发的 `pluginDir`**，不是写死的；
> 插件换安装位置时页面自动跟着变。
>
> 为什么不能做成面板按钮直接升级：上游只发 Docker 镜像，插件没有可用于
> `runtime_manifest_url` 的二进制源。「检查更新 / 下载最新」两个按钮必须
> 先自建发布源才可用，否则会提示未配置。

```powershell
cd 'D:\Program Files\opencode2api\bin\providers\grok'

# 演练：只打印将要做什么，不动文件、不停进程
.\deploy-grok2api-kernel.ps1 -WhatIf

# 升级到最新（自动查 tag）
.\deploy-grok2api-kernel.ps1

# 升级到指定版本
.\deploy-grok2api-kernel.ps1 -Version v3.1.9

# 强制重建当前版本
.\deploy-grok2api-kernel.ps1 -Version v3.1.8 -Force

# 只构建不部署
.\deploy-grok2api-kernel.ps1 -Version v3.1.9 -NoDeploy

# 走自建 manifest 下载而非本地编译
.\deploy-grok2api-kernel.ps1 -ManifestURL https://your-host/manifest.json
```

它自动处理的四件容易出事的事：

| 风险点 | 脚本的处理 |
|---|---|
| 停内核后被守护脚本 2 秒拉起 | 先停守护、再停内核，并轮询确认都退干净 |
| 守护脚本写死旧版本路径 | 自动改 `runtime\grok2api-<ver>\grok2api.exe`，原脚本备份为 `.bak-<时间戳>` |
| 内核换端口，插件仍连旧地址 | 检测端口变化 → 强制重启插件 → 插件重新读 `port.json` |
| 契约变了但看不出来 | 自动跑 `/healthz` + 4 个 admin 端点，逐条打印状态码 |

**已验证**（2026-11-05，隔离沙箱跑完整流程）：

```text
停止内核与守护 → 已停守护/内核，确认干净
启动新内核      → 守护已启动，新内核就绪 64901 → 62663
端口已变        → 提示插件必须重启
重启插件        → 插件运行中
端点验证        → 5/5 全部 200
插件日志        → 已连接外部实例 http://127.0.0.1:62663（新端口，非旧端口）
```

**开发期踩到并已修掉的三个缺陷**（记录在此，避免后人重犯）：

1. **守护进程误判**：原用 `CommandLine -like '*start-grok2api-auto.ps1*'` 匹配，
   会把任何提到该文件名的命令行（包括检测命令自身）当成守护进程并误杀。
   改为只认 `-File <精确路径>`。
2. **插件拉起失败**：手工启动插件必须自己注入 `PROVIDER_DIR` / `PROVIDER_CONFIG` /
   `PLUGIN_AUTH_TOKEN` 三个环境变量（正常由宿主注入），缺任一直接 fatal 退出。
3. **`-WhatIf` 假失败**：`ShouldProcess` 拦住了停止动作，进程其实没停，
   后续"校验已退出"必然抛错。现在 `-WhatIf` 走到部署前显式收口并正常结束。

> 脚本用 **UTF-8 with BOM** 保存。PowerShell 5.1 会把无 BOM 的 `.ps1` 按 ANSI 解码，
> 中文注释里的引号会错位导致语法错误。改这个脚本时务必保留 BOM。
> （注意与 `provider.json` 相反——那里必须无 BOM，否则 Go 的 JSON 解析报错。）

### 4.1 路线 A：从源码自己编（脚本内部就是这么做的）

仓库自带底层脚本：

```powershell
cd 'D:\Program Files\opencode2api\bin\providers\grok\runtime'
.\build-grok2api.ps1 -Version v3.1.9
```

它做的事：
1. 在 `runtime\.cache\grok2api` 克隆/复用源码（`--filter=blob:none` 浅克隆）
2. `git fetch --tags --force` + `git checkout v3.1.9`
3. `go build -trimpath -ldflags "-s -w"` 出单文件二进制
4. 落到 `runtime\grok2api-3.1.9\grok2api.exe` + 写 `VERSION`
5. 打印 sha256 / size 和一段可直接用的 manifest JSON

依赖：`git`、`go`。**不需要 pnpm**——插件不用 grok2api 自带的前端。

> ⚠️ 脚本里 `Set-Content -NoNewline` 写 `VERSION` 文件。PowerShell 5.1 的 `Set-Content -Encoding UTF8` 会带 BOM，如果哪天改了这行要注意——Go 侧读 `VERSION` 时不去 BOM 会得到 `\ufeff3.1.9`。

### 4.2 路线 B：用 Docker 镜像里的二进制

上游发的是多架构镜像：

```powershell
docker pull ghcr.io/chenyme/grok2api:v3.1.9
docker create --name g2a ghcr.io/chenyme/grok2api:v3.1.9
docker cp g2a:/app/grok2api .\grok2api.exe      # 具体路径以镜像为准，先 docker run --rm ... ls 确认
docker rm g2a
```

适合不想装 Go 工具链的情况。缺点是要有 Docker，且镜像内的路径需要先探。

### 4.3 路线 C：自建 manifest 发布源（唯一能在面板里点的方式）

**前提**：你得先把二进制传到某个 HTTPS 地址（对象存储 / 内网静态服务 / GitHub Release 自己的附件都可以）。

`manifest.json` 格式（照 `runtime\manifest.example.json`）：

```json
{
  "latest": "3.1.9",
  "versions": {
    "3.1.9": {
      "windows-amd64": {
        "url": "https://your-host/grok2api-3.1.9-windows-amd64.exe",
        "sha256": "<build 脚本打印的小写 hex>",
        "size": 89580544
      }
    }
  }
}
```

然后：
1. 面板「设置」→ `runtime_manifest_url` 填这个 manifest 的 URL → 保存
2. 「运行时」卡片 → **检查更新** → 有新版本会提示
3. 点确认 → `POST /panel/api/runtime/activate` → 下载 → 校验 sha256 → 解压到 `runtime\grok2api-<ver>\`
4. **external 模式下还要重启内核**才真正生效（见 §3.2）

下载实现要点（`runtime.go:DownloadVersion`）：
- 先写 `grok2api.exe.download` 临时文件，边写边算 sha256
- 校验通过才 rename 成正式文件名（不会留半截文件）
- 落到 `runtime\grok2api-<version>\`，与手工放置的结构一致

## 5. 升级操作的完整清单

> 用 `deploy-grok2api-kernel.ps1` 时本节全部自动完成，仅作原理参考与手工排障用。

不管走哪条路，顺序都是这个：

| 步骤 | 动作 | 注意 |
|---|---|---|
| 1 | 备份 `runtime\grok2api-<旧版本>\` 与 `data\grok2api\config.yaml` | 配置文件里有 `bootstrapAdmin` 密码 |
| 2 | 拿到新二进制，放到 `runtime\grok2api-<新版本>\grok2api.exe` + 写 `VERSION` | 目录名必须等于版本号，`resolveBinary` 按这个找 |
| 3 | **停掉内核实例**（`start-grok2api-auto.ps1` 会每 2 秒重启，要先停这个守护脚本，再停 exe） | 顺序反了会被重启拉起来 |
| 4 | **改守护脚本里写死的内核路径** | 第 3 行的 `$exe = Join-Path $root 'runtime\grok2api-3.1.8\grok2api.exe'`，不改的话守护的还是旧内核 |
| 5 | 改 `provider.json` 的 `grok2api_version` 为 `3.1.9` | 只影响 binary 模式的查找路径 |
| 6 | 启动新内核，跑 §3.1 的四端点验证 | 全 200 才算过 |
| 7 | **重启插件** | 内核换端口后插件缓存的 baseURL 已失效，不重启会一直连不上 |
| 8 | 看面板 runtime 卡片是否 `已就绪` | 失败会显示 `rt.error` 原文（2026-11-05 起） |

> **版本双写规则同样适用**：`provider.json` 的 `version` 是**插件**版本（当前 `0.4.1`），`grok2api_version` 才是**内核**版本。两者不要混。插件版本改了必须同时改 `config.go` 的 `pluginVersion` 常量。

## 6. 常见误判

| 现象 | 是不是问题 |
|---|---|
| `data\grok2api-port.json` 的 `updatedAt` 停在很久以前 | **不是**。脚本只在换端口时刷新时间戳；端口没变就一直不更新。看 `baseURL` 对不对即可 |
| 面板 `/panel/api/status` 返回 401 | **不是**。需带 `X-Panel-Token` 头，裸调必然 401 |
| 内核是外部脚本拉起的，不是插件 | **已知设计**。`runtime_mode=external` 就是这个含义 |
| `runtime\.cache\grok2api` 占空间 | 编译缓存，可删，下次 `build-grok2api.ps1` 会重新 clone |
| 停插件后内核还在跑 | **已知设计**，见 §3.2 |

## 7. 变更记录

| 日期 | 事项 |
|---|---|
| 2026-11-05 | 建立本文档。核对本地仓库 / GitHub release 均为 v3.1.8；内置二进制与仓库产物 SHA256 一致；实测插件四个 admin 端点全 200。同批修复插件侧 `external_base_url=auto` 解析回归（插件 `0.4.1`） |
| 2026-11-05 | 新增 `deploy-grok2api-kernel.ps1`（一条命令完成构建+部署+验证）。隔离沙箱跑通完整流程；修掉三个缺陷：守护进程误判（自匹配误杀）、插件拉起缺环境变量、`-WhatIf` 假失败。补第 5 节"改守护脚本写死路径"与"重启插件"两步 |
| 2026-11-05 | 插件 0.4.2：面板「运行时」页新增「升级内核（推荐方式）」卡片，显示可复制命令。路径经后端新增的 plugin.status.pluginDir 动态下发，不写死安装位置；同时修正原「更新源」处的误导文案 |
