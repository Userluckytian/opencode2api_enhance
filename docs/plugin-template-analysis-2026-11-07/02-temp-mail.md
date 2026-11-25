# 02 — 临时邮箱：全量实现汇总与统一 Go 模块设计

> 需求 2 产出。范围：manifest 中 7 个插件 + 2 个参考工程的全部「临时邮箱」实现。
> 约定：本轮只做分析与设计，不写实现代码。所有结论附源码证据（`path:line`）；无证据处标**推测**；未能验证处如实标注「未验证」。
> 日期：2026-11-07。

---

## 0. 结论速览

1. **9 个代码库里共出现 12 种邮箱源、4 种抽象设计**。功能最全、抽象最接近"可复用模块"的是 **skywork `internal/tempmail` 包**（多源 Registry + 失败回退 + 随机模式 + 凭地址恢复），声明式接入最省代码的是 **flonno/runbios 的 `HTTPSpec`**（新源只写结构体不写逻辑），单源客户端规格最完整的是 **qw2api 的 temp.tf 客户端**。
2. **多源优先级 + 自动换源**已实现的项目：skywork（运行期逐源回退 + 随机换源）、mmxproxy（硬编码 providerChain 逐源重开）、grok-register（cloudflare 建箱失败回退 Mail.tm 风格）。其余项目只有"配置选一个源"。
3. **验证码提取存在 4 个流派**（xAI 三段码 / 关键词前缀 / Clerk 唯一六位数 / 朴素 4-8 位），统一模块必须做成**按目标站点可插拔**，不能只留一条正则——devin 已实测朴素正则会被邮件样式表颜色 `#191919` 干扰（devin `kernel/mail.mjs:8-10`）。
4. **qw2api 判定：收录**。它的 temp.tf 客户端（306 行）是全部项目里对该源端点/坑位记录最完整的规格（`internal/tempmail/tempmail.go:1-306`），且是 skywork 多源包的直系前代（见 §3.7 证据）。但 qw2api 本身是单源，不能作为多源范本。
5. 统一模块建议 = **skywork 的接口骨架 + flonno/runbios 的 HTTPSpec 声明式兜底 + 各家的坑位注释**，详见 §5；逐源收录/排除决策见 §7（2026-11-07 修订补，providers 终版 10 文件）。

---

## 1. Provider 清单表

「API 形态」列只写注册链真正用到的主干端点；完整端点见各项目明细（§3）。

| # | Provider | 来源项目（语言） | API 形态（主干） | 取码方式 | 关键证据 |
|---|----------|------------------|------------------|----------|----------|
| 1 | 自建 CF Worker（cloudflare_temp_email） | grok-register（Python）、grok 插件（Go，仅转发）、skywork（Go）、flonno（Go）、DeepAICode2API（Go，`cfmail`/`cf` 两变体） | 建箱 `POST /api/new_address`（或管理员 `POST /admin/new_address`）→ `{address, jwt}`；列信 `GET /api/mails?address&limit&offset`（Bearer jwt 或管理员轨）；详情 `GET /api/mail/{id}` | 轮询列表→（必要时）取详情→正则提码；grok-register 另有 35s 周期重发回调 | worker 路由清单 `cf_temp_main_worker.js`（grep 路由字面量见 §3.1）；skywork `internal/tempmail/cloudflare.go:356-430`；app.py `:536-578` |
| 2 | temp.tf | qw2api、skywork、flonno、runbios、DeepAICode2API | 建箱 `GET /api/account?providers=&dot=&plus=` → `{email}`（池空仍 200 且 email=null）；列信 `POST /api/check {email,wait}` → `{data:[...]}` | 轮询 `/api/check`，本地正则提码；wait=true 为服务端约 10s 长轮询 | qw2api `internal/tempmail/tempmail.go:79-106,172-201`；flonno `internal/panel/sources.go:139-169`；DeepAI `internal/register/mailbox.go:375-384` |
| 3 | mail.tm（及同构 API） | mmxproxy、flonno、DeepAICode2API、grok-register（duckmail 变体，`https://api.duckmail.sbs`） | `GET /domains`→`{hydra:member:[{domain}]}`；`POST /accounts {address,password}`；`POST /token`；`GET /messages`（Bearer）→ 列表仅摘要；`GET /messages/{id}` 取正文 | 轮询 messages→detail→正则 | mmxproxy `providers.go:40-92`；DeepAI `internal/register/mailbox_mailtm.go:9-16`；app.py `:303,:1395-1433` |
| 4 | maliapi / YYDS Mail（`maliapi.215.im/v1` + Web `vip.215.im`） | grok-register（yyds）、skywork（maliapi）、runbios（yyds，默认排除）、flonno（vip.215.im，未验证） | 域名 `GET /domains`；建箱原 `POST /accounts`（**2026-10-29 起匿名通道已关**，改走 Web App `POST /api/temp-inbox` + 首页会话 cookie）；地址级凭据 `POST /token {address}`；列信 `GET /messages?address&limit`（Bearer 地址级 token）。**2026-11-07 勘误补充：官方 API-key 通道实测可用**——`POST /v1/accounts` + 请求头 `X-API-Key` → 201，返回 `{address, domain, expiresAt(+24h), id, mode:"fixed", source:"api", token:<地址级JWT>}`；`GET /v1/domains` → 200（公共域名池大，含 MX/CNAME 健康态）。provider 实现以 key 通道为首选，Web 会话通道作 fallback；key 属用户凭据，只存运行时配置绝不入 git | 轮询 messages→detail；域名白名单/黑名单/随机三策略 | app.py `:1523-1660`；skywork `internal/tempmail/maliapi.go:195-281`；runbios `internal/autoreg/sources.go:573-601` |
| 5 | 22.do | runbios | `POST /auth {mail,password}`→Bearer（API 通道）；`POST /gmail/generate` 或 `/microsoft/generate`（固定 `plus=false`）；`GET /gmail/inbox`/`/microsoft/inbox`；全账户 20 QPS 限流；另有免凭据网页通道 `mailbox22DoWeb` | 轮询 inbox，字段容错解析 | runbios `internal/autoreg/sources.go:114-348`（QPS 限流 `:725-759`）、`sources_22do_web.go` |
| 6 | EmailMux | runbios | `POST /v1/auth {username,password=APIKey}`→Bearer；`POST /generate-email {domains:["gmail"]}`；`GET /emails?email=`（需 `X-API-Timestamp`/`X-API-Signature`，签名=MD5(secret+email+毫秒时间戳)）；`GET /use-email` | 轮询 /emails；建箱响应后置合规过滤（约 58% 地址带加号→重建箱，上限 8 次） | runbios `internal/autoreg/sources.go:350-571`（签名 `:565-571`） |
| 7 | beeinbox.com | skywork | **无 REST 面**：Laravel+Livewire v2，`POST /livewire/message/{component}` + CSRF + serverMemo.checksum（键序敏感，map 序列化必 500） | 列信内联在 Livewire serverMemo；无按 id 取详情端点 | skywork `internal/tempmail/beeinbox.go:3-52,111-138` |
| 8 | Guerrilla Mail | devin（Node .mjs）、mmxproxy | `GET https://api.guerrillamail.com/ajax.php?f=get_email_address`；`?f=check_email&seq=&sid_token=`；`?f=fetch_email&email_id=`（devin 补正文用） | 轮询 check_email→（列表无正文时）fetch_email→正则 | devin `kernel/mail.mjs:25-56`；mmxproxy `providers.go:100-134` |
| 9 | tmaily | devin | `GET https://tmaily.com/domains`；`GET /generate?force&domain&prefix`；`GET /emails?address=` | 轮询 /emails；`/generate` 今日受 Turnstile 挑战，只作域名列表源 | devin `kernel/mail.mjs:58-111`（挑战注释 `:4-7`） |
| 10 | freecustom.email | mmxproxy | `POST /api/auth`（匿名 Bearer，Set-Cookie authToken ~2h）；`GET /api/public-mailbox?fullMailboxId=`；免费档限流约 1 次/30s | 轮询列表→按 messageId 取详情→正则 | mmxproxy `providers.go:136-233` |
| 11 | mailsac | mmxproxy | `GET api.mailsac.com/api/v1/addresses/{user}/messages`（`Mailsac-API-Key` 头；api 域需走本地代理） | 轮询 messages | mmxproxy `providers.go:235-277` |
| 12 | static / none（无收件箱占位） | DeepAICode2API | 本地拼地址（`prefix+hex@domain`）或用户自带邮箱；`WaitForVerification` 恒返回 `ErrNoInbox` | 无（用于"邮箱已存在→直接登录"等不需收信的分支） | DeepAI `internal/register/mailbox.go:605-646` |

