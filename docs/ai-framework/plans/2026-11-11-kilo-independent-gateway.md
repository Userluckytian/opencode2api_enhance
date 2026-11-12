# 阶段 K：Kilo 独立菜单 + 30080 独立出口（与一切解耦）

> **状态：** ✅ 代码/测试/验收收口完成（2026-11-12），**待部署**（生产 exe/dist 未替换，需停机窗口）
> **交付分支：** `feat/phase-k-kilo-gateway`，提交 `a32ff36` / `2c857c8` / `b1f6ad2` + 本轮收口提交（未 push）
> **For agentic workers:** 按 Task 顺序执行；每 Task 测完再进下一 Task。验收认证据，禁止"应该能过"。
> **元规范：** `docs/ai-framework/phased-plan-driven.md`；仓库约定 `AGENTS.md`
> **验收结论：** V-1~V-10 见 §验收标准总表（逐条附命令与输出摘要）；独立审查见 §代码审查；完整证据在 `docs/issue-log/2026-11-11.md` #4 / `docs/issue-log/2026-11-12.md`。

**Goal:** Kilo 以**独立菜单 + 独立端口 30080** 对外服务，链路直通 `https://api.kilo.ai`，**不经过 50080、不读 providers[]、不与 OpenCode/引擎/其它供应商共享任何运行路径**；页面默认只勾免费模型，并**显著标注"提示词可能被上游用于训练"**。

**Architecture:** manager 进程（40000 同进程）内新增一个独立的 `kiloGateway` HTTP 监听器：`/v1/chat/completions`、`/v1/models`、`/healthz`，自带上游客户端（`http.Client` + 自己的目录缓存 + 自己的 key 槽位 `Bearer anonymous`）。前端新增 `KiloPage`，导航**第一位**。协议转换复用 `core/protocol`（纯转换函数，无供应商耦合）。

**Tech Stack:** Go 1.22 标准库（`net/http`、`encoding/json`）；React 19 + TS + Tailwind（与现有面板同款）。

**实施档位：** 全能
**子代理：** 启用（实施 + 独立审查；模型待用户指定，本会话白名单见文末）

---

## 前置阅读（必须）

| 优先级 | 文件 |
|--------|------|
| P0 | `docs/ai-framework/phased-plan-driven.md`、本文件 |
| P0 | `docs/opencode-channel-research-2026-11-11/top5-98-kilo-mounted.md`（上游实测数据、四条硬限制、静默换模型） |
| P0 | `core/manager/freetier_engine.go`（托管层写法样板：状态结构体 / 原子写盘 / 看门狗语义） |
| P1 | `AGENTS.md`、`coding-standards.md`、`docs/issue-log/OPEN.md` |

**仓库路径：** `D:\AI_Projects\opencode2api_guide\opencode2api_enhance`
**基线分支：** 从 `main`（tip `2238659`，本地领先 origin 1 提交）拉 `feat/phase-k-kilo-gateway`

---

## Global Constraints（冲突时以本节为准）

1. **独立性红线（本阶段的"功能"本身就是它）**：30080 的调用链里**不得出现** `globalAgg` / `chatRouterVar` / `vendors/custom` / `ProviderService` / 50080 端口 / 40080 引擎。允许复用的只有：`core/protocol`（纯转换）、`core/manager` 的配置读写与 `effectiveGatewayKey`、`httpGetJSON` 一类的裸 HTTP 助手。
   - 判据：拔掉 provider-service（50080）进程后，30080 必须照常 200（验收 V-7）。
2. **摘除 providers[] 里的 `kilo` 条目**（今天 15:54 我挂的那个），否则同一上游两条链路并存 = 用户会看到重复模型，也违背"不要和别人有任何关联"。摘除**必须走整表保存 API**（`/api/admin/custom-providers/save`），且**不得用 PowerShell 读改写**（PS 5.1 会把中文源名解码成乱码并回写污染配置）——用 Go stdlib 工具或面板操作。
3. 训练警示是**需求本体**，不是可选装饰：页面横幅 + 模型行标记 + 响应头三处都要有（V-4/V-5）。
4. **密钥不进 git**；`Bearer anonymous` 是上游公开匿名档，可入代码，但要注释写明来源。
5. **不重建、不替换生产 exe**（被运行中进程锁定，属已知限制）。本阶段交付 = 代码 + 测试 + 面板 dist 构建产物；**部署动作单独等用户授权**。
6. 30080 只绑 `127.0.0.1`；鉴权沿用统一网关密钥（`effectiveGatewayKey`），与其它入口同款语义。
7. **Git**：小步 commit，规范 `<gitmoji><type>(<scope>): 中文描述`；**默认不 push**。
8. **YAGNI**：本阶段不做多 key 池、不做配额账本、不做 failover 到别的源、不做 `auto` 候选接入（那正是今天登记的 🔴 安全缺口，独立入口天然不共享 `Free:true` 候选集，本阶段把它**保持为不共享**即可）。
   > 11-12 后续交叉引用：**阶段 M**（`2026-11-12-kilo-virtual-models.md`）在 **30080 网关内部**实现了命名虚拟模型 + 三策略选路，**仍不接入 50080 的 auto_model**，独立性红线未被打破。

---

## 阶段开头：上阶段遗留

