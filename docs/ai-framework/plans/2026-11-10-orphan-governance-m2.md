# M2 阶段计划 — 止血：树杀 + provider-service 父进程自检

> MASTER：[MASTER-orphan-governance.md](./MASTER-orphan-governance.md) 模块 M2
> 前置：M1 已完成（可见性）
> 建立日期：2026-11-10
> 运行模式：**目标模式** ｜ 实施档位：**全能** ｜ 子代理：**启用**

---

## 零、M1 下放项

M1 验收判定：**有条件通过**（P0=0；详见 M1 计划 §十）。下放事项：

### 0.1 待真机补证（不阻断 M2 开工）
| 项 | 内容 | 归属 |
|---|---|---|
| A4 | 隔离实例起服务后 `GET /api/admin/processes/orphans` 实测 200 + JSON 结构 | M2 T3 一并做 |
| A8 | 正向用例：外来 providers 目录 + 已死父进程的真实孤儿样本（本机已清空） | M2 T3 一并做 |

### 0.2 P2 技术债（M2 顺手修，不单独立项）
| ID | 事项 | 位置 | 处置 |
|---|---|---|---|
| P2-1 | `--provider-serve` 为裸子串匹配，无 token 边界（理论上可被无关命令行命中） | `orphan_scan.go:71` | 改 `(^|\s)--provider-serve(\s|$)` 正则，补单测 |
| P2-2 | `pidAlive` 查询失败返回 false（fail-closed 到「已死」），tasklist 偶发失败可致误列/误杀 | `pluginprovider/proc_windows.go:29-38` | kill 前二次校验已有；补一条「探活失败视为存活」的保守判定（需评估对 reapOrphans 的影响，禁止改变其既有语义） |
| P2-3 | 路径分隔符 `/` 与 `\` 不归一化，异形路径下「本目录孤儿」可能被列出（仅重复提示，无安全影响） | `orphan_scan.go:67,77` | 比对前 `strings.ReplaceAll(low, "\\", "/")` |
| P2-4 | 同步全表 CIM 枚举 0.6~5s，无缓存；重复点击会重复枚举 | `orphan_scan_windows.go:28` | 接受现状，不引入缓存（M2 不扩大改动面） |
| P2-5 | `Detail` 文案与计划示例措辞不同（informational） | `orphan_scan.go:84` | 不改 |
| P2-6 | `currentProvidersDir()` 与 `defaultProvidersDir()` 双实现需人工同步 | `orphan_scan.go:128` | 注释已声明；M2 不改包依赖方向 |
| P2-7 | handler 层无 httptest 端到端用例 | `orphan_scan.go:174-209` | M2 T3 用真实实例覆盖 |

### 0.3 无关发现（登记，不属本阶段）
- `core/manager/callmetric_engine_test.go` 4 条失败：硬编码 `2026-11-05` 超出 `ReadCallMetrics(now±48h)` 窗口（时间炸弹）。与本阶段无关，已登记 issue-log。

---

## 一、阶段目标

消除新孤儿产生的两个根因（D2、D3），并保证不误伤现有插件的**合法常驻子进程**。

---

## 二、真实影响面勘察（2026-11-10 实测，实施者必读）

### 2.1 现场：哪些插件真的有子进程

对 `D:\Program Files\opencode2api\bin\providers` 下 10 个运行中插件的子进程实测：

| 插件 | 子进程 | 性质 | `/T` 影响 |
|---|---|---|---|
| devin | `node.exe kernel/kernel.mjs` | **核心内核**，插件功能依赖 | ⚠️ 树杀会一并杀掉，需确认正确 |
| qw2api | `msedge.exe --remote-debugging-port=...` | **浏览器自动化**（自动注册用） | ⚠️ 同上 |
| buddyhub / grok / lobsterai / mmxproxy / lobsterhub / flonno2api / loomy / xhx | 仅 `conhost.exe` | Windows 控制台宿主，无害 | ✅ 无影响 |

**结论**：`/T` 树杀在这两类插件上会连带杀掉 node 内核与浏览器——**这正是期望行为**（父插件死了，内核/浏览器必须死，否则就是孤儿）。但必须验证：
1. 插件正常运行时**不会**因为子进程被杀而异常（正常路径不涉及 kill）
2. 树杀后重启插件，node/msedge 能被正确重新拉起

### 2.2 grok 的特殊性（唯一带外部脚本的插件）

`grok/start-grok2api-auto.ps1` 是一个**常驻 supervisor**：
- 用 `Local\grok2api-provider-backend` 互斥锁保证单实例
- `while($true)` 循环拉起 `runtime/grok2api-3.1.8/grok2api.exe`，写端口到 `data/grok2api-port.json`
- 插件进程读该端口文件访问后端

**风险**：该 ps1 若由插件拉起，`/T` 会杀掉 supervisor；但 supervisor 的 `while` 循环意味着它可能是**故意脱离父进程**的设计。需核实：该脚本由谁启动（插件自身 or 用户手工）。

**M2 处理策略**：先查证。若由插件自身拉起 → 树杀正确（插件死了 supervisor 也该死）。若由用户手工启动 → 必须排除（不能杀用户的独立进程）。

### 2.3 查证结论（2026-11-10，只读调查子代理，B6 已闭环）

**结论：supervisor 由用户开机自启（Startup 快捷方式），非插件拉起。external 模式插件只连接、不拉起。**

| 证据 | 内容 |
|---|---|
| Startup 快捷方式 | `%APPDATA%\Microsoft\Windows\Start Menu\Programs\Startup\grok2api-8101.lnk` → `powershell.exe -NoProfile -WindowStyle Hidden -ExecutionPolicy Bypass -File "...\start-grok2api-auto.ps1"`；lnk 与脚本时间戳同为 10-23 18:06:50 |
| 运行态拓扑 | supervisor powershell PID 3652（2026-11-01 12:19:52 启动，父 9456 已消亡）→ grok2api.exe PID 15116（父 3652，端口 57336）；与宿主/插件树（grok-provider.exe 父=宿主 51224，11-09 23:00 起）完全不相交 |
| 插件配置 | `provider.json`: `runtime_mode: "external"`、`external_base_url: "auto"` |
| 插件日志 | 每次启动均为 `启动模式=external` + `已连接外部实例 http://127.0.0.1:57336`，无拉起行为 |
| 源码 | `D:\AI_Projects\plugins\grok` 全仓 grep 无 powershell/Start-Process/schtasks 拉起逻辑；`runtime.go:129-141` external 模式只读端口文件 |
| 计划任务/注册表 | 无 grok 相关计划任务；Run 键仅宿主本体 |

