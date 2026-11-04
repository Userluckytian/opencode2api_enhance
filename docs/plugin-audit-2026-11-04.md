# 插件体系分析汇总与修改报告

> 日期：2026-11-04 · 范围：`D:\Program Files\opencode2api\bin\providers\` 下 12 个已安装插件 + 各自源码仓库
> 方法：读源码 / 读安装目录数据 / 只读探测（宿主 50080、插件面板端口）；**未改任何插件代码**
> 副作用声明：为验证 QwenWork 对话链路，向本机插件发了 4 次极小测试请求（合计约 0.03 积分）；未改动任何数据文件
> 结论先行：**不建议现在直接开工**。先看第 0 节与第 8 节；3 项已确认，另有 3 项待你拍板后再排期。

---

## 0. 一页结论（TL;DR）

1. **你的判断成立：账号数据没有统一放在各插件的 `data/` 里。**
   按"数据位置"判定：**6 个合规**（RunBios、QwenWork、Skywork、BuddyHub、Grok、xhx；后两个另有打包/体积问题）、**2 个部分违规**（MiniMax、Flonno）、**2 个严重违规**（LobsterAI、LobsterHub）、**2 个没有 data/**（LOOMy、VibeX）。典型问题（按严重度）：
   - LobsterAI：凭证实际写在 **源码仓库** `D:\AI_Projects\Lobster\lobsterai2api\auths`（安装目录的 `auths/` 是空的）；
   - LobsterHub：账号 vault 默认写在 **`%APPDATA%\LobsterHub`**，不在插件目录；
   - MiniMax：104 个账号的 `accounts.json` 在 **插件根目录** 而不是 `data/`；
   - Flonno / RunBios：Chrome profile 写在 **`C:\Users\ASUS\.qw2api`**（实测 1.0GB），不在各自 `data/`。
   另外发现一个 1,003.7MB 的游离目录 `C:\Users\ASUS\.qw2api`（最后写入 2026-11-04 02:06，说明现在还在用）。

2. **`chrome-autoreg` 不是依赖库，是 Chrome/Edge 的 user-data-dir 目录名。**
   QwenWork/Skywork 把它放在插件 `data/` 下，所以你看得见（953MB / 1,175MB）；
   Flonno/RunBios 用的是同一套 `internal/browser` 代码，但默认落到 `~/.qw2api/chrome-autoreg`，所以"看起来没有"；
   Grok 外包给 Python worker，profile 在 `%TEMP%\grok_reg_chrome` 每轮回收；
   LobsterAI 没有它，是因为它走 **OAuth 回调 + 一次性临时 profile**，不需要常驻浏览器（详见第 6 节）。

3. **QwenWork 的"对话有问题"可拆成 4 个可验证缺陷**（不是单点崩溃）：
   流式错误被吞、无多轮上下文、命中卡可能取到别的请求、**真实响应模型完全不透出**。
   真实模型确实在上游 SSE 的 `model` / `raw_usage.model` 里，插件已经解析到了，但在写 OpenAI 帧时被丢掉了。

4. **MiniMax 积分丢失有两条根因**：
   ① 后端 `balanceCache` 只在内存，插件重启即空（实测 104 个账号当前积分字段全空）；
   ② 前端"同步积分"完成后只刷新 summary、不刷新账号列表，且 2.5s/8s 的定时刷新早于 104 个账号的同步完成。
   页面切换本身不丢数据，但"刷新页面/重进插件/重启进程"一定丢。

5. **UI 统一可行、成本可控**：以 RunBios 的暖白+鼠尾草绿为骨架，换成"抹茶 + 奶酪 + 奶油白"（**不用粉**）。
   10 个有面板的插件（不含 RunBios）：7 个以换 token 为主，2 个要从暗色重写（xhx、MiniMax），1 个要补布局（Flonno）；VibeX 无面板不计。纯 UI 约 **12~15 人日**（不含功能）。

6. **`探测链路 / 链路设置` 不是装饰，但当前没生效，而且与注册换 IP 无关。**
   后端真的接了 mihomo delay API；但当前 `clash_api=""` → 功能关闭；
   它只服务"对话出站稳定"，QwenWork 的注册浏览器**没有注入任何代理**（全仓 grep 无 `--proxy-server`）。

7. **功能差距最大的三项**：
   - 调用日志含"扣积分"：只有 QwenWork / Skywork / LobsterHub 完整；
   - Clash 与注册 IP 联动：只有 Flonno / RunBios / Grok worker 闭环；
   - 真实响应模型：几乎全缺。

8. **Grok「删了又回来」不是玄学，是"数据库删了、文件没删"**：
   面板删除只调 grok2api 的 `DELETE /accounts`，从不删 `data/accounts` 里的 `xai-*.json` / `*.txt`；
   而每次注册任务结束都会 `runImport(all)` 把目录里所有文件重新导入 → 已删账号原样复活。
   额度视图只用了参考实现约 1/6 的字段；"轮询"也不是插件能控的，账号选择完全在 grok2api selector 里。
   另外，"注册/签到"这类事件目前没有结构化日志，只有原始日志抽屉（详见 5.4 与 3.6）。

9. **模型上下文/输出：不是"都拿不到"，而是三种情况混在一起**：
   - 真拿到：BuddyHub、xhx、QwenWork/Skywork（context）、RunBios/LOOMy（context）；
   - **有数据但键写错/没透出**：LobsterAI（只写 `context_length`，宿主只认 `context_window`）、Grok（grok2api 内部有，`/v1/models` 没带）；**MiniMax 的 1M 选项也没透出**（默认 512K 正常，见 5.6）；
   - **上游确实不给**：QwenWork/Skywork/LOOMy/RunBios 的 `max_output_tokens`；Flonno 的 context 是注入默认值。
   详见 5.5 / 5.6。

10. **插件能力说明已补全**：新增 `docs/PLUGIN-CAPABILITIES.md`，A~J 共 100+ 项，按 P0~P3 分级，
    含适用条件/最小实现/验收/参考实现，以及事件日志、模型元数据、删除生命周期、profile、反模式。

---

## 1. 宿主与插件契约（为什么"应该"在 data/）

| 事项 | 事实 | 来源 |
|---|---|---|
| 插件形态 | `providers/<id>/provider.json` + 一个可执行文件，宿主扫描并拉起 | `docs/PLUGIN-PROVIDERS.md` §2 |
| 启动契约 | `--provider-serve --port 0`，env `PROVIDER_DIR` / `PROVIDER_CONFIG` / `PLUGIN_AUTH_TOKEN` | 同上 §4.1 / §10.2 |
| **数据边界** | **运行数据写插件目录内 `data/`；不得写宿主目录/系统目录；删除插件时随目录删除** | 同上 §10.5 |
| 配置边界 | 宿主只读 7 个保留键；`provider_private_configs` 完全不解析、不记日志 | 同上 §3.3 |
| 面板 | `panel:"sidecar"`（动态端口，宿主卡片 🌐）或 `panel_port`（固定端口，0=禁用） | 同上 §11.1 |
| 宿主日志 | `%APPDATA%\opencode2api-manager\runtime\_provider-service\call_log.jsonl`：有请求模型/时间/tokens/status，**无扣积分、无真实模型** | 实测 |

**关键推论：**
- 宿主只认 `provider.json`，**根本不知道你的源码仓库在哪**。账号写进源码目录，是插件自己的路径解析造成的，不是宿主行为。
- 账号若放在 `provider.json`（LOOMy 的 session、VibeX 的 cookie），虽然"在插件目录内"，但宿主删除插件即删、且明文；这是"合规但脆"的形态。
- 宿主侧没有统一的"插件数据管理"能力，也没有清理机制 → 4.1GB 的浏览器 profile 无人回收。

> 插件作者的能力清单（A~J，100+ 项，P0~P3）见 [`docs/PLUGIN-CAPABILITIES.md`](PLUGIN-CAPABILITIES.md)；契约强制项见 [`PLUGIN-PROVIDERS.md`](PLUGIN-PROVIDERS.md) §10。

---

## 2. 数据存放审计（对应问题 1）

### 2.1 实测总表

| 插件 | 面板形态 | `data/` 体积 | 账号/凭证实际位置 | 判定 |
|---|---|---:|---|---|
| **runbiosproxy** | sidecar，暖白 4 视图 | 0.1MB | `data/pool.json` | ✅ 合规（基准） |
| **qw2api**（QwenWork） | sidecar，暗/亮 顶栏 | 997.5MB | `data/vault/<uid>/` + `data/state.json` | ✅ 合规，但 profile 953MB、日志 43MB |
| **skywork** | sidecar，暗/亮 顶栏（同源） | 1,175.5MB | `data/vault/` + `data/state.json` | ✅ 合规，但 profile 1,175MB |
| **buddyhub** | sidecar，暗底米白卡 | 1.7MB | `data/auths/workbuddy-*.json` | ✅ 合规 |
| **xhx**（商汤） | sidecar，单页 4 卡，暗色 | 986.9MB | `data/accounts/<uid>/auth.json` | ✅ 合规，但 Edge profile 986MB |
| **mmxproxy**（MiniMax） | sidecar，印刷风 6 tab | 18.6MB | **`accounts.json`（插件根）** | ⚠️ 部分违规 |
| **grok** | sidecar，浅色 6 tab | 116.3MB | `data/accounts/xai-*.json` | ⚠️ 数据合规，但安装目录含源码/注册器/5 个 exe.bak、日志 110MB |
| **flonno2api** | sidecar，极简单页（仅注册） | ~0MB | `data/pool.json` | ⚠️ 部分违规：账号在 `data/`，profile 在 `~/.qw2api` |
| **lobsterhub** | sidecar，5 视图 | **无 data/** | **`%APPDATA%\LobsterHub\vault.json`** | ❌ 违规 |
| **lobsterai** | sidecar，6 视图 | ~0MB | **`D:\AI_Projects\...\auths`（源码仓库）** | ❌ 严重违规 |
| **loomy** | sidecar，深色单页 | **无 data/** | `provider.json` 的 `session` | ⚠️ 无持久化（设计如此） |
| **vibex** | **无面板** | **无 data/** | `provider.json` 的 `cookie` | ⚠️ 单账号，无持久化 |

**游离目录（不属于任何插件 data/）：**

| 路径 | 体积 | 谁写的 | 证据 |
|---|---:|---|---|
| `C:\Users\ASUS\.qw2api` | **1,003.7MB** | flonno2api / runbiosproxy 的通用 Chrome profile；另含旧版 qw2api 独立运行残留（config/state/usage/vault/fail-dumps） | `flonno2api/internal/browser/chrome.go:133-134`、`runbios_proxy/internal/browser/chrome.go:133-134`；目录 mtime 2026-11-04 02:06 |
| `C:\Users\ASUS\AppData\Roaming\LobsterHub` | 0.1MB | lobsterhub（默认 dataDir） | `lobsterhub/store.go:189-198` |
| `C:\Users\ASUS\AppData\Roaming\opencode2api-manager` | 43.2MB | 宿主（合法） | — |

**总量：插件 `data/` 约 3.3GB + 用户目录 1.0GB ≈ 4.3GB。其中浏览器 profile 约 4.1GB，占 95%。**

### 2.2 四类违规（按严重度）

#### A. 凭证在源码仓库 —— LobsterAI（最严重）

- `provider.json` 的 `provider_private_configs.auth_dir` 被写死为 **`D:/AI_Projects/Lobster/lobsterai2api/auths`**（机器相关绝对路径）。
- 实测：安装目录 `providers/lobsterai/auths/` **为空**；源码仓库 `auths/` 有 **6 个** `lobsterai-*.json`；安装目录 `data/state.json` 里正好记录这 6 个 uid。
- 代码直接采用绝对路径：`lobsterai2api/plugin.go:265-271`（相对路径才按插件目录解析）。
- 后果：
  1. 换一台机器安装，`auth_dir` 不存在 → 账号全空；
  2. 删/挪源码仓库 → 账号消失；
  3. 插件与独立部署共享同一批 refresh token，而 refresh token 是**一次性**的，只能靠 `.locks` 互斥，拷两份会互相踢；
  4. 插件升级/重装不会带走这些凭证，用户以为"插件删了"，其实凭证还留在源码目录。

#### B. 数据在用户目录 —— LobsterHub / Flonno / RunBios

- **LobsterHub**：`store.go:189-198` 默认 `os.UserConfigDir()/LobsterHub`，即 `%APPDATA%\LobsterHub`；账号 `vault.json`、`settings.json`、`usage.json`、`usage-log.json`、`login_sessions.json` 全在这里。
  - 有 `data_dir` 覆盖键（`plugin.go:47-50,173-199`），但默认不隔离；宿主删除插件不会清理。
  - 它还读 `<providers>/lobsterai/data/clash.json`（`plugin.go:278-290`），与 LobsterAI 共用绑定表；但两边的写锁落在**两个不同文件**（`lobsterai/data/locks/clash.lock` vs `lobsterai/data/clash.lock`，实测两个都存在）→ 同一份绑定表读-改-写互不互斥，可能撞端口或覆盖 pin。
- **Flonno / RunBios**：`internal/browser/chrome.go:133-134` 的默认 profile 是 `home/.qw2api/chrome-autoreg`，不是 `PROVIDER_DIR/data/`。
  - 实测 `~/.qw2api/chrome-autoreg` 1,003.6MB，最后写入 2026-11-04 02:06（今天凌晨，说明自动注册仍在用）。
  - 两个插件共用同一个路径 → 同时跑自动注册时会互踢（Chrome 同 user-data-dir 只允许一个实例）。
  - 注：RunBios 的 Turnstile 浏览器另有 `data/browser-profiles`（`browserturnstile.go:29`），布局是对的；出错的是通用 `chrome.go` 这条路径。

#### C. 账号文件在插件根而不是 data/ —— MiniMax

- `provider_serve.go:48-49`：`app := NewApp(filepath.Join(dir, "accounts.json"))`，注释明确"号池文件与插件 exe 同目录；运行数据收进 data/"。
- 实测 `providers/mmxproxy/accounts.json`，104 个账号；`data/` 里只有 net.json / usage-log / fail-dumps / 日志。
- 这是**有意的设计**（独立版与插件版共用加载逻辑），但与契约"运行数据只写 data/"冲突；删除/升级时容易被误删，也不利于备份。

#### D. 源码/构建物在安装目录 —— Grok

- `providers/grok/` 里同时有：`src/`（Go 源码）、`build.ps1`、`build-worker.ps1`、`register/`（50.8MB）、`register.bak-clash/`（50.8MB）、`runtime/grok2api-3.1.8/`（85.4MB）、5 个 `grok-provider.exe.bak-*`、`start-grok2api-auto.ps1`。
- `build.ps1:3,20` 以插件目录为构建根，`go build -o $root/grok-provider.exe`；`.gitignore` 只忽略 `data/`、`runtime/`、`register/`，**没有忽略 `src/`**。
- `provider.json` 的 `_source.repo` 指向外部仓库，与实际（源码就在插件目录）不符。
- 后果：整目录覆盖升级会同时覆盖 `provider.json`（含明文密钥）和 `data/`（账号），**高风险**；`data/logs` 已 109.7MB 且**无轮转**。

### 2.3 "不持久化"清单（用户说"很多地方的数据都不提供持久化"）

| 插件 | 仅在内存、重启即丢的数据 | 严重度 |
|---|---|---|
| **MiniMax** | `balanceCache`（积分/签到/API Key/结算状态）、`recentCalls` 80 条、`modelsCache`、网关计数、`schedPos` 游标 | **高**（积分丢失的直接原因） |
| **LOOMy** | `claimState`（领取状态/历史）、`reqLog` 500 条、`modelCache`、积分 60s 缓存 | 中（session 与积分余额在上游，不丢） |
| **VibeX** | 无任何运行数据；只有 provider.json 的 cookie | 中 |
| **BuddyHub** | sticky 绑定、轮转游标、任务日志环形 50 条、OAuth 进行中流程 | 低（账号/积分/任务状态已落盘） |
| **LobsterAI** | 登录会话（重启即作废） | 低 |
| **QwenWork** | 无（state.json / requests.json / usage 都落盘） | — |
| **xhx** | 面板不展示的 per-call `history` 在 state.json（已落盘） | — |

### 2.4 体积构成（为什么一个插件能到 1GB）

| 插件 | 浏览器 profile | 其它大文件 | 说明 |
|---|---:|---|---|
| skywork | 1,175.2MB | 日志 0.3MB | Edge/Chrome 首启解包：`Default/Extensions`、`IndexedDB`、`Service Worker`、`Cache`、`Code Cache`、`component_crx_cache`、`ProvenanceData`、Widevine |
| qw2api | 953MB | `qw2api.log` 43.2MB | 同上 + 日志无轮转 |
| xhx | 985.9MB（Edge profile） | 日志 0.9MB | 用系统 Edge 的完整 user-data-dir 保登录态；`Default` 510MB（Extensions 374MB）、`component_crx_cache` 201MB、`ProvenanceData` 168.6MB |
| `~/.qw2api` | 1,003.6MB | 旧 qw2api 残留 | 与上面同源 |
| grok | 0（外包 worker，profile 在 `%TEMP%` 回收） | `data/logs` 109.7MB | 注册器 50.8MB×2 + runtime 85.4MB 在插件目录 |

**根因**：这些 profile 是"长期复用"的 Edge/Chrome 用户目录，浏览器每次升级会下载组件、扩展、模型；插件没有 TTL、没有清理、没有 `--disable-component-update` / `--disk-cache-size` 之类的约束。
**注意**：自动注册其实不需要长期登录态（每次都是新号），只有 xhx 这种"人工登录后抓 Cookie"才需要持久 profile。

### 2.5 迁移原则（执行前必须遵守）

1. **refresh token 是一次性的**：迁移只能**整目录移动**，不能复制两份；否则两份都会失效。
2. 顺序：停插件 → 整目录移动 → 改默认路径 → 启动 → 面板核对账号数/积分 → 再删旧目录。
3. **每个插件独立 profile**，禁止两个插件共享同一个 `user-data-dir`。
4. 升级脚本必须显式排除 `data/`；Grok 的"整目录覆盖"尤其危险。
5. 保留**一版只读回落**：新路径不存在时读旧路径，避免用户升级后账号"消失"。
6. 面板加"数据目录 / 占用 / 清理"卡片，让用户看得见、能清理。

---

## 3. 功能全景（对应问题 3）

### 3.1 功能分类（5 组 25 项）

| 组 | 编号 | 功能 |
|---|---|---|
| 账号 | A1 | 账号池管理（增/删/启停/批量） |
| 账号 | A2 | 导入 / 导出 |
| 账号 | A3 | 余额 / 积分查询（单号 + 批量） |
| 账号 | A4 | 批量签到（账号维度） |
| 账号 | A5 | 健康 / 冷却 / 失效标记 |
| 调用 | B1 | 对话调试 |
| 调用 | B2 | 调用日志（时间 / 模型 / 账号 / 状态 / tokens / 耗时 / **扣积分**） |
| 调用 | B3 | 用量统计 / 趋势 |
| 调用 | B4 | **真实响应模型名透出** |
| 网络/调度 | C1 | 负载均衡策略 |
| 网络/调度 | C2 | 错误转移 / 无感切号（401/402/429/5xx） |
| 网络/调度 | C3 | Clash 探测 / 切换 / 分组 |
| 网络/调度 | C4 | 多出口 / 按号出线 |
| 网络/调度 | C5 | 代理配置 |
| 自动化 | D1 | 自动注册（协议 / 浏览器） |
| 自动化 | D2 | 夜间 / 定时注册 |
| 自动化 | D3 | 临时邮箱 |
| 自动化 | D4 | 人机验证（CF Turnstile / 阿里云验证码） |
| 自动化 | D5 | 日常任务（签到 / 打卡 / 宠物） |
| 自动化 | D6 | 登录（OAuth / 扫码 / 密码） |
| 平台 | E1 | 内嵌面板 |
| 平台 | E2 | 数据持久化 |
| 平台 | E3 | 数据目录合规 |
| 平台 | E4 | 体积 / 升级友好 |
| 平台 | E5 | **事件日志**（注册 / 签到 / 切号 / 异常，可筛选、可导出、持久化） |

> 说明：A4（批量签到）与 D5（日常任务）不是一回事。A4 是"每个账号去上游点一次签到"；D5 是"插件自己的任务系统"（旅行/活跃/保活/夜猫子等）。很多上游没有签到，A4 就不适用。

### 3.2 功能矩阵 A：账号 / 计费 / 任务

| 插件 | A1 池 | A2 导入导出 | A3 余额/积分 | A4 批量签到 | B1 对话调试 | B2 调用日志(含扣分) | B3 用量 | D5 日常任务 |
|---|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|
| RunBios | ● | ● | ● | ○ | ○ | ●* | ● | ○ |
| Flonno | ◐ | ◐ | ● | ○ | ○ | ◐ | ◐ | ○ |
| QwenWork | ● | ◐ | ● | ○ | ● | ● | ● | ○ |
| Skywork | ● | ◐ | ● | ○ | ● | ● | ● | ○ |
| BuddyHub | ● | ○ | ◐ | ● | ● | ◐ | ◐ | ● |
| Grok | ● | ◐ | ● | ○ | ● | ◐ | ◐ | ○ |
| LobsterAI | ● | ○ | ● | ● | ● | ○ | ○ | ● |
| LobsterHub | ● | ○ | ● | ● | ● | ● | ● | ● |
| LOOMy | ○ | ○ | ● | ○ | ◐ | ○ | ○ | ● |
| MiniMax | ● | ○ | ● | ● | ● | ◐ | ● | ● |
| 商汤 xhx | ● | ○ | ● | ○ | ○ | ◐ | ◐ | ○ |
| VibeX | ○ | ○ | ○ | ○ | ○ | ○ | ○ | ○ |

`●` 有且可用；`◐` 部分/有条件/仅内部；`○` 无；`●*` RunBios 记的是"成本"而非"积分"。

### 3.3 功能矩阵 B：网络 / 自动化 / 平台

| 插件 | C1 负载 | C2 切号 | C3 Clash | C4 多出口 | D1 自动注册 | D2 夜间 | D3 临时邮箱 | D4 人机验证 | D6 登录 | B4 真实模型 | E3 数据合规 |
|---|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|
| RunBios | ● | ● | ● | ◐ | ◐ | ◐ | ● | ● | ◐ | ○ | ● |
| Flonno | ● | ● | ● | ◐ | ● | ○ | ● | ◐ | ◐ | ○ | ◐ |
| QwenWork | ◐ | ● | ◐ | ○ | ● | ● | ● | ◐ | ● | ○ | ● |
| Skywork | ◐ | ● | ◐ | ● | ● | ● | ● | ◐ | ● | ○ | ● |
| BuddyHub | ● | ● | ○ | ○ | ○ | ○ | ○ | ○ | ● | ◐ | ● |
| Grok | ◐ | ◐ | ◐ | ◐ | ● | ○ | ● | ● | ● | ◐ | ○ |
| LobsterAI | ● | ● | ● | ◐ | ○ | ○ | ○ | ○ | ● | ◐ | ○ |
| LobsterHub | ● | ● | ◐ | ◐ | ○ | ○ | ○ | ○ | ● | ◐ | ○ |
| LOOMy | ○ | ○ | ○ | ○ | ○ | ○ | ○ | ○ | ◐ | ◐ | ● |
| MiniMax | ◐ | ● | ● | ◐ | ● | ◐ | ● | ○ | ● | ○ | ◐ |
| 商汤 xhx | ● | ● | ○ | ○ | ○ | ○ | ○ | ○ | ● | ◐ | ● |
| VibeX | ○ | ○ | ○ | ○ | ○ | ○ | ○ | ○ | ○ | ○ | ◐ |

> 读表说明：
> - **B4 的 ◐** 指"上游模型目录/真名清单已透出，但对话响应里没有真实模型"（目前所有插件都还没有"响应真实模型"）。
> - **D4 的 ●** 指"有实现，可能默认关闭"：RunBios `captcha.enabled=false`、QwenWork 是阿里云验证码（不是 CF）、Grok 用 turnstilePatch。
> - **D2 的 ◐** 指"有配置键/窗口但未完整接线"：RunBios 补号窗口存在但 `autoreg.enabled=false`；Grok 只有 `pool_window_*` 键、Go 未实现；QwenWork/Skywork 是真正实现的。
> - **A1 的 ◐** 指"后端有池、面板没有账号管理页"（Flonno）。

### 3.4 按你列的 9 项逐项点名

| 你列的功能 | 真正具备的插件 | 说明 |
|---|---|---|
| 账号池（管理 / 积分 / 批量签到） | 管理：有 UI 的是 QwenWork / Skywork / BuddyHub / Grok / LobsterAI / LobsterHub / MiniMax / xhx / RunBios（Flonno 仅后端 `pool.json`，无管理页）；积分：全部号池型都有；批量签到：BuddyHub / LobsterAI / LobsterHub / MiniMax | 签到是上游政策，不是每家都有 |
| 对话调试 | QwenWork / Skywork / BuddyHub / Grok / LobsterAI / LobsterHub / MiniMax | RunBios/Flonno/xhx 没有；LOOMy 仅独立版有 |
| 调用日志（模型/时间/扣积分） | **完整**：QwenWork / Skywork / LobsterHub；**部分**：RunBios(成本) / BuddyHub(无扣分) / MiniMax(tokens) / Grok(原始日志) / Flonno(无 UI) / xhx(仅 state.json) | 这是差距最大的一项 |
| Clash 对接（多出口 / 注册多 IP） | 对话守护：QwenWork / Skywork；真正注册注入：Flonno / RunBios；外包 worker：Grok | QwenWork 的注册**没有**代理注入 |
| 夜间自动注册 | QwenWork（04:00-06:00，目标 5）/ Skywork / RunBios（补号窗口，当前关闭）/ Grok（配置有键但 Go 未实现） | Flonno 仅手动/浏览器注册；未见定时窗口 |
| 负载均衡 / 错误转移 / 无感切号 | LB：BuddyHub / LobsterAI / LobsterHub / RunBios / Flonno / xhx；切号：几乎所有号池型 | QwenWork 是"轮询 + 粘住当前号"，不是加权 |
| OAuth2 登录 / 自动注册 | OAuth：QwenWork / Skywork / BuddyHub / LobsterAI / LobsterHub；自动注册：QwenWork / Skywork / Flonno / RunBios / Grok / MiniMax | LobsterAI/Hub 是"登录 + 人工现场注册" |
| 临时邮箱管理 | Skywork（4 源）、RunBios、Flonno、Grok、MiniMax（4 源）、QwenWork（仅 temp.tf） | 其他没有 |
| 日常签到 / 打卡 | BuddyHub（7 任务）/ LobsterAI / LobsterHub / LOOMy（宠物打工）/ MiniMax（signin） | QwenWork/Skywork/RunBios/Flonno/Grok/xhx 无 |

### 3.5 结论：功能应分三档，而不是"全都要"

- **必备（所有号池型）**：A1、A3、C2、E1、E2、E3。
- **强烈建议**：B1、B2（含扣分）、C1、A5、**E5（事件日志）**。
- **按上游政策**：A4 / D1 / D2 / D3 / D4 / D5 / D6 / C3 —— 上游没有注册或签到，就不该硬加。
- 你的 9 项清单可以作为"能力目录"，但不能作为"每个插件都必须有"的验收表；否则会给 LOOMy/VibeX 这类单账号插件增加无意义复杂度。

### 3.6 事件日志（E5，新增通用能力）

> 你提的"什么时候注册、签到"属于**事件**，不是**请求日志**。现在各插件只有原始日志文件（`qw2api.log`、`plugin.log`、`register-*.log`），没有可筛选的时间线。

**事件模型**（建议所有插件统一）：

| 字段 | 示例 | 说明 |
|---|---|---|
| `ts` | `2026-11-04T15:36:49+08:00` | 事件时间 |
| `level` | `info/warn/error` | 级别 |
| `plugin` | `grok` | 插件 id |
| `type` | `register.success` | 事件类型（见下） |
| `account` | `xxx@gmail.com` / `Cody` | 关联账号（可空） |
| `message` | `注册成功，已入池` | 人读文案 |
| `meta` | `{"model":"...","credits":12.5,"attempt":2}` | 结构化补充 |
| `request_id` | `req_xxx` | 与调用日志关联 |

**建议事件类型**：
- 账号：`account.add` / `account.delete` / `account.disable` / `account.import`
- 注册：`register.start` / `register.success` / `register.fail` / `register.import`
- 签到：`checkin.start` / `checkin.success` / `checkin.fail` / `task.done`
- 调度：`switch.quota`(402) / `switch.rate`(429) / `switch.auth`(401) / `switch.error`(5xx)
- 额度：`quota.refresh` / `quota.low` / `quota.reset`
- 系统：`plugin.start` / `plugin.stop` / `config.change` / `net.switch`

**存储与接口**：
- `data/events.jsonl` 追加写；按 10MB × 5 轮转（或保留 30 天），启动时加载尾部 N 条。
- `GET /api/events?level=&type=&account=&since=&limit=`；`GET /api/events/export` 导出 CSV/JSONL。
- 写入失败只降级到 stderr，不影响主流程。

**面板**：
- 左侧导航新增"日志"；顶部 chips：全部 / 注册 / 签到 / 切号 / 账号 / 异常。
- 表格列：时间 / 级别 / 类型 / 账号 / 内容；支持搜索、时间范围、自动刷新（5s）、导出。
- 导航上显示未读错误数徽标；空状态给"还没有事件"。

**各插件接入优先级**（先接有注册/签到的）：
- 第一梯队：Grok（注册/删除/额度）、QwenWork（夜间注册/切号）、BuddyHub（7 任务）、MiniMax（签到/注册/重登）、LobsterAI/LobsterHub（签到/登录）。
- 第二梯队：RunBios（注册/补号）、Skywork、Flonno、xhx（登录/刷新）。
- VibeX 无事件来源，可不接。

**工作量**：共享 `eventlog` 参考实现（约 200 行）0.5 人日；每插件接入/面板 0.5 人日；先做 6 个 ≈ **3 人日**。

---

## 4. UI 审计与统一设计系统（对应问题 2）

### 4.1 现状 tokens 对照

| 插件 | 主题 | 背景 | 卡片 | 强调色 | 圆角 | 距目标 |
|---|---|---|---|---:|---|
| **RunBios（基准）** | 暖白 | `#f5f7f4` | `#ffffff` | 鼠尾草绿 `#5f9e7c` | 16px | 基准 |
| QwenWork | 暗色默认 / 可切亮 | `#1f1f1f` / `#fcfcfc` | `#262626` / `#fff` | 品牌绿 `#2ccc6c` | 6~24px | 近（改默认亮色 + 降饱和） |
| Skywork | 同 QwenWork | 同上 | 同上 | `#30bf69` / `#2ccc6c` | 同上 | 近 |
| BuddyHub | 暗底 + 米白卡 + 荧光黄绿 | `#0b0d0b` | `#f1efe8` | 荧光 `#b8f53d` | 11px | 远（色相/饱和度都不对） |
| LOOMy | 深色 | `#0b0f17` | 深色 | 靛紫 `#6366f1` | — | 远 |
| LobsterAI | 冷灰 + 苹果蓝 | `#f0f0f4` | `#fff` | `#007aff` | 12px | 中（结构接近，换色 + 暖白） |
| LobsterHub | 同 LobsterAI（插件面板） | `#f0f0f4` | `#fff` | `#007aff` | 12/14px | 中 |
| MiniMax | 纸墨印刷风 | `#F3EEE2` | `#fff` | 朱红 `#E0522D` | **3px** | 远（字体 Anton、零圆角、朱红） |
| 商汤 xhx | 全暗 | `#0f1117` | `#181b24` | 靛蓝 `#4c6ef5` | — | 远（需整体反色） |
| Grok | 浅灰白 | `#f6f7f9` | `#fff` | 蓝 `#2563eb` | 10px | 近（只换 accent） |
| Flonno | 浅灰 + 蓝 | `#f5f6f8` | `#fff` | `#1f6feb` | 8px | 近但**只有一个注册页** |
| VibeX | 无面板 | — | — | — | — | 无 UI |

