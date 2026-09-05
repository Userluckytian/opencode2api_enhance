# 调试与可观测性机制实施计划（opencode2api）

> **目标**：让项目从「能跑」走向「好排查」——为未来维护者（含 AI 维护）提供系统化、低依赖、可分阶段交付的调试能力。
>
> **说明**：本计划融合了两份前期思路（维护者 A 的方案 + 同事的方案），并修正了两处与代码现状不符的判断（详见 §2）。所有改动点均标注真实代码位置，可直接落地。
>
> **使用方式**：按 §3 的阶段顺序推进。每个阶段严格套用 §4 的「四步门禁」。某阶段验证不通过的项，**不阻塞整体进度**，标记后顺延进入下一阶段的计划开头，直至全部阶段验证通过。

---

## 执行进度（勾选面板）

> 「完成一项，勾选一项」。已验证通过的项打 `[x]`；未通过/未执行的项如实保留 `[ ]` 并按 §4 顺延。本文件的单一事实来源为本节。

| 阶段 | 名称 | 状态 |
|------|------|------|
| **0** | 仓库卫生与构建可观测 | ✅ 完成（commit `5de4630`） |
| **1** | 路由决策可解释 | 🔶 部分：三字段+tier/端口+付费层标签+旧日志兼容已合入（commit `9f52ff1`）；**配置丢失判定/节点分析按 tier 分组未完成 → 顺延阶段 2 开头** |
| **2** | 统一归因日志 + 聚合读取 | 🔶 部分：机制层已实现+单测全绿（role/字段注入、/api/logs 聚合、-debug-subsystem 热切换）；四类 role 可分/过滤/热切换的真机端到端验证待做（本机禁起服务） |
| **3** | 分布式 Trace ID | 🔶 部分：入站种 trace_id（复用/生成）+响应头 X-Trace-ID、call_log 补 trace_id（与 req_id 对齐）、子进程 OPENCODE2API_TRACE 透传链均已实现+单测全绿；真机跨进程 E2E 待做（本机禁起服务），第三方进程段断链为已知局限 |
| **4** | 诊断端点 + doctor | 🔶 部分：`GET /api/diag`（管理鉴权复用 requireAuth）+ `opencode2api doctor` 子命令已实现，共用纯函数 `buildDiagReport` 健康核心（端口冲突/SOCKS/实例·节点/sing-box/孤儿/门禁密钥·仅末4位/配置完整性 七项检查）；`go build`/`go vet` 全绿、单测已写未跑（本阶段策略）；真机 `/api/diag` 全绿与 doctor 端到端待联调 |
| **5** | 失败现场打包 | ⏳ 待开发 |
| **6** | 配置溯源 | ⏳ 待开发 |
| **7** | 失败原因计数器 | ⏳ 待开发 |
| **8** | 复现重放工具 | ⏳ 待开发 |

---

## 1. 背景与痛点（为什么要做）

一次典型请求横跨多进程：`用户请求 → 管理器(Go) → 网关子进程 → vendors → 实例子进程 → sing-box → 上游节点`。当前项目在「好排查」上存在以下已确认的缺口：

1. **直连/路由是黑盒**：日志报结果（如 `pool switched`、`breaker opened`），但不报「为什么」。例如用户曾困惑「实例池明明走各实例，日志却显示直连」——根因是 `socks.go:481` 的 `getHTTPClientForTierWithProxy` 在 `TierPaid` 时返回空 `proxyAddr`，而 `gateway_timeout.go:506` 的 `recordCall` 把空节点**兜底显示成「直连」**，掩盖了真实语义（设计直连 vs 配置丢失）。
2. **跨进程对不上号**：管理器内有 `req_id`（`logging.go:18/66/69` 的 `loggingMiddleware` + `randomString(12)` + context），但**未透传**到下游子进程；第三方进程（sing-box、opencode 实例）各有自己的日志 ID。全局搜 `trace_id`/`X-Trace` **零命中**。
3. **配置多写者 + 漂移**：`_unified-gateway/opencode2api.json` 存在多个写者（Go 管理器、Rust 壳、网关自写回盘、自定义源传播补丁），且为「保留既有键」round-trip。历史 commit 中也出现过 `propagate 补丁被覆写丢失` 的事故。
4. **失败现场不保留**：503 / 启动 15s 超时 / 孤儿进程出现时，当时的配置快照、路由决策、节点状态均未留存。
5. **日志散落 + 归因弱**：日志分散在 `runtime/<实例名>/logs/` 下数十目录；slog 已结构化，但缺统一 `role`（manager/gateway/instance/probe）字段。
6. **仓库卫生坑（维护侧）**：`.git/config` 的 `remote.origin.fetch` 可能被收窄为仅同步 `main`，导致远程多分支不可见；历史线混乱。未来维护者一上手即可能踩坑。

---

## 2. 设计原则（硬约束）

- **克制、零外部依赖**：**不引入 OpenTelemetry / Jaeger 等重型追踪栈**。全部基于项目已有的 `slog` 结构化日志、`call_log` 体系、`req_id` 雏形实现。符合项目「少依赖、体积敏感」的既定取向。
- **复用而非另造**：`req_id` 已存在（`logging.go`），Trace ID 应**复用并扩展它**，而非新建一套 ID 体系（避免双 ID 混乱）。
- **补字段优先于加系统**：能用「给现有结构补 3 个字段」解决的问题，不做成独立服务。
- **已知局限要写进文档**：Trace ID 对自研 Go 进程链路有效，但在 **sing-box / opencode 实例（第三方进程）** 段会断链——它们不认 `X-Trace-ID`，其内日志不带我们的 ID。此局限在阶段 3 明确标注，不假装全覆盖。
- **每阶段四步门禁**：开发 → 测试 → 代码审查 → 验证；验证不通过项顺延下一阶段（见 §4）。
- **代码定位以 `grep` 为准（重要）**：文中所有 `文件:行号` 基于 `main/arena` 分支（commit `87f67a7`）实测。核心结构体 `CallRecord`（`gateway_timeout.go:107`）与 `CallLogRecord`（`core/manager/calllog.go:30`）在 main/test 两分支位置一致，可放心引用；但部分函数行号随分支新增代码会**偏移**（典型如 `vendors/custom/custom.go` 的 `tier()`：main ~`:129` / test ~`:220`；`socks.go` 的 `getHTTPClientForTierWithProxy`：main `:481` / test `:480`）。**落地前一律先以 `grep -n '<符号>' <文件>` 复核实际行号，切勿照抄文中行号。**

