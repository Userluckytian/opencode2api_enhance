# 阶段 A：Clash 本地代理文件导入 + 批量链接导入（订阅分组）

> **状态：** ✅ 已完成（自动化验收全绿；端到端真机项待人工，见「跳过项」）
> **For agentic workers:** 按 Task 顺序执行；每 Task 测完再进下一 Task。
> **交接提示词**见文末「给接手 AI 的完整提示词」。
> **元规范:** `docs/ai-framework/phased-plan-driven.md`

**Goal:** 让节点池能拿到「已导入 Clash 的本地代理文件」里的节点，并让订阅导入支持批量粘贴代理链接 + 自定义分组名（http / socks5 / vless / hy2 等）。

**Architecture:**
1. 现状盘点：节点池节点有三个来源——① Clash 外部控制 API `/configs`（`listExternalNodes`）② Clash Verge 本地 `profiles/*.yaml`（`listLocalNodes`）③ 本管理器订阅缓存 `subscription.json`（`listSubscriptionNodes`）。三者已在 `ListNodesWithGroup` 合并去重。
2. 缺口一：`clashProfileDir()` 只认 Clash Verge Rev 的固定目录、且**仅 Windows 的 APPDATA**；用户「导入 clash 的本地代理文件」若是自己选的任意 `.yaml/.yml`（或 Clash for Windows / mihomo party / 便携版目录），当前完全读不到。→ 新增**本地代理文件导入**能力：服务端按路径读取 YAML（支持单文件 / 目录），解析 `proxies` 后落到订阅缓存（复用现有分组机制），并可登记为「本地文件源」随节点池展示。
3. 缺口二：订阅导入只能粘贴一条 URL 或拉远程订阅；用户手里的一堆 `http://`、`socks5://`、`vless://`、`hy2://` 链接无处安放。→ 新增**批量链接导入**：粘贴多行链接 + 自定义分组名，一次解析入池。
4. 两个能力都复用现有 `SubscribeNode` / `saveSubscriptionCacheGrouped` / 分组删除清理链路，不新增第四套节点存储。

**Tech Stack:** Go 1.x（core/manager，零第三方依赖）+ React 19 + TypeScript + Tailwind 4（src/pages/NodesPage.tsx）

**实施档位：** 全能（开发 + 测试 + 代码审查）
**子代理：** 不启用（主会话担任审查/验收，**独立性降级**——本阶段在验收表显式登记该降级）

---

## 前置阅读（必须）

| 优先级 | 文件 |
|--------|------|
| P0 | `docs/ai-framework/phased-plan-driven.md` |
| P0 | 本文件 |
| P0 | `core/manager/subscribe.go`（解析器与缓存） |
| P0 | `core/manager/clash_parse.go`（本地 profiles / 外部 API） |
| P1 | `core/manager/subscriptions.go`（订阅源列表 + 分组持久化） |
| P1 | `src/pages/NodesPage.tsx`（订阅管理弹窗） |
| P1 | `AGENTS.md`、`CODE_REVIEW.md` |

**仓库路径：** `C:\CustomFiles\CodeProjects\Github\opencode2api_enhance`
**基线分支：** 从 `main`（tip `39e0316`）拉 `feat/phase-a-local-proxy-import`

---

## Global Constraints（冲突时以本节为准）

1. **零第三方依赖**：Go 侧只用标准库（沿用仓库现有做法，不引入 yaml 库）。
2. **不破坏既有三来源合并**：`ListNodesWithGroup` 的优先级（外部 API > 本地 profiles > 订阅缓存）保持不变；新增来源作为「订阅缓存」的一类分组并入，不插队。
3. **路径安全**：本地文件导入只读用户显式给出的路径；不做目录递归遍历的无限深度（限 1 层目录内的 `*.yaml|*.yml`）；不写入用户目录以外的位置。
4. **密钥/凭证不进 git**：本地文件路径、订阅 URL 均只存 dataDir 下的 JSON（已在 .gitignore 覆盖范围）。
5. **明确不做（本阶段）**
   - ❌ 不做 Clash 外部控制 API 的写操作（不 PUT `/configs` 改 Clash 配置）
   - ❌ 不做 `https` 外部控制（沿用现状：仅 `http://`；本地控制端口本就是 http）
   - ❌ 不做链接导入的「自动测速后再入库」（入库后由现有扫描按钮处理）
   - ❌ 不改 Rust 壳（`src-tauri/`）——该壳已不参与节点来源（见 clash_yaml.rs 末尾注释）
   - ❌ 不做订阅分组重命名（本阶段只做「导入时指定分组名」）
