# 阶段 v2.2：OpenCode 通道顺序改为「WARP 优先 → 密钥兜底」

> **状态：** 计划已就绪 / 实施中 / 已完成
> **For agentic workers:** 按 Task 顺序执行；每 Task 测完再进下一 Task。
> **交接提示词**见文末「给接手 AI 的完整提示词」。
> **元规范:** `docs/ai-framework/phased-plan-driven.md`

**Goal:** 让 OpenCode 页的免费通道改成**先用 WARP 出口（匿名），WARP 不行了再用 key**——把 key 的额度留给 WARP 兜不住的场景；同时面板如实展示当前真实走的是哪一档。

**Architecture:** 不改引擎二进制，也不改引擎源码。管理器（40000）生成引擎 `config.json` 时把 `anonymous` 由「无 key 才开」改为「由新配置项 `engine_channel_order` 决定（默认 `warp_first`）」，并补上 `models.anonymous_backoff` 熔断块——引擎第 9964407 版原生支持「匿名先试 → 失败按 `KeyTiers` 打 key」（`internal/gateway/upstream.go:232-257`），所以这是一次**配置语义修正**，不是新功能实现。面板新增：通道顺序开关、出口冷却重置按钮、通道状态条按真实顺序判定。

**Tech Stack:** Go 1.22（纯标准库）／ React 19 + Tailwind 4。

**实施档位：** 全能（默认）
**子代理：** 启用（代码审查 + 验收由独立角色担任）

---

## 前置阅读（必须）

| 优先级 | 文件 |
|--------|------|
| P0 | `docs/ai-framework/phased-plan-driven.md` |
| P0 | 本文件 |
| P0 | `docs/issue-log/OPEN.md` |
| P1 | `AGENTS.md`、`CODE_REVIEW.md` |
| P1 | `core/manager/freetier_engine.go`（引擎 config 生成） |
| P1 | `core/manager/config.go`（配置项读写 + ConfigView） |
| P1 | `src/pages/OpenCodePage.tsx`（OpenCode 页） |
| P2 | 引擎源码参考（只读，**不改**）：`D:\AI_Projects\refs\opencode2api-win\internal\gateway\upstream.go`、`internal/models/catalog.go`、`internal/warp/pool.go` |

**仓库路径：** `D:\AI_Projects\opencode2api_guide\opencode2api_enhance`
**基线分支：** `main`（当前 tip `c90868f`）
**目标分支：** `feat/phase-v22-warp-first`

---

## 决策记录（用户已确认）

| # | 决策 | 依据 |
|---|------|------|
| D1 | 落到**配置 + 护栏**层（不是只改文案，也不是裸开 `anonymous`） | 裸开 `anonymous` 时，WARP 挂了会先陪跑一次失败的代理匿名尝试才落 key；加熔断才安全 |
| D2 | 通道顺序做成**可配置**，默认 `warp_first` | 用户诉求是「优先 warp」；但保留 `key_first` 回退档，避免升级即变行为 |
| D3 | **不改引擎**（GPL 隔离：管理器不链接、不内联、不修改引擎源码） | `docs/FREE-TIER-ENGINE.md` §2；引擎已原生支持该顺序 |
| D4 | 补 `models.anonymous_backoff` 熔断 | 引擎 `internal/gateway/gateway.go:148`：nil → threshold 0 → 熔断关闭 |
| D5 | 一并做「出口冷却重置」 | 当前 WARP 池 52 个 `/64` 全在冷却，WARP 起不来；这是 `OPEN.md` 挂着的待决策项，现在是前置条件 |
| D6 | 面板按**真实顺序**展示通道状态 | 现有 `ChannelStatusBar` 写死「key 优先」，与新行为不符 |

---

## 现状事实（有据可查，实施时勿再重复调研）

### 1. 当前是「钥匙优先」，且写死在管理器里

`core/manager/freetier_engine.go:218-221`：

```go
// Anonymous 只在完全没有付费 key 时开启：实测（同一引擎、同样直连）匿名
// 模式请求 big-pickle 恒 429；配一个 zen key 后同一模型立刻 200。
// 配了 key 还留 anonymous=true，引擎仍会优先走匿名通道，继续 429。
Anonymous: len(cfg.EngineZenKeys) == 0 && len(cfg.EngineGoKeys) == 0,
```

> ⚠️ **这段注释在本版引擎已不成立**（`upstream.go:232-257`：匿名失败会进 key tier）。实施时必须改掉这条误导性注释，否则后人不敢开 `anonymous`。

