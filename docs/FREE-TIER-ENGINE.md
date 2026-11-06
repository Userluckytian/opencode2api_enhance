# 免费层引擎（opencode2api-win）接入说明

v2.0.0 起，OpenCode 免费层不再由本仓库自研网关实现，改由**外部独立进程**承担。
本文说明三进程拓扑、引擎的来源与许可证边界、安装/更新方式、运行目录与端口。

## 一、三进程拓扑

```
┌──────────────────────────┐        ┌─────────────────────────────┐
│ 管理面板 40000            │        │ 供应商服务 50080             │
│ opencode2api（Go 核心，   │◀──────▶│ 自定义源 + 插件供应商         │
│ 控制面：配置/密钥/统计）    │  HTTP  │ （core/manager 内置子服务）    │
└───────────┬──────────────┘        └─────────────────────────────┘
            │ 生成 config.json / 启停 / 健康检查（仅进程管理）
            ▼
┌──────────────────────────┐
│ 免费层引擎 40080           │  opencode2api-win（外部 GPL-3.0 程序）
│ OpenCode 免费池 + WARP 出口 │  对外提供 OpenAI 兼容 /v1
└──────────────────────────┘
```

- **40000** 管理面板：只做控制面（读写配置、启停引擎、看状态与用量），不代理任何模型请求。
- **40080** 免费层引擎：真正的 OpenCode 免费层网关（WARP 出口、轮换、限流冷却）。
- **50080** 供应商服务：自定义源与插件供应商的统一入口，与免费层互不影响。

## 二、为什么引擎是独立进程（GPL 隔离）

上游 `opencode2api-win`（<https://github.com/wbi35772/opencode2api-win>）许可证为
**GPL-3.0**。本项目**不链接、不内联、不修改**其源码：管理器只做三件事——
按配置写出 `config.json`、以子进程方式 `exec`（参数 `-config <路径>`）、通过
127.0.0.1 的 HTTP 做健康检查与统计。这属于 GPL-3.0 下的**聚合分发
（mere aggregation）/ 使用独立程序**，管理面板自身许可证不受传染；作为对价，
发行包必须随附 `third_party/opencode2api-win/`（`LICENSE` + `SOURCE.txt` +
`README.md` + 源码包）。详见该目录内声明。

## 三、运行目录

引擎工作目录 = 数据目录下 `runtime/free-tier/`：