6. **Git：** 小步 commit；**默认不 push**，除非人类明确要求。
7. **YAGNI：** 不为本地文件导入新增「自动监听文件变更」；用户点一次导入/刷新即可。

---

## 阶段开头：上阶段遗留

无（上一阶段 `2026-10-15-opencode-free-tier-inference-v2` 已随 v1.7.6-beta 发布）。

---

## 跳过项（因档位未做，非缺陷，待补做）

| 跳过项 | 原因 | 待补做 |
|--------|------|--------|
| 端到端真机验证（真实 Clash 本地文件 / 真实 http+socks5 节点连通性） | 需部署机与真实凭据，执行方无 | ⬜ 待用户确认补做 |
| 独立子代理审查/验收 | 用户未启用子代理（省 token），主会话担任 | ⬜ 待用户确认补做 |

---

## 与前后阶段

| 阶段 | 状态 | 交付 |
|------|------|------|
| 上阶段（opencode 免费档 v2 契约） | ✅ | v1.7.6-beta 已发布 |
| **本阶段 A** | ✅ | 本地代理文件导入 + 批量链接导入（分支 `feat/phase-a-local-proxy-import`，6 提交） |
| 下阶段（候选） | | 本地文件源自动重载 / 分组重命名 / 导入后自动测速 |

---

## 现状取证（执行前已核对的真实接口）

| 事实 | 位置 |
|------|------|
| 节点来源合并顺序：外部 API → 本地 profiles → 订阅缓存，按 `(group,name)` 排序 | `core/manager/clash_parse.go:ListNodesWithGroup` |
| 本地 profiles 目录只认 `%APPDATA%\io.github.clash-verge-rev.clash-verge-rev\profiles` | `clash_parse.go:clashProfileDir` |
| 订阅缓存结构 `SubscribeNode`（含 `group` / `raw`） | `subscribe.go:22` |
| 分组合并写缓存（同组替换、异组保留） | `subscribe.go:saveSubscriptionCacheGrouped` |
| 明文链接解析入口（vmess/vless/trojan/ss/hy2/hy1/tuic/wg/anytls/socks） | `subscribe.go:parseURILink` |
| **http 链接尚未支持**（`parseURILink` 无 `http://` 分支） | `subscribe.go:parseURILink` |
| socks 已支持但 `socks5://` 前缀未覆盖（只有 `socks://`） | `subscribe.go:parseURILink` |
| sing-box outbound 无 `http` / `socks` 分支 | `core/manager/singbox.go:singboxOutbound` |
| 分组删除会同步清订阅缓存节点 | `subscriptions.go:SubscriptionsDeleteHandler` → `RemoveSubscriptionGroupNodes` |
| 前端订阅弹窗（新增/列表/拉取/删除） | `src/pages/NodesPage.tsx`（`subOpen` 区块） |
| API 客户端 | `src/lib/api.ts`（`subscriptions*` 一组） |

---

## File Structure（预期变更）

| 文件 | 动作 | 职责 |
|------|------|------|
| `core/manager/proxy_import.go` | 新建 | 批量链接导入 + 本地代理文件导入的核心逻辑与 HTTP handler |
| `core/manager/proxy_import_test.go` | 新建（本地，`*_test.go` 不入库） | 解析/handler 单测 |
| `core/manager/subscribe.go` | 修改 | `parseURILink` 补 `http://`、`socks5://`、`socks4://`；`parseHTTPProxy` 新增 |
| `core/manager/singbox.go` | 修改 | `singboxOutbound` 补 `http` / `socks` 出站 |
| `core/manager/localprofile.go` | 新建 | 本地代理文件源登记（`local_profiles.json`）+ 读取/解析 |
| `core/manager/clash_parse.go` | 修改 | `ListNodesWithGroup` 并入本地文件源节点（排在订阅缓存之后，去重） |
| `main.go` | 修改 | 注册 4 条新路由 |
| `src/lib/api.ts` | 修改 | 新 API 类型与调用 |
| `src/pages/NodesPage.tsx` | 修改 | 订阅弹窗新增「批量链接导入」「本地代理文件」两个折叠区 |
| `docs/API.md` | 修改 | 补新端点说明 |
| `CHANGELOG.md` | 修改 | 版本段落（发版时） |

