# 01 — Cloudflare 人机验证（Turnstile / Challenge）过盾办法汇总与统一 Go 包设计

> 日期：2026-11-07　|　范围：需求 1（仅分析与设计，不写实现代码）  
> 证据约定：所有结论附源码 `path:line`；未能从源码证实的点标「推测」。  
> 样本来源（manifest）：grok-register-cloudflare-temp-email（重点通读）、flonno2api、runbiosproxy、skywork、grok（外壳）、devin、mmxproxy、qw2api、DeepAICode2API。

---

## 0. TL;DR

1. 全部 13 插件供应商中，真正与 Cloudflare 人机验证交手的是 **4 个 Go 插件（flonno2api / runbiosproxy / skywork / grok 外壳经注册机 exe）+ 1 个 Python 注册机（grok-register）**；devin、mmxproxy、DeepAICode2API 的注册链路没有 CF 人机验证；qw2api 遇到的是**阿里云验证码 2.0（非 CF）**（判定见 §4）。
2. 两大可行通道在代码中均有实证：**真实有头浏览器渲染（CDP + 指纹补丁扩展 + 真实手势）** 与 **打码 API（2captcha 系/YesCaptcha 系）**；「无头 + 指纹」路线在 4 个项目里都留有失效证据，没有任何项目把它作为主通道。
3. 统一 Go 包（`challenge`）应以「**通道接口 + 有序回退**」为核心：浏览器渲染为首选通道（零边际成本、managed 模式实测秒发 token），打码 API 为付费兜底，人工协助为最后的免费兜底；token 一次性语义与「渲染 IP = 提交 IP」是两条必须内建的硬约束（均有实测证据，见 §2.4）。

---

## 1. 方法对比表（方法 × 来源项目 × 依赖 × 风险）

