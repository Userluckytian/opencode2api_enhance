# 插件式供应商手动刷新模型按钮 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 enhance 宿主「暴露模型」弹层加「刷新模型」按钮，强制从官网（经插件子进程实时透传）拉最新模型清单，同步更新弹层清单、卡片模型数与聚合目录。

**Architecture:** 后端在 `pluginprovider.Manager` 增加 `RefreshModels(id)`（拉子进程 `/v1/models` 更新 `models_all`/`models`，`OnModelRefresh` 回调触发无节流 `refreshModelCatalog()`）；前端在暴露模型弹层头部加刷新按钮。数据源始终是官网（插件子进程实时透传），与 127.0.0.1:7777 无关。

**Tech Stack:** Go（`core/manager/pluginprovider` + 根 `main.go`/`plugin_vendors.go`）、TypeScript/React（`src/lib/api.ts` + `src/pages/CustomModelsPage.tsx`）、Vite + Tauri。

## Global Constraints

- 数据源 = 官网（经插件子进程 `GET {url}/v1/models` 实时透传），绝不从本地 7777 拉。
- 刷新失败保留旧清单，只报错不清空（spec §五）。
- 不改插件契约（`PLUGIN-PROVIDERS.md`）、不改插件侧（loomy2api）、不推倒重构。
- 新 API 走 `requireAuth` 鉴权，与既有 `/api/admin/plugins/*` 同款。
- 命名惯例：Go handler 用 `xxxHandler()`，React busy 状态用 `xxxBusy`（描述性名称，事件处理函数统一 `handle` 前缀——本项目现用 `onClick={() => void fn()}` 模式，沿用现状）。
- 分支：基于 `feature/plugin-refresh-models`（已创建），不要动 main。
- 前端验证：`npm run tauri:dev` 是用户侧验收，自动化用 `npx tsc --noEmit` 或 `npm run build`。

---

## 文件结构

| 文件 | 职责 | 动作 |
|---|---|---|
| `core/manager/pluginprovider/manager.go` | `fetchPluginModels` 拆分 + `RefreshModels` + `Config.OnModelRefresh` | 修改 |
| `core/manager/pluginprovider/manager_test.go` | RefreshModels 单测（成功/不存在/未运行/子进程不可达） | 修改 |
| `core/manager/pluginprovider/http.go` | `RefreshModelsHandler`（POST /{id}/refresh-models） | 修改 |
| `main.go` | 注册 `/api/admin/plugins/{id}/refresh-models` 路由 | 修改 |
| `plugin_vendors.go` | `onPluginModelRefresh(id)`（调 `refreshModelCatalog()` 无节流强制刷新聚合目录） | 修改 |
| `core/manager/pluginprovider/manager_test.go`（或 http 测试） | RefreshModelsHandler HTTP 用例（404/409/200） | 修改 |
| `src/lib/api.ts` | `pluginRefreshModels(id)` | 修改 |
| `src/pages/CustomModelsPage.tsx` | `modelRefreshBusy` + `refreshPluginModels` + 弹层头部刷新按钮 | 修改 |
| `docs/PLUGIN-PROVIDERS.md` | §七 管理 API 表增加 refresh-models 行 | 修改 |

依赖关系：Task 1 → Task 2（后端串行）；Task 3 仅依赖 Task 1/2 的 API 契约（文本约定），文件互不重叠 → **可与 Task 1/2 并行**。Task 4 收尾统一验证。

---

## Task 1: 后端 `RefreshModels` 核心逻辑 + 单测

**Files:**
- Modify: `core/manager/pluginprovider/manager.go`（`Config` struct ~74 行；`queryModelCount` ~910-948 行）
- Test: `core/manager/pluginprovider/manager_test.go`（新增测试函数）

**Interfaces:**
- Consumes: 既有 `plugin` 结构（字段 `url`/`auth`/`status`/`modelCount`/`modelsAll`，`mu` 保护）、`queryModelCount`、`cfg.ModelTimeout`、`errNotFound`。
- Produces:
  - `func (m *Manager) RefreshModels(id string) (View, error)`
  - `Config.OnModelRefresh func(id string)`（可选回调）
  - `func (m *Manager) fetchPluginModels(p *plugin) ([]string, error)`（供 queryModelCount 与 RefreshModels 共用）

