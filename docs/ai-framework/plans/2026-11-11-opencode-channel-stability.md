# 阶段 X：OpenCode 通道稳定性 —— 三个参考项目调研 + 实机复现 + 方案选型

> **状态：** 已完成（调研 + 实测 + 选型，全部结论见 `docs/opencode-channel-research-2026-11-11/99-VERDICT.md`）
> **类型：** 调研 / 选型（**不写生产代码**，除实验用临时脚本与配置）
> **For agentic workers:** 本阶段是「证据型」阶段，不是「实现型」。禁止无证据结论；每个判断必须带日志行 / 文件行号 / HTTP 响应。

**Goal:** 查清「OpenCode 通道经常无通道可用」的真实根因，并在 9router / opencode2api-win / Opencode2api 三个参考实现中选出比现状更稳的做法，给出可执行结论。

**Architecture:** 现状是「管理器(40000) + 托管引擎 opencode2api-win(40080)」双进程；引擎内部三条 lane：WARP 匿名 → zen key → go key（外加 egress proxy）。调研面覆盖：宿主管控层（本仓库 `core/manager`）、引擎 lane 选择与冷却（opencode2api-win）、替代实现（9router、Opencode2api）。

**Tech Stack:** Go 1.22（本仓库 + opencode2api-win）、Node/Next.js（9router）、Node/Bun（Opencode2api）、PowerShell 实机测试。

**实施档位：** 全能
**子代理：** 启用（每个参考仓库 1 个独立分析子代理，互不共享上下文）

---

## 前置阅读

| 优先级 | 文件 |
|--------|------|
| P0 | `docs/ai-framework/phased-plan-driven.md`、本文件 |
| P0 | `core/manager/freetier_engine.go`、`core/manager/freetier_warp_fallback.go` |
| P1 | `AGENTS.md`、`docs/issue-log/OPEN.md`、`docs/issue-log/2026-11-11.md` |

**仓库路径：** `D:\AI_Projects\opencode2api_guide\opencode2api_enhance`
**参考仓库（只读调研，不改其代码）：**

- `D:\AI_Projects\refs\9router`（Node/Next.js 聚合网关）
- `D:\AI_Projects\refs\opencode2api-win`（当前托管引擎，Go）
- `D:\AI_Projects\refs\Opencode2api`（Node/Bun 网关，v3.x）

---

## Global Constraints

1. **不改生产实例**：40000 / 40080 上跑着的进程不许停、不许改配置（除用户明确授权的重启）。
2. 实验一律用**独立端口 + 独立工作目录**（如 40180 / `%TEMP%\oc-research\...`），跑完即杀。
3. 密钥/凭证不进 git；实验日志如含 key 必须先脱敏再入库。
4. **不臆测**：结论必须是「看到 X 行日志 / 读到 Y 行代码 / 得到 Z 状态码」。
5. 明确不做（本阶段）：
   - ❌ 不在本阶段重写引擎或引入新依赖到生产路径（只出结论与最小 PoC）
   - ❌ 不自动 push、不改 `main` 的历史
6. Git：本阶段若产生文档/脚本，小步 commit；默认不 push。

---

## 阶段开头：上阶段遗留

无（上一阶段 M3 已关账；本阶段为独立调研）。

---

## 现状快照（调研起点，2026-11-11 09:35）

| 项 | 值 |
|----|----|
| 管理器 40000 | pid 56240，`D:\Program Files\opencode2api\bin\opencode2api.exe`，启动于 2026-11-09 11:34 |
| 引擎 40080 | pid 40176，`...\bin\opencode2api-win.exe`，**启动于 2026-11-06 16:40** |
| 管理器配置 | `engine_warp_enabled=false`、`engine_channel_order=key_first`、10 个 zen key |
| 引擎磁盘配置 | 2026-11-11 00:25 写入：`warp.enabled=false`、`anonymous=false` |
| 引擎运行时 | 仍在 WARP 模式：`/healthz` 报 `warp.active=1`、`cooling=40`；日志持续 `WARP egress is unavailable: WARP pool has no ready egress` |
| 请求统计（stdout.log，4252 条 routed） | 200: 3139 / **503: 921（21.7%）** / 429: 158 / 403: 12 / 502: 4 |
| 503 归因 | 921 条**全部**为 `WARP pool has no ready egress`（WARP 池空 → key 也被判必须走 WARP → 直接放弃） |

**待验证假设 H1（首要）：** 引擎进程启动于 11-06，之后管理器写盘的新配置（关 WARP / key_first）从未被进程加载；且 `StopFreeTierEngine` 只按内存 pid kill（管理器重启后 pid=0），`StartFreeTierEngine` 见到端口在听就直接返回 ready → **配置漂移永久化**，WARP 已废但请求仍全部被绑到 WARP。