### 4.2 目标设计系统："抹茶奶酪"（Matcha & Cheese）

**关键词**：暖白、奶油、抹茶、奶酪黄、鼠尾草；低饱和；大圆角；细边框；软阴影；数字等宽；**禁用粉色**。

```css
:root{
  /* 底色 */
  --bg:#F7F6F1;            /* 奶油白 */
  --bg-soft:#FBF9F4;       /* 更浅的奶油 */
  --surface:#FFFFFF;       /* 卡片 */
  --surface-2:#FAF8F2;     /* 次级面 */
  --line:#E8E6DE;
  --line-2:#F0EEE7;

  /* 文字 */
  --ink:#26302B;
  --ink-2:#4F5B54;
  --muted:#8A938C;

  /* 抹茶 */
  --accent:#7C9A6D;
  --accent-strong:#5F7D52;
  --accent-soft:#EFF4EA;

  /* 奶酪 */
  --cheese:#E9C46A;
  --cheese-soft:#FBF3DF;

  /* 语义（不用粉） */
  --ok:#6F9E7A;    --ok-soft:#EEF5EF;
  --warn:#C99A4A;  --warn-soft:#FAF1DF;
  --bad:#C06A5A;   --bad-soft:#FAEDEA;   /* 陶土红 */

  /* 形状 */
  --r-card:14px;
  --r-ctrl:10px;
  --r-chip:999px;

  --shadow:0 1px 2px rgba(38,48,43,.04), 0 10px 24px rgba(38,48,43,.05);
  --sans:"Segoe UI","PingFang SC","Microsoft YaHei",-apple-system,sans-serif;
  --mono:"Cascadia Code",Consolas,"SFMono-Regular",monospace;
}
```