> 未收录说明：devin 的 `tmaily` 建箱通道当前受 Turnstile 保护、实际只当域名列表源用（devin `kernel/mail.mjs:4-7`）；mmxproxy 的 mailsac 依赖本机 7897 代理硬编码（`providers.go:265`），均如实按"有实现但受条件限制"记录。

---

## 2. 每源提取维度速查（按 ask 要求的五个维度）

### 2.1 API 形态
见 §1 表。两个结构性事实需要统一模块吸收：

- **同一 Provider 名下存在"建箱分配"与"给定地址建箱"两种形状**。flonno 用 `Source.Assign`（服务端分配：temp.tf/CF/YYDS）与 `Source.Create`（自带地址：mail.tm）两个字段显式区分，`ServerAssigns()` 判定（flonno `internal/panel/sources.go:83-102`）。
- **同一 CF Worker 部署存在"公共轨/管理员轨"两套读信权限**（`/api/mails` 用箱级 jwt；`/admin/mails` 用管理员密钥）。管理员轨可能 403/5xx/返回空列表——空列表与"信未到"无法区分，skywork 的处理是：配置轨优先 + 互补轨兜底 + 空列表在非末轨时按"轨未开通"换轨（skywork `internal/tempmail/cloudflare.go:340-430`）。

### 2.2 取码逻辑（轮询模型）
| 项目 | 轮询入口 | 间隔/超时 | 失败语义 |
|------|----------|-----------|----------|
| skywork | `WaitSourceMessage`（provider.go:707-762） | 默认 timeout 120s、interval 3s | 列表项无正文→按 id 取详情；详情失败包 `ErrDetailFetch` 上抛（不再静默吞，fact#3383）；"有信无正文"不再提前 return，继续等（fact#3510） |
| qw2api | `Client.WaitForMessage`（tempmail.go:183-201），匹配谓词由调用方给（drive.go:698-699 `ExtractCode(m)!=""`） | 3s / 调用方定 | 请求错误静默跳过本轮 |
| grok-register | `cloudflare_get_oai_code`（app.py:1921+） | poll 3s / 180s；每 35s 触发 `resend_callback` 重发（:1936,:1947） | 每封邮件最多解析 5 次（`seen_attempts`）；列表/详情接口失败都继续轮 |
| devin | `waitForCode`（mail.mjs:155-179） | 2.5s / 180s | poll 失败经 `onProgress` 上报后继续 |
| flonno | `Worker.pollMail`（worker.go:318-343） | `cfg.PollInterval` / `cfg.VerifyTimeout`；`since=now-1min` 过滤历史邮件 | 非 `ErrMailboxEmpty` 错误直接终止 |
| runbios | 同 flonno（mailbox.go 文件头注明逐字对齐 flonno worker.go:318-343） | 同上 | 同上 |
| DeepAI | `waitForVerification`（mailbox.go:315-363） | **自适应**：300ms 起步 ×1.5 退避到配置间隔（实测中位收信 14.8s，省 0.35s/号） | 超时错误区分"带最后错误"与"纯超时" |
| mmxproxy | 各 Provider 的 `WaitCode`（providers.go:62-277） | guerrilla/mailtm 3s、mailsac 4s、freecustom 35s（免费档限流） | 请求失败 continue 直到超时 |

### 2.3 验证码提取规则
4 个流派，详见 §4。共同点：**先主题后正文**（grok-register `extract_verification_code` 先匹配主题 `^([A-Z0-9]{3}-[A-Z0-9]{3})\s+xAI`，app.py:1848-1854；flonno/runbios 注释明确"调用方应先传标题，不行再传正文"，flonno `internal/autoreg/mailbox.go:63-65`）。

### 2.4 失败处理
- **哨兵错误**：`ErrEmptyPool`（池空，skywork provider.go:82；temp.tf 池空时 HTTP 仍 200、email=null，qw2api tempmail.go:81,102-104）、`ErrMailboxEmpty`（窗口内无新邮件，flonno autoreg/mailbox.go:92）、`ErrMailboxNotRecoverable`（能力缺失≠网络失败，skywork recover.go:42）。
- **退避与限流**：qw2api 对 temp.tf 429 等 15s 再试、池空 5s 换参数重试 90s（engine.go:798-823）；DeepAI mail.tm 429 按 `Retry-After` 等（mailbox_mailtm.go:42 注释）；runbios 22.do 客户端 20 QPS 令牌桶（sources.go:725-759）。
- **重试上限+合规过滤**：DeepAI temp.tf 带加号地址重建箱上限 5 次（mailbox.go:392,476-499）；runbios emailmux 8 次、temptf 5 次（sources.go:362、sources_temptf.go:29-31）。
- **凭据安全**：skywork 把 query-key 鉴权收敛为 header + `redactURL` 日志兜底 + `maskSecret`（provider.go:766-806；cloudflare.go:18-22）。