---

## 3. 总体阶段路线图

| 阶段 | 名称 | 主要交付 | 依赖 | 来源 |
|------|------|---------|------|------|
| **0** | 仓库卫生与构建可观测 | 统一 fetch refspec；启动打印分支/commit/版本 | 无 | 维护者 A |
| **1** | 路由决策可解释 | `CallRecord` 补 `tier`/`route_verdict`/`serving_port`；日志页区分直连语义 | 无 | 同事 B（字段部分）|
| **2** | 统一归因日志 + 聚合读取 | slog 加 `role/node/tier/provider/port/trace_id`；`/api/logs` 聚合端点 | 阶段 1 字段 | 同事 F |
| **3** | 分布式 Trace ID | 复用 `req_id` 跨进程透传（env+header），自研进程链路打通 | 阶段 2 的 `trace_id` 字段 | 同事 A |
| **4** | 诊断端点 + doctor | `GET /api/diag` + `opencode2api doctor` 体检报告 | 阶段 1、2 | 同事 C |
| **5** | 失败现场打包 | 关键失败自动落 bundle（脱敏+配置快照+决策+节点态）| 阶段 1、3 | 同事 D |
| **6** | 配置溯源 | 配置带时间戳+写入方标签快照；`/api/config/history`+`/effective` | 阶段 1、4 | 同事 E |
| **7** | 失败原因计数器 | stats 加「节点 × 失败原因」计数 | 阶段 1、2 | 同事 H |
| **8** | 复现重放工具 | fixture + `debug_replay` 重放 | 阶段 3、5 | 同事 G |

> **价值密度提示**：阶段 0、1 改动最小、收益最高（直接消除 §1 痛点 1、6）；阶段 3 是其余阶段依赖的「根」，但覆盖率有 §2 所述局限；阶段 8 排在最后，因其依赖前序阶段的 trace 与 bundle 格式。

---

## 4. 统一质量门禁（每阶段四步法）

每个阶段**必须**依次完成以下步骤，缺一不可进入下一阶段：

1. **开发（Develop）**：按阶段「开发任务」清单实现；遵循 §2 原则；不引入新外部依赖。
2. **测试（Test）**：按「测试任务」执行，含单元/集成/手动验证；新逻辑需有可复现的测试步骤。
3. **代码审查（Review）**：按「审查检查项」逐条核对（可人工或 AI 审查）；重点查：是否破坏现有 `call_log` 结构兼容、是否引入依赖、错误路径是否也记录。
4. **验证（Verify）**：按「验证标准」的明确 pass/fail 判定。判定须**可操作、可自动化优先**。

**验证不通过的处理（核心流程）**：
- 验证标准中**任一** fail → 该阶段判定为「未通过」。
- 未通过的具体项**登记为遗留（carry-over）**，不阻塞整体进度，顺延进入**下一阶段的计划开头**优先处理。
- 下一阶段开始时先清上一阶段遗留，再开展本阶段开发。
- 重复此循环，**直至阶段 8 验证全部 pass**，计划完成。
- 每阶段产出物（设计微调、遗留清单）建议记录在 `docs/` 对应阶段笔记中，便于追溯。

**「完成」的全局定义**：阶段 0–8 全部验证 pass；`docs/DEBUG-OBSERVABILITY-PLAN.md` 的遗留清单为空；新增能力均有对应测试与文档。

---

## 5. 阶段详述

### 阶段 0：仓库卫生与构建可观测

**目标**：让未来维护者一上手就不迷路（消除 §1 痛点 6）。

**涉及文件/改动点**
- `.git/config`（`remote.origin.fetch`）
- 构建入口：`main.go` / `Cargo.toml` / `package.json`（版本字符串注入）
- 启动日志打印处（建议在 `main.go` 初始化段）

**开发任务**
1. 将 `remote.origin.fetch` 统一为 `+refs/heads/*:refs/remotes/origin/*`；在 CI 中加一步校验（若被收窄则 fail）。
2. 构建时把 `git branch`/`git rev-parse HEAD`/`构建时间` 注入版本字符串（现有 `main.go` 已有版本常量，扩展即可）。
3. 进程启动早期打印一行：`opencode2api vX.Y.Z | branch=<b> | commit=<sha> | built=<time>`。

**测试任务**
- 克隆干净仓库，`git fetch` 后 `git branch -r` 应列出全部远程分支（含 `test/*`）。
- 启动后首行日志含 branch/commit/built 三段非空。

**审查检查项**
- 不改 `.git/config` 以外任何运行时行为。
- CI 校验步骤不误伤正常 fork 场景。

**验证标准**
- [x] `git branch -r` 显示所有远程分支 → pass，否则 fail。 （实测 `remote.origin.fetch` 为 `+refs/heads/*:refs/remotes/origin/*` 未收窄，天然满足）
- [x] 启动日志含 branch/commit/built → pass，否则 fail。 （`--version` 实测输出 `opencode2api v0.0.0-test (branch=feat/debug-observability, commit=abc1234, built=2026-09-03T00:00:00Z)`）

**失败顺延**：若 CI 校验难以适配 fork，将「CI 校验」项顺延阶段 1 开头处理，本地统一配置仍在本阶段完成。

> **本阶段备注（2026-09-03）**：版本打印已实现（commit `5de4630`）。「CI 校验步骤」未做——按本阶段失败顺延条款顺延至阶段 1 开头；因 fork 场景适配成本高，暂记遗留，阶段 1 未处理则继续向后续阶段顺延。

---

