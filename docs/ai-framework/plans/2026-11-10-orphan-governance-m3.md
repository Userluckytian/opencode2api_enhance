# M3 阶段计划 — reapOrphans 扩展孙进程回收

> MASTER：[MASTER-orphan-governance.md](./MASTER-orphan-governance.md) 模块 M3
> 前置：M2 已完成（树杀语义就位）
> 建立日期：2026-11-10
> 运行模式：**目标模式** ｜ 实施档位：**全能** ｜ 子代理：**启用**

---

## 零、M2 下放项

M2 验收核心结论（详见 M2 计划）：B4 强杀管理面全清 **4697ms**、B5 toggle 后孙进程 **946ms** 消失（`/T` 生效）、A4/A8 实机通过；审查 P1（macOS 无 procfs 误自杀）已修。

**M3 必须纳入的事实变化**：

| ID | 事实 | 对 M3 的影响 |
|---|---|---|
| D-M3-1 | `pluginprovider.killPID` → `killProcess` 已带 `/T`（M2 T1）。**回收「仍活着的孤儿插件」时，其子孙已被连带杀死**（Windows） | M3 在 Windows 侧的增量仅剩：已死插件留下的无主孙进程、以及 M1 端点 kill（走 `core/manager.killProcess`，**无 `/T`**） |
| D-M3-2 | `core/manager/process_windows.go` 的 `killProcess` 被 provider-service 管理复用，M2 明确决定**不给它加 `/T`** | M3 不得改它；M1 孤儿端点要树杀只能**新增独立函数**（如 `killProcessTree`），仅端点使用 |
| D-M3-3 | 非 Windows `killProcess` 为 `Process.Kill()`（SIGKILL），无树杀 | M3 的显式后代枚举在非 Windows 是**必需**，Windows 是双保险 |
| D-M3-4 | 「先杀孙后杀父」只在**父仍活着**时有意义；父已退出时 PPID 关联天然失效 | 见 §三 3.4：明确「父先崩」为已知限制或引入快照（需决策） |
| P2-1（M2 审查） | M1 孤儿端点 kill 不树杀，会留孙进程 | M3 修（独立 tree-kill，不改共享 killProcess） |
| P2-2（M2 审查） | `main.go` 看护接线无自动化测试 | 不属 M3，验收表登记为已知覆盖缺口 |

---

## 一、阶段目标

让 `reapOrphans` 不只回收插件直系子进程，还能回收**孙进程**（node / msedge / mihomo / chrome / sing-box）在父插件已死后的残留。

---

## 二、现状与缺口

`core/manager/pluginprovider/manager.go:696-734` 的 `reapOrphans` 逻辑：

```go
for _, pr := range procs {           // procs 来自 listProviderProcesses()
    if !strings.Contains(low, "--provider-serve") { continue }  // ← 只认插件自身
    if !strings.Contains(low, dirLow) { continue }              // ← 只认本目录
    if alive[pr.PID] { continue }
    if pr.PPID > 0 && hosts[pr.PPID] { continue }
    killPID(pr.PID)
}
```

**缺口**：`listProviderProcesses()`（`proc_windows.go:46`）的 PowerShell 过滤条件是
`Name='opencode2api.exe' OR CommandLine LIKE '%--provider-serve%'`——**node/msedge/mihomo 全部被过滤掉**，从来不会进入 `procs`。

实测证据（2026-11-10）：
- `devin` 的子进程 `node.exe kernel/kernel.mjs`（PPID=29148）
- `qw2api` 的子进程 `msedge.exe --remote-debugging-port=64681`（PPID=56404）

父插件死后，这两个进程不会被任何机制回收。

---

## 三、设计方案

### 3.1 枚举范围扩展

`listProviderProcesses` 增加第三类候选：**父 PID 指向已知插件进程的进程**。

**不能简单放宽命令行匹配**——那会误杀别的项目的 node/mihomo（现场就有 ARTEX 的 5 个 mihomo）。

**正确做法（两阶段）**：

1. 第一阶段：按现有逻辑找出「本目录下、父进程已死的插件进程」（M1/M2 已有）
2. 第二阶段：查全量进程表，找 `PPID ∈ {第一阶段找到的已死插件 PID}` 的进程 → 一并回收
3. 递归一层（孙进程的孙进程），深度上限 2，防失控

### 3.2 关键安全约束