### 2.5 配置项（按项目）
| 项目 | 配置位置 | 关键键 |
|------|----------|--------|
| grok 插件 | `PrivateConfig`（config.go:51-53）→ 写成注册 exe 的 `config.json`（jobs.go:192） | `temp_mail_base` / `temp_mail_key`（脱敏：config.go:247）/ `email_domains`；转发时映射为 `email_provider=cloudflare` + `cloudflare_*` 七键（jobs.go:477-487，管理员建箱轨 `/admin/new_address` + 公共列信轨 `/api/mails`） |
| grok-register | `DEFAULT_CONFIG`（app.py:108-140） | `email_provider`（duckmail/yyds/cloudflare，UI 三选一 app.py:3882）；CF 组：`cloudflare_api_base/key/auth_mode/path_*`（:109-117）；YYDS 组：`yyds_api_key/yyds_jwt/yyds_preferred_domains/yyds_blocked_domains/yyds_domain_selection`（:135-137,1526-1531）；`duckmail_api_key`（:108）；`defaultDomains` 域名轮换（:501-514） |
| skywork | `data/config.json` 的 `tempmail` 段（config.go:72-87） | `sources[]`（`source/base/api_key/jwt/auth_mode/domains/web_base/paths`，:50-70）+ `preferred` + `random_mode(off/source/domain)`（:72-87）；密钥环境变量兜底：`QW2API_CF_API_BASE/KEY`、`QW2API_MALIAPI_*`、`QW2API_BEEINBOX_BASE`、`QW2API_TEMPMAIL_SOURCE`（provider.go:54-68） |
| flonno | `DefaultSources()` 硬编码 4 源（sources.go:119-126） | 每源为声明式 `HTTPSpec`；`Verified=false` 全部标注待验证（:9-11） |
| runbios | `settings.json` `autoreg.mail_source`（settings.go:15，默认 `"22do"` :126） | `mail_user/mail_pass/mail_api_key`；`mail_source` ∈ 22do/emailmux/yyds/temptf（cmd/runbiosproxy/main.go:123-137） |
| mmxproxy | 代码内 providerChain + `mailsac` 构造参数（main.go:61） | 面板可选源清单 `["guerrilla","freecustom","mailtm","mailsac"]`（regtask.go:302-303） |
| qw2api | 引擎级（engine.go:39-40,130） | `Providers`（temp.tf 的 outlook/hotmail/gmail/high.edu.pl 子集，server/autoreg.go:35-62 白名单解析）、`UseDot/UsePlus`；服务端默认固定 `outlook` |
| devin | 请求参数（无落盘配置） | `createMailbox(sourceId, domain)`（mail.mjs:126-131）；面板 `GET /api/mail/sources`、`POST .../create`（kernel.mjs:517-530） |
| DeepAI | 环境变量 + Config（config.go） | `DEEPAI_REGISTER_CFMAIL_BASE/KEY`、`TEMPMAIL_BASE/ADMIN_KEY`、`DEEPAI_REGISTER_DOMAIN`；backend 名单 `cfmail/cf/mailtm/temptf/static/none`，缺省时 `cfg.AutoBackend()` 自动挑（mailbox.go:71-134） |

---

## 3. 各项目实现明细与「多源/换源」判定

### 3.1 grok 插件 Go 壳（`D:\AI_Projects\_plugin-work\phase4\grok`）— **纯转发，无自研邮箱代码**
- 壳内只有配置键定义、脱敏、面板展示与转发：`config.go:51-53`（三键）、`config.go:247`（`temp_mail_key` 归入脱敏名单）、`panel.go:387-389,426`（面板读写白名单）。
- 转发机制：`JobManager.workerConfig()` 组装 `email_provider=cloudflare`、`cloudflare_api_base/key`、`cloudflare_auth_mode=x-admin-auth`、`cloudflare_path_accounts=/admin/new_address`、`cloudflare_path_domains=/admin/mails`、`cloudflare_path_messages=/api/mails`、`defaultDomains`（jobs.go:475-501），`json.MarshalIndent` 后写进注册工作目录的 `config.json`（jobs.go:192），再以 `exec.Command(exe, "--job", cmdFile)` 启动注册机（jobs.go:213）。注册机即 grok-register（见 §3.2）。
- 判定：**无多源、无取码逻辑**（都在 Python 侧）。统一模块对 grok 的价值 = 把"壳转发配置"升级为进程内直接调用 Provider。

### 3.2 grok-register（`D:\AI_Projects\grok-register-cloudflare-temp-email`，Python 5210 行 app.py）
- **三 provider 单选**：`email_provider` ∈ duckmail / yyds / cloudflare（app.py:1760-1763；UI 下拉 :3882）。
- **CF provider（主推）**：
  - 建箱专用模式 `cloudflare_create_temp_address`（:536-578）：POST `{path}`（admin 路径判定的负载是 `{name: 随机10位, enablePrefix:true, domain}`；公共负载 `{domain}`），**响应必须同时含 address 与 jwt**，否则报错。
  - 域名轮换 `cloudflare_next_default_domain`（:501-514）：`defaultDomains` 逗号表 + 全局索引取模。
  - 列信 `cloudflare_get_messages`（:1484-1497）：`params={limit:20,offset:0}` + `_pick_list_payload`（:517-533）兼容 `list/results/hydra:member/data/messages/data.messages` 六种包装。
  - 详情 `cloudflare_get_message_detail`（:1498-1521）：候选 `/api/mail/{id}` 优先、列信路径+`/{id}` 兜底——与 skywork `detailPaths()`（cloudflare.go:506-512）同源。
  - token 双形态：本树 `cloudflare_get_token` 为 **POST `{address,password}`**（:1461-1482，Mail.tm 风格，用于回退模式）；skywork 实现的是 **GET `/api/token?address=`**（cloudflare.go:305-328）。注意：本树 `cf_temp_main_worker.js` 中 grep `api/token` **0 命中**（本次执行 `grep -c "api/token"` → `0`），说明两种 token 流对应**不同版本的 cloudflare_temp_email worker**（推测：skywork 引用的 app.py 快照行号 `:1383-1395` 与本树 app.py 不对应，同一函数在本树位于 ：1461）。统一模块两种都要留。
  - **失败回退（唯一的跨风格回退样本）**：`get_email_and_token` 中 cloudflare 专用建箱失败时，catch 后回退"Mail.tm 风格"：取域名→拼地址→`cloudflare_create_account{address,password}`→`cloudflare_get_token`（app.py:1764-1795）。
- **YYDS provider**：`yyds_pick_domain`（:1630-1657）三策略——`yyds_preferred_domains` 优先、`yyds_blocked_domains`+运行时黑名单 `_yyds_runtime_blocked_domains`（:191）剔除、`yyds_domain_selection=random` 随机；只取 `isVerified` 域名；建箱 payload 恒带 `autoDomainStrategy:"prefer_owned"`（:1561-1563）。`yyds_get_token` POST `/token {address}` 换地址级 token（:1571-1586）。
- **DuckMail provider**（Mail.tm 同构，base `https://api.duckmail.sbs`，:303）：domains/accounts/token/messages 五件套（:1395-1433），列表包装键 `hydra:member`。
- **取码**：`extract_verification_code`（:1848-1869）——① 主题 `^([A-Z0-9]{3}-[A-Z0-9]{3})\s+xAI`（xAI 三段码，Grok 专用）；② 正文 `\b([A-Z0-9]{3}-[A-Z0-9]{3})\b`；③ 关键词前缀 `verification code|your code|confirm(ation)? code` + `\d{4,8}`。
- **收码循环**：`cloudflare_get_oai_code`（:1921+）——每 35s 触发 `resend_callback` 重发验证码（:1936,:1947）；每封邮件 `seen_attempts` 上限 5 次（正文可能延迟可读）；列表项正文字段候选 `text/raw/content/intro/body/snippet` + html 去标签，再 detail 补全（:1966-2000）；收件人匹配宽松化——`to` 数组或顶层 `address` 字段任一命中即可，防字段漂移漏码（:1963-1971）。

