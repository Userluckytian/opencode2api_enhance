# Top5-01 · Lukejdjd/opencode2api-manager —— opencode2api 的「管理面」样本

- 仓库：`C:\Users\ASUS\AppData\Local\Temp\oc-refs-20261111\opencode2api-manager`（只读，未修改，无任何进程被启停）
- HEAD `a70e27c`（2026-11-10 17:46 -0500），11 个提交，单作者 Lukejdjd，`package.json:3` version **0.2.7**，MIT
- 规模：源码 TS 共 **8 个文件 / ~2.1k 行**（`service/` 5 个 + `panel/main.ts` + `cli/main.ts` + `plugin/` 2 个），其余 `*.js` 是**提交进仓库的构建产物**（`cli/main.js` 120KB、`service/main.js` 144KB、`panel/main.js` 63KB、`plugin/index.js` 133KB；`DETAILS.md:56` 「built scripts are committed」）
- 运行验证：`node cli/main.js status`（`OPENCODE2API_MANAGER_DATA_DIR` 指到 `%TEMP%\oc-research\mgr-state`，只读命令）→ **跑通**，输出见 §8；本机无 bun，`bun test` 未跑（见 §8 判断）

---

## 1. 形态与依赖

**一个仓库三种交付面 + 一个外部引擎**（`README.md:5`、`docs/DETAILS.md:15`）：

| 目录 | 是什么 | 关键文件 |
|------|--------|---------|
| `cli/` | 独立 CLI（Node 20+，`type: module`）。`main.ts` 只有 102 行：把 `service/manager.ts` 的导出包成 `status/check/install/update/start/stop/restart/sync/watch/maintenance/enable-plugin/connect/key/logs/settings/warp` 子命令，全部 `console.log(JSON.stringify(...))`（`cli/main.ts:26-97`）。`main.js` 是 `bun build --target=node` 产物 | `cli/main.ts:1-102` |
| `service/` | **宿主侧 Node 服务**（OpenChamber「extension service」，`runtime: "host"`）。起一个只监听 `127.0.0.1` 的 HTTP server，用 `OPENCHAMBER_SERVICE_TOKEN` 做 Bearer 鉴权（`service/main.ts:5-8, 148-150`），把 manager 能力路由成 REST（`/snapshot` `/logs` `/warp-pool` `/action` `/settings` `/local-key` `/status`，`service/main.ts:59-145`）；`contributes.service.permissions.exec` 声明要能跑 `launchctl/plutil/tar/powershell.exe/opencode2api`（`package.json:35-41`），外加 `cron */5` 的 schedule（`package.json:43-48`） | `service/main.ts`、`service/manager.ts`(660 行，全部业务)、`core.ts`(版本比较/JSONC 合并/压缩包路径校验)、`platform.ts`(进程与系统服务)、`lock.ts`(跨进程租约)、`maintenance.ts`(去重协调器)、`warp-observations.ts`(日志反推) |
| `panel/` | OpenChamber 面板（guest 侧 DOM，无框架）：`element()` 手搓 + `@openchamber/sdk/ui` 的 `mountButton/mountSwitch/mountSelect/mountCheckbox/mountTextField`。整页 = 一次 `render()` 全量重绘，数据源只有一个 `/snapshot`（`panel/main.ts:50-63, 68-415`） | `panel/main.ts`(427 行)、`panel/style.css`(102 行) |
| `plugin/` | **OpenCode v1/v2 server plugin**（不是 GUI 插件）：单文件同时导出 `server`(v1) 与 `setup`(v2)（`plugin/index.ts:36`）。它的职责极窄：**只做 check/sync，永不 install/update/start/stop 网关**（`DETAILS.md:23`）；`runtime.ts` 就是一个 60s 心跳唤醒器，真正的调度权在磁盘上的协调器 | `plugin/index.ts`、`plugin/runtime.ts:5-48` |
| `tests/` | 3 个 `bun:test` 单测 + 4 个 smoke 脚本（`core/maintenance/platform/plugin/service/windows`），详见 §8 | `tests/*.ts` |

**它管的「opencode2api」是哪个上游？——`jasonxu114514/opencode2api`，不是 `6Kmfi6HP`，不是我们内嵌的 `wbi35772/opencode2api-win`，也不是自研：**

- `service/core.ts:7` → `export const REPO = "jasonxu114514/opencode2api"`；`manager.ts:214` 打 `api.github.com/repos/${REPO}/releases/latest`；`manager.ts:453` 从该仓库 release 下载。
- **不内嵌**二进制，也不重新分发：`THIRD_PARTY_NOTICES.txt` 只有 5 个 MIT/ISC 依赖（jsonc-parser / @openchamber/sdk / proper-lockfile / graceful-fs / retry / signal-exit），**没有任何 GPL 内容**；`DETAILS.md:123`：「opencode2api is a separate **GPL-3.0** project downloaded from its official release. The manager does **not redistribute or modify** its binary.」——这正是我们 `third_party/opencode2api-win/`（内嵌 + 附源码包）之外的另一种 GPL 隔离姿势：**只当包管理器，不碰二进制**。
- 上游资产命名（`core.ts:34-47`）：`opencode2api_<tag>_windows_amd64.zip` / `..._darwin_arm64.tar.gz`。我实测拉了该仓库 `releases/latest`：**v1.4.7，2026-11-05 发布**，资产命名与其 `assetName()` 完全吻合（含 `windows_arm64.zip`，但其 `core.ts:42-45` 明确拒绝 win32+arm64）。
- 引擎 API 面（它只用这些）：`GET /healthz`（`manager.ts:178-184`）、`GET /v1/models` + `Bearer server_keys[0]`（`manager.ts:192-198`）、`127.0.0.1:8080` **硬编码**（`manager.ts:180`、`core.ts:63`、`DETAILS.md:96`「intentionally supports one local gateway at port 8080」）。
- 配置文件形态与我们同源（`manager.ts:517-528` 生成的默认 config）：`listen / server_keys / zen_keys / go_keys / anonymous / proxies / models.refresh_seconds / retry / warp{enabled,pool_size,stack,source_ipv6} / webui.enabled` —— 和 `core/manager/freetier_engine.go:111-223` 的 `engineConfig` 是同一套引擎方言。

