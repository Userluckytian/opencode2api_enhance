# 阶段计划：插件统一模板（PLT 系列）——阶段一（骨架）+ 阶段二（pluginsdk v0）

> 依据：`docs/plugin-template-analysis-2026-11-07/00-05`（七需求分析已归并，接口裁决 C1-C10 已定稿）。
> 本文单文件承载：系列总览 + 本轮两阶段详排 + 阶段三/四粗排（plan-layering 单文件变体；阶段三开工前必须贴实际产物重出计划）。
> 已关闭的开放问题：00-overview §5-C17（共享包分发形态）→ **独立仓库**，用户 2026-11-07 拍板。

## 0. 元信息

| 项 | 值 |
|----|----|
| Goal | 把七项需求（过CF/临时邮箱/负载切号/多IP并行注册/出口代理/定时任务/额度展示）落成可复用的 `pluginsdk` Go 共享包 + 插件骨架模板 |
| 落点 | **新仓库 `D:\AI_Projects\plugin-template`**（module 名 `pluginsdk`；日后上远程改路径一次 sed 即可） |
| 运行模式 | **目标模式**（用户 2026-11-07 选定）：连续推进「计划→实施→自动验证→自主验收→下放」，待人工项登记不阻塞，红线仍暂停问人 |
| 实施档位 | **全能**（默认）：开发+测试+代码审查；**子代理启用**——审查/验收由独立子代理担任（用户未答档位，按框架默认，可随时降级） |
| 技术栈 | Go ≥1.22（对齐宿主 go.mod）；依赖纪律：标准库优先，仅登记 `gorilla/websocket`（challenge CDP 通道，qw2api 同源先例，01:261-267）；新增任何三方依赖须在审查中单独过 |
| 本轮范围 | 阶段一 + 阶段二；阶段三/四只粗排 |
| 提交规范 | `<gitmoji><type>(PLT1|PLT2): <中文描述>`，默认不 push |

## 1. 全局约束 / 红线（冲突时以本节为准）

1. **密钥不进 git**：模板只含 `config.example.json` 形态；任何真实 token/cookie/密码不得进入新仓库。
2. **环境红线**（`docs/TESTING.md`）：正式版（40000 槽位段 / Program Files）勿动；宿主联调一律走 portable 48200 隔离形态；禁止 kill 非自己启动的进程。
3. **不改存量**：13 个存量插件的 provider.json 与运行实例一律不碰（迁移属阶段三，需用户停机窗口）。
4. **manifest 写入纪律**：JSON/provider.json 一律原子写 + 无 BOM（2026-11-05 BOM 事故教训，OPEN.md 有案）。
5. **包依赖方向**（00-overview §1.1，硬约束）：`register → {challenge, tempmail, egress}`；`routing → quota(只读视图)`；`schedule` 无包依赖；`challenge / tempmail 不 import egress`（出口以 string 流动，裁决 C1）。
6. **接口以 00-overview §1.2 为准**，十项裁决 C1-C10 全部落地；实现若需偏离，先改裁决再改码。
7. 默认不 `git push`；破坏性操作需人类授权。

## 2. MASTER 目标（目标模式终止判据）

| # | 判据 | 完成标志 |
|---|------|---------|
| M1 | 仓库骨架 + 生成器可用 | `cmd/new-plugin` 从模板生成插件目录；生成的 example-plugin 可编译 |
| M2 | pluginsdk 7 包 v0 落地 | routing/schedule/quota/tempmail/challenge/egress/register 全部实现，`go vet` + `go test ./...` 全绿 + 关键包 `-race` |
| M3 | 面板骨架 | console-mockup 13 视图导航 + 11 主题 tokens 进 `go:embed` 单文件 HTML；`/admin/*` API 占位齐 |
| M4 | example-plugin 过契约自测 | stdout ready 行、`/v1/models`、`/v1/chat/completions` echo 三项脚本自测通过 |
| M5 | 审查+验收闭环 | 每包独立子代理审查完成；验收表全部 ✅ 或缺陷显式带入下阶段 |

**待人工清单（登记不阻塞，目标模式结束时交用户）**：① example-plugin 放入宿主 portable 48200 `providers/` 验证聚合目录注册 + 面板 🌐 打开 13 视图；② BrowserSolver 真实过 CF 盾一次（桌面 Chrome）；③ 任一真实临时邮箱源在线收码一次；④ 真实 clash 实例外控一次。

