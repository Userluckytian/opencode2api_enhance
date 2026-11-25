# 阶段计划：PLT-3 双试点迁移（runbiosproxy 全链路型 + xhx 调度额度型）

> 依据：阶段一+二已验收（`2026-11-07-plugin-template.md` §9 结论）；现状勘察报告 2026-11-07（4 候选五域 files:line 级事实，本计划引用处标注「勘察 §n」）。**实事求是**：本计划所有映射与裁剪均基于勘察实读，开工后 T0 只做「核实 6 项遗留」，不重开探索。

## 0. 元信息

| 项 | 值 |
|----|----|
| Goal | 把 runbiosproxy 与 xhx 两个存量插件迁到 plugin-template 模板+pluginsdk 上，验证共享包在真实插件里的成立性 |
| 试点选型 | **runbiosproxy**（全链路型：注册/CF/邮箱/出口/额度五域全有，勘察 §一）+ **xhx**（调度额度型：三域 N/A 显式裁剪的对照样本，勘察 §三）。备选：skywork（体量最大 15 包+双键族未收敛，留 PLT-4）、buddyhub（嫁接 Wails 仓库+双树张力，前置「双树定源」后可替换 xhx） |
| 运行模式 | 交互式（默认）；可随时口令切目标模式 |
| 档位 | 全能 + 子代理（沿用阶段一+二工作方式；子代理用 worker/GLM-5.3-Flash） |
| 工作分支 | 各试点源码树内 `feat/plt3-migration`；pluginsdk 侧改动走 `plugin-template` 的 `feat/plt-phase-1-2` 延续分支 |
| 红线 | 沿用 PLT 计划 §1 全部红线；新增：**不动两个存量插件的现网运行实例与已部署 exe**，迁移全程在源码树分支进行，部署需用户停机窗口单独授权 |

## 1. 阶段地图

```
T0 核实勘察遗留（6 项）→ T1 schema 扩键裁决（3 类映射缺口）
  → PLT-3a runbiosproxy 迁移（T2.1-T2.6）
  → PLT-3b xhx 迁移（T3.1-T3.4，可与 3a 尾段并行）
  → T4 双试点回归验收 → PLT-4 全量推广（另立计划）
```

## 2. T0 核实勘察遗留（半天内，产出并入计划附录）

| # | 待核实项 | 手段 |
|---|---------|------|
| 1 | runbiosproxy 源码根 `D:\AI_Projects\BIOS-all\runbios_proxy` 系推定 | 部署 exe 字符串/构建脚本反查证实 |
| 2 | runbios 余额端点具体 path（勘察未命中）+ eventlog 有无 | grep upstream/admin 源码 |
| 3 | buddyhub 部署树 vs phase5 树 diff 结论（回退说法与 slot_hours 仍在的矛盾） | 两树 `diff -r` 摘要 |
| 4 | buddyhub `daily_limit` 键真实语义（config.go:24） | 实读 upstream 使用点 |
| 5 | skywork `clash_*` 与 `egress_clash_*` 双键族生效优先级 | 实读 privateconfig.go 装载序 |
| 6 | xhx requestlog 是否落盘 | 实读 requestlog.go |

## 3. T1 schema 扩键裁决（改 plugin-template/docs/schema.md + provider.json.tmpl，同步 pluginsdk config 结构）

| # | 缺口 | 裁决（勘察依据） |
|---|------|----------------|
| 1 | xhx `switch_threshold`+`switch_floor_points` 双阈值、skywork `switch_threshold` | schema 增加 **routing 段插件私有扩展约定**：`routing.extras`（map[string]any 透传）承载站点特有调度阈值——不为核心包造键（对应裁决 C 系列精神：核心收敛、站点私有下沉） |
| 2 | strategy 别名 balanced/round-robin/round | 归一 `round_robin`（C3 已有别名机制，把 balanced 加进别名表并登记映射）；xhx `failover` bool→`failover_rounds` int（≥2）改写规则写进迁移映射表 |
| 3 | 单位与形态改写 | `refresh_interval_seconds→refresh_interval_sec`、`slot_jitter_min→jitter_sec`（×60）——统一进 schema.md **新 §「存量键名迁移映射表」**（PLT-4 全量推广复用） |
| 4 | 能力 N/A 显式化 | 模板生成器支持 `--without=challenge,tempmail,egress` 裁剪：不生成空段不注入空包（防「空包强注入」，勘察 §三-7③） |

## 4. PLT-3a：runbiosproxy 迁移（T2.1-T2.6）

