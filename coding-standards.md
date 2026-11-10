# opencode2api_enhance 编码规范

> 本文件为**通用工程约定**，适用于任意语言/技术栈。各语言/框架的**项目专属约定**（如前端组件命名、后端分层规则、特定 Lint 配置）请在「项目专属约定」一章补充，由项目自行维护。

## 核心原则

- **可读性优先**：代码是写给后来者（包括未来的自己）读的。
- **职责单一**：一个函数/类/模块只做一件事；过大时拆解。
- **一致优先**：以项目现有代码的主流写法为主；有约定先遵守约定。
- **最小修改**：改动只动必要部分，不做顺手重构，避免放大 diff。

## 命名规范

- 文件、类、函数、变量的命名必须**表达意图**，避免 `tmp`、`data`、`obj` 这类无意义命名。
- 遵循项目所在**语言/社区的通行命名风格**，并以项目现有代码为准。
- 缩写仅在项目内通用时使用；跨模块命名保持一致。

## 代码结构与组织

- 按**功能/模块**组织文件，而非按文件类型堆叠。
- 单个文件/函数保持合理长度，超出阈值就拆分。
- 公共逻辑抽取复用，避免复制粘贴（DRY）。

## 代码风格

- **格式化交给工具**：优先使用项目配置的 formatter（如 prettier/black/gofmt）与 linter，不靠人工对齐。
- 避免无意义的冗余；注释表达「为什么」而不是「做了什么」。
- 魔法数字/字符串抽成命名常量。

## 错误处理

- 明确错误边界在哪里；不要用空 `catch`/`except` 吞掉异常。
- 关键路径的错误必须记录日志并给出可读提示。
- 对外部输入（用户输入、API 返回值、文件内容）做校验与兜底。

## 安全

- **密钥、token、密码、凭证绝不进 git**；使用环境变量/密钥管理。
- 不硬编码密钥与真实地址。
- 对敏感操作（push 远程、删除、改动生产）需人类明确授权。

## Git 提交规范

> 下列为默认约定，仅供**无既有约定**时使用；若项目已有自己的提交规范，以项目现有规范为准（可直接覆写本节）。

### 格式

```
<gitmoji><type>(<scope>): <中文描述>
```

### type 与 gitmoji

| type | gitmoji | 说明 |
|------|---------|------|
| feat | ✨ | 新功能 |
| fix | 🐛 | 修复 bug |
| docs | 📝 | 文档更新 |
| style | 🎨 | 代码格式（不影响功能） |
| refactor | ♻️ | 重构（非修复或新增） |
| test | ✅ | 添加或修改测试 |
| chore | 🔧 | 构建/工具变动 |
| perf | ⚡ | 性能优化 |
| ci | 🐳 | CI/CD 配置 |
| revert | ⏪ | 回滚 |

### 规则

- 描述使用**中文**，祈使语气，首字母不大写，结尾不加句号
- 首行尽量不超过 50 字符
- 正文每行不超过 72 字符

## 模块/组件开发约定

- 公共可复用模块放入统一目录（如 `src/components/`、`lib/`、`common/`），保持职责单一。
- 对外接口支持统一的输入/输出约定，必要时提供默认值。
- 样式若使用 CSS 变量，统一使用 `--app-*` 前缀（适配主题/暗色切换）。

## 项目专属约定

> 本节按项目实际结构填写（依据 go.mod、Makefile、package.json、vite.config.ts、src-tauri/、core/ 等），AI 与协作者据此执行。

### 技术栈
- 语言/框架：后端 Go 1.22（go.mod：`github.com/6Kmfi6HP/opencode2api`），HTTP 代理 + 管理面，单二进制；前端 React 19 + TypeScript + Tailwind CSS 4，Vite 8 构建；Tauri 2（Rust，`src-tauri/`）打包桌面端
- 构建/运行命令：后端 `make build`（输出 `bin/opencode2api`）、`make release-snapshot VERSION=...`、`make clean`；前端/桌面 `npm run dev`（Vite 固定端口 1420，`/api` 代理到 `127.0.0.1:19090`）、`npm run build`（`tsc -b && vite build`）、`npm run tauri:dev` / `npm run tauri:build`
- 包管理器：npm（仓库仅含 `package-lock.json`）
- 测试命令/工具：后端 `make test`（即 `go test ./...`）；前端测试未约定（package.json 无 test 脚本）

### 前端约定
- 组件命名：PascalCase（`src/components/`，如 `Modal.tsx`、`TitleBar.tsx`）；页面文件 `XxxPage.tsx`（`src/pages/`）
- 状态管理：未引入独立状态库，页面切换与任务状态由 `App.tsx` 内 `useState` 管理并向子组件传递
- 样式作用域 / 主题变量：Tailwind CSS 4 工具类（`@tailwindcss/vite`）；主题 CSS 变量未约定（未见 `--app-*` 定义）
- 路由 / 目录结构：无路由库，`App.tsx` 以 `useState<Tab>` 切换页面；目录为 `src/components/`、`src/pages/`、`src/lib/`

### 后端约定
- 分层结构：`main.go` + 根目录功能文件（admin/auth/chat_handler/convert/models 等）+ `core/`（aggregator、contract、manager、protocol、router）+ `vendors/`（custom、remote）；`providers/`、`plugin/`、`third_party/` 为本地依赖目录
- API 返回结构：管理面 API 经 `writeJSONResp`/`writeJSON` 辅助函数直接返回 JSON；统一响应信封未约定
- 异常/错误定义：上游错误分类统一委托 `core/contract`（`ClassifyUpstreamError`、`RetryAfterMS`），以结构化 `CallRecord` 字段记录
- 数据访问方式：无数据库，文件持久化——`config.json`（配置）、`stats.json`/`node_stats.json`（统计）、`call_log.jsonl`（调用日志）

### 其他
- Lint/格式化工具与配置：Go 用 `gofmt`（`make fmt`，注意当前 Makefile 仅格式化 `main.go`）+ `go vet`（`make vet`）；前端未配置 ESLint/Prettier
- 特殊工具链 / 版本要求：Go ≥ 1.22；Tauri CLI（`@tauri-apps/cli`）；版本号以 `package.json` 为准，由 vite.config.ts 注入 `__APP_VERSION__`