- **只按 PPID 关联**，不按进程名匹配。这样 ARTEX 的 mihomo（PPID 指向 ARTEX 的进程）天然被排除
- 跨平台一致：`orphan_scan_other.go` 的 `ps` 实现同样要能拿到 PPID
- 回收顺序：**先杀孙进程，再杀插件**（反过来插件死后 PPID 信息丢失，孙进程变无主）

### 3.3 与 M1 端点的关系

M1 端点只**列出**（用户手动选择清理），M3 是**自动回收**。两者共用分类逻辑：

- M1 的 `classifyProcesses` 增加孙进程识别（`category: "probe"` 复用，或新增语义）
- M3 的 `reapOrphans` 复用同一判定

**本阶段要求**：抽取共享判定函数，避免两处逻辑漂移。

### 3.4 「父先崩」的已知限制（必须先决策）

若插件**自身先退出**（崩溃/被外部工具强杀），其子进程 PPID 指向的 PID 已从进程表消失，且子进程命令行里通常没有 providers 目录特征（如 `node kernel/kernel.mjs`）。此时**没有可靠依据**把它关联到本项目——按 PPID 找不到根，按名字/路径匹配会误伤其它项目（ARTEX mihomo）。

两条路线，实施者先选一条并在汇报中说明：
- **路线 A（推荐，范围可控；M3 采用并显式接受残余风险）**：M3 只覆盖「插件仍存活 + 父进程已死」的孤儿（含 M1 端点手动清理），Windows 用 `/T`，非 Windows 用显式后代枚举；「父先崩」登记为已知限制（MASTER 判据①在此场景不承诺，写进验收表 C8 备注）。
  - **PID 复用残余风险（§六.1 的创建时间校验未实现，经独立审查批准显式接受）**：后代判定只按 PPID，不校验进程创建时间；窗口 = 枚举→探活→杀之间（极小），完整消除需要 Job Object 或带创建时间的运行期快照（路线 B）。**本阶段不以「无条件全绿」表述**，C8 与 issue-log #7 同步登记该残余风险。
- **路线 B（根治，成本高）**：插件运行期周期性快照「插件 PID → 后代 PID 集合」（带创建时间），父死后按快照回收。需新增持久化/内存表、失效清理与误杀防护，建议另立阶段。

**默认走路线 A**；若实施中发现现场有「父先崩」造成的真实残留，再升级到 B 并单独授权。

---

## 四、任务拆分

### T1 — 共享分类逻辑

- 在 `core/manager/orphan_scan.go`（M1 产物）扩展：`findDescendants(procs []procLine, roots []int, maxDepth int) []procLine`
- 单测：多层嵌套、环状引用防护、深度上限
- **Windows 与非 Windows 语义一致**：纯函数输入是 procLine（含 PPID），不依赖平台

### T2 — 回收接入（两条路径分开改）

1. **自动回收** `pluginprovider.reapOrphans`：
   - Windows：`killPID` 已是 `/T`（M2 T1），回收活着的孤儿插件时子孙连带死亡——**只需加断言/测试锁定，不加代码**
   - 非 Windows：杀插件前先用后代枚举快照（深→浅），再杀插件（否则插件死后丢 PPID）
   - **注意包依赖方向**：`pluginprovider` 不能 import `core/manager`（会循环）。方案二选一：
     - (a) 把共享逻辑下沉到 `core/contract` 或新建小包
     - (b) 在 `pluginprovider` 内独立实现（可接受，逻辑简单）
     - **实施者需给出选择及理由**
2. **手动清理** M1 端点 `OrphanKillHandler`：新增**独立** `killProcessTree`（`core/manager` 内，`/T`）仅供端点使用；**禁止改共享 `killProcess`**（provider-service 管理路径依赖它不加 `/T`，见 §零 D-M3-2）

### T3 — 验证

- 隔离环境（复用 M2 T3 的假插件手法）：
  - 正向：起插件 → 确认孙进程存在 → **杀其父进程制造「插件存活但父进程已死」的孤儿** → 触发回收 → 确认插件与孙进程都被清理；非 Windows 语义用单测覆盖（本机 Windows 跑不了）
  - 手动端点：孤儿 + 孙进程 → POST kill → 断言孙进程也被树杀
  - **负向验证**：ARTEX 的 mihomo 必须**不被误杀**（这是最重要的用例，记录清理前后 PID 存活状态）
- 已知限制：按 §3.4 路线 A，验证「父先崩」场景**不承诺**，在验收表 C8 显式登记

---

## 五、验收表