## 3. 阶段地图

```
PLT-1 模板骨架（本轮，详） → PLT-2 pluginsdk v0（本轮，详）
  → PLT-3 双试点迁移（粗：runbiosproxy 或 skywork 全链路型 + buddyhub 或 xhx 调度型）
  → PLT-4 全量推广 + 13 视图接真数据/N/A（粗；判级依据 05 矩阵：必128/可15/N/A26）
```

## 4. 文件与契约

### 4.1 仓库结构

```text
plugin-template/
├── go.mod                      # module pluginsdk
├── challenge/  tempmail/  routing/  schedule/  egress/  quota/  register/
├── scaffold/                   # 插件骨架模板（main.go.tmpl、provider.json.tmpl、panel/ 单文件 HTML 模板）
├── cmd/new-plugin/             # 骨架生成器：读 schema → 渲染插件目录
├── cmd/example-plugin/         # 最小示例插件（--provider-serve 契约自测载体）
└── scripts/                    # 契约自测脚本（PowerShell，-WindowStyle Hidden）
```

### 4.2 provider.json 私有段 schema（阶段一定稿）

统一段：`tempmail{preferred,random_mode,sources[]}` / `routing{strategy,weighted,failover_rounds,first_byte_gate_sec,refresh_request_ids,cooldowns,sticky}` / `schedule{job,slots,jitter_sec,daily_target,daily_max_attempts,min_gap_sec,grace_sec,trigger}` / `egress{mode,global_list,clash{api,secret,group,auto_switch,watch,ensure},pinned,proxy_url,follow_system_proxy}` / `register{concurrency,min_interval_ms,per_task_timeout_sec,threads_cap}` / `challenge{order,browser,api,assist}` / `quota{refresh_interval_sec,low_balance_threshold}`。
键名统一：`clash_api/clash_secret`（C2）、`round_robin`（C3）；双闸归 `schedule`（C4，register 不设 daily_*）。

### 4.3 --provider-serve 契约（对齐宿主 core/manager/pluginprovider）

argv `<exe> --provider-serve --port 0`；stdout 逐行 JSON：ready 行（含 http 端点与一次性令牌）/ need_config / fatal；env `PROVIDER_DIR/PROVIDER_CONFIG/PLUGIN_AUTH_TOKEN`；HTTP：`/v1/models`、`/v1/chat/completions`（echo 即可）、`/admin/*` 面板与 API。以宿主 `spawnAndReadReady`（manager.go:867）实读为准，实现前先读该函数。

## 5. 阶段一 Task（PLT1）

| Task | 内容 | Files | 测法 |
|------|------|-------|------|
| T1 仓库初始化 | git init、go.mod、目录骨架、README、.gitignore（config.json 等）、LICENSE 占位 | 仓库根 | `go build ./...` |
| T2 schema 定稿 + 三个开放问题收口 | 4.2 全段 schema 结构体（各包 config.go）；同时定稿 00-overview §5-C2（Refresher 并发预算+进度回报：成功/失败/待处理/耗时四计数，buddyhub 并发 3 + mmx syncRunning 先例）、C3（面板资产复用：设计系统 tokens+组件 CSS 抽 `scaffold/panel/assets/`，构建期拼入单文件 HTML）、C4（站点定制层：challenge 盾位清单/tempmail 提取流派归插件 provider.json，模板只定义 schema 槽位） | 各包 config.go、scaffold/provider.json.tmpl | schema 渲染单测 |
| T3 面板骨架 | 从 console-mockup 抽取：21 令牌×11 主题 CSS、13 视图导航骨架、组件层；`go:embed` 单文件 HTML；`/admin/` 路由占位（视图数据端点空实现+空态文案） | scaffold/panel/ | 生成物 HTML 本地打开 13 视图可切换（脚本 grep 断言 + 待人工目检） |
| T4 example-plugin + 生成器 | `cmd/new-plugin` 渲染；example-plugin 实现 4.3 契约：ready 行/models/echo chat/面板托管 | cmd/ 两处、scripts/ | `scripts/selftest.ps1`：启动子进程→读 ready→GET /v1/models→POST echo→退出码判定 |

**阶段一验收**：M1/M3/M4 达成；生成的插件目录结构完整、provider.json 无 BOM。

## 6. 阶段二 Task（PLT2，按依赖与风险排序）

