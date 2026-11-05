# 插件整改阶段计划（2026-11-04）

> 状态：P1/P3/P4/P5 副本通过；9 插件契约冒烟通过（loomy 需真实 session）；P2 待上线
> 依据：`docs/plugin-audit-2026-11-04.md`、`docs/PLUGIN-CAPABILITIES.md`
> 上线操作：`docs/ai-framework/plans/2026-11-04-deploy-runbook.md`
> 实施：`qw2api/qwork-auto`；审查：`mmxproxy/MiniMax-M3.1-Flash`；验收：主代理（命令/证据）
> 纪律：只改副本/worktree；线上替换等停机窗口；RunBios 不动。

---

## 1. 目标

1. 数据合规：账号/额度/日志/事件落各自插件 `data/`。
2. 显示正确：`context_window` / `max_output_tokens` 真实或明确标"上游未提供"。
3. 关键功能：QwenWork 真实模型+对话；MiniMax 持久化；Grok 删除复活/额度/轮询。
4. 事件日志：注册/签到/切号/额度可查。
5. UI 统一：白底+抹茶/奶酪（无粉），RunBios 基准；Skywork 纳入；MiniMax 彻底换。
6. 可回滚：每插件独立上线/回滚。

**非目标**：不改宿主契约；不做公共 autoreg 包；不做严格 RR（需改 grok2api）；RunBios 不改。

---

## 2. 已确认决策

| # | 事项 | 决策 |
|---|---|---|
| 1 | LobsterHub 数据 | 合规 `<插件>/data/`；`%APPDATA%` 一次性迁移后与桌面版分家 |
| 2 | Flonno/RunBios profile | 优先临时 profile；不迁移 1GB；变差则保持现状；禁止复制 |
| 3 | Skywork/MiniMax | Skywork 纳入 UI；MiniMax 彻底换 |
| 4 | Grok 轮询 | A：插件侧"轮询友好"+可观测；严格 RR 另立项 |
| 5 | 动效 | A：QwenWork 先做 5 个低风险效果 |
| 6 | 实施子代理 | qw2api 实施 + MiniMax 审查 + 主代理验收 |
| 7 | MiniMax 1M | 支持可选档；`-1M` 别名，默认 512K |
| 8 | 上下文/输出 | LobsterAI 加键；Grok 需改 grok2api；LobsterHub 先验证；无 output 的标"上游未提供" |

---

## 3. 通用工作流

```
副本/worktree → 实施(qw2api) → 审查(MiniMax) → 构建/测试(主代理)
→ 金丝雀 <id>-dev → 验收 → 停机窗口上线 → 回滚(.bak)
```

