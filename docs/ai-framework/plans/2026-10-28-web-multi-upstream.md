# 阶段 Z：web 自助服务多上游（上游 ID 作为 model 前缀路由）

> **状态：** 计划已就绪
> **For agentic workers:** 按 Task 顺序执行；每 Task 测完再进下一 Task。
> **交接提示词**见文末「给接手 AI 的完整提示词」。
> **元规范:** `docs/ai-framework/phased-plan-driven.md`

**Goal:** 让 `web/` 自助领 Key 服务支持配置多个上游，请求按 `model` 第一段的**上游 ID 前缀**路由到对应上游并剥离前缀后转发。
**Architecture:** `Config` 增加 `Upstreams []Upstream`（`ID/Name/BaseURL/APIKey/Enabled`），旧单上游字段保留为迁移来源；新增 `web/upstreams.go` 承载注册表与 `Resolve`；`gateway.go`、`ask.go`、`admin.go`、`main.go` 统一改走注册表。
**Tech Stack:** Go 1.22（`web/` 独立 `package main`，原生 `net/http`）、原生 JS 后台（`web/static/admin.js`）。
**实施档位：** 全能（默认）
**子代理：** 启用

---

## 前置阅读（必须）

| 优先级 | 文件 |
|--------|------|
| P0 | `docs/ai-framework/phased-plan-driven.md` |
| P0 | 本文件 |
| P0 | `docs/superpowers/specs/2026-08-05-web-self-service-design.md`（§非目标需同步修订） |
| P1 | `AGENTS.md`、`CODE_REVIEW.md` |
| P1 | `docs/DEPLOY-WEB.md` |

**仓库路径：** `D:\AI_Projects\opencode2api_guide\opencode2api_enhance`
**基线分支：** `feature/web-self-service`（当前已检出，与 `origin/feature/web-self-service` 同步）

---

## Global Constraints（冲突时以本节为准）

1. **路由规则必须是「白名单匹配」而非「盲切前缀」**：现有真实模型名本身含斜杠
   （`or/minimax/minimax-m3.1-flash:free`、`vibex/openai/gpt-6-astra-saver`），
   因此**只有第一段能精确匹配到某个已配置且启用的上游 ID 时才剥离并路由**；
   匹配不到则视为普通模型名，走默认上游。
2. **默认上游**：`Config.DefaultUpstreamID` 指定；为空时取 `Upstreams` 中第一个 `Enabled` 的。
   不带前缀的请求一律走默认上游，保证存量客户端零改动。
3. **SSRF 防护不得退化**：每个上游的 `base_url` 在写入前都必须过 `ValidateUpstreamBaseURL`；
   白名单仍只由部署方 `config.json` / 环境变量下发，管理 API 不可写。
4. **密钥不回传明文**：管理 API 只返回 `api_key_masked` + `api_key_set`，与现有单上游行为一致。
5. **明确不做（本阶段）**
   - ❌ 模型归属自动探测 / 按模型查表选上游（用户已否决，改为 ID 前缀）
   - ❌ 同模型多上游的加权轮询与故障转移
   - ❌ URL 路径前缀路由（`/u/{id}/v1/...`）
   - ❌ 上游分组、配额、计费
6. **Git：** 小步 commit；**默认不 push**。
7. **YAGNI：** 不引入上游健康检查后台协程；连通性测试做成手动触发。
8. **兼容性**：升级后现网 `config.json`（只有 `upstream_base_url` + `upstream_api_key`）
   必须零手工操作即可用；迁移只在内存中发生并回写一次。

---

## 阶段开头：上阶段遗留

> 规则：把上一阶段验收表中 ⬜/❌ 项原文抄入；本阶段优先修复并回归。

| 上阶段遗留项 | 来源（测试/验证/审查） | 未通过原因 | 状态 |
|-------------|----------------------|-----------|------|
| 无（上一阶段为 2026-10-21 管理后台改造，OPEN.md 记为「待人工验收」，非本阶段依赖） | — | — | — |

---

## 跳过项（因档位未做，**非缺陷**，待补做）

| 跳过项 | 原因 | 待补做 |
|--------|------|--------|
| 真实上游多实例端到端（需两台可达上游） | 本机只有单个上游可测 | ⬜ 待用户确认补做 |

---

## 与前后阶段

| 阶段 | 状态 | 交付 |
|------|------|------|
| 上阶段（2026-10-21 管理后台改造） | ⬜ 待人工验收 | 按 IP 汇总 / 窗口授权 / 输出限速 / 分析导向 |
| **本阶段** | ⬜ | 多上游 + ID 前缀路由 |
| 下阶段 | | 上游健康检查 / 故障转移（本阶段不做） |