- [ ] **Step 1: 写失败测试**（`manager_test.go` 追加）

```go
// TestRefreshModels 手动刷新模型：从子进程(/v1/models)拉最新清单更新 models_all/models，
// 并触发 OnModelRefresh 回调；失败时保留旧清单并返回错误。
func TestRefreshModels(t *testing.T) {
	var called string
	h := newHarness(t, Config{OnModelRefresh: func(id string) { called = id }})
	setHelper(t, "ready", "rp")
	h.installPlugin("rp", "", "")
	h.pm.Start()
	waitStableRunning(t, h.pm, "rp", 5*time.Second)

	v, err := h.pm.RefreshModels("rp")
	if err != nil {
		t.Fatalf("RefreshModels err: %v", err)
	}
	if v.Models != 2 || len(v.ModelsAll) != 2 {
		t.Fatalf("models 未更新: models=%d models_all=%v", v.Models, v.ModelsAll)
	}
	if called != "rp" {
		t.Fatalf("OnModelRefresh 未被触发, called=%q", called)
	}

	// 不存在
	if _, err := h.pm.RefreshModels("nope"); err == nil {
		t.Fatal("期望 errNotFound，实际 nil")
	}
}

// TestRefreshModelsNotRunning 非 running 插件返回错误。
func TestRefreshModelsNotRunning(t *testing.T) {
	h := newHarness(t, Config{})
	setHelper(t, "need_config", "rp")
	h.installPlugin("rp", "", "")
	h.pm.Start()
	waitStatus(t, h.pm, "rp", StatusNeedCfg, 5*time.Second)

	if _, err := h.pm.RefreshModels("rp"); err == nil {
		t.Fatal("期望非 running 报错，实际 nil")
	}
}

// TestRefreshModelsChildDown 子进程不可达：返回错误且保留旧 models_all。
func TestRefreshModelsChildDown(t *testing.T) {
	h := newHarness(t, Config{})
	setHelper(t, "ready", "rp")
	h.installPlugin("rp", "", "")
	h.pm.Start()
	waitStableRunning(t, h.pm, "rp", 5*time.Second)
	waitFor(t, 3*time.Second, "模型数已拉取", func() bool { return h.pm.View("rp").Models == 2 })

	// 同包访问内部字段：把 url 改成不可达端口，模拟子进程挂起
	h.pm.mu.Lock()
	p := h.pm.plugins["rp"]
	oldURL := p.url
	p.url = "http://127.0.0.1:1"
	h.pm.mu.Unlock()
	defer func() {
		h.pm.mu.Lock()
		p.url = oldURL
		h.pm.mu.Unlock()
	}()

	if _, err := h.pm.RefreshModels("rp"); err == nil {
		t.Fatal("期望子进程不可达报错，实际 nil")
	}
	vv := h.pm.View("rp")
	if len(vv.ModelsAll) != 2 {
		t.Fatalf("失败后旧清单被清空: %v", vv.ModelsAll)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**（编译失败即可）

Run: `go test ./core/manager/pluginprovider/ -run TestRefreshModels -count=1`
Expected: 编译失败（`RefreshModels` 未定义）；或 FAIL。

- [ ] **Step 3: 实现**（`manager.go`）

在 `Config` struct（~74 行）追加字段：

```go
	// OnModelRefresh 手动刷新模型回调（main 侧注入强制刷聚合目录；可为 nil）。
	OnModelRefresh func(id string)
```

把 `queryModelCount`（910-948 行）拆出 fetch 并新增 RefreshModels：

```go
// fetchPluginModels 向子进程拉取最新模型 ID 清单（GET {url}/v1/models）。
func (m *Manager) fetchPluginModels(p *plugin) ([]string, error) {
	m.mu.Lock()
	url, auth := p.url, p.auth
	timeout := m.cfg.ModelTimeout
	m.mu.Unlock()
	if url == "" {
		return nil, errors.New("插件端点未就绪")
	}
	ctx, cancel := context.WithTimeout(m.ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+auth)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Data))
	for _, d := range out.Data {
		if d.ID != "" {
			ids = append(ids, d.ID)
		}
	}
	if len(ids) == 0 {
		return nil, errors.New("empty model list")
	}
	return ids, nil
}