**对 `/T` 的决策**：树杀仅限插件自身 spawn 的后代，supervisor 不在插件进程树内，不会误伤。红线：不得把 supervisor（PID 3652）当插件后代清理；只杀内核 grok2api.exe 也无意义（supervisor 2s 后自愈重启并换端口）。无特殊情况，不写特例代码。

---

## 三、任务拆分

### T1 — `/T` 树杀（Windows）

**改动**：`core/manager/pluginprovider/proc_windows.go:21-24`

```go
// 现状
taskkill := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/F")

// 改为
taskkill := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F")
```

**同步检查**：`core/manager/process_windows.go:21` 的 `killProcess` 是**另一个**实现（管理面用，管 provider-service），是否也要 `/T`？
- provider-service 是 `opencode2api.exe`，它自己会 `pluginMgr.Close()` 清理插件
- 若加 `/T`，Stop 优雅退出路径会变成强杀树，可能跳过 `Close()` 的清理
- **决策**：此处**不加** `/T`（provider-service 有自己的优雅退出），只改插件侧的 `pluginprovider/proc_windows.go`

**验收**：
- `go build ./...` 通过
- 单测：`killProcess` 传入 PID 时构造的命令含 `/T`（可用注入 runner 或断言 argv）

### T2 — provider-service 父进程自检

**改动**：`main.go` 的 `runProviderServiceMode`（`main.go:196`）加一个看护协程。

**逻辑**：
1. 启动时记录 `os.Getppid()`（父进程 = 管理面）
2. 每 5 秒检查父进程是否存活
3. 父进程消失 → `slog.Warn` + `pluginMgr.Close()`（回收全部插件）+ `os.Exit(0)`

**平台实现**：
- Windows：复用 `pidAlive` 思路（`tasklist /FI "PID eq <ppid>" /FO CSV /NH`）
- 非 Windows：`syscall.Kill(ppid, 0)` 或 `os.FindProcess` + `Signal(0)`