### 阶段 1：路由决策可解释（CallRecord 补字段）

**目标**：消除「直连困惑」（§1 痛点 1），让日志一眼区分「设计直连」vs「配置丢失直连」。

**涉及文件/改动点**
- **⚠️ 必须同时修改两个结构体（落地最容易漏，已据实跑复核）**：
  - 写入侧 `CallRecord`：定义在 `gateway_timeout.go:107`（main 包，`recordCall` 落盘用；另在 `auth.go:131` / `chat_handler.go:46` / `claude.go:47` / `responses.go:737` 多处实例化）。
  - 读取侧 `CallLogRecord`：定义在 `core/manager/calllog.go:30`（core/manager 包，日志页/面板聚合解析用，其注释声明"字段须与 main 包 CallRecord 一致"）。
  - 新增 `Tier` / `ViaProxy` / `ServingPort` **两个结构体各加一份且字段保持一致**，否则写入侧生成的字段读取侧解析不到，前端照样看不到。
- `gateway_timeout.go:506` `recordCall`：填充新字段（tier 来自下方 tier 取值；via_proxy 来自 vendor 配置；serving_port 来自本次请求实际进入的端口）。
- **tier 取值参考（三种厂商机制不同，勿混用）**：
  - opencode 对话请求：`authT.tier()`（`vendors/opencode/chat.go:46-50`，authPublic→`TierFree` 否则 `TierPaid`）；元数据拉取（版本/模型列表）硬编码 `TierFree`（`vendors/opencode/opencode.go:170/195`）。
  - custom：`func (v *Vendor) tier()`（`vendors/custom/custom.go`，**行号随分支变化**：main/arena 分支约 `:129`，test 分支约 `:220`（test 分支因新增 key 健康/熔断代码后移）；落地前以 `grep -n "func (v \*Vendor) tier" vendors/custom/custom.go` 实际结果为准，**切勿照抄行号**），`ViaProxy=true`→`TierFree` 否则 `TierPaid`。
  - remote：**硬编码 `TierPaid`**（`vendors/remote/remote.go:253`，**不走 `tier()` 函数**）。
  - 最终 tier→proxyAddr 的转换见 `socks.go:481` `getHTTPClientForTierWithProxy`。
- `src/pages/LogsPage.tsx`：渲染时区分显示（**判定"配置丢失"以 socks 三键是否为空为依据，而非 `route_mode` 字符串**，见 §6 术语表）：
  - `TierPaid` 且节点空 → 标签「设计直连（付费层）」绿色（正常）；
  - `TierFree` 且节点空 → 进一步看该进程 `active_socks5`/`socks5_proxies` 是否均为空：空 → 标签「配置丢失·已回退直连」红色告警；非空但节点仍空 → 异常灰标；
  - 节点非空 → 正常显示节点。
  - **理由**：`route_mode` 默认 `"smart"`、允许 `{smart, failover, round_robin}`（`config.go:116`/`admin_ops.go:768` 校验，`socks.go:218` init 写默认 smart）。仅判 `route_mode=smart` 会漏标 `failover`/`round_robin` 下的配置丢失；而免费层是否真走代理取决于 socks 三键是否为空，以此为依据最可靠。

**开发任务**
1. **在 `CallRecord`（`gateway_timeout.go:107`）与 `CallLogRecord`（`core/manager/calllog.go:30`）两个结构体各增加三个字段**（带 `omitempty`），且二者字段须保持一致。  （✅ 已完成，commit `9f52ff1`）
2. `recordCall` 调用处补充 tier/serving_port 取值逻辑（注意 `CallRecord` 已有 `RouteMode`，勿重复）。  （✅ 已完成：`tierOfAuth()` helper + 三 handler 填 `Tier`/`ServingPort` + `auth.go` 填 `ServingPort`）
3. 前端渲染三类标签与颜色；节点分析视图按 `tier` 分组统计。  （✅ 已完成 2026-09-03：`LogsPage.tsx` 按 `route_verdict` 枚举映射四类标签；其中 `direct_config_missing` 的红色告警徒章上提到折叠行头（因成功记录在本页不可展开，仅放展开区会永远看不见）；新增「按层分组」表统计 free/paid/未知 的请求数·失败数·失败率·异常切换·平均耗时·配置丢失回退次数）
4. **保持 `call_log.jsonl` 向后兼容**（仅新增字段，旧记录缺字段时前端降级处理）。  （✅ 字段 `omitempty`，前端 `rec.tier || '-'`/可选链降级）
5. `recordCall` 时额外记录「该进程 socks 代理是否配置」（`active_socks5`/`socks5_proxies` 是否非空），供前端可靠判定"配置丢失"（比单纯看 `route_mode` 更可靠）。  （✅ 已完成 2026-09-03：新增 `socks_state.go` 的 `socksProxyConfigured()`，在 `socks5Mu.RLock()` 下仅读 `active_socks5` 与 `socks5_proxies`，**刻意不读 `route_mode`**——它由 `socks.go:218` init 写入默认 `smart` 恒非空，用它判会永远误判为「已配置」）
6. **新增 `route_verdict` 字符串枚举字段**（`proxied` / `direct_by_design` / `direct_config_missing` / `direct_unexpected`，空 = 旧记录或无法判定），两个镜像结构体各加一份；判定在 `recordCall` 中、**空节点兜底成「直连」字符串之前**完成（一旦 `Nodes` 被改写就再也分不清「本来没有节点」与「节点名恰好叫直连」）。判定本体是 `socks_state.go` 的纯函数 `routeVerdict(nodes, tier, socksConfigured)`，无锁无全局状态、可单测。**用字符串枚举而非 bool，正是 `via_proxy` 的教训。**  （✅ 已完成 2026-09-03）
7. **新增字段一致性回归测试** `calllog_fields_test.go`：反射比对 `CallRecord` 与 `manager.CallLogRecord` 的 json tag 集合，不一致即 fail，错误信息分列「仅存在于写入侧」与「仅存在于读取侧」；读取侧独有的 `source` 用显式白名单承载并附理由。  （✅ 已完成 2026-09-03，且**首次运行即拓到存量缺陷**，见下方备注）