| # | 方法 | 来源项目（证据位置） | 核心依赖 | 配置项 | 成功率 / 失效风险（代码证据） |
|---|------|----------------------|----------|--------|------------------------------|
| M1 | **真实有头浏览器渲染**：CDP 驱动本机 Chrome/Edge + `turnstilePatch` 指纹补丁扩展 + 真实鼠标键盘事件 | grok-register（`turnstilePatch/content.js:7-113`、`grok_register/cli.py:85-90`、`grok_register/app.py:1160-1164`）；flonno2api（`internal/browser/extension.go:17` 内嵌补丁、`internal/autoreg/drive.go:287-297`）；runbiosproxy（`internal/browser/browserturnstile.go:84-165` 全流程）；skywork（`internal/skywork/register_browser.go:14-21`、`internal/browser/cdp.go:1-8`）；grok 外壳（`jobs.go:235-241` 传 `GROK_REGISTER_TURNSTILE_DIR` 给注册机 exe） | 自研 CDP over `gorilla/websocket`（Go 侧）/ DrissionPage（Python 侧）；`go:embed` 内嵌 MV2 扩展 | chrome 路径、profile 隔离目录、代理、headless=false、超时 | runbios 实测 managed 模式真实渲染几秒自动下发 token（`internal/settings.go:40-45`，2026-11-03）；flonno「实测该补丁在有头 Chromium 下可让 Turnstile 免打码服务通过」（`internal/browser/extension.go:5-13`）；风险：需要桌面环境、单机并发受限（grok-register README:606 建议 2–4 线程） |
| M2 | **无头 + 指纹**（headless=new + 反检测） | 无项目作为主通道；各项目仅保留开关并留警告：grok-register（`grok_register/cpa_export.py:131` "headless is frequently Cloudflare-blocked"、`cpa_xai/browser_confirm.py:134-145`）；flonno（`internal/browser/chrome.go:33` 验证码弹窗可能不出、`endpoint.go:121` 自动注册永远传 headless=false）；skywork（`register_browser.go:129`）；runbios（`turnstileprovider.go:42`） | 同 M1 | headless 开关 | **系统性失效风险**：四个项目独立给出「无头易被 Cloudflare 拦 / 验证码弹窗不出」的同类结论（上行各行号），故统一包默认必须 false |
| M3 | **打码 API**（外部付费服务解 Turnstile） | grok-register（`grok_register/xconsole_client/solver.py:34-169`，YesCaptcha `createTask/getTaskResult`）；flonno2api（`internal/autoreg/solver.go:65-188`，2captcha 契约 task type 1400）；runbiosproxy（`internal/autoreg/captcha.go:31-52`，2captcha/capmonster/anticaptcha 三通道） | 纯标准库 `net/http`，无 SDK | `yescaptcha_api_key`（config.example.json）；`FLONNO2API_CAPTCHA_PROVIDER/API_KEY/API_URL/POLL_WAIT/MAX_WAIT`（solver.go:16-22）；`captcha.provider/api_key/site_key/page_url/timeout_seconds`（runbios settings.go:24-36） | 定价证据：TurnstileTaskProxyless 25 分 / M1 30 分（solver.py:22-23）、2captcha $2/1000、capmonster $1.3/1000（captcha.go:11-15）；**风险**：付费 + token 可能被绑浏览器会话/IP 的站点拒绝——runbios 实测空/假 token 恒 400 REGISTRATION_DECLINED（worker.go:71、turnstileprovider.go:9） |
| M4 | **CloudFlareTaskS2（打码 API 解 5 秒 challenge / cf_clearance）** | grok-register（`solver.py:171-193`，自注 experimental） | YesCaptcha | 同 M3 | API 自己声明对 managed challenge（如 auth.grok.com 的 cf_clearance）"may not work reliably"（solver.py:189-193）；项目内无任何成功调用点 → **不建议收录为可靠通道** |
| M5 | **token 一次性语义管理**（复用即失败） | runbiosproxy（`internal/autoreg/turnstile_one_shot_test.go:5-15`：同一 token 第二次提交必得 400 REGISTRATION_DECLINED "This sign-up session has expired."；重试前必须重新取 token） | 无（worker 状态机约束） | — | 硬约束级证据；统一包的缓存/重试设计必须遵守（§3.1） |
| M6 | **IP 一致性约束（渲染 IP = 提交 IP）** | runbiosproxy（`internal/browser/browserturnstile.go:36-38`：「Turnstile 渲染」与「提交注册」必须同一出口 IP，否则 Cloudflare 判 token 无效）；grok-register（`grok_register/cpa_xai/proxyutil.py:455` 代理认证扩展；代理池 429 自动停用） | 浏览器 `--proxy-server` + HTTP 客户端同代理 | `ProxyServer`（browserturnstile.go:37）、`proxy` / `proxy_pool_*`（config.example.json）、`RegisterProxy`（grok 外壳 jobs.go:239-241） | 失效风险：换出口后旧 token 作废 → 统一包必须把代理作为 Request 的一部分传递（§3.1） |
| M7 | **IP 轮换配合（Clash / 代理池）** | grok-register（`proxyutil.py` 头注：random/round_robin 池、429/CONNECT 失败移入 `*.disabled.txt`、thread-local pin）；flonno2api（`cmd/register-browser/main.go:31-34` clash-api/clash-group/clash-port 轮换 flags）；skywork（面板 clash_auto_switch，见 manifest 粗判，未逐行读，标推测程度低） | Clash external-controller API 或代理池文件 | clashApi/clashSecret/clashGroup/clashPort | 与 M6 联动：每账号 pin 一个出口，注册全程不变 |
| M8 | **managed challenge 免交互直过**（真实渲染即出 token） | runbiosproxy（`internal/settings.go:40-45`：「runbios 的 Turnstile 是 managed 模式，真实渲染几秒即自动下发 token，无需点击、无需人机交互」，2026-11-03 实测） | M1 的子情形 | UseBrowser=true（settings.go:42-47） | 使「零成本过盾」成立的前提是目标站为 managed 模式；checkbox 交互式站点仍需 M1 的补丁/手势或 M8' 人工兜底 |
| M9 | **人工协助兜底（user-assist）** | flonno2api（`internal/autoreg/drive.go:287-297` 「若窗口出现复选框请人工点击（最长等 CaptchaWait 默认 5 分钟）」、`drive.go:463-481` waitCaptcha 探测 iframe 消失、`cmd/register-browser/main.go:5-8,34` assist-file 每 2 秒读人工投喂的验证码/魔法链接） | 无 | CaptchaWait（cdp.go:44-45,61）、assist 文件路径 | 成功率取决于人；作为无人值守失败后的降级路径价值高 |
| M10 | **避盾 / 通道选择**（不硬过盾，改走无盾端点或无盾通道） | devin（`kernel/mail.mjs:4-8,14-17`：tmaily 的 `/generate` 在 Turnstile 之后 → 只用作「域列表+读信箱」，建箱改走无盾 guerrilla）；skywork（`register_browser.go:6-9,20-21`：发码端点不校验 token 但**邮件投递侧**要 Turnstile → 发码走浏览器、登录走纯 HTTP）；DeepAICode2API（全链路无验证码，`grep captcha|turnstile --include=*.go` 0 命中） | 无 | 邮箱源选择（mail.mjs SOURCES 的 challenge 标志） | 风险最低；统一模板应在设计期支持「端点盾位探测」清单而不是盲目给所有请求套盾 |

**方法分类维度对照（需求原文要求）**：真实浏览器渲染 CDP → M1/M8；无头+指纹 → M2（证伪）；打码 API → M3/M4；token 复用 → M5（结论：**不可复用**）；IP 轮换配合 → M6/M7；另有 M9 人工协助、M10 避盾两个来源项目实际使用、但需求清单未列的维度。