| ID | 项 | 判据 | 状态 |
|---|---|---|---|
| C1 | 孙进程识别单测 | 深度/环/上限全覆盖 | ✅ `internal/proctree` 8 组 + mihomo 负向；WSL 实跑 PASS |
| C2 | reapOrphans 接入 | 代码路径连通（Windows `/T` 锁定 + 非 Windows 显式枚举）；父进程真实探活（P1-1） | ✅ `killOrphanTree` 平台分派 + `pidAliveConservative`；Windows 真孤儿正向 `TestReapOrphansReapsRealOrphanWindows` PASS，WSL 负向（父存活不杀）PASS |
| C3 | 正向往返 | 孤儿孙进程被回收（自动 + 手动端点各一次） | ✅ 隔离实例：自动回收 1926ms 双 PID 消失；端点 `{"killed":[24400]}` 双 PID 消失；P1-1 修复后由 Windows 真孤儿自动化用例复验（cmd 父进程已死 → 插件+子进程均被回收） |
| C4 | **负向：不误杀** | ARTEX mihomo 清理前后 PID 全部存活 | ✅ 5 个 ARTEX mihomo + 7 个 verge-mihomo 前后快照 `NO_DIFF` |
| C5 | 全量编译测试 | build + vet + test 全绿（含 `GOOS=linux/darwin`） | ✅ build/vet/pluginprovider 全绿；整包 manager 仅 4 条既有 callmetric 时间炸弹 |
| C6 | 代码审查 | 独立子代理，P0 全清；P1-1/P1-2/P1-3 修复并复验 | ✅ 独立审查完成：P1-1（真实探活父进程）/P1-2（部分失败不静默）/P1-3（C8 显式接受）已修；P2 顺手修项已落地；待阶段验收 |
| C7 | M1 端点树杀 | 独立 `killProcessTree`，不动共享 `killProcess` | ✅ `killTreeArgs`/`killTreeWith` 单测 + 源码守卫；共享函数零改动 |
| C8 | 已知限制登记 | 「父先崩」按路线 A 不承诺；**PID 复用窗口（枚举→探活→杀之间）未消除，经独立审查批准显式接受，需路线 B 根治**；验收表与 issue-log 不得写成无条件全绿 | ✅ 已显式登记：本表本行 + issue-log 2026-11-10 #7 + OPEN.md |

### 实施记录（2026-11-10）

- **包依赖方案 (a)**：新增 `core/manager/internal/proctree`（`Process` + `FindDescendants`）。
  `core/manager` 的 `procLine` 与 `pluginprovider` 的 `procInfo` 均改为该类型别名，
  两包共用同一后代判定；未选 `core/contract`（供应商契约包，语义不符）与 `(b)` 独立实现（逻辑漂移风险）。
- **Windows 自动回收零代码改动**：`killOrphanTree` 仅委托 `killPID`（`taskkill /T` 已由 M2 提供）；
  `TestTreeKillTaskkillArgs` 继续锁定参数，新增 `TestReapOrphansReapsDescendants` 行为级锁定。
- **非 Windows 自动回收**：`killOrphanTree` 先 `ps` 全量快照 → `FindDescendants(root, 2)` 后杀后代（深→浅）→ 再杀插件。
- **M1 端点**：`OrphanKillHandler` 的 kill 注入改为独立 `killProcessTree`（Windows `taskkill /T`；非 Windows 显式枚举）；
  共享 `killProcess` 保持非树杀，`TestKillProcessSharedStaysNonTree` 以源码级守卫锁定。
- **E2E 隔离环境注意**：`Start-Process` 拉起 provider-service 时其父立即退出，会触发 M2 父进程自检；
  验证时用既定逃生开关 `OPCODE2API_NO_PARENT_WATCH=1`（与 reapOrphans 无关）。

### 独立审查修复记录（2026-11-10，P1-1/P1-2/P1-3）

- **P1-1 真实探活父进程**：删除 `reapOrphans` 的 `hosts`（Cmd 含 `opencode2api` 的 PID 集合）名称启发式，
  新增平台函数 `pidAliveConservative`（Windows tasklist CSV 第二列精确比对，exec 失败=存活；非 Windows
  signal 0：nil/EPERM=存活，ESRCH/ErrProcessDone=死亡，其它=存活）。`PPID<=0` 或探活为存活/未知 → 不杀。
  `TestStateFileFollow` 增加断言：第二个 Manager.Start 不得杀掉第一个 Manager 持活的插件 PID。