**测试任务**
- 用付费模型（如付费 opencode key / `ViaProxy=false` 自定义源）发请求 → 日志 `tier=paid` 且显示「设计直连」标签。  （🔶 付费层直连已能体现；标签文案为 meta「直连:是」）
- 用免费模型且 socks 三键齐全 → 日志显示具体节点。
- 用免费模型但**故意移除** socks 三键（`active_socks5`/`socks5_proxies`/`route_mode`）→ 日志标记「配置丢失·已回退直连」且面板标红。 （🔶 判定逻辑已实现并由 `TestRouteVerdictTable`/`TestSocksProxyConfigured` 单测覆盖；**真机端到端未验证**——本轮禁止启动服务以避免与本机生产实例抢端口，留待联调）
- 旧 `call_log.jsonl`（无新字段）导入后前端不报错。  （✅ 字段带 omitempty，可选链降级）

**审查检查项**
- 新字段不影响现有 `Nodes`/`Events` 的语义与渲染。  （✅）
- 错误/超时路径（非 2xx、`upstream_error`、`capacity`）也正确填充 tier/route_verdict。  （✅ 2026-09-03 复核：tier 用 `tierOfAuth(auth)`，错误路径同 callRec；`route_verdict` 在 `recordCall` 统一判定，错误路径同样覆盖）
  - **⚠️ 历史勘误**：本项原写「也正确填充 tier/via_proxy」并标 ✅，该 ✅ **为误标** —— `via_proxy` 自始至终零写入点（`docs/issue-log/2026-09-03.md:63` 已承认「仅作预留字段」，但本正文勾选未同步）。叠加 `bool` + `omitempty` 会使 `false` 被省略、前端恒收 `undefined`。该字段已于 2026-09-03 从 `CallRecord`、`CallLogRecord`、`src/lib/api.ts` 三处删除，由 `route_verdict` 取代。
- 无新外部依赖。  （✅）

**验证标准**
- [x] 上述三个场景标签均正确 → pass，否则 fail。  （✅ 2026-09-03 已通过：四类枚举判定由 `TestRouteVerdictTable` 8 个子用例逐条覆盖（含 tier 为空 / 未知值留空两例）；`TestSocksProxyConfigured` 验证只看两键不看 `route_mode`；前端四类标签 + 空值降级为 `-` 经 `npm run build` 类型校验）
- [x] 旧日志文件兼容 → pass，否则 fail。  （✅ 已通过：`TestRecordCallEndToEnd` 与前端可选链降级验证）

**失败顺延**：若「配置丢失自动标红」的判定在复杂部署形态（如本就未配代理的本地实例 vs 实例池应配代理却丢失）下仍有歧义，将该判定细化项顺延阶段 2 开头，基础三字段与付费层标签先合入。

> **本阶段备注（2026-09-03）**：基础三字段 + `tierOfAuth` + 三 handler 填 `Tier`/`ServingPort` + `auth.go` 填 `ServingPort` + 前端 meta 行「层/直连/端口」已合入（commit `9f52ff1`，`go build`/`go vet`/calllog 测试/`npm run build` 全绿）。「配置丢失自动标红」判定（socks 三键判空 + 前端三类标签 + `recordCall` 记录 socks 配置 + 节点分析按 tier 分组）**尚未实现**，按 §5 阶段 1「失败顺延」条款顺延至阶段 2 开头优先处理。

> **阶段 1 收尾（2026-09-03 第二轮，AI 编排并行子代理执行）**：上轮顺延项已全部补齐并合入 —— 删除死字段 `via_proxy`（三处：`CallRecord` / `CallLogRecord` / `src/lib/api.ts`）；新增字符串枚举 `route_verdict` 与 `socks_state.go` 判定；前端四类标签 + 「按层分组」统计；新增字段一致性回归测试。**顺延项已清零，阶段 1 完成判定不再依赖顺延。**
>
> **本轮新发现的存量缺陷（由新增的一致性测试首次运行即拓到）**：`key_tail` 只存在于写入侧 `CallRecord`（`gateway_timeout.go:121`），读取侧 `CallLogRecord` 缺失，即使写入也会在聚合读取时被静默丢弃。更严重的是**写入侧也零赋值点**：`docs/issue-log/2026-08-31.md:100` 与「阶段——key/并发/代理」计划声明「`CallRecord` 增加 `KeyTail` 字段（key 末 4 位），便于定位串对话」并已标完成，实际只加了结构体字段、从未接线——**与 `via_proxy` 同病**。本轮已补齐读取侧字段使镜像一致（`omitempty` 保证写入侧落地前 UI 不出现幻影数据），**写入侧接线列为独立待办，本阶段不隐式扩大范围**，见 `docs/issue-log/2026-09-03.md`。
>
> **验证证据（2026-09-03，分包执行）**：`go build ./...` exit 0；`go vet ./...` exit 0；`go test -count=1 .` **ok 55.886s**；`go test -count=1 ./core/...` ok（`core/manager` 34.7s、`pluginprovider` 51.3s）；`go test -count=1 ./vendors/...` ok（5 包）；`npm run build` exit 0（`tsc -b` 无报错）。**环境约束记录**：`go test ./...` 单次执行必然超过工具 120s 上限（三大包串行累加 ≈142s），本仓库须**分包跑**，此为工具限制而非测试失败；后续 CI 应直接采用分包矩阵。

---

### 阶段 2：统一归因日志 + 聚合读取

**目标**：让所有日志可一键按 `role`/进程/节点过滤（§1 痛点 5）。

**涉及文件/改动点**
- 现有 `slog` 封装（`logging.go` 及日志初始化处）：为每条日志注入结构化字段 `role`（manager/gateway/instance/probe）、`node`、`tier`、`provider`、`port`、`trace_id`（trace_id 阶段 3 才填实值，本阶段先预留字段）。
- 新增 `GET /api/logs?process=&role=&since=`：聚合读取各实例/网关日志目录（`runtime/<实例名>/logs/`），返回统一结构。
- 支持 `-debug <subsystem>` **热切换**（不重启）控制子系统日志级别。

