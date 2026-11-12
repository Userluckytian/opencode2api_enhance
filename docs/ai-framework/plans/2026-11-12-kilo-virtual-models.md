# 阶段 M：Kilo 虚拟模型（命名组合 + 多策略选路）

> **状态：** 计划已就绪，待批准后实施
> **For agentic workers:** 按 Task 顺序执行；每 Task 测完再进下一 Task。验收认证据，禁止「应该能过」。
> **元规范:** `docs/ai-framework/phased-plan-driven.md`；仓库约定 `AGENTS.md`
> **基线分支：** 当前 `feat/phase-k-kilo-gateway`（tip `3710d03`），**不新开分支**，小步 commit，不 push

**Goal:** 在 Kilo 独立网关（30080）内支持用户创建**多个命名虚拟模型**：客户端请求虚拟名，网关在用户勾选的一组候选模型里按策略选一个（或并发竞速），把胜出者的响应原样透传。

**Architecture:**
- 虚拟模型配置存**独立 JSON**（`<dataDir>/runtime/kilo/virtual_models.json`），不塞进 `config.json`（避免 `MergeConfigJSON` 双结构冲突，也便于原子整表保存）；后端 `core/manager` 包内读写，零 main 包依赖（延续阶段 K 独立性红线）。
- `handleChat` 在解析出 `model` 后、free_only 校验前插入「虚拟模型解析」：命中虚拟名 → 用同一份请求体按策略打到候选 → 胜出响应沿用现有透传/观测/警示头逻辑（抽成 `forwardOne()` 复用）。
- 候选范围 = **Kilo 全部模型（含付费）**，但 free_only 开关仍生效（虚拟模型自身不绕过边界；用户勾付费档需先关 free_only）。
- 前端 KiloPage 新增「虚拟模型」管理卡片：增删改、候选多选（从当前目录勾）、策略选择、启停。

**Tech Stack:** Go 1.22 标准库（`context` / `sync` / `net/http`）；React 19 + TS + Tailwind。

**实施档位：** 全能　**子代理：** 启用（独立代码审查 + 验收）

---

## 参考实现调研结论（9router，用户 11-12 要求核对）

| 项 | 结论 | 证据 |
|---|---|---|
| 有无「组合模型」概念 | ✅ 有，叫 **Combo** | `D:\AI_Projects\refs\9router\docs\ARCHITECTURE.md:14,117,142` |
| 语义 | **按序 fallback 链**：按序试，失败/额度耗尽才换下一个；**不是并发竞速** | `ARCHITECTURE.md:210-240` 流程图；`README.md:619-628` 示例（订阅→便宜→免费自动切换） |
| 配置实体 | `{id, name, models: string[], kind}`；名称校验 `a-zA-Z0-9-_`；API `src/app/api/combos/*` | `ARCHITECTURE.md:354-360`；`src/app/api/combos/route.js:6,24-41` |
| 轮询策略 | 有 `resetComboRotation(comboName)`，说明支持轮换 | `src/app/api/combos/[id]/route.js:52-53` |
| fallback 判定 | 状态码 + 错误消息启发式 | `ARCHITECTURE.md:240`；`open-sse/services/accountFallback.js` |
| 值得抄 | ① 命名组合 + 候选序列（最简可行）② 失败自动切换 ③ 轮询；**不抄**它的「多账户/多供应商」复杂度 | — |

**策略决策（结合用户选择）：** 用户选「Kilo 全部模型（含付费）+ 多个命名虚拟模型」，「谁快用谁」未明确选 A/B/C。考虑到 Kilo 全部免费档 `mayTrainOnYourPrompts=true`（提示词会被训练）、付费档会真计费，**真并发竞速 = 同一份提示词复制 N 份给上游 + N 倍计费**，默认不做。实现三种策略，默认 `fallback`：

| 策略 | 行为 | 上游请求数 | 适用 |
|---|---|---|---|
| `fallback`（默认，9router 式） | 按候选顺序试，2xx 即返回；失败/429/5xx 自动换下一个 | 1（失败才加） | 稳定优先、省额度 |
| `round_robin` | 每次请求轮换起点，后续仍按序 fallback | 1 | 分散负载 |
| `race` | 同时发全部候选，第一个返回 2xx 的胜出，取消其余 | N | 真·谁快用谁（用户显式开启，代价自担） |