### 3.3 devin 插件（`D:\AI_Projects\Devin-all\devin-plugin`，Node .mjs 内核）
- `kernel/mail.mjs`（186 行）两源：**guerrilla**（免挑战，可建箱可收信）+ **tmaily**（`/generate` 受 Turnstile，只当域名列表源，诚实上报不能建箱，mail.mjs:4-7）。
- guerrilla 细节：首调不带 `sid_token`（空 sid 会被 API 404，mail.mjs:28-29）；每次响应刷新 sid（:53-54）；列表无正文时逐封 `fetch_email` 补（:46-51）。
- **验证码提取（CSS-aware，本项目独有）**：`extractCode`（mail.mjs:141-152）——① `color:#317cff…>(\d{6})<`；② `letter-spacing…>(\d{6})<`；③ `>(\d{6})<`；④ 剥掉 `#hex` 与 `rgb()` 后 `\b(\d{6})\b`；⑤ 最后主题。动机：Devin 邮件样式表颜色 `#191919` 会被朴素正则误判（mail.mjs:8-10,138-140）。
- 收件人过滤：正文/主题/发件人拼串匹配 `/devin|cognition/i` 才提码（mail.mjs:171-174）。
- **换源判定：无自动换源**。`createMailbox(sourceId)` 由调用方显式传源（register.mjs:235 默认 `"guerrilla"`；kernel.mjs:188）；面板可列源（kernel.mjs:517）但切源是人工动作。

### 3.4 flonno2api（`D:\AI_Projects\_plugin-work\phase5\flonno`）— 声明式 HTTPSpec
- `internal/panel/sources.go`（717 行）：`HTTPSpec`（:37-73）描述单请求形状（Method/Path/Headers/Query/BodyTemplate/AddressKey/TokenKey/TokenHeader/TokenPrefix/ArrayKey/DomainKey/IdKey/Detail/四组候选字段名）；`Source`（:76-97）= Assign | Create+Domains + List。
- 4 源：temp.tf（**`providers=gmail&dot=true&plus=true` 取真 @gmail.com 变体**——high.edu.pl 被 Flonno 注册页黑名单拒 "Temporary email services are not supported"，gmail.com 不在（:148-152））；mail.tm（Domains+Create+List+Detail 全形状，:172-213）；cf-worker `temp-mail.tianzhuzhucn.eu.org`（:216-243）；vip.215.im（:246-279，`Verified=false`）。
- 诚实标注：文件头声明"真实入信链路尚未闭环（intent 4705），所有源 Verified 一律 false，不作为已验证可用源对外承诺"（:9-11）。**未验证**。
- 运行时：`HTTPMailbox` 实现 flonno `autoreg.Mailbox{Create,Poll}`（types.go:47-50），幂等建箱（`assign` 在 addr+token 已有时直接返回，:400-403）；`Poll` 自后向前找首个 `since` 之后有正文的邮件，正文空且有 IdKey 时取 Detail（:459-508）。
- 换源判定：**无**——面板候选 4 源，注册链装配单个 `HTTPMailbox`。
- 对接点：`Worker.pollMail`（worker.go:318-343）→ `fillCode`（drive.go:409）；Clerk 流程同时用 `ExtractMagicLinks`（magic link 与验证码双轨，autoreg/mailbox.go:20-49）。

### 3.5 runbiosproxy（`D:\AI_Projects\BIOS-all\runbios_proxy`）
- `internal/autoreg/sources.go`（970 行）：HTTPSpec/Source 形状注明对齐 flonno（文件头 ：5-6）；四适配器：`mailbox22Do`（:187-348）、`mailboxEmailMux`（:368-571）、`mailboxYYDS`（:580-721，**DefaultExcluded**，域池 354 域不含真实 gmail/outlook，:158-178）、`mailboxTempTF`（sources_temptf.go，plus=false + 合规过滤重建箱上限 5）。
- 22.do 零凭据降级：mail/pass 全空时自动改返回免凭据网页通道 `mailbox22DoWeb`（:200-212；`sources_22do_web.go`），避免"配置缺→必失败"。
- EmailMux 签名：`signEmailMux = MD5(secret+email+毫秒时间戳)` 32 位小写 hex，secret `yjd683c@47` 硬编码自前端反解（:364-366,565-571）；实测不带签名恒 401、POST /use-email 恒 405（须 GET）（:354-361）。
- 合规过滤 `validateEmailPolicy`（policy.go:31-40）：local 含 `+` 拒（`ErrEmailPlusRejected`）、domain 必须精确命中白名单（`ErrEmailDomainRejected`）。
- 换源判定：**无自动换源**。装配单源 `buildMailbox(settings.Autoreg.MailSource,…)`（cmd/runbiosproxy/main.go:122-137,363）；`ActiveSources()` 只是过滤面板候选（sources.go:93-102），文件头"#7180 主源/备用"是选型结论不是运行期链路。
- 对接点：`autoreg.NewWorker(Deps{Turnstile, Mailbox, Sink, Client})`（main.go:366-372）。

### 3.6 mmxproxy（`D:\AI_Projects\_plugin-work\phase3\mmxproxy`）
- `providers.go`：`MailProvider{Name,Create,WaitCode}` 接口（:20-26）+ 4 实现（mailtm/guerrilla/freecustom/mailsac），`MailBox.Ref` map 携带 provider 私有态（sid/token/password，:14-18）。
- **providerChain 自动换源（硬编码顺序）**：`providerChain() = [Guerr, FCE, MailTM, Mailsac]`（store.go:260-266）；`registerOne` 逐源完整注册，单源失败记"邮箱源 X 失败，换下一个源重试"后重开任务（store.go:271-293）。
- 提取规则最弱：`extractCode = (\d{4,8})` 首个匹配（core.go:238-243），依赖外层"主题含 Verification Code"过滤兜底（providers.go:84,126,217,269）。
- 坑位证据：mail.tm POST 必须显式 `Content-Type: application/json`，否则 415 拒绝导致 mailtm 源整体不可用（providers.go:289-292 注释）。

### 3.7 qw2api（`D:\AI_Projects\_plugin-work\phase3\qw2api`）— **价值判定：收录**
- 全部邮箱代码 = `internal/tempmail/tempmail.go`（306 行）+ 引擎调用点。单源 temp.tf，**无 Registry/多源**（`grep -rn "Registry|InstallRegistry|NewSource|SourceConfig" cmd` 0 命中，本次已执行）。
- 收录理由（三条实证）：
  1. **temp.tf 端点规格最完整**：四个端点 + `wait` 长轮询语义 + "provider 名只接受 outlook/hotmail/gmail/high.edu.pl，写 `outlook.com` 被服务端静默过滤返回 email:null"（tempmail.go:1-12）——该坑位被 flonno（sources.go:143）、DeepAI（mailbox.go:377-390）、runbios（sources_temptf.go:7-9）各自复述，源头表述在此。
  2. **codePatterns 四条正则**（tempmail.go:205-221）是 skywork 提取层的直系前代（skywork provider.go:143 注明"复用 tempmail.go:255 的 4 条 codePatterns"，本树行号 ：205）。
  3. **池空/限流语义**：池空 HTTP 200 + email:null→`ErrEmptyPool`（:81,102-104）；引擎对 429 等 15s、池空 5s 换参重试、90s 总限（engine.go:798-823）。