| Task | 内容 | Files（勘察 §一） | 验证 |
|------|------|------------------|------|
| T2.1 配置收敛 | provider.json 7 键 + data/settings.json + billing-settings.json 三处 → provider.json 私有段（schema 七段）单源装载；billing 日限入插件私有 | settings.go:13-59、provider.go:15-19 | 装载单测（旧 settings.json 能迁移读入） |
| T2.2 exe 形态定案 | provider-shim 双 exe → **合并为单 exe**（模板形态），README:280-282 构建链改写 | plugin-dist/build/provider-shim | 构建产物由宿主 spawnAndReadReady 拉起成功（selftest 思路） |
| T2.3 routing 接入 | scheduler.go/pool.go/dispatcher.go（选号/402 exhausted/429 冷却/粘性三判据）→ `pluginsdk/routing`（ErrRules 逐码对齐现行为：402→Exhaust、401/403→Cool、429→Retry-After）；存量 45 测试语义保持 | scheduler.go:27-137、pool.go:43-137、dispatcher.go:240,269 | 旧测试改造后全绿（语义等价断言） |
| T2.4 egress 接入 | clash.go ClashClient+随机节点 → `pluginsdk/egress`（ClashSelector+注册每尝试轮换=RotateNext 语义映射） | clash.go:29-126 | 假 clash 单测 + 行为对齐表 |
| T2.5 register+challenge+tempmail 接入 | autoreg/ 25 文件编排 → `pluginsdk/register` Runner+台账；turnstile token.go/captcha.go → `pluginsdk/challenge`（APISolver 双协议，token one-shot 语义用 chain 闸保证）；邮箱源 emailmux/22do/yyds → `pluginsdk/tempmail`（emailmux/22do 已有 provider；yyds=maliapi 已有） | internal/autoreg/、sources.go:15、token.go | 注册全链路 httptest 冒烟（假打码+假邮箱+假 clash） |
| T2.6 quota+面板 | RunBalanceWatcher → `pluginsdk/quota` Coordinator（balance_refresh_seconds→refresh_interval_sec）；web/assets/index.html 46KB 单文件面板保留但挂 13 视图骨架主题 tokens；**eventlog 按 T0-#2 结论补齐或登记** | admin.go:775-784 | 账号池/余额视图出真数据（需真机，列待人工） |

## 5. PLT-3b：xhx 迁移（T3.1-T3.4，与 3a 并行）

| Task | 内容 | Files（勘察 §三） | 验证 |
|------|------|------------------|------|
| T3.1 裁剪生成 | 生成器 `--without=challenge,tempmail,egress` 重刷骨架，xhx 业务键（base_url/nation_code/双阈值）入 provider_private_configs | config.go:50-74 | 生成的 provider.json 与现网键逐键映射表 |
| T3.2 routing 接入 | pool/select.go sticky/balanced 双策略 + 双阈值 + 全员低于 floor 回退 → `pluginsdk/routing`（StickyUntilFail/StickySession + routing.extras 双阈值）；chat.go 请求内 failover maxFailoverAttempts=3 → routing.Failover attemptBudget | select.go:14-51,81、chat.go:115-192、pool/errors.go | 既有 7+internal 全包测试语义保持 |
| T3.3 quota 接入 | settle.go StartRefresher（interval≤0 回落 5min、启动即刷）→ `pluginsdk/quota` Refresher+Coordinator（保留「启动立即刷一次」语义）；估算/结算双口径字段映射 | settle.go:189-215、client.go:35-39 | 额度视图真数据结构单测 |
| T3.4 面板移植 | panel.go:373 `const panelHTML` 2003 行字符串 → go:embed 模板形态，保 `__PANEL_TOKEN__` 运行时替换约定；挂设计系统 tokens | panel.go:35,373 | 面板自测脚本（token 替换+13 视图导航） |

## 6. T4 回归验收（两试点共用口径）

1. **功能回归**：各试点迁移前测试清单逐项在新实现上重跑通过（语义等价，非快照相等）；
2. **契约回归**：新 exe 过 `scripts/selftest.ps1` 同款契约自测（ready/models/chat）；
3. **视图回归**：账号池/积分与余额视图出真数据（真机项，待人工）；
4. **包级回归**：pluginsdk 若因试点需求改动，全仓 `go test -race` 保持全绿，改动回写 schema.md。

## 7. 验收标准总表

| # | 通过条件 | 层 |
|---|---------|---|
| B1 | T0 六项全部有结论并回填本计划附录 | 自动+实读 |
| B2 | T1 三类映射缺口裁决落进 schema.md + 生成器裁剪开关可用 | 自动 |
| B3 | runbiosproxy 新 exe：契约自测过 + 旧 45 测试改造后全绿 | 自动 |
| B4 | runbiosproxy 注册全链路 httptest 冒烟过（假打码/假邮箱/假 clash） | 自动 |
| B5 | xhx 新 exe：契约自测过 + 既有测试全绿 + 面板 token 替换自测过 | 自动 |
| B6 | 两试点 `go vet`/`gofmt` 干净；pluginsdk 全仓 -race 全绿 | 自动 |
| B7 | 每迁移一步独立审查（worker），发现项修复入库 | 审查 |
| B8 | 独立验收：对照本表复跑 + 四段结论 | 验收 |
| B9 | 待人工：两插件真机部署到 portable 48200、账号池/积分视图真数据、真实注册一次（runbios） | 待人工 |

