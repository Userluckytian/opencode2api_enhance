# 阶段：插件源码归位 + 最新构建部署 + 冗余清理 + 可回撤归档

> **状态：** 已完成（2026-11-09）
> **For agentic workers:** 按 Task 顺序执行；每 Task 验证通过再进下一 Task。
> **元规范:** `docs/ai-framework/phased-plan-driven.md`

**Goal:** 把 14 个在役插件的源码收敛到 `D:\AI_Projects\plugins\`，把 10 个「源码比已装 exe 新」的插件重新构建部署，清理 ≈627MB 严格冗余，全部动作可回撤。

**Architecture:** 四个阶段串行——源码 Move（保留 .git）→ 构建到暂存区（不碰生产）→ 逐插件停/换/启 → 冗余 Move 到归档。所有搬移与删除都是 `Move-Item` 到 `_archive-20261109\`，由 `manifest.json` + `rollback.ps1` 保证可逆。

**实施档位：** 全能
**子代理：** 启用（构建失败排查、部署后验收各用独立角色）

---

## Global Constraints（冲突时以本节为准）

1. **只 Move 不 Remove**：任何"删除"都是移动到 `_archive-20261109\`，最终删除另行授权。
2. **凭据不进 git**：`provider.json` 的 `provider_private_configs` 只改 `_source` 一个键，其余逐字保留；不复制 cookie/token 到任何仓库。
3. **明确不做（本阶段）**
   - ❌ 不动 `data\` / `config\` / `auths\` / `web\` / 日志 / 浏览器缓存（≈3.6GB）
   - ❌ 不部署 `buddyhub` / `zcode` / `vibex` / `runbiosproxy`
   - ❌ 不 push 远程、不改宿主二进制 `opencode2api.exe`
   - ❌ 不擅自把当前 `enabled=false` 的插件改成 `true`
4. **Git：** 源码目录搬移会改变多个仓库的工作树位置，**本阶段不提交任何仓库**；`opencode2api_enhance/providers/grok` 的删除记录待人工确认后单独提交。
5. **生产写权限**：阶段 3 前必须用探针文件验证 `D:\Program Files\opencode2api\bin\providers` 可写，不可写则停并要求管理员权限。

---

## 前置事实（已实测，不再重复验证）

| 事实 | 证据 |
|---|---|
| 9 个已迁移插件用**绝对路径** `replace pluginsdk => D:/AI_Projects/plugin-template` | 逐个 `go.mod` 读取；无相对 replace、无 go.work |
| 搬移源码目录不会断构建 | 同上（replace 与插件自身位置无关） |
| 宿主提供启停 API | `core/manager/pluginprovider/http.go:90` `ToggleHandler`，`POST /api/admin/plugins/{id}/toggle` |
| 宿主 admin 端点 | `http://127.0.0.1:50080`，`Bearer sk-unified-local`（取自 devin/zcode deploy.ps1 默认值） |
| 当前 `enabled` 状态 | `providers\.plugin-state.json`：`runbiosproxy/skywork/vibex/zcode = false`，其余 true |
| 10 个插件源码比 exe 新 | 逐目录 newest `.go` mtime vs 安装 exe mtime |

---

## File Structure（预期变更）