- 不作多源范本的理由：engine 硬绑 `mail: tempmail.New()`（engine.go:218）、默认固定 outlook（:130,:207）。
- 换源判定：**无**。取码：`WaitForMessage(90s, ExtractCode(m)!="")` → `ExtractCode(msg)`（drive.go:698-704）。
- **谱系注记（推测）**：skywork `internal/tempmail/provider.go:54-68` 的环境变量名全部是 `QW2API_*` 前缀，注释引用 `cmd/qw2api/main.go`（:933）与 `cmd/qw2api/tempmail_setup.go`（autoreg/mailbox.go:12——该文件在 qw2api 树中不存在）；且两树 tempmail.go 函数级同构。推测 skywork 的 tempmail/autoreg 包源自 qw2api 谱系（或共同祖先），方向无法仅凭源码证实。

### 3.8 skywork（`D:\AI_Projects\_plugin-work\phase5\skywork`）— 功能最全的多源实现
- 包结构：`internal/tempmail/` 22 文件 7641 行（`ls`+`wc -l` 实测）；核心五文件 provider.go(997)/beeinbox.go(965)/cloudflare.go(551)/maliapi.go(498)/tempmail.go(306)。
- 抽象：`Source{Kind,NewMailbox,ListMessages,GetMessage}`（provider.go:152-157）+ 可选能力槽 `StatsSource`（:160-162）、`MailboxRecoverer{RecoverMailbox(kind,address)}`（recover.go:49-51）；跨源归一 `MessageInfo{ID,Subject,From,To,CreatedAt,Text,HasBody,Raw}`（:132-141）+ 键优先级表 `idKeys/subjectKeys/…`（:582-591）+ 信封解包 `unwrapEnvelope` 三形状（:486-513）+ 列表挑取 `pickListPayload`（:595-617）。
- **多源优先级 + 失败换源**：`Registry{order,srcs}`（:243-247）→ `PickMailboxMode(ctx, prefer, randomMode)`：prefer 提到队首、其余按注册顺序，逐源 `NewMailbox`，失败/池空记 `lastErr` 继续下一源（:357-394）；`RandomSource` 用 crypto/rand 打乱（:400-416，防风控序列可预测）；`RandomDomain` 固定源换域名（random.go:29-36）。
- **构建期校验**（provider.go:201-236）：空 provider 名硬失败（不再静默回落 temp.tf，:182-185 注释记了教训）；CF 缺 base/未知 auth_mode/未知路径键硬失败（cloudflare.go:65-107）；maliapi 缺 key+jwt 硬失败（maliapi.go:62-69）。
- **源内换轨**：CF 列信双轨 `/api/mails`↔`/admin/mails`，401/403/404/5xx/网络错/空列表（非末轨）都换轨（cloudflare.go:356-451）；详情双候选 `/api/mail/{id}`→列信路径+`/{id}`（:464-512）。
- **凭地址恢复**（relogin 对接）：CF/MaliAPI 可恢复（凭地址重取 token，recover.go:108-159）；temp.tf 判"不接"（装配层无法判定地址归属，:163-180）；beeinbox 结构性不可恢复（会话态只活在内存，:182-196）；`RandomSourceSupported` 据此提示"随机到不可恢复源=放弃自动重登"（random.go:66-89）。
- 引擎对接：`candidates()` prefer 三级来源（任务级 cfg.PreferSource > 装配期 e.prefer > `tempmail.PreferredSource()`）+ RandomSource 打乱（autoreg/mailbox.go:236-278）→ `PickMailboxFrom`（:155-167）→ `WaitSourceMessage`（:309-320）→ `msg.Code()`（engine.go:427；实现 provider.go:144-149→`ExtractCodeFromText`→tempmail.go:205 四正则）。
- 面板：`GET/POST /api/tempmail/config`（密钥打码 + 热换全局注册表，tempmailpanel.go:5-6）。

### 3.9 DeepAICode2API（`D:\AI_Projects\refs\DeepAICode2API`）— 接口切分最细
- 三层接口（mailbox.go:43-69）：`Provider{Name,Create}`（工厂）、`Mailbox{Email,Provider,Fresh,WaitForVerification}`（`Fresh` 免掉"邮箱已存在"预检，:46-50）、可选 `InboxProvider{Inbox(email)}`（补跑既有地址，:61-64）、可选 `HealthProvider{Health}`（doctor 用，:66-69）。
- 后端 6 种：cfmail（自建 CF，`POST /api/mailboxes`→`{emails:[...]}`、列信 `GET /api/inbox?email=`，:84-96,223-256）、cf（另一 CF 变体）、mailtm（独占收件箱、429 退避，mailbox_mailtm.go:9-16,42）、temptf（免凭据共享池，按 since 过滤历史邮件，:375-384）、static、none；`NewProvider` 缺省时 `AutoBackend()` 按"配了 CF→cf，有域名→static，否则 none"自动选（:71-83）。
- 验证形态是**链接**不是验证码：`verifyLinkRe = https://api.deepai.org/verify_email?key=…`，正文需含 verif 语境（:29-30,366-373）。
- HTTP 韧性：5xx 指数退避重试 4 次、4xx 立即失败（:151-209）；自适应轮询 300ms→×1.5（:315-363）。
- 换源判定：**无运行期换源**（backend 名单 + 配置驱动自动选择，单后端运行）。

---

## 4. 验证码提取规则汇总（统一模块的输入）

| 流派 | 代表 | 规则 | 已知误报/漏报 |
|------|------|------|----------------|
| A. 目标站点专用码 | grok-register（xAI） | 主题 `^([A-Z0-9]{3}-[A-Z0-9]{3})\s+xAI` → 正文三段码 → 关键词+4-8 位（app.py:1848-1869） | 只适用 xAI/Grok |
| B. 关键词前缀 | qw2api/skywork | `(code\|otp\|pin\|verif\w*\|passcode\|kode)[^0-9A-Za-z]{0,24}(\d{4,8})` → `\b\d{6}\b` → `\b\d{4,8}\b` → `\b[A-Z]-\d{6}\b`（tempmail.go:205-210） | 需先去 HTML（`stripHTML`，tempmail.go:288-299） |
| C. 语境唯一六位数 | flonno/runbios（Clerk） | `code\s*[:：]?\s*(\d{6})` → 限定"含 verification/code/验证 语境"后，全文六位数**唯一**才采用（mailbox.go:16-17,66-89） | 专治 Clerk 标题"192401 is your verification code"（数字在前） |
| D. CSS-aware | devin | 样式内 `color:#317cff` / `letter-spacing` → `>(\d{6})<` → 剥 `#hex`/`rgb()` 后六位数 → 主题（mail.mjs:141-152） | 反例实证：朴素 `\b\d{6}\b` 会抓到样式表 `#191919`（mail.mjs:8-10） |
| E.（反例）朴素 | mmxproxy | `\d{4,8}` 首个匹配（core.go:238-243） | 靠"主题含 Verification Code"外层过滤兜底 |
| F. 链接型 | DeepAI | 验证链接 `verify_email?key=[0-9a-f-]+`（mailbox.go:30） | 非码，但与码同属"邮件凭证"，接口需兼容 |