引擎侧对应逻辑（`internal/models/catalog.go:282-291`）：`hasAnonymous` 为真才建匿名路由，否则直接建密钥路由。**所以「WARP 优先」在配置层就被掐掉了，与 WARP 开没开无关。**

### 2. 引擎原生支持「WARP → 密钥」两级降级

`internal/gateway/upstream.go:232-257`（匿名路由）：先 `doAnonymousUpstream`（WARP 出口），失败后 `entering preferred key tiers` 再按 `route.KeyTiers` 顺序打 key。

探测链路同构（`internal/gateway/probe.go:123-157`）：匿名优先、zen key 兜底。

**结论：缺的只是配置，不是路由能力。**

### 3. WARP 池可用时，zen key 打免费模型**也走 WARP**

`internal/gateway/warp.go:38`：

```go
func (g *Gateway) usesWARP(model string, tier config.Tier, anonymous bool) bool {
	return g.warp != nil && g.catalog.IsFreeModel(model) && (anonymous || tier == config.TierZen)
}
```

所以「WARP 优先」真正省下的是 **key 的额度**（key 只在 WARP 通道全挂时才被消耗）。

### 4. 本机 WARP 池当前起不来（前置阻塞）

- `GET http://127.0.0.1:40080/healthz` → **响应里没有 `warp` 字段** = 进程运行在无 WARP 模式（`health.go:74-91`：`g.warp == nil` 时不写该字段）。
- 日志：`warp_startup_fallback` → `warp_retry_failed` attempt 1..9。
- 根因：`config.json.warp-state.json` 里 **52 个 `/64` 全在冷却**（24h 黑名单，见 `internal/warp/pool.go:648-672`），新注册的出口都被判「already active or cooling down」。

**只开 `anonymous` 不解决这个**——引擎会回落到 `proxies: http://127.0.0.1:7890` 的代理匿名通道，而不是 WARP。所以「出口冷却重置」必须与本阶段一起做。

### 5. 引擎二进制确认支持熔断字段

`bin/opencode2api-win.exe` 字符串表实测含 `anonymous_backoff` / `base_hours` / `max_hours` / `threshold`，且 `internal/config/config.go:167` 的 json tag 为 `anonymous_backoff`（嵌在 `models` 块内）。

> 引擎用 `DisallowUnknownFields` 严格解析 config.json（`internal/config/config.go:309`），**写错字段名会让引擎启动失败**。实施时字段名必须精确。

---

## Global Constraints（冲突时以本节为准）

1. **不修改引擎**（源码 + 二进制）。管理器只按配置写出 `config.json`。
2. **密钥不进 git。** 引擎 config.json 写在数据目录（`%APPDATA%/opencode2api-manager/runtime/free-tier/`），不进仓库。
3. **不引入第三方 Go 依赖。** 纯标准库。
4. **配置字段名必须精确**：引擎 `DisallowUnknownFields` 严格解析，写错一个字段名 = 引擎启动失败。
5. **向后兼容**：未配置 `engine_channel_order` 时默认 `warp_first`（本阶段的核心交付），但**保留 `key_first` 档位**以便可回退。
6. **Git：** 小步 commit；**默认不 push**（需人类明确授权）。
7. **YAGNI：** 不做「自动调优出口池大小」、不做「多通道策略 DSL」。本阶段只做「两档顺序 + 熔断 + 冷却重置」。
8. **明确不做（本阶段）：**
   - ❌ 改引擎源码或重编引擎二进制
   - ❌ 自动重置出口冷却（只提供手动按钮）
   - ❌ 改动自定义源 / 供应商服务的出口逻辑
   - ❌ 改调用指标的数据结构（`channel` 字段已有 `anonymous` 值，无需新增）

---

## 阶段开头：上阶段遗留（必填小节）

> 规则：把上一阶段验收表中 ⬜/❌ 项原文抄入（**含测试/验证/代码审查任一不通过项**），标注未通过原因与来源；**本阶段优先修复并回归，全部通过后才允许推进本阶段新功能**。若无遗留写「无」。

| 上阶段遗留项 | 来源（测试/验证/审查） | 未通过原因 | 状态 |
|-------------|----------------------|-----------|------|
| 无（本阶段为独立需求，不承接 v2.1 阶段验收表） | — | — | — |

> 说明：`OPEN.md` 中「2026-11-06 面板改造第一批」「插件面板改造」等条目属**插件面板**范围，与本阶段（宿主管理器 + OpenCode 页）无交集，不在本阶段处理。

---