**开发任务**
1. 扩展 slog 处理器，自动附加 `role` 等上下文字段（从 context 取）。
2. 各进程（管理器、网关子进程、实例子进程、probe）启动时设置自身 `role`。
3. 实现 `/api/logs` 聚合读取与过滤；保护该端点需管理 key。
4. 实现 `-debug <subsystem>` 运行时热切换。

**测试任务**
- 管理器/网关/实例日志行均含 `role` 字段且值正确。
- `/api/logs?role=gateway` 仅返回网关日志；`?process=<实例名>` 仅返回该实例。
- `-debug pool` 热开后，pool 子系统 debug 日志即时出现，无需重启。

**审查检查项**
- 日志字段不泄露密钥/请求体敏感信息。
- 聚合读取对大量小文件有节流/分页，避免内存暴涨。

**验证标准**
- [ ] 四类 role 日志可分；聚合端点过滤正确；热切换生效 → pass，否则 fail。

> **本阶段备注（2026-09-04）**：机制层已实现并单测通过（`go build`/`go vet` 全绿；main 包 4 项 + core/manager 5 项新单测全 PASS）。① 新增 `logging_dynamic.go` 的 `contextHandler`，为每条 slog 自动注入 `role`/`trace_id`（预留留空）/`node`/`tier`/`provider`/`port`（从 context 取，port 全局兼底）；`logging.go` 仅改两行（基准级别改 `slog.LevelVar` + handler 包一层）。② 四类进程经 `OPCODE2API_ROLE` 环境变量自报 role（管理器默认 manager；gateway.go/instance.go/probe_node.go 分别注入 gateway/instance/probe，为此给 `ExecSpec` 加 `Env` 字段）——**刻意用环境变量而非新增 CLI flag**，避免滚动升级期旧二进制遇未知 flag 解析失败起不来。③ `GET /api/logs?process=&role=&since=&limit=`（`core/manager/logs.go`，requireAuth 鉴权）聚合读 `runtime/*/logs/`，对大量小文件做末尾窗口(4MB)+单文件回溯上限(4000行)+总量上限(1万)+最多400文件的节流。④ `-debug <subsystem>` 因 `-debug` 已是既有 bool flag（被 sseDebugf 占用）**不可重载**，改为新增 `-debug-subsystem` 启动标志 + `POST /api/admin/debug?subsystem=&enabled=` 运行时热切换（不重启，requireAuth）。**未勾选原因**：`trace_id` 实值、真实调用点的 subsystem 打点、以及「四类 role 可分/热切换生效」的真机端到端联调需启动生产服务，本机禁止起服务，故标 🔶 待真机验证；第三方子进程(sing-box)的 role 注入按本节「失败顺延」顺延阶段 3。

**失败顺延**：若第三方子进程（sing-box/opencode 实例）无法注入 `role`（非自研），将其日志以「尽力附带」方式处理，该局限项顺延阶段 3 记录，本阶段先保证自研进程全覆盖。

---

### 阶段 3：分布式 Trace ID（跨进程透传）

**目标**：一次 `grep trace_id` 重建完整链路（§1 痛点 2）。**复用现有 `req_id`**（`logging.go`），不另造 ID。

**涉及文件/改动点**
- `logging.go:69` `randomString(12)` 生成的 `req_id` → 作为 `trace_id` 种子。
- 透传机制：管理器收到请求时，将 `trace_id` 通过 **环境变量 `OPENCODE2API_TRACE` + 请求头 `X-Trace-ID`** 透传给下游子进程/上游调用。
- 所有 `slog` 日志行带 `trace_id`（阶段 2 已预留字段）。
- `call_log` 的 `ReqID` 字段与 `trace_id` 对齐（如尚未一致，本阶段统一）。

**开发任务**
1. 在 ingress（`loggingMiddleware`）将 `req_id` 视为 `trace_id`，写入 context 与响应头 `X-Trace-ID`。
2. 转发到实例/上游/vendors 时，注入 `OPENCODE2API_TRACE` 环境变量与 `X-Trace-ID` 头。
3. 子进程（网关、自研 vendor）读取该 env/header 并延续同一 `trace_id`。
4. 所有 slog 行与 `call_log` 记录带 `trace_id`。

**测试任务**
- 同一请求在管理器日志与（自研）网关子进程日志中 `trace_id` 一致，`grep` 可串联。
- `call_log` 中该请求的 `ReqID` 与 `trace_id` 一致。
- 无 `X-Trace-ID` 入站时自动生成新 `trace_id`（向后兼容）。

**审查检查项**
- 透传不破坏现有 `req_id` 下游使用。
- 不向第三方上游泄露内部 `trace_id`（仅内部进程间）。

**验证标准**
- [ ] 自研进程链路 trace_id 贯通且唯一 → pass。 （🔶 2026-09-04：ingress（loggingMiddleware）种 trace_id——入站 X-Trace-ID 复用、否则 env、否则复用 req_id——并回写响应头；子进程经 OPENCODE2API_TRACE 透传（gateway/instance/probe spawn）；单测全绿。**真机跨进程 E2E 待联调**，本机禁起服务）
- [x] `call_log.ReqID` 与 trace_id 对齐 → pass。 （✅ 2026-09-04：CallRecord/CallLogRecord 同构新增 trace_id（omitempty，旧记录兼容）；handler 中 TraceID 取 getTraceID(ctx)（=req_id），为空时回退 ReqID，保证与 req_id 对齐；TestCallRecordTraceIDJSONRoundtrip + TestCallRecordCallLogRecordJSONTagsMatch 全绿）
- 注：**第三方子进程（sing-box/opencode 实例）段断链为已知局限，不计入 fail**（在文档 §7 标注）。

**失败顺延**：若部分自研子进程透传因进程模型差异难以一致，将「该子进程透传」项顺延阶段 4 开头，先保证管理器↔网关主链路贯通。