设计含义：提取器必须**按目标站点（收码方）选择流派 + 通用兜底链**，且输入是"去 HTML 后的正文 + 主题"二元组，输出候选而非单值（flonno 的"唯一才采用"证明单值正则不够）。

---

## 5. 统一 Go 模块设计（目标：13 插件供应商复用、轻依赖、Go 方向）

### 5.1 选型基调
- **零外部依赖**（标准库 only）：9 个库全部如此（skywork/qw2api/flonno/mmxproxy/runbios/DeepAI 的 go.mod 均无三方邮箱依赖；本次阅读未见任何 import 三方 mail 库——如需可复核，此处按"已读代码未见"表述）。
- **骨架抄 skywork**：`Source` 接口 + `Registry` + `WaitSourceMessage` + 能力槽（Stats/Recover）是唯一被生产验证过的多源运行期方案（§3.8）。
- **兜底抄 flonno/runbios**：`HTTPSpec` 声明式让新源（如 EmailMux、freecustom）零逻辑接入；已知局限是表达不了 Livewire 键序 checksum（beeinbox）这类协议，这类源必须写代码适配器（beeinbox.go:111-138 的 beeOrdered 证明）。
- **规格注释搬 qw2api/grok-register**：各家实测坑位（provider 名静默过滤、415 content-type、checksum 键序、双轨权限）是模块的隐性价值。

### 5.2 包结构（建议）

```text
tempmailx/                 # 统一临时邮箱模块（假设名）
├── types.go               # Mailbox / MessageInfo / 哨兵错误（ErrEmptyPool/ErrMailboxEmpty/ErrNotRecoverable）
├── source.go              # Source 接口 + NewSource(kind, SourceConfig) 构建期校验（抄 skywork provider.go:209-236）
├── registry.go            # Registry + PickMailboxMode(prefer, randomMode)（抄 skywork :243-416）
├── wait.go                # WaitMessage / WaitCode 轮询循环（抄 skywork :707-762：详情补取、空正文不提前返回）
├── extract.go             # 验证码提取：按目标站点分流的规则集 + 通用兜底链（§4 流派 B/C/D 融合）
├── domain.go              # 域名变体策略：轮换/偏好/黑名单/plus/dot 合规过滤（§6）
├── httpspec.go            # 声明式 HTTPSpec Source（抄 flonno sources.go:37-73 + runbios 零凭据降级思路）
├── redact.go              # redactURL / maskSecret（抄 skywork :766-806）
└── providers/           # 2026-11-07 修订：下列为原 8 文件；§7 补收 freecustom.go / mailsac.go（终版 10 文件），tmaily 与 static/none 显式排除，逐源决策见 §7
    ├── cloudflare.go      # 公共轨/管理员轨双轨换轨 + token 双形态（GET ?address= / POST {address,password}）
    ├── temptf.go          # qw2api tempmail.go 移植 + ErrEmptyPool
    ├── maliapi.go         # 含 Web App 建箱通道（首页会话→POST /api/temp-inbox）
    ├── mailtm.go          # hydra:member + Content-Type 415 坑 + 429 Retry-After
    ├── g22do.go           # 双模块 gmail/microsoft + 20QPS + 零凭据降级网页通道
    ├── emailmux.go        # MD5 签名 + plus 合规重建箱
    ├── guerrilla.go       # devin 版本（sid 会话）
    └── beeinbox.go        # Livewire 键序回填（可选，维护成本最高）
```

### 5.3 核心接口

```go
// Source：一个邮箱来源的最小能力面（skywork provider.go:152-157 原样采纳）
type Source interface {
    Kind() string
    NewMailbox(ctx context.Context) (*Mailbox, error)          // 分配或建箱；池空返回 ErrEmptyPool
    ListMessages(ctx context.Context, mb *Mailbox) ([]MessageInfo, error)
    GetMessage(ctx context.Context, mb *Mailbox, id string) (*MessageInfo, error) // 无此端点的源可"列箱+按id匹配"退化（skywork :870-884 先例）
}

// 可选能力槽（Go 惯例：接口断言探测，不强加）
type StatsSource interface { SourceStats(ctx) (map[string]any, error) }      // skywork :160-162
type Recoverer interface { RecoverMailbox(ctx, kind, address string) (*Mailbox, error) } // skywork recover.go:49-51
type HealthSource interface { Health(ctx) error }                            // DeepAI mailbox.go:66-69

// 声明式兜底：与 Source 平行的 HTTPSpec 注册（flonno sources.go:76-97）
// Assign|Create+Domains + List + Detail，渲染为同一个 Source 接口。
```

`Mailbox{Address, Token, Source}`、`MessageInfo{ID,Subject,From,To,CreatedAt,Text,HasBody,Raw}` + 归一化键优先级表（skywork :582-591）——**Raw 必须保留**：字段漂移时（grok-register 收件人匹配宽松化，app.py:1963-1971）调用方还能自救。

### 5.4 多源优先级 + 失败换源（采纳 skywork 语义，补两点）

1. **候选序** = `preferred` 提队首 + 其余按注册序；`PickMailboxMode` 逐源建箱，`err`/`ErrEmptyPool` 都继续（provider.go:357-394）。
2. **随机模式** `off|source|domain`（config.go:72-87 + random.go）；`RandomDomain` 为面板推荐值（不可恢复源参与 source 随机会牺牲 relogin，random.go:7-22 的论证直接搬）。
3. **源内换轨**（CF 双轨）作为 Source 内部实现细节，不进 Registry 层（cloudflare.go:356-430）。
4. **补充一（源自 mmxproxy）**：换源不只在"建箱失败"发生——**取码超时也应触发整号换源重开**（mmxproxy store.go:271-293 是"完整注册"粒度的换源；skywork 只在建箱粒度）。统一建议：引擎层定义"邮箱阶段失败可重开"边界，任务进度按源记录（mmxproxy regtask.go `task.advance(…,"邮箱源 X 失败")` 的 UX 值得保留）。
5. **补充二（源自 grok-register）**：源内风格回退（CF 专用建箱失败→Mail.tm 风格建箱，app.py:1772-1790）实现为 Source 的内部 fallback，不表现为第二个 Registry 条目。

### 5.5 验证码提取设计

```go
type Target struct{ Name string }              // "grok" / "devin" / "clerk" / …
type Extractor func(subject, body string) []string  // 返回候选（有序）

// 内置流派（§4）：xAIThreeSeg、KeywordPrefixed(codePatterns 四条)、ClerkUniqueSix、CSSAwareSix
// 通用兜底：stripHTML → 剥 #hex/rgb() → 关键词前缀 → 唯一六位数 → 任意 4-8 位
// 调用方按注册目标选择流派；未知目标走兜底链。
func ExtractCode(t Target, subject, body string) []string
```

规则要点（全部有实证）：
- 输入必须**先去 HTML**（skywork `stripHTML` tempmail.go:288-299；grok-register 列表项 html 先 `re.sub(r"<[^>]+>"," ")`）且**剥颜色字面量**（devin mail.mjs:147）。
- **先主题后正文**（grok-register :1848-1854；flonno mailbox.go:63-65）。
- 候选制而非首中制（flonno "唯一才采用" ：77-88；devin 多级正则 :143-145）。
- 收件人/发件人语境过滤交给调用方（devin `/devin|cognition/i` mail.mjs:171-174；grok-register 宽松收件人匹配 app.py:1963-1971）。
- "有信无正文"不判失败，继续轮询（skywork fact#3510 修正，provider.go:744-754）。