## 跳过项（因档位未做，**非缺陷**，待补做）

| 跳过项 | 原因 | 待补做 |
|--------|------|--------|
| 端到端真机验证（真发请求看通道切换） | 档位为「全能」（不含端到端） | ⬜ 待用户确认补做 |

---

## 与前后阶段

| 阶段 | 状态 | 交付 |
|------|------|------|
| v2.1 | ✅ | 免费模型「已恢复」误导修复 + 429 额度重置可视化 |
| **本阶段 v2.2** | ⬜ | OpenCode 通道顺序：WARP 优先 → 密钥兜底 |
| 下阶段 | — | （勿塞进本阶段） |

---

## File Structure（预期变更）

| 文件 | 动作 | 职责 |
|------|------|------|
| `core/manager/config.go` | 修改 | 新增 `EngineChannelOrder` 配置项（读写 + ConfigView 暴露） |
| `core/manager/freetier_engine.go` | 修改 | `anonymous` 按通道顺序生成；补 `models.anonymous_backoff`；新增冷却重置方法 |
| `core/manager/freetier_channel_test.go` | 新建 | 通道顺序 → 引擎 config 的映射单测 |
| `core/manager/freetier_cooldown_test.go` | 新建 | 冷却状态文件读取/清空单测 |
| `main.go` | 修改 | 新增 `POST /api/admin/free-tier/reset-warp-cooldown` 路由 |
| `src/lib/api.ts` | 修改 | 新增类型与方法（`engine_channel_order`、冷却重置） |
| `src/pages/OpenCodePage.tsx` | 修改 | 通道顺序开关 + 冷却重置按钮 + 通道状态条按真实顺序判定 |
| `docs/FREE-TIER-ENGINE.md` | 修改 | 补「WARP 优先 → 密钥兜底」语义说明与排障条目 |
| `CHANGELOG.md` | 修改 | 记录本次行为变更 |
| `docs/issue-log/2026-11-06.md` | 修改 | 按天日志（本地，不入库） |

---

## 配置或 API 契约

### 管理器配置（`dataDir/config.json`）

```jsonc
{
  "engine_channel_order": "warp_first",  // warp_first（默认）| key_first
  "engine_anonymous_backoff": {           // 可选，未配置 = 用引擎默认
    "threshold": 3,                        // 连续失败次数 → 熔断
    "base_hours": 1,                       // 首次熔断时长
    "max_hours": 6                         // 指数退避上限
  }
}
```

### 引擎配置（`runtime/free-tier/config.json`，管理器生成）

```jsonc
{
  "anonymous": true,          // ← 本阶段核心变更：由通道顺序决定
  "models": {
    "free_only": true,
    "refresh_seconds": 300,
    "protocols": {},
    "anonymous_backoff": {     // ← 本阶段新增：引擎侧熔断
      "threshold": 3,
      "base_hours": 1,
      "max_hours": 6
    }
  }
}
```

### 新增管理端点

```
POST /api/admin/free-tier/reset-warp-cooldown
→ { "status": "ok", "cleared": 52 }
```

---

## Task 1：配置项与引擎 config 生成（核心语义变更）

**Files:** `core/manager/config.go`、`core/manager/freetier_engine.go`

**行为:**

1. `Config` 新增 `EngineChannelOrder string \`json:"engine_channel_order,omitempty"\``。
2. 新增常量与判定函数：

```go
// 通道顺序档位。
const (
	// ChannelOrderWarpFirst：先走 WARP 出口（匿名），失败再用 key。
	ChannelOrderWarpFirst = "warp_first"
	// ChannelOrderKeyFirst：只用 key（v2.1 之前的行为）。
	ChannelOrderKeyFirst = "key_first"
)

// engineChannelOrder 生效的通道顺序（nil/空 = 默认 warp_first）。
func engineChannelOrder(cfg Config) string {
	switch strings.TrimSpace(cfg.EngineChannelOrder) {
	case ChannelOrderKeyFirst:
		return ChannelOrderKeyFirst
	default:
		return ChannelOrderWarpFirst
	}
}
```

3. `ConfigGet` / `ConfigSet` 各加一个分支：

```go
case "engine_channel_order":
    return engineChannelOrder(cfg), nil
// Set:
case "engine_channel_order":
    v := strings.TrimSpace(value)
    if v != ChannelOrderWarpFirst && v != ChannelOrderKeyFirst {
        return fmt.Errorf("engine_channel_order 只能是 %s 或 %s", ChannelOrderWarpFirst, ChannelOrderKeyFirst)
    }
    cfg.EngineChannelOrder = v
```