---

## 2. 各方法细节与代码证据

### 2.1 M1 真实有头浏览器渲染（主力方法）

**Python 参考实现（grok-register，DrissionPage）**

- 指纹补丁扩展 `turnstilePatch/`：MV2 content script，`run_at=document_start`、`all_frames=true` 注入所有页面（`turnstilePatch/manifest.json:2-12`）。补丁内容（`turnstilePatch/content.js`）：
  - 隐藏 `navigator.webdriver`（:7-16）、删除 `chrome.runtime.onConnect/onMessage`（:19-24）、修补 `permissions.query` notifications（:27-35）、伪造 `plugins` 数量（:38-45）与 `languages`（:48-55）；
  - 每 500ms 探测 `iframe[src*="challenges.cloudflare.com"]` 并尝试点击复选框，最多 100 次≈50 秒（:57-113）。
- 加载方式：`ChromiumOptions().add_extension(TURNSTILE_DIR)`（`grok_register/cli.py:85-90`）；目录由 `GROK_REGISTER_TURNSTILE_DIR` 覆盖（`grok_register/paths.py:24-28`），部署时由 worker 拷贝到 home 并注入环境变量（`register_worker.py:63-68,154`）。
- **有头而非无头是刻意的**：`app.py:1160-1164` 注释 "Place Chrome off-screen … This is intentionally NOT headless — Cloudflare/Turnstile need a real page."；实现为屏外坐标 `--window-position=-2400,100` + 防节流三参数（`app.py:1167-1178`），即「不抢前台的有头」。启动 flags 为精简集（`app.py:1142-1152`）。
- OAuth 协议流（无注册页纯 API）里 Turnstile 的用法：sitekey 硬编码 `TURNSTILE_SITEKEY = "0x4AAAAAAAhr9JGVDZbrZOo0"`（`oauth_protocol.py:45`），调用 `solver.solve_turnstile`（:292-300）后把 token 编进 grpc-web 结构 field 1 `turnstileToken`（:19-71）。默认 premium=True（:169-174）。
- CPA mint 的浏览器辅助路径同样加补丁扩展（`protocol_mint.py:131-132`、`browser_confirm.py:118`）。

**Go 侧三个同源实现（互相移植，注释互认）**

- flonno2api：`internal/browser/extension.go:5-13` 注释明说补丁「源自 grok-register 的 turnstilePatch」，`//go:embed turnstilePatch`（:17）落盘临时目录后以 `--load-extension=<dir>` 加载；并警告**扩展内容脚本不在无痕窗口注入，必须以常规窗口启动**（extension.go 文件尾注）。浏览器启动：`FindChrome` 找 chrome.exe/msedge.exe（`chrome.go:80-81`），headless 开关存在但注释「验证码弹窗可能因此不出，默认 false」（:33-34），自动注册链路「永远传 headless=false」（`endpoint.go:121`）。
- runbiosproxy：`internal/browser/browserturnstile.go` 是**把浏览器渲染直接当成一个 token 供给通道**的完整实现：
  - token 探针 JS 三口径兜底：`window.__cfToken` / `turnstile.getResponse()` / `input[name="cf-turnstile-response"]`（:61-71，自注 inferred：按公开 API 形态编写）；
  - 启动参数 `--lang=en-US --disable-background-timer-throming` + `--proxy-server` + `--load-extension` + `--disable-extensions-except`（:114-123）；
  - CDP 开页后每 500ms 轮询探针直到拿到 >20 字符 token 或超时（默认 90s，:84-165）；
  - profile 隔离：turnstile 专用 profile 目录 + 等待上一个实例退出（`WaitProfileFree`，:97-108）。
- skywork：用 CDP 派发**真实输入事件**（`Input.dispatchMouseEvent / dispatchKeyEvent`），因为 JS `element.click()` 是合成事件、不能产生 user activation（`internal/browser/cdp.go:1-8`）；注册页结构用**类名选择器**（`getVerification / otp-input / submitBtn`）而非 placeholder，保证中英文站点通用（`register_browser.go:45-59`）。
- grok 插件外壳自己不实现过盾，只装配：`jobs.go:235-241` 给注册机 exe 注入 `GROK_REGISTER_HOME / GROK_REGISTER_OUTPUT_DIR / GROK_REGISTER_TURNSTILE_DIR` 三个环境变量，并把 `RegisterProxy` 转成 `HTTP_PROXY/HTTPS_PROXY`（:239-241）；注册机默认路径 `register/grok-register.exe`（`config.go:44,91`、`jobs.go:105-110`）。

### 2.2 M2 无头 + 指纹（证伪记录）

四项目各自独立留下「无头不行」的同类证据：