> **验收结论（2026-11-07 独立终验）**：B1-B6/B8 全 ✅，B7/B9 ⚠️（留痕级缺陷：审查 low 项仓内无登记、PLT-3 待人工未回填 handoff）——已收口：发现项登记 `plugin-template/docs/plt3-review-findings.md`（runbios 3 修 1 备案 / xhx 3 待修 1 观察），B9 待人工⑤-⑦ 已增补至 `D:\AI_Projects\plugin-template\docs\handoff-manual.md`，B9→✅。遗留备查：xhx 基线快照提交缺 gitmoji（用户裁量）。**阶段三实现层完成，待人工⑤-⑦ 真机验收。**

## 8. 风险与降级

| 风险 | 降级 |
|------|------|
| runbios autoreg 25 文件语义复杂度超预期 | T2.5 拆两步：先邮箱源+编排上 pluginsdk（不动 turnstile 细节），challenge APISolver 真机验证列待人工 |
| xhx 双阈值 extras 方案与 routing 内核冲突 | 退化为插件侧 Strategy 装饰器（routing 包已有装饰器先例），不动核心 |
| buddyhub 候选替换触发（若 T0-3/4 结论不利） | xhx 单试点先行，buddyhub 迁移移入 PLT-4 |
| skywork 弃选 | 其双键族收敛/switch_threshold 方案沉淀进 schema.md 迁移映射表，PLT-4 再战 |

## 9. 交接提示词（零上下文，给执行方）

> 你在执行 PLT-3 双试点迁移。必读：本计划全文 + `D:\AI_Projects\plugin-template\docs\schema.md` + 勘察报告（宿主仓库 docs/ai-framework/plans/ 下 2026-11-07 勘察产物，各断言带 files:line）+ 两试点源码树。做：T0→T1→T2/T3（可并行）→T4，每 Task 一条 `<gitmoji><type>(PLT3):` 提交。不做：碰已部署 exe 与运行实例（停机窗口另批）；改 pluginsdk 接口语义（需先改裁决）；绕过审查。工作方式：语义等价迁移（旧测试是行为基准，不是快照）；证据优先；审查/验收由独立子代理担任。交卷：分支+commit 链+本计划 §7 验收表自评+测试输出+残留风险。回复语言中文。

## 附录 A：T0 核实结论（2026-11-07 回填，B1 ✅）

| # | 结论 | 对计划的影响 |
|---|------|------------|
| 1 | runbiosproxy 源码根证实=`D:\AI_Projects\BIOS-all\runbios_proxy`（部署两 exe 与源码树 plugin-dist 产物**字节一致** cmp 实证；go.mod module=runbiosproxy） | §4 T2 引用锚点全部有效 |
| 2 | 余额端点：管理面 `POST /admin/accounts/balance`（admin.go:74）→ 上游 `GET /api/billing/wallet`（accountinfo.go:13，balance_dollars 为字符串）；**eventlog 现状无**（全树 0 命中） | T2.6 落定为**补齐 eventlog**（参照 xhx requestlog.go 的 events.jsonl+轮转实现） |
| 3 | buddyhub 部署树**不是回退，是更旧**（Oct vs phase5 Nov）；两树都有 slot/tracker/dayclaim，DoneInSlot 升级仅 phase5 有 | §8「buddyhub 候选替换」不触发；PLT-4 若迁 buddyhub 双树定源取 phase5 |
| 4 | buddyhub `daily_limit`=每账号每日调用上限：0=不限（默认）、n=当日达限退出候选次日恢复、负数忽略（config.go:24/pool.go:281-308/pick.go:74 实证） | schema.md §12 该行已改为「不映射（插件私有/routing.extras）」——它是账号级选号限流，非任务级双闸 |
| 5 | skywork `clash_*` 与 `egress_clash_*` **分属两个独立子系统**（前者=出站链路守护+面板，后者=按号出线），无覆盖无回退（privateconfig.go:43-69,142-178 实证） | schema 迁移映射**不得当别名合并**；PLT-4 迁 skywork 时分别映射两族 |
| 6 | xhx requestlog **落盘**：按天 JSONL+10MB 轮转×5+usage.json 原子写+events.jsonl 0600（requestlog.go:19-51,76-115,181-229,270-300） | T3 面板/日志无缺口；其 events.jsonl 实现作 T2.6 参照 |

> 你在执行 PLT-3 双试点迁移。必读：本计划全文 + `D:\AI_Projects\plugin-template\docs\schema.md` + 勘察报告（宿主仓库 docs/ai-framework/plans/ 下 2026-11-07 勘察产物，各断言带 files:line）+ 两试点源码树。做：T0→T1→T2/T3（可并行）→T4，每 Task 一条 `<gitmoji><type>(PLT3):` 提交。不做：碰已部署 exe 与运行实例（停机窗口另批）；改 pluginsdk 接口语义（需先改裁决）；绕过审查。工作方式：语义等价迁移（旧测试是行为基准，不是快照）；证据优先；审查/验收由独立子代理担任。交卷：分支+commit 链+本计划 §7 验收表自评+测试输出+残留风险。回复语言中文。