> `race` 的代价在 UI 上必须写明（N 倍上游调用 / 提示词被复制 N 份 / 付费档 N 倍计费），默认关闭。

---

## 前置阅读（必须）

| 优先级 | 文件 |
|--------|------|
| P0 | 本文件、`docs/ai-framework/phased-plan-driven.md` |
| P0 | `core/manager/kilo_gateway.go`（`handleChat` :619-712、`rewriteModel` :826、`isModelBlockedByFreeOnly`、`apiMux` :545） |
| P0 | `core/manager/kilo_calls_log.go`（观测账本，虚拟模型要记「实际胜出模型」） |
| P1 | `AGENTS.md`、`coding-standards.md`、`docs/issue-log/OPEN.md`、`docs/ai-framework/plans/2026-11-11-kilo-independent-gateway.md`（Global Constraints 8 明确「不做 auto 候选接入」—— 本阶段在**30080 内部**实现，不接入 50080 auto） |

---

## Global Constraints（冲突时以本节为准）

1. **独立性红线**：虚拟模型全部逻辑在 `core/manager/kilo_gateway*.go` 内，**不得**引用 `globalAgg` / `chatRouterVar` / `vendors/custom` / `ProviderService` / `auto_model.go` / 50080 / 40080 / main 包。K8 守卫继续覆盖新增文件。
2. **候选范围**：Kilo 上游目录全部模型（含付费），但 `kilo_free_only=true` 时 `isModelBlockedByFreeOnly` 仍对每个实际转发的候选生效 —— 虚拟模型**不绕过** free_only 边界。
3. **响应不改写**：胜出者响应体（含上游真实 `model`）、状态码、Content-Type 原样透传；`X-Kilo-Data-Policy` 头每个响应都有。
4. **观测账本**：每次请求记录 `Requested`（虚拟名）/ `Actual`（胜出的上游真实 model）；`race` 额外记参与了几个候选。
5. **密钥不进 git**；配置 JSON 只存模型 ID 与策略，不存密钥/请求体。
6. **不替换生产 exe/dist**；验证走 `npm run tauri:dev`（dev 30082 / 44100 段）。前端改动需同步 `dist/` → `src-tauri/target/debug/bin/dist/`。
7. **Git**：小步 commit，规范 `<gitmoji><type>(<scope>): 中文描述`；**默认不 push**。
8. **YAGNI**：不做跨链路混合候选、不做权重/成本账本、不做配额预检、不做虚拟模型嵌套（虚拟名不能指向另一个虚拟名）、不做 50080 auto 接入。

---

## 阶段开头：上阶段遗留（如实带入，本阶段不修）

| 上阶段遗留项 | 来源 | 状态 |
|---|---|---|
| V-6b：`custom.go:332` 硬编码 `Free:true` + `auto_model.go:266` auto 候选通用缺陷 | 阶段 K 独立审查 B4 | ⬜ 本阶段不修（与本阶段无依赖） |
| B5：生产部署 + 停机 V-7 | 阶段 K 独立审查 B5 | ⬜ 需人类授权 |
| `callmetric_engine_test.go` 4 条时间炸弹 | OPEN.md 11-10 #5 | ⬜ 整包「无新增失败」判定 |
| 累计统计缺 vendor 维度 | 阶段 L 遗留 | ⬜ 另立阶段 |
| 阶段 L 同步落盘偏离（书面接受）、`call_log_max` 不约束 Kilo 日志 | 阶段 L 审查 A/E | ⬜ 已知限制，另立阶段 |

---

## 与前后阶段

| 阶段 | 状态 | 交付 |
|---|---|---|
| 阶段 K / L | ✅ 代码验收完成，**待部署** | 30080 网关 + 日志/统计/诊断/设置四页 |
| **本阶段 M** | ⬜ | Kilo 虚拟模型（命名组合 + 三策略） |
| 部署窗口（K+L+M 一起） | ⬜ | 需停机授权 |