- **P1-1 测试真伪纠正**：原 `TestReapOrphansOnStart` / `TestReapOrphansReapsDescendants` 的「孤儿」其实是
  测试进程的直系子进程（父进程存活），修复后改为负向用例（`TestReapOrphansKeepsLiveParent*`，断言父存活时
  插件与后代均保留）；新增 Windows 真孤儿正向用例 `TestReapOrphansReapsRealOrphanWindows`（`cmd /c start` 使
  cmd 立即退出 → PPID 已死）。非 Windows 因 POSIX reparent 无法按 PPID 判死，不写正向用例（路线 A）。
- **P1-2 部分失败不再静默**：`killTreeWith` 分离根/后代错误；新增 `treeKillError`（RootErr/DescErrs/EnumErr）；
  `selectKillTargets` 对「根已杀、后代失败」仍计入 Killed 并写 errors；端点复用已拿到的进程快照
  （`killProcessTreeWith(procs, pid)`，Windows 忽略快照）；`killOrphanTree` 返回错误，`reapOrphans` 日志分
  reaped/failed 两态；非 Windows 枚举失败返回 `treeKillError` 而非退化单杀后报成功。
- **P1-3 C8 显式接受**：创建时间校验未实现，PID 复用窗口（枚举→探活→杀之间）经审查批准接受，需路线 B；
  已在 §3.4 / §六.1 / C8 / issue-log #7 / OPEN.md 与代码注释（proctree、process_other、reapOrphans）同步登记，
  不写成无条件全绿。
- **P2 顺手修**：`killProcess`（非 Windows）补 `pid<=0` 防御；`--provider-serve` token 与路径归一共享
  `internal/proctree`（`HasProviderServeToken`/`NormalizePath`）；修正「pluginprovider 不能 import core/manager」
  错误注释；`pluginprovider.listAllProcesses` 加 10s 超时；`memoizeAlive` 直接单测。

---

## 六、风险

1. **误杀风险最高的一环**：PPID 复用可能导致把无关进程当孙进程。完整消除需校验父进程创建时间（或 Job Object/运行期快照，路线 B）；**M3 显式接受「枚举→探活→杀」之间的极小复用窗口**（C8，经独立审查批准），不把该风险写成已消除。
2. **包依赖边界**：`pluginprovider` 已单向 import `core/manager`；共享进程树纯逻辑仍下沉 `core/manager/internal/proctree`，避免依赖 manager 未导出实现，也避免两包各写一份导致漂移。
3. **性能**：全量进程枚举比现在更重（现有 15s 一次已注释说明成本高），扩展后需评估是否要降频。

---

## 七、交接提示词（零上下文可执行）

```
你在仓库 D:\AI_Projects\opencode2api_guide\opencode2api_enhance（Go 1.26，Git Bash，Windows）。

任务：实现 M3 阶段（孙进程回收）。先完整读
docs/ai-framework/plans/2026-11-10-orphan-governance-m3.md（重点 §零 下放项、§三 设计与 §3.4、§四 任务）。

背景事实（已由 M2 验证，不必重新调研）：
- 插件 = <plugin>.exe --provider-serve；子进程如 node kernel/kernel.mjs、msedge --remote-debugging-port
- M2 已给 pluginprovider.proc_windows.go 的 taskkill 加 /T：回收「仍活着的孤儿插件」时子孙会连带死
- core/manager/process_windows.go 的 killProcess 被 provider-service 管理复用，禁止加 /T
- 机器上有 ARTEX 项目的 5 个 mihomo，绝对不能误杀；只允许按 PPID 关联，禁止按进程名匹配

要做：
1. core/manager/orphan_scan.go 加纯函数 findDescendants(procs, roots, maxDepth)（含环/深度防护）
2. 非 Windows 的 reapOrphans 路径：杀插件前先枚举并回收其后代（先深后浅）；Windows 侧用 /T 已有能力，补测试锁定即可
3. M1 孤儿端点新增独立 killProcessTree（/T），仅端点使用；不改共享 killProcess
4. 单测覆盖 findDescendants；负向用例证明 ARTEX mihomo 不在回收集合
5. 真机隔离验证：插件存活但父进程已死的孤儿 + 其孙进程，自动回收与手动端点各一次

包依赖边界：pluginprovider 已单向 import core/manager；共享的进程树纯逻辑下沉 core/manager/internal/proctree（不要反向 import，也不要在两包各写一份）。

必须实际运行并贴真实输出（禁止「应该能过」）：
  gofmt -l <改动文件>
  go build ./...
  go vet . ./core/...
  go test ./core/manager/ -run "Descendant|Orphan|ClassifyProcesses" -count=1 -v
  GOOS=linux go build ./... && GOOS=darwin go build ./...
已知既有失败：core/manager/callmetric_engine_test.go 时间炸弹（与本阶段无关），整包测试如遇它请注明并绕过。
```