---

## File Structure（预期变更）

| 文件 | 动作 | 职责 |
|------|------|------|
| `web/config.go` | 修改 | 新增 `Upstream` 结构体、`Upstreams`/`DefaultUpstreamID` 字段、迁移函数 |
| `web/upstreams.go` | 新建 | 上游注册表：`Resolve(model)`、`List()`、`Get(id)`、`Add/Update/Delete`、前缀剥离 |
| `web/upstreams_test.go` | 新建 | 路由规则、前缀剥离、ID 冲突、迁移 |
| `web/config_test.go` | 修改 | 补迁移用例（旧字段 → 新列表） |
| `web/gateway.go` | 修改 | `/v1/*` 转发目标改由 `Resolve` 决定 |
| `web/ask.go` | 修改 | AI 问数走同一套 `Resolve` |
| `web/admin.go` | 修改 | 新增上游 CRUD handler；`handleAdminModels` 按上游分组；`handleGetSettings` 回显上游列表 |
| `web/main.go` | 修改 | 路由注册 + 启动时逐上游 SSRF 校验 |
| `web/static/admin.js` | 修改 | 「上游与额度」卡片拆分；新增上游管理列表 |
| `docs/DEPLOY-WEB.md` | 修改 | 多上游配置说明 |
| `docs/superpowers/specs/2026-08-05-web-self-service-design.md` | 修改 | 非目标「不做多上游」改为已实施 |

---

## 配置或 API 契约

### config.json（新增字段，旧字段保留）

```json
{
  "upstream_base_url": "http://127.0.0.1:18080/v1",
  "upstream_api_key": "sk-legacy",
  "default_upstream_id": "main",
  "upstreams": [
    {
      "id": "main",
      "name": "主上游",
      "base_url": "http://127.0.0.1:18080/v1",
      "api_key": "sk-xxx",
      "enabled": true
    },
    {
      "id": "backup",
      "name": "备用上游",
      "base_url": "http://10.0.0.2:18080/v1",
      "api_key": "sk-yyy",
      "enabled": true
    }
  ]
}
```

迁移规则：`upstreams` 为空且 `upstream_base_url` 非空 → 生成
`{id:"default", name:"默认上游", base_url:<旧值>, api_key:<旧值>, enabled:true}`，
`default_upstream_id` 设为 `"default"`，并回写一次 config.json。旧字段**保留不删**（只读兼容），
后续所有写入只动新字段。

### 上游 ID 规则

- 正则：`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,31}$`（与主项目 `custom_providers.go` 的 ID 规则一致）
- 不允许 `default` 之外的保留字；ID 唯一（大小写敏感）
- ID 中不含 `/`，因此前缀分隔符无歧义

### 路由规则（核心）

```
请求 model = "backup/gpt-4o"
  → 取第一段 "backup"
  → 若存在 id=="backup" 且 enabled 的上游：转发到该上游，body 中 model 改写为 "gpt-4o"
  → 否则：整体视为普通模型名，转发到默认上游，body 不改写

请求 model = "or/minimax/minimax-m3.1-flash:free"
  → 第一段 "or" 无匹配上游 → 走默认上游，model 原样
```