> 对我们的意义：它是**同一条赛道上、同一天**做出来的另一个 40000 管理面，且它对引擎的读法（`/healthz` 的 `warp.active/standby/cooling`）与我们的 `FreeTierEngineStatus()`（`freetier_engine.go:521-540`）**逐字段一致** —— 可比性极强，抄过来没有翻译成本。

---

## 2. WARP 出口观测（a70e27c，重点）—— 数据来源是「healthz 计数 + 引擎日志正则反推」，冷却是**估的**

它把 WARP 面板拆成**两层不同可信度**的数据（这是这段提交最值得抄的东西）：

### 2.1 第一层：`counts` —— 来自引擎 `/healthz`（权威总数）
`service/manager.ts:618-627`：
```ts
export async function warpPool() {
  const h = await health();                      // GET 127.0.0.1:8080/healthz
  if (!h?.warp) return { counts: null, observations: null };
  const { active, standby, cooling } = h.warp;   // 只要这三个数
  const log = await gatewayLogTail(1024 * 1024).catch(() => "");
  return { counts: { active, standby, cooling },
           observations: h.version === "v1.4.7" && log ? warpObservations(log) : null };
}
```
路由 `GET /warp-pool`（`service/main.ts:63`，a70e27c 新增这一行 + `manager.ts` 的 81 行改动）。

### 2.2 第二层：`observations` —— **不读引擎状态文件，不查引擎 API，而是「读引擎日志最后 1MB，用 4 条正则把出口生命周期推出来」**
- 日志来源（`manager.ts:603-617` `gatewayLogTail`）：优先 `<root>/service.log`（= 引擎自己写的），否则 `<root>/manager-gateway.log`（= 它用 `spawn(..., stdio:["ignore",fd,fd])` 重定向的，`platform.ts:145-151`）；**按字节 seek 到尾部读 1MB**（不是 `tail -n`），坏行 `JSON.parse` 失败即跳过。
- 推断器全文只 57 行：`service/warp-observations.ts`。逐条口径：
  | 识别的日志形态（`component:"warp"` 的 JSON 行） | 正则 | 动作 |
  |---|---|---|
  | `OpenCode WARP/<x> egress <id> ready at <ip>` | `:20` | 进 `active`，记 `lastReady` |
  | `OpenCode WARP/<x> pool ready (...` | `:26` | **清空 active+cooling**，仅当 `lastReady` 距本次 **< 1000ms** 才把它留下（`:29-30`，抗启动竞态的土办法） |
  | `Rotating OpenCode WARP egress <id> (...` / `OpenCode WARP egress <id> (...` | `:34` | 出 active；若括号形态（=被轮换）→ `cooling.set(address, at + 30min)` |
  | `... egress <id> scaled down at ...` | 同一正则第 2 组 | 出 active，**不计冷却** |
  | `OpenCode WARP egress <a> replaced by <b> at <ip> ` | `:43` | 旧 id 摘除、新 id+新址进 active |
- **「cooldown estimates」的估法：`at + 30 分钟`（`warp-observations.ts:3` `minimumCooldownMs = 30*60*1000`）**。它**丢掉了日志里引擎自报的真实冷却时长**（我们的日志里写得很清楚：`cooling 24h0m0s`、`cooling 1m0s`、`cooling 1m30s`），一律用固定 30min 代替，只保留 `estimatedUntil` 一个时刻。

### 2.3 UI 呈现（`a70e27c` 里 `panel/main.ts:237-304`）
`<details>` 折叠区「View WARP exits」，展开时才请求 `/warp-pool`（`toggle` 事件 `:298-304`；收起时 `clearTimeout` 停表），并给一颗独立的 **Refresh pool status** 按钮（`:297`）。渲染次序即「可信度阶梯」：
1. `counts` 为 null → 「WARP status is unavailable.」；
2. `observations` 为 null → 「Individual exits are unavailable for this gateway or log.」；
3. 有 observations → **先挂一句免责**：「Exit addresses come from **recent gateway logs**. The list **can be incomplete or differ from the live totals**.」（`:258-262`）；
4. 「Observed active exits」= 裸 IPv6 地址列表（`:264-269`）；
5. 「Recent cooldowns」= 地址 + 秒级倒计时 `~Xm Ys left` / 到点后写「standard window elapsed」（`:270-292`，`tick` 递归 `setTimeout(1s)`，且 `details.isConnected && details.open` 才继续），再挂一句：「Timers estimate the **standard 30-minute window**. **The upstream can request a longer cooldown.**」（`:279`）；
6. `counts.cooling>0` 但匹配不到具体地址 → 「No current cooldowns could be matched to an exit in the logs.」（`:294`）。
Work Status（`service/main.ts:116-125`）只放 `X active · Y standby · Z cooling` 一行，不放地址。

### 2.4 我把它的推断器原样移植成 JS 做了三次实测（脚本在 `%TEMP%\oc-research\warp-obs-check.js` / `-probe2.js` / `-probe3.js`，只读、不启停任何进程）

按真实引擎日志形态造样本（`now` 固定 17:46:00Z）：

