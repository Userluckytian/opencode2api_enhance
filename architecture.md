# opencode2api_enhance 架构说明

> 碳卫星数据生产系统（Carbon Satellite Data Production System）前端技术架构

## 技术栈

| 分类 | 技术 | 版本 |
|------|------|------|
| 框架 | Vue 3 (Composition API + `<script setup>`) | 3.5 |
| 构建 | Vite 8 + vue-tsc + UnoCSS | 8.1 |
| 路由 | Vue Router 4 (Hash 模式) | 4.6 |
| 状态 | Pinia | 2.3 |
| UI | Element Plus (全局注册) | 2.14 |
| 图表 | ECharts 6 (按需引入) | 6.1 |
| 2D 地图 | Leaflet 1.9 | 1.9 |
| 3D 地图 | Cesium 1.143 (占位) | 1.143 |
| 国际化 | vue-i18n 11 | 11.4 |
| HTTP | Axios + qs | 1.18 |
| 加密 | crypto-js | 4.2 |
| 语言 | TypeScript 6 | 6.0 |

## 目录结构

```
src/
├── api/                    # API 请求层
│   ├── request.ts          # Axios 封装（拦截器、token 注入、错误处理）
│   └── modules/            # 按模块拆分的 API
│       ├── auth.ts         # 认证（/sso-server/auth/* + /tansat-server/auth/*）
│       ├── user.ts
│       ├── role.ts
│       ├── menu.ts
│       └── dict.ts
├── assets/                 # 静态资源
│   └── styles/
│       ├── index.scss      # 入口样式（@use 引入所有子模块）
│       ├── reset.scss      # 全局重置 + 暗色滚动条
│       ├── dark/custom.css # Element Plus 暗色主题覆盖
│       └── theme/          # opencode2api_enhance 自定义主题变量
│           ├── variables.scss
│           ├── light.scss   # --app-* 浅色变量
│           └── dark.scss    # --app-* 深色变量
├── components/             # 公共组件
│   ├── CgIcon/             # 图标组件
│   ├── CgTable/            # 表格组件（分页、loading）
│   ├── CgPanel/            # 面板容器
│   ├── CgChart/            # ECharts 封装
│   ├── CgLeafletMap/       # Leaflet 地图封装
│   ├── CgCesium/           # Cesium 地球（占位）
│   ├── form/               # 表单组件（FormInput, FormSelect, FormDateRange, FormCheckbox）
│   ├── svgIcon/            # SVG 图标组件
│   ├── ZoomImageViewer/    # 图片缩放预览（支持缩放持久化）
│   ├── LazyFileTree.vue    # 懒加载文件树
│   ├── UpdateProfile/      # 个人信息修改（头像 + 别名）
│   └── ChangePassword/     # 修改密码
├── composables/            # 组合式函数
│   ├── useToken.ts         # URL token 提取 + 管理
│   ├── useAuth.ts          # 登录/注册/登出
│   ├── useDict.ts          # 字典数据管理
│   ├── useTheme.ts         # 主题切换
│   ├── useECharts.ts       # ECharts 实例管理
│   ├── useLeaflet.ts       # Leaflet 地图管理
│   └── useTable.ts         # 表格分页/加载管理
├── directive/              # 自定义指令
│   └── index.ts            # v-auth（RBAC 权限控制）
├── i18n/                   # 国际化
│   ├── index.ts
│   └── lang/
│       ├── zh-cn.ts        # 中文
│       └── en.ts           # 英文
├── layout/                 # 布局组件
│   ├── index.vue           # 主布局（Sidebar + Header + Content）
│   └── components/
│       ├── Sidebar.vue     # 侧边栏（菜单）
│       ├── Header.vue      # 顶部栏（用户下拉、主题切换、语言切换）
│       └── SubMenu.vue     # 递归子菜单
├── router/                 # 路由
│   ├── index.ts            # 创建路由实例 + setupGuard
│   ├── routes.ts           # 静态路由配置
│   └── guard.ts            # 全局前置守卫（token 校验）
├── stores/                 # 状态管理
│   ├── index.ts            # Pinia 实例
│   └── modules/
│       ├── app.ts          # 站点配置（loadConfig）
│       ├── user.ts         # 用户信息 + token
│       └── theme.ts        # 主题 + 语言
├── utils/                  # 工具函数
│   ├── storage.ts          # Session / Local 封装
│   ├── crypto.ts           # SHA256 + AES 加密
│   ├── mitt.ts             # 事件总线
│   ├── format.ts           # 日期/数字格式化
│   ├── validate.ts         # 表单校验规则
│   └── loading.ts          # 全局 loading
├── views/                  # 页面视图
│   ├── login/              # 登录/注册
│   ├── business/           # 业务模块
│   │   ├── home/           # 首页
│   │   ├── map/            # 2D 地图
│   │   ├── globe/          # 3D 地球
│   │   └── charts/         # 图表
│   ├── system/             # 系统管理
│   │   └── setting/
│   │       ├── menu/       # 菜单管理
│   │       ├── role/       # 角色管理
│   │       ├── user/       # 用户管理
│   │       └── directory/  # 字典管理
│   └── error/              # 错误页
│       └── 404.vue
├── App.vue
└── main.ts                 # 入口（异步加载配置 → 初始化主题 → 挂载）
```