// queryModelCount 就绪后向子进程拉一次模型目录计数（不参与桥接，仅列表展示）。
func (m *Manager) queryModelCount(p *plugin) {
	ids, err := m.fetchPluginModels(p)
	if err != nil {
		return
	}
	m.mu.Lock()
	p.modelCount = len(ids)
	p.modelsAll = ids
	m.mu.Unlock()
}
```

在操作面（`Rescan` 附近，~972 行后）新增：

```go
// RefreshModels 手动刷新模型：从子进程拉取官网最新清单，更新模型计数，并触发
// OnModelRefresh 回调（main 侧强制刷新聚合目录）。失败时保持旧清单并返回错误。
func (m *Manager) RefreshModels(id string) (View, error) {
	m.mu.Lock()
	p, ok := m.plugins[id]
	m.mu.Unlock()
	if !ok {
		return View{}, errNotFound
	}
	if p.status != StatusRunning || p.url == "" {
		return View{}, errors.New("插件未运行，无法刷新模型")
	}
	ids, err := m.fetchPluginModels(p)
	if err != nil {
		return View{}, fmt.Errorf("刷新模型失败: %w", err)
	}
	m.mu.Lock()
	p.modelCount = len(ids)
	p.modelsAll = ids
	m.mu.Unlock()
	if m.cfg.OnModelRefresh != nil {
		m.cfg.OnModelRefresh(id)
	}
	return m.View(id), nil
}
```

需确认第 2 行 import（`errors`、`fmt`、`context`、`io`、`net/http`、`encoding/json` 是否已有——`manager.go` 顶部 imports 需补 `errors`）。
同时确认 `StatusRunning` 常量名（对照 `statusOf`/`setStatus` 使用的状态名，如既有 `StatusRunning`/`StatusNeedConfig`，以 `manager.go` 中 `const` 为准）。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./core/manager/pluginprovider/ -run TestRefreshModels -count=1`
Expected: PASS（3 个用例）。

- [ ] **Step 5: 全包编译 + 回归**

Run: `go build ./... && go test ./core/manager/pluginprovider/ -count=1`
Expected: 通过。

- [ ] **Step 6: 提交**（阶段收尾统一提交，见下方提交策略）

---

## Task 2: 后端 HTTP handler + main 装配 + 单测

**Files:**
- Modify: `core/manager/pluginprovider/http.go`（追加 handler）
- Modify: `main.go`（~211 行 Config 装配、~327-331 行路由区）
- Modify: `plugin_vendors.go`（追加 `onPluginModelRefresh`）
- Test: `core/manager/pluginprovider/manager_test.go`（HTTP 用例；复用 h.pm + httptest 请求）

**Interfaces:**
- Consumes: Task 1 的 `RefreshModels`、`errNotFound`、`writeJSON`/`writeErr`、`requireMethod`。
- Produces:
  - `func (m *Manager) RefreshModelsHandler() http.HandlerFunc`（`POST /api/admin/plugins/{id}/refresh-models`，返回 `{"status":"ok","plugin":<View>}`）
  - `func onPluginModelRefresh(id string)`（根包 main，调 `refreshModelCatalog()`）

- [ ] **Step 1: 写失败测试**（`manager_test.go` 追加，仿既有 `TestPluginHandlers` 的 mux 挂法——对照 manager_test.go 577-580 行同款挂 handler）

```go
func TestRefreshModelsHandler(t *testing.T) {
	h := newHarness(t, Config{})
	setHelper(t, "ready", "rp")
	h.installPlugin("rp", "", "")
	h.pm.Start()
	waitStableRunning(t, h.pm, "rp", 5*time.Second)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/admin/plugins/{id}/refresh-models", h.pm.RefreshModelsHandler())
	do := func(method, path string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	code, out := do(http.MethodPost, "/api/admin/plugins/rp/refresh-models")
	if code != 200 {
		t.Fatalf("POST 期望 200, got %d (%v)", code, out)
	}
	if out["plugin"] == nil {
		t.Fatalf("响应缺 plugin: %v", out)
	}

	code, _ = do(http.MethodPost, "/api/admin/plugins/nope/refresh-models")
	if code != 404 {
		t.Fatalf("不存在期望 404, got %d", code)
	}

	code, _ = do(http.MethodGet, "/api/admin/plugins/rp/refresh-models")
	if code != 405 {
		t.Fatalf("GET 期望 405, got %d", code)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./core/manager/pluginprovider/ -run TestRefreshModelsHandler -count=1`