| 样本 | 输入日志 | 它的输出 | 判定 |
|---|---|---|---|
| A | 2 条 `ready at` → `pool ready` → 第 2 条 `Rotating...(HTTP 403), cooling 24h0m0s` | active=`[]`（pool ready 把 17:05 的 ready 清掉了，距 1.0s+ 都超窗），cooling=1 条 `est_left=1440s` | 冷却**严重低估**：真实 24h → 只显示 24min 剩余 |
| B | `ready` + 30s 后 `Rotating...(HTTP 429), cooling 1m0s` | cooling `est_left=1770s` | 真实 60s → 估成 29.5min，**高估 30 倍** |
| C | 40min 前的 403 轮换 | cooling=`(none)` | 真实还在 24h 冷却中 → **完全消失**（面板只剩「计数说有 40，列表说没东西」） |
| D | `scaled down` | 不进冷却 | 正确 |
| E | 只有 `Discarding duplicate WARP exit ... already active or cooling down: <prefix>/64` | active/cooling **都空** | **正则没有这一支**：这条恰好是「池填不满」的核心信号，我们生产日志里 **7798 次** |

再用**本机生产引擎日志尾部 1MB**（`%APPDATA%\opencode2api-manager\runtime\free-tier\stdout.log`，含 2873 行 warp）直接喂它：
```
observed active : ["2a09:bac5:1ef7:2da5::48c:48"]        ← 和 /healthz active=1 对得上
observed cooling: 1 条（30min 估计）
真实账本 config.json.warp-state.json: cooling 41 条，其中仍在冷却 40 条   ← /healthz cooling=40 也对得上
```
`pool ready` 的 1 秒窗口也单独验证过：`+0.5s` 保留、`+1.0s` 丢弃、冷启动场景（pool ready 远晚于最后一条 ready）全部丢弃。

**结论（对我们的用法）**：
- **可抄的是「呈现与话术」，不是「估法」。** 「counts（权威总数）+ observations（观测样本，显式声明可能不完整/与总数不一致）+ 按需展开 + 独立刷新 + 秒级倒计时 + 到期文案」这套**漂移可视化**框架是现成的、可直接搬到我们的面板；`warp-observations.ts` 的 `+30min` 常量和「不读账本」这两点**我们必须反超**——我们已有 `WarpCooldown()` 读引擎的权威 24h 账本（`core/manager/freetier_cooldown.go:62-89`，含 `Corrupt` 标志），拿真值当主数据、把日志反推的地址当**副数据并明确标注**，比它强。
- **它把「不可用」分成三种原因分别告知**（healthz 无 warp 字段 / 日志拿不到 / 版本不认识）——这正好治我们「`warp_cooling` 在非 WARP 模式恒 0」那种「0 到底是『没有』还是『看不见』」的歧义。
- **它对引擎版本做了硬钉**：`h.version === "v1.4.7"`（`manager.ts:625`）才做日志反推。我们引擎 `/healthz` 实测报 `"version":"dev"`，照抄这一行会**永远返回 null**；改成「按消息形态自适应 + 认不出来就明说 unavailable」。

---

## 3. 进程托管与重启 —— 有 **pid 文件（持久化 pid + 可执行路径）**、有**逐进程校验**、**故意不认领孤儿**

### 3.1 启停机制（`service/platform.ts:92-167` `control()`）
macOS 且有 LaunchAgent label → 交 `launchctl`；否则（Windows / Linux / 无 label 的 mac）走它自己的「分离子进程 + pid 文件」：

1. **pid 文件**：`<stateDir>/gateway-pid.json`，内容 `{ pid, binary }`，写盘即 `mode 0o600`（`:110`、`:158-162`）。**这就是我们缺的那一块**：它把 pid 和被管进程的可执行路径**成对持久化**，管理器/宿主重启后仍能找回自己拉起的进程。我们的 `e.pid` 只在内存（`freetier_engine.go:357`），`Stop` 里还 `e.pid = 0`（`:425`），一次管理器重启就永久失联 → 面板「重启成功」实为空操作（`docs/opencode-channel-research-2026-11-11/00-current-state-and-tests.md:66-70`）。
2. **不信任 pid，只信任「pid + 路径」的等式**（`:41-58` `processMatches`）：Windows 走 `Get-CimInstance Win32_Process -Filter "ProcessId=N"` 比 `ExecutablePath -eq <绝对路径>`；mac/Linux 走 `ps -p N -o comm=`；pid<2 直接 false。**先验证、后 kill**（`:117-118`），kill 后 30×200ms 轮询确认死透，没死透就 **抛错**「Gateway is still shutting down; try again.」（`:127-128`），而不是继续往下走。
3. **Windows/Linux 不领养别人的进程**（`:133-144`）：`start/restart` 前先探 `http://127.0.0.1:8080/healthz`，活着就抛「Port 8080 is already in use. Stop the existing gateway first.」；`stop` 时 pid 不匹配就抛「This gateway was not started by the manager. Stop it in its original service manager.」`DETAILS.md:94` 把这条写成产品政策：「**The manager only terminates processes whose saved PID and executable path match its own gateway.**」
4. **启动 = 等 ready**：`serviceControl` 除 stop 外一律 `await waitHealthy()`（`manager.ts:355-361`），`waitHealthy` 45×1s 轮询 `/healthz` 且**可选地要求 `version` 等于期望值**（`:363-372`）。

### 3.2 `Restart macOS LaunchAgents in place to avoid registration races`（50a4a01）治的什么竞态
**旧实现**（该提交删掉的 `platform.ts` 老代码）：restart = `bootout` → `bootstrap`，bootstrap 失败再兜一层 `kickstart`。竞态 = **launchd 的卸载是异步的**，紧接着 `bootstrap` 会撞上「服务正在拆除/已注册一半」→ 报错 → 旧代码**误跳到 kickstart 分支**，对一个其实没注册上的服务 kickstart，结果就是「操作成功但进程状态不明」。