## 认证流程

### SSO Token 提取

URL 带 `?token=xxx` 参数时自动提取并存储：

```
http://host/#/login?token=xxx
  ↓ useToken.extractTokenFromUrl()
  ↓ Cookie 存储 token
  ↓ replaceState 清除 URL 参数
```

### 登录流程

```
用户输入用户名密码
  ↓ POST /sso-server/auth/login (userName, password, appId=siteId)
  ↓ 响应 { status: 200, data: { userId, userName, userToken, ... } }
  ↓ 存储 token + userInfo 到 SessionStorage
  ↓ 路由跳转到首页
```

### 路由守卫

```
beforeEach:
  1. extractTokenFromUrl() 或 getToken()
  2. 无 token → 重定向 /login?redirect=原路径
  3. 有 token 且目标是 /login → 重定向 /business/home
  4. 有 token → 检查 userStore，加载存储的用户信息，next()
```

## RBAC 权限模型

```
用户 (User) → 1:1 → 角色 (Role)
角色 (Role) → 1:N → 菜单 (Menu)  [menuIds[] 数组]
菜单 (Menu) → M:N → 角色 (Role)  [展示用，数据库关联]
```

- 路由 `meta.roles` 限制访问权限
- `v-auth` 指令控制按钮级权限
- 侧边栏根据菜单数据动态渲染

## 主题系统

### 双重机制

1. **opencode2api_enhance 自定义主题**：`data-theme` 属性控制 `--app-*` CSS 变量
   - 浅色：`#f4f6f9` 背景
   - 深色：`#0f253d` 背景

2. **Element Plus 暗色模式**：`html.dark` 类控制 `--el-*` CSS 变量
   - 使用官方 `element-plus/theme-chalk/dark/css-vars.css`
   - 自定义 `dark/custom.css` 覆盖（深蓝色系）

### 切换逻辑

```ts
// theme store
toggleTheme() → 设置 isDark → applyTheme()
  → document.documentElement.setAttribute('data-theme', 'dark' | 'light')
  → document.documentElement.classList.toggle('dark', isDark)
```

## API 层设计

### 请求封装 (`request.ts`)

- **baseURL**：`/tansat-server`（通过 Vite proxy 代理）
- **认证头**：`Authorization: {token}`
- **站点头**：`siteId: {siteId}`（从 SessionStorage 读取）
- **错误处理**：默认全局 `ElMessage.error()`，支持 `{ showGlobalError: false }` 在组件内 catch 处理
- **响应兼容**：支持 `status: 200` 和 `code: 200` 两种成功判断

### 代理配置

```ts
proxy: {
  '/sso-server' → http://10.0.10.10:8877
  '/tansat-server' → http://10.0.10.10:8877
}
```

### API 模块划分

| 模块 | 前缀 | 说明 |
|------|------|------|
| auth | `/sso-server/auth/` | 登录、注册、登出、修改资料、修改密码 |
| auth | `/tansat-server/auth/` | 重置密码（管理员） |
| user | `/tansat-server/user/` | 用户 CRUD |
| role | `/tansat-server/role/` | 角色 CRUD |
| menu | `/tansat-server/menu/` | 菜单 CRUD |
| dict | `/tansat-server/dict/` | 字典管理 |

## 站点配置

`public/config.json` 在启动时加载，不参与打包，可运行时修改：

```json
{ "siteId": "your-site-id" }
```

加载流程：`main.ts` → `appStore.loadConfig()` → `fetch('/config.json')` → `Session.set('siteId', siteId)`

## 国际化

- 语言：`zh-cn`（中文）、`en`（英文）
- 路由 key 格式：`router.{RouteName}`（如 `router.BusinessHome`）
- 切换存储：`localStorage` 的 `locale` 字段

## 样式规范

- 入口：`@/assets/styles/index.scss`（使用 `@use` 引入子模块）
- CSS 变量：所有自定义颜色使用 `--app-*` 前缀
- 暗色模式：通过 `html.dark` 类 + `data-theme` 属性双重控制
- 组件库：Element Plus 暗色覆盖在 `dark/custom.css`