硬规则：不在 `Program Files\...\providers\` 改源码/覆盖运行中 exe；每 Phase 独立目录；只改白名单文件；每 Phase 更新 issue-log。

---

## 4. Phase 总表

| Phase | 内容 | 人日 | 线上影响 | 状态 |
|---|---|---:|---|---|
| P0 | 基线/副本/构建/验收脚本 | 0.5 | 无 | 部分完成 |
| P1 | MiniMax 1M 别名 + LobsterAI context_window | 2 | 无（副本） | ✅ 副本通过 |
| P2 | 数据合规：LobsterAI/LobsterHub/MiniMax/profile | 5~6 | 停机 1~5min/插件 | 代码/脚本完成，待迁移 |
| P3 | QwenWork 真实模型/对话 + MiniMax 持久化 | 4 | 替换 exe | 待实施 |
| P4 | Grok 删除/额度/轮询 + 事件日志 | 9 | 替换 exe + grok2api | 待实施 |
| P5 | UI 统一 + 动效 | 15 | 逐插件替换 | 待实施 |

总计约 32~36 人日（不含 P6 可选）。

---

## 5. P1（已完成）

**MiniMax**：`handlers.go` 读 `context_window_options`；基础 512K；新增 `MiniMax-M3-1M` / `MiniMax-M3.1-Flash-1M`（1M）；`proxy.go` 剥 `-1M` + 传 `context_limit:1000000`，非 1M 不传。
**LobsterAI**：`models()` 双写 `context_window`。

副本：`D:\AI_Projects\_plugin-work\phase1\{mmxproxy,lobsterai2api}`
验证：mmx `build/vet/test` 通过；lobster `build/vet` 通过；`TestSuspendedUpstreamSurfacesInStatus` 为基线失败；原始目录未动。
审查处理：删死代码、去跨 provider 去重、`-1M` 白名单、hints 透出、补 3 个单测。
遗留：`context_window_options` 强类型对字符串数字不容错；空号池 fallback 无 caps。

---

## 6. P2（代码/脚本完成，待迁移）

### P2-A LobsterAI
- 默认 `<插件>/auths` 已合规；问题是安装态 `provider.json` 写了绝对路径。
- 迁移脚本：`scripts/migrate-plugin-data.ps1` 移动凭证 + 改 `auth_dir="auths"`；要求停插件。
- 脚本硬化（2026-11-04）：支持 `-SkipProcessCheck`（仅沙箱演练）；写 `provider.json` 改为无 BOM UTF-8（Go `encoding/json` 不接受 BOM）。

### P2-B LobsterHub
- 代码：`pluginDataDir(configured, dir)`：未配置时 `<插件>/data`；桌面版默认不变。
- 测试：`plugin_data_dir_test.go`。
- 迁移：脚本把 `%APPDATA%\LobsterHub` 文件移到 `<插件>/data`；与桌面版分家。
- 副本：`D:\AI_Projects\_plugin-work\phase2\lobsterhub`；build/vet/test 通过。
- 注：拷贝时漏了 `browserid/`、`lockfile/`、`build/`，已补；后续建副本不要漏。

### P2-C MiniMax
- 代码：`migrateAccountsToData(dir)`：`data/accounts.json`，旧根文件存在且新文件不存在时复制（保留旧文件）；`provider_serve.go` 使用；独立版不变。
- 测试：`store_migrate_test.go`（旧→新、新文件不覆盖、幂等、日志一行、无 tmp 残留）。
- 副本：`D:\AI_Projects\_plugin-work\phase2\mmxproxy`；build/vet/test 通过。

### P2-D Profile
- Flonno/RunBios：本轮不迁移 1GB；临时 profile 方案待实测通过率；失败保持现状并登记例外。
- **不要**复制 `~/.qw2api/chrome-autoreg`。

### P2 上线检查
- 停插件 → 备份 → 脚本 DryRun 核对 → `-Execute` → 启动 → 面板核对账号/额度。
- 一次只动一个插件；LobsterAI refresh token 只能移动。

---

## 7. P3：QwenWork + MiniMax 持久化

### P3-A QwenWork 真实模型
- `upstream` 解析 `raw_usage.model`；`RequestRecord.RealModel`；`chat.go` 写入；面板日志列/调试 meta/命中卡。
- 验收：`qwork-auto` 一次请求，与 creditprobe 一致。

### P3-B QwenWork 对话
- `debugRelayStream` 错误帧；`relaySSE` 解析 error/usage/finish；命中卡按 request_id；多轮可选。
- 验收：402/429 可见原因；并发不串。

### P3-C MiniMax 持久化
- `Account.Balance` 或 `data/balance-cache.json`；启动回填；同步完成 `loadPool()`。
- 验收：同步 104 号 → 切页/刷新/重启仍在。

### P3 执行记录（2026-11-04）

| 项 | 结果 |
|---|---|
| 副本 | `_plugin-work/phase3/{qw2api,mmxproxy}` |
| QwenWork | `client.go`：`RawUsage`/`RealModel`/model 事件；`server.go`：`realModel`+`RequestRecord.RealModel`；`aggregate.go` 非流式回填；`index.html` 日志 `→真实模型` + 命中卡 |
| QwenWork 测试 | 新增 `real_model_test.go`（raw_usage 双层、raw-only、覆盖优先级）；`build/vet/test` 通过 |
| MiniMax | `Account.Balance` + `load()` 回填 + `balance()` 镜像 + GET balance 落盘 + `balanceLight` 镜像；前端 sync 轮询 status 后 `loadPool()` |
| MiniMax 测试 | `store_balance_test.go`（load 恢复、save/load 往返）；`build/vet/test -count=1` 通过 |
| 审查 | MiniMax 指出 QwenWork 优先级反转/非流式/面板缺失；已修（raw-only 覆盖、aggregate 回填、日志/命中卡、单测） |
| P3-B | `relaySSE` 解析 `error`/`usage`/`finish_reason`，失败显示"上游错误"，meta 显示 finish/token；`relaySSE` 传入 `myIdBefore`，命中卡不再取错记录 |
| 原始目录 | 未动；线上未动 |
| 遗留 | 多轮对话仍未做；真实模型需一次真实请求验证 |

---

## 8. P4：Grok + 事件日志

- P4-A 删除复活：清文件（预览/匹配行）、注册后增量导入、孤儿清理、`deletion-preview`+`linkedDeleteTargets`、`auto_import` 警示。
- P4-B 额度：补 `used/usagePercent/unit/periodEnd/nextProbeAt/source/confidence`；进度条/重置/详情/刷新全部。
- P4-C 轮询：暴露 priority/maxConcurrent/minRemaining；"轮询友好"预设；命中账号/换号原因/分布。
- P4-D 夜间/Clash：`pool_window_*` 生效；`register_clash_*` 接线或明确未实现。
- P4-E 事件日志：`data/events.jsonl` + API + 面板；首批 Grok/QwenWork/BuddyHub/MiniMax/LobsterAI/Hub。

### P4-A 执行记录（2026-11-04）

- 副本：`_plugin-work/phase4/grok`
- `cleanup.go`：`removeCredentialFiles(dir, accounts)` —— 按 email/userID 删 `xai-*.json`；`*.txt` 逐行剔除匹配行（全空删文件）；tmp+rename。
- `panel.go`：delete 前取账号详情，`BatchDelete` 成功后清文件；响应带 `removedFiles`。
- 测试：`cleanup_test.go`（email、userID、无匹配 3 例）；`build/vet/test` 通过。
- 仍待：孤儿文件清理入口、注册后增量导入、`linkedDeleteTargets`、`deletion-preview`（当前先解"删了又回来"主路径）。

### P4-B/C 执行记录（2026-11-04）

- `admin.go` 的 `QuotaInfo` 补齐 `source/confidence/unit/used/usagePercent/periodStart/periodEnd/exhaustedAt/nextProbeAt`。
- `panel.html` 的额度列改为 `quotaCell()`：`used/limit`、百分比、重置时间；没有 quota 时回退主 `quotaWindow`；`unknown` 显示"待查询"。
- `admin.go` 新增 `BatchUpdate`（PATCH `/accounts/batch`，priority/maxConcurrent/minimumRemaining）。
- `panel.go` 新增 `rotation_friendly`：priority=0、maxConcurrent=1、minimumRemaining=0；面板加"轮询友好"按钮。
- 验证：`go build/vet/test` 通过。
- 仍待：严格 RR（需改 grok2api）、按账号内联编辑、命中账号/换号原因可观测、额度详情抽屉。

### P4-D 执行记录（2026-11-04）

- `config.go` 新增 `pool_window_start` / `pool_window_end`。
- `keeper.go`：非手动 `Tick` 只在窗口内补号；`inPoolWindow` 支持同日/跨午夜/空值不限；手动"立即检查"仍可强制。
- `panel.html` 设置页加两个窗口字段；`panel.go` 读写/allowlist/maskedSettings 同步。
- 测试：`keeper_window_test.go`（同日、跨午夜、空值、非法、start==end）通过。
- `register_clash_*`：**仍未接线**（Go 层没有读取；注册 worker 是否支持需另查）。本轮明确标记为"未实现"，不再宣称支持。

### P4-E 执行记录（Grok 首批，2026-11-04）

- `eventlog.go`：`data/events.jsonl` 追加写，10MB 轮转；`logEvent` / `readEvents`。
- 事件接入：`account.delete`、`quota.refresh`、`token.refresh`、`schedule.rotation_friendly`、`register.start/success/fail`。
- 面板：日志抽屉新增 `events` 源（"事件"），显示时间/级别/类型/账号/消息。
- 验证：`go build/vet/test` 通过。
- 仍待：QwenWork/BuddyHub/MiniMax/LobsterAI/Hub 接入；事件筛选/导出 API。

### P4-E 补充（QwenWork，2026-11-04）

- `internal/server/eventlog.go`：`data/events.jsonl` + 10MB 轮转；`logEvent/readEvents`；`eventTypeForPath` 映射管理端点。
- 事件：`account.add/remove/switch/refresh/import`、`autoreg.start/stop`，由 HTTP 中间件按状态记录（>=400 记 error）。
- 面板：请求日志卡新增"事件"按钮，切换显示 `/api/events?lines=300` 时间线。
- 测试：`eventlog_test.go` 追加/读取/字段；`go build` + `go test` 通过。
- 仍待：BuddyHub/MiniMax/LobsterAI/Hub 接入。

### P4-E 补充（MiniMax，2026-11-04）

- `eventlog.go`：`data/events.jsonl` + 10MB 轮转；`logEvent/readEvents`；`GET /v1/events?lines=200`。
- 事件：`account.register`、`account.delete`、`account.signin`、`quota.sync`。
- 面板：左侧新增"07 事件"页，显示时间线（`loadEvents()`）。
- 测试：`eventlog_test.go`（路径/追加/读取/字段）；`go build` + `go test` 通过。
- 仍待：BuddyHub/LobsterAI/Hub 接入；事件筛选/导出。

### P4-E 补充（LobsterAI，2026-11-04）

- `internal/server/eventlog.go`：`data/events.jsonl` + 10MB 轮转；`SetEventsDir` 注入数据目录（空 = 关闭）。
- `cmd/server/main.go`：从 `cfg.StateFile` 反推目录注入；`GET /admin/events?lines=200`。
- 事件：`account.delete`；后续可加 `credential.refresh`/`login`。
- 测试：`eventlog_test.go`（往返 + 空目录关闭）；`go build` + `go test -run TestEventLog` 通过。
- 仍待：面板入口（LobsterAI 面板为独立 Web，后续再加）；BuddyHub/Hub 接入。

### P4-E 补充（BuddyHub，2026-11-04）

- `internal/pluginhub/eventlog.go`：`<dataDir>/events.jsonl` + 10MB 轮转；`DataDir` 为空即关闭。
- API：`GET /api/events?lines=200`（Bearer）。
- 事件：`account.toggle`、`account.revive`、`account.delete`、`task.run`、`credits.refresh`。
- 面板：新增"事件"标签页 + `loadEvents()`。
- 测试：`eventlog_test.go`（往返 + 无 DataDir 关闭）；`go build` + `go test -run TestEventLog` 通过。
- 仍待：LobsterHub 接入；LobsterAI 面板入口。

### P4-E 补充（LobsterHub，2026-11-04）

- `eventlog.go`：`<dataDir>/events.jsonl` + 10MB 轮转；`vault.Dir` 为空即关闭。
- API：`GET /api/events?lines=200`（Bearer）。
- 事件：`account.enable`、`account.disable`、`account.delete`、`account.clear_suspend`。
- 面板：左侧新增"事件"导航 + `#view-events` + `loadEvents()`，路由到 `events`。
- 测试：`eventlog_test.go`（往返 + 无 vault 禁用）；`go build` + `go test -run TestEventLog` 通过。
- P4-E 现状：Grok/QwenWork/MiniMax/LobsterAI/BuddyHub/LobsterHub 均有 `events.jsonl`；LobsterAI 面板入口仍待加。

