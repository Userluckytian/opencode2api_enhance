# 阶段：巡检打真出口（修 poolquality 探测盲区）

> **状态：** 待实施（方案已定，未动代码）
> **分支：** `feature/real-exit-probe`（从 `c5a0d83` 切出）
> **For agentic workers:** 按 Task 顺序执行；每 Task 测完再进下一 Task。
> **元规范:** `docs/ai-framework/phased-plan-driven.md`

## Goal

让实例池的周期巡检**真正经实例的 SOCKS 出口打到上游**，而不是打实例本地 API 端口读内存缓存目录。

改造后，「节点池效果不如参考项目」这一现象的根因被消除：巡检分数从此反映**出口 IP 到上游的可达性与延迟**，而不是「实例进程是否活着」。

## 背景证据（2026-11-03 实测）

### 1. 现有巡检不经过出口

`core/manager/poolquality_socks.go:160` `probeInstanceOnce` 打的是：

```go
url := fmt.Sprintf("http://127.0.0.1:%d/v1/models", inst.Port)
```

`GET /v1/models` 在实例侧走 `chat_handler.go:268` `listModelsHandler`，它读的是**进程内内存缓存** `modelsCache`：

```go
modelMu.RLock()
loaded, models := modelsLoaded, modelsCache
modelMu.RUnlock()
```

只有当缓存为空时才调 `refreshModelCatalogIfDue()` → `models_source.go:511` `refreshModelCatalogLocked()` → `globalAgg.Refresh(ctx)`。而 `globalAgg` 是**进程级全局单例**（`models_source.go:498`），与具体实例的 SOCKS 出口**没有任何绑定关系**。

**结论**：`probeInstanceOnce` 测的是「实例进程是否活着 + 目录缓存是否非空」，**与该实例的出口节点无关**。

`poolquality.go:1-6` 的文件头注释自称「度量整条链路（实例进程 → 节点出口 → 上游 API）」，**与实际行为不符**——这是需要修正的文档债。

### 2. 参考项目的巡检确实经代理打上游

`D:/AI_Projects/refs/Opencode2api/server/egress.js:825` `_probeRoute`：通过代理隧道建立到上游的 socket，主动发 HTTP 请求到 `/zen/v1/models`，并且**要求响应行包含 ` 200`**：

```js
const isOk = targetHost === 'opencode.ai' ? firstLine.includes(' 200') : /^[A-Z\/0-9.]+ [23]\d\d/.test(firstLine);
if (!isOk && recordErrors) {
  this.probeErrors.set(route.id, `Upstream rejected: ${firstLine.slice(0, 40)}`);
}
```

注释明写意图：`Active HTTP probe to verify the proxy IP is clean and not blocked (403/429) upstream`。

即：**403（Cloudflare 挑战）与 429（限流）都被判为不可用**——这正是增强版缺失的判定维度。

### 3. 增强版已具备「打真出口」的能力，只是巡检没用

`core/manager/probe_completion.go:86` `freeCompletionEx` 的第二阶段会发真实 chat：

```go
status, chatResp, chatHeader, err := httpPostJSONProbeResp(port, "/v1/chat/completions", time.Until(deadline), password, chatBody)
```

这个请求走实例的完整链路（实例进程 → 鉴权 → 出口 → 上游）。`probe_node.go:153` 已经在用它做节点扫描。

**所以这不是「新增能力」，而是「让周期巡检复用已有能力」。**

### 4. 参照项目 Bun bridge 与本项无关（避免走回头路）

本次三栈实测（Go crypto/tls / Node fetch / Bun fetch）确认：同一时刻、同一模型 `deepseek-v4.1-flash-free`，三者返回**逐字相同**的 `429 FreeUsageLimitError`；假模型名则三者都返回 `400 Model is unavailable`（身份/指纹层全过）。

本项目 `docs/ai-framework/plans/2026-10-30-remove-free-sidecar.md` 已记录同一结论（Go 直连已过门禁，sidecar 原始理由已失效，`bin/free-sidecar.exe` 作为 opt-in 逃生门保留）。

**本阶段不做**：sidecar / uTLS / Bun 指纹复刻。

## 约束

1. **不改上游请求语义**——只改「探测打哪里」，不动 `vendors/opencode` 的门禁与请求构造。
2. **不改 `Transport` 接口签名**——`core/contract/contract.go:115` 的 `Client(tier, streaming) (*http.Client, string)` 已经返回出口地址，够用。
3. **不改 `freeCompletion` 的语义**——它是节点扫描用的，复用即可，不要为巡检另写一套。
4. **探测不得污染上游免费额度**——chat 探测用 `max_tokens: 1`（`probe_completion.go` 现有做法），且必须尊重 `Retry-After`。
5. **配额耗尽 ≠ 节点坏**——上游全局桶耗尽时，所有节点同时 429。此时巡检**不能**把所有节点标 down。必须区分「这条出口坏了」与「上游整体没额度了」。
6. 密钥不进 git；默认不 push。