| 上阶段遗留项 | 来源 | 未通过原因 | 状态 |
|-------------|------|-----------|------|
| Kilo 静默换模型未回写真实 `model` | `top5-98-kilo-mounted.md` §6-2 | 当时挂在 providers[]，无自有展示面 | ✅ **已处理**：网关 `handleChat` 原样透传上游 `model`（K2 单测 + 真上游实测 `model=nvidia/nemotron-3.5-lightning:free`）；Kilo 页「实际模型」列由试跑回填。**剩余**：静默换模型写进日志/调用分析未做（登记为 `2026-11-11.md` #4 遗留 6） |
| auto 候选会把会训练的源选进来 | `OPEN.md` 🔴 风险条目 | `custom.go:333` 对所有 custom 源硬编码 `Free:true` | ⚠️ **Kilo 风险源头已移除，通用缺陷仍在**：摘除 `providers[].kilo` 后 Kilo 不再进 `visibleFreeModels`（V-6 按「Kilo 去关联」判据通过）；但 `custom.go:332` 硬编码 `Free:true` + `auto_model.go:266` 选候选的通用缺陷未修，任何带 `data_policy_note` 的 custom 源开 `auto` 仍可能被选中 ⇒ 另立待办，见 `docs/issue-log/2026-11-12.md` #2 / `OPEN.md` 11-12 行。**不伪装成全绿** |

---

## 与前后阶段

| 阶段 | 状态 | 交付 |
|------|------|------|
| 通道稳定性调研（11-11） | ✅ | `99-VERDICT.md` + top5 系列；止血 + v1.4.7 升级已完成 |
| **本阶段 K** | ✅ 代码/验收收口完成，**待部署** | 30080 独立网关 + 独立菜单 + V-1~V-10 证据；部署需停机窗口 |
| 下阶段 P0（托管层修复） | ⬜ | 孤儿 pid 认领 / 观测优先于配置 / 引擎 reload / 漂移可视化 —— **勿塞进本阶段** |

---

## File Structure（预期变更）

| 文件 | 动作 | 职责 |
|------|------|------|
| `core/manager/kilo_gateway.go` | 新建 | 独立监听器：状态结构体、Start/Stop、上游客户端、目录缓存、handler |
| ~~`core/manager/kilo_models.go`~~ | 不建（YAGNI，独立审查 B10 订正） | 目录缓存/过滤/解析仅约 120 行，按 YAGNI 并入 `kilo_gateway.go`，不单独拆文件；V-1 的 `gofmt -l core/manager/kilo_*.go` 仍覆盖 |
| `core/manager/kilo_gateway_test.go`、`kilo_live_test.go` | 新建（11-12 `git add -f` 入库） | 契约单测 K1-K8 + B 系列回归断言 / 真上游端到端（默认 Skip，`KILO_LIVE=1` 开启） |
| `core/manager/config.go` | 修改 | 新增 `kilo_enabled`/`kilo_port`/`kilo_free_only`/`kilo_proxy` 四字段 + ConfigGet/Set 分支 + ConfigView 下发 |
| `main.go` | 修改 | manager 模式启动/关闭 Kilo 网关；`registerHTTPRoutes` 加 `/api/admin/kilo/{status,models,toggle}` |
| `src/pages/KiloPage.tsx` | 新建 | 独立菜单页：警示横幅、开关、模型列表（含"实际模型"列）、端点/密钥复制、试跑 |
| `src/App.tsx` | 修改 | `Tab` 加 `'kilo'`；`NAV` **首位**插 `{ id:'kilo', label:'Kilo', icon }` |
| `src/lib/api.ts` | 修改 | `KiloStatus` / `KiloModelsView` 类型 + 三个 API 方法 |
| `core/manager/kilo_gateway_test.go` | 新建 | 单测：免费过滤、静默换模型透传、鉴权、free_only 开关、端口配置 |
| 删除：`%APPDATA%\...\config.json` 内 providers[].kilo | 运维动作 | 见约束 2 |

---

## 配置契约

```jsonc
// manager config.json 新增键（全部 omitempty，缺省即默认行为）
{
  "kilo_enabled": true,        // 默认 true：与 engine_enabled 同款「未设置=开」语义（用 *bool）
  "kilo_port": 30080,          // 0 = 默认 30080；env OPCODE2API_KILO_PORT 优先
  "kilo_free_only": true,      // 默认 true（用户明确要求默认只勾免费模型）
  "kilo_proxy": ""             // 空 = 直连（实测 Kilo 无需代理）；可填 http/socks5
}
```

```jsonc
// GET /api/admin/kilo/status → KiloStatus
{
  "enabled": true, "running": true, "port": 30080,
  "address": "http://127.0.0.1:30080/v1",
  "api_key": "sk-unified-local",
  "free_only": true,
  "model_total": 386, "model_shown": 11,
  "may_train": true,                      // 上游全量 mayTrainOnYourPrompts=true
  "last_refresh": "...", "error": ""
}
```

---

## Task 1：配置字段与生效语义（manager 侧，无 UI）

**Files:** `core/manager/config.go`

**行为:** 四个新键可读可写；`kilo_enabled` 用 `*bool`（nil = 默认开）与 `engineEnabled(cfg)` 同款助手；`kilo_port` 走 env > config > 默认 30080；ConfigView 下发**生效值**而非原始值。

> 11-12 订正（独立审查 B10）：实现为**混合** —— `KiloEnabled`/`KiloFreeOnly`/`KiloPortEffective` 是生效值，`KiloPort` 是原始值、`KiloProxy` 原始（沿用既有 raw+effective 双字段模式）。故准确措辞是「布尔下发效值、端口沿用双字段」，本行「下发生效值」按字面不完全成立，但不影响行为（K7 PASS）。