- grok-register：`cpa_export.py:131` "Default headed: headless is frequently Cloudflare-blocked on accounts.x.ai"；`browser_confirm.py:134-145` 打印 "headless=True (may hit Cloudflare / break real clicks)"；README:193 「程序使用真实浏览器（非 headless），需要桌面环境」。
- flonno2api：`chrome.go:33`、`endpoint.go:121`（见上表）。
- skywork：`register_browser.go:129` 「Headless 用无头模式。Turnstile 在无头下可能不过，默认 false。」
- runbiosproxy：`turnstileprovider.go:42` 同口径。

另有历史取证记录：flonno `types.go:25-26` 注明「fact #4480/#4498/#4499 已机制级证伪 headless/headed 浏览器自动化路线」（指其目标站 Clerk Turnstile 的自动化点击路线，最终改用「补丁自动尝试 + 人工兜底」）。**结论：无头只能作为实验开关保留，默认关闭。**

### 2.3 M3 打码 API（付费兜底）

三套 Go/Python 实现的契约对照：

| 维度 | grok-register（YesCaptcha 官方协议） | flonno2api（2captcha 兼容协议） | runbiosproxy（三家可切） |
|---|---|---|---|
| 提交 | POST `/createTask` `{clientKey, task:{type, websiteURL, websiteKey}}`（solver.py:61-91,154-159） | POST `/create.php` form：`task={"type":1400,...}`（solver.go:104-121） | createTask（capmonster/anticaptcha）或 in.php（2captcha）（captcha.go:37-41 注释） |
| 轮询 | POST `/getTaskResult`，status=processing→ready（solver.py:93-131） | GET `/res.php`，`CAPCHA_NOT_READY`→继续（solver.go:146-185） | 同左，默认超时 2min、间隔 3s（captcha.go:39-40） |
| 任务类型 | TurnstileTaskProxyless / **M1（premium）**（solver.py:154） | type **1400**（solver.go:106） | TurnstileTaskProxyless（captcha.go:31-35） |
| 配置 | `yescaptcha_api_key`（config.example.json；`cpa_export.py:142-143`）；env `YESCAPTCHA_API_KEY`（solver.py:237） | `FLONNO2API_CAPTCHA_PROVIDER/API_KEY/API_URL/POLL_WAIT/MAX_WAIT`（solver.go:16-22）；部署 provider.json 同名键 | `captcha.provider/api_key/site_key/page_url/timeout_seconds/poll_interval_seconds`（settings.go:27-36） |
| 防伪 | 返回无 token 即 RuntimeError（solver.py:164-167） | 空 token 一律 `ErrCaptchaTokenEmpty`（types.go:16） | 「严格不伪造 token」：空 token 一律 `ErrTurnstileTokenEmpty`（captcha.go:14-16、worker.go:77-92） |
| stub | 无 | `StubSolver` 返回固定 token 供离线冒烟（solver.go:50-63） | `BlockedTurnstile` 永远返回 `ErrTurnstileUnavailable`（worker.go:77-92）——**宁受阻不造假** |

**关键风险证据**：runbios `worker.go:71` 「#7183 实测空/假 token 均 400 REGISTRATION_DECLINED」→ 说明 runbios 目标站的 token 与浏览器渲染会话绑定，打码 API 的 token 未必可用；这正是把「浏览器通道」设为首选、API 设为兜底的依据。

### 2.4 M4/M5 Token 生命周期硬约束（统一包必须内建）

1. **一次性**：同一 `turnstile_token` 第二次提交 `/api/auth/register` 必得 `400 REGISTRATION_DECLINED "This sign-up session has expired."`（runbios `turnstile_one_shot_test.go:5-7`，2026-11-03 真实浏览器实测）；因此「可重试失败（5xx）在重试前必须重新取一枚 token，绝不复用旧 token」（:13-15）。
2. **有效期**：打码 API 口径 token 有效约 120s（grok-register `solver.py:148`）。
3. **IP 绑定**：渲染与提交必须同一出口 IP，否则 Cloudflare 判 token 无效（runbios `browserturnstile.go:36-38`）。
4. **禁止伪造**：空/假 token 恒被真站拒绝（`worker.go:71`），所有通道实现「拿不到就报错，绝不返回空串」。

### 2.5 M7 IP 轮换配合（与注册流程的协作点）

- grok-register `proxyutil.py` 头注：代理池文件一行一个 `http://user:pass@host:port`，`random | round_robin` 两模式，429/CONNECT 失败自动把节点移入 `*.disabled.txt`，`next_pool_proxy()` 用 thread-local pin（即**线程内稳定、跨线程轮换**）；浏览器侧带认证代理靠动态生成的代理认证扩展 `write_chromium_proxy_auth_extension`（:455）。
- flonno2api `cmd/register-browser/main.go:31-34`：clashApi/clashSecret/clashGroup/clashPort 四个 flag 覆盖 settings.json，注册前经 Clash 策略组轮换出口。
- grok 外壳 `jobs.go:239-241`：把 `RegisterProxy` 注入子进程 env。

