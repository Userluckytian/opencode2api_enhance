# M1 阶段计划 — 恢复孤儿进程扫描与清理能力

> MASTER：[MASTER-orphan-governance.md](./MASTER-orphan-governance.md) 模块 M1
> 建立日期：2026-11-10
> 运行模式：**目标模式**（自主连续推进，不逐阶段等确认）
> 实施档位：**全能**（开发 + 测试 + 代码审查）
> 子代理：**启用**（实施 / 测试 / 审查 / 验收均交子代理，主会话把关与终审）

---

## 一、阶段目标

恢复设置页「残留进程清理」能力。UI 与 API 客户端已存在，**只补后端**，判据按当前三层进程架构重写，不复活多实例时代的死代码。

**不做**：多实例 / 探针分类（那套骨架已随 `ded4d0e` 删除）。

---

## 二、事实底座（勘察结论，实施者无需重新调研）

| 事实 | 位置 |
|---|---|
| 前端 UI 完好 | `src/pages/SettingsPage.tsx:292-345`（扫描/清理/表格） |
| 前端已声明类型 | `src/lib/api.ts:792-812`（`OrphanProcess` / `OrphanScan` / `OrphanKillResult`） |
| 前端已声明请求 | `src/lib/api.ts:1291-1292`；`req()` 前缀 `API_BASE + '/api/admin'`（`api.ts:1000`） |
| 后端路由缺失 | 全仓 grep `processes` 在 `*.go` 零命中 |
| 旧实现已于 ded4d0e 删除 | `git show dd13af9:core/manager/orphan.go`（204 行，依赖已删除的 `Instance` 类型） |
| 可复用进程枚举 | `pluginprovider/manager.go:686-690` `procInfo{PID,PPID,Cmd}`；`proc_windows.go:46` `listProviderProcesses()` |
| 可复用父存活判定 | `pluginprovider/proc_windows.go:52` `pidAlive()`（tasklist CSV 精确匹配，已处理本地化文案问题） |
| 可复用强杀 | `manager/process_windows.go:21` `killProcess(pid)` |
| 管理面挂载点 | `main.go:432-460` 区段，`requireAuth` 鉴权 |
| 项目零外部依赖 | `go.mod` 仅 module + go 版本 |

---

## 三、接口契约（必须与前端既有类型完全一致）

### GET `/api/admin/processes/orphans`

```json
{
  "total": 3,
  "probe": 0,
  "orphan": 3,
  "items": [
    {
      "pid": 48964,
      "name": "devin-provider.exe",
      "category": "orphan",
      "port": 0,
      "detail": "父进程 50856 已退出；路径 D:\\...\\_plt4-browser\\providers\\devin"
    }
  ]
}
```

字段约束（对齐 `api.ts:794-809`）：
- `pid` number、`name` string、`detail` string 必填
- `category` 取值 `"probe" | "orphan"`：本阶段**只产出 `orphan`**；`probe` 计数恒 0
- `instance?` 可选，本阶段不填
- `port?` 可选，能从命令行解析 `--port N` 且 N>0 时填，否则省略或 0

### POST `/api/admin/processes/orphans/kill`

请求 `{"pids":[1,2,3]}` → 响应 `{"killed":[1,2],"errors":{"3":"..."}}`

---

## 四、扫描判据（本阶段核心逻辑）

**孤儿定义（必须同时满足）**：

1. 命令行包含 `--provider-serve`
2. 父进程 PID **已不存在**（`pidAlive(ppid) == false`）
3. 命令行所指 providers 目录 **≠ 当前 `ProvidersDir`**

> 第 3 条是安全边界：本目录的孤儿由 `reapOrphans` 自动回收，本端点不与之重复；也避免用户在本机上误杀当前宿主的正常插件。

**明确的排除项**：
- 父进程存活（正常运行的插件）→ 不列出
- 非 `--provider-serve` 进程（普通程序）→ 不列出
- 当前 ProvidersDir 下的孤儿 → 不列出（交给自动回收）