**Steps:**
1. 加字段 + `kiloEnabled(cfg)/kiloFreeOnly(cfg)/managerKiloPort()` 助手 + ConfigGet/ConfigSet 分支（`parsePort`/`parseBool` 复用现成的）。
2. 跑：`go build ./...`；`go vet ./core/manager/` → 期望 exit 0
3. 跑：`gofmt -l core/manager/config.go` → 期望无输出

> 11-12 订正（独立审查 B10）：**原计划「HEAD 已 gofmt-clean」与事实不符** —— `git show main:config.go | gofmt -d` 与基线 `2238659` 均有同类差异（ConfigView 字段对齐/缺空行），属既有格式债；工作区全文件 CRLF 又会放大 gofmt 报告。按「不扩大 diff」不修，V-1 只约束 `kilo_*.go` 干净。
4. Commit：`✨ feat(kilo): 新增独立网关配置项`

---

## Task 2：Kilo 独立网关本体

**Files:** `core/manager/kilo_gateway.go`、`core/manager/kilo_models.go`

**行为:**
- `StartKiloGateway()`：`http.Server` 绑 `127.0.0.1:<port>`，路由
  - `POST /v1/chat/completions`：读 body → 提取 `model` → 剥可选前缀 `kilo/` → 转发上游 `<base>/chat/completions`，头 `Authorization: Bearer anonymous`（来源：`refs/Opencode2api` 调研 + 今日实测，上游无账号即放行）→ **响应原样透传**（含上游真实 `model` 字段，不改写）
  - `GET /v1/models`：目录缓存（60s 后台刷新 + 10s 节流，抄 `providerservice.go:63-68` 的缓存语义，**代码自持不共享**）；`kilo_free_only=true` 时只回 `isFree==true || id 含 ":free"` 的条目
  - `GET /healthz`：`{status, models_shown, may_train, last_refresh}`
- 上游 4xx/5xx **原样透传状态码与错误体**，不包装成 502（今日教训：上游把 403 洗成 502 会让人误判）。
- 响应统一加头 `X-Kilo-Data-Policy: prompts-may-be-used-for-training`。
- 生命周期：随 manager 起停；端口被占 → 状态记 `failed` + 错误，**不拖垮 manager 主监听**（对齐 `freetier_engine.go:159-165` 的后台启动教训：别同步等长任务）。

**Steps:**
1. 实现 + 单测（Task 4）先写失败用例再实现（红-绿）。
2. 跑：`go test ./core/manager/ -run Kilo -count=1 -v` → 期望全 PASS
3. 跑：`go build ./...`、`go vet ./core/manager/`、`gofmt -l core/manager/kilo_*.go` → 全 0
4. Commit：`✨ feat(kilo): 30080 独立 Kilo 网关`

---

## Task 3：接入 main 生命周期 + 管理端点

**Files:** `main.go`、`core/manager/kilo_gateway.go`

**行为:** manager 模式（非 provider-service 模式）下随进程启动网关、`defer` 关闭；新增 `GET /api/admin/kilo/status`、`GET /api/admin/kilo/models`、`POST /api/admin/kilo/toggle`（enabled / free_only 两个开关，写盘 + 生效，不需重启）。路由挂 `requireAuth`（面板侧）而非 `apiKeyAuthMiddleware`。

**Steps:**
1. 接线。
2. 跑：`go build ./...` → 0
3. 手工冒烟（临时目录 + 独立端口，**不碰生产 40000/40080/50080**）：
   ```
   OPCODE2API_KILO_PORT=30081 <新构建的 exe> -port 40999 -config <temp config>
   curl 127.0.0.1:30081/healthz
   curl -H 'Authorization: Bearer sk-unified-local' -H 'Content-Type: application/json' ^
        -d '{"model":"nvidia/nemotron-3.5-lightning:free","max_tokens":400,"messages":[{"role":"user","content":"say KILO-OK"}]}' ^
        127.0.0.1:30081/v1/chat/completions
   ```
   期望：healthz 200；completions 200 且 `content` 含 `KILO-OK`；响应头有 `X-Kilo-Data-Policy`。
   ⚠️ 探测务必 `max_tokens>=400`：这批模型多为推理型，小预算会出现 `content:null + finish_reason:length`（今日实测踩坑，会被误判为失败）。
4. 冒烟完杀掉临时进程、删除临时目录，端口复查 `Get-NetTCPConnection -LocalPort 30081` 为空。
5. Commit：`✨ feat(kilo): 网关接入管理生命周期与状态端点`

---

## Task 4：单测（红-绿顺序，覆盖以下契约）

**Files:** `core/manager/kilo_gateway_test.go`

| # | 用例 | 期望 |
|---|------|------|
| K1 | 免费过滤 | `isFree=true/false` 混合目录 + `free_only=true` → 只回免费；`free_only=false` → 全回 |
| K2 | 静默换模型透传 | 假上游把 `model` 改成别的值 → 客户端收到的 body `model` 是**上游真实值**（不许洗成请求名） |
| K3 | 鉴权 | 无/错 `Authorization` → 401；正确 key → 放行 |
| K4 | 上游错误透传 | 假上游 403 + JSON 错误体 → 网关回 403 且 body 原样（**不得变 502**） |
| K5 | 警示头 | 任一 2xx 响应含 `X-Kilo-Data-Policy` |
| K6 | 前缀剥离 | 请求 `kilo/nvidia/...` 与裸 `nvidia/...` 命中同一模型 |
| K7 | 端口/开关 | `kilo_port` env>config>默认 30080 三源优先级；`kilo_enabled=false` 时监听不起 |
| K8 | 独立性（红线） | 单测里 `import` 断言：kilo 文件**不得**引用 `vendors/`、`core/router`、`aggregator`（可用 `go list -deps` 或 grep 守卫） |