4. `ConfigView` 增 `EngineChannelOrder string \`json:"engine_channel_order"\``，`ConfigViewOf` 填 `engineChannelOrder(cfg)`。

5. **`freetier_engine.go` 的 `anonymous` 改为按通道顺序生成**：

```go
// Anonymous 决定引擎是否启用匿名（无 key）通道：
//   - warp_first：始终开。引擎路由顺序是「匿名(WARP) → key tier」，
//     匿名失败会自动落到 key（upstream.go:232-257），key 只做兜底。
//   - key_first：只在完全没有 key 时开（v2.1 之前的行为）。
//
// 为什么开 anonymous 不会让 key 白费：引擎的匿名路由带 KeyTiers，
// 匿名通道失败才进 key tier；key 依然会被用，但排在 WARP 之后。
Anonymous: engineAnonymousEnabled(cfg),
```

```go
// engineAnonymousEnabled 引擎匿名通道开关（按通道顺序）。
func engineAnonymousEnabled(cfg Config) bool {
	if engineChannelOrder(cfg) == ChannelOrderKeyFirst {
		return len(cfg.EngineZenKeys) == 0 && len(cfg.EngineGoKeys) == 0
	}
	return true
}
```

6. **改掉 `freetier_engine.go:218-220` 的过期注释**（「配了 key 还留 anonymous=true，引擎仍会优先走匿名通道，继续 429」在本版引擎已不成立）。

7. **新增 `models.anonymous_backoff`**：`engineModels` 结构体加字段，写进引擎 config：

```go
type engineModels struct {
	RefreshSeconds int            `json:"refresh_seconds"`
	Protocols      map[string]any `json:"protocols"`
	FreeOnly       bool           `json:"free_only"`
	// AnonymousBackoff 匿名通道熔断：连续失败达阈值后暂停该通道，
	// 指数退避到 max_hours。缺省时熔断关闭（引擎 threshold=0），
	// 那样 WARP 挂了每条请求都要先陪跑一次失败的匿名尝试。
	AnonymousBackoff *engineAnonymousBackoff `json:"anonymous_backoff,omitempty"`
}

type engineAnonymousBackoff struct {
	Threshold int `json:"threshold,omitempty"`
	BaseHours int `json:"base_hours,omitempty"`
	MaxHours  int `json:"max_hours,omitempty"`
}
```

**默认值**：`threshold=3, base_hours=1, max_hours=6`（写在管理器侧，不进管理器 config.json，避免配置面膨胀；用户如需覆盖再加 `engine_anonymous_backoff` 配置项——本阶段**先只做常量默认值**，D5 之外不扩张配置面）。

> **YAGNI 决策：** 本阶段熔断参数**固定为默认常量**，不开放成配置项。理由：先把「熔断存在」这件事做对，参数调优等真实反馈。若用户需要再单独加。

8. `engineModels` 的写入处补 `AnonymousBackoff: &engineAnonymousBackoff{Threshold: 3, BaseHours: 1, MaxHours: 6}`。

**Steps:**

1. 实现 1–8。
2. 跑：`go build ./...`
   期望：exit 0
3. 跑：`go vet ./core/manager/`
   期望：exit 0
4. Commit：`✨ feat(opencode): 通道顺序改为 WARP 优先并补匿名熔断`

---

## Task 2：出口冷却状态读取与重置

**Files:** `core/manager/freetier_engine.go`（新方法）、`core/manager/freetier_cooldown_test.go`（新建）

**行为:**

1. 冷却状态文件路径：`<FreeTierEngineDir>/config.json.warp-state.json`（引擎侧路径规则见 `internal/gateway/runtime.go:322-327`：`configPath + ".warp-state.json"`）。

2. 新增：

```go
// WarpCooldownState 出口冷却状态（读 config.json.warp-state.json）。
type WarpCooldownState struct {
	// Path 状态文件路径。
	Path string `json:"path"`
	// Cooling 冷却中的出口前缀数。
	Cooling int `json:"cooling"`
	// Exists 状态文件是否存在。
	Exists bool `json:"exists"`
}

// WarpCooldown 读出口冷却状态（文件不存在 = 无冷却）。
func (m *Manager) WarpCooldown() WarpCooldownState { ... }

// ResetWarpCooldown 清空出口冷却状态（删除状态文件）。
//
// 为什么需要：引擎把被上游拒绝的出口写进 24h 黑名单（warp-state.json），
// 新注册的出口若落在已冷却的 /64 上会被判「already cooling」，
// 于是 WARP 池永远起不来（当前 52 个 /64 全冷却）。删文件即可让引擎
// 重新注册出口；**不影响账号额度**（额度在服务端，本操作只清本地出口账本）。
func (m *Manager) ResetWarpCooldown() (WarpCooldownState, error) { ... }
```