**与 RunBios 的差异**：把 `#f5f7f4` 调成更暖的 `#F7F6F1`，主色从 `#5f9e7c` 稍降饱和到 `#7C9A6D`，新增奶酪黄作为"奖励/积分/高亮"辅色；危险色用陶土红而不是正红/粉红。

**布局规范**
- 侧栏 220px（白底、1px 右边框、品牌块 + 导航 + 底部网关状态）。
- 主区最大 1320px，页头（标题 + 说明 + 右侧主/次按钮）。
- 卡片：白底、1px `--line`、14px 圆角、软阴影、20/22px 内边距。
- 统计卡：3 列，标签 11.5px，数字 23px 等宽。
- 表格：表头 `--surface-2` 粘顶，12.5px，hover 行浅底。
- Tabs：浅绿底槽 + 白色 active；Badge：低饱和底 + 深字。
- Toast：底部居中，深墨底白字，左侧 3px 语义色条。
- 空状态：一行说明 + 一个主操作，不要留大白块。

**纪律**
- Light-first；暗色作为后续"暖夜"主题，不在本轮。
- 状态不能只靠颜色：徽标 + 文本 + 图标。
- 对比度 ≥ 4.5:1；焦点态用 `--accent` 3px 外发光。
- 数字/模型名/端口一律等宽字体。

### 4.3 各插件改造清单与工作量

| 插件 | 改法 | 关键工作 | 人日 |
|---|---|---|---:|
| RunBios | **不动**（保持现状） | — | 0 |
| QwenWork | token 替换 + 默认亮色 + 侧栏化（可分批）+ 日志加真实模型列 | `index.html` 101KB 单文件；CSS 变量 + 顶栏改侧栏 | 1.5~2 |
| Skywork | 与 QwenWork 同源，同步改（本轮可选） | 同一套 token / 布局 | 1.5 |
| BuddyHub | 暗转亮 + token + 日志补扣分/TTFB 列 | `panel.html` 123 行，`panel.go` 1 处 `:root` | 0.5~1 |
| LOOMy | 暗转亮 + token + 补"模型/最近请求"只读卡 | `panel.go:219-329` 内嵌页 | 0.5~1 |
| LobsterAI | 苹果蓝→抹茶 + 暖白 + 圆角 | `web/assets/app.css` 70KB，22 处硬编码蓝 | 1 |
| LobsterHub | 同上 + 插件面板/Wails 桌面两套（桌面可选） | `panel_page.go` 51KB | 1（+2 桌面） |
| MiniMax | 印刷风→抹茶清新：换字体（去 Anton）、圆角 3→10/14、朱红→抹茶、重排卡片 | `web/index.html` 64KB | 2~2.5 |
| 商汤 xhx | 暗→亮、靛蓝→抹茶、清 20+ 硬编码 hex | `panel.go` 28KB 字符串，改完需重编译 | 1~1.5 |
| Grok | 蓝→抹茶 + 状态色/圆角统一 | `web/panel.html` 1156 行，变量集中 | 0.5 |
| Flonno | 只有注册页 → 建统一 shell（账号/注册/日志/设置） | `internal/panel/panel.go` 需扩页 | 2.5~3 |
| VibeX | 无面板；保持无面板或新建（+3） | — | 0 |

**纯 UI 合计约 12~15 人日**（含 2 人日截图/对比度/回归验收）；不含功能改动。

### 4.4 实施方式建议

- **不要跨仓库共享 Go 包**：每个插件是独立仓库/独立二进制，引入共享模块会把发布耦合在一起。
- 用"**设计令牌 + 组件 CSS 快照**"：
  - 维护一份 `ui/tokens.css` + `ui/components.css`（约 300 行），每个插件 `go:embed` 时带版本号；
  - 每个插件只写"业务组件"样式，通用卡片/表格/按钮/徽标/toast 用快照；
  - 加一个脚本检查硬编码 hex（白名单 `#fff` 等），防止下次又漂移。
- 分两阶段：
  - **P2a（低风险）**：只换 token（颜色/圆角/字体/阴影），不动布局；
  - **P2b（中风险）**：统一 shell（侧栏/页头/卡片/表格），逐插件做。
- 验收：每插件 5 个主页面 × 亮色截图；对比度检查；功能点击回归（不改逻辑）。

### 4.5 动效与创意：P5.js / Barba.js / Uiverse 评估

**先说结论：**