**Steps:**
1. 先写 K1–K8（全 FAIL），再实现到全 PASS。
2. 跑：`go test ./core/manager/ -run Kilo -count=1 -race` → 全绿
3. 跑：`go test ./core/manager/ ./vendors/custom/ -count=1` → **不得新增失败**
   （已知既有失败：`callmetric_engine_test.go` 4 条时间炸弹，见 `OPEN.md` 2026-11-10 #5，不属本阶段）
4. Commit：`✅ test(kilo): 独立网关契约单测 K1-K8`

---

## Task 5：前端独立菜单页

**Files:** `src/pages/KiloPage.tsx`、`src/App.tsx`、`src/lib/api.ts`

**行为:**
- `NAV` **首位**：`{ id: 'kilo', label: 'Kilo', icon: <lucide 图标> }`；默认 tab 仍为 `opencode`（**不抢默认落地页**，避免用户一进来就撞警示源；如要改默认由用户另行决定）。
- 页面顶部**玫瑰色警示横幅**（常驻、不可关闭）：「⚠️ Kilo 免费档的上游服务条款声明：请求内容可能被用于模型训练/记录，请勿提交个人或机密数据」+ 依据来源小字。
- 卡片：运行状态、端口、`/v1` 地址与 key 一键复制、`enabled`/`free_only` 两个开关（默认勾选只免费）、模型表（列：模型 ID / 是否支持工具 / `mayTrain` / 最近一次**实际**返回模型）。
- 样式对齐现有面板（`bg-white rounded-2xl border`、`text-[15px]`、`text-zinc-500` 体系），**不引入新 UI 依赖**。
- 轮询 5s，抄 `OpenCodePage.tsx:225-229` 的 `aliveRef` + `reloadRef` 防泄漏写法。

**Steps:**
1. 实现。
2. 跑：`npx tsc -b` → exit 0
3. 跑：`npm run build` → exit 0（产出 `dist/`；**只构建不部署**，约束 5）
4. 面板自检：`npx vite preview`（临时端口，用完杀）截图存 `docs/design/screenshots-20261111/kilo-page.png`
5. Commit：`✨ feat(kilo): 独立菜单页与训练警示`

---

## Task 6：摘除 providers[] 的 kilo 条目（去关联）

**行为:** 通过管理 API 整表保存摘除今天挂载的 `id=kilo` 条目；保留其它 19 个源逐字段不变。

**Steps:**
1. **备份** `%APPDATA%\opencode2api-manager\config.json` → `%TEMP%`。
2. 用 **Go stdlib 一次性工具**（禁 PowerShell 读改写，理由见约束 2）：GET `/api/admin/custom-providers` → 过滤掉 `kilo` → POST save。
3. 校验：
   - `GET 127.0.0.1:50080/v1/models` → **不再出现任何 `kilo/*`**；模型数 `31 → 20`
   - `GET /api/admin/custom-providers` → 19 个源，`nv` 名字仍为 `英伟达`、`yn` 仍为 `越南-1亿-走代理`（**用 Go 客户端读，别用 PowerShell**，否则会把正确 UTF-8 看成假乱码——今日已踩）
4. Commit（若产生文档/脚本变更）：`♻️ refactor(kilo): 免费源改由独立网关承载，摘除 providers 条目`

---

## Task 7：文档与日志（AGENTS.md 强制第 6 条）

- `docs/issue-log/2026-11-11.md`：#3 状态改为「已由独立网关承载，providers 条目摘除」，新增 #4 记录本阶段（描述/分析/修改结果/验证证据/遗留）。
- `docs/issue-log/OPEN.md`：更新对应行（🔴 auto 候选风险应随 providers 条目摘除而**关闭**，写明证据）。
- `docs/opencode-channel-research-2026-11-11/top5-98-kilo-mounted.md`：加「⚠️ 已被阶段 K 取代」顶注，保留历史。

---

## 代码审查（阶段级环节，验收前）

**审查方：** 独立角色子代理（非实施者；第一次因上游额度 14018 中断，已重跑）+ 主会话复核

**审查面：** 独立性红线（约束 1）是否真守住 / 并发与生命周期（goroutine 泄漏、端口重绑、`defer` 关闭）/ 上游错误是否被洗 / 警示是否可能被 UI 绕过 / 密钥与 PII 入日志 / 测试是否真断言（防"永真用例"）/ 与既有面板风格一致性 / `gofmt` 与 `coding-standards.md`

**主会话复核（11-12，基于实跑与 `文件:行号`）：**