---

## 八、阶段验收结论（2026-11-10，主会话终审）

**判定：通过（路线 A 范围内）**。C1-C8 全绿，P0=0；3 个 P1 已修复并经主会话独立复核。C8 的「父先崩」「PID 复用窗口」「POSIX reparent」为**显式批准的已知限制**，不表述为无条件全绿。

### 完成项

- C1 孙进程识别：`internal/proctree.FindDescendants` 深度/环/自环/重复 PID/多根/深度上限全覆盖；`TestFindDescendantsDoesNotMatchByName` 证明不按进程名匹配；Windows + WSL 双平台 PASS。
- C2 自动回收接入：`reapOrphans` 改为**真实父进程探活**（`pidAliveConservative`：查询失败=存活、`PPID<=0` 不杀），删除名称启发式；Windows 走 M2 `/T`，非 Windows 先深后浅显式枚举。Windows 真孤儿用例 `TestReapOrphansReapsRealOrphanWindows` PASS；负向「父存活不杀」Windows/WSL 双 PASS。
- C3 正向往返：隔离实例自动回收 1926ms 双 PID 消失；手动端点 `{"killed":[24400]}` 后根+孙进程消失；P1-1 修复后由真孤儿自动化用例复验。
- C4 负向不误杀：ARTEX 5 个 mihomo + 7 个 verge-mihomo 清理前后 `NO_DIFF`；越界 PID 13908（ARTEX mihomo）被拒且存活。
- C5 全量编译测试：gofmt 空输出；`go build ./...`、`go vet . ./core/...`、`GOOS=linux/darwin go build` 均 0；`pluginprovider` 全包 PASS；整包 manager 仅 4 条既有 callmetric 时间炸弹。
- C6 独立审查：首轮 P0=0、3 个 P1（真实探活/部分失败不静默/C8 显式接受），全部修复；P2 顺手项落地（PID 0 防御、共享 token/路径归一、注释纠错、ps 超时、memoize 单测）。
- C7 端点树杀：独立 `killProcessTree`/`killProcessTreeWith`，共享 `killProcess` 零改动，源码守卫 `TestKillProcessSharedStaysNonTree` 锁定；部分失败时根仍计入 Killed 且 errors 写明「后代回收失败」。
- C8 已知限制：已在本表、§3.4、§六.1、issue-log #7、OPEN.md 与代码注释同步登记。

### 未通过项

无。

### 待人工项 / 已知限制（不阻断）

1. **「父先崩」无主孙进程**：插件自身先退出时 PPID 关联丢失，按名字匹配会误伤 ARTEX mihomo；路线 A 不承诺，需路线 B（运行期快照）才能根治。
2. **PID 复用窗口**：枚举→探活→杀之间未校验创建时间（计划 §六.1 未实现），经独立审查批准显式接受；完整消除需 Job Object/快照表。
3. **POSIX reparent**：父进程退出后子进程 PPID 变 init/subreaper，非 Windows 自动回收正向场景不承诺；WSL 只验证负向（父存活不杀）与纯逻辑。
4. **生产部署**：需停机窗口 + 用户授权；本阶段未触碰生产目录/进程。
5. **非 Windows 正常停插件**：`pluginprovider.killProcess` 仍为单 SIGKILL，Toggle/Close 路径不树杀（孤儿回收与端点已覆盖）；生产 Windows 由 `/T` 覆盖。

### 证据（主会话实际执行）

- `gofmt -l` 14 个改动文件 → 空输出；`go build ./...`、`go vet . ./core/...` → 0；`GOOS=linux/darwin go build ./...` → 0。
- Windows：`go test ./core/manager/ -run "Descendant|Orphan|ClassifyProcesses|SelectKillTargets|KillTree|KillProcess|MemoizeAlive"` → ok（16 用例）；`pluginprovider -run "ReapOrphans|StateFileFollow|PidAliveConservative|ParseTasklistPIDCSV"` → ok（6 用例，含真孤儿 7.56s）。
- WSL Ubuntu-24.04：proctree 全部 PASS；manager 树杀/端点部分失败/memoize PASS；pluginprovider 负向（父存活不杀）与 `TestStateFileFollow` PASS。
- 真机隔离证据与 ARTEX PID 表见 §四 T3 与 issue-log #7。