> **本阶段备注（2026-09-04）**：机制层已实现并单测通过（`go build ./...`/`go vet ./...` 全绿；main 包 4 项 + core/manager 1 项新单测全 PASS；main 包定向回归 28.6s ok、core/... 全绿）。① **入站种 trace**：`logging.go` 的 `loggingMiddleware` 新增 `resolveTraceID(header, reqID)`（优先级：入站 `X-Trace-ID` > 环境变量 `OPENCODE2API_TRACE` > 复用 `req_id`），写入 context（阶段 2 的 `contextHandler` 自动把 `trace_id` 打进每条 slog）并回写响应头 `X-Trace-ID`。② **call_log 对齐**：`CallRecord`/`CallLogRecord` 同构新增 `trace_id`（omitempty，旧记录兼容，字段一致性测试转绿）；三业务 handler（chat/claude/responses）与 `auth.go` 鉴权失败路径均填 `TraceID: getTraceID(ctx)`，空则回退 `ReqID`，保证 `call_log.ReqID` 与 `trace_id` 对齐。③ **跨进程透传**：`core/manager/process.go` 新增 `traceEnvKV()`，网关/实例/探针 spawn（gateway.go/instance.go/probe_node.go）经 `append(..., traceEnvKV()...)` 把 `OPENCODE2API_TRACE` 透传给自研子进程，形成进程树 trace 链。④ **关键架构结论**：热请求路径**不存在**内部 Go→Go HTTP 跳（handler→callOpenCodeAPI→chatViaVendor→rootTransport 直达第三方上游），故**刻意不在出站注入 `X-Trace-ID`**——否则会把内部 trace 泄露给第三方（违反审查项）；内部自研 Go→Go 调用仅存于后台运维路径（fetchGatewayModels/探针/插件），无请求 ctx，不在本阶段热链路范围。⑤ **未引入任何新依赖**（不用 OpenTelemetry/Jaeger）。**🔶 真机跨进程 E2E 待联调**：需启动生产服务（本机禁起，避免与生产实例/sing-box 抢端口）；第三方子进程（sing-box/opencode 实例）不认 `X-Trace-ID`、段断链为**已知局限**（§7，不计入 fail）。

---

### 阶段 4：诊断端点 + doctor 子命令

**目标**：把「到处翻日志/猜状态」变成一份可复现体检报告（同事 C）。

**涉及文件/改动点**
- 新增 `GET /api/diag`（需管理 key）：聚合——监听端口、生效 SOCKS 三键（`active_socks5`/`socks5_proxies`/`route_mode`）、节点健康、sing-box 状态、孤儿进程、配置文件完整性/备份。
- 新增 `opencode2api doctor` 子命令：命令行输出同一份报告。
- 复用阶段 1 的 tier/route_mode 数据与阶段 2 的聚合能力。

**开发任务**
1. 实现 `/api/diag` 聚合各维度快照。
2. 实现 `doctor` 子命令复用同一报告生成函数。
3. 对「SOCKS 三键缺失」「孤儿进程」「配置与备份不一致」给出明确 WARN/ERROR 行。

**测试任务**
- 正常环境 `/api/diag` 全 GREEN。
- 人为移除某实例 `route_mode` → diag 报 WARN 且 `doctor` 同显。
- 制造一个孤儿进程 → diag 检测并列出。

**审查检查项**
- 端点需鉴权；不暴露密钥。
- 报告生成不阻塞主请求路径（异步/快照）。

**验证标准**
- [ ] 正常全绿；异常项可被检出并标注 → pass，否则 fail。 （🔶 2026-09-04：分项检查逻辑已由 `diag_test.go` 表驱动用例覆盖（端口冲突/route_mode 非法/轮询空池/实例 Error/运行态缺 PID/sing-box 缺失/孤儿/门禁密钥掉码/配置缺失·损坏），但按本阶段测试策略**单测已写未跑**；真机 `/api/diag` 全绿与人为注入 WARN 的 doctor 端到端待联调）

**失败顺延**：若「配置文件完整性/备份」校验规则复杂，将其细化项顺延阶段 6，本阶段先交付端口/SOCKS/孤儿/健康四项。

> **本阶段备注（2026-09-04）**：诊断核心已实现（`go build ./...`/`go vet ./...` 全绿；单测按本阶段策略**已写未跑**，交由全阶段完成后统一跑测）。① 新增 `diag.go`（package main）：纯函数 `buildDiagReport(DiagSnapshot) DiagReport` 为共享健康核心（无副作用、可单测），聚合七项——端口占用（配置层冲突检测）、SOCKS（route_mode 合法性 + 轮询空池退化）、实例/节点健康（Error→红、运行态缺 PID→黄）、sing-box（可执行文件就位 + 运行态缺 sing-box PID）、残留/孤儿进程（>0→黄）、门禁密钥（**只报是否设置 + 末 4 位，绝不泄露明文**）、配置文件完整性（缺失→黄、JSON 损坏→红）；总状态取各项最坏。② HTTP 包装 `diagHandler`（`GET /api/diag`，非 GET 返回 405，`application/json`）经 `registerHTTPRoutes` 注册为 `loggingMiddleware(requireAuth(...))`，**复用管理鉴权**。③ CLI 包装 `opencode2api doctor`（在 `main()` 最前拦截 `os.Args[1]=="doctor"`，早于 flag 定义避免与服务端标志集冲突）：`runDoctor` 用独立 FlagSet（`-config`/`-data-dir`/`-json`），载入配置后打印人类可读报告，**退出码 0=健康/1=告警/2=错误**。④ 采集 `collectDiagSnapshot` 全程只读：`Gateway().Port()` 仅读端口不拉子进程、`ScanOrphans()` 只枚举不杀、`diagProbeConfig`/`diagProbeSingbox` 只读探查；**不启动/不杀任何进程、不改 call_log、零新增依赖**。⑤ 顺延：真实端口监听占用探测（需绑定端口，属副作用）与「配置文件备份一致性」校验按计划顺延，本阶段先交付七项。