| 审查项 | 结论（✅/⚠️/❌） | 问题清单（证据） |
|--------|------------------|----------|
| 独立性红线 | ✅ | `kilo_gateway.go` 文件头与 import 仅标准库（`bytes/json/fmt/io/slog/net/http/url/os/strings/sync/time`），grep 全文件无 `globalAgg`/`chatRouterVar`/`vendors`/`ProviderService`/`50080`/`40080` 调用；`main.go:169-178` 接线仅 manager 模式（provider-service 模式在 `:128` 提前 return）；`K8` 单测守卫 + V-7 端到端停 50090 后 30081 全 200 双重证明 |
| 并发与生命周期 | ✅ | `Start` 幂等（`state==ready` 直接返回）、`Stop` 幂等；`serveOn` 的 `srv.Serve` 在 goroutine 内、`Stop` 用 `srv.Close()`；`Watchdog(30s)` 按「观测到的 state」纠偏（不读配置缓存，避免 L2 死锁）；`KiloGateway()` 用 `depMu` 懒建单例；目录首拉与刷新各自 goroutine |
| 错误透传语义 | ✅ | `handleChat` 传输失败才 `504`（`kilo upstream unreachable`），上游 4xx/5xx 走 `resp.StatusCode` 原样 + body 原样（K4 假上游 403 + JSON 错误体断言通过）；`requireKey` 401；`X-Kilo-Data-Policy` 在 `handleChat`/`handleModels`/401 分支三处均设置（K5 PASS） |
| 警示不可绕过 | ✅ | `KiloPage.tsx:45-64` 横幅常驻、无 close/dismiss 入口；`:197` 渲染；`may_train` 由 `Status()` 驱动且恒不下发 false（catalog 非空即 true）；`free_only` 默认 true（V-8），`/v1/models` 只回 12 个免费档（V-4/V-7 实测）；响应头是第三层机器可读保障 |
| 测试有效性 | ✅ | K1-K8 + 3 补充用例在 `-race -v` 下逐条 PASS；K2 断言上游换 model 后 body 不被洗、K4 断言 403 不变 502、K4b 断言请求真的到达假上游（防「永真」）、K8 做独立性 grep 守卫；`kilo_live_test.go` 真上游 `-run TestKiloLiveEndToEnd` PASS 且需 `KILO_LIVE=1` 显式开启（不会误跑） |
| 风格与规范 | ⚠️ | `gofmt -l core/manager/kilo_*.go` 干净；`main.go` struct tag 已修正；`config.go` 的 gofmt 报告经 `git show main:config.go` 对拍为**基线既有**（全文件 CRLF 工作区行尾），按计划约束不扩 diff；`go vet ./...`=0；提交信息符合 AGENTS.md gitmoji 中文规范；`Bearer anonymous` 是上游公开匿名档且代码有来源注释，测试 key 仅在临时目录，**无密钥/PII 入日志** |

**主会话复核结论（事后修正）：** ⚠️ **主会话六面复核有漏项** —— 错误地把「并发与生命周期 / 错误透传 / 测试有效性」判为全 ✅，而独立审查在这些面找到了真实缺陷（下表 B1/B2/B6/B7/B9 与 B3）。本阶段以**独立审查为准**，主会话复核表保留作「初筛记录」，不作为验收依据。教训：自验必须交叉检查，尤其是「无鉴权端点回传了什么」这类契约越界。

**独立子代理结论（11-12 回传）：** ❌ **不通过** —— 独立性红线守住、构建/vet/本地单测全绿，但列出 B1–B10，其中 5 项高 severity（B1–B5）。核心阻断：

| 编号 | 级别 | 问题 | 修复状态 |
|---|---|---|---|
| B1 | ❌ 高 | `/healthz` 无鉴权却整包 `Status()`，回传明文 `api_key` | ✅ 已修：`handleHealth` 只回计划 4 字段（status/models_shown/may_train/last_refresh），K5 增泄密断言 |
| B2 | ❌ 高 | `Status():288` 用 `cErr` 无条件覆盖 `lastErr`，端口被占时真实原因被吞 | ✅ 已修：两类错误合并、启动错误优先；新增 `TestKiloB2StatusErrorMerge` |
| B3 | ❌ 高 | 测试被 `.gitignore:57` 忽略、test commit 不含测试 → 干净 checkout 上 V-2 不可复跑 | ✅ 已修：`git add -f` 两文件入库（`7f3d3ff`）；K8 加强为三处守卫 + `go list -deps` |
| B4 | ❌ 高 | V-6 字面 grep 判据不通过、custom 源通用缺陷仍在却写「消解」 | ✅ 已修（措辞）：拆 V-6a（去关联 ✅）/ V-6b（通用机制 ❌ 转下阶段），见验收总表；通用缺陷本身按 Global Constraints 8 不在本阶段修 |
| B5 | ❌ 高 | V-7 生产硬证据与部署均未完成 | ⚠️ **保留**：V-7 在隔离实例完成（等价证明），生产停机实测与部署需人类授权，无法由 AI 单方完成，如实列为残留 |
| B6 | ⚠️ 中 | `/healthz` 缺 `X-Kilo-Data-Policy` | ✅ 已修：补头 + K5 增 `/healthz` 断言 |
| B7 | ⚠️ 中 | `free_only` 只过滤展示、chat 不校验，编付费档 id 即绕过 | ✅ 已修：`isModelBlockedByFreeOnly` 轻量边界（目录已拉取才生效，目录抖动不误伤）+ `TestKiloB7FreeOnlyChatBoundary` |
| B8 | ⚠️ 中 | `MayTrain` 实现 AND 与 `api.ts` 注释 OR 矛盾 | ✅ 已修：改 OR |
| B9 | ⚠️ 中 | Watchdog 无退出条件；并发 Start 可把 ready 覆盖成 failed | ✅ 已修：`startMu` 贯穿 listen+serve；`watchdogStop` channel 随 Stop 关闭；`TestKiloB9ConcurrentStart` |
| B10 | ⚠️ 低 | 计划措辞与交付边界：Task1 称 config.go gofmt-clean 与事实不符、kilo_models.go 未建、504/502 口径不一、ConfigView 混合下发 | ✅ 已修（措辞/文档）：Task1 与 §配置契约 订正；`kilo_models.go` 按 YAGNI 并入 `kilo_gateway.go` 并在 File Structure 说明；504/502 分别对应传输失败/目录不可用，语义不同，保持 |

