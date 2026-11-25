# LOOMy 插件面板改版前调研：现状功能 / 可扩 / 不可为

> 日期：2026-11-05 · 状态：**已调研，待用户拍板**
> 输入：设计稿 `docs/design/console-mockup.html`（13 视图 / 11 主题）
> 现状基线：`D:\Program Files\opencode2api\bin\providers\loomy\`（v1.8.0，进程在跑，面板 `127.0.0.1:11457` 在线）
> 源码基线：`D:\AI_Projects\_plugin-work\phase5\loomy`（= 已装 exe，逐字节核对命中）
> 本阶段**只读**，未改任何源码、未重启插件、未动 `data/`

---

## 0. 三个必须先知道的事实

### 0.1 改代码要改 phase5 副本，不是 `D:\AI_Projects\loomy2api`

| 项 | `D:\AI_Projects\loomy2api` | `_plugin-work\phase5\loomy` | 已装 exe |
|---|---|---|---|
| `pluginVersion` | **1.5.1** | **1.8.0** | 1.8.0 |
| `panel.go` 内嵌 HTML | 深色靛蓝 `#0b0f17` | 暖白抹茶 `#F7F6F1` | = phase5 版 |
| `main.go` 根路径 | `FileServer(".")` 暴露整个 cwd | 内嵌 `panelHTML` | = phase5 版 |
| `index.html` | 43 KB 旧面板（已废弃） | 已删除 | 无 |

核对方法：从 exe 里按 `<!DOCTYPE html>` 抽出 HTML 段（6572 B），与 phase5 `panel.go` 的
`const panelHTML` **逐字节一致**；再与线上 `/` 实际返回的 6572 B 一致。三方对齐，无歧义。

> **`D:\AI_Projects\loomy2api` 已过期**，在上面改等于把面板退回深色旧版。建议先把 phase5
> 并回该目录（或直接以 phase5 为准），再开工。

### 0.2 现有面板只有 4 张卡，设计稿 13 页里**现存 0 页**

线上 `/` 只有：网关接入 / 积分余额 / 宠物打工 / 登录态·日志。
后端只有 2 个接口：`GET /api/status`、`POST /api/claim`（CORS 全开，`127.0.0.1` 绑定）。
契约面只有 2 个：`GET /v1/models`、`POST /v1/chat/completions`。

### 0.3 插件模式**零落盘**

积分、领取状态、请求日志全在内存，进程重启即清空；`reqEntry` 只有
`time/method/path/status/duration/bytes/key/err`，**没有 model / tokens / points / ttfb**。
要支撑设计稿的「调用日志」「用量统计」，必须新增 `data/` 落盘层（现状连 `data/` 目录都没建）。

---

## 1. LOOMy 现有能力（= 面板真能做的）

### 1.1 管理 API（2 个）

| 端点 | 返回 | 对应设计稿视图 |
|---|---|---|
| `GET /api/status` | `version` / `plugin_id` / `session_masked` / `points{balance,daily,available}` / `claim{claimed,points,date,last_at,last_error,history[20]}` | 仪表盘 / 积分与余额 / 任务 |
| `POST /api/claim` | `{ok, granted_points, business_date, duplicate}` | 签到任务（宠物打工） |

### 1.2 插件契约面（2 个，宿主调用）

| 端点 | 说明 |
|---|---|
| `GET /v1/models` | 上游 `/models` 透传，并补 `context_window = context_length`（宿主只认这个键） |
| `POST /v1/chat/completions` | SSE 逐块 flush 透传；自动补 `traceparent`（缺了上游会挂死） |

### 1.3 后台循环（已有）

| 循环 | 周期 | 说明 |
|---|---|---|
| 宠物打工领取 | 启动 + 每 30 分钟 | 先查快照，未领满才 POST `tier_3`，重试 3 次，服务端 duplicate 兜底 |
| provider.json 热重载 | 3 秒 | 只读 `session`，不写回 |
| 积分缓存 | 60 秒 | 避免面板轮询打爆上游 |

### 1.4 登录能力（源码有，插件模式未接线）

`auth.go` 实现了讯飞 caccount 手机验证码 / 微信扫码登录，但**插件模式的 `ServeHTTP` 不暴露
`/api/auth/*`**（那是独立代理模式的路由）。要启用需在 `plugin.go` 补路由。

---

## 2. 上游真实能力（本次用线上 session 实测，均 HTTP 200）

这是判断「能不能扩」的唯一依据，全部实跑，不是猜。