**新实现**（`platform.ts:59-91` `controlLaunchAgent`，可注入 `run` 与 `pause` 以便测试）：
- `stop` 才 `bootout`（注释 `:100`：bootout 是为了让 `KeepAlive` 不会立刻把刚停的进程拉回来）；
- `start/restart` 先 `launchctl print <domain>/<label>` 判断**是否已注册**：已注册 → **原地重启** `kickstart -k`，注释写得很直白：`// Restart in place: unregistering first races launchd's asynchronous teardown.`（`:78-79`）；
- 未注册 → `bootstrap`，失败 **500ms × 5 次**重试，耗尽则**原样抛出 bootstrap 错误**（不再 kickstart 一个不存在的服务）。

**Windows 侧的对应做法**：它没有 Windows 服务，等价物就是 §3.1 那三条 —— 「先观测再决定动作」「不要拆了再建（能原地 `kickstart -k` 就别 bootout）」「瞬态失败有界重试、真失败必须抛错且绝不静默 fallback」。`tests/platform.test.ts:1-33` 三条用例直接把**argv 序列**钉死（`[["print",...],["kickstart","-k",...]]`；重试 3 次成功且没出现过 kickstart；失败保留原错误）——用「注入 runner + 断言调用序列」来锁死重启语义，成本极低。

### 3.3 对我们 P0-1 的直接可抄清单（落点 `core/manager/freetier_engine.go:350-447`）
| 抄什么 | 它的位置 | 我们怎么写 |
|---|---|---|
| pid **与 exe 路径成对持久化** | `platform.ts:110-116`、`:158-162` | 新增 `FreeTierEngineDir()/engine-pid.json` `{pid, exe, startedAt, configFingerprint}`；`Start` 成功后落盘，`Stop` 成功后删除。建议直接扩我们已有的 `writeFileAtomic`（`core/manager/fsutil.go:29-53`，Windows rename 瞬时 Access denied 已有 10×20ms 重试，比它的裸 `writeFile` 更硬） |
| **kill 前必须证明 pid 就是我们要的那个进程** | `platform.ts:41-58`（CIM `ExecutablePath` 比对） | 复用我们已有 `listPortPids(port)`（`core/manager/netstat_windows.go:25-45`）拿端口持有者 + 校验进程名/镜像路径 = `bin/opencode2api-win.exe`（VERDICT P0-1 已是这个口径，它给了现成的「路径全等 + pid<2 拒绝 + 死透轮询」三件套）。**只有全等才 kill**，其余一律拒绝并报错 |
| **不认领 = 不空操作**：任何一步无法确定归属就**抛错**，别 return nil | `platform.ts:127-132`、`:135-144` | `Start` 里 `probePort` 短路（`freetier_engine.go:362-366`）改成：端口活着 → 读 pid 文件 → 匹配则**采纳**（把 pid 填回内存、再比配置指纹，见 §4）；不匹配则返回**显式「外来进程」错误 + 面板显示「未托管进程」**，绝不 `setState("ready") return nil` |
| **重启 = 原地重启，而非先杀后建** | `platform.ts:78-79` | 优先 `POST /api/config/reload`（P0-3，我们已实测 warp.enabled/anonymous/proxies/zen_keys 可热应用）；只有确实要换进程时才 Stop+Start，并把 `waitHealthy` 的**版本/期望值比对**搬过来（`manager.ts:363-372`）——我们引擎报 `version=dev`，用「进程启动时刻 > 配置写入时刻」或 pid 变化替代版本等式 |
| **瞬态失败有界重试 + 保留原始错误** | `platform.ts:82-90` | `runner.Kill` 的错误被 `_ =` 丢弃（`freetier_engine.go:430`）；`waitPortClosed` 的错误虽进了 `e.lastErr`（`:433-437`）但 `Stop` **仍然 `return nil`**（`:440`），于是 `Restart` 拿着「假成功」继续 `Start` → 端口在听即短路。必须把 kill 失败 / 端口未释放如实向上返回 |
| **用注入 runner 断言 argv/调用序列** | `tests/platform.test.ts` | Go 里把 `Runner` 接口（我们 `Restart` 已接 `runner Runner`，`freetier_engine.go:444`）扩展记录调用，加一条「restart 不得在已采纳 pid 时重复 Kill」「Stop 失败不得吞错」的回归锁；`freetier_drift_repro_test.go` 转正时同批加 |

**注意差异**：它**没有**「按端口反查 PID 并认领孤儿」这条路 —— 它是刻意拒绝（fail-closed）。我们要的 P0-1 比它多做一步（因为我们已经**事实存在**一个 11-06 起的孤儿），所以「反查 + 全等校验 + 采纳」是我们自己的增量，它的价值在于给了**校验与报错纪律**，不是给「认领」本身。

---

## 4. shared operation conflicts / Detect configured providers（65c4a81 / 88be505）