| 方案 | 适合吗 | 原因 |
|---|---|---|
| **P5.js** | **谨慎/局部** | 适合"茶园/蒸汽"这类生成式 hero，但要引入 ~1MB 库 + 常驻 canvas 循环；后台面板以表格/表单为主，大面积 canvas 会拖性能、挡点击、伤可读性。只在登录页/空状态/关于页做**小面积**装饰可以。 |
| **Barba.js** | **不建议** | 它是"多页站点 + PJAX 转场"的库；我们的插件是**单 HTML + 前端切视图**（`render()`/`tab`），没有真实页面跳转。用原生 **View Transitions API**（`document.startViewTransition`）或 CSS class 切换即可，零依赖。 |
| **Uiverse.io** | **推荐（取其形）** | 按钮/开关/卡片的 hover/press 微交互很适合；但不要整段抄霓虹/3D/彩虹，挑"低饱和、轻位移、细边框"的款式，统一到抹茶 token。 |

**动效系统（建议写进 tokens.css）**

```css
--dur-1:120ms;  --dur-2:200ms;  --dur-3:320ms;
--ease:cubic-bezier(.22,1,.36,1);
/* 只动 transform / opacity / filter；不动 width/height/top/left */
@media (prefers-reduced-motion: reduce){
  *{animation-duration:.01ms!important;transition-duration:.01ms!important;}
}
```

**10 个具体效果（按性价比排序）**

| # | 效果 | 用在哪 | 实现 | 成本 |
|---|---|---|---|---|
| 1 | **统计数字 count-up** | 总览 KPI | `requestAnimationFrame` 600ms，首次进入才跑 | 低 |
| 2 | **额度条 fill + 低额度呼吸** | 账号池/用量 | 宽度从 0 过渡；<10% 时 amber 缓慢 pulse | 低 |
| 3 | **状态点 pulse** | 网关/插件状态 | 2s 柔和 ring，仅 running；error 只抖一次 | 低 |
| 4 | **表格行 stagger** | 账号/日志列表 | 4px 上移 + 20ms 递增，最多 12 行 | 低 |
| 5 | **Toast 弹簧** | 保存/注册/签到 | translateY + 轻微 overshoot；成功勾选 morph | 低 |
| 6 | **按钮微交互**（Uiverse 风格） | 主 CTA | hover 抬 1px + 阴影；active scale(.98)；渐变描边 | 低 |
| 7 | **View Transition 页面切换** | 侧栏/顶栏切页 | 原生 API + 绿色柔光 wipe；不支持则无动画 | 中 |
| 8 | **骨架屏 shimmer** | 首屏/表格 | 1.2s 线性渐变，内容到达即撤 | 低 |
| 9 | **成功茶点彩带** | 注册成功/签到成功 | 8~12 个茶叶/奶酪方块，800ms 一次性；`reduced-motion` 关闭 | 中 |
| 10 | **命令面板 Ctrl+K** | 全局 | 模糊背景 + scale-in；搜页面/操作 | 中 |

**P5.js 如果一定要用**：只放在"关于/空状态"做 30fps 的抹茶粒子/蒸汽，`IntersectionObserver` 离开视口暂停，`prefers-reduced-motion` 直接静态图；不要放在表格页。

**Barba.js 的替代**：用 View Transitions API；不支持的老浏览器直接瞬切，不做 polyfill。

**性能/体验红线**：
- 只动 `transform/opacity/filter`，不动布局属性；单页同时最多 3 个动画。
- 页面隐藏时暂停（`visibilitychange`）；长列表用 `content-visibility:auto`。
- 不做无限循环的背景动画（除非 20s+ 极慢且可暂停）；不做闪烁 >3Hz。
- 所有动效必须有 `prefers-reduced-motion` 降级。
- 后台面板的第一目标是"看得清、点得准"，动效只做"状态反馈"和"空间连续性"，不做炫技。

**落地顺序**：先在 QwenWork 面板做 1/2/3/5/6 五项（低风险），确认观感后再抽进 `components.css`，随 P2a 推广到其它插件；7/9/10 作为 P2b 的加分项。

---

## 5. 单插件深入

### 5.1 QwenWork（问题 2）

#### 5.1.1 对话功能：实测到的事实

**基础链路是通的**：我通过宿主 50080 + `sk-unified-local` 测了 `qw2api/qwork-auto` 与 `qw2api/qwork-advanced`，非流式/流式都返回 200 和"你好"，命中账号 Cody，日志有积分/Token 记录。所以不是"完全不能用"。

但有 5 个确定缺陷：

| # | 缺陷 | 证据 | 用户感受 |
|---|---|---|---|
| 1 | **流式错误被吞**：前端只处理 `choices[0].delta`，忽略 `error` 帧；服务端先写 200 + `debug_meta` 再转发，错误无法回退 | `index.html` 的 `relaySSE`；`debugchat.go:270-300` | 失败时只显示"(无内容返回)"，看不到原因 |
| 2 | **命中卡可能取错记录**：流式结束 `dbgFetchHit(seq, null)` 恒取 `logs[0]` | `index.html` 约 2230 行 | 并发时显示别的请求的账号/积分 |
| 3 | **没有多轮**：面板只有一个 textarea，每次都是单轮 prompt，无历史/系统提示/温度 | `index.html:761` | 不是"对话"，只是"单次测试" |
| 4 | **真实模型不显示** | 见 5.1.2 | 不知道背后到底是哪个模型 |
| 5 | **空回复有上游原因**：`fail-dumps` 共 50 个（最新 11-03 08:38 为 `"上游未返回任何内容（账号 yorhun27）"`）；日志有 402 `All models failed` / 429 `All backends failed` 后换号 | `data/fail-dumps/`（50 个）、`qw2api.log` | 偶发空回复；不知道试了哪些号 |

**建议改法（按性价比）**：
1. 解析并展示 SSE `error` / `usage` / `finish_reason`（服务端 + 前端）。
2. 命中卡按 `request_id` 精确取记录，而不是 `logs[0]`。
3. 增加多轮 message 数组（可选，工作量 0.5 人日）。
4. 失败时展示 failover 轨迹（尝试了哪些账号/错误类型/最后原因）。
5. 对话区增加"请求模型 → 真实模型"对照（见下）。

#### 5.1.2 真实响应模型：在哪、怎么显示

**真实模型确实存在，且已有现成解析先例**：
- 上游 SSE 是双层封装：`data: {"headers":...,"body":"<内层JSON>","statusCodeValue":200}`。
- 内层 JSON 有 `model` 字段，也有 `raw_usage.model`（`cmd/creditprobe/main.go:90-160` 就是用这两个字段拿到"真实模型名"的）。
- 插件的 `upstream.StreamEvent` **已经带 `Model`**（`internal/upstream/client.go:583`、`:600`），说明解析层拿到了。
- 但 `internal/server/chat.go` 在写 OpenAI 帧时固定用**请求的** `model`（`:210`、`:265`、`:300`），`RequestRecord.Model` 也只记请求名（`reqlog.go:17-22`）→ 真实名在中途被丢掉。

**建议实现面（不写代码）**：

| 层 | 改动 | 说明 |
|---|---|---|
| 解析 | `StreamEvent` 增加 `RawUsageModel`，解析 `raw_usage.model`；优先它，其次 `chunk.model` | 兼容上游只给其中一个 |
| 记录 | `RequestRecord` 增加 `RealModel string json:"real_model"`；流式/非流式/`settleUsage` 都写入 | 面板与导出都能用 |
| 面板-日志 | 请求日志表新增"真实模型"列（在"请求模型"旁，弱化样式） | 最直观 |
| 面板-调试 | `debug_meta` / 命中卡显示 `请求模型 → 真实模型` | 调试时一眼看到 |
| 可选-API | 响应头 `X-Upstream-Model: <真实名>` | 宿主是否透传需实测；插件自身端口可透传 |

**验收**：用 `qwork-auto` 发一次，面板日志/命中卡显示的真实模型与 `creditprobe` 输出一致；换 `qwork-advanced` 时真实模型随之变化。

#### 5.1.3 "探测链路 / 链路设置"到底是什么、有没有用

| 按钮 | 实际动作 | 代码 | 当前状态 |
|---|---|---|---|
| **探测链路** | `POST /api/clash/probe` → 读取 provider.json 的 Clash 配置 → 构造 mihomo client → 对该分组每个真实节点打 delay → 返回延迟/存活/最快节点；**明确不改当前选择** | `internal/server/clashpanel.go:120-190`；`internal/upstream/clash.go:151+` | `clash_api=""` → 返回"尚未配置" |
| **链路设置** | 写 `clash_api / clash_secret / clash_group / clash_auto_switch / clash_interval_sec / bad_delay_ms / bad_streak / cooldown_sec` 到 provider.json，3 秒热重载 | `cmd/qw2api-plugin/clashpanel.go:56-100` | 可用，但写入后仍空配置则守护不启动 |

**它到底用在哪：**
- **对话**：`startNetShield` 只在 `clash_api` 非空时启动；`auto_switch=false` 时只探测不切换（`internal/upstream/netshield.go:1-30`、`cmd/qw2api-plugin/main.go:293-319`）。
- **注册**：**完全没接**。`internal/autoreg` + `internal/browser` 全仓 grep 不到 `--proxy-server` / `ProxyServer` → 注册浏览器走本机出口，与 Clash 无关。
- **结论**：它是"对话出站稳定"的可选守护，**不是多 IP 注册工具**。当前默认关闭，所以你觉得"不知道有什么用"是合理的。
- **建议**：面板加状态徽标（未配置 / 仅探测 / 自动切换），并把文案改成"仅影响对话出站，不影响注册"；若确实要注册换 IP，需要单独做（P3）。

### 5.2 MiniMax 积分/数据持久化（问题 4）

#### 复现与证据

- 实测 `GET http://127.0.0.1:11455/v1/accounts`：104 个账号，`remainingCredits / creditsTotal / balanceUpdatedAt` **全部为空**（15:20 查询，插件 14:44 重启过）。
- `balance()` 成功后只写 `balanceCache`（`handlers.go:474-491`），**从不 `save()`**；`accounts.json` 的 `Account` 结构没有积分字段（`minimax.go`）。
- `syncWorkerPool` 只调 `app.balance`（`handlers.go:496-508`），不落盘。
- 前端 `syncAll()` 完成后只 `refreshAll()`（summary），**不调 `loadPool()`**；`liveTick()` 在 pool tab 只更新进度条；2.5s/8s 两个定时点在"104 账号 × 10 并发"下早于同步完成（`web/index.html` 的 `syncAll/liveTick`）。
- 页面切换本身不丢（纯前端 `render()`），但"刷新页面 / 重进插件 / 插件重启"会重新 `loadPool()` → 服务端缓存空/旧 → 看起来丢了。

#### 两条根因

1. **后端**：积分是内存缓存，没有持久化。插件进程一重启（宿主升级/改配置/崩溃重拉）就全空。
2. **前端**：同步完成后不刷新账号列表；进度轮询不触发最终 reload；定时刷新早于完成。

#### 修复面（文件/接口级）

| 层 | 改动 | 验收 |
|---|---|---|
| 存储 | `Account` 增加 `Balance *BalanceInfo json:"balance,omitempty"`，或独立 `data/balance-cache.json`；`balance()`/`balanceLight()` 成功后 debounce 落盘；`load()` 时回填 `balanceCache` | 重启后 `/v1/accounts` 仍有积分 |
| 路径 | `accounts.json` 迁到 `data/`（兼容：`data/accounts.json` 不存在则读根，写时写 data 并保留根只读一版） | 旧目录仍能启动；新数据在 data/ |
| 前端 | 轮询 `/v1/accounts/sync/status`，`running=false` 时 `await loadPool(); render()`；pool tab 的 `liveTick` 改为 `loadPool()`（或合并积分）；同步期间禁用重复点击 | 同步结束 3s 内表格自动更新 |
| 其它内存态 | `recentCalls`（80 条）至少落 `data/recent.json`；`modelsCache`/网关计数/`schedPos` 可接受内存 | 刷新后最近调用仍在 |
| 迁移 | 一次性：旧 `accounts.json` → `data/accounts.json`；保留根文件只读回退一版 | 104 个账号不丢 |

**工作量**：1.5~2 人日（含迁移与测试）。

### 5.3 其余插件逐条

#### BuddyHub（不新增功能，UI）
- 合规：所有运行期写入都在 `data/`（`auths`、`state.json`、`task-state.json`、`usage.json`、`usage-log.json`）。
- 功能强项：7 个日常任务（签到/旅行/活跃/保活/夜猫子/连登/开学季）、加权+轮询+粘性、402/429/401/5xx 换号、OAuth device 扫码。
- 缺口：无导入导出、无单号积分查询、无调用日志扣分列、无 Clash/代理、无自动注册。
- UI：暗底 + 米白卡 + 荧光黄绿，改成暖白 + 抹茶；`panel.html` 很小，0.5~1 人日。