## Task 列表

| # | 任务 | 验证命令 | 期望 |
|---|------|----------|------|
| T1 | 引入探测模式开关：`probeInstanceOnce` 增加 `deep bool` 参数，或拆成 `probeInstanceShallow` / `probeInstanceDeep` 两个函数 | `go build ./...` | 编译通过 |
| T2 | 实现 deep 探测：复用 `freeCompletionEx`（`probe_completion.go:86`）发 chat，替换 `probeInstanceOnce` 的 `/v1/models` 弱探测 | `go build ./...` | 编译通过 |
| T3 | 区分「出口故障」与「上游全局配额耗尽」：识别响应体 `FreeUsageLimitError` / 429 + `Retry-After`，此类样本**不计入节点失败**，单列为「上游额度耗尽」状态 | `go build ./...` + 人工目检 | 逻辑分支清晰 |
| T4 | 配置项：新增 `pool_probe_deep`（默认 true）与 `pool_probe_chat_max_tokens`（默认 1），`core/manager/config.go` 与设置页可配 | `go build ./...` | 编译通过 |
| T5 | 探测节流：deep 探测比 shallow 贵，需限制频率（建议 deep 探测窗口 ≥ 5 分钟，shallow 仍 45 秒），避免打爆上游额度 | `go build ./...` | 编译通过 |
| T6 | 修正文档债：`core/manager/poolquality.go:1-6` 文件头注释改为准确描述（当前自称度量「节点出口 → 上游 API」但实际未覆盖） | 人工目检 | 注释与行为一致 |
| T7 | 单测（本地执行，注意 `*_test.go` 不入库） | `go test ./core/manager/ -count=1` | 全绿 |
| T8 | 真实链路验证：起一个实例，确认 deep 探测能区分「出口 403/429」与「出口正常」 | curl + call_log | 分数随出口真实状态变化 |

## 验收表

| 项 | 状态 | 证据 |
|----|------|------|
| 巡检请求实际经 SOCKS 出口打到上游 | ⬜ | deep 探测在实例侧留有上游请求日志（call_log.jsonl 含目标=上游域名） |
| 能识别 Cloudflare 403 为「出口不可用」 | ⬜ | 单测：注入 403 响应 → 样本 OK=false |
| 能识别上游全局 429 为「额度耗尽」而非「节点坏」 | ⬜ | 单测：注入 429 + FreeUsageLimitError → 样本 OK=true 或标记为 quota-exhausted，不计入失败率 |
| 探测不显著消耗上游额度 | ⬜ | 单测/实测：deep 探测频率 ≤ 每 5 分钟 1 次，max_tokens=1 |
| 注释与行为一致 | ⬜ | `poolquality.go` 文件头与实现相符 |
| 构建通过 | ⬜ | `go build ./...` EXIT=0 |
| 单测全绿 | ⬜ | `go test ./core/manager/ ./vendors/opencode/ -count=1` ok |

## 基线说明（重要）

`go test ./...` 在**本地工作区**有 7 个失败 + 2 处包编译失败（根包、`web/`）。已查明**全部与代码状态无关**：

- `.gitignore:55` 的 `*_test.go` 规则把所有测试源码排除在版本库外
- 失败的 `calllog_handler_test.go`、`web/asksuggest_test.go` 等文件未被 git 跟踪，且引用的 `installFakeOpenCodeClient` / `App` 在仓库中**根本不存在**——是历史遗留的孤儿文件
- **干净克隆实测全绿**：`git clone` 到 /tmp 后 `go test ./...` 仅 `ok github.com/6Kmfi6HP/opencode2api/core/manager`

**结论**：仓库基线是健康的。那 7 个失败只污染本地 `go test ./...` 的输出，不代表代码缺陷。本阶段的验证命令一律用**具体包**（`go test ./core/manager/ ./vendors/opencode/`），不用全量。

## 后续方向（不在本阶段范围）

1. **节点池独立菜单 + 设置页瘦身**：把节点管理从设置页移到独立页面（参考项目 `client/src/pages/Settings.jsx` 的 "Egress Proxy Pool" 卡片整块搬出）。
2. **实例池一键启动**：当前 `InstancesPage.tsx`（715 行）功能过载，砍成「一键启动 + 状态表」。
3. **自动代理发现**：参考项目 `server/proxyDiscovery.js` 从 18 个公开源抓取 + 3s 筛选 + 5 连验，不依赖 clash。增强版目前只有订阅导入。
4. **上游额度闸门下沉**：全局额度闸门应下沉到 `vendors/opencode/chat.go` 真实推理路径（当前 `core/manager/freetier_gate.go` 只挂在管理端测试路径）。