| Task | 包 | 要点（接口签名见 00-overview §1.2，不复制） | 测试 |
|------|----|------|------|
| T5 | routing | ErrClass 八类/Action 四种/Classifier+ErrRules/7 策略/StickySession/Failover.Dispatch（attemptBudget、excluded、relaxed 豁免轮、firstByteGate 20s、refreshRequestIDs）/默认错误→动作表；`P` 内嵌 quota 只读视图（C5） | 表驱动：默认映射表全格、各策略选取序、failover 预算/豁免轮 |
| T6 | schedule | Spec（Slots/Interval/Window 跨午夜/JitterSec **确定性纯函数**/双闸/MinGap/Grace）/Tracker（(job,天,档) 去重、落盘、启动清旧态）/Scheduler（nextWake+wake 热重载+迟到宽限+档内并行）；clock 注入 | 确定性断言：同输入同唤醒序列；(天,档) 去重；双闸；跨午夜窗口 |
| T7 | quota | AccountQuota/Unit/Source/Confidence/QuotaBucket/QuotaWindow + Refresher 接口（并发预算+四计数进度回报，T2 定稿稿） | 模型序列化/0与未知表达/分桶与窗口 |
| T8 | tempmail | Source 正典接口（C8 适配器）/Registry+PickMailboxMode/RandomSource/WaitMessage·WaitCode·WaitLink（自适应退避）/ExtractCode 四流派+兜底链（候选制，C9）/DomainPolicy（RejectPlus/RecreateUntil）/HTTPSpec 声明式/redact.go 凭据脱敏 + **10 providers**（02 §7 决策表：cloudflare/temptf/maliapi/mailtm/g22do/emailmux/guerrilla/beeinbox/freecustom/mailsac；beeinbox P2 可最后）。**maliapi 勘误（2026-11-07 实测）**：API-key 通道可用（`POST /v1/accounts` + `X-API-Key` → 201，地址级 JWT，`source:"api"`），优先于 Web 会话通道；用户已提供 key，只存运行时配置绝不入 git | httptest 假源逐 provider 契约测试；提取流派黄金样例；换源/池空续走 |
| T9 | challenge | Solver/Kind（v1 clearance=ErrUnsupported）/哨兵错误（绝不空 token）/BrowserSolver 骨架（go:embed turnstilePatch、FindChrome、探针三口径、**Headless 默认 false**）/APISolver 双协议（yescaptcha/2captcha/capmonster/anticaptcha）/AssistSolver/StubSolver/ChainSolver | Stub/Chain/配置/探针单测；**真实过盾=待人工③** |
| T10 | egress | Lease（nil=直连）/四 Provider（GlobalList/ClashSelector/ClashPinned/ExclusiveBorrow）/ClashClient 最小外控面（GET /proxies、PUT selector、/delay 非破坏探测；永不改全局 mode）/账号→端口绑定 JSON 落盘；auto_switch 默认 false | httptest mock clash：acquire/release、探针、切换幂等、绑定持久化 |
| T11 | register | Job（Run(ctx, Lease)）/Runner.RunBatch（worker-pool、CapConcurrency 压并发、全局 pacing、结果按索引回填）/Resume 台账幂等补跑（ledger.json）/threshold+window 两种触发 | 并发与台账：假 Job 统计并发上限/补跑不重复/退避 |
| T12 | 组合冒烟 | register 编排 challenge(Stub)+tempmail(HTTPSpec 假源)+egress(mock) 全链路一次 | 一条组合测试 + `-race` |

**阶段二验收**：M2 达成；C1-C10 裁决落地对照表逐项打勾；零未登记三方依赖。

## 7. 验证分工（两层）

- 自动化（执行方全责）：`go vet ./...`、`go test -count=1 ./...`、关键包 `-race`、selftest.ps1、生成物 HTML 断言。
- 待人工（登记，不阻塞）：MASTER 目标节①-④项，由用户/真机执行后回填验收表。

## 8. 代码审查（独立子代理）

每包完成后审查：风格/测试完整性（关键路径断言）/依赖登记/与 00-overview 接口一致性/安全（redact、token 不落日志）。产出 通过/有条件/不通过；问题进验收表，不通过项下放（§4.1 下放机制）。阶段级收口审查覆盖全提交链。

## 9. 验收标准总表

