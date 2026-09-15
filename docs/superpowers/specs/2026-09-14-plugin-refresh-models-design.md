# 插件式供应商：手动刷新模型按钮 设计文档

> 日期：2026-09-14
> 状态：设计定稿
> 相关文档：`docs/PLUGIN-PROVIDERS.md`（插件契约）、`core/manager/pluginprovider/`（R1-R3 实现）

## 一、背景与问题

插件式供应商（如 loomy）的模型目录展示存在"过期"问题：

1. **面板插件卡片模型数 / 「暴露模型」弹层的模型清单**：来自 `queryModelCount()`
   （`core/manager/pluginprovider/manager.go:911`），只在插件**就绪时拉取一次**
   （`manager.go:502` / `:823`），之后永不更新。上游（官网）新增/下线模型后，
   弹层清单与卡片数字保持旧值。
2. **聚合目录（网关 `/v1/models`）**：由 `aggregator.Refresh()` 实时从各厂商拉取
   （remote vendor → 插件子进程 `/v1/models` → 官网实时透传），仅在插件集合变化
   （`syncPlugins`）与每 10 分钟定时触发（`startModelRefresh`），存在最多 10 分钟的滞后。

用户诉求：在「暴露模型」弹层内提供**手动刷新按钮**，点击后**从官网强制拉取最新模型列表**，
立即更新弹层清单、卡片模型数，并同步刷新聚合目录。

### 数据源澄清

- 插件子进程的 `/v1/models` 是**实时透传官网**（`forwardToUpstream`），不经任何本地缓存。
- 本功能刷新动作的最终数据源是**官网**（经插件子进程），与 loomy2api 独立服务的 `127.0.0.1:7777`
  端口**完全无关**（7777 是独立代理模式的独立实例）。

## 二、目标 / 非目标

### 目标

- 插件式供应商「暴露模型」弹层提供「刷新模型」按钮。
- 点击后强制从官网拉取该插件最新模型清单，更新 `models_all` / `models` 与聚合目录。
- 刷新失败时**保留弹层现有清单并报错**（不清空旧数据，防误伤）。
- 改动只落在 enhance 宿主，不改 loomy 插件侧。

### 非目标

- 不修改 loomy2api（7777 独立服务）的模型缓存逻辑。
- 不新增自动定时刷新（保留现有 10 分钟定时）。
- 不改动插件契约（`PLUGIN-PROVIDERS.md`）。
- 不提供"全量刷新所有插件"按钮（单插件粒度足够，YAGNI）。

## 三、现状链路

```
插件就绪 ──→ queryModelCount() 拉一次 ──→ p.modelCount / p.modelsAll（此后冻结）
插件集合变化 / 每10分钟 ──→ syncPlugins/refreshModelCatalogIfDue ──→ aggregator.Refresh()
                                                                        └─→ remote.Vendor.ListModels()
                                                                              └─→ GET {子进程}/v1/models
                                                                                    └─→ 官网（实时透传）
```

核心问题：`queryModelCount` 没有二次触发入口，聚合目录刷新有签名去重（`syncPlugins` 的
`lastPluginSig`）与节流（`refreshModelCatalogIfDue` 的 10s 最小间隔），导致手动强制刷新无法
复用现有路径。

## 四、设计

### 4.1 后端

#### 4.1.1 `core/manager/pluginprovider/manager.go`

- `Config` 新增字段：`OnModelRefresh func(id string)`（可选回调，main 侧注入强制刷聚合目录）。

- 拆分 `fetchPluginModels(p *plugin) ([]string, error)`：从子进程 `GET {url}/v1/models`
  拉取最新模型 ID 清单，带错误返回；`queryModelCount` 改为复用其实现（保持现签名）。
  超时用 `cfg.ModelTimeout`（与 `queryModelCount` 现状一致）。

- 新增 `RefreshModels(id string) (View, error)`：
  1. 查插件：不存在 → 返回 `errNotFound`。
  2. 校验状态：非 `running` 或无 `url`/`auth` → 返回错误「插件未运行，无法刷新模型」。
  3. 调 `fetchPluginModels` 拉官网最新清单：
     - 成功 → 更新 `p.modelCount` / `p.modelsAll`。
     - 失败 → 返回错误（保留旧 `models_all` 不变，由前端呈现旧清单+报错）。
  4. 触发 `m.cfg.OnModelRefresh(id)`（若注入）。
  5. 返回 `m.View(id)`（含最新 `models` / `models_all`）。

#### 4.1.2 `core/manager/pluginprovider/http.go`