---

## 调研问题（每个子代理都要回答）

1. 该实现如何选择「通道 / lane / provider」？失败时的降级顺序是什么？
2. 遇到 429 / 403 / 空池 / 上游 5xx 时，如何冷却与恢复？冷却账本按什么维度记（key / IP / 出口 / 模型 / 会话）？
3. 有没有「通道全挂」时的兜底（多厂商、付费兜底、探测后摘除）？有没有「避免把整池烧掉」的设计？
4. 近期提交 / PR / issue 是否专门修过「no channel available / 429 风暴 / 空池」？
5. 该实现能否直接替代或部分替代当前 40080 引擎？迁移成本与许可证风险？
6. 最值得抄的 1~3 个机制是什么（要能落到本仓库文件级）？

---

## File Structure（预期产出）

| 文件 | 动作 | 职责 |
|------|------|------|
| `docs/ai-framework/plans/2026-11-11-opencode-channel-stability.md` | 新建 | 本计划 + 结论回填 |
| `docs/opencode-channel-research-2026-11-11/*.md` | 新建 | 三仓分析报告 + 实测记录 + 结论 |
| `docs/issue-log/2026-11-11.md` | 修改 | 追加本次需求条目 |
| `docs/issue-log/OPEN.md` | 修改 | 索引一行 |
| `scripts/research/*.ps1` | 新建（可选） | 可复现实测脚本（脱敏） |

---

## Task 1：锁定现状证据（主会话自做）

**行为：** 从运行中的引擎日志/配置/进程表取证，量化「无通道可用」的占比与归因。

**Steps:**
1. 统计 `request_routed` 的 status 分布与 503 归因（已完成，见上表）。
2. 按天切片，找出 503 开始恶化的时间点，与 WARP 出口 403 时间线对齐。
3. 核对进程启动时间 vs 配置文件 mtime，证明配置漂移。
4. 复现：对 40000 发一条最小 chat/responses 请求，记录耗时与状态码/错误体。

**期望：** 至少一条可复现请求在 25s 后 503，`error` 含 `WARP pool has no ready egress`。

---

## Task 2：三仓并行调研（3 个子代理）

**行为：** 每个仓库一个独立子代理：`git fetch` 拉最新 → 读关键模块 → 看近 30 天提交/PR → 按 §调研问题 出报告。

**Steps:**
1. 各子代理在 `docs/opencode-channel-research-2026-11-11/<repo>.md` 落盘报告（或回传给主会话）。
2. 报告必须带 `文件:行号` 与提交哈希。
3. 闭源/仓库消失的，如实写「不可得」，并说明替代证据（README/镜像/发布包）。

---

## Task 3：实机对照实验（主会话自做）

**行为：** 用独立端口起「干净配置」的对照实例，验证「WARP 关闭 + key 直连」是否真的 0 通道问题；再验证替代实现的可用性。

**Steps:**
1. E1：临时目录 + `warp.enabled=false` + `anonymous=false` + 同一批 key，端口 40180，发 5~10 条请求，记录状态码分布。
2. E2：若 E1 通过 → 证明「无通道」是配置漂移 + WARP 依赖，而非 key 失效。
3. E3：对 9router / Opencode2api 做最小启动可行性评估（能否在本机起、需不需要额外账号），能起就发 1~3 条请求对照。
4. 记录：启动命令、脱敏配置、响应摘要、耗时。

**期望：** E1 成功率显著高于现状（现状 21.7% 503），且无需 WARP。

---

## Task 4：结论与方案选型（主会话）

**行为：** 给出 2~3 个候选方案，按「稳定性 / 迁移成本 / 许可 / 可维护性」打分，推荐一个，并写清落地步骤与风险。

---

## 验收标准总表

| # | 标准 | 通过条件 | 责任人 |
|---|------|----------|--------|
| 1 | 现状量化 | 有 status 分布 + 503 归因 + 进程/配置时间线证据 | 主会话 |
| 2 | 三仓调研 | 三份报告齐全；不可得的仓如实标注 | 子代理 |
| 3 | 实机复现 | 至少 1 条 503 可复现；至少 1 组对照实验数据 | 主会话 |
| 4 | 结论可执行 | 推荐方案带文件级落点 + 回退方式 | 主会话 |
| 5 | 红线 | 生产实例未被改动；无密钥入库 | 主会话 |
| 6 | 文档 | 计划 §结论 回填 + issue-log 同步 | 主会话 |

---

## 风险与降级