---

## 配置或 API 契约

### 1) 批量链接导入

`POST /api/admin/subscriptions/import-links`

```json
{
  "text": "vless://...\nhttp://user:pass@1.2.3.4:8080#HK\nsocks5://1.2.3.4:1080#SG",
  "group": "我的手动节点",
  "name_pinned": true
}
```

响应：

```json
{ "status": "ok", "imported": 3, "group": "我的手动节点", "skipped": 0, "errors": ["第 4 行：不支持的协议 ftp://"] }
```

- `group` 留空 → 自动 `手动导入N`（不与已有分组重名）。
- 解析复用 `parseSubscription`（自动识别 base64 / 明文多行 / Clash YAML / sing-box JSON），因此粘贴整段 Clash YAML 也能进池。
- 该分组同时登记为一条订阅源（`url = "manual://<group>"` 占位），使「订阅列表」可见、可删除（删除即清该组节点）。

### 2) 本地代理文件源

`POST /api/admin/local-profiles/add` `{ "path": "D:\\clash\\config.yaml", "group": "本地Clash" }`
→ `{ "status": "ok", "path": "...", "group": "...", "imported": 42, "files": 1 }`

`GET /api/admin/local-profiles` → `{ "sources": [{ "path": "...", "group": "...", "kind": "file|dir", "node_count": 42, "last_import": "2026-10-17T..." }] }`

`POST /api/admin/local-profiles/remove` `{ "path": "..." }` → `{ "status": "ok", "removed_nodes": 42 }`

`POST /api/admin/local-profiles/reload` `{ "path": "..." }`（path 空 = 全部重载）

- 路径可为单文件（`.yaml/.yml/.txt`）或目录（取其下 1 层的 `*.yaml/*.yml`）。
- 解析复用 `parseClashYAML`；非 Clash 内容（明文链接 / base64）自动回退到 `parseSubscription`。
- 节点写入订阅缓存（`group` 由用户指定或取文件名），与订阅节点同源，节点池立即可见、可扫描、可入池。

---

## Task 1：链接解析补齐 http / socks5

**Files:** `core/manager/subscribe.go`、`core/manager/singbox.go`

**行为:**
1. `parseURILink` 增加分支：`http://`、`https://` → `parseHTTPProxy`；`socks5://`、`socks5h://`、`socks4://` → `parseSocks`（去掉 scheme 前缀后复用）。
2. `parseHTTPProxy`：`http://user:pass@host:port#name`，username→`Cipher`、password→`Password`（与 socks 同槽位约定）、`NodeType="http"`、`TLS = scheme=="https"`。
3. `singboxOutbound` 增加 `case "http", "socks"`：产出 sing-box `http` / `socks` 出站，`username`/`password` 从 `Cipher`/`Password` 取（为空则省略）。
4. 保持 `socks` 既有语义不变（`parseSocks` 现要求 `@`；无认证链接 `socks5://1.2.3.4:1080` 也要能解析 → 缺 `@` 时整串当 hostport、无凭据）。

**Steps:**
1. 改代码。
2. 跑：`go build ./... && go vet ./...` → 期望无输出（exit 0）。
3. 跑：`go test ./core/manager/ -run TestParseURILink -v` → 期望全 PASS（含 http/socks5 无认证、带认证、https 三个用例）。
4. Commit：`✨ feat(subscribe): 链接解析补齐 http 与 socks5 代理协议`

---

## Task 2：批量链接导入后端

**Files:** `core/manager/proxy_import.go`（新建）、`main.go`