#### Flonno（不新增功能，UI）
- 面板只有一个"账号池 · 自动注册"页（720px，蓝 `#1f6feb`），不像一个网关控制台。
- 后端其实有：`pool.json`（账号/余额/quota）、`usage-log.json`、`scheduler`（策略/日限）、`clash`（注册轮换）、`autoreg`（浏览器）、`sources`（临时邮箱）。
- UI 优化 = 从"注册页"扩成"总览/账号/注册/日志/设置"五页；工作量 2.5~3 人日，是 10 个里最大的。
- 数据：`data/pool.json` 合规；但通用浏览器 profile 在 `~/.qw2api`（见 2.2 B）。

#### Grok（本轮修账号/额度/轮询/日志，详见 5.4）
- 面板浅色 6 tab（总览/账号/注册/对话测试/运行时/设置），`--accent:#2563eb`，改成抹茶即可，0.5 人日。
- 功能：账号池/额度刷新、注册任务、对话测试、CPA OAuth 导入、临时邮箱（自建 CF worker）、Turnstile（turnstilePatch + 浏览器）、DrissionPage 外部注册器。
- 其它问题：
  - `pool_window_start/end`（01:30–05:30）**Go 源码 0 处引用** → 夜间窗口只是配置，没实现；
  - `register_clash_*` 键 **Go 层没接**，只传单个 `register_proxy` → 目前等于"固定 Clash 混合口"，不是多 IP 轮换；
  - 安装目录含源码/注册器/runtime/5 个 exe.bak，升级覆盖风险高；
  - `data/logs` 109.7MB 无轮转；
  - `provider.json` 只暴露 `grok-4.7`，`provider-magpie.json` 才列 8 个真名。
- 数据：账号在 `data/accounts` 合规，但整体打包不合规（见 2.2 D）。

#### LOOMy（不新增功能，UI）
- 插件面板是 `panel.go:219-329` 的**深色页**，不是 `index.html`（`index.html` 只在独立模式 7777 用）；装的是 `panel.go` 那套。
- 单账号、纯协议、零落盘：session 在 `provider.json`；积分/领取/日志全内存；无账号池/Clash/注册/切号。
- 每日"宠物打工"领 1000 积分（`petwork.go`），每 30 分钟自动领。
- `points_alert_threshold` 与 `xfyun_*` 在插件模式**不生效**（结构体没读 / 只在独立模式用）。
- UI：暗转亮 + 抹茶，0.5~1 人日；若要补"模型/最近请求"卡，再加 0.5。

#### LobsterAI / LobsterHub（不新增功能，UI）
- LobsterAI：
  - OAuth 登录（portal + `127.0.0.1` 回调 exchange），不是 device-code；
  - 浏览器是系统 Chromium + `%TEMP%\lb2a-login-*` 一次性 profile，**没有 chrome-autoreg**（合理）；
  - 有账号池、积分、批量签到、Clash 绑定/探测、负载均衡、402/429 切号、日常签到；
  - **auth_dir 绝对路径指向源码仓库**（最严重的合规问题）；
  - 调用日志只有计数，没有流水。
- LobsterHub：
  - 有账号池、积分、签到、对话调试、用量（模型/tokens/积分 x→y）、Clash 绑定（无批量探测）、负载均衡、402/429 切号；
  - 数据默认 `%APPDATA%\LobsterHub`；与 LobsterAI 共用 `clash.json` 但锁不同源；
  - UI 是"苹果蓝"面板，改抹茶 + 暖白，1 人日；桌面 Wails 另一套（可选 +2）。
- 两者都**没有自动注册/临时邮箱/Turnstile**；LobsterAI 的"注册"其实是人工在浏览器里完成，插件只负责打开一个干净窗口并接收 OAuth 回调。

#### 商汤 xhx（不新增功能，UI）
- 暗色单页 4 卡（接入信息/登录/选号设置/账号池），`--brand:#4c6ef5`；改白+抹茶要整体反色，1~1.5 人日。
- 有账号池、积分、`balanced` 选号、402/429 切号 3 次、浏览器登录（Edge 优先）+ 密码自动重登。
- **没有自动注册**（README 明示），不处理 CF（验证码用户手过）。
- `data/browser-profile` 是**系统 Edge 的完整 user-data-dir**（986MB），真实数据只有 ~1MB；需要 TTL/清理。
- 面板不展示 per-call 模型/积分，但 `state.json` 里有 `history`（at/model/points/tokens）——加一个只读表就能补上（但按你的要求，本轮不加功能）。

#### VibeX（不新增功能）
- 无面板、无 data/、单 cookie（`provider.json`）、无账号池/日志/注册。
- 它更像"一次性接入适配器"，不适合套用号池型 UI；建议本轮**不改**（或只做数据说明）。

#### RunBios（保持现状）
- 不做任何改动（包括 UI、数据路径）。它是本次 UI 的基准。
- 备注：它的 `data/pool.json` / `usage/` / `registrations/` 是合规范本；但通用浏览器 profile 也在 `~/.qw2api`（2.2 B），如果哪天要修数据合规，需要单独开一项。

### 5.4 Grok 专项：账号删除复活 / 额度视图 / 轮询 / 日志

> 对应你这次补充的问题 1：注册、轮询、额度查看这三个主用法都不太行，删除的账号还会回来。
> 参考实现：`D:\AI_Projects\grok2api`（v3.1.8，与插件内置 runtime 同版本）。

#### 5.4.1 「删了又回来」的四条复活路径（已定位）

| # | 路径 | 证据 |
|---|---|---|
| **1** | **DB 删了，文件没删**：账号来源是 `data/accounts/xai-*.json` 与 `*.txt`；面板删除只调 grok2api，从不删这些文件 | `panel.go:246-250` 只 `BatchDelete`；`admin.go:264-268` 只发 `provider+ids`；`import.go` 的 `scanBuildEntries/scanWebTokens` 只读不删 |
| **2** | **注册后无条件全量重导入**：每次注册任务结束（成功或 stopped）都 `runImport(ctx,true,true)`，把目录里**所有**文件重新导入；已删账号因为文件还在 → 原样复活 | `jobs.go:327`；`import.go:180+` |
| **3** | **自动导入开关**：`auto_import=true` 时启动即全量导入；当前配置是 false，但打开就会复现 | `main.go:158-183`（`:169` 检查 `cfg.AutoImport`） |
| 4 | **关联账号残留**（次要）：grok2api 支持 build/web/console 关联；插件删除没传 `linkedDeleteTargets`，关联账号会留下，同邮箱看起来"回来了" | 参考 `handler.go:476-500` + `POST /accounts/deletion-preview`；插件 `admin.go:264` 未使用 |

**结论**：不是 UI 缓存、不是 grok2api bug，是**凭证文件的生命周期没有跟着账号走**。只要还会注册新号（触发全量导入），已删账号就会一直回来。

**修复方案（按顺序做）**：
1. **删除时同步清文件**：先按账号 `email`/`userId` 预览命中的 `xai-*.json` 与 `*.txt` 行；用户确认后，删 DB + 删文件（`.txt` 用"删除匹配行"而不是整文件删，避免误删同文件其它账号）。
2. **注册后只导入新增**：`runImport` 改为"先扫目录得到文件指纹（path+mtime+size），只导入本次新增/变更的文件"，不再全量 re-import。这样即使旧文件还在，也不会把已删账号带回来。
3. **孤儿文件清理**：加一个维护动作——列出"文件里有、DB 里没有"的账号，一键清理；同时列出"DB 里有、文件里没有"的（仅提示，不自动删）。
4. **删除确认升级**：弹窗显示"将删除 N 个账号 + M 个本机凭证文件 + K 个关联账号"，关联账号用 `deletion-preview` 查，勾选后传 `linkedDeleteTargets`。
5. **`auto_import` 警示**：打开时明确提示"会把目录里所有历史凭证重新导入，包括已删除账号"，默认保持 false。

**验收**：
- 删除 1 个账号 → 文件命中被清 → 再跑一次注册任务 → 该账号**不再出现**；
- 删除后重启插件 → 不复活；
- `deletion-preview` 显示的关联账号数与实际删除数一致。

#### 5.4.2 额度查看：参考实现 vs 插件（差距最大）

**参考实现**（`account-quota.tsx`）按账号类型分层：
- **free**：`used / limit`（按百万 token 格式化）、`usagePercent` 进度条、估算标记、`nextProbeAt`/重置时间；
- **build（paid）**：周额度百分比 + 月额度 `used/limit`，重置时间、billing 详情；
- **console**：`quotaWindows` 分窗（auto/fast/expert/heavy/image/video），每窗 remaining/total/percent/reset；
- 账号行还显示 `priority / maxConcurrent / minimumRemaining / failureCount / cooldownUntil / lastUsedAt / linkedAccounts`。

**插件现状**：
- Go DTO 先把字段丢了：`QuotaInfo` 只有 `type/remaining/limit/status/limitKnown`（`admin.go:161-176`），参考实现还有 `used/usagePercent/unit/periodStart/periodEnd/nextProbeAt/source/confidence`；
- `QuotaWindow` 有 `mode/remaining/total/usagePercent/resetAt/source`，但 UI 只取 `auto` 或第一个窗口；
- 表格只有一格"额度"：`remaining/limit` 或 `main window`（`panel.html` 约 600 行），没有进度条、重置时间、周/月拆分、billing、详情弹层；
- `Priority/MaxConcurrent/MinimumRemaining` 在 `Account` 结构里，但面板不显示、不可编辑。

**修复方案**：
1. 扩展 `QuotaInfo` 到参考字段（至少 `used/usagePercent/unit/periodEnd/nextProbeAt/source/confidence`）。
2. 账号表"额度"列改成：进度条 + `used/limit` + 重置时间（悬浮/详情看全量）。
3. 点账号行开详情抽屉：free/build/console 三种视图 + billing + quotaWindows 分窗。
4. 顶部加"刷新全部额度"；批量操作保留"刷新额度"。
5. 账号行加 `priority / maxConcurrent / minimumRemaining` 的内联编辑（也服务于 5.4.3 的轮询）。

**验收**：与参考实现同账号并排对比，free/build/console 三种类型的显示一致；重置时间与 grok2api 后台一致。

#### 5.4.3 轮询调用：插件是薄代理，没有自己的调度

- 插件 `proxy.go:71-91` 只把请求转给 grok2api，**没有任何选号/权重/粘性代码**；账号选择完全由 grok2api 的 selector 决定。
- grok2api 的选择是 **priority + maxConcurrent + minimumRemaining + 冷却 + sticky 会话** 的组合；`RoutingConfig` 主要是 `stickyTTL/cooldownBase/cooldownMax/maxAttempts/preferFreeBuild/segmented`，**没有严格 round-robin 模式**。
- 所以"轮询不生效"通常不是插件 bug，而是：
  1. **sticky** 把同一会话/同一 `affinityKey` 钉在一个号上（看起来永远不换）；
  2. 各账号 **priority 不均**，selector 优先高优先级；
  3. 某个号额度高/冷却短，被反复选中；
  4. 插件没有暴露这些旋钮，用户无法把行为调成"均匀轮询"。

**能做到的（插件侧，不改 grok2api）**：
1. 账号表暴露 `priority / maxConcurrent / minimumRemaining` 并可批量设置；
2. 加一个"**轮询友好**"预设：所有号 `priority` 相等、`maxConcurrent` 相等、`minimumRemaining=0`，并把 sticky 调短/关闭（如果 grok2api 设置接口可写）；
3. 调用台账增加"命中账号 / 本次是否换号 / 换号原因"，让用户能验证是否真的在轮询；
4. 增加"最近 100 次调用的账号分布"小图（按账号计数），一眼看出偏斜。

**做不到的（要说清楚）**：严格 round-robin 需要 grok2api 的 selector 增加 `round_robin` 模式——那是另一个仓库（`D:\AI_Projects\grok2api`）的改动，插件单独做不到。建议先做"轮询友好"预设，若仍不满足，再单独立项改 grok2api。

#### 5.4.4 日志：现在只有原始日志抽屉

- 插件有一个"日志"抽屉（`openLogs()`），可选 plugin / grok2api / register 三类**原始文本**；
- 没有结构化事件、没有按账号/类型筛选、没有"注册成功/失败"时间线；
- 注册任务有 `data/jobs/*.json`（含 Registered/Failed/status/时间），但面板没有把它渲染成事件流。

**建议**：按 3.6 的 E5 事件模型接：`register.start/success/fail`、`account.delete`、`quota.refresh`、`switch.*`、`plugin.start/stop`；面板"日志"页用 chips 筛选 + 时间线 + 导出。Grok 是第一梯队（注册/删除/额度事件最全）。

### 5.5 上下文 / 最大输出：哪些是真拿不到，哪些是有数据没透出