| 接口 | 实测结果 | 可用性 |
|---|---|---|
| `/api/v1/models` | 10 个模型，带 `context_length` / `max_output_tokens` / `capabilities`(vision/reasoning/function_calling) / `type`(chat,image) / `reasoning_efforts` | ✅ |
| `/api/v1/chat/completions` | `usage.points_consumed` **有值**；流式带末帧 usage；`reasoning_effort` 生效；`auto` 模型会路由到真实模型并在 `model` 字段返回 | ✅ |
| `/api/v1/points/records` (v1) | `balance/dailyBalance/availableBalance` + 台账 17625 条（`modelName`/`description`/`consumeSource`/`balanceBefore/After`） | ✅ |
| `/api/v2/points/records` | 按 `chat_aggregate` 聚合，带 `chatId` / `direction` / `points` | ✅ |
| `/api/v1/points/by-chat` `/by-turn` | 按对话 / 按轮**精确扣分**（`points_consumed` / `ledger_count`） | ✅ |
| `/api/v1/pet-work` | `earnedPointsToday` / `rewardedTierIds` / `dailyRewardCapped` / `lastGrant` / `config.tiers`（tier_1=60, tier_2=100, tier_3=150 分） | ✅ |
| `/api/v1/onboarding/tasks` | 8 项新手任务合计 **10000 分**（`first_message` 500 / `pick_skill` 1000 / `generate_ppt` 1500 / `set_schedule` 1000 / `install_skill` 1500 / `configure_remote` 1000 / `create_soul` 1500 / `share_soul` 2000） | ✅（本账号已全完成） |
| `/api/v1/points/activation` | `{activated, appliedInvitationCode}` | ✅ |
| `/api/v1/invitation-codes` | 邀请码列表（`inviteCode` / `usedCount` / `maxUses` / `status`） | ✅ |
| `/api/v1/points/redemption-codes/redeem` | 兑换码（POST） | ✅ |
| `/api/v1/images/generations` | **可用**，返回图片 URL | ✅ |
| `/api/v1/home-cards` `/api/v1/public/promo-banners` | 首页运营位 / 活动位 | ✅ |
| `/api/v1/skills/store/list` | 技能商店 | ✅ |
| `/api/v2/buddies` `/api/v2/buddy-teams` | 角色模板 / 团队模板 | ✅ |
| `/api/v1/messages` | **仅 `spark-x2.6`** 走 anthropic 协议成功；其余模型 503 `no_provider_error` | ⚠️ 部分 |
| `/api/v1/team-points/*` | `100001 当前未切换到团队`（个人号不可用） | ⚠️ |
| `/api/v1/responses` `/api/v1/embeddings` | **404，上游不存在** | ❌ |
| `/api/v1/user/current-team` `/api/v1/points/first-login` | **404** | ❌ |

---

## 3. 可以扩充（设计稿有 / 现在没有 / 做得到）

按「投入产出」分四档。

### A 档：纯前端 + 已就绪数据，1~2 天

| # | 设计稿视图 | 要做的事 | 数据来源 |
|---|---|---|---|
| A1 | 仪表盘 | 4 个 KPI（积分余额 / 今日额度 / 可用合计 / 今日领取）+ 领取状态时间线 | `/api/status` 已有 |
| A2 | 积分与余额（单号版） | 余额 / 日额度 / 可用合计 + 消费台账表（模型、方向、扣分、余额前后、时间） | `/api/v1/points/records` 现成 |
| A3 | 任务中心 | 宠物打工（已有）+ 新手任务 8 项进度条（`earned/total`）+ 邀请码列表 + 兑换码输入 | `/pet-work` `/onboarding/tasks` `/invitation-codes` `/redemption-codes/redeem` |
| A4 | 对话调试 | 模型下拉 + 流式/非流式 + 多轮；显示「请求模型 → 真实模型」+ `points_consumed` + token 数 + 耗时 | 需服务端代转（见 §5.1），`usage` 字段现成 |
| A5 | 登录态 / 续期 | session 掩码、剩余天数倒计时、过期告警 | `/api/status` 已有 `session_masked`，需补 `updatedAt` |
| A6 | 侧栏 + 分组导航 + 11 主题 + 动效 | 纯 CSS/JS，`data-palette` + localStorage | 设计稿现成 |
| A7 | 设置页（只读） | 展示 panel_port / 积分告警阈值 / 插件版本 / 上游地址 / 数据目录 | provider.json 现成 |

### B 档：需补 1~2 个只读 API，3~5 天