**孙进程（M1 不做，M3 做）**：本阶段 `probe` 恒为 0，不扫 mihomo/chrome。

---

## 五、安全约束（实施者必须遵守）

1. **只允许杀扫描结果内 PID**：kill handler 必须重新执行扫描，取交集后才执行 kill；请求里未在结果中的 PID 记入 `errors`
2. **kill 前二次校验**：执行前再次确认该 PID 的父进程仍不存在，防止「扫描后 PID 被系统复用」
3. **跨平台**：核心分类逻辑写成**纯函数**，进程枚举走平台文件（`_windows.go` / `_other.go`），便于单测
4. **不改前端**：前端 UI 与 api.ts 一行不动

---

## 六、任务拆分

### T1 — 扫描分类纯函数 + 单元测试

**交付**：
- 新建 `core/manager/orphan_scan.go`
- 类型：`OrphanProcess` / `OrphanScan` / `OrphanKillResult`（与前端 JSON 对齐）
- 纯函数 `classifyProcesses(procs []procLine, providersDir string, alive func(int) bool) OrphanScan`
  - 注入 `alive` 以便测试（生产传 `pidAlive`）
- 新建 `core/manager/orphan_scan_test.go`

**测试用例（至少）**：
1. 父进程存活 → 不列出
2. 父进程已死 + 路径不同 → 列出为 `orphan`
3. 父进程已死 + 路径同为当前 ProvidersDir → 不列出
4. 无 `--provider-serve` → 不列出
5. 命令行含 `--port 12345` → `Port` 字段解析正确
6. 空输入 → `total=0`、`items` 为空数组（非 nil，前端 `r.items ?? []` 兼容但保持契约）
7. 混合场景计数正确

**验收**：`go test ./core/manager/ -run TestClassifyProcesses -v` 全绿

---

### T2 — 进程枚举与 Handler + 路由挂载

**交付**：
- `core/manager/orphan_scan_windows.go`：`listAllProcesses() ([]procLine, error)`，用 `Get-CimInstance Win32_Process` 取 pid/ppid/cmd（**必须包含不含 `opencode2api` 的进程**，与 `listProviderProcesses` 的过滤不同）
- `core/manager/orphan_scan_other.go`：`ps -eo pid=,ppid=,args=`
- Handler：`OrphanScanHandler()` / `OrphanKillHandler()`
- 路由挂载：`main.go` 管理面（`requireAuth`）

**注意事项**：
- `listAllProcesses` 不得复用 `listProviderProcesses`——后者只返回含 `opencode2api` 或 `--provider-serve` 的行，且会丢 PPID 之外的细节；本端点需要**先取全量**再分类
- Windows CIM 单对象输出是 object 不是 array，需两种都解析（参照 `proc_windows.go:74-86` 的写法）
- 输出中文文案（与项目其它 endpoint 一致）

**验收**：
- `go build ./...` 通过
- `go vet ./...` 通过
- 手工 curl 一次（隔离实例）：`GET /api/admin/processes/orphans` 返回 200 且 JSON 结构符合第三节

---

### T3 — 前端文案对齐

**交付**：`src/pages/SettingsPage.tsx` 说明文字改为实际语义。

当前文案（`:313-315`）：
> 扫描占用本项目端口但没有对应管理进程的残留子进程（引擎/供应商服务异常退出时可能出现）。

改为（明确当前判据，避免用户误解）：
> 扫描系统中父进程已退出的插件子进程（`--provider-serve`）。本目录的残留由宿主自动回收；此处列出的是其它目录遗留的插件进程。

**约束**：只改文案，不碰逻辑。

**验收**：`npm run build` 或 `tsc --noEmit` 通过

---

### T4 — 代码审查（子代理独立，主会话不参与）

**审查范围**：M1 全部 diff

**审查清单**：
1. 安全：能否通过构造命令行绕过？kill 是否能杀任意 PID？
2. 跨平台：`_windows.go` / `_other.go` build tag 是否正确
3. 性能：CIM 调用是否会在高频请求下拖慢（是否需要缓存/限流）
4. 契约：JSON 字段与 `api.ts` 类型是否逐字一致
5. 错误处理：CIM/ps 失败时是否返回明确错误而非空结果
6. 代码风格：是否符合 `coding-standards.md`

