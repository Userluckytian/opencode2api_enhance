# opencode2api_enhance 编码规范说明

> 本项目 = **Go 后端（纯标准库）+ React 前端（Tailwind）+ Tauri 壳**。规范以真实代码为准，坚持零依赖与轻量化。

## 一、Go 代码规范

### 文件与包命名

| 类型 | 规范 | 示例 |
|------|------|------|
| 源文件 | snake_case.go | `chat_handler.go`、`custom_providers.go`、`gateway_timeout.go` |
| 测试文件 | 被测文件 + `_test.go` | `chat_handler_test.go` |
| 包名 | 小写单词，不加下划线 | `package manager`、`package contract` |
| 平台分支文件 | 后缀 `_windows.go` / `_other.go` | `autostart_windows.go`、`netstat_other.go` |

### 标识符命名

- 导出标识符 **PascalCase**，未导出 **camelCase**。
- 常量、构造函数、接收者命名保持简短一致。

```go
// 导出处理函数 PascalCase；内部函数 camelCase
func HandleChat(w http.ResponseWriter, r *http.Request) { ... }
func parseUpstreamUsage(resp *http.Response) (*Usage, error) { ... }
```

### 依赖纪律（零第三方）

- **只用 Go 标准库**，go.mod 不引入任何 require。
- 确需新增第三方依赖，必须在提交说明中论证理由，并评估是否可用标准库替代。

### 错误处理

- 错误向上冒泡时用 `%w` 包装并附上下文，不吞错、不 panic（除非不可恢复）。

```go
if err != nil {
    return fmt.Errorf("加载配置 %s: %w", path, err)
}
```

### 并发与 context

- goroutine 生命周期必须受 `context` 控制，取消信号跨进程/请求链路透传。
- 共享状态用 `mutex` 保护；channel 传递所有权，避免数据竞争（CI 跑 `-race`）。

```go
ctx, cancel := context.WithTimeout(r.Context(), timeout)
defer cancel()
```

## 二、React / TypeScript 规范

### 组件与文件

- 组件文件 **PascalCase.tsx**，放在 `src/pages/` 或 `src/components/`。
- 一律使用**函数组件 + hooks**，不使用 class 组件。

```tsx
// src/pages/InstancesPage.tsx
export function InstancesPage() {
  const [instances, setInstances] = useState<Instance[]>([])
  return <div className="flex flex-col gap-2">...</div>
}
```

### Hooks

- 自定义 hook 以 `use` 前缀命名，放在 `src/lib/` 或就近文件。
- 副作用集中在 `useEffect`，依赖数组完整；避免在渲染中产生副作用。

### 类型

- 开启严格模式，**避免 `any`**；接口/类型用 PascalCase，字段 camelCase。
- 与后端交互的数据结构在 `src/lib/api.ts` 集中定义，字段对齐 Go 侧 JSON tag。

### API 对接层

- 所有对 core 的调用统一走 `src/lib/api.ts` → `/api/admin/*`，桌面与 Web 共用同一层，不散落 fetch。

## 三、样式规范（Tailwind）

- 优先使用 **Tailwind 4 utility class** 内联布局与配色，全局样式集中在 `src/index.css`。
- **不使用 SCSS、不使用 Element Plus、不使用 `--app-*` 变量体系**。
- 暗色/主题用 Tailwind 的 `dark:` 变体或 data 属性方案，保持与七页 UI 一致。
- 图标用 `lucide-react`，不引入额外图标库。

```tsx
<div className="rounded-lg border border-neutral-200 bg-white p-3 dark:border-neutral-800 dark:bg-neutral-900" />
```

## 四、测试规范

- 每步改动后 `go test -count=1 ./...` **全绿**才提交；不触网，以 mock / httptest 为准。
- HTTP 相关测试一律用 `httptest` **随机端口**，禁止占用真实服务端口。
- 任何真实服务启动/测试前，按 `docs/AI-TESTING-GUIDE.md` 做端口与进程检查，并用三件套环境变量隔离：`OPCODE2API_DATA_DIR` / `OPCODE2API_GATEWAY_PORT` / `OPCODE2API_INSTANCE_BASE_PORT`；禁止 kill 非自己启动的 opencode2api / sing-box 进程。
- 新逻辑须有可复现的测试步骤；错误路径也要覆盖测试。
- 前端以 `npm run build`（tsc -b + vite build）通过为门槛。

## 五、轻量化原则

- **少依赖**：不引入图表库、状态管理库等重组件；分析/可视化用纯 CSS 实现。
- **按需添加**：只加有实际使用价值的功能，不为「看起来丰富」堆功能。
- **体积敏感**：UI 与运行时保持精简，避免无意义依赖膨胀拖慢启动、增大打包体积。
- 新功能优先用现有技术栈（Tauri command + React + Tailwind + Go 标准库）完成。

## 六、目录与分层约定

- **core 与 vendor 解耦**：新增供应商 = 在 `vendors/` 加一个文件夹并实现 `core/contract`，**零 core 改动**。
- 管理域逻辑集中在 `core/manager`，协议转换集中在根 main 包，不交叉混放。
- 平台相关实现用 `_windows.go` / `_other.go` 构建标签隔离，公共逻辑不掺平台代码。
- 前端七页 UI 是全平台唯一事实界面，新增页面须先与七页对齐。

## 七、Git 提交规范

### 格式

```
<gitmoji><type>(<scope>): <中文描述>
```

### type 类型

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

### scope 范围（对齐真实模块）

`gateway`、`manager`、`router`、`aggregator`、`contract`、`vendors`、`custom`、`pool`、`proxy`、`socks`、`calllog`、`stats`、`ui`、`tauri`、`docs`、`ci`

### 示例

```
✨feat(custom): 自定义源 key 连续失败熔断冷却自动恢复
🐛fix(gateway): 修复新增供应商后已有供应商全部 502
♻️refactor(router): smart 路由每请求推进游标并加质量加权抖动
```

### 规则

- 描述使用**中文**，祈使语气，结尾不加句号。
- 首行尽量不超过 50 个字符；正文每行不超过 72 个字符。
- 默认不 push 远程；破坏性操作与 push 需人类明确授权。