| # | 通过条件 | 层 |
|---|---------|---|
| A1 | `go build ./...` 通过 | 自动 |
| A2 | `go vet ./...` + `go test -count=1 ./...` 全绿，routing/schedule/register `-race` 通过 | 自动 |
| A3 | selftest.ps1 三项契约自测通过 | 自动 |
| A4 | 生成插件 provider.json 无 BOM、schema 键名与 4.2 一致（C2/C3/C4 落地） | 自动 |
| A5 | ExtractCode 黄金样例全过（xAI 三段码/Clerk/CSS-aware/通用兜底） | 自动 |
| A6 | schedule jitter 确定性 + (天,档) 去重 + 双闸测试通过 | 自动 |
| A7 | tempmail 10 provider httptest 契约全过（beeinbox 可 P2 延后并登记） | 自动 |
| A8 | egress mock clash 四 Provider 行为全过；auto_switch 默认 false | 自动 |
| A9 | 面板 13 视图可切换 + 11 主题 tokens 齐全（grep 断言 + 待人工目检） | 半自动 |
| A10 | C1-C10 裁决落地对照表全勾 | 审查 |
| A11 | 每包独立审查结论 ≥ 有条件通过，问题已登记 | 审查 |
| A12 | 待人工①-④已登记并给出操作指引 | 待人工 |

> **验收结论（2026-11-07 独立验收，复跑全部自动化门）**：A1-A12 全 ✅——9 包 `go test -race` 全绿、selftest PASS（含 13/13/11 计数断言）、A4 实跑生成器验无 BOM+键名一致、A10 抽查 C1/C3/C4 落地、红线三项（提交规范/凭据扫描/三方依赖）全过。三组独立代码审查「有条件通过」的 13 处发现已全部修复入库（60c5299）。阶段一+二达成 M1-M5；待人工①-④见仓库 `docs/handoff-manual.md`。

## 10. 风险与降级

| 风险 | 降级 |
|------|------|
| BrowserSolver CDP 复杂度超预期（A1/A3/A4，01 文档已登记失效证据） | v0 交付接口+APISolver+Chain+Stub，BrowserSolver 降为骨架（编译通过+探针单测），真实通道移阶段三试点 |
| 10 providers 体量超预算 | 按 02 §7 优先级交付（P0 cloudflare/guerrilla/mailtm/g22do → P1 其余 → P2 beeinbox），延后项登记 |
| ClashClient 与真机行为差异（B4/B5 证据缺口） | mock 全过后真机验证列待人工④；不阻塞 |
|  lobsterai/lobsterhub/loomy/vibex/xhx 五家调度细节未入 03 样本（B1） | 不影响本轮（模板不含站点逻辑）；阶段三迁移对应插件前补查 |
| MV2 补丁随 Chrome 退场失效 | challenge 文档化风险，BrowserSolver 内置探针报清晰错误 |

## 11. 阶段三/四粗排（开工前必须贴实际产物重出计划）

- **PLT-3 双试点**：全链路型 runbiosproxy 或 skywork（覆盖 challenge+tempmail+egress+register+quota）；调度型 buddyhub 或 xhx（routing+schedule+quota）。验收=旧功能逐项回归 + 账号池/积分视图出真数据。
- **PLT-4 全量**：13 插件迁移 + 视图按 05 矩阵接真数据/降级展示/显式 N/A；provider.json 键名迁移（C2/C3）。

## 12. 交接提示词（零上下文，供任意执行方）

> 你在 `D:\AI_Projects\plugin-template`（新仓库，module=pluginsdk）执行 PLT 系列阶段一+二。必读：本计划全文 + `opencode2api_enhance/docs/plugin-template-analysis-2026-11-07/00-overview.md`（§1.2 接口签名与 C1-C10 裁决为准）+ 宿主 `core/manager/pluginprovider/manager.go:867`（--provider-serve 契约）。做：T1→T12 顺序执行，每 Task 一条 `✨feat(PLTx):` 提交。不做：碰 13 个存量插件与正式版环境（40000/Program Files 红线）；push 远程；引入未登记三方依赖；实现偏离 00-overview 接口（需先改裁决）。工作方式：开工先读红线（§1），每包先测后审（§8 独立审查），证据优先（命令+输出），待人工项登记不阻塞（§2）。交卷：分支+commit 链+验收表自评（§9 A1-A12）+测试输出+残留风险。回复语言中文。