### 2.6 M9 人工协助兜底（flonno 独有，值得收录）

- 检测到验证码页后：补丁自动尝试 + 提示人工点击，最长等 `CaptchaWait`（默认 5 分钟，`drive.go:287-297`、`cdp.go:44-45,61`）；通过判定 = `iframe[src*="challenges.cloudflare.com"]` 不可见（`drive.go:463-481`）。
- 验证码/魔法链接可由人写进 `temp/autoreg_code.txt`，程序每 2 秒读取（`cmd/register-browser/main.go:34`、`drive.go` pollCode 的 assistFeed 分支）。

### 2.7 M10 避盾式设计（零成本，优先考虑）

- devin：把「在 Turnstile 之后的端点」与「无盾端点」在邮箱源清单里显式标注 `challenge: false/true`，建箱永远走无盾通道（`kernel/mail.mjs:14-17`）。
- skywork：同一业务链路里「哪个端点有盾」经过实测后混合编排——发码走浏览器（盾在邮件投递侧），登录走纯 HTTP（端点不校验）（`register_browser.go:6-21`）；并以网络日志抓发码请求体（含 `turnstile_token` 与设备指纹）作为排障证据（:736-745）。
- 教训：统一模板的第一步应是**盘清目标站哪些端点有盾**，而不是默认全部请求都要过盾。

---

## 3. 统一 Go 包设计（包名 `challenge`）

### 3.1 对外接口签名草案

```go
// Package challenge 统一封装 Cloudflare 人机验证（Turnstile / managed challenge）
// 的 token 获取通道。设计来源：flonno2api/internal/autoreg（CaptchaSolver 契约）、
// runbiosproxy/internal/autoreg（TurnstileProvider + 三家打码 API）、
// runbiosproxy/internal/browser（真实渲染通道）、grok-register（turnstilePatch + YesCaptcha）。
package challenge

import (
	"context"
	"errors"
	"time"
)

// Kind 标识 Cloudflare 人机验证类别。
type Kind string

const (
	// KindTurnstileManaged：managed 模式，真实渲染即自动出 token（runbios settings.go:40-45 实测）。
	KindTurnstileManaged Kind = "turnstile_managed"
	// KindTurnstileInteractive：checkbox 交互式，需要补丁/手势/人工。
	KindTurnstileInteractive Kind = "turnstile_interactive"
	// KindClearance：5 秒盾 / cf_clearance。grok-register 实测打码 API 不可靠
	// （solver.py:189-193），v1 明确返回 ErrUnsupported。
	KindClearance Kind = "cf_clearance"
)

// Request 一次 token 获取请求。
type Request struct {
	Kind      Kind          // 未指定时按 managed → interactive 顺序自动探测（推测：以探针结果为准）
	PageURL   string        // 承载 challenge 的页面
	SiteKey   string        // turnstile sitekey（0x4...）；浏览器通道可留空（探针按页面读 token，browserturnstile.go:61-71），API 通道必填
	Proxy     string        // 出网代理。硬约束：token 渲染与后续注册提交必须同一出口 IP（browserturnstile.go:36-38）
	Attempt   int           // 第几次尝试（从 1 起）；调用方重试时必须递增并重新 Solve（turnstile_one_shot_test.go:13-15）
	Email     string        // 可选，日志与 sitekey 兜底用（对齐 TurnstileRequest.Email，worker.go:58）
	Headless  bool          // 强烈不建议 true：四项目均有失效证据（§2.2）；默认 false
	Timeout   time.Duration // <=0 取 90s（browserturnstile.go:94-96 口径）
}

// Result 一次 token 获取结果。
type Result struct {
	Token     string        // 永不为空；实现保证拿不到即 error（对齐 runbios「严格不伪造」）
	Provider  string        // "browser" | "yescaptcha" | "2captcha" | "capmonster" | "anticaptcha" | "assist" | "stub"
	Elapsed   time.Duration // 实测耗时（对齐 browserturnstile.go:152-154 的 ElapsedMS）
	ExpiresIn time.Duration // 打码 API 口径约 120s（solver.py:148）；浏览器通道为 0（未知，一次性语义见下）
	OneShot   bool          // 恒 true：token 一次性，重试必须重新 Solve（turnstile_one_shot_test.go:5-7）
}

// Solver 是统一对外接口。语义对齐三处既有契约：
//   - flonno autoreg.CaptchaSolver（types.go:27-29）
//   - runbios autoreg.TurnstileProvider（worker.go:73-75）
//   - grok-register YesCaptchaSolver.solve_turnstile（solver.py:133-169）
type Solver interface {
	Solve(ctx context.Context, req Request) (Result, error)
}

// 哨兵错误。调用方用 errors.Is 分支；文案对齐 runbios types.go:30-32。
var (
	ErrUnavailable   = errors.New("challenge: 通道不可用（无浏览器/无 key/网络失败）")
	ErrTokenEmpty    = errors.New("challenge: 上游返回空 token")           // 绝不静默返回空串
	ErrInvalid       = errors.New("challenge: 请求参数不合法（缺 PageURL/SiteKey）")
	ErrUnsupported   = errors.New("challenge: 该 challenge 类型暂不支持") // 如 KindClearance
	ErrAssistNeeded  = errors.New("challenge: 需要人工协助（弹出窗口点击/投喂文件）")
)
```