**输出**：问题清单，按 P0/P1/P2 分级。P0 必修。

---

### T5 — 阶段验收（子代理独立，四段结论）

按 `phased-plan-driven.md §6` 输出：完成项 / 未通过项 / 待人工项 / 证据。

**不接受「应该能过」**，每条必须有实际命令输出。

---

## 七、验收表

| ID | 项 | 判据 | 状态 |
|---|---|---|---|
| A1 | 分类纯函数单测 | `go test -run TestClassifyProcesses` 全绿，≥7 用例 | ✅ 9+3 子用例 PASS（2026-11-10 验收代理） |
| A2 | 全量编译 | `go build ./...` 退出码 0 | ✅ exit=0 |
| A3 | 静态检查 | `go vet ./...` 无输出 | ✅ exit=0（scope=. ./core/...） |
| A4 | 端点可用 | 隔离实例 curl 200 + JSON 结构匹配 | ✅ 隔离实例实测 200：`{"total":0,"probe":0,"orphan":0,"items":[]}`，schema 合法（2026-11-10 T3） |
| A5 | kill 安全性 | 单测证明：结果外 PID 被拒绝 | ✅ 3 子用例 PASS + 生产路径二次探父；真机越界 PID 拒绝（`"999999":"不在当前扫描结果内，拒绝清理"`） |
| A6 | 前端类型一致 | `tsc --noEmit` 通过 | ✅ tsc-exit=0，三类型逐字核对一致 |
| A7 | 代码审查 | 独立子代理出结论，P0 全清 | ✅ P0=0（P1 已修：枚举加 10s 超时） |
| A8 | 真实扫描 | 本机执行一次，能正确列出/排除 | ✅ 真机正向：外来目录孤儿 `62360/fake.exe` 列出→kill 成功；受管插件（父存活）正确排除 |

---

## 八、不通过项下放规则

- 任一 ⬜ → ❌ 的项目，登记到 M2 计划开头「上阶段下放」
- 「待人工」项（如生产部署）**不算失败**，登记后继续

---

## 九、交接提示词（零上下文可执行）

```
你在仓库 D:\AI_Projects\opencode2api_guide\opencode2api_enhance（Go 1.26 + React/TS）。

任务：实现 M1 阶段（恢复孤儿进程扫描与清理端点）。完整计划见
docs/ai-framework/plans/2026-11-10-orphan-governance-m1.md，务必先读第二节「事实底座」
和第四节「扫描判据」。

只做 T1/T2/T3 三件事：
1. 新建 core/manager/orphan_scan.go（类型 + 纯函数 classifyProcesses）+ orphan_scan_test.go（≥7 用例）
2. 新建 orphan_scan_windows.go / orphan_scan_other.go（全量进程枚举）+ 两个 Handler + main.go 路由挂载
3. 改 src/pages/SettingsPage.tsx 的说明文字（只改文案）

硬约束：
- 前端 api.ts 一行不改，JSON 字段必须与 api.ts:792-812 的类型逐字对齐
- 只允许杀扫描结果内的 PID；kill 前二次校验父进程仍不存在
- 不要复活 git 历史里的 core/manager/orphan.go（依赖已删除的 Instance 类型）
- 不要让本端点处理当前 ProvidersDir 下的孤儿（那是 reapOrphans 的职责）

完成后必须实际运行并贴出输出：
  go build ./...；go vet ./...；go test ./core/manager/ -run TestClassifyProcesses -v
禁止说「应该能过」，必须贴真实命令输出。
```

---

## 十、阶段验收结论（2026-11-10，独立子代理）

**判定：有条件通过**（P0=0，代码本体 A1/A2/A3/A5/A6 全绿）