**注意**：
- `os.Getppid()` 在 Windows 上 Go 1.22+ 有实现，若不可靠则用 `exec.Command("powershell", ...)` 查 `ParentProcessId`
- 父进程退出后 ppid 可能被系统复用 → 必须同时校验该 PID 的进程**启动时间**或**进程名**，避免误判（**这是本任务最大的技术风险**，实施者需给出方案）
- 该协程必须能被 `ctx` 取消（进程正常退出时不要误触发）

**验收**：
- 单测：父进程存活判定函数（含 PID 复用场景）
- 手工验证：kill 管理面 → 观察 provider-service 在 ≤10s 内自退且插件全清

### T3 — 回归验证（真实环境）

**必须在隔离实例上做**，不动生产：

1. 起隔离实例（独立 providers 目录）
2. 确认插件运行 + 子进程存在
3. 强杀管理面（`taskkill /F` 管理面 PID）
4. 观察 ≤15s 内：provider-service 自退 → 插件全灭 → 孙进程（node/msedge）全灭

**验收判据**：`Get-CimInstance Win32_Process` 中不再有相关 `--provider-serve` 与子进程。

---

## 四、验收表

| ID | 项 | 判据 | 状态 |
|---|---|---|---|
| B1 | 树杀代码生效 | 命令含 `/T`，单测断言 | ✅ `taskkillArgs` 含 `/T`；单测 PASS；真机 toggle(false) 后孙进程 946ms 消失 |
| B2 | 全量编译 | `go build ./...` 退出码 0 | ✅ Windows/Linux/darwin 三平台 build+vet 均 0 |
| B3 | 父进程自检单测 | PID 复用场景有覆盖 | ✅ 8 条 ParentWatch 测试（复用/查询失败保守/迟到捕获/取消），`-race` 干净 |
| B4 | 隔离环境回归 | 强杀管理面后 15s 内全清 | ✅ 实测全清 max **4697ms**（插件 2405ms / 孙 2543ms / ps 自杀 4626ms / 端口释放 4697ms） |
| B5 | 不误伤正常路径 | 插件正常启停无异常 | ✅ toggle 关 848ms/孙 946ms 消失；toggle 开 587ms 重新 running；管理面与 ps 全程健康 |
| B6 | grok supervisor 归属查证 | 明确由谁启动，结论有依据 | ✅ 用户开机自启（Startup lnk），非插件拉起；见 §2.3 |
| B7 | 代码审查 | 独立子代理，P0 全清 | ✅ P0=0；首轮 P1（macOS procfs）+ P1b（Linux ErrProcessDone）+ 第四轮 P1-1（POSIX 捕获时机）/P1-2（CIM 故障误退）全部修复，主会话独立复核（含 WSL 实跑）通过 |
| B8 | M1 遗留 A4/A8 | 隔离实例端点 200 + 正向孤儿 | ✅ 见 M1 计划 A4/A8（2026-11-10 T3） |

---

## 五、风险与红线

1. **PID 复用误判**：父进程自检若只用 PID 比对，可能在极短时间内误判。必须加进程启动时间校验。
2. **`/T` 影响全部 14 个插件**：改动落在共享代码路径，一次失误全插件受影响。必须隔离环境先验。
3. **不得改生产** `D:\Program Files\opencode2api\`。
4. **grok 脚本**：查证前不得贸然断言；若归属不明，`/T` 仍执行（插件死了 supervisor 理应连带），但要在验收里显式记录该决策。

---

## 六、交接提示词（零上下文可执行）

```
你在仓库 D:\AI_Projects\opencode2api_guide\opencode2api_enhance（Go 1.26，Git Bash）。

任务：实现 M2 阶段（止血：树杀 + provider-service 父进程自检）。
先完整读 docs/ai-framework/plans/2026-11-10-orphan-governance-m2.md，
特别是第二节「真实影响面勘察」——里面有你必须知道的现场事实。

做三件事：
1. core/manager/pluginprovider/proc_windows.go:21-24 的 taskkill 加 /T。
   注意：core/manager/process_windows.go 的 killProcess 是另一个函数（管 provider-service 的），
   那个不要加 /T，理由见计划 T1。