### P4-E 收口（LobsterAI 面板入口，2026-11-04）

- `web/index.html`：导航加"事件"（`data-view="events"`）。
- `web/assets/app.js`：`VIEWS.events`、`renderView()` 分派、`renderEvents()`（`/admin/events?lines=200`，带刷新按钮）。
- 验证：`go build` + `go test ./web` 通过。
- **P4-E 全量收口**：6 个插件都有 `events.jsonl` + 面板入口。

---

## 9.5 契约冒烟（2026-11-04，P0 补充）

脚本：`scripts/smoke-provider-serve.ps1`（真拉起每个插件，不碰线上）

对 9 个副本逐个 `go build` → `--provider-serve --port 0` → 校验 ready 行 / `/v1/models` 非空且带 `context_window` / 无令牌 401 / 数据落 `data/`。

| 插件 | ready | models | 401 | data/ |
|---|---|---|---|---|
| mmxproxy | ✅ | 4（caps 4） | ✅ | ✅ |
| qw2api | ✅ | 2（caps 2） | ✅ | ✅ |
| grok | ✅ | 1（caps 1） | ✅ | ✅ |
| lobsterai | ✅ | 19（caps 19） | ✅ | ✅ |
| lobsterhub | ✅ | 25（caps 25） | ✅ | ✅ |
| buddyhub | ✅ | 27（caps 27） | ✅ | ✅ |
| skywork | ✅ | 2（caps 2） | ✅ | ✅ |
| loomy | need_config | — | — | — |
| flonno | ✅ | 1（caps 1） | ✅ | ✅ |