> 宿主 `vendors/remote` 只解析 **`context_window`** 与 **`max_output_tokens`**（`remote.go:107-108`）；只写 `context_length` 会被忽略。

| 插件 | context_window | max_output_tokens | 判定 |
|---|---:|---:|---|
| **MiniMax** | 512,000（可选 1M） | 128,000 | ✅ 默认 512K 真拿到；1M 是目录 options 里的可选档，插件未透出（见 5.6） |
| **BuddyHub** | 1,000,000 / 960,000 | 128,000 / 64,000 | ✅ 真拿到（上游动态 + 静态种子 + models.dev；双写 `context_window`/`context_length`） |
| **xhx** | 1,000,000 | 100,000 | ✅ 真拿到（`data/models.json`） |
| **RunBios** | 1,048,576 | — | ◐ context 从上游 `context_length` 注入；output 上游没给 |
| **LOOMy** | 131,072 ~ 1,048,576 | — | ◐ context 从上游 `context_length` 补 `context_window`；output 上游没给 |
| **QwenWork** | 1,000,000 | — | ◐ context 真拿到（`context_config`）；output 上游恒 0（代码注释明确） |
| **Skywork** | 1,000,000 | — | ◐ 同上 |
| **Flonno** | 131,072 | 32,768 | ◐ **注入的默认值**，不是每个模型的真实值 |
| **LobsterAI** | — | — | ❌ **有数据但键写错**：上游给了 `ContextWindow`，插件只写 `context_length`（`handler.go:371-381`），宿主忽略 |
| **LobsterHub** | — | — | ❌ **未实现**：`modelList()` 只输出 `id/object/created/owned_by`；上游 `/api/models/available` 原始字段是否含上下文待验证 |
| **Grok** | — | — | ❌ **有数据但未透出**：grok2api 的 `UpstreamModelProfile` 有 `ContextWindow/MaxCompletionTokens`，但 `/v1/models` 的 `modelListItem` 没带（`json:"-"`） |
| **VibeX** | — | — | — 单 cookie，无模型目录 |

**修复优先级（性价比）**：
1. **LobsterAI**：加一行 `context_window = ctxLen` 即可（0.1 人日）；
2. **Grok**：在 grok2api 的 `newModelListItems` 里带 `context_window`/`max_output_tokens`（另一个仓库，0.5 人日）；插件侧不用改；
3. **LobsterHub**：先抓一次上游 `/api/models/available` 原始响应确认字段，再决定映射（0.5~1 人日）；
4. **QwenWork/Skywork**：output 上游确实不给，别编造；可在 UI 标注"上游未提供"；
5. **Flonno**：把注入默认值标注为"估算/默认"，或改为读上游真实字段。

### 5.6 MiniMax 1M：**支持，但插件没做**（更正 5.5 的旧结论）

> 你截图的 "512K / 1M（Higher usage）" 是对的。我上一版只看了 `limit.context`，漏了模型目录里的
> `context_window_options`，结论写错了，这里更正。

**模型目录**（`GET https://agent.minimax.io/mavis/api/v1/models`）对 `MiniMax-M3.1-Flash` 的完整字段：

```json
{
  "limit": { "context": 512000, "output": 128000 },
  "context_window_options": [512000, 1000000],
  "context_window_option_hints": { "1000000": "higher_usage" }
}
```

- `limit.context = 512000` 是**默认值**，不是上限；
- `context_window_options` 明确给出 **512K 与 1M** 两档，1M 标注 `higher_usage`（更贵）。

**抓包也吻合**：同一会话先出现 `context_limit: 1,000,000`（entry 7/8/9），发第二条消息时前端按当前选择写
`context_limit: 512000`（entry 30），之后会话变 512,000（entry 35/36/37）。说明 1M 是**可选档位**，不是模型不支持。

**插件现状（缺口）**：
- `handlers.go:126-153` 只读 `limit.context/output`，**完全忽略 `context_window_options`** → 目录只显示 512K；
- `proxy.go:74-130` 的 `openaiToAnthropic` 只传 `model/max_tokens/temperature/stream/thinking/messages/tools`，**不传 `context_limit`** → 即使客户端想选 1M 也到不了上游；
- 我直接对上游 Anthropic 通道 `/mavis/api/v1/llm/v1/messages` 带 `context_limit: 1000000` 发了一次极小请求 → **200**，说明该字段至少被接受（是否真正按 1M 截断/计费需要长文实测）。

**建议实现（三选一，推荐 A+B）**：

| 方案 | 做法 | 优点 | 缺点 |
|---|---|---|---|
| **A. 模型别名** | `/v1/models` 多暴露 `MiniMax-M3.1-Flash-1M` / `MiniMax-M3-1M`，`context_window=1000000`；代理把后缀映射回 base + `context_limit:1000000` | 客户端可明确选择；计费口径清晰 | 模型数变多 |
| **B. 面板/配置** | 面板加"上下文 512K/1M"选择，写 `context_limit`；config 加默认值 | 不改模型名 | 只影响面板调试，宿主调用仍需别名 |
| **C. 只透出最大值** | 直接把 `context_window` 填 1M | 简单 | 默认其实是 512K，会让客户端误以为默认 1M；且没有真正传 `context_limit` |

> 计费注意：1M 是 `higher_usage`，**不要**把 512K 的模型悄悄按 1M 跑；要么用独立模型 id，要么在面板明确提示。

---

## 6. 自动注册 / 浏览器 / 人机验证（问题 6）

### 6.1 五种形态（先纠正一个概念）

**`chrome-autoreg` 不是被引入的库/依赖，而是 Chrome/Edge 的 `--user-data-dir` 目录名。** 各插件"有没有它"取决于"浏览器由谁托管、profile 放哪"，不是"有没有引入某个大包"。

| 形态 | 插件 | profile 位置 | 体积 | 生命周期 |
|---|---|---|---:|---|
| 插件进程内长期托管浏览器（CDP） | QwenWork / Skywork | `PROVIDER_DIR/data/chrome-autoreg` | 953MB / 1,175MB | 跨注册复用，持续增长 |
| 插件进程内长期托管浏览器（CDP），但路径写错 | Flonno / RunBios | `~/.qw2api/chrome-autoreg` | 1,004MB（共享） | 跨注册复用，两个插件共用 |
| 插件进程内一次性浏览器 | LobsterAI（登录）/ RunBios（Turnstile） | `%TEMP%\lb2a-login-*` / `data/browser-profiles` | 临时 / 小 | 用完即弃 |
| 外包 worker | Grok | `%TEMP%\grok_reg_chrome` | 临时 | 每轮回收 |
| 纯协议（无浏览器） | MiniMax | — | — | — |
| 无自动注册 | BuddyHub / LOOMy / LobsterHub / xhx / VibeX | — | — | — |

### 6.2 为什么"只有 QwenWork / Skywork 有"

- 它们把 `internal/browser` 的 profile 明确解析到 `PROVIDER_DIR/data/chrome-autoreg`（`qw2api-plugin/main.go:198`、`autoreg/engine.go:50` 注释："插件模式必须显式传 providers/<id>/data/chrome-autoreg：契约要求全部..."）。
- Flonno / RunBios 是**同一套代码谱系**（文件几乎逐字相同），但 `internal/browser/chrome.go:133-134` 的默认值是 `home/.qw2api/chrome-autoreg`；只有当调用方显式传 `AutoregProfileDir` 时才用插件目录。它们没传 → 落到用户主目录，且两个插件共享。
- Grok 把浏览器外包给 Python worker，profile 在 `%TEMP%\grok_reg_chrome`（`app.py:1256-1270`），每轮回收 → 插件目录里看不到。
- xhx 没有自动注册，只有人工登录；它用系统 Edge 的 `data/browser-profile`（986MB），名字不叫 chrome-autoreg，但问题一样（体积）。
- 所以不是"别的插件没引入"，而是"路径与形态不同"。**真正需要统一的是"profile 归属与清理策略"，不是"引入同一个包"。**

### 6.3 LobsterAI 为什么没有（它是合理的）

- LobsterAI 的注册/登录链路是：`portal#/login?...&redirect_uri=http://127.0.0.1:<port>/auth/callback` → 用户在浏览器完成登录 → 本地回环收 code → `/auth/exchange` 换 token → 写 `auths/`。
- 它只需要一个"干净的、带代理的、一次性浏览器窗口"（`%TEMP%\lb2a-login-*`，用完删除），不需要：
  - 常驻浏览器；
  - CDP 页面脚本注入；
  - 注册表单编排；
  - 验证码求解；
  - 临时邮箱收码。
- 因此没有 `chrome-autoreg` 是**架构选择**，不是缺失。它缺的是"自动注册"本身——目前仍是人工扫码/登录。
- LobsterHub 同理：现场注册，人工填表，十几分钟一个，没有自动化。

### 6.4 RunBios 的 CF 能力与参考项目

**RunBios 确实有完整的人机验证能力件**（你记得没错）：

| 能力 | 代码 | 说明 |
|---|---|---|
| 浏览器真实渲染取 Turnstile token | `internal/browser/browserturnstile.go` | CDP 打开页面，读 `cf-turnstile-response` |
| 打码平台 | `internal/autoreg/captcha.go` | 2captcha / capmonster / anticaptcha，含空 token 归一 |
| 通道选择 | `cmd/runbiosproxy/main.go:704-756` | 浏览器优先 → 打码 API → `BlockedTurnstile` |
| 浏览器补丁扩展 | `internal/browser/extension.go` + `turnstilePatch` | 降低 Turnstile 识别 |
| 注册代理注入 | `cmd/runbiosproxy/main.go:355` | `BrowserTurnstile` + `regProxy` → `--proxy-server` |
| 临时邮箱 | `internal/autoreg/sources*.go` | 22.do / EmailMux / temp.tf |

当前 `settings.json` 里 `captcha.enabled=false`、`autoreg.enabled=false` → 能力在，默认关闭；点注册会走 `BlockedTurnstile` 并给出原因。

**参考项目 `grok-register-cloudflare-temp-email`**：
- 技术栈：Python + DrissionPage（Chromium CDP）+ `turnstilePatch` + 自建 Cloudflare temp-mail worker + xconsole 协议 solver；
- 强制非 headless（`app.py:1163` 注释：Cloudflare/Turnstile 需要真实页面）；
- 注册线程 + mint worker；输出 CPA auth / 写 grok2api；
- Grok 插件的 `register/grok-register.exe` 就是它的 PyInstaller onedir 产物（`_internal` 里能看到 DrissionPage 4.1.1.4、curl_cffi、tldextract、turnstilePatch）。

**可借鉴的 5 个点**：
1. 真实渲染优先，别用无头/协议硬闯；
2. 浏览器与注册用**同一出口**（代理注入 profile）；
3. Turnstile token 三通道（浏览器 / 打码 API / 明确失败），不要静默空串；
4. 临时邮箱 API 化、可切换源；
5. 失败分类（CF 拦截 / 邮箱超时 / 验证码失败 / 限流）并落日志。

**不建议直接搬**：Python/DrissionPage 产物大（50MB+）、依赖系统 Edge、与 Go 插件契约耦合差；优先复用 RunBios 的 Go 实现（`BrowserTurnstile` 接口已经是可插拔的）。

### 6.5 是否要做"公共自动注册能力包"

- **不建议近期做**：各上游的注册页/验证码/邮箱链路差异极大，强行抽一个公共包会把"适配层"变成"配置地狱"。
- **建议做**：
  1. 统一 `profile` 归属（每插件 `data/`）与清理；
  2. 统一 `TurnstileProvider` 接口（RunBios 已有），其它插件按需实现；
  3. 统一失败分类与日志字段；
  4. 一份"注册能力清单"文档（哪种上游需要浏览器/邮箱/CF/代理），而不是共享二进制。

---

## 7. 修改报告（分阶段，不实施）

### 7.1 范围与原则

**本轮只做：**
- 数据合规（P0）
- QwenWork 对话 + 真实模型（P1）
- MiniMax 持久化（P1）
- **Grok 账号删除 / 额度视图 / 轮询调优 / 事件日志（P1.5）**
- **事件日志 E5（先 6 个插件，Grok 首批）**
- UI 统一（P2，含 Skywork；MiniMax 彻底换风格；QwenWork 先做动效试点）
- RunBios **不动**
- **LobsterHub 走合规 `data/`；Flonno/RunBios profile 优先改临时 profile，不迁移 1GB**
- 其余插件（BuddyHub、Flonno、LOOMy、LobsterAI、LobsterHub、商汤、VibeX）**不加业务新功能**，只做 UI（+ 数据路径修正）；Grok 是例外（本轮修 bug）
- 事件日志 E5 是横切能力（不是业务功能），按 7.4 单独列，首批 6 个插件

**不做：**
- 不改宿主契约 / `provider.json` 保留键；
- 不复制 refresh token；
- 不在本轮做"公共 autoreg 包"；
- 不把 Skywork 的 egress/relay 等能力反向移植到 QwenWork。