### 管理 API（新增）

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/api/admin/upstreams` | 列表（key 只回 `api_key_masked`） |
| `POST` | `/api/admin/upstreams` | 新增（`base_url` 过 SSRF 校验） |
| `PUT` | `/api/admin/upstreams/{id}` | 修改（`api_key` 留空 = 不变） |
| `DELETE` | `/api/admin/upstreams/{id}` | 删除（禁止删默认上游 / 最后一个启用上游） |
| `POST` | `/api/admin/upstreams/{id}/test` | 连通性测试：拉 `/v1/models` |

`GET /api/admin/models` 返回格式变更（**破坏性**，前后端同步改）：

```json
{
  "upstreams": [
    {"id": "main", "name": "主上游", "models": ["gpt-4o"], "warning": ""},
    {"id": "backup", "name": "备用上游", "models": ["claude-sonnet-4"], "warning": ""}
  ],
  "allowed": ["main/gpt-4o", "backup/claude-sonnet-4"]
}
```

`allowed` 中的条目带上游前缀，与用户实际要填的 model 字符串一致。

---

## Task 1：配置结构与迁移

**Files:** `web/config.go`、`web/config_test.go`

**行为:** 引入 `Upstream` 类型与 `Upstreams`/`DefaultUpstreamID` 字段；加载时自动迁移旧单上游配置。

**Steps:**

1. 在 `web/config.go` 新增：
   ```go
   type Upstream struct {
       ID      string `json:"id"`
       Name    string `json:"name"`
       BaseURL string `json:"base_url"`
       APIKey  string `json:"api_key"`
       Enabled bool   `json:"enabled"`
   }
   ```
   `Config` 增加 `Upstreams []Upstream` 与 `DefaultUpstreamID string`。
2. 实现 `migrateUpstreams(c *Config) (changed bool)`：
   - `len(c.Upstreams) > 0` → 只补齐 `DefaultUpstreamID`（空则取第一个 enabled），返回是否改动
   - `len(c.Upstreams) == 0 && c.UpstreamBaseURL != ""` → 生成 `id="default"` 的一条
   - 两者都空 → 不生成，保持「未配置上游」语义
3. `LoadConfig` 中调用迁移，`changed` 为 true 时回写（复用现有 `changed` 变量）。
4. 测试：
   - 旧配置只有 `upstream_base_url` → 迁移出一条 `default`，`DefaultUpstreamID=="default"`
   - 已有 `upstreams` → 不被旧字段覆盖
   - 两者都空 → `Upstreams` 为空，不 panic
   - 迁移幂等（连续调用两次结果一致）
5. 跑：`go test ./web/ -run 'TestMigrate|TestConfig' -v`
   期望：全 PASS

---

## Task 2：上游注册表与路由解析

**Files:** `web/upstreams.go`、`web/upstreams_test.go`

**行为:** 提供 `Resolve(model) (Upstream, string, error)`——返回目标上游与剥离前缀后的模型名。

**Steps:**

1. 新建 `web/upstreams.go`：
   ```go
   // Resolve 按 model 第一段匹配上游 ID。
   // 命中 → 返回该上游 + 剥离前缀后的 model；
   // 未命中 → 返回默认上游 + 原 model（不报错，存量模型名照常可用）。
   func (a *App) Resolve(model string) (Upstream, string, error)
   ```
   - 从 `a.Cfg.Upstreams` 读（`a.Mu.RLock`），筛选 `Enabled`
   - 第一段 = `strings.IndexByte(model, '/')` 之前的部分
   - 命中且剥离后剩余非空才路由；剩余为空（如 `model=="backup/"`）→ 返回错误
   - 默认上游：`DefaultUpstreamID` 对应的；若该 ID 不存在或未启用 → 取第一个 enabled
   - 无任何 enabled 上游 → 返回 `errNoUpstream`（新增错误变量）
2. 实现 `validUpstreamID(id string) bool`（正则见契约）。
3. 实现 `FindUpstream(list []Upstream, id string) (Upstream, bool)` 供 CRUD 复用。
4. 测试（表驱动）：
   - `"backup/gpt-4o"` + 存在 `backup` → 上游 backup、model `"gpt-4o"`
   - `"or/minimax/minimax-m3.1-flash:free"` + 无 `or` → 默认上游、model 原样
   - `"gpt-4o"` → 默认上游、model 原样
   - `"backup/"` → 错误
   - `"backup/gpt-4o"` 但 `backup` 已禁用 → 默认上游、model **原样**（不剥离）
   - 全部禁用 → `errNoUpstream`
   - 大小写：`"Backup/gpt-4o"` 与 id `backup` 不匹配（大小写敏感）→ 默认上游
5. 跑：`go test ./web/ -run 'TestResolve' -v`
   期望：全 PASS

---

## Task 3：网关与 AI 问数接入路由

**Files:** `web/gateway.go`、`web/ask.go`、`web/gateway_test.go`

**行为:** `/v1/*` 与 `/api/admin/ask` 都改由 `Resolve` 决定目标与改写后的 model。

**Steps:**

1. `gateway.go`：
   - 在 `resolveRequestedModel` 拿到 model 后，调用 `a.Resolve(model)` 得到 `target` 与 `forwardModel`
   - **白名单校验仍用客户端原始 model**（即 `backup/gpt-4o`），保证 Key 绑定语义不变
   - 转发前若 `forwardModel != model`，用 `json.RawMessage` 精确改写 body 的 `model` 字段
     （**不要** `map[string]any` 反序列化再序列化——会丢字段顺序/精度/未知字段）
   - `target` 为错误时：`errNoUpstream` → 503；其余 → 500
   - 日志 `LogEntry.Model` 记**原始 model**（含前缀），便于排查
2. `ask.go`：同样走 `Resolve`，`upstream+"/chat/completions"` 用解析出的 `target.BaseURL`。
3. 测试：
   - `model:"backup/gpt-4o"`，Key 绑定 `backup/gpt-4o` → 转发到 backup，上游收到 `model:"gpt-4o"`
   - `model:"gpt-4o"`，Key 绑定 `gpt-4o` → 转发到默认上游，上游收到 `model:"gpt-4o"`
   - body 中的其它字段（`temperature`、未知字段 `x_foo`）在改写后保持不变
   - 无可用上游 → 503
4. 跑：`go test ./web/ -run 'TestGateway|TestAsk' -v`
   期望：全 PASS

---

## Task 4：管理 API

**Files:** `web/admin.go`、`web/main.go`、`web/admin_test.go`

**行为:** 上游 CRUD + 连通性测试；`/api/admin/models` 按上游分组。

**Steps:**

1. `web/main.go` 注册路由：
   ```
   GET    /api/admin/upstreams
   POST   /api/admin/upstreams
   PUT    /api/admin/upstreams/{id}
   DELETE /api/admin/upstreams/{id}
   POST   /api/admin/upstreams/{id}/test
   ```
2. `web/admin.go` 实现：
   - 新增/修改：校验 ID 格式与唯一；`base_url` 过 `ValidateUpstreamBaseURL`；`api_key` 留空保持原值
   - 删除：禁止删 `DefaultUpstreamID` 指向的上游；禁止删到只剩 0 个 enabled 上游
   - 所有写操作落 `Audit.Write`（含 `id` 与 `base_url` 变更，key 只记末 4 位）
   - `handleAdminModels`：逐上游 `FetchModels`，返回分组结构（见契约）；单个上游失败只填 `warning`，不影响其它
   - `handleGetSettings`：增加 `upstreams` 数组（key masked）+ `default_upstream_id`
3. `handlePutSettings`：上游字段**不再接受**写入（返回 400 提示改用 `/api/admin/upstreams`）；
   其余字段（`public_base_url`/`ttl_days`/`token_quota`/`rate_limit_per_min`）保持不变
4. 测试：
   - 新增上游 → 出现在列表、`api_key` 不回明文
   - 非法 `base_url`（`http://169.254.169.254/`）→ 400 且不落盘
   - 删除默认上游 → 400
   - 删除到只剩一个 enabled → 拒绝删最后一个
   - `/api/admin/models` 单上游失败 → 该组有 `warning`，其它组正常
5. 跑：`go test ./web/ -run 'TestAdminUpstream|TestAdminModels' -v`
   期望：全 PASS

---

## Task 5：前端上游管理

**Files:** `web/static/admin.js`、`web/static/admin.html`（如需）

**行为:** 「参数设置」页拆出上游管理；模型管理页按上游分组展示。

**Steps:**

1. `loadSettings` 中移除 `upstream` / `upkey` 输入框，改为「前往上游管理」按钮。
2. 新增上游管理视图（`data-view="upstreams"`）：
   - 列表：ID / 名称 / Base URL / 状态 / 密钥末 4 位 / 默认标记
   - 操作：新增、编辑、启用/停用、测试连通性、删除
   - 新增/编辑弹窗：ID（新增时可填，编辑时只读）、名称、Base URL、API Key（留空=不变）、启用
3. `loadModels` 按上游分组渲染，勾选项的 value 用 `<upstreamID>/<model>` 拼接。
4. 跑：`go build ./web/` + 手工打开后台确认页面可用
   期望：页面无 JS 报错，增删改查生效

---

## Task 6：文档与回归

**Files:** `docs/DEPLOY-WEB.md`、`docs/superpowers/specs/2026-08-05-web-self-service-design.md`

**Steps:**

1. `docs/DEPLOY-WEB.md` 补多上游配置示例与路由规则说明（含「前缀必须匹配到上游 ID 才剥离」）。
2. 设计文档「非目标」中「不做多上游轮询/容灾」改为：
   「~~不做多上游轮询/容灾~~ → 2026-10-28 已支持多上游（ID 前缀路由），仍不做自动故障转移」。
3. 全量回归：`go build ./...`、`go vet ./web/`、`go test ./web/ -race`
   期望：全 PASS

---

## 代码审查（阶段级环节，验收前）

**审查方：** 独立角色（非本阶段实现者）

**审查面：** 代码风格 / 测试完整性 / 依赖合理性 / 架构红线 / 安全（SSRF、密钥、越权）/ API 契约一致性

| 审查项 | 结论（✅/⚠️/❌） | 问题清单 |
|--------|------------------|----------|
| 风格 | | |
| 测试完整性 | | |
| 依赖与架构红线 | | |
| 安全（SSRF/密钥/越权） | | |
| API 契约 | | |

**结论：** ✅ 通过 / ⚠️ 有条件通过 / ❌ 不通过

---

## 验收标准总表

| # | 标准 | 通过条件 | 验证责任人 |
|---|------|----------|----------|
| 1 | 旧配置零改动可用 | 只有 `upstream_base_url` 的 config.json 启动后自动迁移，`/v1` 请求正常 | 自动化 |
| 2 | 前缀路由正确 | `backup/gpt-4o` → backup 上游且上游收到 `gpt-4o` | 自动化 |
| 3 | 存量模型名不受影响 | `or/minimax/minimax-m3.1-flash:free` 原样走默认上游 | 自动化 |
| 4 | 禁用上游不参与路由 | 禁用 `backup` 后 `backup/x` 走默认上游且不剥前缀 | 自动化 |
| 5 | SSRF 不可绕过 | 新增指向 `169.254.169.254` 的上游 → 400 且不落盘 | 自动化 |
| 6 | 密钥不回明文 | `GET /api/admin/upstreams` 无明文 key | 自动化 |
| 7 | 构建与测试 | `go build ./...`、`go vet ./web/`、`go test ./web/ -race` 全绿 | 自动化 |
| 8 | 代码审查 | 审查结论 ✅ 或 ⚠️（问题已登记） | 独立角色 |
| 9 | 红线 | 无禁止项；密钥未入库 | 自动化 |
| 10 | 前端可用 | 上游增删改查与模型分组展示正常 | 端到端（需人工过目） |

---

## 风险与降级

| 风险 | 缓解 |
|------|------|
| 模型名本身含斜杠导致误剥前缀 | 只认「第一段精确匹配已启用上游 ID」；配测试钉死 |
| 前端 `allowed_models` 存量值不含前缀，升级后与新格式不一致 | 迁移时给存量 `allowed_models` 补默认上游前缀；或首次加载时按「不含 `/` 或第一段非已知 ID」自动归到默认上游 |
| `/api/admin/models` 返回结构变更破坏前端 | 前后端同一 commit 提交 |
| 上游数量多导致「拉取模型」变慢 | 各上游并发拉取 + 5s 超时；失败只告警 |

---

## 给接手 AI 的完整提示词

将下面整段粘贴给执行 AI 即可开工：

---

你是负责 **opencode2api_enhance** 的实现代理。请**完整执行本阶段**，不要只写方案。

### 基线
- 目录：`D:\AI_Projects\opencode2api_guide\opencode2api_enhance`
- 当前分支：`feature/web-self-service`（已检出，与远程同步）
- 唯一实施计划：`docs/ai-framework/plans/2026-10-28-web-multi-upstream.md`
- 必读：`docs/ai-framework/phased-plan-driven.md`、`AGENTS.md`、`docs/superpowers/specs/2026-08-05-web-self-service-design.md`

### 做
1. 按 Task 1→6 顺序实现「web 自助服务多上游（上游 ID 作为 model 前缀路由）」。
2. 核心规则：`model` 第一段精确匹配到**已启用**的上游 ID 才剥离前缀并路由；否则原样走默认上游。
3. 每个上游 `base_url` 写入前必须过 `ValidateUpstreamBaseURL`；管理 API 不回传明文 key。
4. 旧单上游配置必须零手工迁移。

### 不做
- 模型归属自动探测、加权轮询、故障转移
- URL 路径前缀路由
- 提交密钥；未授权的 `git push`

### 工作方式
1. 先跑 `go build ./web/ && go test ./web/` 确认基线干净。
2. **严格按计划 Task 顺序**；每 Task 测试后 commit。
3. 证据优先：完成前必须重跑计划中的验证命令。
4. 用简体中文回复进度；代码标识符保持原样。

### 交卷
全部完成后给出：分支名、提交列表、验收表自评、测试/构建结果、残留风险。

现在开始：读完本阶段计划，从 Task 1 执行到最后。

---

## 残留手工验收清单

1. 启动 `web.exe`，登录后台，新增第二个上游（指向一个真实可达的兼容网关）。
2. 领取页选择 `backup/<模型>`，用领到的 Key 实际发一次对话，确认命中备用上游。
3. 不带前缀的模型名仍走默认上游。
4. 停用备用上游后，`backup/<模型>` 走默认上游（不剥前缀）。