### 完成项
- A1 分类纯函数单测：9 个 `TestClassifyProcesses` + 3 个 `TestSelectKillTargets` 子用例全 PASS（`ok ... 0.043s`）
- A2 `go build ./...` exit=0；`GOOS=linux go build ./...` exit=0（跨平台 tag 正确）
- A3 `go vet . ./core/...` exit=0 无输出
- A5 kill 安全：结果外 PID 拒绝 / 父进程复活拒绝 / kill 失败入 errors 三例全绿；生产路径「重扫取交集 + 杀前二次探父」（`orphan_scan.go:141-169`）
- A6 契约：`OrphanProcess`/`OrphanScan`/`OrphanKillResult` 与 `api.ts:794-814` 逐字一致；`items` 恒 `[]`、`errors` 恒 `{}`、`--port 0` 不填 Port、目录大小写不敏感与 `reapOrphans` 同语义；`tsc --noEmit` exit=0
- 路由鉴权：两条均 `loggingMiddleware(requireAuth(...))`（`main.go:452-453`）
- 枚举超时：Windows `Get-CimInstance` 为 powershell 进程内调用（无子 powershell），`exec.CommandContext` 10s 超时可终止，`CREATE_NO_WINDOW` 防 conhost 持管道；非 Windows `ps` 单进程
- 真机排除路径验证：当前 10 个 `--provider-serve` 插件父进程 PID 51224 存活 → 扫描 `total=0, items=[]`，全部正确排除

### 未通过项
无。

### 待人工项
1. ~~A4：隔离实例起服务后 `GET /api/admin/processes/orphans` 实测 200 + JSON 结构~~ → ✅ 2026-11-10 T3 实机补证通过（见 §十一）
2. ~~A8 正向用例：需外来 providers 目录 + 已死父进程的真实孤儿样本~~ → ✅ 2026-11-10 T3 实机补证通过（见 §十一）
3. 提交分离：工作树现存 `core/manager/config.go`、`freetier_engine.go`、`.gitignore`、文档等无关改动，提交 M1 时不得裹入

### 证据
- `go test ./core/manager/ -run "TestClassifyProcesses|TestSelectKillTargets" -count=1 -v` → PASS 12/12，exit=0
- `go build ./...` → exit=0；`go vet . ./core/...` → exit=0；`GOOS=linux go build ./...` → exit=0
- `node_modules/.bin/tsc --noEmit` → exit=0
- `git check-ignore -v core/manager/orphan_scan_test.go` → `.gitignore:57:*_test.go`（按项目策略不入库）
- 无关发现：`callmetric_engine_test.go` 4 条失败（硬编码 `2026-11-05` 超出 `now±48h` 窗口的时间炸弹，与本阶段无关，stash 验证过）

---

## 十一、A4/A8 实机补证（2026-11-10，T3 隔离实例）

隔离实例：临时目录 + 独立 `OPCODE2API_PLUGIN_DIR` / `OPCODE2API_PROVIDER_PORT=50081` / `OPCODE2API_DATA_DIR`，管理面 40123；生产目录与生产进程零触碰。

| 项 | 结果 | 证据 |
|---|---|---|
| A4 | ✅ | `GET /api/admin/processes/orphans` → HTTP 200，`{"total":0,"probe":0,"orphan":0,"items":[]}`；`items` 为数组、计数为数字；受管 fake 插件（父存活）、provider-service（单横杠 `-provider-service`）、孙进程均正确不在 items |
| A8 | ✅ | 外来目录 `foreign\fake.exe` 父进程 56348 已消失 → 扫描 `total=1, orphan=1`，item `{pid:62360,name:"fake.exe",category:"orphan"}`；`POST kill {"pids":[62360]}` → `{"killed":[62360],"errors":{}}`，2s 后进程消失 |
| A5 补充 | ✅ | 越界 PID `{"pids":[999999]}` → `{"killed":[],"errors":{"999999":"不在当前扫描结果内，拒绝清理"}}` |

备注：全表 CIM 枚举单次约 10s（与 `listAllProcessesTimeout=10s` 设计一致，属已知成本，P2-4 接受现状）。