| # | 设计稿视图 | 要做的事 | 缺口 |
|---|---|---|---|
| B1 | 调用日志 | 时间 / 请求模型 / 真实模型 / 状态 / 输入 / 输出 / 积分 / 耗时 / TTFB，可筛选可导出 | `reqEntry` 缺 model/tokens/points/ttfb；需扩字段 + 解析 SSE 末帧 |
| B2 | 用量统计 | 按天趋势 + 按模型占比 + 近 7/30 天 | 需 `data/usage.json` 按天聚合 |
| B3 | 仪表盘「近 24 小时调用」 | 小时级折线（调用/Token/错误） | 同上，需落盘才有历史 |
| B4 | 模型目录页 | 10 个模型 + 上下文/输出/能力徽章（vision/reasoning/tools）+ `type` 区分 chat/image | `/v1/models` 已带全部字段，前端渲染即可 |
| B5 | 设置页（可写） | 改 `panel_port` / `points_alert_threshold` / `session` | 见 §5.2（插件自写 provider.json） |
| B6 | 链路与出口 | `proxy_url`（HTTP/SOCKS）+ 连通性探测 | **上游 `loomyad.xunfei.cn` 在国内可直连**，代理非必需；仅保留配置项 + 探测按钮 |

### C 档：需改引擎行为，1~2 周

| # | 项 | 难点 |
|---|---|---|
| C1 | 积分低水位告警（阈值已配但**代码里没实现**） | `points_alert_threshold` 只出现在 `_hint` 文本和自举默认值，**无任何读取逻辑**；需补判断 + 面板红条 + 事件日志 |
| C2 | session 过期前自动提醒 / 一键续期 | 续期需讯飞签名凭据（`.env` 里是空占位），且 `auth.go` 未接入插件模式 |
| C3 | 事件日志（注册/领取/切号/告警） | 设计稿 C8 规范要求 `data/events.jsonl` + 面板时间线 |
| C4 | 日志轮转 | 现在日志是内存环形 500 条，落盘后需 10MB×5 轮转 |

### D 档：纯视觉（设计稿核心资产，零后端）

D1 11 套主题 / D2 动效 6 件套 + 环境画布 / D3 侧栏 5 组导航 / D4 数字滚动 + 图表描线 +
表格行入场 / D5 抽屉 + toast + 骨架屏 / D6 设计系统页。

---

## 4. 确实做不到 / 不该做

| 设计稿项 | 判定 | 理由 |
|---|---|---|
| **账号池**（12 号 / 批量签到 / 导入导出 / 按号积分对比） | ❌ 无对象 | LOOMy 是**一人一 session**，没有多账号体系。日上限 1000 分固定，多号也无额外收益。设计稿的「账号池」在 LOOMy 语义下 = 一个账号 |
| **自动注册**（浏览器渲染 + Turnstile + 夜间窗口 + 注册队列） | ❌ 做不到 | LOOMy 只支持**手机号短信验证码**登录（`isCNPhone` 校验 11 位），无邮箱/密码注册通道。要自动注册需短信接码平台 + 破风控，与本插件无关且高风险 |
| **临时邮箱**（多源 + 自动取码） | ❌ 没有对象 | 同上：登录链路不经过邮箱，面板放邮箱源是空壳 |
| **负载与切号**（选号策略 / 粘性 / 402·429 换号 / 冷却 / 最大换号轮数） | ❌ 无对象 | 单 session 无号可切；上游 402/429 只能退避重试，不能「换个号再试」。宿主聚合器的 failover 是跨供应商的，与本插件内部无关 |
| **按号出线 / 每号独立端口 18301** | ❌ 无对象 | 同上，只有一个号 |
| **链路与出口：Clash 探测 + 自动切节点** | ⚠️ 能做但不建议 | 上游国内直连可用，切节点会**改变本机其它应用出网**。建议只保留「出站代理」一个输入框 + 探测按钮，不做自动切换（设计稿自己也写「自动切换默认关闭」） |
| **调用日志 → 缓存命中列** | ❌ 上游无数据 | 实测 `prompt_tokens_details` 恒为 `{}`，`completion_tokens_details` 只有 `reasoning_tokens`。缓存命中率算不出来，只能显示「—」 |
| **P95 延迟 / 并发峰值** | ⚠️ 需新增采样 | 现在只有单次 duration，没有直方图。可先显示均值 + 最大值，P95 需加采样环 |
| **`/v1/responses`（Responses API）** | ❌ 上游 404 | 宿主插件契约也固定只走 `/v1/chat/completions`（`PLUGIN-PROVIDERS.md` §七），插件内部转换也拿不到上游支持 |
| **`/v1/embeddings`** | ❌ 上游 404 | 无 embedding 端点 |
| **`/v1/messages`（Anthropic 协议透出）** | ⚠️ 仅 1 个模型 | 只有 `spark-x2.6` 走 anthropic 成功，其余 503。做成「协议切换」价值极低，建议不做 |
| **团队积分 / 多租户** | ❌ 个人号不可用 | `team-points/*` 返回 `100001 当前未切换到团队`；`user/team-assets` 返回 `900000 团队模型清单暂不可用` |
| **数据目录占用统计 / 清理缓存** | ⚠️ 现在无对象 | 插件模式零落盘，没有 `data/` 可统计。等 B 档落盘后再加 |
| **危险操作：删除全部账号** | ❌ 无对象 | 单 session，删了就没得用 |