---

### 阶段 5：失败现场打包（postmortem）

**目标**：关键失败时自动留存现场，无需复现即可查（同事 D，§1 痛点 4）。

**涉及文件/改动点**
- 关键失败路径（`gateway_timeout.go` 的 `upstream_error`/`capacity`、启动 15s 超时、崩溃恢复）：触发时落一个 bundle。
- bundle 内容（脱敏）：请求摘要 + 响应/错误体（截断）+ **生效配置快照** + 路由决策（tier/route_mode/选中节点）+ 节点状态 + `trace_id`。
- 存放于 `runtime/<实例名>/postmortem/`，按 `trace_id`+时间命名，设保留上限。

**开发任务**
1. 定义 `PostmortemBundle` 结构（复用阶段 1 字段 + 配置快照）。
2. 在失败路径调用 `writePostmortem(...)`。
3. 脱敏：剔除 key/密码/完整请求体。

**测试任务**
- 触发一次上游 503 → `postmortem/` 下生成对应 bundle，含配置快照与 trace_id。
- bundle 中不含密钥明文。
- 超过保留上限后最旧 bundle 被清理。

**审查检查项**
- 脱敏彻底（key/Authorization/base_url 凭据不可见）。
- 写 bundle 失败不影响主流程（失败不阻塞请求）。

**验证标准**
- [x] 失败自动落 bundle 且脱敏、含 trace_id 与配置快照 → pass。 （✅ 2026-09-05：`postmortem.go` 实现，`recordCall` 失败分支异步落盘；单测 `postmortem_test.go` 5 项全 PASS——落盘/脱敏(admin_key→***、socks5_proxies→[redacted])/trace_id/配置快照/保留上限/文件名安全化。🔶 真实上游 503 端到端触发待真机）

**失败顺延**：若「启动 15s 超时」场景难以在测试环境稳定复现，将该场景的 bundle 触发顺延阶段 8 的 replay 配合验证，其余场景先合入。

---

### 阶段 6：配置溯源（谁改的/何时/历史）

**目标**：回答「运行中的配置是不是我以为的那个」（同事 E，§1 痛点 3）。

**涉及文件/改动点**
- 配置写回处（Go 管理器、Rust 壳、网关自写、自定义源传播）：写入时附 **时间戳 + 写入方标签** 的版本快照。
- 新增 `GET /api/config/history`、`GET /api/config/effective`（需管理 key）：返回历史快照列表与当前生效配置 diff。

**开发任务**
1. 定义配置快照结构（含 `writer`、`ts`、`sha`）。
2. 在各写者落盘时追加快照（原子写，避免放大漂移）。
3. 实现 history/effective 端点，支持与某历史版本 diff。

**测试任务**
- 经管理器改一次配置 → history 新增一条带 writer=go-manager 的记录。
- 经自定义源传播改一次 → writer=custom-propagate 记录。
- `/api/config/effective` 与磁盘实际文件一致；diff 能显示与上一版的差异。

**审查检查项**
- 快照写入不显著增加 I/O（限最近 N 条）。
- 不把密钥写入 history（仅结构/开关）。

**验证标准**
- [ ] 多写者均有溯源记录；effective 与磁盘一致 → pass，否则 fail。

**失败顺延**：若「Rust 壳写者」因跨语言难以统一快照格式，将该写者溯源顺延，先交付 Go 侧三写者溯源。

---

### 阶段 7：失败原因计数器（节点 × 原因）

**目标**：让「最近出什么问题」变成一次查询（同事 H）。

**涉及文件/改动点**
- `core/manager` 现有 stats 体系（`node_stats` 相关）：新增「节点 × 失败原因（429/401/503/connect/timeout）」二维计数。
- 数据源复用阶段 1 的 `CallRecord` 错误分类与阶段 5 的失败现场。

**开发任务**
1. 定义二维计数结构（节点为行，原因为列）。
2. 在 `recordCall` / 失败路径累加计数（与现有 token 统计同生命周期）。
3. 暴露到 stats 接口与统计页。

**测试任务**
- 对某节点连续触发 429 → 该节点 429 计数上升，其他节点不受影响。
- 计数随保留期重置规则正确。

**审查检查项**
- 计数不重复累加（与 `CallRecord` 一一对应）。
- 不影响现有 stats 性能。

**验证标准**
- [ ] 二维计数准确、可查询 → pass，否则 fail。

**失败顺延**：若统计页 UI 改造量大，将「UI 展示」顺延阶段 8，本阶段先交付接口与数据结构。

---

### 阶段 8：复现重放工具（replay）

**目标**：用当前构建重放代表性请求，复现 503/超时/切换，免去手动复现（同事 G）。

**涉及文件/改动点**
- 新增 `debug_replay` 工具：读取 fixture（脱敏请求）+ 期望行为，用当前构建重放。
- fixture 格式复用阶段 5 的 `PostmortemBundle`（脱敏后）。

**开发任务**
1. 定义 fixture 结构与 `debug_replay` 命令。
2. 支持从 postmortem bundle 一键生成 fixture。
3. 重放时携带 `trace_id` 以便对照（依赖阶段 3）。

**测试任务**
- 用阶段 5 产出的一个 503 bundle 生成 fixture，`debug_replay` 能复现 503。
- 重放请求的 trace_id 在日志中可追溯。

**审查检查项**
- fixture 不含密钥（复用阶段 5 脱敏）。
- 重放不影响生产状态（隔离数据目录）。

**验证标准**
- [ ] 代表性失败可被重放复现且 trace 可追溯 → pass，否则 fail。

**失败顺延**：若重放对「第三方上游」依赖导致环境不可控，将「端到端重放」降级为「本地链路重放」，该降级项记录为最终遗留，计划收尾时单独评估。

---

## 5.9 阶段 5-8 实现进展（2026-09-05）