**冒烟发现并修掉的真实缺陷（都会导致插件在宿主里"没有模型"）：**

1. **MiniMax**：`fallbackModels` 只返回 `id`，空号池时无 `context_window`/`max_output_tokens`，也没有 1M 别名 → 补齐（含 `context_window_options`）。
2. **LobsterHub**：`modelList()` 的 `modelEntry` 只有 id → 加 `context_window`/`context_length`/`max_output_tokens` + `modelCaps` 表。
3. **BuddyHub**：`/v1/models` 纯动态，空池返回 0 条（宿主视为目录不可用）→ 加 `upstream.SeedModels()` 与 `seedModels()` 兜底。
4. **Grok**：grok2api 未就绪时 `/v1/models` 回 503 → 回退内置 `grok-4.7`（256K/32K）。
5. **Flonno**：空池时透传上游 502 → 空池直接回内置目录（`qwen3.8-27b` + 设置里的 context 值）。

**loomy**：`need_config`（缺真实 session）是契约允许状态，不算缺陷，登记为"需真实凭据验证"。

**脚本自身的坑（已修）：**

- `Set-Content -Encoding UTF8` 写 BOM → Go 拒收；改 `[IO.File]::WriteAllText` + 无 BOM。
- `Where-Object ... .Count` 在单元素列表上返回标量 → 改显式 foreach 计数。