**内置实现与组合器**

```go
// BrowserSolver 真实有头 Chrome/Edge 渲染通道（默认通道）。
// 内嵌 turnstilePatch（go:embed，对齐 flonno/runbios extension.go:17）；
// 启动参数对齐 browserturnstile.go:114-123（--load-extension + --disable-extensions-except + --proxy-server）；
// token 探针三口径（browserturnstile.go:61-71）；每 500ms 轮询。
type BrowserSolver struct{ /* ChromePath、ProfileDir、LaunchTimeout… */ }

// APISolver 打码 API 通道（协议自动区分 createTask 系与 create.php 系，
// 对齐 solver.py 与 solver.go/captcha.go 两套契约）。
type APISolver struct{ /* Provider、APIKey、Endpoint、PollInterval、MaxWait、Premium */ }

// AssistSolver 人工协助通道：探测到 checkbox 后等待人工点击/文件投喂
//（对齐 flonno drive.go:287-297 + assist-file）。
type AssistSolver struct{ /* AssistFile、PollEvery */ }

// StubSolver 固定 token，离线冒烟（对齐 flonno solver.go:50-63）。

// ChainSolver 按配置顺序回退：前一个 ErrUnavailable/ErrAssistNeeded 时尝试下一个；
// ErrInvalid/ctx.Err() 直接透传。把 runbios「UseBrowser 与 APIKey 互斥」（settings.go:42-47）
// 升级为可配置的有序回退（推测设计：互斥是 order=[browser] 与 order=[api] 的特例）。
type ChainSolver struct{ Chain []Solver }
```

### 3.2 配置结构体草案

```go
type Config struct {
	Order []string `json:"order"` // 有序回退链，如 ["browser","yescaptcha","assist"]；空 = ["browser"]
	// 对齐既有配置键命名：flonno captcha_provider/captcha_api_key/captcha_api_url（部署 provider.json）、
	// runbios captcha.*（settings.go:24-48）、grok-register yescaptcha_api_key（config.example.json）。
	Browser BrowserConfig `json:"browser"`
	API     APIConfig     `json:"api"`
	Assist  AssistConfig  `json:"assist"`
}

type BrowserConfig struct {
	ChromePath  string        `json:"chrome_path"`   // 空 = FindChrome 自动找（chrome.go:80-81）
	ProfileRoot string        `json:"profile_root"`  // 空 = data/browser-profiles（browserturnstile.go:27-29 口径）
	Headless    bool          `json:"headless"`      // 默认 false；true 会有 §2.2 风险
	ProxyServer string        `json:"proxy_server"`  // 必须与注册 HTTP 客户端同出口（M6 硬约束）
	WaitProfile bool          `json:"wait_profile"`  // 等 profile 空闲，防实例互吞（browserturnstile.go:106-108）
	Timeout     time.Duration `json:"timeout"`       // 默认 90s
	// PatchEmbedded：turnstilePatch 以 go:embed 内嵌，无需外部目录；
	// 兼容旧部署的 GROK_REGISTER_TURNSTILE_DIR 覆盖（paths.py:24-28 / grok 外壳 jobs.go:237）。
}

type APIConfig struct {
	Provider     string        `json:"provider"`      // yescaptcha | 2captcha | capmonster | anticaptcha（runbios captcha.go:31-35）
	APIKey       string        `json:"api_key"`
	Endpoint     string        `json:"endpoint"`      // 空 = 各家默认（captcha.go:37-39）
	PollInterval time.Duration `json:"poll_interval"` // 默认 3s（captcha.go:40）
	MaxWait      time.Duration `json:"max_wait"`      // 默认 120s（solver.go:22）
	Premium      bool          `json:"premium"`       // YesCaptcha M1 档（solver.py:154；oauth_protocol.py:169 默认 true）
	SiteKey      string        `json:"site_key"`      // API 通道必填；浏览器通道不需要
	PageURL      string        `json:"page_url"`
}

type AssistConfig struct {
	Enabled   bool          `json:"enabled"`
	AssistFile string       `json:"assist_file"`   // 人工投喂验证码/魔法链接（flonno register-browser main.go:34）
	MaxWait   time.Duration `json:"max_wait"`      // 默认 5min（flonno CaptchaWait，cdp.go:61）
}
```