### 4.1 「多实例抢同一件事」的答案 = 一把跨进程租约 + 一份按目标哈希的账本
- **跨进程操作锁**（`service/lock.ts:17-47`）：`proper-lockfile` 锁 `<stateDir>/operation.lock`，`stale: 120000`（崩溃 2 分钟后可被接管）、`update: 10000`（心跳续租）、`retries: 0`（**不排队，直接拒绝**）。抢到前拿不到就抛 `ManagerBusyError`；`onCompromised` 把 `{compromised}` 塞进 `AsyncLocalStorage`，**每次写盘前后都 `assertLockValid()`**（`lock.ts:12-15` + `manager.ts:43, 50`），续租失败 → 立刻中止正在写的操作，报「Manager lock was lost; operation cancelled.」（`assertLockValid` 在 `manager.ts:42` 入口、`:51` rename 前各调一次）
- 65c4a81 治的是**文案**：把「Another manager operation is running.」换成人话「A model sync, update check, or gateway action is already in progress. **Wait a moment, then try again.**」（`lock.ts:8`）；同提交把 `connectProvider` 的退化路径改成**显式说明这是空操作**：`return { ...(await syncModels()), message: "Provider already added. Models refreshed." }`（`manager.ts:317-321`）。
- **多接口去重协调器**（`service/maintenance.ts:36-133`）：`targetID = sha256([realpath(configPath), realpath(providerPath)])`（Windows 再 `toLowerCase`，`:15-27`）→ 按目标记 `jobs[id].attemptedAt` 与 `notices[id]`，`SYNC_INTERVAL_MS=300s` / `CHECK_INTERVAL_MS=3600s`，**失败同样计入节流**（`:80-83`），手动 sync 的时间戳会挡住 5 分钟内的后台重复（`:66-72`）。`tests/maintenance.test.ts:45-60` 用 **4 个真实子进程**（openchamber/v1/v2/cli-watch）并发跑，断言 `calls` 文件里 `sync`/`release` 各只出现 **1 次**；`:112-128` 断言「stale 锁可恢复、活锁不可被抢」。
- **同一份配置的反覆盖**：`atomic(file, text, original)` 带 **CAS**——先读原文比对、写临时文件后再比一次、才 `rename`（`manager.ts:41-56`），不同就抛「File changed during this operation.」/「Concurrent edit detected.」。所有对 `opencode.jsonc` / `config.json` 的写都走这条（`syncModels:296-297`、`connectProvider:341-345`、`setWarp:397`）。

### 4.8be505「Detect configured providers」= 把按钮状态改成**由观测推导**
`manager.ts:253-257`（snapshot 里真去读 provider 配置文件，判断 `providers.opencode2api || provider.opencode2api` 是否存在）→ `panel/main.ts:166-172`：标签 `x.providerAdded ? "Provider added" : "Add provider"`，`disabled = x.providerAdded || !x.health`。要点：**按钮的可点性来自磁盘上的事实，不来自前端本地状态**；快照里还并列 `installed`（二进制在不在，`:262`）、`configured`（config 在不在，`:263`）、`health`（进程活不活，`:265`）——四个正交布尔量各说各话，用户一眼看出是哪一环缺了。

### 4.3 有没有「配置已保存但未生效」的**显式漂移提示**？—— 没有指纹比对；但它用**架构**绕开了这个坑
- 全仓 grep `reload|fingerprint|applied|drift|hash`（`.ts`）**零命中**（只有 `applyEdits`/`applyHostReady` 误命中）。它**不做**「配置指纹 vs 运行态指纹」比对。
- 为什么它不会得我们这个病：它**从不允许「写配置」和「让配置生效」脱钩**。`setWarp()` 是「备份 → 写盘 → restart → `waitHealthy()`」一把梭（`manager.ts:387-415`）；restart 失败就 **回滚旧配置 → 再 restart 一次 → 把两段失败拼成一条错误**抛给面板：
  ```
  WARP change failed: <reason> Previous configuration restored, but restarting it also failed: <detail>
  ```
  （`:406-413`；17f43e8 专门为了「把 WARP restart 错误暴露出来」写的这段）。也就是说它的纪律是：**要么配置和进程一起成功，要么显式失败并回滚**，中间态不存在。
- 对我们的判断：**P0-4 的「漂移指纹比对」它没有现成实现可抄**，但 **65c4a81 的「退化操作必须自报是空操作」+ 88be505 的「按钮态由磁盘/进程观测推导」这两条，恰好是治「面板报成功但实际空操作」的两味最便宜的药**，可以直接落：
  - `RestartFreeTierEngine`（`freetier_engine.go:444-447`）在 Stop 没杀掉任何东西 / Start 走短路时，返回 `applied:false + reason:"端口由未托管进程占用(pid=…)"`，面板据此显示「未生效」而非「重启成功」；
  - `freeTierEngineStatus` 契约里把 `managed`（pid 是否匹配）、`config_applied`（指纹是否一致）与 `state` 分列，学它「四个正交布尔量」的排版。
- P0-2（自愈死锁）它同样没有直接实现（不依赖观测 vs 配置的分支决策），但它的**锁 + CAS + 每写必校验**给了我们一个原则：**自愈动作必须和配置写入在同一把锁里**，否则看门狗和用户保存会互相踩。

---

## 5. 密钥与配置治理