---

## 9.6 面板 UI 冒烟（2026-11-04，P0 补充）

脚本：`scripts/verify-panel-ui.ps1`（拉起插件 → 抓真实 HTML → 校验"事件"入口）

| 插件 | 面板 | markers | 备注 |
|---|---|---|---|
| mmxproxy | ok | 2/2 | `panel_port=18991` 独立端口 |
| qw2api | ok | 2/2 | `/api/events` 在页内 |
| grok | ok | 2/2 | 日志抽屉 `log-source` |
| lobsterai | ok | 2/2 | HTML 导航 + `assets/app.js` 里的 `renderEvents` |
| lobsterhub | ok | 2/2 | `view-events` |
| buddyhub | ok | 2/2 | `loadEvents` |

**验证方式**：不只 grep Go 源码，而是**真拉起进程、抓真实返回的 HTML/JS**。LobsterAI 的 `renderEvents` 在独立 `app.js` 里，脚本对它额外抓了一次 `/assets/app.js`——这是我第一版脚本的漏项（只看 HTML 会误判）。

---

## 9.7 事件日志端到端（2026-11-05）

脚本：`scripts/verify-events-e2e.ps1`（触发动作 → 读 API → 核对写盘）

| 插件 | 触发 | 读回 | 落盘 |
|---|---|---|---|
| mmxproxy | `POST /v1/accounts/sync`（面板端口） | 200 events=1 | `data/events.jsonl` ✅ |
| qw2api | `POST /api/accounts/refresh` | 200 events=1 | `data/events.jsonl` ✅ |
| lobsterhub | `POST /api/accounts/1/disable`（预置 vault） | 200 events=1 | `data/events.jsonl` ✅ |
| buddyhub | `POST /api/credits/refresh` | 200 events=1 | `data/events.jsonl` ✅ |
| lobsterai | `POST /admin/refresh` | 200 events=1 | `data/events.jsonl` ✅ |