| 平台 | 路径 |
|------|------|
| Windows | `%APPDATA%\opencode2api-manager\runtime\free-tier\` |
| Linux | `~/.config/opencode2api-manager/runtime/free-tier/` |
| 便携测试包 | `%APPDATA%\opencode2api-manager-test\runtime\free-tier\` |

该目录内容：`config.json`（由面板生成，引擎以 `DisallowUnknownFields` 严格解析）、
引擎自写的状态文件（模型目录、轮换、可用性），以及 `stdout.log` / `stderr.log`。
可执行文件由 Rust 壳释放到 **exe 同级 `bin/opencode2api-win[.exe]`**，找不到时
免费层不可用（面板会提示，不影响其他功能）。

## 四、安装 / 更新引擎

本地（Windows，需 git + go ≥ 1.24 + 网络）：

```powershell
powershell -ExecutionPolicy Bypass -File scripts\fetch-free-tier-engine.ps1
```

产物 `bin\opencode2api-win.exe`；随后 `cargo build`（`build.rs` 检测到文件才注册
cfg `embed_free_tier_engine`，缺失时只给 warning，不会编译失败）。
便携打包 `scripts\prepare-portable.ps1` 会在缺失时自动调用上面的脚本。

CI（`.github/workflows/build-release.yml`）按矩阵交叉构建三平台：
`windows/amd64`、`linux/amd64`、`darwin/arm64`，并导出与构建完全一致的源码包
`opencode2api-win-source.tar.gz` + `COMMIT.txt`，随 Release 一起分发。

换版本时**三处同步**（缺一不可，否则 SOURCE.txt 与实物不符）：
1. 覆盖 `bin/opencode2api-win[.exe]`；
2. `third_party/opencode2api-win/SOURCE.txt` 的 commit；
3. `scripts/fetch-free-tier-engine.ps1` 的 `-Commit` 默认值与 workflow 的 `ENGINE_COMMIT`。

## 五、WARP 与出口代理

不手改 `config.json`（面板会覆盖）。全部在 WebUI **「OpenCode 免费层」页**配置：
- **WARP 出口池**：开关、池大小（默认 2）、指定出口 IPv6；每个出口一个独立公网 IPv6，
  被限流后自动冷却替换。
- **出口代理**：多行，每行一个（默认 `direct`）；关闭 WARP 时免费通道回落到该列表。
保存后面板重写配置并重启引擎生效。

### 通道顺序：WARP 优先 → 密钥兜底

面板「通道顺序」有两种档位（对应管理面配置 `engine_channel_order`）：

| 档位 | 引擎 `anonymous` | 实际顺序 |
|------|------------------|---------|
| **WARP 优先**（默认） | 恒为 `true` | 请求先走**匿名通道**（WARP 出口）→ 不通才按 `KeyTiers` 顺序打 key |
| 密钥优先 | 仅当无 key 时为 `true` | 只用 key（v2.1 之前的行为），保留作回退档 |

引擎侧的依据（上游 `opencode2api-win` 9964407）：

- 路由只有 `anonymous=true` 才建匿名路由，否则直接建密钥路由
  （`internal/models/catalog.go:282-291`）—— 所以「配了 key 又关掉 anonymous」
  会让 WARP 出口完全不参与请求。
- 匿名路由先走 `doAnonymousUpstream`（WARP 出口），失败后
  `entering preferred key tiers` 再打 key（`internal/gateway/upstream.go:232-257`）。
  **开 anonymous 不会让 key 白费**，只是把 key 排到 WARP 之后。

WARP 优先时另有一层保护：管理器在引擎 `models.anonymous_backoff` 写入熔断参数
（连续失败 3 次 → 暂停匿名通道 1 小时，逐次翻倍，上限 6 小时）。缺了它，
WARP 不可用时每条请求都要先陪跑一次失败的匿名尝试才落到 key。

> 注意：**WARP 池可用时，免费模型的 key 请求本身也走 WARP 出口**
> （`internal/gateway/warp.go:38`）。所以 WARP 优先真正省下的是 **key 的额度** ——
> key 只在 WARP 通道全挂时才被消耗。

### 出口冷却账本（为什么 WARP 起不来）

引擎把被上游拒绝的公网出口写进一份 24h 黑名单，落盘在
`<runtime>/free-tier/config.json.warp-state.json`（引擎路径规则 = `configPath + ".warp-state.json"`）。
新注册的出口若落在已冷却的 `/64` 上会被判「already active or cooling down」，
于是 **WARP 池永远起不来**，引擎退回无 WARP 模式（`/healthz` 里连 `warp` 字段都没有）。

面板「WARP 出口池」卡里的**「重置出口冷却」**按钮会删掉这份账本。语义边界：

- **只清本地出口账本，不影响上游账号额度。** 免费额度是服务端按账号/session 记账的，
  重启引擎、换出口、换 key 都重置不了它。
- 清完必须**重启引擎**才会重新注册出口（引擎只在启动时读该文件）。

## 六、端口

| 进程 | 默认端口 | 覆盖方式 |
|------|---------|---------|
| 管理面板 | 40000 | 环境变量 `OPCODE2API_MANAGER_PORT` |
| 免费层引擎 | 40080 | 环境变量 `OPCODE2API_GATEWAY_PORT`，或面板配置网关端口 |
| 供应商服务 | 50080 | 环境变量 `OPCODE2API_PROVIDER_PORT` |

便携测试包走独立槽位（面板 48200、引擎 48280），与正式环境互不干扰。
三者只监听 `127.0.0.1`，不要暴露到公网；调用密钥由面板下发，引擎侧不落 git。

## 七、排障

| 现象 | 先看 |
|------|------|
| 面板显示「引擎二进制不存在」 | `bin/opencode2api-win[.exe]` 是否随包释放；缺失则跑一次面板（或 `fetch-free-tier-engine.ps1` 后重新构建） |
| 启动后 40080 不监听 | `runtime/free-tier/stderr.log`；端口被占用时改 `OPCODE2API_GATEWAY_PORT` |
| 保存配置后不生效 | 配置只在引擎重启后加载，用面板的「重启引擎」 |
| 免费层大面积 429 / 冷却中 | 看 `warp_cooling` 与出口池大小；必要时增加 WARP 出口或切换代理列表 |
| 面板显示 WARP 优先，但出口一个都起不来 | 看 `runtime/free-tier/config.json.warp-state.json`：里面压着 24h 黑名单，新出口全被判「已在冷却」。用面板「重置出口冷却」清掉后**重启引擎** |
| `/healthz` 里没有 `warp` 字段 | 引擎跑在无 WARP 模式（`g.warp == nil`）：WARP 池没起来。查 `runtime/free-tier/stdout.log` 的 `warp_startup_fallback` / `warp_retry_failed` |
| 配了 key 仍 429 | key 不是万能：免费额度按账号/session 记账，匿名通道同样受限。key 是兜底不是保证 |
| 构建只有 warning `bin/… not found` | 正常：`build.rs` 不注册 `embed_free_tier_engine`，`FREE_TIER_ENGINE = None`，面板其余功能不受影响 |

引擎状态字段（面板/OpenCode 免费层页读取）：`state` / `running` / `healthy` / `pid` /
`version` / `model_total` / `warp_status` / `warp_active` / `warp_cooling` / `bin_path` / `bin_exists`。