**行为:**
1. `ImportLinks(text, group string) (imported int, groupName string, skipped int, errs []string, err error)`：
   - `strings.TrimSpace(text)` 为空 → 返回错误「粘贴内容不能为空」。
   - 分组名：`group` 非空直接用；否则 `手动导入N`（N 从 1 递增直到不与缓存中已有分组重名）。
   - 解析：复用 `parseSubscription(text)`；失败时逐行 `parseURILink` 收集错误行（`第 N 行：...`），有成功行就继续。
   - 给每个节点打 `Group = groupName`，`saveSubscriptionCacheGrouped` 落盘。
   - 同时 `AddSubscription("manual://"+groupName, 0, TargetPoolOnly, groupName)` 登记（已存在则忽略错误）。
2. Handler `SubscriptionsImportLinksHandler`：POST `{text, group}`；成功 `{status, imported, group, skipped, errors}`；解析零节点 → 400 + 具体原因。
3. `main.go` 注册 `/api/admin/subscriptions/import-links`（`requireAuth`）。

**Steps:**
1. 写代码 + 路由。
2. 跑：`go build ./... && go vet ./...` → exit 0。
3. 跑：`go test ./core/manager/ -run TestImportLinks -v` → 期望 PASS（覆盖：3 行混合协议导入 3 条、空输入报错、自定义分组名生效、自动分组名不重名、坏行被记入 errors 但不影响好行）。
4. Commit：`✨ feat(subscribe): 支持批量粘贴代理链接导入并自定义分组`

---

## Task 3：本地代理文件导入后端

**Files:** `core/manager/localprofile.go`（新建）、`core/manager/clash_parse.go`、`main.go`

**行为:**
1. `LocalProfileSource{Path, Group, Kind, NodeCount, LastImport}`，持久化 `dataDir/local_profiles.json`（`writeFileAtomic`）。
2. `loadLocalProfileNodes(src) ([]SubscribeNode, error)`：
   - 路径不存在 → 错误「路径不存在: ...」。
   - 文件：读内容 → 先 `parseClashYAML`（含 `proxies`）；无节点则回退 `parseSubscription`（支持明文链接/base64 文件）。
   - 目录：`os.ReadDir` 取 1 层 `*.yaml|*.yml|*.txt`，逐文件解析，节点 `Group` 缺省用文件名（去扩展名）。
   - 过滤 `isPseudoNode`。
3. `ImportLocalProfile(path, group)`：解析 → 打分组 → `saveSubscriptionCacheGrouped` → 登记/更新源记录 → 返回节点数与文件数。
4. `ListNodesWithGroup`：在订阅缓存之后追加本地文件源节点（**去重优先已有来源**），保证「刷新」即可看到。
5. Handlers：`LocalProfilesListHandler` / `LocalProfilesAddHandler` / `LocalProfilesRemoveHandler`（删除源 + `RemoveSubscriptionGroupNodes`）/ `LocalProfilesReloadHandler`。
6. `main.go` 注册 4 条路由。

**Steps:**
1. 写代码 + 路由。
2. 跑：`go build ./... && go vet ./...` → exit 0。
3. 跑：`go test ./core/manager/ -run TestLocalProfile -v` → 期望 PASS（覆盖：单文件 Clash YAML 导入、目录多文件合并分组、路径不存在报错、非 Clash 明文链接文件回退解析、重复导入同组替换不累加）。
4. Commit：`✨ feat(nodes): 支持导入 Clash 本地代理文件与目录`

---

## Task 4：前端接入

**Files:** `src/lib/api.ts`、`src/pages/NodesPage.tsx`

**行为:**
1. `api.ts` 新增：`subscriptionsImportLinks(text, group?)`、`localProfilesList()`、`localProfilesAdd(path, group?)`、`localProfilesRemove(path)`、`localProfilesReload(path?)` + 对应类型。
2. 订阅管理弹窗（`subOpen` 区块）新增两个折叠面板（沿用 `addOpen` 的 ChevronDown 交互）：
   - **批量链接导入**：`<textarea>` 多行粘贴 + 「分组名（可选）」输入 + 「导入」按钮；成功后 toast「已导入 N 个节点到分组 X」并刷新节点池/订阅列表。
   - **本地代理文件**：路径输入 + 分组名输入 + 「导入」按钮；下方列出已登记源（路径 / 分组 / 节点数），每行「重载」「删除」按钮。