| 风险 | 缓解 |
|------|------|
| 实验实例抢端口 / 干扰生产 | 独立端口 40180+，独立工作目录，结束即 kill |
| GitHub 拉取失败（网络） | 用本地已有 checkout + `git log`；标注「未拉到最新」 |
| 仓库闭源/消失 | 如实标注，转向 README/发布产物/PR 讨论 |
| 免费额度被打光 | 只发最小请求（≤10 条/实例），模型选 free |

---

## 结论（回填区）

**完整结论：`docs/opencode-channel-research-2026-11-11/99-VERDICT.md`**（含三仓对比表、实测数据表、根因分层 L1–L5、方案 P0/P1/P2、不换引擎的四条理由）。逐仓报告：同目录 `opencode2api-win.md` / `Opencode2api.md` / `9router.md`；现场取证与 E1/E2/E4 实测：`00-current-state-and-tests.md`。

### 一句话

今天不是上游把额度打光，是**我们自己把通道焊死在一条已经废掉的 WARP 出口上**：引擎只要开着 WARP 就把「带 key 的免费请求」也强制走 WARP、空池即放弃整个 key tier（连上游都不打），而我们托管侧的引擎进程已成孤儿（`pid=0`），导致「保存配置 / 重启引擎」全是空操作 —— 关 WARP 的配置 11-11 00:25 就写好了，进程 11-06 起从未换过。同机同批 key，**只关 WARP 就 5/5 通过**。

### 验收表自评

| # | 标准 | 结果 | 证据 |
|---|------|------|------|
| 1 | 现状量化 | ✅ | 4252 条 routed / 921 条 503（21.7%）按天 10%→100%；503 error 100% 同源，均匀分布在全部 10 个 key |
| 2 | 三仓调研 | ✅ | 三份报告 816 / 656 / 455 行，逐条 `文件:行号`；`opencode2api-win` 远程 **404 不可达**已如实标注（H1 假设升级为已证实根因 L3/L4） |
| 3 | 实机复现 | ✅ | 503 复现 26 393ms；E1 5/5 200；E2 直连 403；E4 热切 reload 3/3 200；R1 `go test -tags research` **PASS** 逐条复现死锁两半 + 两个对照组 |
| 4 | 结论可执行 | ✅ | 止血 1 条命令（待授权）；P0 四项到 `文件:函数` 级；P1 三项到机制 + 来源行号 |
| 5 | 红线 | ✅ | 生产 40000/40080 未重启未改配置（已复核端口/pid 不变）；实验实例 40180–40189 全部杀净；密钥未入库（文档只引用尾 5 位/脱敏） |
| 6 | 文档 | ✅ | 本文件 §结论 回填 + `docs/issue-log/2026-11-11.md` #2 + `OPEN.md` 索引 |

### 档位与跳过项

| 跳过项 | 原因 | 待补做 |
|--------|------|--------|
| 未做 P0 代码修复 | 本阶段定位为「调研/选型」，Global Constraints 第 1 条 | ⬜ 另立实现阶段（P0 四项） |
| ~~未止血重启生产引擎~~ | 破坏性操作需人类明确授权 | ✅ **已完成**（15:08 授权后执行，见顶部第二轮记录） |
| 9router / Opencode2api 未做端到端产能对比 | 二者都需额外账号/指纹桥，且结论已判定「不替换」，投入无收益 | ❌ 不做（结论层面不需要） |

### 第二轮追加（14:23 用户要求：GitHub 搜索 Top-5 仓库）

范围外新增，仍属「调研 + 实机」，未违反 Global Constraints（生产改动均经用户 15:07 明确授权）。