**脚本迭代中修正的 4 个判断错误（都是"看起来通过其实没验证到"）：**

1. MiniMax 的管理 API 在**面板端口**，不在契约端口 → 第一版 404；
2. 事件 API 返回的是**已格式化的一行一条**数组，不是原始 JSON 对象 → 数 `"ts"` 恒为 0；
3. LobsterHub 的 `disable` 必须先有账号才写事件 → 加 `seedVault` 预置 `vault.json`；
4. BuddyHub/LobsterAI 的 `RefreshCredits` 其实**已注入** → 从 `readOnly` 改为真触发。

**补的事件：** LobsterAI 的 `adminRefresh`/`adminRewards` 之前没写事件，已补 `credits.refresh`、`rewards.claim`。

---

## 9. P5：UI + 动效

- P5-A token（5）：QwenWork/Skywork/Grok/BuddyHub/LobsterAI/LobsterHub/LOOMy。
- P5-B 布局/暗色（7）：xhx、MiniMax、Flonno shell、侧栏/页头/卡片/表格。
- P5-C 动效（3）：count-up/额度条/状态 pulse/toast/按钮；`prefers-reduced-motion`。
- 设计令牌见报告 4.2；动效见 4.5。

### P5-A 执行记录（2026-11-04）

- QwenWork（`_plugin-work/phase3/qw2api/.../index.html`）：默认亮色；暖白 `#F7F6F1` + 抹茶 `#7C9A6D` + 奶酪 `#E9C46A`；危险色陶土红；品牌绿阶全部替换；主题 fallback 改 `light`。
- Skywork（`_plugin-work/phase5/skywork/.../index.html`）：同 QwenWork 的全套 token（默认亮色、抹茶阶、奶酪、陶土红）。
- Grok（`_plugin-work/phase4/grok/web/panel.html`）：`--bg/--accent/--green/--red/--amber` 全部换抹茶/暖白；圆角 10→12；加 `--cheese`。
- LobsterAI（`_plugin-work/phase5/lobsterai/web/assets/app.css`）：System Blue → 抹茶；表面/文字/边框暖灰；success/warning/error/info 换抹茶/奶酪/陶土/雾蓝。
- LOOMy（`_plugin-work/phase5/loomy/index.html`）：靛蓝/紫 → 抹茶；暖白表面；状态色换抹茶/奶酪/陶土。
- 验证：QwenWork/Skywork/LobsterAI/LOOMy `go build` 通过；QwenWork/Skywork server 测试通过。
- 仍待：BuddyHub/LobsterHub 暗色重写见 P5-B；xhx/Flonno shell；P5-C 动效。

### P5-B 执行记录（2026-11-04）

- BuddyHub（`_plugin-work/phase5/buddyhub/frontend/dist/style.css`）：深墨+荧光绿 → 暖白+抹茶；`#e6ead9`/`rgba(184,245,61)`/粉色 danger 全部替换；卡片/侧栏跟随。
- LobsterHub（`_plugin-work/phase5/lobsterhub/frontend/dist/style.css`）：同 BuddyHub 反转；品牌 mark/pill 用墨绿底。
- MiniMax（`_plugin-work/phase3/mmxproxy/web/index.html`）：纸墨朱红 → 暖白+抹茶；`--acc/#7C9A6D`、`--acc-deep/#5F7D52`、`--blue/#6E8FA8`、状态色换抹茶/奶酪/陶土。
- 验证：三者 `go build` 通过；MiniMax `go test` 通过。
- 仍待：xhx（Edge 面板）、Flonno shell 未改；P5-C 动效。