3. **实现要点（安全）**：
   - 只删 `<FreeTierEngineDir>/config.json.warp-state.json` 这**一个**文件，路径必须由 `m.paths.FreeTierEngineDir()` 拼出，**不接受外部路径参数**（防路径穿越）。
   - 文件不存在 → 返回 `cleared: 0`，不报错。
   - 删之前先读出冷却条数放进返回值（面板要显示「清掉了 N 个」）。
   - 删后**不自动重启引擎**（由用户在面板点「重启引擎」，避免长等待阻塞）。
   - 用 `os.Remove`；若文件被占用返回明确错误。

**Steps:**

1. 实现 + 单测（见 Task 3 的测试文件，或本 Task 自带）。
2. 跑：`go build ./... && go vet ./core/manager/`
   期望：exit 0
3. Commit：`✨ feat(opencode): 新增出口冷却状态读取与重置`

---

## Task 3：单元测试

**Files:** `core/manager/freetier_channel_test.go`（新建）、`core/manager/freetier_cooldown_test.go`（新建）

**行为（必须覆盖的断言）:**

`freetier_channel_test.go`：

| 用例 | 断言 |
|------|------|
| `TestEngineChannelOrderDefaultsToWarpFirst` | `engineChannelOrder(Config{})` == `warp_first` |
| `TestEngineChannelOrderKeyFirstExplicit` | 显式 `key_first` 生效；非法值回落 `warp_first` |
| `TestEngineAnonymousEnabledWarpFirstWithKeys` | 有 10 个 zen key + `warp_first` → `anonymous=true`（**这是本阶段核心断言**） |
| `TestEngineAnonymousEnabledKeyFirstLegacy` | 有 key + `key_first` → `anonymous=false`（保持 v2.1 行为） |
| `TestEngineAnonymousEnabledNoKeysAnyOrder` | 无 key + 任意顺序 → `anonymous=true` |
| `TestWriteEngineConfigEmitsAnonymousBackoff` | 生成的引擎 config.json 里 `models.anonymous_backoff.threshold==3`、`base_hours==1`、`max_hours==6`，且 `anonymous==true` |

> 关键：最后一条要**真实调用 `m.WriteEngineConfig()`** 写临时目录，再读回来解析断言——不能只测结构体。

`freetier_cooldown_test.go`：

| 用例 | 断言 |
|------|------|
| `TestWarpCooldownReadsCount` | 写一个含 N 条 cooling 的假状态文件 → `Cooling == N` |
| `TestResetWarpCooldownRemovesFile` | 重置后文件不存在，`WarpCooldown().Exists == false` |
| `TestResetWarpCooldownMissingFileIsNoop` | 文件不存在时重置不报错，`cleared == 0` |
| `TestResetWarpCooldownKeepsOtherFiles` | 只删 warp-state.json，同目录的 `config.json` 不受影响 |

**Steps:**

1. 写测试。
2. 跑：`go test ./core/manager/`
   期望：`ok`（含新用例全过）
3. Commit：`✅ test(opencode): 覆盖通道顺序与冷却重置`

> ⚠️ **执行修正（2026-11-06）**：本仓库 `.gitignore:55` 明确排除 `*_test.go`
> ——「Go test source files — 测试源码不入库（本地保留，测试纪律仍在本地执行）」。
> 因此**新测试文件不提交**（保持本地），验收凭「测试实际跑过 + 输出全过」，
> 不凭「文件进了 git」。若未来要把测试入库，需先与用户确认是否修改该忽略规则。

---

## Task 4：管理端点

**Files:** `main.go`

**行为:**

```go
// freeTierResetWarpCooldownHandler 清空 WARP 出口冷却状态。
//
// 只清本地出口账本（config.json.warp-state.json），不影响上游账号额度。
// 清完需要重启引擎才会重新注册出口（引擎只在启动时读该文件）。
func freeTierResetWarpCooldownHandler(m *manager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		st, err := m.ResetWarpCooldown()
		if err != nil {
			writeErrResp(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSONResp(w, map[string]any{"status": "ok", "cleared": st.Cooling})
	}
}
```

路由注册（挨着现有 free-tier 路由）：