### 3.3 与注册流程的对接点

1. **状态机接入点 = solving 步**。既有状态机均为「取邮箱 → solving → 提交注册 → 收码/验证 → 建 key → 入池」（flonno `doc.go:3`；runbios StateSolving；skywork `engine.go:378-381`）。统一模板在 solving 步调用 `Solver.Solve`，token 只进当次提交。
2. **依赖倒置与包方向**：注册包只声明接口，challenge 包提供实现，装配层注入——照抄 runbios 的接线模式：`browser 包单向 import autoreg`，autoreg 不反向 import（`turnstileprovider.go:5-8` 硬约束 + 编译期断言 `var _ autoreg.TurnstileProvider = (*BrowserTurnstile)(nil)`，:29）。统一模板建议：`challenge` 不 import 任何注册包，注册包定义 `TokenProvider` 小接口，由 challenge 实现满足（或直接 import challenge——包方向取决于谁定义接口，推荐后者以省适配层）。
3. **重试纪律**：可重试失败（5xx/网络）→ Attempt+1 并重新 Solve；邮箱类终态（ErrAccountExists 等）直接终止不再重试，避免白烧一次性 token（`turnstile_one_shot_test.go:13-15`）。
4. **IP 一致性纪律**：`Request.Proxy` 与注册 HTTP 客户端、临时邮箱出网必须同一出口；账号级 pin + 池级轮换（M6/M7）。统一模板的注册 worker 应持有「本次注册的出口」句柄并同时传给 challenge 与 HTTP 层。
5. **账号池守护触发**：池低于水位触发注册（grok `keeper.go` 的 pool_min_valid/pool_window_* 模式，manifest 粗判）→ 注册 → solving 步进 challenge。
6. **观测**：Result.Provider/Elapsed 必须进事件日志（对齐 runbios `turnstileprovider.go:88-92,105-107` 的通道/耗时日志与 skywork 的分阶段计时 StageTurnstile，`register_browser.go:149-163`）。

### 3.4 依赖选型建议（Go、轻依赖）

| 决策 | 建议 | 证据 |
|---|---|---|
| CDP 层 | **自研轻量 CDP 客户端 + `github.com/gorilla/websocket`**，不用 chromedp/rod/playwright-go | 四个 Go 插件 go.mod 全部只有 gorilla/websocket 这一个浏览器相关外部依赖（flonno/runbios/skywork/qw2api go.mod 已读）；skywork/qw2api cdp.go:8 明言「只依赖 gorilla/websocket，不引入 cgo、不依赖 Node，保持单文件 exe 分发」；playwright 需要随附 driver 二进制，违背单文件分发（推测：按其官方架构） |
| 指纹补丁 | `go:embed` 内嵌 turnstilePatch 目录（MV2 扩展），运行时落盘临时目录 | flonno `extension.go:17`、runbios `extension.go:17` 均已这样移植并注明「源自 grok-register、零语义改动」（runbios extension.go:1） |
| 打码 API 客户端 | 纯标准库 `net/http` + 两套协议适配（createTask 系 / create.php 系） | flonno solver.go、runbios captcha.go 均零 SDK；grok-register 侧也仅用 requests |
| 浏览器发现 | 内置 FindChrome（chrome.exe/msedge/chromium 常见路径表） | flonno chrome.go:80-81、runbios browserturnstile.go:109、grok-register cli.py:72-83（Linux 路径表）——三家实现高度一致，可合并 |
| 风险登记 | ① MV2 扩展（`turnstilePatch/manifest.json:2`）随 Chrome MV2 退场可能失效，需按 Chromium 实际行为回归（**推测**：Manifest V3 的 content_scripts 仍可用，但需实测）；② 探针 JS 三口径中 `window.__cfToken` 为 grok-register 特有约定（browserturnstile.go:65），换目标站可能只剩后两口径（**推测**） | 见引 |

**排除项**：rod / chromedp / playwright-go / selenium —— 无任何样本项目使用（各 go.mod 已读，grep 无命中），引入即违反「轻依赖 + 单文件 exe」共识。

---

## 4. 收录决策（逐项目，含 qw2api 取舍）

