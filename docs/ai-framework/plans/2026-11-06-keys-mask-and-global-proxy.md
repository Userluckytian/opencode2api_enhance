# 阶段 K：密钥遮罩 + 出口代理统一维护

> **状态：** 已完成（tauri:dev + CDP 真机验证通过，见 `docs/issue-log/2026-11-06.md`）
> **元规范：** `docs/ai-framework/phased-plan-driven.md`
> **实施档位：** 全能
> **子代理：** 不启用（改动集中、需连续上下文）

**Goal:** 解决宿主项目两个误触/重复配置问题：
1. OpenCode 页「密钥通道」卡片默认明文展示全部 key，且随时可改；改为**默认遮罩 + 眼睛图标查看 + 编辑图标进入可改态**。
2. 自定义源编辑弹窗的「出站走节点池代理」开关是**死开关**（后端已改为 `proxy` 字符串，`via_proxy` 不再被读取）；改为**设置页统一维护出口代理**，各消费方只给「使用代理」复选框。

**Architecture:**
- 全局出口代理仍存 `manager.Config.EngineProxies`（`engine_proxies`，免费层引擎本就在用），设置页成为唯一编辑入口。
- 自定义源保存 `use_proxy:true`（不复制 URL）；出站时由 `mergeVendorParams` 解析为「全局列表第一个非 `direct` 项」，因此改全局代理无需重存各源。
- 密钥遮罩纯前端；不改变存储与接口。

---

## 现状与根因

| 项 | 现状 | 结论 |
|---|---|---|
| 密钥通道 | `<textarea>` 恒明文、恒可编辑，误点即改 | 需遮罩 + 编辑态 |
| `via_proxy` 开关 | 前端仍发送，后端 `customProviderInput` 无该字段（零读取） | **死开关**：点了不生效 |
| 后端代理字段 | `custom.ParamProxy = "proxy"`（单 URL），`mergeVendorParams` 转 `StaticProxyTransport` | 保存 `proxy` 才能生效 |
| 出口代理列表 | 在 OpenCode 页编辑 `engine_proxies` | 移到设置页 |

## 变更清单

| 文件 | 变更 |
|---|---|
| `vendors/custom/registry.go` | 新增 `ParamUseProxy = "use_proxy"` 常量 |
| `core/manager/config.go` | 新增纯函数 `FirstEgressProxy([]string) string`（跳过空/`direct`） |
| `models_source.go` | `mergeVendorParams`：`use_proxy` → 解析全局代理为 `StaticProxyTransport` |
| `custom_providers.go` | 视图/入参加 `use_proxy`；兼容旧 `via_proxy`；保存写 `use_proxy`；测试接口按同口径解析 |
| `src/lib/api.ts` | `CustomProviderView` 加 `proxy`/`use_proxy`；入参 `via_proxy` → `use_proxy` |
| `src/pages/CustomModelsPage.tsx` | 死开关换成「使用代理」+ 解析结果提示；行徽标改「走代理」 |
| `src/pages/SettingsPage.tsx` | 新增「出口代理」维护区块（唯一入口） |
| `src/pages/OpenCodePage.tsx` | 删除「出口代理」卡片；密钥通道加眼睛/编辑按钮与遮罩 |
| 文档 | `CHANGELOG.md`（v2.0.0 段）、`docs/CONFIGURATION.md`、`docs/CUSTOM-MODELS.md`、`docs/ROUTING.md`、`docs/issue-log/*` |

## 语义约定

- **全局出口代理**：设置页「出口代理」多行列表，支持 `http/https/socks5/socks5h`，`direct` 表示直连。
- **使用代理**：勾选后该自定义源出站走全局列表**第一个非 `direct` 项**；未勾选 = 直连。
- 全局列表为空时勾选：界面明确提示「未配置」，连通测试直接报错，不静默直连。
- 引擎侧照旧吃完整列表（不改引擎行为）。

## 验收表

| # | 项 | 命令/操作 | 期望 | 结果 |
|---|---|---|---|---|
| 1 | 后端编译 | `go build ./...` | exit 0 | ✅ exit 0 |
| 2 | 静态检查 | `go vet ./core/... ./vendors/... .` | exit 0 | ✅ exit 0 |
| 3 | 相关包测试 | `go test ./core/manager/... ./vendors/...` | ok | ✅ 4 包 ok |
| 4 | 前端类型 | `npx tsc --noEmit` | exit 0 | ✅ exit 0 |
| 5 | 前端构建 | `npm run build` | exit 0 | ✅ 428.61 kB |
| 6 | 解析函数单测 | `go test ./core/manager -run FirstEgressProxy` | PASS | ✅ 6 组用例 |
| 7 | 密钥默认遮罩 | 读代码 + 构建产物 | 仅进入编辑态才出现明文 textarea | ✅ 见 `OpenCodePage.tsx` `SecretList` |
| 8 | 死开关消除 | `grep -rn via_proxy src/` | 0 命中 | ✅ 仅剩历史注释，无字段引用 |
| 9 | UI 点击验收 | tauri:dev + headless Edge CDP | 遮罩/显示/编辑/设置/勾选均符合 | ✅ 6 张截图，`docs/design/screenshots-20261106/` |
| 10 | 代理生效 A/B | mock 代理 18990 + 上游 18991 | 勾选→proxy-ok-model；取消→direct-model | ✅ |
| 11 | 端到端推理 | `POST 44280/v1/chat/completions` | 经代理返回 | ✅ `chatcmpl-proxy` |
| 12 | 未配置代理 | 清空后勾选测试 | 400 且不直连 | ✅ |

## 跳过项（如实登记）

| 项 | 原因 |
|---|---|
| 真实上游代理连通性 | 用本机 mock 代理 + mock 上游验证链路；未接真实付费上游 |
