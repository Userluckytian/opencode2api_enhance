# opencode2api_enhance 编码规范说明

## 命名规范

### 文件命名

| 类型 | 规范 | 示例 |
|------|------|------|
| Vue 组件 | PascalCase | `FormInput.vue`, `ZoomImageViewer/index.vue` |
| 组合式函数 | camelCase + `use` 前缀 | `useToken.ts`, `useECharts.ts` |
| Pinia Store | camelCase + `use` 前缀 + `Store` 后缀 | `useUserStore` |
| API 模块 | camelCase | `auth.ts`, `user.ts` |
| 工具函数 | camelCase | `storage.ts`, `validate.ts` |
| 样式文件 | kebab-case | `custom.css`, `variables.scss` |
| 类型定义 | 文件名小写，类型名 PascalCase | `types.ts` → `FormItemOptions` |

### 变量与函数

```ts
// 组合式函数：use 前缀
const { token, setToken } = useToken()

// 事件处理函数：handle 前缀
function handleClick() { ... }
function handleCheckChange() { ... }

// 计算属性：get 前缀或描述性名称
const isLoggedIn = computed(() => !!token)
const innerValue = computed({ get, set })

// 响应式变量：描述性名称
const viewerVisible = ref(false)
const selectedNodeId = ref<string | number | null>(null)
```

### CSS 类名

```scss
// 组件根类名：组件名 kebab-case
.zv-overlay { ... }
.zv-toolbar { ... }

// 修饰符：BEM 风格
.node-label { ... }
.node-label.is-active { ... }

// CSS 变量：统一前缀
--app-color-primary
--app-bg-container
--app-border-color
```

## Vue 组件规范

### 单文件组件结构

```vue
<template>
  <!-- 模板在最前 -->
</template>

<script setup lang="ts">
// 第三方导入
import { ref, computed } from 'vue'
import { ElMessage } from 'element-plus'

// 本地导入
import type { UserInfo } from './types'

// Props & Emits
const props = defineProps<{ ... }>()
const emit = defineEmits<{ ... }>()

// 状态
const data = ref(null)

// 计算属性
const derived = computed(() => ...)

// 方法
function handleAction() { ... }

// 生命周期
onMounted(() => { ... })
</script>

<style lang="scss" scoped>
/* 组件样式 */
</style>
```

### Props 与 Emits

```ts
// Props：使用泛型定义
const props = defineProps<{
  modelValue: string
  options: SelectOptionItem[]
  control?: SelectControlOptions
}>()

// 带默认值
const props = withDefaults(
  defineProps<{
    loadFn: (parentId: string | number) => Promise<FileNodeItem[]>
    modelValue?: FileNodeItem | null
  }>(),
  { modelValue: null }
)

// Emits：使用泛型定义
const emit = defineEmits<{
  'update:modelValue': [value: any]
  'node-select': [node: FileNodeItem | null]
}>()
```

### 组件命名

```vue
<!-- 组件内 name 由文件名自动推断 -->
<!-- 不使用 vite-plugin-vue-setup-extend -->

<!-- 使用时统一 PascalCase -->
<FormInput v-model="form.name" />
<ZoomImageViewer :src="url" />
```

## TypeScript 规范

### 类型定义

```ts
// 接口使用 PascalCase
export interface FileNodeItem {
  id: string | number
  name: string
  fileType: 'FOLDER' | string
  [key: string]: any  // 允许扩展属性
}

// 类型别名
export type FormItemOptions = Partial<FormItemProps>
export type SelectControlOptions = Partial<SelectProps>
```

### 函数签名

```ts
// 具体类型，避免 any
const handleLoad = async (
  node: any,
  resolve: (data: FileNodeItem[]) => void
) => { ... }

// 回调函数明确类型
const triggerSelectUpdate = (node: FileNodeItem | null) => { ... }
```

## CSS 规范

### 主题变量使用

```scss
// ✅ 正确：使用 opencode2api_enhance CSS 变量
color: var(--app-text-primary);
background-color: var(--app-bg-container);
border: 1px solid var(--app-border-color);

// ❌ 错误：硬编码颜色值
color: #303133;
background-color: #ffffff;

// ❌ 错误：使用其他项目变量
color: var(--next-color-bar);
```

### 样式作用域

```vue
<!-- scoped 样式：组件内使用 -->
<style lang="scss" scoped>
.component-class { ... }
</style>

<!-- 非 scoped 样式：覆盖第三方组件内部样式 -->
<style lang="scss">
.el-tree-node__content { ... }
</style>
```

### 样式引入

```scss
// index.scss 使用 @use 引入
@use './reset.scss';
@use './nprogress.scss';
@use './element.scss';
@use './transition.scss';
@use './theme/variables.scss';
```

## Git 提交规范

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

### scope 范围

`auth`, `api`, `login`, `layout`, `theme`, `i18n`, `map`, `chart`, `user`, `role`, `menu`, `dict`

### 示例

```
✨feat(auth): 添加用户注册接口
🐛fix(login): 修复登录报错弹窗重复显示
♻️refactor(api): 请求拦截器支持 showGlobalError 开关
```

### 规则

- 描述使用**中文**，祈使语气，首字母不大写，结尾不加句号
- 首行不超过 50 个字符
- 正文每行不超过 72 个字符

## 组件开发约定

### 公共组件

- 放在 `src/components/` 下
- 支持 `v-model` 双向绑定
- Props 使用泛型定义，提供默认值
- 样式使用 `--app-*` CSS 变量，适配暗色模式

### 页面组件

- 放在 `src/views/` 对应目录下
- 使用 `CgPanel`、`CgTable` 等公共组件
- API 调用通过 `useAuth`、`useDict` 等组合式函数

### 组合式函数

- 返回值解构使用 `const { ... } = useXxx()` 模式
- 内部状态使用 `ref` 或 `reactive`
- 可选链调用方法避免空值错误

### 样式

- 所有颜色使用 `--app-*` CSS 变量
- 暗色模式通过 `html.dark` 类自动切换
- 组件内使用 `scoped` 样式
- 覆盖 Element Plus 样式时使用非 `scoped` 样式块