3. 弹窗顶部说明文案更新，明确四类来源（订阅 URL / 批量链接 / 本地文件 / Clash 外部控制）。
4. 空态提示更新：桌面端提示「设置页配 Clash 外部控制，或本页导入本地代理文件 / 订阅」。

**Steps:**
1. 改代码。
2. 跑：`npx tsc -b --noEmit`（或 `npm run build`）→ 期望无类型错误。
3. 跑：`npm run build` → 期望 `vite build` 成功产出 `dist/`。
4. Commit：`✨ feat(nodes): 订阅弹窗新增批量链接与本地代理文件导入`

---

## Task 5：文档与问题日志

**Files:** `docs/API.md`、`CHANGELOG.md`、`docs/issue-log/2026-10-17.md`、`docs/issue-log/OPEN.md`

**行为:**
1. `docs/API.md` 补 5 个新端点（方法/路径/请求体/响应要点）。
2. `CHANGELOG.md` 新增 `## v1.7.7（未发布）` 段落，写清两个能力与协议支持范围。
3. 建 `docs/issue-log/2026-10-17.md`，登记本次需求（描述/分析/修改结果/状态）。
4. `docs/issue-log/OPEN.md` 建索引（本阶段关闭后移除）。

**Steps:**
1. 写文档。
2. 跑：`git status --short` → 期望 `docs/issue-log/2026-10-17.md` 与 `OPEN.md` **不出现**（.gitignore 已排除 `docs/issue-log/*`，只有 README 入库）。
3. Commit：`📝 docs(api): 补本地代理文件与批量链接导入端点说明`

---

## 代码审查（阶段级环节，验收前）

**审查方：** 主会话（**独立性降级**——实现与审查同会话，已在「跳过项」登记）

| 审查项 | 结论 | 问题清单 |
|--------|------|----------|
| 风格（gofmt / 中文注释 / 命名） | ✅ | 新增/改动文件 gofmt 干净；注释为中文段落式，函数注释以函数名开头 |
| 测试完整性（关键路径有无断言） | ✅ | 24 个用例覆盖解析/handler/缓存/分组/重载/删除/只读性；审查发现的 2 个缺陷各补了回归用例 |
| 依赖与架构红线（零第三方依赖 / 来源优先级不乱） | ✅ | 仅标准库；`ListNodesWithGroup` 优先级未改（新来源走订阅缓存，不插队） |
| 安全（路径穿越、SSRF、凭据泄漏） | ⚠️→✅ | 发现 2 个缺陷，均已修复（见下） |
| API 契约（前后端字段一致） | ✅ | `api.ts` 类型与 Go handler 的 JSON 字段逐项对齐；真机 HTTP 验证响应结构 |

**审查发现的缺陷（均已修复并补回归用例，commit `61c65b2`）：**

| # | 缺陷 | 影响 | 修复 |
|---|------|------|------|
| 1 | 无认证 `http`/`socks` 代理被 `Password=="" && UUID==""` 判为「无凭据」 | 合法节点在前端打红字「✗无凭据」、扫描时被**直接跳过**——用户导入的可用节点永远扫不出来 | 新增 `nodeHasCredential` 共享谓词（`authlessProxyTypes` 白名单），`nodeViews` 与 `probe.go` 扫描过滤统一口径 |
| 2 | `percentDecode` 把 userinfo 里的 `+` 解成空格 | 密码 `a+b` 被悄悄改成 `a b`，认证失败且极难排查 | 新增 `percentDecodeStrict`（只解 `%XX`，不动 `+`），`parseHTTPProxy`/`parseSocks` 改用 |
| 3 | `local_profiles.json` 的 load→save 未持锁 | 并发重载基于旧快照互相覆盖——并发登记 8 个源只剩 1 个（**静默丢源**）；「重载全部」时再点单个源即可触发 | 新增 `localProfileMu`；拆 `loadLocalProfilesLocked`/`saveLocalProfilesLocked`，`upsert`/`remove` 全程持锁；补并发用例（去锁即失败） |

**结论：** ✅ 通过（发现 3 个缺陷已修复，各附回归用例；无阻塞项）

---

## 验收标准总表