**修复提交：** `7f3d3ff 🐛 fix(kilo): 独立审查缺陷修复并让测试入库`。改码后回归（11-12 07:51–07:53）：`go build ./...`=0、`go vet ./core/manager/ .`=0、`gofmt -l core/manager/kilo_*.go` 无输出、`go test -run Kilo -race` **15 条全 PASS**、`KILO_LIVE=1` 真上游 PASS、隔离实例 `/healthz` 只回 4 字段且无密钥、B7 付费档 400、V-7 停 50090 前后 30081 全 200、`npx tsc -b`/`npm run build`=0。整包仍只有 4 条既有时间炸弹，无新增失败。

**结论：** ✅ **有条件通过（独立审查缺陷收口后复核）** —— B1/B2/B3/B4/B6/B7/B8/B9/B10 已修并回归；B5（部署 + 生产停机 V-7）按 Global Constraints 第 5 条保留为人类授权项，不表述为「生产已生效」；V-6 按 6a/6b 拆分如实不全绿。

---

## 验收标准总表（2026-11-12 回填，逐条附真实输出摘要）

| # | 标准 | 结果 | 命令 + 输出摘要 |
|---|------|------|----------------|
| V-1 | 构建/静态检查 | ✅ | `go build ./...` exit 0；`go vet ./core/manager/` exit 0（全仓 `go vet ./...` 亦 0）；`gofmt -l core/manager/kilo_*.go` 无输出。`main.go` struct tag 已 gofmt 修正；`config.go` 的 gofmt 报告经 `git show main:config.go` 对拍为**基线既有**（全文件 CRLF 工作区行尾），按计划约束不扩 diff |
| V-2 | 单测 | ✅ | `go test ./core/manager/ -run Kilo -count=1 -race -v` → K1-K8 + 3 补充 + **4 条 B 系列回归**共 15 条 **全 PASS**（11-12 修复后复核）；测试文件已于 `7f3d3ff` `git add -f` 入库，干净 checkout 可复跑（独立审查 B3 已闭环）；`go test ./core/manager/ ./vendors/custom/ -count=1` → 仅既有 4 条 `callmetric_engine_test.go` 时间炸弹 FAIL（`metrics = 0, want 1/2`，OPEN.md 11-10 #5，Task 4 已豁免），`vendors/custom` ok，**无新增失败** |
| V-3 | 前端 | ✅ | `npx tsc -b` exit 0；`npm run build` exit 0（vite 8.2.0，1809 模块，`dist/assets/index-unuVWMqx.js` 449.39 kB，含 Kilo 页） |
| V-4 | 30080 端到端 | ✅ | `KILO_LIVE=1 go test ./core/manager/ -run TestKiloLiveEndToEnd -count=1 -v` → **PASS**：上游目录 12 条、免费 12、`mayTrain=12/12`（上游已从 11 漂移为 12）；真推理 `max_tokens=500` HTTP 200（2.75s）、`content` 含 `KILO-OK`、响应 `model=nvidia/nemotron-3.5-lightning:free` 原样透传、`X-Kilo-Data-Policy` 在位。`/v1/models` free_only=true 只回 12 个免费档 |
| V-5 | 警示可见 | ✅ | 代码复核：`KiloPage.tsx:45-64` 横幅常驻、无 close handler；`kilo_gateway.go` 所有响应加 `X-Kilo-Data-Policy`，**含 `/healthz`（独立审查 B6 修复后）**；K5 增 `/healthz` 泄密/字段断言（B1）。截图 `docs/design/screenshots-20261111/kilo-page.png`（Playwright + Edge，1440×1000）：横幅在位、12 行模型表带「数据去向」列、「只显示免费模型」默认勾选、控制台 0 错误。**注**：`docs/design/` 被 `.gitignore:116` 忽略 ⇒ 截图本地保留、**未入库**（如实登记，不冒称「截图入库」） |
| V-6a | 去关联（Kilo 已从 providers 移除） | ✅ | 运行配置 JSON 解析：20 个源、`has_kilo=false`、无 `kilo_*` 键、中文源名完好；**带网关密钥打生产 `50080/v1/models` → HTTP 200、20 个模型、`kilo/*` 条目为空**（真证据，非 401 兜底）；`autoCandidateModels` 因源已摘除不含 Kilo |
| V-6b | 通用机制（计划字面 grep 判据） | ❌ **转下阶段** | `grep -rn "ParamDataPolicyNote\|DataPolicyNote"` 仍命中 `custom_providers.go` / `registry.go`（通用 `data_policy_note` 能力按设计保留），且 `custom.go:332` 硬编码 `Free:true` + `auto_model.go:266` auto 候选缺陷未修 ⇒ 任何带训练警示的 custom 源开 `auto` 仍可能被选中。按 Global Constraints 8「本阶段保持不共享即可」不在本阶段修；已登记 `2026-11-12.md` #2 / OPEN.md 11-12 行。**独立审查 B4 判定此条不得全绿** |
| V-7 | **独立性证明** | ✅（隔离实例等价） | 完整隔离实例（独立 exe / 数据目录 / 端口 30081+40999+50090+40090，引擎二进制不存在）：显式 `POST /api/admin/provider-service/start` → 50090 `LISTENING pid 30884` → 停前 30081 `/healthz`=200、`/v1/models`=200(12)、chat=200 且带警示头、内容 `KILO-V7-OK` → `POST /api/admin/provider-service/stop` → 50090 无 LISTEN → 停后 30081 `/healthz`=200、`/v1/models`=200(12)、chat=200 且带警示头、内容 `KILO-V7-AFTER-OK`；`/api/admin/kilo/status` 全程 `state=ready`。**未停生产 50080**（无需授权、避免影响线上）；用同一二进制的独立 provider-service 子进程，语义等价。这是「不转发、不依赖」的硬证据 |
| V-8 | 默认值 | ✅ | `go test ./core/manager/ -run TestKiloK7PortAndToggle -count=1 -v` PASS：`kilo_enabled=nil→true`、`kilo_free_only=nil→true`、端口 env>config>默认 30080 三源优先级；隔离实例以 `OPCODE2API_KILO_PORT=30081` 实测 env 生效 |
| V-9 | 红线 | ✅ | `stat`：`bin/opencode2api.exe` 11-06 19:59 / 12844032 字节、生产 `bin/dist/index.html` 11-07 18:20 / 488 字节，**均未替换**；隔离端口 30081/40999/50090/40090/19100/1420 全清场，生产 40000/40080/50080 全程在听；`Bearer anonymous` 是上游公开匿名档（代码注释写明来源），测试用 `sk-kilo-isolated-test` 仅在临时目录，无密钥入库 |
| V-10 | 文档 | ✅ | `docs/issue-log/2026-11-11.md` #4（阶段 K 完整记录）+ 顶部状态汇总；`docs/issue-log/2026-11-12.md`（收口日志 + 通用缺陷待办）；`docs/issue-log/OPEN.md`（过期行改写 + 新增待办）；`top5-98-kilo-mounted.md` 加「⚠️ 已被阶段 K 取代」顶注；本计划 §状态/§阶段开头/§代码审查/§验收总表/§残留手工清单 全部回填 |