### 5.6 域名变体规则

| 规则 | 证据 | 统一模块落点 |
|------|------|--------------|
| dot/plus 布尔变体（temp.tf） | qw2api tempmail.go:91-93（`dot`/`plus` query 参数）；flonno 用 `gmail+dot+plus` 过注册页黑名单（sources.go:148-152）；DeepAI/runbios 反向固定 `plus=false`（mailbox.go:390、sources_temptf.go:24） | `SourceConfig{Providers, Dot, Plus}` 原样保留（skywork :111-113 已如此） |
| 配置域名轮换（CF） | app.py:501-514 取模轮换；skywork `nextDomain` atomic 计数（cloudflare.go:197-203） | CF provider 内置 |
| 偏好/黑名单/随机三选一（YYDS） | app.py:1630-1657（preferred → blocked+运行时累积 → random；只取 isVerified） | `DomainPolicy{Preferred, Blocked, Selection}`，运行时拉黑累积进内存态 |
| 加号合规过滤 | runbios `validateEmailPolicy`（policy.go:31-40）+ emailmux 58% 加号率实测重建箱 8 次（sources.go:361-362,394-440）；DeepAI temptf 5 次（mailbox.go:391-392） | `domain.go` 提供 `RejectPlus(addr) error` + `RecreateUntil(ctx, create, check, maxAttempts)` |
| 服务端分配 vs 自带地址 | flonno `ServerAssigns()`（sources.go:100-102） | HTTPSpec 的 Assign/Create 双形状 |

### 5.7 与注册流程对接点（对照表）

| 项目 | 对接形态 | 统一模块应支持的契约 |
|------|----------|----------------------|
| grok 壳→exe | JSON 配置文件 + 子进程（jobs.go:192,213） | 模块化后壳可直接进程内调用；仍保留"导出 config.json"能力兼容旧 exe |
| devin | `emailStart(email) → waitForCode(mailbox) → emailComplete(token,code)`（register.mjs:121-136,253-259） | `WaitCode(ctx, mb, Matcher{SenderRe, Extractor}, timeout)` |
| qw2api | `grabEmail(429/池空退避) → drive → WaitForMessage(ExtractCode!= "")`（engine.go:798-823, drive.go:698-704） | 同上 + `NewAddress` 的 429/池空退避内建在 provider |
| skywork | `PickMailboxFrom → WaitSourceMessage → msg.Code()`（autoreg/mailbox.go:155-167,309-320, engine.go:427） | Registry 直连；`WaitMessage` 返回 `*MessageInfo` 由调用方提取 |
| flonno/runbios | `Mailbox.Create/Poll(since)` + `Worker.pollMail`（types.go:47-50, worker.go:318-343） | 兼容适配器：把 `Source` 包成 `Create/Poll` 二方法视图（`since` 过滤在 wait.go 统一实现） |
| mmxproxy | `registerOne` 逐源重开（store.go:271-293） | 引擎层"邮箱阶段失败→换源重开"编排（§5.4-4） |
| DeepAI | `Provider.Create → Mailbox.WaitForVerification(since,timeout,interval) → link`（mailbox.go:306-373） | `WaitMessage` 之上再封 `WaitCode/WaitLink` 两出口；自适应退避（300ms ×1.5）进 wait.go 默认值 |

### 5.8 配置 schema（直接采纳 skywork `TempMailConfig`，config.go:50-87）

```jsonc
{
  "tempmail": {
    "preferred": "cloudflare",
    "random_mode": "off",                     // off | source | domain
    "sources": [
      { "source": "cloudflare", "base": "https://…", "api_key": "…",
        "auth_mode": "x-admin-auth",           // none|x-api-key|x-admin-auth|bearer|query-key
        "domains": ["a.com","b.com"], "paths": {"accounts":"/admin/new_address"} },
      { "source": "maliapi", "api_key": "…", "web_base": "https://vip.215.im" },
      { "source": "temp.tf", "providers": ["gmail"], "dot": true, "plus": false }
    ]
  }
}
```

外加三条工程约定（全部有前代教训背书）：构建期硬校验（§3.8）、密钥 env 兜底 + 打码（skywork provider.go:54-68,766-776；grok config.go:247）、query-key 默认收敛 header（cloudflare.go:18-22）。

### 5.9 资产复用对照（统一模块从哪抄什么；逐源收录/排除决策见 §7——2026-11-07 修订补）

| 资产 | 出处 | 复用方式 |
|------|------|----------|
| Source/Registry/PickMailboxMode/WaitSourceMessage/能力槽 | skywork internal/tempmail/provider.go | 骨架原样（去 QW2API_ 前缀改模块自有 env 名） |
| temp.tf 客户端与坑位注释 | qw2api internal/tempmail/tempmail.go | providers/temptf.go 直迁 |
| CF 双轨换轨 + detailPaths + admin 模式跳过 Token | skywork cloudflare.go:356-512、recover.go:100-129 | providers/cloudflare.go |
| maliapi Web 建箱通道（首页会话→/api/temp-inbox） | skywork maliapi.go:195-281 | providers/maliapi.go |
| HTTPSpec 声明式 + Assign/Create 双形状 + 幂等建箱 | flonno internal/panel/sources.go | httpspec.go |
| 零凭据降级 + 20QPS + MD5 签名 + 合规过滤重建箱 | runbios sources.go:200-212,725-759,565-571, policy.go:31-40 | providers/g22do.go、emailmux.go、domain.go |
| providerChain 整号换源 | mmxproxy store.go:259-293 | 引擎编排层 |
| CSS-aware 提取 | devin kernel/mail.mjs:141-152 | extract.go（Go 移植） |
| xAI 三段码 + 35s 重发回调 + 宽松收件人匹配 | grok-register app.py:1848-1869,1936,1963-1971 | extract.go + wait.go |
| 自适应轮询 + Provider/Mailbox/Inbox/Health 分层 | DeepAI mailbox.go:20-69,315-363 | wait.go + 接口切分 |
| Livewire 键序回填 | skywork beeinbox.go:111-138 | providers/beeinbox.go（可选，维护成本最高，建议 P2） |

---

## 6. 未验证 / 未找到 / 推测事项（如实清单）