### P5-B 补充（Flonno，2026-11-04）

- `_plugin-work/phase5/flonno/internal/server/web/assets/style.css`：深墨+荧光绿 → 暖白+抹茶；`#e6ead9`/`rgba(184,245,61)`/粉色 danger 全替换；`index.html` 内嵌 SVG 的 `#b8f53d` 改 `#7C9A6D`。
- xhx：源目录没有 HTML/CSS 面板（只有 Go/Edge profile），**不适用** token 替换，登记为 N/A。
- 验证：Flonno `go build` 通过。

### P5-C 执行记录（QwenWork 首批，2026-11-04）

- 只加 `transform/opacity`，不改布局：
  - 按钮 hover/active 轻缩放；
  - 统计卡/账号卡/卡片 hover 上浮 + 阴影；
  - 连接状态 chip 柔和 pulse；
  - 进度条宽度过渡；
  - toast 出现/消失更短。
- 全部包在 `@media (prefers-reduced-motion: no-preference)`；已有的 reduce 全局降级仍生效。
- 验证：`go build` + `go test ./internal/server` 通过。
- Skywork 同套 5 个效果（同源 index.html）也已加，`go build` + server 测试通过。
- Grok/LobsterAI/LOOMy：同款低风险动效（按钮/卡片/状态 pulse），均带 reduce 降级；三者 `go build` 通过。
- 仍待：P4-E 其余插件接入事件日志；P2 上线；真实 UI/对话验收。

---

## 10. 子代理编排

| 角色 | 模型 | 权限 |
|---|---|---|
| 实施 | `qw2api/qwork-auto` | 只改白名单；不 push/不删目录 |
| 审查 | `mmxproxy/MiniMax-M3.1-Flash` | 只读 |
| 验收 | 主代理 | 构建/测试/截图/回滚 |

```js
phase("implement");
const impl = await agent(handoffPrompt, { provider: "local-proxy", model: "qw2api/qwork-auto" });
phase("review");
const review = await agent(reviewPrompt + "\n---\n" + impl, {
  provider: "local-proxy", model: "mmxproxy/MiniMax-M3.1-Flash"
});
return { impl, review };
```

失败：构建错误回给实施者 ≤2 次；审查阻断由主代理裁决；402/429 重试+回退默认模型。

---

## 11. 上线检查表

- [ ] 副本 build/vet/test
- [ ] canary `<id>-dev` 5 页 + 1 对话
- [ ] 备份 exe/provider.json/data
- [ ] 停插件 → 确认进程退出
- [ ] 替换 exe（留 `.bak`）→ 启动 → ready
- [ ] 面板 5 页 + 1 对话 + 数据核对
- [ ] 观察 10 分钟；失败还原 `.bak`

---

## 12. 风险

| 风险 | 缓解 |
|---|---|
| 覆盖运行中 exe | 停→备份→替换→启；canary |
| refresh token 复制 | 整目录移动；禁止复制 |
| 迁移丢账号 | 迁移前整目录备份；失败回滚 |
| UI 回归 | 只改样式；5 页截图 |
| MiniMax 1M 计费 | 独立 `-1M`；默认 512K |
| Grok 删除复活 | 文件+DB+关联+增量导入 |
| qw2api 池不稳定 | 重试/回退；任务拆小 |

---

## 13. 当前下一步

1. P2：用户停机窗口执行迁移脚本（LobsterAI/LobsterHub），MiniMax 启动自动迁移。
2. P3：QwenWork 真实模型/对话 + MiniMax 持久化（副本 → 验收）。
3. P4/P5 依次。