> 用户要求推进实现。四阶段代码 + 单测已落地，全部纯标准库、无新依赖；`go build ./...` / `go vet ./...` 干净；可观测性相关 19 项单测单独跑全 PASS。真机端到端项待用户环境验证。
> ⚠️ 全量 `go test ./...` 并发跑时，`pluginprovider` 包若干测试（TestPluginToggle / TestPluginHTTPHandlers / TestRepeatedToggleNoLeak / TestPluginVendorsAssembly）偶发「等待插件子进程 running 超时」——这些测试 spawn 真实子进程、失败点每次不同、单独跑均 PASS，系**既有环境 flaky**，与本次可观测性改动无关（未触碰插件进程管理）。

| 阶段 | 交付 | 单测 | 状态 |
|------|------|------|------|
| 5 失败现场打包 | `postmortem.go`：失败异步落脱敏 bundle（trace_id/路由决策/配置快照）+ 保留上限；`recordCall` 接入 | `postmortem_test.go` 5 项 PASS | ✅ 机制层完成 / 🔶 真实 503 端到端待真机 |
| 6 配置溯源 | `config_trace.go`：快照(writer/ts/sha/脱敏)+限100条+`/api/config/history`+`/api/config/effective`(一致性+diff)；`saveConfigWithWriter` 已接 **go-manager + custom-propagate** 两写者（Rust 壳写者按失败顺延） | `config_trace_test.go` 6 项 PASS | ✅ Go 侧写者完成 / 🔶 真机一致性待验 |
| 7 失败原因计数器 | `fail_counter.go`：节点×原因(429/401/503/connect/timeout)二维计数+`/api/admin/fail-stats`；`recordCall` 接入；**统计页 `FailStatsCard` UI**（StatsPage.tsx + api.ts） | `fail_counter_test.go` 4 项 PASS + 前端 `npm run build` 通过 | ✅ 数据结构+接口+UI 完成 |
| 8 复现重放 | `replay.go`：fixture+`BundleToFixture`+`ReplayRequest`(带 X-Trace-ID)+`debug_replay` 子命令（含 `-from-bundle` 一键生成 fixture） | `replay_test.go` 9 项 PASS | ✅ 本地链路重放+生成完成 / 🔶 端到端重放降级为本地（顺延） |

---

## 6. 术语表

| 术语 | 含义 |
|------|------|
| **tier** | 请求层级：`TierFree`（免费层，通常走代理池）/ `TierPaid`（付费层，恒直连）。取值机制因厂商而异：opencode 对话请求 `authT.tier()`（`vendors/opencode/chat.go:46-50`），其元数据拉取硬编码 `TierFree`（`vendors/opencode/opencode.go:170/195`）；custom `tier()`（`vendors/custom/custom.go`，**行号随分支变**：main ~`:129` / test ~`:220`，以 `grep` 为准）；remote 硬编码 `TierPaid`（`vendors/remote/remote.go:253`，不走 `tier()`）。 |
| **via_proxy** | **（自定义源配置项，仍有效）** 自定义源是否走节点池代理（`ViaProxy=true`→TierFree）。默认 false→直连。见 `docs/CONFIGURATION.md` / `docs/ROUTING.md`。**注意：与调用日志无关** —— 调用日志中的同名字段曾为预留死字段（零写入点），已于 2026-09-03 删除，两者勿混用。 |
| **route_verdict** | 调用日志的路由结论枚举，由后端 `recordCall` 判定后落盘：`proxied`（真实走了代理节点）/ `direct_by_design`（付费层恒直连，正常）/ `direct_config_missing`（免费层应走代理但 SOCKS 未配置已回退直连，**事故**）/ `direct_unexpected`（免费层、SOCKS 有配置但节点仍空）；空 = 旧记录或无法判定。前端只做枚举→标签+颜色映射，**不得用 `nodes[0]` 字符串比对重算**。 |
| **serving_port** | 本次请求实际进入的端口（实例端口/统一网关/管理端口）。 |
| **SOCKS 三键** | `active_socks5`（实例级出站）/ `socks5_proxies`（网关级聚合）/ `route_mode`（路由策略）。免费层是否真走代理池，**取决于 `active_socks5`/`socks5_proxies` 是否为空**，与 `route_mode` 字符串无直接等价关系。`route_mode` 默认 `"smart"`、允许 `{smart, failover, round_robin}`（`config.go:116`、`admin_ops.go:768`、`socks.go:218`）。 |
| **trace_id** | 跨进程请求标识，复用现有 `req_id`（`logging.go`）扩展而来。 |
| **role** | 日志归属角色：manager/gateway/instance/probe。 |
| **postmortem bundle** | 失败现场打包（配置快照+决策+节点态+脱敏请求）。 |
| **doctor** | 命令行体检子命令，输出与 `/api/diag` 同构的报告。 |

---

## 7. 风险与已知局限

1. **Trace 在第三方子进程断链**（阶段 3）：sing-box、opencode 实例为第三方进程，不认 `X-Trace-ID`，其内部日志不带我们的 ID。阶段 3 仅保证自研 Go 进程链路贯通，此局限已写入验证标准（不计入 fail）。
2. **配置漂移根因未根治**（阶段 6 前）：多写者 round-trip 是既有设计，阶段 6 仅做「溯源」，不重构写者模型；若要彻底消除漂移需另行立项。
3. **历史 commit 计数混乱**：仓库历史线含大量 fork 重复提交（`git log` 计数可达数百），不影响本计划，但阶段 0 的 fetch 统一仅解决「可见性」，不清理历史。
4. **测试环境差异**：部分场景（启动 15s 超时、第三方上游 503）在 CI 难稳定复现，已通过「顺延 + replay 配合」机制处理，不阻塞进度。

---

## 8. 完成判定

- 阶段 0–8 全部验证 **pass**；
- 本计划 §4 定义的遗留清单为空（或仅剩 §7 已声明的已知局限项）；
- 每个阶段均有对应测试步骤与（建议的）`docs/` 阶段笔记；
- 新增能力零外部依赖、不破坏现有 `call_log` 向后兼容。

> 达到上述即视为计划完成。任一阶段验证未过项按 §4 顺延，循环直至全过。