---

## File Structure（预期变更）

| 文件 | 动作 | 职责 |
|---|---|---|
| `core/manager/kilo_virtual.go` | 新建 | `VirtualModel` 结构、独立 JSON 读写（原子写）、CRUD、名称校验、轮询游标 |
| `core/manager/kilo_virtual_race.go` | 新建 | 三策略选路：`fallback` / `round_robin` / `race`（含 SSE 取消、只回胜出者） |
| `core/manager/kilo_gateway.go` | 修改 | `handleChat` 抽 `forwardOne()`；命中虚拟名走选路；`/v1/models` 追加虚拟项 |
| `core/manager/config.go` | 修改 | 无需新增配置键（独立 JSON）；`ConfigView` 可不加字段 |
| `main.go` | 修改 | 新增 `/api/admin/kilo/virtual`（GET/POST/DELETE）管理端点 |
| `src/lib/api.ts` | 修改 | `KiloVirtualModel` 类型 + 三个 API 方法 |
| `src/pages/KiloPage.tsx` | 修改 | 「虚拟模型」卡片：列表/新建/编辑/删除、候选多选、策略、启停、race 代价提示 |
| `core/manager/kilo_virtual_test.go` | 新建 | 配置 CRUD/校验/轮询/三策略（含假上游竞速与 SSE 取消）单测 |
| `docs/issue-log/2026-11-12.md`、`OPEN.md` | 修改 | 阶段 M 记录 |
| 本计划 | 修改 | 验收表 / 审查表 / 状态回填 |

---

## 配置或 API 契约

```jsonc
// <dataDir>/runtime/kilo/virtual_models.json（独立文件，原子整表保存）
{
  "version": 1,
  "models": [
    {
      "name": "fast-code",                 // 客户端用 kilo/fast-code 或 fast-code 请求
      "models": ["nvidia/nemotron-3.5-lightning:free", "cohere/north-mini-code:free"],
      "strategy": "fallback",              // fallback | round_robin | race
      "enabled": true
    }
  ]
}
```

```jsonc
// GET /api/admin/kilo/virtual → { models: [...] }
// POST /api/admin/kilo/virtual  body { name, models, strategy, enabled }（整表 upsert）
// DELETE /api/admin/kilo/virtual?name=xxx
// 名称规则：^[a-zA-Z0-9_-]{1,48}$，不可与上游真实模型 ID 冲突（命中真实 ID 时以真实模型优先）
```

```jsonc
// GET /v1/models（30080）追加虚拟项
{ "id": "fast-code", "object": "model", "owned_by": "kilo-virtual" }
// 客户端 POST /v1/chat/completions { "model": "kilo/fast-code", ... }
// 响应 model = 胜出上游真实 model（原样透传，不写回虚拟名）
```

---

## Task 1：后端虚拟模型配置层

**Files:** `core/manager/kilo_virtual.go`、`kilo_virtual_test.go`

**行为:**
- `VirtualModel{Name, Models []string, Strategy, Enabled}`；`VirtualModelStore` 读写独立 JSON（路径 `<RuntimeDir>/kilo/virtual_models.json`），`sync.RWMutex` 保护、`writeFileAtomic` 原子写。
- CRUD：`List()` / `Upsert(vm)` / `Delete(name)` / `Get(name)`；`Upsert` 校验：名称正则、候选非空、去重、策略合法、禁止虚拟名互指、禁止候选含 `kilo/` 前缀以外的自引用。
- 轮询游标：`round_robin` 用内存 map（name→idx），`atomic.AddUint64` 无锁；改名/改候选时重置（`resetComboRotation` 抄 9router 思路）。
- 引用现存 `core/manager` 的 `writeFileAtomic`（若无则本文件自实现，标准库 `os.WriteFile` + rename）。

**Steps:** 1. 先写失败用例（CRUD/校验/轮询/并发安全）→ 实现到 PASS。2. `go test ./core/manager/ -run KiloVirtual -race` 全绿。3. Commit：`✨ feat(kilo): 虚拟模型配置层`

---

## Task 2：后端选路（三策略）