```go
mux.HandleFunc("/api/admin/free-tier/reset-warp-cooldown", loggingMiddleware(requireAuth(freeTierResetWarpCooldownHandler(managerInst))))
```

**Steps:**

1. 实现 + 注册路由。
2. 跑：`go build ./... && go vet .`
   期望：exit 0
3. Commit：`✨ feat(opencode): 新增出口冷却重置端点`

---

## Task 5：前端

**Files:** `src/lib/api.ts`、`src/pages/OpenCodePage.tsx`

**行为:**

### 5.1 `src/lib/api.ts`

- `ConfigView` 类型增 `engine_channel_order: string`。
- `FreeTierEngineStatus` **不需改**（warp 字段已有）。
- 新增类型与方法：

```ts
export type WarpCooldownState = {
  /** 冷却状态文件路径 */
  path: string
  /** 冷却中的出口前缀数 */
  cooling: number
  /** 状态文件是否存在 */
  exists: boolean
}

// 免费层：重置 WARP 出口冷却（清本地出口账本，不影响账号额度）
freeTierResetWarpCooldown: () =>
  req<{ status: string; cleared: number }>('POST', '/free-tier/reset-warp-cooldown'),
```

> 注：需确认 `api.ts` 里 `req` 的 base URL 前缀规则——现有方法写的是 `'/free-tier/status'`（无 `/api/admin`），实施时按现有约定写。

### 5.2 `src/pages/OpenCodePage.tsx`

**a) 通道顺序开关**（放在「接入信息」卡下方或工具条附近）：

- 一个二选一的分段控件（或两个 radio 按钮）：
  - `WARP 优先`（`warp_first`）：先走 WARP 出口，失败再用 key。
  - `密钥优先`（`key_first`）：只用 key。
- 切换后：写 `engine_channel_order` → 重写引擎 config → **异步重启引擎**（复用现有 `doAction('restart')` / `freeTierEngineRestartAsync` 模式）。
- 必须有 `restartingAt` 期间的禁用态（与「只使用免费模型」开关一致）。

**b) 出口冷却重置按钮**（放在「WARP 出口池」卡内）：

- 显示当前冷却数：`{status?.warp_cooling ?? 0}` 之外，还要显示**状态文件里的冷却条数**（因为进程不在 WARP 模式时 `warp_cooling` 恒为 0，而文件里有 52 条）。
- 按钮「重置出口冷却」，点击后调端点，toast 提示「已清除 N 个冷却出口，请重启引擎」。

**c) `ChannelStatusBar` 改为按真实顺序判定**：

现有逻辑（`keyCount > 0 && status?.healthy` → 显示「密钥通道」）必须改。新判定顺序：

1. `channelOrder === 'key_first'` 且有 key 且健康 → 「密钥通道（N 个 key）」
2. `channelOrder === 'warp_first'`：
   - WARP 活跃 > 0 → 「WARP 优先（WARP 出口活跃 N，key 兜底）」
   - WARP 不可用但引擎健康 → 「WARP 不可用，当前回落到 key」
   - 引擎不健康 → 「不可用」
3. 无 key → 「匿名直连」

**必须修正的文案**（现有文案在新语义下是错的）：

- `src/pages/OpenCodePage.tsx:1086`「已配 key，匿名通道自动让位；key 会均匀绑定到各出口」→ 改为「优先用 WARP 出口；WARP 不通时才消耗 key」
- `src/pages/OpenCodePage.tsx:1058` 注释「v2.0 起通道是「key 优先 → WARP → 匿名直连」三级降级」→ 更新为新语义
- `src/pages/OpenCodePage.tsx:967-971` 密钥卡提示「完全不配密钥（纯匿名）时，big-pickle 等模型会稳定返回 429……免费层建议至少填一个 oc_sk_ 密钥」→ 保留「建议至少填一个 key」（匿名通道仍会 429，这是引擎实测结论），但**补一句**「key 现在只在 WARP 不通时兜底」。

**Steps:**

1. 实现 a/b/c。
2. 跑：`npx tsc --noEmit`
   期望：exit 0
3. 跑：`npm run build`
   期望：exit 0
4. Commit：`✨ feat(opencode): 面板加通道顺序开关与出口冷却重置`

---

## Task 6：文档

**Files:** `docs/FREE-TIER-ENGINE.md`、`CHANGELOG.md`、`docs/issue-log/2026-11-06.md`（本地）

**行为:**