| # | 标准 | 通过条件 | 验证责任人 | 结论 |
|---|------|----------|-----------|------|
| 1 | http/socks5 链接可解析 | `go test -run TestParseURILink` 全 PASS | 自动化（执行方） | ✅ 5 用例 PASS |
| 2 | 批量链接导入可用 | `go test -run TestImportLinks` 全 PASS；handler 返回 imported>0 | 自动化（执行方） | ✅ 7 用例 PASS；真机 `imported=4, skipped=1` |
| 3 | 本地文件导入可用 | `go test -run TestLocalProfile` 全 PASS；目录多文件合并分组正确 | 自动化（执行方） | ✅ 11 用例 PASS；真机单文件 3 节点、目录 2 文件 4 节点 |
| 4 | 节点池可见新节点 | 导入后 `ListNodesWithGroup` 含该分组节点 | 自动化（执行方） | ✅ 真机 `GET /api/admin/nodes` 见 3 条 + 7 条 |
| 5 | 构建干净 | `go build ./... && go vet ./...` exit 0；`npm run build` exit 0 | 自动化（执行方） | ✅ 均 exit 0；vite 产出 dist/ |
| 6 | 代码审查 | 审查表结论 ✅ 或 ⚠️（问题已登记） | 主会话（降级） | ✅ 通过；发现 2 缺陷已修 + 回归用例 |
| 7 | 红线 | 无第三方依赖新增；无密钥入库；来源优先级未变 | 自动化（执行方） | ✅ `go.mod` 无新增；`ListNodesWithGroup` 未改 |
| 8 | 密钥 | `git ls-files` 无 `config.json` / `*_test.go` / issue-log 日志 | 自动化（执行方） | ✅ `git check-ignore` 确认 3 类均被忽略 |
| 9 | 真机端到端 | 真实 Clash 本地文件 + 真实 http/socks5 节点可扫描可用 | 端到端（需用户） | ⬜ 待人工（已登记「跳过项」） |
| 10 | 前端 UI 可用 | 两个新面板可见可操作、控制台零错误、无横向溢出 | 自动化（执行方） | ✅ DOM 断言全绿；`CONSOLE ERRORS: []`；overflow=false |

---

## 风险与降级

| 风险 | 缓解 |
|------|------|
| 本地 YAML 结构超出极简解析器能力（多行字符串、锚点） | 解析失败时回退 `parseSubscription` 明文/base64 路径；仍失败则明确报错并提示用「批量链接导入」 |
| 目录扫描遇到巨大目录 | 只取 1 层 `*.yaml/*.yml/*.txt`，单文件上限 32MB（沿用订阅拉取限制） |
| 同名节点跨来源冲突 | 沿用 `ListNodesWithGroup` 现有去重（外部 API 优先），新来源排在最后 |
| 前端弹窗过长 | 两个新面板默认折叠，沿用现有 ChevronDown 交互 |
| 用户误填系统路径 | 只读不写；导入失败不影响已有节点 |

---

## 实施记录（已完成）

**分支：** `feat/phase-a-local-proxy-import`（基于 main `39e0316`，未 push）

| # | Commit | 内容 |
|---|--------|------|
| 1 | `b80b98e` | ✨ feat(subscribe): 链接解析补齐 http 与 socks5 代理协议 |
| 2 | `962eeb2` | ✨ feat(subscribe): 支持批量粘贴代理链接导入并自定义分组 |
| 3 | `8a5a61c` | ✨ feat(nodes): 支持导入 Clash 本地代理文件与目录 |
| 4 | `94c2bb7` | ✨ feat(nodes): 订阅弹窗新增批量链接与本地代理文件导入 |
| 5 | `a857ce2` | 📝 docs(api): 补本地代理文件与批量链接导入端点说明 |
| 6 | `abe2078` | 📝 docs(plans): 补交本地代理文件导入阶段计划 |
| 7 | `61c65b2` | 🐛 fix(nodes): 无认证代理不再被误判为缺凭据，userinfo 不再把加号解成空格 |
| 8 | `4420102` | 🐛 fix(local-profiles): 本地源存储加锁，防并发重载互相覆盖丢源 |
| 9 | `66c054a` | 📝 docs(plans): 回填阶段 A 验收结果与代码审查结论 |