**Files:** `core/manager/kilo_virtual_race.go`、`kilo_gateway.go`

**行为:**
- 抽 `forwardOne(ctx, upBody, modelID, stream, w) (winnerActual string, status int, err error)`：把现有 `handleChat` 的「建请求 → client.Do → 透传响应头/body → 观测」搬进去，**不改变原样透传语义**。
- `fallback`：按候选顺序调 `forwardOne`，2xx 立即返回；失败（非 2xx / 传输错）继续下一个；全败回最后一个响应（不洗成 502）。
- `round_robin`：轮换起点后按 `fallback`。
- `race`：`context.WithCancel` 派生 N 个子 ctx，并发 `forwardOne`；**首个成功拿到响应头且 2xx 的胜出**：需要一个「只让胜出者写 w」的仲裁（`sync.Once` + buffer），其余 `cancel()`。**非流式**等完整 body 判定；**流式**等首个 SSE `data:` 且带有效内容再判胜出（避免空头误判）。失败候选自动退出，全败返回最后一个错误。
- 竞速取消：`defer cancel()` 关掉未胜出请求；响应体 `io.Copy(io.Discard, ...)` 排空后关闭，防连接泄漏。
- 观测账本：`Requested=虚拟名`、`Actual=胜出真实 model`；`race` 在 `ErrMsg` 或独立字段记候选数（不新增必填字段，用现有 `KiloCallRecord` 的可选备注）。

**Steps:** 1. 假上游单测覆盖三策略 + 竞速取消 + SSE + 全败。2. `go test ./core/manager/ -run 'Kilo|Virtual' -race` 全绿。3. Commit：`✨ feat(kilo): 虚拟模型三策略选路`

---

## Task 3：/v1/models + 管理端点

**Files:** `kilo_gateway.go`、`main.go`、`src/lib/api.ts`

**行为:** `GET /v1/models` 在真实模型后追加 `enabled=true` 的虚拟项（`owned_by: "kilo-virtual"`）；`api.kiloVirtualList/Upsert/Delete`；`main.go` 三个 handler 挂 `requireAuth`。**Steps:** `go build`/`tsc -b`=0；curl 复核。Commit。

---

## Task 4：KiloPage 虚拟模型管理 UI

**Files:** `src/pages/KiloPage.tsx`

**行为:** 新增「虚拟模型」卡片：列表（名称/策略/候选数/启停）、新建/编辑表单（名称输入 + 候选多选从 `models` 勾 + 策略下拉 + 启用）、删除二次确认；`race` 选中时显示红字代价提示（N 倍上游调用/提示词复制/付费档 N 倍计费）；展示"用 `kilo/<name>` 调用"的复制按钮。不引入新 UI 依赖。

**Steps:** `npx tsc -b`=0；`npm run build`=0；内置浏览器点一遍增删改。Commit。

---

## Task 5：单测 + 全量回归 + dev 端到端

- `go test ./core/manager/ -run 'Kilo|Virtual' -count=1 -race` 全 PASS；整包无新增失败（4 条时间炸弹豁免）。
- `go build ./...`=0、`go vet . ./core/manager/`=0、`gofmt -l core/manager/kilo_*.go` 无输出、`npx tsc -b`=0、`npm run build`=0。
- 重编 core + dist + 同步 dev bin/dist + 重启 tauri:dev；**三策略真实上游端到端**：创建两个虚拟模型（`fallback` 与 `race`），各发一次 `max_tokens>=400` 请求，验证 200、响应 `model` 为胜出真实模型、日志页 `vendor=kilo` 记录 `Requested=虚拟名 / Actual=真实模型`；`round_robin` 连发 3 次验证起点轮换。
- 独立性：确认 50080 / 40080 停掉不影响虚拟模型（V-7 语义延续）。

---

## Task 6：文档与回填

`docs/issue-log/2026-11-12.md` 追加阶段 M；`OPEN.md` 更新；本计划验收表/审查表/状态回填；阶段 K 计划 Global Constraints 8 加一句「阶段 M 在 30080 内部实现虚拟模型，不接入 50080 auto」。

---

## 代码审查（阶段级环节）