2. main.go 的 runProviderServiceMode 加父进程自检协程：
   os.Getppid() 记录 → 每 5s 检查 → 父进程消失则 pluginMgr.Close() + os.Exit(0)。
   必须处理 PID 复用误判（不能只看 PID，要校验进程启动时间）。
3. 写单测覆盖：taskkill argv 含 /T、父进程存活判定（含 PID 复用场景）。

另外必须查证一件事并给出依据：
   D:\Program Files\opencode2api\bin\providers\grok\start-grok2api-auto.ps1
   这个 supervisor 脚本是由 grok 插件自身拉起的，还是用户手工启动的？
   （查 grok 插件源码/日志/该脚本的调用点。结论要有证据，不能猜。）

完成后必须实际运行并贴真实输出：
   go build ./...
   go vet ./...
   go test ./core/manager/pluginprovider/ -v

禁止说「应该能过」。失败要如实贴报错。

最后汇报：改动文件、grok supervisor 的查证结论与依据、命令真实输出、
以及 PID 复用问题的具体解决方案（你在代码里怎么做的）。
```

---

## 八、实机回归与审查证据（2026-11-10）

### 8.1 T3 隔离实例回归（全部 PASS，FAILED_ASSERTIONS=0）

隔离实例：临时目录独立 providers/data/provider_port，管理面 40123 / provider-service 50081；生产进程与生产目录零触碰（收尾确认生产宿主 PID 51224 等原样）。

| 项 | 结果 | 关键证据 |
|---|---|---|
| A4 扫描无误报 | ✅ | GET 200 `{"total":0,"probe":0,"orphan":0,"items":[]}`；受管 fake/ps/孙进程均不在 items |
| A8 正向孤儿 | ✅ | 外来目录孤儿 pid=62360 列出（category=orphan）→ kill `{"killed":[62360]}` → 2s 后 GONE；越界 PID 999999 被拒 |
| B4 强杀管理面 | ✅ | taskkill 管理面 → 插件 2405ms、孙 2543ms、ps 自杀 4626ms、端口释放 4697ms；日志 `parent process gone; provider-service self-terminating` |
| B5 正常路径 | ✅ | toggle(false) → 插件 848ms、孙 946ms 消失（`/T` 直接证据）；toggle(true) → 587ms 重新 running；管理面/ps 全程健康 |

### 8.2 独立代码审查（两轮）

- 首轮：**P0=0 / P1=1 / P2=2**。
  - P1：`parentwatch_other.go` 无 procfs 平台（macOS/BSD）跳过 signal-0 兜底 → 连续 3 次「未知」误判死亡 → 健康服务自杀并被 Watchdog 永久重启。**已修**：/proc 读取失败不区分原因一律 signal-0 兜底（nil/EPERM=存活、ESRCH=确凿死亡、其它=未知），并补 `parentwatch_other_test.go`（真实父进程 + 不可能 PID + 哨兵 PID）。
  - P2-1：M1 孤儿端点 kill 不树杀，留孙进程 → 下放 M3（新增独立 tree-kill，不改共享 killProcess）。
  - P2-2：`main.go` 看护接线无自动化覆盖 → 以 T3 真机回归作为最终裁决，登记为已知覆盖缺口。
- 复核补充修复：`selfExit` 与 shutdown/ListenAndServe 退出路径曾用 `atomic.Bool` 先到先得，后到者会 `os.Exit` 打断 `pluginMgr.Close()`。已改 `sync.Once`（并发调用阻塞至首次 Close 完成），并统一 `defer`/错误路径。
- **P1b（WSL 实跑抓到的 Linux 功能失效）**：Go 对 Linux 已退出 PID 的 `Signal(0)` 返回 `os.ErrProcessDone`（非裸 `ESRCH`），旧 `switch` 用 `==` 比较落 default「未知」→ `pidAliveSafe` 保守判存活 → **Linux 孤儿扫描永远列不出孤儿**。已改 `errors.Is` 四分类（nil/EPERM=存活；ESRCH/ErrProcessDone=确凿死亡；其它=未知），`orphan_scan_other.go` 注释同步。交叉编译 Linux 测试二进制在 WSL Ubuntu-24.04 实跑 `TestParentWatchOtherRealParent/ImpossiblePid/InvalidPid` 三条 PASS（主会话独立复跑，`WSL_EXIT=0`）。
- **P1-1（第四轮复审，POSIX 父 PID 捕获时机 + reparent）**：Linux/macOS 上父进程退出后 `getppid()` 变为 subreaper/init。旧 `StartParentWatch` 在 `pluginMgr.Start()` 之后才取 `os.Getppid()`——管理面若在这段窗口内死亡，看护会把 init 当父进程，D3 在 POSIX 上永久漏检；旧父进程若成僵尸，signal 0/CIM 还会探到「存活」。**修法**：`main()` 第一条语句捕获 `startupParentPID := os.Getppid()`（doctor/flag 解析之前），经 `runProviderServiceMode(port, listenAddr, startupParentPID)` 显式传入 `manager.StartParentWatch(ctx, parentPID, selfExit)`；看护 spec 固定使用该值，不再回调 `currentParentPID`。新增平台函数 `parentReparented(recorded int) bool`：Windows 恒 false（Getppid 保持创建者 PID）；POSIX `os.Getppid() != recorded`（O(1)，僵尸 PID 假存活的唯一确凿出口）。看护启动时先查、每轮 tick 先查，真即 `onGone`/判死并统一写 reparent 日志；`reparented` 为注入字段便于单测。`OPCODE2API_NO_PARENT_WATCH` 禁用语义不变。
- **P1-2（第四轮复审，CIM/PowerShell 故障不误退健康服务）**：`parentCheck` 的「未知」路径（超时/exec 错误/输出不可识别）旧实现单轮保守、连续 3 次（≈15s）即 `onGone`；WMI 仓库故障或 PowerShell 被策略禁用时会误退健康 provider-service，Watchdog 反复拉起形成循环。**修法**：`parentwatch_windows.go` 新增第二探活 `tasklistProbe(pid) (alive, ok bool)`（`tasklist /FI "PID eq <pid>" /FO CSV /NH`，`CREATE_NO_WINDOW`，2s 超时）与纯函数 `parseTasklistPID`（逐行 CSV、第二列精确比对 PID，不依赖本地化提示文案）。CIM 未知时先 tasklist：明确存活 → `(true, 0, nil)`（降级 PID-only；tick 的 `started != 0 && started != st.started` 守卫保证 started=0 不被当成「启动时间不符」）；明确死亡 → `(false, 0, nil)`；tasklist 也失败 → 返回原 CIM 错误（未知，继续保守累计）。CIM 成功路径不变（保留 FILETIME 指纹）。
- **P2（第四轮顺手）**：`OPCODE2API_NO_PARENT_WATCH` 仅精确值 `"1"` 禁用（`"0"` 不再禁用，与注释一致）；`ListenAndServe` 错误路径把 `slog.Error` 前移到 `closePlugins()` 之前，避免看护 `selfExit` 抢赢 `sync.Once` 后错误日志丢失；`orphan_scan.go` 新增 `memoizeAlive`，`OrphanScanHandler` 单次扫描内按父 PID 记忆化，`selectKillTargets` 只对分类阶段记忆化、杀前二次校验仍重新查询（`TestSelectKillTargets/父进程扫描后复活的PID被拒绝` 继续锁定该语义）。M1 端点同一父 PID 的多次 0.7s CIM 单查降为一次。

**第四轮验证证据（2026-11-10）**：
- `gofmt -l`（8 个改动文件）空输出；`go build ./...`、`go vet . ./core/...` 均 exit 0。
- 定向单测 `go test ./core/manager/ -run "ParentWatch|Orphan|ClassifyProcesses|SelectKillTargets|ParseTasklist" -count=1 -v`：**23 个顶层用例全 PASS**（新增 reparent 3 条 + ParseTasklistPID 10 个子例 + ParentWatchWindowsReparented 等）。
- 额外 `-run "TestTasklistProbe"`：`TestTasklistProbeCurrentProcess` / `TestTasklistProbeImpossiblePID` 均 PASS（真实 tasklist：存在 PID 输出 CSV 行、缺失 PID 输出中文「信息: 没有运行的任务匹配指定标准。」且 exit 0）。
- `GOOS=linux go build ./...`、`GOOS=darwin go build ./...`、`GOOS=linux/darwin go vet ./core/manager/` 全 exit 0。
- WSL Ubuntu-24.04 实跑交叉编译 `manager.test`：`TestParentWatchOther|Tick|Loop|Start` 共 **14 条 PASS，WSL_EXIT=0**（含 `TestParentWatchOtherReparented` 真实父 PID 正反例）。
- 整包 `go test ./core/manager/ -count=1` 仍仅 4 条既有 callmetric 时间炸弹失败，无新增失败。

### 8.3 遗留（非阻断）

- 「父先崩」导致的无主孙进程不在 M2/M3 路线 A 承诺范围（M3 计划 §3.4 显式登记）。
- 生产部署需停机窗口 + 用户授权（红线）。

---

## 九、阶段验收结论（2026-11-10，主会话终审）

**判定：通过**（P0=0、P1 全清；B1-B8 全绿；遗留项均为已登记的已知边界/待人工项，不阻断）

### 完成项

- B1 树杀代码生效：`pluginprovider/proc_windows.go` 的 `taskkillArgs` 含 `/T`；单测锁定 argv；真机 toggle(false) 后孙进程 946ms 消失。
- B2 全量编译：Windows/Linux/darwin 三平台 `go build` + `go vet` 均 exit 0（主会话复跑）。
- B3 父进程自检单测：PID 复用、查询失败保守、迟到捕获、取消、reparent 启动/运行两态全覆盖；`-race` 干净。
- B4 隔离环境回归：强杀管理面后全树 max 4697ms 清空（插件 2405ms / 孙 2543ms / ps 自退 4626ms / 端口释放 4697ms），日志 `parent process gone; provider-service self-terminating`。
- B5 不误伤正常路径：toggle 关 848ms/孙 946ms 消失；toggle 开 587ms 重新 running；管理面与 provider-service 全程健康。
- B6 grok supervisor 归属：用户开机自启（Startup lnk）+ external 模式，与插件树拓扑不相交，不写特例；结论有源码/日志/进程拓扑证据。
- B7 代码审查：首轮 P0=0/P1=1 → 修复；WSL 实跑追加 P1b → 修复；第四轮复审追加 P1-1/P1-2 → 修复。主会话独立复跑 build/vet/Windows 单测/WSL 全绿。
- B8 M1 遗留 A4/A8：隔离实例端点 200 + 正向孤儿列出→kill，越界拒绝。

### 未通过项

无。

### 待人工项（不阻断）

1. **生产部署**：`D:\Program Files\opencode2api\` 需停机窗口 + 用户明确授权（红线），本阶段未触碰生产。
2. **POSIX 真机 B4**：Linux 侧已用 WSL 交叉编译测试二进制验证探活/reparent 语义，macOS 仅编译 + 逻辑验证，无 darwin 真机回归。
3. **P2-1 已知边界**：管理面 `ProviderService.Stop` 的 3s 兜底强杀可能打断 provider-service 的 `pluginMgr.Close()`；生产 10 插件时 Close 可能超 3s。属既有生命周期参数，未在本阶段调整（登记为已知边界，非 M2 引入）。
4. **P2-2 Windows 启动期 PID 复用**：若管理面在初始查询前死亡且 PID 被复用，看护可能记录冒充者启动时间（漏检不误杀）；窗口已由「main() 第一行捕获 + reparent 检测」压缩到最小。
5. **P2-3 Linux 孤儿端点**：POSIX 父死即 reparent（PPID→init），按 PPID 判死不成立；Linux 设置页扫描可能列不出孤儿。生产以 Windows 为主，登记为平台限制。
6. **P2-7 非 Windows 停插件树杀**：`pluginprovider.killProcess` 在 POSIX 仍是单 SIGKILL；M3 的显式后代枚举只覆盖 `killOrphanTree`（孤儿回收）与端点树杀，正常 Toggle/Close 路径未加树杀。生产 Windows 由 `/T` 覆盖。

### 证据（主会话实际执行）

- `gofmt -l` 改动文件 → 空输出；`go build ./...` → 0；`go vet . ./core/...` → 0；`GOOS=linux/darwin go build ./...` → 均 0。
- Windows：`go test ./core/manager/ -run "ParentWatch|Orphan|ClassifyProcesses|SelectKillTargets|ParseTasklist"` → ok（23 用例）；`pluginprovider` 定向用例全 PASS。
- WSL Ubuntu-24.04：`TestParentWatchOther|Tick|Loop|Start` 14 条 PASS，`WSL_EXIT=0`。
- 真机 T3 证据见 §8.1；审查/修复证据见 §8.2。
