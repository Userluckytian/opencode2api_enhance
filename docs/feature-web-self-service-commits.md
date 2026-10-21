# 网页自助发 Key 服务 —— 功能提交清单

> 本文件是 `feature/web-self-service` 分支的**功能档案**：逐个记录该分支独有的 31 个提交，
> 供日后评估「是否/如何把此功能迁移到 main（v2 架构）」时参考。
> 生成日期：2026-08-11（main 已演进为 v2 架构，此分支基于 v1.0.1 时代，未合并未推送远端）。

## 一、这是什么

一个完整的**网页自助发 Key 服务**（"token 自助餐厅"）：管理员配置上游实例与可分发的模型，
用户通过网页自助领取 API Key（含配额/有效期），客户端用该 Key 经网关调用模型。
属于「多用户/多 Key」方向的功能——与 v2 架构规划中"Web 版定位单用户/内网，多用户后续再做"是同一脉络。

## 二、分支概况

- 分支基址：`feb88fb`（v1.0.1 时代）
- 独有提交：**31 个**（`git rev-list --count main..feature/web-self-service`）
- 与 main（v2）关系：**未合并、未推送远端、内容未迁移**

## 三、功能模块清单（按提交归组）

### 核心后端模块（web/ 目录）

| 模块 | 提交 | 功能 |
|---|---|---|
| `web/config` | `034989e` | 配置模块：加载与保存 |
| `web/store` | `1706ef7` | Key 存储与生成 |
| `web/ratelimit` | `89ef918` | 固定窗口限速器 |
| `web/auth` | `8b64c30` | 管理员密码哈希与登录令牌 |
| `web/models` | `1475479` | 从上游拉取模型列表 |
| `web/api` | `54f8dff` | 用户发 Key 接口（含按天 jsonl 调用日志 logger） |
| `web/gateway` | `9d6e50e` | /v1 网关校验与转发（配额校验） |
| `web/admin` | `37552ca` | 管理后台接口 |
| `web/logger` | `f1b4e29` | 按天 jsonl 调用日志（删除 api.go 占位） |

### 前端页面（static/）

| 提交 | 功能 |
|---|---|
| `22ae6a3` | 管理后台前端页面 static |
| `0ea56c4` | 发 Key 前端页面 static |

### 功能增强与修复

| 提交 | 功能 |
|---|---|
| `670bba0` | 程序入口与冒烟测试、运行说明 |
| `c2f11fb` | 部署资产：CI 构建发布、Docker、systemd、nginx HTTPS 反代与部署文档 |
| `a701b00` | 模型列表空值防御 + 页面标题改为"token 自助餐厅" |
| `4116677` | 前端改相对路径支持子路径部署 + 空上游地址友好提示 |
| `0857285` | **上游 API Key 认证**（拉模型/转发带 Key）；默认值：上游 127.0.0.1:18080/v1、Key sk-unified-local、额度 3000 万、有效期 1 天 |
| `ce6e3dc` | 发 Key 返回 base_url 补 X-Forwarded-Prefix 前缀（nginx 子路径访问不再 404） |
| `730e5e6` | handleLogin 读配置加 RLock 消除竞争 |
| `c2242c8` | /v1 usage 兼容 Anthropic input_tokens/output_tokens |
| `a0ae8f5` | 消除网关热路径数据竞争、配额非原子 check-then-act |
| `9ebcf55` | 修复 admin_test.go 两处 go vet 告警 |

### 端口/环境隔离（三环境）

| 提交 | 功能 |
|---|---|
| `d6c49b1` | **数据目录隔离**：OPCODE2API_DATA_DIR 优先，tauri dev 自动用 `-web-dev` 目录 |
| `7bebdd8` | **端口隔离**：debug 网关 22080 / 实例 32000 段 / 探针 38090/48090（release 保持 18080/18100/19090/29090） |
| `e3ffa01` | 前端批量添加去硬编码 18100，由 Rust 条件编译决定（debug 32000 / release 18100） |
| `6ef9a55` | 发 Key 服务端口 18081/23081 三环境隔离 + nginx/Docker/systemd/compose 资产更新 |
| `581f68a` | 前端网关兜底地址 DEV/release 区分（22080/18080） |
| `cfec616` | review 修复：port_suggest 恢复 main 原公式、空 OPCODE2API_DATA_DIR 视为未设置、数据目录创建失败告警 |
| `aaccb15` | **-free 模型名自动映射**（新模型无需手动加别名） |

### 文档

| 提交 | 功能 |
|---|---|
| `9a8974d` | 设计文档（网页自助发 Key 服务） |
| `fd9dd30` | 实施计划（feature/web-self-service） |

## 四、与 v2 架构（main）的关系评估

- 该分支的「网关/上游」逻辑基于 v1.0.1 的 Go 核心，与 v2 的 `core/router` + `vendors/` 架构不同，**不能直接合**，需按 v2 契约迁移。
- 其中「-free 模型名自动映射」（aaccb15）与 v2 的 `core/aggregator` 别名机制**功能重叠**，迁移时可直接吸收进聚合器。
- 「端口/数据三环境隔离」（d6c49b1/7bebdd8 等）在 v2 里已由**环境槽位表**（40000/44100/48200 段）实现，**无需迁移**。
- 真正有独立价值、v2 尚缺的是：**多用户发 Key + 配额 + 限速 + 按天日志**（web/* 模块 + 两个 static 页面）。

## 五、后续动作建议

1. **保留本分支**作为功能储备（远端无备份，删了不可恢复）。
2. 若决定上线"多用户发 Key"：按 v2 架构在 Go core 里新增 `web/` 等价模块（或独立子服务），
   复用本分支的 store/ratelimit/auth/gateway 配额逻辑，UI 页面对齐 v2 六页风格。
3. 在 main 的 ARCHITECTURE-V2-PLAN 的「P4 Web 版定位」处挂一条备注，标注此分支的存在。