1. **CF worker `/api/token` 端点**：在本树 `cf_temp_main_worker.js` 中 `grep -c "api/token"` = **0**（本次执行）。skywork 实现的 `GET /api/token?address=`（cloudflare.go:305-328）与其引用的 app.py 行号（:1383-1395）在本树 app.py 中对应的是别的代码（本树 `cloudflare_get_token` 位于 ：1461，为 POST `{address,password}`）。**推测**：两者对应不同版本的 cloudflare_temp_email worker/参考快照；统一模块两种 token 流都保留。
2. **flonno 4 源的真实入信**：sources.go:9-11 自述"真实入信链路尚未闭环（intent 4705），Verified 一律 false"——未验证，未编造。
3. **runbios emailmux 签名 secret** `yjd683c@47`（sources.go:366）：来源标注为前端 JS 反解（#8542），本轮未做在线复核。
4. **skywork ← qw2api 谱系方向**：QW2API_ 前缀 env（provider.go:54-68）+ 注释引用 qw2api 中不存在的 `cmd/qw2api/tempmail_setup.go`（skywork autoreg/mailbox.go:12）为实证；方向判定为**推测**（§3.7）。
5. **devin tmaily 建箱**：受 Turnstile 挑战（mail.mjs:4-7 注释自述），本轮未在线复测。
6. **qw2api 无多源**：`grep -rn "Registry|InstallRegistry|NewSource|SourceConfig" cmd` 0 命中（本次执行）；engine.go:218 硬绑 `tempmail.New()`。
7. **各库三方依赖**：本次阅读的邮箱相关代码均只见标准库 import；未逐个打开全部 go.mod 复核，如需可补（不影响"轻依赖可行"结论——所有 observed 实现本身零三方邮箱依赖）。
8. **beeinbox reCAPTCHA v3 是否拦建箱**：skywork 自标 indeterminate（beeinbox.go:50-51）。
9. **manifest 所列 7 插件源码全部找到**（本节基于各路径实读）；未出现"插件源码缺失需标未找到"的情况。grok 插件的部署 provider.json `_source.repo` 指向 phase4 树一事引自 manifest（ask 材料），本轮未重复核验部署实例文件。
10. **freecustom 免费档限流（约 1 次/30s）与 mailsac 本机代理（127.0.0.1:7897）可达性**：均为 mmx providers.go 源码注释/硬编码记载（providers.go:136-233、:265），本轮未在线复测——§7 补收的两源同此（2026-11-07 修订新增）。

---

## 7. 逐源收录决策表（§1 盘点 12 源 → 统一 providers；2026-11-07 修订补）

> 修订背景：§5.2 providers 原只列 8 文件，§1 已盘点的 freecustom / mailsac / devin tmaily / DeepAI static·none 四源被静默落掉，且 §5.9 与 §6 均无收录/排除决策记录（2026-11-07 独立审读意见）。本表对齐 01 §4「收录决策（逐项目）」的严谨度补齐逐源决策；mmx 侧依据 store.go:260-266（providerChain）与 providers.go 实读。

| # | §1 盘点源 | 决策 | 依据与复用方式 |
|---|-----------|------|----------------|
| 1 | 自建 CF Worker（cloudflare_temp_email） | ✅ providers/cloudflare.go | §5.9 原有行 |
| 2 | temp.tf | ✅ providers/temptf.go | §5.9 原有行 |
| 3 | mail.tm（及同构 API） | ✅ providers/mailtm.go | §5.9 原有行（duckmail 同构走同文件） |
| 4 | maliapi / YYDS Mail | ✅ providers/maliapi.go | §5.9 原有行 |
| 5 | 22.do | ✅ providers/g22do.go | §5.9 原有行 |
| 6 | EmailMux | ✅ providers/emailmux.go | §5.9 原有行 |
| 7 | beeinbox.com | ✅（可选 P2）providers/beeinbox.go | §5.9 原有行（Livewire 键序维护成本最高） |
| 8 | Guerrilla Mail | ✅ providers/guerrilla.go | §5.9 原有行（devin 版 sid 会话实现） |
| 9 | tmaily（devin） | ❌ 暂不收录为建箱源 | `/generate` 受 Turnstile、devin 实际只当域名列表源（§1 #9 表注、§6-5；devin kernel/mail.mjs:4-7）；域名能力由 domain.go 承载；Turnstile 解除后可按 HTTPSpec 声明式零逻辑接入 |
| 10 | freecustom.email（mmx） | ✅ 补收 → providers/freecustom.go | mmx providerChain 第 2 顺位（store.go:260-266，[Guerr,FCE,MailTM,Mailsac]），全文无「受限」注记；API 已盘点（§1 #10）；实现 mmx providers.go:136-233 可直迁；免费档 ~1 次/30s 限流进该源轮询间隔缺省 |
| 11 | mailsac（mmx） | ✅ 补收 → providers/mailsac.go | 实现 mmx providers.go:235-277 可直迁；api.mailsac.com 需走本机代理（:265 硬编码 127.0.0.1:7897，§1 #11 表注）→ 收录连带需求：SourceConfig 增可选 `proxy` 字段（string 流动，对齐总纲 00 §1.3-C1「tempmail 不 import egress」裁决） |
| 12 | static / none（DeepAI） | ❌ 不收录为 provider | 非邮箱源：本地拼地址/用户自带邮箱，`WaitForVerification` 恒返回 `ErrNoInbox`（§1 #12，DeepAI mailbox.go:605-646）；统一模块对应物是编排层「邮箱阶段可跳过」语义（03 §2 register 编排），不是 tempmail 的 Source 实现 |

**对 mmx 迁移的影响（修订补充，2026-11-07）**：§5.4-4 已采纳 mmx「整号换源」（providerChain 粒度重开）编排。mmx 现网四源 [Guerr, FCE, MailTM, Mailsac]（store.go:260-266）；若统一 Registry 只有原 8 文件（guerrilla/mailtm 在列，freecustom/mailsac 缺），mmx 迁移后换源链只剩 2 源——freecustom/mailsac 补收后恢复四源对等，§5.4-4 编排方可在 mmx 上无损落地。

**providers 最终清单（10 文件）**：`{cloudflare, temptf, maliapi, mailtm, g22do, emailmux, guerrilla, beeinbox, freecustom, mailsac}.go`——总纲 00 §1.1 包结构行已同步（2026-11-07）。

---

## 附：本次实际执行过的检查（摘要）

- 9 个项目目录 `ls` 全部存在（首个命令）。
- 各库 `grep -rliE "tempmail|temp_mail|mail\.tm|emailmux|22\.do|temp\.tf|beeinbox|maliapi|email_domain"` 定位实现文件（grok/devin/flonno/skywork/mmxproxy/qw2api/runbios/DeepAI 各一次）。
- `grep -c "api/token" cf_temp_main_worker.js` → 0（§6-1）。
- `grep -rn "Registry|InstallRegistry|NewSource|SourceConfig" qw2api/cmd` → 0（§6-6）。
- `cf_temp_main_worker.js` 路由字面量提取（grep -oE，admin 30+ 条、api 28 条）。
- 精读文件：devin kernel/mail.mjs(全)、skywork tempmail.go/provider.go/cloudflare.go/maliapi.go/beeinbox.go(头)/recover.go/random.go/autoreg-mailbox.go(段)、flonno sources.go(全)/autoreg mailbox.go+types.go(段)/worker.go(段)、runbios sources.go(全)/mailbox.go(头)/sources_temptf.go(头)/settings.go(键)/cmd main.go(段)/policy.go(键)、mmxproxy providers.go(全)/store.go(段)/core.go(段)、qw2api tempmail.go(概览+全文 grep)/engine.go(段)/drive.go(键)/server-autoreg.go(键)、DeepAI mailbox.go(全)/mailbox_mailtm.go(头)、grok-register app.py(定向 8 段)/cf_temp_main_worker.js(路由)、grok 壳 config.go/jobs.go/panel.go(定向段)、skywork config.go(段)/tempmailpanel.go(键)/relogin_wiring.go(键)。