### 7.2 P0 数据合规（约 5~6 人日）

| 项 | 改动 | 验收 | 风险 |
|---|---|---|---|
| P0-1 LobsterAI | `auth_dir` 改为相对 `auths`（或 `data/auths`）；整目录移动 6 个凭证；删源码仓库副本 | 面板显示 6 个账号；源码 `auths/` 为空；重启后 token 不失效 | 高：refresh token 一次性，必须移动不能拷贝；先停插件 |
| P0-2 LobsterHub | 默认 `data_dir=<插件>/data`（桌面版行为不变）；迁移 vault/settings/usage/login_sessions；统一 clash 锁到同一文件 | 删除插件即清理；与 LobsterAI 并发不再撞端口/覆盖 pin | 中：与桌面版分家需用户确认 |
| P0-3 Flonno/RunBios profile | `chrome.go` 默认改为 `PROVIDER_DIR/data/chrome-autoreg`；移动 `~/.qw2api/chrome-autoreg`；加 `PROVIDER_DIR` 缺失时的显式报错 | 1GB 目录进入各自 data/；两插件不再共享 | 中：移动前停插件；保留旧路径只读一版 |
| P0-4 MiniMax 账号 | `accounts.json` → `data/accounts.json`，兼容回退 | 104 账号不丢；新路径在 data/ | 低 |
| P0-5 Grok 打包 | 构建根移出安装目录；`src/`、`register.bak-clash/`、`runtime/`、`exe.bak` 不进交付包；日志轮转 | 安装目录只留 exe + provider.json + data/；data/logs 有上限 | 中：需改 `build.ps1`/`pack` 脚本 |
| P0-6 体积治理 | profile 加 TTL/清理（`--disable-component-update`、`--disk-cache-size`、定期删 Cache/Code Cache/component_crx_cache）；日志轮转 | 单插件 profile <200MB（xhx 登录类可放宽） | 低：只删缓存，不删登录态 |

**顺序**：P0-1 / P0-2 / P0-3 / P0-4 先做（迁移类，必须先于 UI）；P0-5 / P0-6 可并行。

### 7.3 P1 QwenWork + MiniMax（约 4 人日）

| 项 | 改动面 | 人日 | 验收 |
|---|---|---:|---|
| 真实模型-解析 | `upstream.ChatChunk` 加 `raw_usage.model`；`StreamEvent.RawUsageModel` | 0.5 | 单测：两种封装都能取到 |
| 真实模型-记录/展示 | `RequestRecord.RealModel`；`chat.go` 写入；面板日志列 + 调试 meta + 命中卡；可选响应头 | 0.5 | 实测与 creditprobe 一致 |
| 对话-错误/用量 | `debugRelayStream` 错误帧处理；`relaySSE` 解析 error/usage/finish；命中卡按 request_id | 0.5 | 402/429 场景能看到原因与尝试轨迹 |
| 对话-多轮（可选） | 面板 messages 数组 + 历史 | 0.5 | 连续两轮上下文 |
| 链路 UI | 状态徽标 + "仅影响对话"文案 + 未配置时禁用 | 0.5 | 不误导 |
| MiniMax 持久化 | `Account.Balance` / `data/balance-cache.json`；`load()` 回填；前端同步完成 reload | 1.5 | 重启/刷新/切页积分仍在 |

### 7.4 Grok + 事件日志（P1.5，约 8~9 人日）

**Grok（约 5~6 人日）**

| 子项 | 改动 | 人日 | 验收 |
|---|---|---:|---|
| 删除复活修复 | 删除时按 email/userId 清 `xai-*.json` 与 `*.txt` 匹配行（先预览）；注册后改增量导入；孤儿文件清理；`deletion-preview` + `linkedDeleteTargets`；`auto_import` 警示 | 1.5 | 删后注册/重启都不复活；预览数=实际删除数 |
| 额度视图 | `QuotaInfo` 补 `used/usagePercent/unit/periodEnd/nextProbeAt/source/confidence`；表格进度条+重置时间；详情抽屉（free/build/console + billing）；"刷新全部额度" | 1.5 | 与参考实现同账号并排一致 |
| 轮询/调度 | `priority/maxConcurrent/minimumRemaining` 内联+批量编辑；"轮询友好"预设；台账加命中账号/换号原因；账号分布小图 | 1.5 | 100 次调用分布接近均匀，或能解释 sticky 导致的偏斜 |
| 事件日志 | 接 E5：`register.* / account.delete / quota.refresh / switch.* / plugin.start-stop`；日志页 | 1.0 | 注册成功/失败、删除、额度刷新可筛选查看 |

> **边界**：严格 round-robin 需要改 `D:\AI_Projects\grok2api` 的 selector（另一个仓库），插件单独做不到；本轮先做"轮询友好"预设 + 可观测性，若仍不满足再单独立项。

**事件日志 E5（约 3 人日，首批 6 个）**

| 子项 | 内容 | 人日 |
|---|---|---:|
| 共享参考实现 | `eventlog`（append jsonl / rotate / 查询 / 导出）约 200 行 + 契约文档 | 0.5 |
| 首批接入 | Grok、QwenWork、BuddyHub、MiniMax、LobsterAI、LobsterHub：发事件 + 面板"日志"页 | 2.5 |
| 验收 | chips 筛选、搜索、时间范围、5s 自动刷新、导出、空状态；错误事件有徽标 | 含上 |

### 7.5 P2 UI 统一（约 15 人日）

| 阶段 | 内容 | 人日 | 验收 |
|---|---|---:|---|
| P2a | `tokens.css` + `components.css` 快照；7 个插件只换 token（QwenWork、Skywork、Grok、BuddyHub、LobsterAI、LobsterHub、LOOMy） | 5 | 颜色/圆角/字体统一；功能不变 |
| P2b | 暗色重写（xhx、MiniMax）+ Flonno shell + 布局统一（侧栏/页头/卡片/表格） | 7 | 5 页截图一致；对比度 ≥4.5 |
| P2c | 截图对比、回归、文档 | 2 | 每插件 5 页 × 亮色 |

**每插件工作量**见 4.3；**总工期**约 3 周（1 人）或 1.5~2 周（2 人并行，但数据迁移类必须串行）。

### 7.6 P3 可选（不在本轮承诺）

- Skywork 同步（与 QwenWork 同源，1.5 人日）。
- Flonno shell（2.5~3 人日）。
- QwenWork 注册接 Clash 代理（1 人日，但会改变注册行为，需单独评审）。
- 公共 autoreg 能力包（评估 5+ 人日，不建议近期）。
- 宿主侧"插件数据管理"（展示/清理/迁移，需改宿主，另立阶段）。

### 7.7 里程碑与依赖

```
M1  P0 数据迁移（必须先做，避免 UI 改完再迁移重测）
M2  P1 QwenWork + MiniMax / P1.5 Grok + 事件日志（可与 M1 后半并行，仓库独立）
M3  P2a token 替换（低风险，快速见效）
M4  P2b 布局统一 + 暗色重写
M5  验收/截图/回归
```

**总量**：P0 5~6 + P1 4 + P1.5 8~9 + P2 15 ≈ **32~34 人日**（不含 P3；含动效试点，不含严格 RR 的 grok2api 改造）。

**为什么 P0 先于 UI**：数据迁移会改 `provider.json` / 目录结构，UI 改完再迁移要重测面板；反之则不会。

### 7.8 验收与回滚

- 每项都要有：命令/证据、前后数据对比、面板截图。
- 迁移类：先整目录备份；新路径失败可回落旧路径一版；确认后再删旧目录。
- UI：不改逻辑，只改样式/布局；每插件保留"回退到旧 CSS"开关（或 tag）。

### 7.9 安全交付与热替换流程（应用正在使用时）

> 你担心的点是对的：**不能直接覆盖正在运行的 exe**。Windows 会锁文件，宿主还会监视 `providers/` 自动重拉；覆盖失败或半覆盖会把插件弄成"启动失败"。

**原则：源码在 worktree，构建在 staging，验证在 canary，最后才碰线上。**

| 阶段 | 做什么 | 影响线上吗 |
|---|---|---|
| 0 源码 | 每个插件单独 `git worktree`/分支；不在 `D:\Program Files\...\providers\<id>` 里改源码 | 否 |
| 1 构建 | 构建到 `providers-out/<id>/`；`go build` / `go test` / `go vet`；跑契约冒烟（`/v1/models`、`/v1/chat/completions`、401 校验） | 否 |
| 2 金丝雀 | 复制包到 `providers/<id>-dev/`，改 `provider.json` 的 `id` 与端口（`panel_port` 或 sidecar）；用**独立测试数据**，不要指向线上 vault | 否（新增一个 dev 插件） |
| 3 验收 | 打开 `-dev` 面板，跑 5 页视觉 + 1 次对话 + 关键操作；确认无回归 | 否 |
| 4 上线 | 宿主面板 **停用** 插件 → 确认进程退出 → 备份 exe/provider.json → 替换 exe（保留 `.bak`）→ **启用** → 看 ready/日志 → 面板 + 1 次对话 | 是，单插件 10~30s |
| 5 回滚 | 停用 → 还原 `.bak` → 启用；`provider.json` 不动 | 是，10~30s |
| 6 数据迁移 | 单独窗口：停插件 → 整目录备份 → **移动**（不是复制）→ 启动 → 面板核对账号数/积分 | 是，2~5min |

**具体注意：**
- **不要**在插件运行中 `Copy-Item` 覆盖 exe；先停（宿主 toggle 或 `taskkill`），确认 `Get-Process <exe>` 为空再替换。
- 宿主可能在文件变更后自动重拉；替换前先停用，避免"旧进程还在写、新 exe 已就位"的竞态。
- canary 的 `provider.json` 必须把 `id` 改成目录名（宿主校验 id=目录名），端口要避开线上；**不要**让 canary 和线上共用同一份 refresh token/账号目录，否则会互相踢。
- 数据迁移（LobsterAI auth_dir、LobsterHub data_dir、MiniMax accounts.json）**不能和 UI 改动混在一次上线**；先迁移、验证、再改 UI。
- RunBios **不参与**任何替换。
- 建议补一个 `scripts/deploy-plugin.ps1`：`-Stage`（构建到 out）、`-Canary`（生成 -dev）、`-Swap`（停/备份/替换/启）、`-Rollback`；把上面步骤固化成命令，避免手抖。

**停机窗口建议**：纯 UI 每插件 10~30 秒；带数据迁移的 2~5 分钟；一次只动一个插件，动完观察 10 分钟再下一个。

### 7.10 用 `qw2api/qwork-auto` 做实施子代理（已实测可行）

**访问性：已确认。**
- `GET http://127.0.0.1:50080/v1/models`（`Bearer sk-unified-local`）200；
- `POST /v1/chat/completions` 与 `/v1/responses`（`model=qw2api/qwork-auto`）都 200；
- DSH 的 `web` profile 里 `local-proxy` provider 已包含 `qw2api/qwork-auto`（`api: openai-responses`，baseURL `127.0.0.1:50080/v1`），**不需要改 DSH 配置**。

**工具能力：已实测。**
- 只读探针：让它读 `docs/plugin-audit-2026-11-04.md` 前 15 行 + 跑 `git status --short`，返回 `TITLE=插件体系分析汇总与修改报告`、`GITLINES=44`，与真实值完全一致；
- 写入探针：让它 `write` 一个文件再 `read` 回来，返回 `QW2API_WRITE_OK`、`BYTES=16`，文件真实存在且字节一致。
- 结论：它能真实调用 read/write/pwsh，不是"只会聊天"。

**怎么接：**
- `subagent` 工具本身**没有 model 参数**，不能直接指定；
- `workflow` 的 `agent(prompt, { provider:"local-proxy", model:"qw2api/qwork-auto" })` 可以，已实测；
- 所以实施型任务走 workflow：`implementer`（qw2api）+ `verifier`（另一个模型，如当前默认 `lobsterai-plan/deepseek-flash`）+ 我做最终验收（跑命令/看证据）。

**建议的角色分工：**

| 角色 | 用谁 | 做什么 | 不能做什么 |
|---|---|---|---|
| 派单/验收 | 我 | 拆任务、给文件白名单、跑构建/测试、对照验收表 | 不替实施者写代码（保持独立验收） |
| 实施 | `qw2api/qwork-auto` | 单文件/单模块修改、写测试、按锚点改 | 不改架构、不碰数据迁移、不 push |
| 独立审查 | `mmxproxy/MiniMax-M3.1-Flash`（DSH 已配置；`-Preview` 直连可用但 DSH 未配置，网关会归一成同一上游） | 对抗式 review：找回归/安全/边界 | 不直接改代码 |
| 金丝雀验证 | 我 + 脚本 | 构建、起 -dev、点 5 页、发 1 次对话 | 不在 canary 用线上 token |