- **key 显隐（4530406）**：`panel/main.ts:176-204`。设计点：① 默认 `keyBox.hidden = true` 且 `replaceChildren()` 清空；② **key 不进 snapshot**，只在点「Show」时现取（`POST /local-key` → `readLocalKey()` → `gatewayKey()` 从 config 的 `server_keys[0]` 读，`manager.ts:185-191, 352-354`）；③ 取的过程中按钮 `disabled`，`finally` 里恢复到「`busy || !x.configured`」的正确态；④ 再点即「Hide」并**把 DOM 里的明文抹掉**；⑤ 提示语固定「Keep this key private…」（`:192-196`）。配套的字段白名单：`/logs` 只回 `time/level/event/component/status/model/attempts/duration_ms/outcome` 8 个字段（`manager.ts:628-652`），**结构上不可能带出 key 与会话内容**（`DETAILS.md:11` 明写「Inspect selected structured log fields **without exposing API keys** or conversation content」）。
- **写盘方式 = 唯一入口 `atomic()`**：`manager.ts:41-56`。同目录唯一随机后缀临时文件（`file + ".manager-" + hex12`）、`flag:"wx"` 独占创建、`mode 0o600`、可选 CAS、`rename` 替换、`finally` 无条件删临时文件、写前写后各一次 `assertLockValid()`。目录一律 `mode 0o700`（`:43, 285, 465`）。
- **备份**：`backup()` 把原文件复制到 `<stateDir>/backups/<epoch>-<hex6>-<basename>`，`copyFile` 后**再 `chmod 0o600`**（`:279-289`）；每次改 `providerPath`/`configPath`/替换二进制前都先备份（`:296, 341, 552-553`）。备份落在**扩展包与 Git 仓库之外**（`DETAILS.md:92`），并如实写明「Backups **may contain credentials**; keep them private. They are currently **retained until manually removed**.」——即**无保留期/无轮转**，是它自己承认的缺陷。
- **配置读**：JSONC 走 `jsonc-parser` 的 `parse/modify/applyEdits`，**按字段逐个 edit 而不是整块覆盖**（`core.ts:15-22`、`:119-126` 的注释「Edit individual fields to retain comments on custom fields and names」），保留注释、自定义名、variants、`disabled`、`modelID` 重定向；`baseURL` 不等于 `http://127.0.0.1:8080/v1` 直接拒绝合并（`core.ts:63-66`）；空/畸形 catalog 拒绝写入（`core.ts:49-51`）。`tests/core.test.ts:30-57` 把这些不变量全钉住了。
- **是否做「配置文件 vs 运行进程实际状态」的指纹比对？——没有。**（见 §4.3：它靠「写+重启+验版本+失败回滚」的原子流程消掉漂移，而不是靠观测比对。）我们**必须自己做**：磁盘 `config.json` 的规范化 SHA-256 vs `/healthz` 回报的有效字段（`keys.zen`/`warp`/`anonymous`）比对。它唯一接近「比对」的东西是 `waitHealthy(version)` 的**版本等式**（`manager.ts:363-372`）——把「期望值」传进等待条件，这就是「生效验证」的雏形，值得沿用到我们 P0-3/P0-4。

---

## 6. 更新 / 安装链路（防「升级后进程还是旧的」）

**两段式：stage（下载+验+解包，绝不碰运行中的东西）→ apply（停-替换-起-验版本-失败回滚）**

1. `release()`（`manager.ts:200-240`）：GitHub `releases/latest`，**拒 draft / prerelease / 非 `^v\d+\.\d+\.\d+$`**（`:222`）；结果**双级缓存**（内存 + `<stateDir>/release.json`，1 小时），网络失败**回退旧缓存并打 `stale:true`**，面板老实显示「Release check unavailable. Showing the last known result」（`panel/main.ts:102-108`）。
2. `stageRelease()`（`:443-503`）：要求资产成对 `<name>` + `<name>.sha256`（`:448-452`）→ `download()` **只允许 https + GitHub 三个域白名单**，手动跟 6 跳重定向（`:417-442`）；**流式限尺寸**（archive 150MB / sha 4KB，`:156-177, 454-455`）；**SHA-256 全量校验**，失败即抛（`:459-463`）；解包前 **ZIP 路径穿越校验**（`(^/|^C:|/../)` 逐条目）、tar 先 `-tzf` 验条目再 `-tvzf` 拒 `l/h` 链接、解包后递归再查一次符号链接（`:470-489`、`core.ts:134-143`）；要求**包内恰好一个** `opencode2api[.exe]`（`:498-500`）；全程落在 `stateDir/staging/<hex16>`，成功后 `rm -r`（`:464, 533, 599`）。
3. `install()`（`:504-538`）：**已有二进制或已有 config 就拒绝安装**（「Use Update or configure its paths instead.」）；**端口已被占用也拒绝**（`if (await health()) throw ...`）；然后才落 config（**现场生成 `oc2-<64hex>` server key**、`warp.enabled:false`、`webui.enabled:false`）→ `control("start")` → **`waitHealthy(staged.version)` 带期望版本**（`:532`）。
4. `update()`（`:539-601`）—— 这就是它防「升级后进程还是旧的」的全部机关：
   - **必须先 health 才允许更新**：`Start the gateway before updating so rollback can be verified.`（`:542-545`）——理由是「回滚需要一个可比对的基准」；
   - 先取 `oldVersion = health().version`；若 `oldVersion === staged.version` → **直接报「Already up to date」并清理暂存**，不做任何替换（`:547-551`）；**这一步就是在识别「磁盘已新、进程已新」**；
   - 备份 config + 备份旧二进制（`:552-553`），新二进制写成 `<binary>.manager-new`（同目录，`:554-556`），**stop → `rename` 替换 → start → `waitHealthy(staged.version)`**：等待条件里带着**期望版本**，所以「文件换成功了、进程起来但还是旧版」会**卡在这里失败**（`:363-372`）；
   - 任一环节失败 → stop（吞错）→ `copyFile(old, binary)` 还原 → start → `waitHealthy(oldVersion)`，然后抛「Update failed; previous binary restored and restarted.」；还原后再起也失败则抛「…old binary restored but restart needs attention.」（`:572-596`）。
   - `DETAILS.md:107` 诚实标注边界：「**Updates verify binary readiness, not a paid inference call.** Only the previous binary is restored on update failure; a private pre-update configuration backup is kept separately.」