---

## 5. 硬约束（动手前必须知道）

### 5.1 对话调试必须由插件服务端代转

浏览器直连宿主 `50080` 会跨源：宿主**无 CORS 头**（实测 `/v1/models` 401 响应里只有
`Content-Type`/`X-Trace-Id`/`Date`），且要求 `Authorization: Bearer <adminPassword>`。

合法路径：面板 `POST /api/chat-test` → 插件内部转发到宿主 `?gateway=` 地址（宿主打开面板时
已把 `gateway`/`key`/`models` 作为查询参数传给面板）。**服务端聚合 SSE 后返回**，浏览器不直连网关。

> 这也是 `skywork` 插件的既有做法（`PLUGIN-CAPABILITIES.md` §11.2 明确写了）。

### 5.2 配置写入：插件自写，不要碰宿主 admin API

面板不能直连宿主 `/api/admin/plugins/loomy/config`（无 CORS）。可行路径：面板
`POST /api/config` → 插件读-合并-写自己的 `provider.json`（`PROVIDER_CONFIG` 环境变量已有）。

**注意**：宿主 3 秒热重载只读 `session`，写 `panel_port` 需重启插件进程才生效（已有文档说明）。
设置页要明确标注「改端口需重启」。

### 5.3 面板是内嵌单文件，不是 SPA 工程

`panelHTML` 是 Go 字符串常量，serve 时原样输出。所以设计稿必须是**单个自包含 HTML**
（内联 CSS/JS、零 CDN）——设计稿正好满足（零外部依赖）。

**但**：LOOMy 面板**不需要** `__PLUGIN_AUTH_TOKEN__` 占位符（那是 skywork 的做法）；
LOOMy 面板是独立端口 + 无鉴权（仅绑 `127.0.0.1`），CORS 全开。移植时不要照抄别的插件。

### 5.4 上游鉴权头的坑

- `/v1/models`：`token` 或 `Authorization` 均可（实测都 200）
- `/v1/chat/completions`：**必须带 `Authorization: Bearer`**，只带 `token` 会返回
  `100002 缺少 token`（实测）
- 所有请求**必须带 `traceparent`**，缺了上游挂死（源码注释明确）

现有 `forwardToUpstream` 已同时注入两个头 + `traceparent`，是对的。新增接口要照抄这个组合。

---

## 6. 建议落地顺序（未开工，等拍板）

1. **第一步（纯视觉，零风险）**：把设计稿的 11 主题 + 侧栏 5 组 + 卡片/表格/令牌 + 动效
   移植进 `panelHTML`，保留现有 4 张卡的全部 JS 逻辑。先看到效果，可随时回滚。
2. **第二步（A 档）**：补 `/api/points`（包 `points/records`）、`/api/tasks`（包
   `pet-work` + `onboarding/tasks` + `invitation-codes`），把「积分与余额」「任务中心」做满。
3. **第三步（A4）**：对话调试（服务端代转 + 真实模型 + 扣分 + 耗时）。
4. **第四步（B1/B2）**：扩 `reqEntry` 字段 + `data/usage.json` 按天聚合，做「调用日志」「用量统计」。
5. **暂不做**：账号池、自动注册、临时邮箱、负载与切号、按号出线（§4 已说明是空壳）。

---

## 7. 待用户确认

1. 代码基准：先把 `_plugin-work/phase5/loomy`（1.8.0）并回 `D:\AI_Projects\loomy2api`，
   还是直接以 phase5 为唯一源码目录？
2. 设计稿 13 视图，砍掉「账号池 / 负载与切号 / 自动注册 / 临时邮箱」四页后剩 9 个
   （仪表盘 / 积分与余额 / 任务中心 / 对话调试 / 调用日志 / 用量统计 / 链路与出口(精简) /
   设置 / 设计系统），是否接受？
3. 是否要加设计稿**没有**但上游支持的能力：**图片生成**（`/v1/images/generations` 实测可用）、
   **技能商店**、**邀请码/兑换码**？
4. 设置页要「可写」（插件自写 provider.json，端口改动需重启）还是「只读」（去宿主改）？
5. 是否保留 11 主题切换（localStorage 记忆）？

**状态：待处理**（调研完成，等以上 5 项决策后再开阶段计划）