1. `docs/FREE-TIER-ENGINE.md` §5「WARP 与出口代理」补一段「通道顺序」：
   - `warp_first`（默认）：引擎 `anonymous=true` → 匿名(WARP) → key tier。
   - `key_first`：v2.1 行为。
   - 排障表加两行：
     - 「面板显示 WARP 优先但出口全冷却」→ 出口冷却状态文件有 24h 黑名单，用面板「重置出口冷却」再重启引擎。
     - 「配了 key 仍 429」→ 匿名通道也受免费额度限制，key 是兜底不是保证；额度是账号级。

2. `CHANGELOG.md` 记录本次行为变更（**行为变更**：默认通道顺序变了，需显式提示）。

3. `docs/issue-log/2026-11-06.md` 按天日志：描述 / 分析 / 修改结果 / 状态（含验证证据）。

4. `docs/issue-log/OPEN.md` 同步：
   - 把「2026-11-06 免费模型「已恢复」误导…」等已验证项按规则处理（本阶段只动与本阶段相关的行）。
   - 新增/更新本阶段条目。

**Steps:**

1. 改文档。
2. 跑：`git diff --stat`
   期望：只有预期文件变更；`docs/issue-log/2026-11-06.md` **不应出现在暂存区**（`.gitignore` 已忽略）。
3. Commit：`📝 docs(opencode): 补充通道顺序语义与排障说明`

---

## 代码审查（阶段级环节，验收前）

**审查方：** 独立子代理（非本阶段实现者）

**审查面**（按项目补）：代码风格 / 测试完整性 / 依赖合理性 / 架构红线（GPL 隔离：不得改引擎、不得把引擎源码内联）／ 安全（密钥、路径穿越、越权）／ API 契约一致性（字段名与引擎 `DisallowUnknownFields` 严格匹配）

| 审查项 | 结论（✅/⚠️/❌） | 问题清单 |
|--------|------------------|----------|
| 风格 | | |
| 测试完整性 | | |
| 依赖与架构红线 | | |
| 安全 | | |
| API 契约 | | |

**结论：** ✅ 通过 / ⚠️ 有条件通过（问题进验收表，❌ 下放下阶段）/ ❌ 不通过（阻塞）

---

## 验收标准总表

| # | 标准 | 通过条件 | 验证责任人 |
|---|------|----------|------------|
| 1 | 通道顺序语义生效 | `warp_first` + 有 key → 引擎 config.json `anonymous == true` | 自动化（执行方） |
| 2 | 熔断已下发 | 引擎 config.json 含 `models.anonymous_backoff`（3/1/6） | 自动化（执行方） |
| 3 | 冷却重置可用 | 端点返回 `cleared` 数；文件被删；同目录其他文件不受影响 | 自动化（执行方） |
| 4 | 单测 | `go test ./core/manager/` 全过（含 10 条新用例） | 自动化（执行方） |
| 5 | 构建 | `go build ./...`、`go vet ./core/manager/ .`、`npx tsc --noEmit`、`npm run build` 全 exit 0 | 自动化（执行方） |
| 6 | 前端文案一致 | 无「key 优先」残留口径；状态条按真实顺序判定 | 自动化（执行方）+ 人工抽查 |
| 7 | 代码审查 | 审查结论 ✅ 或 ⚠️（问题已登记）；❌ 不通过则下放下阶段 | 独立角色（非实现者） |
| 8 | 红线 | 引擎源码/二进制未被修改；`git diff` 无引擎相关文件 | 自动化（执行方） |
| 9 | 密钥 | `git ls-files` 无敏感文件；`docs/issue-log/2026-11-06.md` 未入库 | 自动化（执行方） |
| 10 | 引擎能带新 config 启动 | 真机重启引擎后 `/healthz` 仍 200 且 `warp` 字段出现（若 WARP 注册成功） | 端到端（需用户/部署机） |

---

## 风险与降级

| 风险 | 缓解 |
|------|------|
| 引擎 `DisallowUnknownFields` 因字段名写错而启动失败 | Task 3 用例真实写盘再读回解析；字段名对照 `internal/config/config.go:162-176` |
| 开 `anonymous` 后 WARP 不通导致**每条请求**先陪跑失败匿名尝试 | 熔断块（threshold=3）——连续失败即暂停匿名通道，key 直接接管 |
| 匿名通道熔断触发后 WARP 恢复了，但熔断要等 1 小时 | 引擎的熔断在匿名通道成功一次即自动关闭（`pool.go:210` `NoteSuccess`）；面板「重启引擎」也能立刻重置 |
| 引擎未带 WARP 启动（当前本机状态） | 引擎会 `warp_startup_fallback` 并后台重试；面板展示真实状态，不假装 WARP 可用 |
| 用户升级后行为突变（原来 key 优先） | 提供 `key_first` 回退档；CHANGELOG 显式标注行为变更 |
| 出口冷却重置误清了别的东西 | 路径只由 `FreeTierEngineDir()` 拼出，不接受外部参数；单测断言同目录其他文件不受影响 |