> **审查模型已实测**：`mmxproxy/MiniMax-M3.1-Flash` 直接调用 200，作为 workflow agent 也能真实调用 read/pwsh（返回 `TITLE=插件体系分析汇总与修改报告 | GITLINES=44`）。
> `mmxproxy/MiniMax-M3.1-Flash-Preview` 直连 200（网关把请求归一到上游 `MiniMax-M3.1-Flash`），但 DSH 的 `local-proxy` 模型清单里没有这个名字，workflow 会直接失败；要用它得先往 `C:\Users\ASUS\.dsh-latest\profiles\web\cordis.patch.yml` 加一条并重载。**没必要**——两者上游是同一个模型，直接用已配置的 `mmxproxy/MiniMax-M3.1-Flash` 即可。

**护栏：**
- 在 `git worktree` 里做，不直接改线上 `providers/`；每任务限定文件白名单；禁止 `git push`、禁止删目录。
- 每个任务 ≤ 1~2 个文件或 1 个模块；先写测试再改；失败可回滚。
- qw2api 池会 402/429/空回复（已有 50 个 fail-dump）；workflow 里加重试 + 失败回退到默认模型，避免整批卡死。
- 同一模型既当实施又当验收 = 没有独立性；验收必须由我跑命令、看 diff、看截图。

**建议试点**：先拿 **Grok 删除复活**（范围小、可验证：删账号 → 跑注册 → 不复活）做一次完整闭环；通过后再扩到 QwenWork 真实模型 / UI token 替换。

---

## 8. 决策：已确认 3 项，待确认 3 项

### 8.1 已确认（按你的回复）

| # | 事项 | 结论 | 影响 |
|---|---|---|---|
| 1 | **LobsterHub 数据归属** | **合规**：`data_dir=<插件>/data`，每个插件管自己的账号 | 从 `%APPDATA%\LobsterHub` **一次性迁移**（移动）到插件 `data/`；之后与桌面版**分家**，不再共享账号/用量。需在 UI 里注明"桌面版与插件版各自独立" |
| 2 | **Flonno/RunBios 1GB profile** | **优先替代方案，不迁移 1GB** | 方案：自动注册改用**临时 profile**（每次注册新建、结束清理），不需要长期登录态；这样没有 1GB、也没有迁移丢失问题。风险：CF/Turnstile 可能更常触发；若实测明显变差，**保持现状**并登记为"已知例外"（不复制、不迁移） |
| 3 | **Skywork / MiniMax** | **Skywork 纳入本轮 UI；MiniMax 彻底换风格** | Skywork 已在 7 个 token 换肤名单里；MiniMax 按 2.5 人日上限做完整重构（字体/圆角/配色/布局） |

> 关于 2 的替代方案细节：Flonno/RunBios 的注册是"一次性新号"，不需要持久 Cookie；只有 Turnstile 的 clearance 可能受益于 warm profile。建议先跑 10 次注册对比"临时 profile vs 现有 profile"的通过率，再决定是否删旧目录。**不复制** profile（复制会带来两份 clearance，反而容易互相踢）。

### 8.2 待确认（3 项）

| # | 事项 | 选项 |
|---|---|---|
| 4 | **Grok 轮询** | **A（推荐，本轮可做）**：插件侧"轮询友好"预设（priority 相等、sticky 调短/关闭、暴露 maxConcurrent/minRemaining）+ 可观测性，不改 grok2api；**B**：改 `D:\AI_Projects\grok2api` 的 selector 加严格 `round_robin`（另一个仓库，工期另算） |
| 5 | **动效范围** | **A（推荐）**：先在 QwenWork 做 5 个低风险效果（count-up / 额度条 / 状态 pulse / toast / 按钮），确认观感后再推广；**B**：一次性全插件上动效（风险与工时都更高） |
| 6 | **qw2api 实施子代理** | **A（推荐）**：先拿 Grok 删除复活做一次完整闭环（实施=`qw2api/qwork-auto`，审查=`mmxproxy/MiniMax-M3.1-Flash`，验收=我）；**B**：暂不使用，全部由当前模型实施；**C**：只把 qw2api 当"第二意见"，不碰代码 |

---

## 9. 附录

### A. 证据索引（关键路径 / 行号）

| 主题 | 证据 |
|---|---|
| 契约：数据只写 data/ | `docs/PLUGIN-PROVIDERS.md` §10.5 |
| 宿主启动契约 | 同上 §4.1 / §10.2 |
| LobsterAI 绝对 auth_dir | `providers/lobsterai/provider.json` 的 `auth_dir`；`lobsterai2api/plugin.go:265-271` |
| LobsterAI 源码仓库 6 个凭证 | `D:\AI_Projects\Lobster\lobsterai2api\auths\lobsterai-*.json` |
| LobsterHub 默认 %APPDATA% | `lobsterhub/store.go:189-198`；`plugin.go:47-50,173-199` |
| LobsterHub/LobsterAI 共用 clash.json、锁不同 | `lobsterai/data/clash.json`；`data/clash.lock` 与 `data/locks/clash.lock` 同时存在 |
| Flonno/RunBios profile 在 home | `flonno2api/internal/browser/chrome.go:133-134`；`runbios_proxy/internal/browser/chrome.go:133-134` |
| `~/.qw2api` 1,003.7MB | 实测目录；mtime 2026-11-04 02:06 |
| MiniMax accounts.json 在根 | `mmxproxy/plugin/src/provider_serve.go:48-49`；实测 104 账号 |
| MiniMax 积分仅内存 | `handlers.go:474-491`（balanceCache）、`:496-508`（sync 不落盘） |
| MiniMax 前端同步不刷列表 | `web/index.html` 的 `syncAll()` / `liveTick()` |
| QwenWork 真实模型被丢 | `upstream/client.go:583,600`（有 Model）→ `server/chat.go:210,265,300`（固定请求名） |
| QwenWork 真实模型解析先例 | `cmd/creditprobe/main.go:90-160`（`model` + `raw_usage.model`） |
| QwenWork 流式错误被吞 | `server/debugchat.go:270-300`；`web/index.html` 的 `relaySSE` |
| QwenWork 命中卡取 logs[0] | `web/index.html` 约 2230 行 `dbgFetchHit(seq, null)` |
| QwenWork 空回复证据 | `data/fail-dumps/` 共 50 个（含 `1793666280_502_qwork-auto.json`）；`qw2api.log` 的 402/429 |
| QwenWork 链路探测 | `server/clashpanel.go:120-190`；`cmd/qw2api-plugin/clashpanel.go:56-100` |
| QwenWork 链路守护 | `upstream/netshield.go:1-30`；`cmd/qw2api-plugin/main.go:293-319` |
| QwenWork 注册无代理 | `internal/autoreg` + `internal/browser` 全仓无 `--proxy-server` |
| QwenWork/Skywork profile 路径 | `cmd/qw2api-plugin/main.go:198`；`autoreg/engine.go:50` |
| Grok 安装目录含源码 | `providers/grok/src`、`build.ps1:3,20`、`register/`、`runtime/`、5 个 `exe.bak` |
| Grok 夜间窗口未实现 | `provider.json` 的 `pool_window_*`；Go 源码 0 引用 |
| Grok clash 键未接 | `provider.json` 的 `register_clash_*`；Go 只传 `register_proxy` |
| Grok 日志 109.7MB | `providers/grok/data/logs` 实测 |
| Grok 删除只删 DB | `grok/src/panel.go:246-250`（只 `BatchDelete`）；`admin.go:264-268`（只发 provider+ids） |
| Grok 注册后全量重导入 | `grok/src/jobs.go:327`；`import.go:180+`（`runImport` 扫全目录） |
| Grok 自动导入 | `grok/src/main.go:158-183`（`:169` 检查 `AutoImport`） |
| Grok 额度字段被丢 | `grok/src/admin.go:161-176`（`QuotaInfo` 仅 5 字段）；参考 `account-quota.tsx` / `accounts-api.ts` 有 used/usagePercent/unit/periodEnd/nextProbeAt |
| Grok 无调度权 | `grok/src/proxy.go:71-91`（纯转发）；参考 `selector.go:501+` + `RoutingConfig`（无 RR 模式） |
| Grok 原始日志抽屉 | `grok/src/web/panel.html:107,417-429` |
| 参考删除预览 | `grok2api/handler.go:476-500`、`POST /accounts/deletion-preview`、`BatchDeleteWithLinked` |
| 事件日志设计 | 本报告 3.6（E5） |
| **模型元数据规范** | `docs/PLUGIN-CAPABILITIES.md` §4（宿主只认 `context_window` / `max_output_tokens`） |
| **完整能力目录** | `docs/PLUGIN-CAPABILITIES.md`（A~J 100+ 项，P0~P3） |
| MiniMax 模型目录 | `GET https://agent.minimax.io/mavis/api/v1/models` → M3/M3.1-Flash 均 `context:512000, output:128000` |
| MiniMax 1M 抓包 | `C:\Users\ASUS\Downloads\minimax插件分析\download.devtools`：entry 7/8/9 为 1,000,000；entry 30 请求 `context_limit:512000`；35/36/37 为 512,000 |
| LobsterAI 上下文键写错 | `lobsterai2api/internal/server/handler.go:371-381` 只写 `context_length`；上游 `internal/upstream/client.go:450` 有 `ContextWindow` |
| Grok 上下文未透出 | `grok2api/backend/internal/domain/model/upstream_profile.go:24-31` 有 `ContextWindow/MaxCompletionTokens`；`transport/http/inference/handler.go` 的 `modelListItem` 无这些字段 |
| LobsterHub 未实现 | `lobsterhub/gateway.go` 的 `modelList()` 只输出 `id/object/created/owned_by` |
| Flonno 注入默认值 | `flonno2api/internal/server/server.go:144-156` 的 `modelsEnricher` 注入 `settings.modelContextWindow` |
| RunBios CF 能力 | `internal/browser/browserturnstile.go`、`internal/autoreg/captcha.go`、`cmd/runbiosproxy/main.go:704-756`、`:355` |
| Grok 注册器来源 | `register/grok-register.exe` + `_internal`（DrissionPage 4.1.1.4 / curl_cffi / turnstilePatch） |
| 参考项目 | `D:\AI_Projects\grok-register-cloudflare-temp-email`（DrissionPage + turnstilePatch + CF temp-mail） |
| UI 基准 tokens | `providers/runbiosproxy/web/assets/style.css:1-30` |
| 各插件 tokens | QwenWork `index.html:11-90`；BuddyHub `panel.html:3`；LOOMy `panel.go:226`；LobsterAI `app.css:12-153`；LobsterHub `panel_page.go:26-83`；MiniMax `index.html:12-31`；xhx `panel.go:368`；Grok `panel.html:8-13`；Flonno `panel.go:527-540` |

### B. 插件清单（安装态）

| id | 名称 | 版本 | 模型数（暴露） | 启用 |
|---|---|---|---|---|
| buddyhub | BuddyHub 多账号网关 | 1.0.0 | 4 | ✅ |
| flonno2api | Flonno 账号池网关 | 1.0.0 | 1 | ✅ |
| grok | Grok (grok2api) | 0.1.0 | 1 | ✅ |
| lobsterai | LobsterAI | 1.0.0 | 1 | ✅ |
| lobsterhub | LobsterHub | 1.0.0 | 1 | ✅ |
| loomy | LOOMy | 1.5.1 | 4 | ✅ |
| mmxproxy | MiniMax 号池代理 | 1.2.0 | 1 | ✅ |
| qw2api | QwenWork 号池代理 | 0.4.8 | 1 | ✅ |
| runbiosproxy | RunBios 账号池网关 | 0.2.0 | 1 | ✅ |
| skywork | Skywork AI 号池代理 | 0.1.0 | 全量 | ✅ |
| vibex | VibeX | 1.3.1 | 4 | ❌（停用） |
| xhx | 商汤小浣熊 | 1.2.1 | 1 | ✅ |

### C. 本次只读探测记录

- `GET http://127.0.0.1:63178/api/logs`：100 条，模型均为 `qwork-auto`（无真实模型字段）。
- `POST /api/debug/chat`（自测 / 宿主 50080）：`qwork-auto`、`qwork-advanced` 非流式 + 流式均 200，返回"你好"；流式帧的 `model` 始终是 `qwork-auto`（请求名），证实真实模型未透出。
- `GET http://127.0.0.1:11455/v1/accounts`：104 个账号，积分字段全空，证实 MiniMax 缓存未持久化。
- `GET http://127.0.0.1:40000/api/admin/provider-service`：返回 50080 与 `sk-unified-local`（用于链路测试，未记录到报告正文）。
- 未修改任何文件；仅生成/删除了 `.scratch` 下的临时探测 JSON。

---

**报告结束。** 如需我按其中某一阶段出"可执行的交接提示词/计划"，告诉我选 P0 / P1 / P2 哪一段即可。