| 项目 | 是否收录进 CF 方法库 | 判定依据 |
|---|---|---|
| **grok-register-cloudflare-temp-email** | ✅ 收录（Python 参考实现；补丁、打码 API、代理池三法齐全） | §2.1–2.3、§2.5 全部证据；README:186-193/624 的 403 处置口径（保存账号继续流程）也值得收录 |
| **flonno2api** | ✅ 收录（Go 侧接口契约 + 补丁内嵌 + 人工协助兜底） | `types.go:27-29` 契约、`extension.go` 内嵌、`drive.go:287-297` user-assist、`solver.go` 打码 API、`cmd/register-browser` Clash 轮换 |
| **runbiosproxy** | ✅ 收录（Go 侧最完整的 token 通道与生命周期约束） | `worker.go:54-92` 契约与防伪、`captcha.go` 三家打码、`browserturnstile.go` 渲染通道、`turnstile_one_shot_test.go` 一次性语义、`settings.go:40-48` managed 实测与 UseBrowser 互斥、`turnstileprovider.go` 适配器与包方向约束 |
| **skywork** | ✅ 收录（真实手势 CDP + 盾位混合编排 + 排障取证） | `register_browser.go:1-31,129,516-545,736-745`、`internal/browser/cdp.go:1-8`（与 qw2api 同源） |
| **grok（phase4 外壳）** | ✅ 收录（作为「外壳把过盾整体委托给注册机 exe」的模式样本） | `jobs.go:105-110,235-241`、`config.go:44,91`；外壳自身零 CF 代码 |
| **devin** | ⚠️ 部分收录（仅 M10 避盾思想） | 注册主链路纯 HTTP 8 步无验证码（`register.mjs:1-13`）；CF 相关仅邮箱源选择：tmaily `/generate` 在 Turnstile 之后改作只读（`mail.mjs:4-8,14-17`）；`devin-core.mjs:955-1005` 的 challenge 是 PKCE code_challenge，与 CF 无关 |
| **mmxproxy** | ❌ 不收录 | 全仓 `grep -rli turnstile` 0 命中（本会话实测命令：`grep -rli "turnstile" D:/AI_Projects/_plugin-work/phase3/mmxproxy`，无输出）；注册为邮箱验证码 6–8 步纯流程（`regtask.go:15,129`、邮箱源清单 `regtask.go:301-303`），目标站无 CF 人机验证证据 |
| **qw2api** | ❌ 不收录进 CF 方法表；其浏览器驱动底座作为 §3.4 选型旁证收录 | **判定：注册涉及人机验证但不是 Cloudflare**。证据：`internal/browser/cdp.go:3-5`「登录/注册页提交按钮挂了**阿里云验证码 2.0**（initAliyunCaptcha，mode=popup）…产出 captchaVerifyParam」；`internal/autoreg/drive.go:21,56`（弹窗选择器 `#aliyunCaptcha-window-popup`）；`internal/server/autoreg.go:9`（纯 HTTP 模拟过不去）。grep 命中的 `deviceflow.go:17-131` 全部是 PKCE `code_challenge`，与 CF 无关。取舍理由：① 验证码提供方、探针选择器、token 字段（captchaVerifyParam vs cf-turnstile-response）均不同，收录进 CF 表会混淆「统一模板要解的问题域」；② 但其「真实手势 CDP 驱动 + gorilla/websocket 单依赖」是统一 challenge 包浏览器底座的直接同源实现，已在 §3.4 引用 |
| **DeepAICode2API** | ❌ 不收录 | 注册链路（邮箱注册→取 Key→验活→入池）无任何人机验证：全仓 `grep -rniE "captcha|turnstile" --include=*.go` 0 命中（本会话实测）；唯一 CF 命中是 `cmd/register/main.go:349` 的 CF 临时邮箱管理端 env（cloudflare_temp_email，收信基础设施，与 CF 盾无关） |
| **宿主 opencode2api_enhance** | ➖ 无注册链路，不适用 | manifest 自述（providers/ 为插件宿主） |

### 统计口径说明

- manifest 粗判的「注册/验证码 N 文件」是关键词面命中，不等于「存在 CF 处理」；本节判定以逐文件阅读为准。
- 「未找到源码」的插件：本次分析覆盖的 9 个项目（3 refs + 7 plugins 减宿主重复计数）全部实际读到了源码，无缺源项目；devin 与 runbiosproxy 部署 provider.json 无 `_source` 字段，按 manifest 给定路径定位源码树（已验证 VERSION/结构对应关系沿用 manifest 记录，未重复核验）。

---

## 5. 遗留不确定点（显式登记）

1. **skywork clash_auto_switch**：来自 manifest 粗判，本次未逐行读其实现（时间盒），标注为未复核。
2. **KindClearance（cf_clearance）**：全部样本中只有 YesCaptcha CloudFlareTaskS2 的「不可靠」注记（solver.py:189-193），没有任何项目实现过真实 5 秒盾通过；统一包 v1 按 `ErrUnsupported` 处理，浏览器渲染取 cookie 的可行性**推测**成立但无样本证据。
3. **打码 API token 与浏览器会话/IP 绑定的边界**：runbios 实测「空/假 token 恒拒」（worker.go:71），但「API 真实 token 在 runbios 站点是否也被拒」未实测；统一包的回退链设计不依赖该结论。
4. **MV2 扩展存续**：见 §3.4 风险登记。