Expected: 编译失败（handler 未定义）。

- [ ] **Step 3: 实现 handler**（`http.go`，`DeleteHandler` 后追加）

```go
// RefreshModelsHandler POST /api/admin/plugins/{id}/refresh-models：手动从上游（官网，
// 经插件子进程）刷新该插件模型清单并触发聚合目录重建。
func (m *Manager) RefreshModelsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		id := r.PathValue("id")
		view, err := m.RefreshModels(id)
		if err != nil {
			if errors.Is(err, errNotFound) {
				writeErr(w, http.StatusNotFound, err.Error())
			} else {
				writeErr(w, http.StatusConflict, err.Error())
			}
			return
		}
		writeJSON(w, map[string]any{"status": "ok", "plugin": view})
	}
}
```

`main.go`（~211 行）装配回调：

```go
	pluginMgr := bindPluginMgr(pluginprovider.New(pluginprovider.Config{
		OnChange:       syncPlugins,
		OnModelRefresh: onPluginModelRefresh,
	}))
```

`main.go`（~331 行，`{id}` DELETE 下一行）注册路由：

```go
		mux.HandleFunc("/api/admin/plugins/{id}/refresh-models", loggingMiddleware(requireAuth(pluginMgr.RefreshModelsHandler())))
```

> 注意 Go 1.22+ ServeMux 路由冲突：`POST .../plugins/{id}/refresh-models` 与 `DELETE .../plugins/{id}` 方法不同、路径更具体，不冲突。

`plugin_vendors.go` 追加（文件尾部，import 需补 `log/slog` 已有；`refreshModelCatalog` 在 `models_source.go`，同包 main 可直接调用）：

```go
// onPluginModelRefresh 手动刷新插件模型回调：无节流强制刷新聚合目录（/v1/models 即时更新）。
func onPluginModelRefresh(id string) {
	if globalAgg == nil {
		return
	}
	refreshModelCatalog()
	slog.Info("plugin models refreshed", "plugin", id)
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./core/manager/pluginprovider/ -run TestRefreshModelsHandler -count=1`
Expected: PASS。

- [ ] **Step 5: 全量编译 + 回归**

Run: `go build ./... && go test ./core/manager/pluginprovider/ -count=1`
Expected: 通过。

- [ ] **Step 6: 提交**（阶段收尾提交）

---

## Task 3: 前端刷新按钮（可与 Task 1/2 并行）

**Files:**
- Modify: `src/lib/api.ts`（~695-706 行插件 API 区）
- Modify: `src/pages/CustomModelsPage.tsx`（state ~136-139 行、`savePluginExpose` 附近加函数、弹层头部 ~1179-1184 行）

**Interfaces:**
- Consumes: Task 2 的 API 契约 `POST /plugins/{id}/refresh-models` → `{"status","plugin":<PluginProviderView>}`；既有 `PluginSaveResponse` 类型（`api.ts:196`）。
- Produces:
  - `api.pluginRefreshModels(id: string): Promise<PluginSaveResponse>`
  - `CustomModelsPage` 内 `refreshPluginModels()` 与弹层刷新按钮

- [ ] **Step 1: 写实现**（api.ts 追加方法，`pluginDelete` 后 ~706 行）

```ts
  pluginRefreshModels: (id: string) =>
    req<PluginSaveResponse>('POST', `/plugins/${encodeURIComponent(id)}/refresh-models`),
```

- [ ] **Step 2: 前端状态 + 逻辑**（CustomModelsPage.tsx）

state 区（~139 行 `modelSearch` 后）追加：

```ts
  const [modelRefreshBusy, setModelRefreshBusy] = useState(false)
```

`savePluginExpose` 之后（~488 行）追加函数：