- 新增 `RefreshModelsHandler()`：`POST /api/admin/plugins/{id}/refresh-models`
  - 方法非 POST → 405。
  - `m.RefreshModels(id)` 失败：`errNotFound` → 404；其它 → 409 Conflict（含错误文案）。
  - 成功 → `{"status":"ok","plugin":<最新 View>}`。

#### 4.1.3 根 `main.go`

- 注册路由（与既有插件路由同款 `loggingMiddleware(requireAuth(...))`）：
  `POST /api/admin/plugins/{id}/refresh-models`。
- 装配回调：`pluginprovider.Config{OnChange: syncPlugins, OnModelRefresh: onPluginModelRefresh}`。
- 新增 `onPluginModelRefresh(id string)`（放 `plugin_vendors.go`）：调用
  `refreshModelCatalog()`（`models_source.go:381`，**无节流**强制刷新聚合目录，走
  `catalogRefreshMu` 串行化）+ `slog.Info` 记录。

> 注意：`refreshModelCatalog` 已存在且无最小间隔限制，是复用的正确入口；
> 不用 `refreshModelCatalogIfDue`（有 10s 节流，手动按钮不应被截断）。

#### 4.1.4 单测 `core/manager/pluginprovider/manager_test.go`

- 复用既有 fake provider 基建（`fake_provider_test.go`，httptest mock 子进程 `/v1/models`）。
- 用例：
  - 运行中插件刷新成功：`models` / `models_all` 更新为 mock 返回的清单，`OnModelRefresh`
    被触发一次且带正确 id。
  - 插件不存在 → `errNotFound`。
  - 未运行插件 → 返回明确错误。
  - 子进程不可达 → 返回错误，且旧 `models_all` 保留。

### 4.2 前端

#### 4.2.1 `src/lib/api.ts`

- 复用 `PluginSaveResponse`（`{status, plugin}`，与返回契约一致）。
- 新增：`pluginRefreshModels(id: string) => req<PluginSaveResponse>('POST', \`/plugins/${encodeURIComponent(id)}/refresh-models\`)`。

#### 4.2.2 `src/pages/CustomModelsPage.tsx`

- 新增状态 `modelRefreshBusy: boolean`（独立于 `pluginBusy`，避免与保存共用禁用态）。
- 新增 `refreshPluginModels()`：
  1. 置 `modelRefreshBusy`。
  2. 调 `api.pluginRefreshModels(pluginExposing.id)`。
  3. 成功：更新 `setPlugins`（卡片模型数）；`setPluginExposing` 更新 `allModels` 为返回的
     `models_all`；`allowed` 仅保留**仍存在于新清单**的已勾选项（不在新清单的旧勾选项自动移除）；
     清 `modelSearch`；toast「已从官网刷新模型列表（N 个）」。
  4. 失败：toast 报错，**不改动弹层现有清单**。
  5. `finally` 复位 `modelRefreshBusy`。
- 「暴露模型」弹层头部（`暴露模型 · {name}` 行）「全部暴露」旁新增「刷新模型」按钮：
  - 点击 `refreshPluginModels()`。
  - `modelRefreshBusy` 时按钮禁用 + `RefreshCw` 旋转。
  - `title` 提示「从官网重新拉取该插件最新模型列表」。

### 4.3 数据流（刷新后）

```
点击「刷新模型」→ POST /api/admin/plugins/{id}/refresh-models
  → m.RefreshModels(id)
      → fetchPluginModels → 子进程 GET /v1/models → 官网（实时透传）
      → 更新 models_all / models
      → OnModelRefresh(id) → refreshModelCatalog() → aggregator.Refresh()（全厂商并行，无节流）
          → /v1/models 聚合目录立即更新
  → 返回最新 View
→ 前端弹层清单 / 卡片模型数同步更新
```

## 五、错误处理

| 场景 | 行为 |
|---|---|
| 插件不存在 | HTTP 404，toast 报错 |
| 插件非 running | HTTP 409，toast「插件未运行，无法刷新模型」 |
| 子进程 / 官网拉取失败 | HTTP 409，toast 报错；**弹层保留旧清单**，`models_all` 不变 |
| OnModelRefresh 至聚合刷新失败 | 主流程已成功返回（聚合刷新失败仅记日志），下次触发重试 |

## 六、验证

- `go test -count=1 ./...`（enhance 根目录）全绿，含新增 RefreshModels 单测。
- 前端构建通过（`npm run build`，或 tsc 类型检查）。
- `go vet ./...` 通过。

## 七、范围约束

- 最小修改：不改插件契约、不推倒重构、不动 loomy 插件侧。
- 命名遵循项目惯例（handler 用 `xxxHandler()`、状态用 `xxxBusy`）。
- 新端点加入 `docs/PLUGIN-PROVIDERS.md §七` 管理 API 表（可选，建议补充）。