**总评（独立审查修复后复核）：** V-1~V-5、V-6a、V-7~V-10 通过；V-6b 按独立审查 B4 显式判 ❌ 并转下阶段（不伪装全绿）。**阶段 K 代码与验收收口完成，唯一未完 = 部署**（需停机窗口，属 Global Constraints 第 5 条有意留下，非缺陷）。

---

## 风险与降级

| 风险 | 缓解 |
|------|------|
| Kilo 上游目录漂移（其默认链今日已 2/7 死） | 网关每次启动 + 后台刷新都实拉 `/models` 按 `isFree` 过滤，**不硬编码模型清单**；`/v1/models` 空结果时回退上次缓存（抄 `providerservice.go` 语义） |
| 免费端点未来要求账号 | 上游返回 401/403 → 状态端点透出 `error`，页面红字显示，不改行为、不重试烧额度 |
| 用户把私有代码误接到 30080 | 警示三处（横幅/模型列/响应头）+ README 段落；文档明确"默认只免费档 = 全部会训练" |
| 与 40080 引擎抢 CPU/连接 | 独立 `http.Client`，`MaxIdleConns` 保守（64）；本阶段不做限流（YAGNI） |
| 端口 30080 被占 | 配置/env 可改；启动失败只标 `failed` 不影响 manager 主服务 |
| 审查发现违反独立性红线 | ❌ 不通过 → 返工，缺陷显式带入下一轮，不允许"下阶段再改" |

---

## 给接手 AI 的完整提示词（零上下文可直接粘贴）

---

你是负责 **opencode2api_enhance** 的实现代理。请**完整执行本阶段**，不要只写方案。

### 基线
- 目录：`D:\AI_Projects\opencode2api_guide\opencode2api_enhance`（Windows，PowerShell）
- 从 `main`（tip `2238659`）创建并切换：`feat/phase-k-kilo-gateway`
- 唯一实施计划：`docs/ai-framework/plans/2026-11-11-kilo-independent-gateway.md`
- 必读：`AGENTS.md`、`coding-standards.md`、`docs/ai-framework/phased-plan-driven.md`、
  `docs/opencode-channel-research-2026-11-11/top5-98-kilo-mounted.md`
- 环境事实：Go 1.22 在 PATH；`npm/npx` 可用；**PowerShell 5.1，读 UTF-8 文件必须显式指定编码**（`[IO.File]::ReadAllText($p,[Text.UTF8Encoding]::new($false))`），否则中文全乱码。

### 做（按 Task 1→7 顺序，每 Task 测过再 commit）
1. Task 1 配置四键（`*bool` 默认开语义，抄 `engineEnabled` 写法）
2. Task 2 `core/manager/kilo_gateway.go` + `kilo_models.go`：**独立** `http.Client` / 目录缓存 / handler
3. Task 3 接入 `main.go` 生命周期 + `/api/admin/kilo/*`，临时端口 30081 手工冒烟后杀净
4. Task 4 单测 K1–K8 **先红后绿**，含 `-race`
5. Task 5 `KiloPage.tsx` + `App.tsx` NAV **首位** + `api.ts`；`tsc -b` 与 `npm run build` 必须 0
6. Task 6 **Go stdlib 工具**摘除 `providers[].kilo`（**禁止 PowerShell 读改写 config.json**），复核 19 源无损、50080 无 `kilo/*`
7. Task 7 issue-log / OPEN.md / top5 顶注三处文档