---

## 给接手 AI 的完整提示词

将下面整段粘贴给执行 AI 即可开工：

---

你是负责 **opencode2api_enhance** 的实现代理。请**完整执行本阶段**，不要只写方案。

### 基线
- 目录：`D:\AI_Projects\opencode2api_guide\opencode2api_enhance`
- 从 `main`（tip `c90868f`）创建并切换：`feat/phase-v22-warp-first`
- 已完成：v2.1 的免费模型可用性展示修复（勿重做）
- 唯一实施计划：`docs/ai-framework/plans/2026-11-06-warp-first-channel.md`
- 必读：`docs/ai-framework/phased-plan-driven.md`、`AGENTS.md`、本计划全文

### 做
1. **Task 1**：新增 `engine_channel_order` 配置项（默认 `warp_first`），改 `freetier_engine.go` 让 `anonymous` 按顺序生成（`warp_first` → 恒 true），并在引擎 config 里补 `models.anonymous_backoff`（threshold=3 / base_hours=1 / max_hours=6）。**同时改掉 `freetier_engine.go:218-220` 那条已过期的注释。**
2. **Task 2**：新增 `WarpCooldown()` / `ResetWarpCooldown()`，操作 `<FreeTierEngineDir>/config.json.warp-state.json`，路径不接受外部参数。
3. **Task 3**：写 10 条单测（6 条通道顺序 + 4 条冷却重置），其中必须有一条**真实调用 `WriteEngineConfig()` 写临时目录再读回解析**。
4. **Task 4**：`POST /api/admin/free-tier/reset-warp-cooldown` 端点。
5. **Task 5**：前端加「通道顺序」开关、「重置出口冷却」按钮，并把 `ChannelStatusBar` 改成按真实顺序判定；**修正所有「key 优先」残留文案**。
6. **Task 6**：更新 `docs/FREE-TIER-ENGINE.md`、`CHANGELOG.md`、`docs/issue-log/2026-11-06.md`、`docs/issue-log/OPEN.md`。

### 不做
- 不改引擎源码，不重编引擎二进制（GPL 隔离红线）
- 不把熔断参数开放成配置项（本阶段用固定默认值）
- 不做自动重置冷却 / 不做出口池自动调优
- 不提交密钥；不执行 `git push`

### 工作方式
1. 先跑基线：`go build ./... && go test ./core/manager/ && npx tsc --noEmit`，确认干净。
2. **严格按计划 Task 顺序**；每 Task 测完 commit，commit message 用仓库规范（`<gitmoji><type>(scope): 中文说明`，不加句号）。
3. 证据优先：完成前必须重跑全部验证命令并贴真实输出。
4. 用简体中文回复进度；代码标识符保持原样；注释写「为什么」不写「是什么」。
5. 计划里的行号可能因前序改动漂移 —— **以符号名定位**（如 `engineAnonymousEnabled`），不要死抠行号。

### 交卷
全部完成后给出：分支名、提交列表、验收表自评、测试/构建真实输出、残留风险。

现在开始：读完本阶段计划，从 Task 1 执行到最后。

---

## 残留手工验收清单

（自动化之外的 GUI / 真机项）

1. 打开 OpenCode 页 → 通道顺序显示「WARP 优先」→ 切到「密钥优先」→ 引擎重启 → 观察 `runtime/free-tier/config.json` 的 `anonymous` 变 `false` → 切回 `warp_first` → `anonymous` 变 `true`。
2. 点「重置出口冷却」→ toast 提示清掉 N 个 → 检查 `config.json.warp-state.json` 消失 → 点「重启引擎」→ 看引擎 stdout 是否出现 `OpenCode WARP/... pool ready`（不再 `warp_startup_fallback`）→ 面板「活跃出口」> 0。
3. 发一条免费模型请求，看 `runtime/free-tier/stdout.log` 的 `request_routed` 事件 `channel` 字段是 `anonymous` 还是 `key`。
4. WARP 不可用时发请求，确认 key 通道接管且**没有**每条请求都陪跑一次失败的匿名尝试（熔断生效后 `anonymous_breaker_open` 应出现在日志里）。