| 路径 | 动作 | 职责 |
|---|---|---|
| `D:\AI_Projects\plugins\<id>\`（14 个） | 新建/搬入 | 源码唯一真源 |
| `D:\AI_Projects\_archive-20261109\` | 新建 | 归档根（snapshot / install-pre-deploy / removed / build / moved-away） |
| `providers\<id>\provider.json`（14 个） | 修改 1 个键 | `provider_private_configs._source.repo` |
| `providers\flonno2api\provider.json` | 修改 `entry` | `flonno2api-provider.exe` → `flonno2api.exe` |
| `plugins\mmxproxy\provider.json` | 修改 note | `_source.note` 内自身绝对路径 |
| `plugins\qw2api\build.cmd` | 修改 1 行 | 输出路径 `..\qw2api-plugin.exe` → `plugin-dist\qw2api-plugin.exe` |
| `_archive-20261109\manifest.json` | 生成 | 全量 original→archived 映射 |
| `_archive-20261109\rollback.ps1` | 生成 | 回撤脚本 |

---

## Task 0：落盘与快照

**Steps:**

1. 写本文件 + 在 `docs/issue-log/2026-11-09.md` 增条目。
2. 建 `_archive-20261109\{snapshot,install-pre-deploy,removed,build,moved-away,source-old-copies}`。
3. 快照：`.plugin-state.json`、14 个 `provider.json`、14 个在役 exe 的 size+sha256 → `snapshot\`。
4. 建空 `manifest.json`。

**验证:** `ls _archive-20261109\snapshot` 有 15 个 JSON + `exe-hashes.json`。

---

## Task 1：源码归位

**Steps:**

1. `New-Item D:\AI_Projects\plugins`
2. 14 条 `Move-Item`（映射见下），每条写 manifest `action=moved-source`。

| 目标 | 来源 |
|---|---|
| `plugins\buddyhub` | `D:\AI_Projects\buddyhub` |
| `plugins\devin` | `Devin-all\devin-plugin` |
| `plugins\zcode` | `Devin-all\zcode-plugin` |
| `plugins\flonno2api` | `_plugin-work\phase5\flonno` |
| `plugins\grok` | `_plugin-work\phase4\grok` |
| `plugins\lobsterai` | `Lobster\lobsterai2api` |
| `plugins\lobsterhub` | `_plugin-work\phase5\lobsterhub` |
| `plugins\loomy` | `loomy2api` |
| `plugins\mmxproxy` | `_plugin-work\phase3\mmxproxy` |
| `plugins\qw2api` | `_plugin-work\phase3\qw2api` |
| `plugins\runbiosproxy` | `BIOS-all\runbios_proxy` |
| `plugins\skywork` | `_plugin-work\phase5\skywork` |
| `plugins\vibex` | `vibexapi-all\vibex2api` |
| `plugins\xhx` | `_plugin-work\phase5\xhx` |

3. 更新 14 个安装目录 `provider.json` 的 `_source.repo`（4 个新增 `_source`，note 用统一文案）。
4. 修 `plugins\mmxproxy\provider.json` 的 note、`plugins\qw2api\build.cmd` 的输出路径。
5. 门禁：每个 Go 仓库 `go build ./...` exit 0。

**验证:** `go build ./...` 全绿；14 个 `provider.json` 的 `_source.repo` 等于 `D:\AI_Projects\plugins\<id>`。

---

## Task 2：构建到暂存区

产物落 `_archive-20261109\build\<id>\`，**不部署**。

| id | 命令 |
|---|---|
| devin | `powershell -File scripts\build.ps1` |
| flonno2api | `go build -trimpath -ldflags "-s -w" -o <stage>\flonno2api.exe ./cmd/flonno2api` |
| grok | `go build -trimpath -ldflags "-s -w" -o <stage>\grok-provider.exe .` |
| lobsterai | `powershell -File pack-plugin.ps1 -OutDir <stage>` |
| lobsterhub | `go build -trimpath -ldflags "-s -w" -o <stage>\lobsterhub.exe .` |
| loomy | `powershell -File scripts\make-provider-package.ps1 -OutDir <stage>` |
| mmxproxy | `go build -trimpath -ldflags "-s -w" -o <stage>\mmxproxy-provider.exe .` |
| qw2api | `go build -trimpath -ldflags "-s -w" -o <stage>\qw2api-provider.exe ./cmd/qw2api-plugin`（**去掉 `-H windowsgui`**） |
| skywork | `powershell -File build.ps1 -NoDeploy` |
| xhx | `go build -trimpath -ldflags "-s -w" -o <stage>\xhx-provider.exe ./cmd/xhx-provider` |

**门禁:** 有 `scripts\selftest.ps1` 的必须跑过；任一失败即停。

---

## Task 3：部署

固定流程（每插件）：备份现 exe+provider.json → toggle false → 等进程退出 → 复制新 exe → 恢复原 enabled → 记 manifest。

特殊项：
- **flonno2api**：`entry` 改 `flonno2api.exe`，旧 `flonno2api-provider.exe` 移入 `removed\`。
- **qw2api**：产物名对齐 `qw2api-provider.exe`。
- **skywork**：当前 enabled=false，部署后保持 false。

**门禁:** `/api/admin/plugins` 状态 running；`/v1/models` 或面板可达。

---

## Task 4：清严格冗余

全部 Move 到 `_archive-20261109\removed\`，保留原相对路径。清单见交付时生成的 `removed-index.md`。

---

## Task 5：回撤保障

1. `manifest.json` 完整（含 size/sha256/ts）。
2. `rollback.ps1`：`-WhatIf` / `-Only <id>` / 逆序还原。
3. 演练：`loomy` 部署→回撤→再部署。

---

## 验收标准总表

| # | 标准 | 通过条件 | 责任人 | 结果 |
|---|---|---|---|---|
| 1 | 源码归位 | `plugins\` 下 14 个目录，`.git` 与文件结构完整 | 自动化 | ✅ 14/14 |
| 2 | 搬移未断构建 | 每个 Go 仓库 `go build ./...` exit 0 | 自动化 | ✅ 13 个 Go 模块全绿（vibex 为 Node，无 go.mod） |
| 3 | `_source.repo` 正确 | 14 个 `provider.json` 逐一比对 | 自动化 | ✅ 14/14 |
| 4 | 部署成功 | 10 个插件 running，新 exe sha256 = 暂存产物 | 自动化 | ✅ 10/10 哈希一致；9 running + skywork 保持 disabled |
| 5 | flonno 单 exe | `entry=flonno2api.exe`，旧壳已归档 | 自动化 | ✅ 旧 `flonno2api-provider.exe` 已移入 `removed\install\flonno2api\` |
| 6 | 冗余可查 | `removed\` 与 manifest 一一对应 | 自动化 | ✅ 681.5 MB，121 条 manifest 记录 |
| 7 | 回撤可用 | loomy 演练通过 | 自动化 | ✅ 部署→回撤（哈希回到 `a8e22d28`）→再部署（`07fee396`） |
| 8 | 生产未受损 | enabled 语义不变；各插件 `/v1/models` 可用 | 端到端（用户） | ✅ 模型数全部与部署前一致 |

---

## 执行记录（2026-11-09）

### 途中发现并修正的问题

1. **搬移中断事故（devin）**：`shutil.move` 在 rename 失败后自动 copy+delete，`rmtree` 又被 `.git/objects` 锁打断，源目录丢了 18 个 `.git/hooks` + `logs` 文件（无内容损失）。副本完整（`git fsck` 通过、HEAD 正确），残留源已归档。**后续所有搬移改为 rename-only**。
2. **三个目录被遗留测试进程占用**：`python -m http.server 18899`（cwd 在 skywork）、`mockdbg.ps1`（cwd 在 grok）、`xhx-st.exe --provider-serve`（cwd 在 xhx）。均为 11-06/11-07 的测试残留，清理后搬移成功。
3. **devin 源码目录被 `node kernel/kernel.mjs` 占用**：同一类残留，清理后归档成功。
4. **`lobsterai/pack-plugin.ps1` 不支持绝对 `-OutDir`**：`Join-Path $root <绝对路径>` 拼出非法路径。已修（绝对路径直接使用）。
5. **`loomy/scripts/make-provider-package.ps1` 在 PS 5.1 下无法解析**：UTF-8 无 BOM + 第 62 行中文 note 被按 ANSI 解码。已加 UTF-8 BOM。
6. **`qw2api/build.cmd` 用 `-H windowsgui` 是错的**：GUI 子系统下宿主读不到 ready 行。已改为 CONSOLE 子系统 + 输出 `plugin-dist\qw2api-provider.exe`（与已装 exe 一致）。
7. **buddyhub 源码判错（重要）**：`provider.json` 的 `_source.repo` 指向 `D:\AI_Projects\buddyhub`，但该树最新 `.go` 是 10-29；真实最新源码在 `_plugin-work\phase5\buddyhub`（11-05，多 `internal/pluginhub/{eventlog,status}.go`）。证据：已装 exe 含 `eventlog`/`status.go` 符号。已用后者替换 `plugins\buddyhub`，旧树归档。
8. **`lobsterhub` 首次部署失败**：进程未在 30s 内释放 exe 锁，`copy2` 抛 WinError 32。重试成功，enabled 已恢复 true。
9. **`lobsterai` selftest 首次 FAIL**：`TestEnsurePinnedSkipsOccupiedPorts` 端口 35873 冲突（测试自身并发抢端口）。单包重跑与整体重跑均 PASS，判为 flaky，非回归。
10. **loomy 部署成"旧版本"（用户反馈，已修正）**：`loomy2api`（PLT-4 迁移分支）基于**缺 4 个模块的旧特性基座**——`store.go`(283 行)/`panel_api.go`(433)/`upstream_api.go`(308)/`observe.go`(217) 从未进过该仓库 git 历史（`git log --all -- <file>` 全空）。真实运行源码是 `_plugin-work\phase5\loomy`（无 git，11-06），已装 exe 内嵌路径 `D:/AI_Projects/_plugin-work/phase5/loomy/main.go` 为铁证。表现为面板功能回退（LOOMy 的独立控制台在 `panel_port=11457`，A 版 `/` 返回 102KB 且 `/api/status`、`/api/points` 均 200；B 版无 `panel_port` 概念，面板挂在插件端口且功能少）。**已回撤 exe 至 `a8e22d28…`，并把 A 恢复为 `plugins\loomy`；PLT-4 分支移到 `D:\AI_Projects\_branches\loomy-plt4` 保留待合并。**
11. **`rollback.ps1` 变量名冲突**：PowerShell 变量名大小写不敏感，`$pre`（局部）覆盖了 `$PRE`（常量），第二次回撤同一插件时拼出 `install-pre-deploy\loomy\loomy`，误判"无备份"并把现役 exe 移进 `rollback-discard`（插件一度 `status=error`）。已改名 `$PREROOT`/`$preDir`，并对 `deployed` 按 id 去重（只回撤最新一次）。

### 附：loomy 血脉合并（1.9.0 功能基座 + PLT-4 迁移 = 1.9.1）

**问题**：loomy 有两条血脉，各自缺一半——

| | A `_plugin-work/phase5/loomy`（1.9.0） | B `loomy2api`（1.5.1 基座 + PLT-4） |
|---|---|---|
| 独有功能 | `store.go`(283) `panel_api.go`(433) `upstream_api.go`(308) `observe.go`(217)、调用日志/用量统计（`logReqFull`/`usageCollector`）、`fetchPointsPage` 三头组合、独立版根路径内嵌面板 | `quota` 投影、`schedule` 两 Job、分段鉴权（回环面板豁免）、`pluginsdk` 依赖 |
| 缺 | PLT-4 全部迁移 | A 的 4 个模块与 1.9.0 面板能力 |

**合并方式**：以 A 为基座，把 B 的 PLT-4 增量移植进来（不是合并文件，B 的公共文件更旧）。

| 文件 | 改动 |
|---|---|
| `go.mod` | 加 `require pluginsdk v0.0.0` + `replace => D:/AI_Projects/plugin-template` |
| `points.go` | 新增 `(d *pointsData) accountQuota()` 投影（unit=points，daily 单窗口） |
| `schedule_jobs.go` | 新增：`petworkClaimJob`(30min) / `sessionAlertJob`(6h) / `startScheduleJobs` |
| `panel.go` | `claimLoop` 的 tick 闭包抽成 `claimTick()`；`claimLoop` = 先跑一次 + `startScheduleJobs(30min)` |
| `main.go` | `sessionWatchdog` 循环体抽成 `scanSessionAlerts()`；`sessionWatchdog` = `startScheduleJobs(6h)` |
| `plugin.go` | `ServeHTTP` 分段鉴权：`/v1/*` 强制 Bearer；非 `/v1` 走 `panelServeHTTP` 且回环豁免；新增 `isLoopbackRemote`；版本 1.9.0→1.9.1 |
| `plt4_migration_test.go` | 新增（quota 投影 / Job Spec / Job 契约 / 告警扫描，4 组） |
| `provider_serve_test.go` | 追加 `TestPluginPanelLoopbackAuth`（6 子用例：回环/非回环 × 面板/模型代理 × 有无令牌） |

**验证**：

| 门禁 | 结果 |
|---|---|
| `go build ./...` / `go vet ./...` | 通过 |
| `go test ./...` | 通过（含新增 5 组测试，6 子用例全 PASS） |
| 离线契约冒烟（`--provider-serve --port 0`） | ready 行 `version=1.9.1`、auth 回显一致；`/` 200（102 KB 完整面板）、`/api/status` 200、`/api/settings` 200、`/v1/models` 无令牌 401 |
| 线上部署后 | 插件端口：`/`、`/api/status`、`/api/settings`、`/api/points` 全 200；`/v1/models` 无令牌 401；独立控制台 `11457` 全部 200；宿主显示 `v1.9.1`，模型数 10 不变 |

**血脉保留**：

- `plugins\loomy` = 合并版 1.9.1（唯一真源）
- `D:\AI_Projects\_branches\loomy-a-1.9.0` = A 原始树
- `D:\AI_Projects\_branches\loomy-plt4` = B（git 仓库，`feat/plt4-migration`）

**回退**：`install-pre-deploy\loomy\loomy-provider.exe` 仍是 A 1.9.0 的 exe（`a8e22d28…`），`rollback.ps1 -Only loomy -Actions deployed -Execute` 可一键还原。

---

### 事后复盘：判"源码真源"不能只看 mtime

两次误判（buddyhub、loomy）同一个根因：**用最新 `.go` 的 mtime 当"哪棵树最新"的证据**。但 PLT-4 迁移会在旧基座上产生新提交，mtime 反而更新。

可靠判据（本次已验证）：
1. **exe 内嵌路径**：未 `-trimpath` 构建的 Go 二进制里能直接读出源码目录（`strings | grep AI_Projects`）。
2. **符号存在性**：`eventlog`/`store.go`/`observe` 这类只在某一棵树里出现的标识符，能在 exe 里 grep 到。
3. **git 历史**：`git log --all -- <file>` 为空 = 该文件从未属于这个仓库，说明是另一条血脉。


### 最终产物

| 项 | 值 |
|---|---|
| 源码根 | `D:\AI_Projects\plugins\`（14 个） |
| 归档根 | `D:\AI_Projects\_archive-20261109\`（681.5 MB） |
| 归档索引 | `_archive-20261109\removed-index.md` |
| 回撤脚本 | `_archive-20261109\rollback.ps1`（`-Execute` / `-Only` / `-Actions`） |
| manifest | `_archive-20261109\manifest.json`（121 条） |

---


## 风险与降级

| 风险 | 缓解 |
|---|---|
| Program Files 不可写 | 阶段 3 前探针验证，失败即停 |
| admin API 不可达 | 退回 `Stop-Process -Name` |
| qw2api GUI 子系统读不到就绪行 | 构建去掉 `-H windowsgui`；selftest 不过不部署 |
| flonno 单/双 exe 切换失败 | entry 与文件名同改；失败用 `install-pre-deploy\flonno2api\` 回滚 |
| 构建产物行为不一致 | 先暂存 + selftest 门禁，不直接覆盖生产 |

---

## 给接手 AI 的完整提示词

你是负责 **opencode2api_enhance** 的实现代理。请完整执行本阶段。

- 唯一实施计划：`docs/ai-framework/plans/2026-11-09-plugin-source-consolidation.md`
- 必读：`AGENTS.md`、`docs/ai-framework/phased-plan-driven.md`
- 顺序：Task 0 → 5，每 Task 验证通过再进下一
- 铁律：只 Move 不 Remove；不 push；不动 data/日志/缓存；不擅自启用 disabled 插件
- 交卷：manifest 摘要、10 个插件的部署前后 sha256、`go build ./...` 输出、loomy 回撤演练记录

---

## 残留手工验收清单

1. 打开宿主面板，逐个插件确认模型列表与面板可打开。
2. 确认 `skywork / runbiosproxy / vibex / zcode` 仍是 disabled（未被我擅自开启）。
3. 确认归档目录 `_archive-20261109\` 保留，暂不删除。