```ts
  // 手动刷新模型：从官网（经插件子进程）拉最新清单，更新弹层 + 卡片模型数；
  // 失败保留旧清单只报错；已勾选项中不在新清单的自动移除。
  const refreshPluginModels = async () => {
    if (!pluginExposing) return
    setModelRefreshBusy(true)
    try {
      const r = await api.pluginRefreshModels(pluginExposing.id)
      const fresh = r.plugin.models_all ?? []
      setPlugins((prev) => prev?.map((x) => (x.id === pluginExposing.id ? r.plugin : x)) ?? null)
      setPluginExposing((prev) => {
        if (!prev) return prev
        const keep = new Set([...prev.allowed].filter((m) => fresh.includes(m)))
        return { ...prev, allModels: fresh, allowed: keep }
      })
      setModelSearch('')
      toast(`已从官网刷新模型列表（${fresh.length} 个）`, true)
    } catch (e) {
      toast(`刷新模型失败：${String(e)}`, false)
    } finally {
      setModelRefreshBusy(false)
    }
  }
```

- [ ] **Step 3: 弹层头部加按钮**（~1179-1184 行「暴露模型 · name」与关闭按钮之间）

```tsx
              <div className="flex items-center gap-2">
                <button
                  type="button"
                  onClick={() => void refreshPluginModels()}
                  disabled={modelRefreshBusy}
                  className="flex items-center gap-1.5 border border-zinc-200 text-zinc-600 rounded-lg px-3 py-1.5 text-[13px] hover:bg-zinc-50 disabled:opacity-50"
                  title="从官网重新拉取该插件最新模型列表"
                >
                  <RefreshCw size={14} className={modelRefreshBusy ? 'animate-spin' : ''} />
                  刷新模型
                </button>
                <button type="button" onClick={() => { setPluginExposing(null); setModelSearch('') }} className="p-1.5 rounded-lg text-zinc-400 hover:bg-zinc-100">
                  <X size={16} />
                </button>
              </div>
```

（端点原型见文件 1181-1183 行 `<div className="flex items-center justify-between">...</div>` 的右侧关闭按钮，替换该右侧为「刷新模型 + 关闭」的组合块。`RefreshCw` 已在第 3 行 import。）

- [ ] **Step 4: 类型检查**

Run: `npx tsc --noEmit`（项目根）
Expected: 无错误。
> 若项目 tsc 无独立配置（Vite），改跑：`npm run build`（先确认 package.json scripts）。

- [ ] **Step 5: 提交**（阶段收尾提交）

---

## Task 4: 收尾——全量验证 + 文档同步

**Files:**
- Modify: `docs/PLUGIN-PROVIDERS.md`（§七 管理 API 表，~225-232 行区域）
- 不改代码，仅验证 + 文档。

**Interfaces:**
- Consumes: Task 1-3 产物。

- [ ] **Step 1: 全量单测 + vet**

Run: `go vet ./... && go test -count=1 ./...`（enhance 根目录）
Expected: 全绿。

- [ ] **Step 2: 前端构建**

Run: `npm run build`（若失败以 `npx tsc --noEmit` 为准并报告）
Expected: 成功。

- [ ] **Step 3: 文档同步**（PLUGIN-PROVIDERS.md §七 表追加一行）

```markdown
| `/api/admin/plugins/{id}/refresh-models` | POST | 手动从上游（官网，经插件子进程）刷新该插件模型清单并触发聚合目录重建（`models_all`/`models` 即时更新） |
```

- [ ] **Step 4: 阶段审查 + 提交**

- 本分支全部改动 git status 清点。
- 提交前向用户确认 commit 信息。
- 提交后由用户在 `npm run tauri:dev` 手工验收（暴露模型弹层 → 刷新模型按钮 → 官网最新模型），验收问题记入下一轮。

---

## 提交策略（AGENTS.md）

- 每阶段完成后，**主代理在阶段工作报告中列出将提交的 commit 信息，征求用户确认后 commit**（分阶段 commit：Task1+2 后端一提交、Task3 前端一提交，Task4 文档一提交）。
- 分支上可累积未提交改动，直到用户确认。不得直接 push（用户未要求）。