**对我们的启示（对应 OPEN.md「BIOS 插件更新重置配置」同类历史问题）**：
- 抄三件事：① **替换前先比对「期望版本 vs 运行版本」**，相等就是 no-op 且要**说出来**；② **替换后等待「运行态 == 期望值」**才算成功（我们引擎报 `dev`，改用 pid 变化 + 进程启动时刻 > 配置/二进制 mtime 做等式）；③ **失败必回滚 + 回滚也要再验 + 两段结果拼进一条错误**（对应我们 `update`/`fetch-free-tier-engine.ps1` 覆盖 `bin/opencode2api-win.exe` 的路径）。
- 它**没有**「配置版本化 / 更新后迁移或保留用户配置」的机制：`update()` 只备份 config 不动它，`install()` 遇到已有 config 直接拒——所以它的防「更新重置配置」手段是「**更新永远不写配置，安装永远不覆盖已有配置**」这条不变量。这条值得写进我们的更新契约（我们现在缺的就是这条：更新链路里 config 会被重写）。

---

## 7. 对我们 P0 四项的逐条判断

| P0 | 判断 | 可直接抄的具体做法 | 我们的落点文件 |
|---|---|---|---|
| **P0-1 引擎变孤儿 / Stop-Start 空操作** | **部分（但很值钱）**：它**不做孤儿认领**（刻意 fail-closed，`platform.ts:129-144`），所以「反查端口 PID 并采纳」还得我们自己写。但它给了三件我们缺的硬东西：**pid+exe 成对持久化**（`platform.ts:110-116, 158-162`）、**kill 前 exe 路径全等校验 + 死透轮询**（`:41-58, 123-128`）、**「重启成功」的定义 = 观测到期望值而非「命令返回」**（`manager.ts:363-372`）。另：`waitPortClosed` 的错误必须像它一样**向上抛**，而不是 `_ =`（我们 `freetier_engine.go:433`）。 | 新增 `engine-pid.json`（用我们的 `writeFileAtomic`）；`Start` 的 `probePort` 短路改成「读 pid 文件 → 路径全等 → 采纳并比配置指纹」；不匹配 → 返回 `applied:false` + 显式「未托管进程占用」错误。测试用「注入 Runner + 断言调用序列」口径锁语义（照 `tests/platform.test.ts`）。 | `core/manager/freetier_engine.go:350-447`、`core/manager/process_windows.go`、`core/manager/netstat_windows.go`、`core/manager/fsutil.go` |
| **P0-2 自愈死锁（判据是配置开关而非观测）** | **无直接实现，有 1 条可抄的替代物**：`warp-observations.ts` 证明**从引擎日志就能独立观测 WARP 池真实形态**，这正是「不信配置、只信观测」的样本。且它的日志尾部 seek 读法（`manager.ts:603-617`，1MB + 跳过坏行）可直接移植。另外 `lock.ts` 的纪律给了我们「自愈与用户写配置必须同锁」的依据。 | 把 `maybeAutoFallbackWarp` 的门槛从 `engineWarpEnabled(cfg)` 改成 `health.Present \|\| 日志里出现 warp 出口行`；再补一条「配置关 & 观测开 ⇒ **判定漂移** ⇒ 先 apply/reload，不直接降级」。落 `engineWarpEnabled` 判据处。 | `core/manager/freetier_warp_fallback.go:165-188`、新增 `core/manager/freetier_engine_log.go`（日志反推，仿 `warp-observations.ts`） |
| **P0-3 打开引擎管理口 + 先 reload 再重启** | **部分**：它**不碰引擎管理 API**（只用 `/healthz` 与 `/v1/models`），所以 reload 链路只能靠我们已做的 E4 实测。但它的 **`setWarp` 编排（写盘 → apply → 验期望值 → 失败回滚 → 复合错误）就是「apply 优先、restart 兜底」的模板**，把其中的 `restart` 换成 `POST /api/config/reload` 即可（`manager.ts:387-415`）。它「原地 kickstart 优先于拆了重建」（`platform.ts:78-79`）与 reload 优先于 restart 是同一思想。 | `WriteEngineConfig` 后：`webui.enabled=true` + 随机口令 + 仅回环监听（我们 `freetier_engine.go:334` 现在写死 `Enabled:false`），然后先 reload、reload 失败或不支持才 Stop+Start；两条路都以「观测到期望值」为成功定义。 | `core/manager/freetier_engine.go:230-343`（`WriteEngineConfig`）、新增 `applyEngineConfig()`、`core/manager/admin_http.go` |
| **P0-4 漂移可视化 + `warp_cooling` 恒 0** | **有（这条它是我们最直接的样本）**：`a70e27c` 整套「counts(权威) + observations(观测，显式声明不完整) + `<details>` 按需加载 + 独立刷新 + 秒级倒计时 + 三种 unavailable 文案」可原样搬到我们面板；`snapshot()` 把 `installed/configured/health/providerAdded` 四个正交布尔量分列 + 按钮态由观测推导（88be505，`panel/main.ts:166-172`）是现成的「不可点 = 因为缺 X」排版。**但估法别抄**：`+30min` 常量（`warp-observations.ts:3`）与「不读账本」在我们机器上实测会把 40 条冷却显示成 1 条（见 §2.4），我们已有的 `WarpCooldown()` 才是真值。 | 状态契约里同时给：`warp_ledger{cooling_total, earliest_unfreeze_at, corrupt, path}`（来自 `WarpCooldown()`，**主数据**）+ `warp_observed{active_addrs[], recently_rotated[]}`（日志反推，**副数据，UI 标注「来自最近日志，可能不完整」**）+ `warp_counts`（healthz）。`warp_cooling` 在非 WARP 模式改为 `null + reason` 而不是 0。 | `core/manager/freetier_cooldown.go:62-113`（已有，补进契约）、`core/manager/freetier_engine.go:478-543`（`FreeTierEngineStatus`）、`web/` 面板 WARP 卡片 |