### 不做
- ❌ 不引用 `globalAgg`/`chatRouterVar`/`vendors/custom`/`ProviderService`/50080/40080 —— 独立性是本阶段的功能本身
- ❌ 不替换/不重启生产 `bin/*.exe` 与 `bin/dist`（部署另行授权）；不停生产 40000/40080/50080
- ❌ 不做多 key 池、配额账本、跨源 failover、auto 候选接入
- ❌ 不 `gofmt -w` 整仓库（`core/manager/config.go` 等文件在 HEAD 已 clean，`registry.go` 的 gofmt 报告是**既有**问题，别顺手改扩大 diff）
- ❌ 不提交密钥；不 `git push`

### 工作方式
1. 开工先跑基线：`go build ./...`、`go test ./core/manager/ -run Kilo`（应报"无此测试"而非编译错）、`npx tsc -b`，确认干净。
2. 证据优先：完成前重跑计划里每条验证命令，把**真实输出**贴进交付说明；禁止"应该能过"。
3. 上游试跑 `max_tokens>=400`（推理型模型小预算会假失败）。
4. 用简体中文汇报；代码标识符保持原样。
5. 实验一律用独立端口（30081/40999）+ 临时目录，结束杀进程并复查端口为空。

### 交卷
分支名、提交列表、V-1～V-10 自评表（每条附命令与输出摘要）、`go test`/`tsc`/`build` 结果、面板截图路径、残留风险与需人类执行项（V-7 停 50080 需授权、部署需停机窗口）。

现在开始：读完计划，从 Task 1 执行到最后。

---

## 残留手工验收清单（2026-11-12 更新）

0. **`npm run tauri:dev` 是免部署的验证/使用路径（用户 11-12 提醒，已实测）**：dev 槽位 44100/44180/44280 + 独立数据目录 + vite 1420，与生产完全隔离；重新 `go build -o bin/opencode2api.exe .` + `npm run build` + `touch src-tauri/src/main.rs` 触发 cargo 重嵌后，`tauri:dev` 会拉起**含阶段 K 的新 core**。实测（设 `OPCODE2API_KILO_PORT=30082` 避免占用）：30082 `/healthz` 只回 4 字段且带警示头、44100 `/api/admin/kilo/status` 返回完整状态、30082 `/v1/models` 12 条、chat 真推理 200 且内容 `KILO-TAURI-DEV-OK`、44100 SPA 资源含 Kilo 页与警示文案；生产 40000/40080/50080 全程未受影响、生产 30080 仍 000。**注意**：①只 `go build` 不重新内嵌会跑旧 core（README 已警告）；②vite 的 `/api` 代理指向 19090（当前被 mihomo 占用），**Tauri 窗口内可用**（同源打到 44100），单独开浏览器访问 1420 需设 `VITE_API_BASE`。这条路径把「验证/日常使用」与「生产部署」解耦，**不替代正式部署**。

1. ~~**V-7 独立性证明**~~ → ✅ **11-12 已在隔离实例完成**（30081/40999/50090/40090，显式起停 provider-service 前后 30081 全 200，见 V-7）。**未停生产 50080**：语义等价且避免影响线上；若用户坚持要在生产做一次停机实测，需另行授权。
2. **部署（唯一未完项）**：新 `opencode2api.exe` 与被锁定的运行中 exe 交换 + `bin/dist/` 替换，需停机窗口（`OPEN.md` 已多次登记该限制）。同窗口可一并上：11-06 WARP 优先通道、宿主 stderr 日志端点 `53f7902`、`data_policy_note` 徽标。部署后复核：30080 `/healthz`、`/v1/models`、`/api/admin/kilo/status`、Kilo 页真实数据。
3. ~~面板 Kilo 页在真实数据下的观感~~ → ✅ **11-12 已截图目视**（`docs/design/screenshots-20261111/kilo-page.png`：横幅显眼、12 行模型表、「数据去向」列清晰、只免费默认勾选；本地保留不入库）。用户如需再目视可直接看该文件。
4. **是否把 Kilo 设为默认落地页** —— 本阶段保守保持 `opencode`，需用户决定（未决）。
5. **P2 免费/付费 lane 是否合并统一入口** —— 需用户拍板（阶段 X 遗留，非本阶段）。
6. **通用缺陷另立阶段**：`custom.go:332` 硬编码 `Free:true` + `auto_model.go:266` auto 候选（见 `2026-11-12.md` #2）；以及 `callmetric_engine_test.go` 4 条时间炸弹、`calllog_engine.go` 死字段映射清理。
7. ~~**测试入库待定**~~ → ✅ **11-12 已闭环**（`7f3d3ff` `git add -f` 两文件；K8 加强；新增 B1/B2/B6/B7/B9 断言；改码后 15 条 `-race` 全 PASS）。
8. **独立审查 B5 保留项**：生产停机实测 V-7 + 部署窗口；B4 的 V-6b 通用缺陷转下阶段（`custom.go:332` / `auto_model.go:266`，见 `2026-11-12.md` #2）。