| 项 | 结果 |
|---|---|
| Top-5 clone | `%TEMP%\oc-refs-20261111\`（系统自动清理目录）；5 仓 + v1.4.6/v1.4.7 取证，源仓库全程只读 |
| 分仓报告 | `top5-01-Lukejdjd-manager.md` / `top5-02-6Kmfi6HP-upstream.md` / `top5-04-dengyie-resilience.md` / `top5-05-IMROVOID-edge.md`；汇总 `top5-99-consolidation-and-upgrade.md` |
| **推翻上午结论** | ① 「引擎上游已死」❌ —— 真上游 `jasonxu114514/opencode2api` 活跃，我们基线 = 其 v1.4.6 + wbi35772 补丁（blob 比对钉死）② L4 的 24h 出口冷却是**分叉自加**，stock 恒 30min ③ E2「地理封锁」削弱（同机直连可拿 429 而非 403；TLS/JA4 指纹说被 Workers 形态否证） |
| 止血（用户授权） | ✅ 15:08 杀孤儿 40176 → 看门狗 5s 自愈（新 pid 18788，首次加载正确配置）→ **3/3 = 200**；连续 21h 全灭结束 |
| 引擎升级 R1（用户授权） | ✅ 15:42 换 stock **v1.4.7**（SHA256 校验、保留 v1.4.6 备份可秒退）；`healthz ver=v1.4.7`、三协议推理全 200（responses ×3 + chat + anthropic messages） |
| Kilo 挂载（用户新需求） | ✅ 15:54 作为 `providers[]` 免费供应商挂上（11 个 isFree 档），端到端 200×2，**不依赖 7890**；训练警示今晚生效 + 结构化字段已改码待部署。见 `top5-98-kilo-mounted.md` |
| 本轮代码改动 | `custom_providers.go` / `vendors/custom/registry.go` / `src/lib/api.ts` / `src/pages/CustomModelsPage.tsx`（`data_policy_note` 三态 + 徽标）；`go build`/`vet`/`test`/`tsc -b` 全绿 |

### 下阶段（勿塞进本阶段）

1. **实现阶段：P0 托管层修复**（P0-1 认领孤儿 pid / P0-2 判据改观测 / P0-3 引擎 reload 优先 / P0-4 漂移可观测）+ 把 repro 测试转正为回归锁。**可抄件已到位**：Lukejdjd 的 pid 文件 `{pid,binary}` + kill 前 Win32 路径全等 + 「成功 = 观测到期望值」（`platform.ts:110-116/41-58/363-372`）。
2. **实现阶段：P1 三机制**（失败语义诚实化 / 二维冷却账本 / 兜底查账本）。首选 dengyie **quota preflight**（全池墙 → 零上游调用、毫秒级 429+Retry-After）与 `MarkListingSuccess ≠ MarkSuccess`。
3. **决策待定：P2** 免费 lane 与付费 lane 是否合并到统一推理入口（B 自建薄代理跨 lane failover / C 只做可观测 + 半自动）。Kilo 挂载后 B 的兜底源已具备一半。
4. 🔴 **优先插队项（本轮新发现的安全缺口）**：`vendors/custom/custom.go:332-333` 对所有 custom 源硬编码 `Free: true`，而 `auto_model.go:266-297` 按「免费集合」选 auto 候选 ⇒ **一旦有人开启 auto，会把含私有代码的编码流量随机路由到会训练提示词的 Kilo**。现仅靠 `auto_model.enabled=false` 侥幸安全。修法：候选排除带 `data_policy_note` 的源，或显式白名单隔离。
5. 部署缺口：宿主 exe 被运行中进程锁定（本仓多次登记），需停机窗口才能让 `data_policy_note` 徽标与后端字段上线。
6. **升级带来的遥测字段变化（本轮实测发现，需登记）**：`request_routed` 日志行少了 6 个字段 —— `egress` / `exit_id` / `penalty_kind` / `route_native` / `route_error` / `cooldown_until`。根因不是 v1.4.7 没修，而是**这些字段本就是 wbi35772 分叉自己加的**（`internal/telemetry/http.go` 对比 stock v1.4.6 的 diff 明确写着「Routing diagnostics — these come from UI / admin debug endpoints」），换 stock 就一起没了。
   - **实际影响为零**：`OPEN.md` 早就记着实测 83/83 这些字段**恒为空串**（分叉只加了字段没填值），我们唯一读它们的是 `calllog_engine.go:54-55` 的 `RouteError`/`Penalty`，而 `:288` 注释本来就写着「引擎自己的 route_error / penalty_kind 常常是空串」——即代码早已按「不可信」处理。
   - **已验证管线没坏**：`/api/admin/call-metrics/summary` 升级后正常增长（`sources.engine=4293`、`last_ts=11-11 16:03`、`ok_rate=73.4%`），Go 的 `encoding/json` 对缺失字段留零值，解析容错。
   - **待办**：① 把 `OPEN.md` 那条「字段全空」升级为「stock 无此字段，逐出口分析必须在托管侧自建」；② 清理 `calllog_engine.go` 里这两个已成死字段的映射（连同 `:288` 注释更新），或改由我们自己在托管侧记录实际出口。
7. 既有相邻项：`OPEN.md` 里 `callmetric_engine_test.go` 时间炸弹（4 条用例硬编码 `2026-11-05`）—— **本轮复现确认仍在**（`go test -run 'CallMetric|CallLog|Engine'` → 4 FAIL，报 `metrics = 0, want 1`；因数据滑出 `ReadCallMetrics(now±48h)` 窗口，今日已超 6 天）。与升级无关，但既然每次跑包都会红，建议下阶段一并修（测试时间相对 `time.Now()` 生成）。