**一句话总评**：它在**产品语义与 UI 诚实度**（no-op 要自报、按钮态来自观测、不可用要分原因、估计值要标「估计」）上领先我们，在**底层可靠性**（pid 持久化+路径校验、CAS 写盘、跨进程租约、更新的双向版本验证）上领先我们；但在**引擎侧观测深度**上明显不如我们（不读冷却账本、不做配置指纹比对、把真实的 24h 换成 30min 常量、且把解析器钉死在单个引擎版本号上）。

---

## 8. 活跃度与可信度（不因新而忽略，也不因新而高估）

- **节奏**：11 个提交全部落在 **2026-11-10 一天内**（03:33 → 04:16 连发 9 个，17:46 再发 1 个），单作者，无 tag、无分支、无 PR 记录。版本 0.2.3→0.2.7 在一天里跳 5 次。**判断：这是一个「一天冲刺」的个人项目**，不是成熟实现——但冲刺内容不像玩具：三条主线（跨平台安装/更新、进程托管、配置治理）都有明确的失败语义与回滚设计。
- **测试覆盖（结构上很好，但有洞）**：`bun test` 单测 3 个文件共 **18 条 test**（`core.test.ts` 6、`maintenance.test.ts` 5、`platform.test.ts` 3、`plugin.test.ts` 4，均断言到 argv/字段级），外加 **4 个 smoke**：`service-smoke.ts`（真起服务 + Bearer 401 校验 + providerAdded 三态 + 设置校验 + 非法 WARP 异步报错）、`windows-smoke.ts`（**真下载官方 release + 校验和 + 解包 + install/connect/sync/stop/start**，用 `OPENCODE2API_MANAGER_DATA_DIR` 完全隔离）、`plugin-smoke.ts`（把产物塞进**真实 OpenCode v1/v2 二进制**加载）、`maintenance.test.ts` 用 4 个子进程真并发验「只做一次」。CI 三平台矩阵跑 typecheck + test + build + `openchamber-guest-check`（`.github/workflows/ci.yml:1-26`）。
  - **关键洞**：a70e27c 这个「旗舰提交」**一个新测试都没加**——`warpObservations`（57 行推断逻辑）**无任何单测**（全仓 grep 只在 `manager.ts` 和构建产物里出现），`panel/main.ts` 的 68 行 UI 也无测试。它反而在代码里承认结果不可靠（「can be incomplete or differ from the live totals」）。
  - 本机**跑不起来**（诚实记录）：无 `bun`（`Get-Command bun` → NOT FOUND），且 `node_modules` 未安装；硬约束不装全局依赖，故 `bun test`/`typecheck` 未执行。可读产物 `cli/main.js` 是提交的构建物，我用**只读子命令**验了真实链路：`node cli/main.js status` 在隔离 stateDir 下**成功**（真联网命中 `jasonxu114514/opencode2api` releases/latest = **v1.4.7**，`installed:false / health:null`，正确判定本机没有它认的那个网关）。**没有**跑 install/update/start/stop/warp，不碰 40000/40080，未使用 40210–40219（无需实验端口，无残留进程）。
- **代码质量印象**：偏高的防御性——CAS 写盘 + 锁丢失即中止、ZIP 穿越/符号链接三处检查、下载域白名单 + 流式限尺寸 + 重定向次数上限、`AbortSignal.timeout` 全面覆盖、`parseConfig` 严格拒非对象、`launchLabel` 白名单正则、`isAbsolute` 校验、`processMatches` 的 `pid<2` 拒绝、异步 operation 单飞（`service/main.ts:34-35`）+ 前端 1.5s 轮询 job（`panel/main.ts:58-62`）。文案克制且承认局限（`DETAILS.md:98-108` 一整节「Compatibility and limits」）。
  可见的技术债：① `/healthz`/`8080` 硬编码（4 处）⇒ 天生只支持单网关、不可换端口；② `observations: h.version === "v1.4.7"`（`manager.ts:625`）**把日志解析器钉死在单个上游版本**，上游一发新版这段可视化就静默退化；③ 冷却估算是常量而非解析日志里的真实时长（实测两个方向都错，见 §2.4）；④ 备份无保留策略；⑤ `panel/main.ts:240-304` 的倒计时 `tick` 用 `setTimeout` 自递归 + `details.isConnected` 守卫，能工作但不如 `onServiceEvent` 驱动干净；⑥ mac 的 `ps -o comm=` 比对 `path.resolve(binary)`，遇符号链接/短路径会误判（Windows 分支用 CIM 绝对路径则没这问题）。
- **它对我们的定位价值**：**同级竞品参照物**，不是可引入依赖。它 MIT 许可、逻辑都很小（`warp-observations.ts` 57 行、`platform.ts` 167 行、`lock.ts` 48 行），**思路与结构可以照搬、逐行代码建议重写进 Go**（避免把 JS 语义和 MIT 声明混进 GPL 隔离区）。

---

## 附：本目录相关文档
- 现场证据与 P0 定义：`00-current-state-and-tests.md`、`99-VERDICT.md:84-87`
- 同批调研：`opencode2api-win.md`（我们的引擎）、`Opencode2api.md`、`9router.md`
- 本次实测脚本（可删）：`%TEMP%\oc-research\warp-obs-check.js`、`warp-obs-probe2.js`、`warp-obs-probe3.js`、隔离 stateDir `%TEMP%\oc-research\mgr-state\`（只落了 settings/release 缓存，未写任何生产配置、未启停任何进程）