**审查方：** 独立角色子代理（非实施者）

| 审查项 | 结论 | 问题清单 |
|---|---|---|
| 独立性红线（零 main 包 / 50080 / auto_model） | | |
| 竞速正确性（只回胜出者 / 取消其余 / SSE 首个 data 判定 / 连接不泄漏） | | |
| 原样透传与警示头（虚拟路径不洗状态码、不改 model） | | |
| free_only 边界（虚拟模型不绕过、付费候选需显式关） | | |
| 配置安全（原子写、名称校验、禁止自引用、无密钥） | | |
| 测试有效性（三策略真断言、无永真用例） | | |

**结论：** ⬜

---

## 验收标准总表

| # | 标准 | 通过条件 | 责任人 |
|---|---|---|---|
| M-1 | 构建/静态 | `go build ./...`=0；`go vet . ./core/manager/`=0；`gofmt -l core/manager/kilo_*.go` 无输出；`npx tsc -b`=0；`npm run build`=0 | 自动化 |
| M-2 | 单测 | `go test ./core/manager/ -run 'Kilo\|Virtual' -race` 全 PASS；整包无新增失败 | 自动化 |
| M-3 | 配置 | CRUD/校验/轮询/并发安全单测；重启后虚拟模型仍在（独立 JSON 持久化） | 自动化 |
| M-4 | 三策略 | 假上游：`fallback` 失败换下一个、`round_robin` 轮换、`race` 只回胜出者且取消其余；全败返回最后一个错误不洗 502 | 自动化 |
| M-5 | 端到端 | dev 真实上游：两个虚拟模型各 1 次 200、响应 `model` 为真实胜出模型；日志页记 `Requested=虚拟名/Actual=真实模型`；`round_robin` 3 次轮换 | 浏览器 + curl |
| M-6 | /v1/models | 虚拟项出现且 `owned_by=kilo-virtual`；禁用的不出现 | curl |
| M-7 | UI | 增删改/候选多选/策略/`race` 代价提示/复制调用名 全部可操作 | 内置浏览器 |
| M-8 | 红线 | 无密钥入库；生产 exe/dist 未替换；隔离端口清场；生产 40000/40080/50080 在听；K8 守卫覆盖新文件 | 自动化 + 主会话 |
| M-9 | 文档 | issue-log / OPEN.md / 计划回填 / 阶段 K 交叉引用 | 主会话 |
| M-10 | 独立审查 | 6 面结论 + 阻断项清单，不通过项返工复验 | 子代理 |

---

## 风险与降级

| 风险 | 缓解 |
|---|---|
| `race` 把提示词复制 N 份给会训练的上游 + 付费档 N 倍计费 | 默认 `fallback`；`race` 必须 UI 显式开启并显示红字代价；候选含付费档时额外提示 |
| 竞速「先发响应头但内容空/失败」误判胜出 | 非流式等完整 2xx body；流式等首个有效 SSE `data:` chunk；全败回退最后一个错误 |
| 竞速未胜出请求泄漏连接 | `context.WithCancel` + `defer cancel()` + 排空 body + `Close`；单测断言无 goroutine 泄漏 |
| 虚拟名与上游真实模型 ID 冲突 | 真实模型优先（先查真实目录再查虚拟）；`Upsert` 拒绝与真实 ID 同名 |
| `free_only` 被虚拟模型绕过 | 每个实际转发候选都过 `isModelBlockedByFreeOnly`；关掉开关才能用付费候选 |
| 独立 JSON 与 config.json 双写不一致 | 虚拟模型只写独立文件，不进 config；原子整表保存 |
| 阶段 L 的同步落盘在竞速下放大 | 竞速只记 1 条胜出观测（不记每个候选），避免 N 倍写 |

---

## 残留手工验收清单

1. **部署**：K+L+M 代码上生产需停机窗口（人类授权）。
2. **`race` 的真实代价评估**：建议先用免费档小样本试，确认可接受再用于含付费档的组合。
3. 阶段 K/L 遗留（V-6b、B5、时间炸弹、累计统计 vendor 维度、同步落盘偏离）按「阶段开头遗留」如实带入。