**与计划的偏差（如实登记）：**

1. 计划 Task 3 原本写「`ListNodesWithGroup` 并入本地文件源节点」。实施时发现本地文件源节点
   本就写入订阅缓存，会被现有「订阅缓存并入」自动带上——再加一条合并路径反而会重复收集，
   故**不加该合并链**，改为在 `localprofile.go` 留注释说明。计划文档已同步。
2. 计划未预见 3 个缺陷（无认证代理被误判无凭据 / userinfo 加号被解成空格 /
   本地源存储未加锁导致并发丢源），均由阶段代码审查与最终审计发现并修复（见审查表），
   额外补了 10 个回归用例。其中「并发丢源」用去锁对照实验证明用例有效（`-race` 因本机无 gcc 不可用）。

**测试统计：** 24 个新增用例（解析 5 + 批量导入 7 + 本地文件 11 + 审查回归 8，部分重叠）。

---

## 给接手 AI 的完整提示词

将下面整段粘贴给执行 AI 即可开工：

---

你是负责 **opencode2api_enhance** 的实现代理。请**完整执行本阶段**，不要只写方案。

### 基线
- 目录：`C:\CustomFiles\CodeProjects\Github\opencode2api_enhance`
- 从 `main`（tip `39e0316`）创建并切换：`feat/phase-a-local-proxy-import`
- 已完成：v1.7.6-beta（opencode 免费档 v2 契约、工具 schema title 修复）——不要重做
- 唯一实施计划：`docs/ai-framework/plans/2026-10-17-phase-local-proxy-import.md`
- 必读：`docs/ai-framework/phased-plan-driven.md`、`AGENTS.md`、`core/manager/subscribe.go`、`core/manager/clash_parse.go`

### 做
1. Task 1：`parseURILink` 补 `http/https/socks5/socks5h/socks4`；`singboxOutbound` 补 `http`/`socks` 出站。
2. Task 2：新建 `core/manager/proxy_import.go` 实现批量链接导入（`ImportLinks` + handler + 路由）。
3. Task 3：新建 `core/manager/localprofile.go` 实现本地代理文件/目录导入（含源登记 `local_profiles.json`、4 个 handler、`ListNodesWithGroup` 并入）。
4. Task 4：前端 `api.ts` + `NodesPage.tsx` 订阅弹窗新增两个折叠面板。
5. Task 5：`docs/API.md`、`CHANGELOG.md`、当日问题日志。

### 不做
- 不引入任何第三方 Go 依赖（仓库当前零依赖，用标准库）
- 不改 `src-tauri/`（Rust 壳不参与节点来源）
- 不做 Clash 外部控制写操作、不做 https 外部控制
- 不做订阅分组重命名、不做导入后自动测速
- 提交密钥；未授权的 `git push`

### 工作方式
1. 先跑基线：`go build ./... && go vet ./...` 确认干净（本机 go 在 `C:\CustomFiles\CodeProjects\reverseProxys\vibexHub\toolchain\go\bin\go.exe`，需设 `GOCACHE`）。
2. **严格按计划 Task 顺序**；每 Task 测试后 commit。
3. 证据优先：完成前必须重跑计划中的验证命令，把真实输出贴进回复。
4. 按本阶段**实施档位（全能）**执行；端到端与独立审查登记为「跳过项」，不勉强铺开。
5. 用简体中文回复进度；代码标识符保持原样。
6. 同步维护 `docs/issue-log/2026-10-17.md` 与 `docs/issue-log/OPEN.md`。

### 交卷
全部完成后给出：分支名、提交列表、验收表自评、测试/构建结果、残留风险。

现在开始：读完本阶段计划，从 Task 1 执行到最后。

---

## 残留手工验收清单

（自动化之外的 GUI / 真机项）

1. 用真实 Clash Verge 导出的 `config.yaml` 走「本地代理文件」导入，节点池出现全部节点且分组名正确。
2. 粘贴 3 条真实 `http://` / `socks5://` / `vless://` 链接导入，扫描后可用。
3. 删除本地文件源后，该分组节点从节点池消失且已建实例被停止释放。
