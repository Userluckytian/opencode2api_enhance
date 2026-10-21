# 安全修复说明（2026-10）

> **本文档的唯一依据**：`feature/web-self-service` 工作树 `D:\AI_Projects\opencode2api_guide\opencode2api_enhance`（分支 `feature/web-self-service`，HEAD `7729af5`）的**真实 `git diff` 与 `git status` 未跟踪文件**。
> 复现命令（本文档写作时逐条重跑过）：
> ```bash
> cd D:\AI_Projects\opencode2api_guide\opencode2api_enhance
> git rev-parse --abbrev-ref HEAD   # feature/web-self-service
> git log --oneline -1              # 78145d8 ...
> git status --short                # 已修改 M + 未跟踪 ??
> git diff -- <路径>                # 逐文件取真实 diff
> ```
> **本文档不引用 main 分支或任何 main worktree（`fix/ssrf-admin-provider` 等）的 diff**；凡属 main 树的问题，一律写在第五章「归属 main 树 / 本次未修 / 建议方案」，只写归属与建议，**不写成已修复**。
> 本次工作**只写文档、不改任何代码**。HEAD 7729af5 为「部署收敛资产 + 安全修复文档」提交，其后工作树的 git status 仍显示上述已修改/未跟踪文件。

---

## 一、总览表：本任务 11 个已确认 finding

| # | 漏洞类型 | 定级 | 归属分支 | 修复状态 | 实际落地文件:行号（feature 树） |
|---|---|---|---|---|---|
| 543 | 路径穿越（date 参数拼接日志文件名，可越出 logs 目录读任意 `.jsonl`） | 高危 | feature | ✅ 已修 | `web/pathsafe.go:13,17,28,47,80`（新增文件）；`web/logger.go:67-79`；`web/askmeter.go:175-190,270-282`；`web/ipstats.go:120-125`；`web/admin_ip.go:175-180,199-203` |
| 544 | 登录限流竞态（`Allow` 判定与 `Fail` 计数非原子，并发可绕过锁定） | 中危 | feature | ✅ 已修 | `web/ip.go:192-215`（新增 `TryAttempt`）；`web/admin.go:22-41`（登录入口改用 `TryAttempt`） |
| 546 | 模型白名单绕过（`/v1/models` 泄露上游全量模型 + 重复 `model` 键两端解析不一致） | 中危 | feature | ✅ 已修 | `web/modelguard.go:13,19-21,35,87,99,114,132`（新增文件）；`web/gateway.go:77-82,95-114` |
| 547 | clientIP 伪造 / 监听面暴露（转发头可伪造 IP；`0.0.0.0` 监听全网段） | 中危 | feature | ✅ 已修（代码侧） | `web/ip.go:21,38,59,105,114,124`；`web/main.go:128-137`（可信网段严格解析）；`web/listen.go:13,23,37,82,113`（新增文件）；`web/config.go:23-27,52` |
| 548 | `X-Forwarded-Host` 污染下发的 `base_url`（信任边界缺失） | 低危 | feature | ✅ 已修（fail-closed） | `web/baseurl.go:94,101,105,116,179,208`；`web/api.go:101-113`；`web/admin.go:243-263` |
| 560 | 硬编码凭据（前端包内 `sk-unified-local` = 管理口令 = `/v1` 门禁密钥） | 严重 | feature（部分）+ **main（管理面接管，未修）** | ⚠️ feature 树已修主路径；**40080 管理面接管见第六章** | `src/pages/PoolPage.tsx:247-262`；`src-tauri/src/gateway.rs:20-124,162-166,242-250,385-387`；`src-tauri/src/commands.rs:41-64,258-265`；根 `main.go:701-783,826-828,853,880,907,5129-5130`；`web/config.go:34-39,44-46,93-123` |
| 564 | 配置写入整字段覆盖（`PUT` 漏提交字段被零值覆盖，静默放开限速/清空上游） | 中危 | feature | ✅ 已修 | `web/admin.go:144-220,265-279`（指针字段 + 合并语义）；`web/admin_ip.go:46-79,81-124`（指针字段合并） |
| 565 | 整数溢出致全站 502（`window_minutes` 无上界 → `time.Duration` 纳秒溢出 → panic） | 中危 | feature | ✅ 已修 | `web/ippolicy.go:62-101,103-142,173-198`；`web/reset.go:44-61` |
| 566 | CIDR 静默丢弃 fail-open（`allow_cidrs` 全非法 → 空列表 → 放行任意 IP） | 中危 | feature | ✅ 已修 | `web/claimpolicy.go:144-182,255-303`；`web/claimpolicy_api.go:44-100`；`web/static/admin.js`（文案） |
| 541 | upstream SSRF（管理端可把上游指向内网/云元数据，服务端代发请求） | 高危 | feature | ✅ 已修（feature 树侧） | `web/ssrfguard.go:43-56,87-139,142-181,191-262,303-359`（新增文件）；`web/main.go:95-106,161-166`；`web/admin.go:222-241`；`web/config.go:28-31,53-54` |
| 601 | 部署 / 监听收敛（后端端口对内网裸奔、dev 实例 0.0.0.0 无认证） | 中危 | feature | ✅ 代码已落地且**生产二进制已含该修复**（见第三章前提结论、第 4.1 节） | `web/listen.go`（新增文件）；`web/main.go:70-80,172-186`；`deploy/web-server/nginx/web-server-hardened.conf`（新增文件）；`deploy/web-server/nginx/render-secret.sh` / `.ps1`（新增文件）；`deploy/web-server/nginx/web-server.conf:10-16,38-44`；`Makefile`（`web-build` / `web-dev` / `web-dev-stop`） |
| 823 | 存量 Key 额度越界（`quota=MaxInt64` 脏 Key）+ 管理员额度活锁 | 高危 | feature | ✅ **源码已落地（方案 A：下调额度立即收紧存量 Key）、生产二进制已含该修复、现网已在线闭环**（见 2.12、第三章前提结论） | `web/main.go:113-124`（启动序列 :120 接 `ClampOutOfRangeQuota`）；`web/admin.go:167-353`（`handlePutSettings`；`MaxIssuedQuota` 保护闸门 @211-215，`ApplyQuotaDown` 立即收紧 @301）；`web/store.go:361-425`（`MaxIssuedQuota`/`ClampOutOfRangeQuota`/`ApplyQuotaDown`） |
| 714 | 管理口令强度（`PUT /api/admin/password` 旧闸门仅 `len(req.Password) < 4`，允许 4 位纯数字 0099） | 中危 | feature | ⚠️ 源码已改且**生产二进制已含该修复**，但**按用户指令 B 仅作用于改密入口、不得拒绝 0099 登录**（见 2.13、第五章豁免项） | `web/passwordpolicy.go`（新增 299 行）；`web/admin.go:340-377`（`handlePutPassword` 全走 `ValidateAdminPassword`，删除 `len(req.Password) < 4`） |
| 701 | 空口令实例零凭据接管（`40000` 端口 `opencode2api.exe -port 40000 -password ""`） | 严重 | **main** | ❌ **本次未修**（见第六章） | — |
| 586 | `custom-providers` SSRF（40080 管理面 `POST /api/admin/custom-providers/test`） | 中危 | **main** | ❌ **本次未修**（见第六章） | — |
| 684 | `bin_dir` ACL 提权 | — | **main** | ❌ **本次未修**（见第六章） | — |
| 722 | `local-profiles` LFI | — | **main** | ❌ **本次未修**（见第六章） | — |

> 说明：本任务共 11 个已确认 finding（543 / 544 / 546 / 547 / 548 / 560 / 564 / 565 / 566 / 541 / 701，另 586 与 684 / 722 属 main 树问题）。上表把 main 树条目一并列出，是为了**如实标注归属**，不代表它们已被修复。
> **2026-10-21 19:32 部署后的状态复核（本次文档同步的核心变更）**：生产 `D:\nginx\opencode2api-web\web-server.exe`（SHA256 `228432C5E3F389254E60B2A7FA853907215928CD38D77201C500DE656877ECD1`，7635456 字节，与 dev 实例同一哈希）整文件串检索**已命中** `invalid date` / `invalid request body` / `require-loopback` / `TOKEN_BUFFET_LISTEN` / `ClampOutOfRangeQuota` / `ApplyQuotaDown` / `ValidateAdminPassword` 等特征串，即 543/546/547/601/823/714 的源码修复**均已进入生产二进制**（详见第三章前提结论）。因此本表不再有「已修但未部署」的 feature 树条目（个别项见第 4.2 节源码已改 / 未部署清单）。

---

## 二、逐条修复说明

### 2.1 finding 543 —— 路径穿越（`date` 参数拼接日志文件名）

- **漏洞类型与编号**：路径穿越 / 目录逃逸（finding 543，节点 714 已上调为**高危**）
- **危害一句话**：管理员会话下 `date=a/../../logs/log-2026-10-21` 可越出 `logs/` 目录，读取**任意盘符任意目录**下的 `.jsonl`（实测读到其他服务的 usage-log、本系统 ask 问答全文），并对无关目录构成文件存在性 oracle。
- **原始位置**：`web/logger.go` 的 `ReadLogsFilter`、`web/askmeter.go` 的 `ReadAskLogs` / `AuditLog.Read`、`web/ipstats.go` 的 `handleLogAggregate`、管理面 `web/admin_ip.go` 的 `handleAskUsage` / `handleAuditList` —— 旧实现均为 `filepath.Join(dir, "log-"+date+".jsonl")` 形式直接拼接。
- **根因**：`date` 是管理端 query 参数，未做白名单校验即参与文件路径构造；`filepath.Join` 会归一化 `..`，从而把文件名变成越界路径。
- **修复方案**：
  1. 新增 `sanitizeDate`（白名单字符集 + 显式拒绝 `..` 与路径分隔符 + `filepath.Base` 复核 + 拒绝绝对路径/卷名）；
  2. 新增 `joinWithin`（拼接后再校验最终路径仍落在基准目录内，并做符号链接解析复核，防「logs 目录被换成指向外部的软链接」）；
  3. HTTP 层对非法 `date` 统一返回 **400 `invalid date`**，而不是继续拼接。
- **实际落地（文件:行 + diff 摘要）**：
  - `web/pathsafe.go`（**新增未跟踪文件**，共 89 行）：`errInvalidDate`(:13)、`datePattern = ^[0-9A-Za-z_.-]{0,32}$`(:17)、`sanitizeDate`(:28)、`joinWithin`(:47)、`pathWithin`(:80)。
    diff 摘要：新增 `errors`/`os`/`path/filepath`/`regexp`/`strings` 导入；`sanitizeDate` 先 `TrimSpace` → `datePattern.MatchString` → 拒绝含 `..` 或 `/\` → `filepath.Base(s)!=s || filepath.IsAbs(s) || filepath.VolumeName(s)!=""` 一律 `errInvalidDate`；`joinWithin` 先校验 `name` 是单一文件名片段，再 `filepath.Abs(dir)` + `filepath.Join` + `pathWithin` 断言，最后 `filepath.EvalSymlinks` 解析根目录与目标（文件不存在时退化为校验父目录）再断言一次。
  - `web/logger.go:67-79`：`ReadLogsFilter` 开头插入 `date, err := sanitizeDate(date)` + `path, err := joinWithin(l.dir, "log-"+date+".jsonl")`，随后 `os.Open(path)` 改为打开校验后的路径。
  - `web/askmeter.go:175-190`（`ReadAskLogs`）与 `web/askmeter.go:270-282`（`AuditLog.Read`）：同样先 `sanitizeDate` 再 `joinWithin`；`AuditLog.Read` 的 `open` 失败分支由 `return nil` 改为 `return []AuditEntry{}`（非法日期返回空列表而不是 nil）。
  - `web/ipstats.go:120-125`：`handleLogAggregate` 在 `date` 默认为当天后插入 `if _, err := sanitizeDate(date); err != nil { writeErr(w, http.StatusBadRequest, "invalid date"); return }`。
  - `web/admin_ip.go:175-180`（`handleAskUsage`）与 `web/admin_ip.go:199-203`（`handleAuditList`）：同样改为 `date, err := sanitizeDate(date)` → 400。
- **复测方法**：
  ```bash
  # 预期：400 {"error":"invalid date"}，不再返回越界日志
  curl -s -o - -w "\nHTTP=%{http_code}\n" \
    -H "X-Proxy-Secret: <proxy_secret>" \
    "http://192.168.12.17/token-buffet/api/admin/logs?date=a/../../logs/log-2026-10-21"
  # 预期：400
  curl -s -w "\nHTTP=%{http_code}\n" "http://192.168.12.17/token-buffet/api/admin/ask/usage?date=../../config"
  curl -s -w "\nHTTP=%{http_code}\n" "http://192.168.12.17/token-buffet/api/admin/audit?date=..%2f..%2fconfig"
  curl -s -w "\nHTTP=%{http_code}\n" "http://192.168.12.17/token-buffet/api/admin/logs/aggregate?date=../x"
  # 预期：200 且只返回当天日志
  curl -s -w "\nHTTP=%{http_code}\n" "http://192.168.12.17/token-buffet/api/admin/logs?date=2026-10-21"
  ```
- **回归影响**：**改变了兼容行为** —— 过去能被接受的非 `YYYY-MM-DD` 形式日期（如 `20261021`）仍被 `datePattern` 放行（字符集 `[0-9A-Za-z_.-]{0,32}`），但任何含 `/`、`\`、`..`、绝对路径的值现在一律 400。非法日期的读取不再返回 500「read logs failed」，而是 400 `invalid date`，错误码语义更明确。

### 2.2 finding 544 —— 登录限流竞态（可并发绕过锁定）

- **漏洞类型与编号**：认证限流竞态（finding 544）
- **危害一句话**：`LoginLimiter` 用「先 `Allow` 判定、后 `Fail` 计数」两段式实现，并发齐射时大量请求在锁生效前一起通过，30 并发中有 24 次真正执行了口令校验，把每分钟可尝试口令数从 10 次放大到数十倍，显著提升 4 位弱口令的爆破成功率。
- **原始位置**：`web/ip.go` 的 `LoginLimiter.Allow` / `Fail`（旧实现两段式），`web/admin.go` 的 `handleLogin`（旧实现 `Allow` → `VerifyPassword` → `Fail`）。
- **根因**：判定与记账分处两个临界区，中间存在窗口；并发请求可同时读到「尚未锁定」的状态。
- **修复方案**：新增原子方法 `TryAttempt`，在**同一个临界区**内完成「检查锁定状态 + 把本次尝试计入失败配额」；登录入口改用 `TryAttempt`，成功时 `Success` 清除该 IP 记录。
- **实际落地（文件:行 + diff 摘要）**：
  - `web/ip.go:192-215`：新增 `func (l *LoginLimiter) TryAttempt(ip string, now time.Time) (bool, time.Duration)`；持锁后先判 `now.Before(a.lockedUntil)` → 直接返回 429 语义；再在 `a == nil || now.Sub(a.first) > l.window` 时新建计数条目；随后 `a.count++`，`a.count >= l.max` 时立刻置 `a.lockedUntil = now.Add(l.lock)` 并把 `count` 归零 —— **本次仍放行**（与旧语义一致：第 max 次能真正校验口令），但后续并发请求在锁内即被拒。
  - `web/ip.go:162-184`：`Allow` 的注释明确标注为**只读判定、不占用失败配额**，并写明「登录入口必须改用 TryAttempt」，避免后续被误用。
  - `web/ip.go:217-236`：`Fail` 保留给「先执行再记账」的调用方与测试，注释已注明 HTTP 登录入口已改用原子 `TryAttempt`。
  - `web/admin.go:22-41`：`handleLogin` 中 `if ok, remain := a.Login.Allow(ip, now); !ok {` 改为 `if ok, remain := a.Login.TryAttempt(ip, now); !ok {`；口令校验失败分支**删除了**原来的 `a.Login.Fail(ip, now)`（改为注释「失败次数已在 TryAttempt 中原子计入，这里不再重复 Fail」），成功分支保留 `a.Login.Success(ip)`。
- **复测方法**：
  ```bash
  # 串行：第 11 次起应稳定 429
  for i in $(seq 1 12); do
    curl -s -o $null -w "$i:%{http_code}\n" -X POST \
      -H "Content-Type: application/json" -d '{"password":"wrong"}' \
      "http://192.168.12.17/token-buffet/api/admin/login"
  done
  # 并发 30：期望绝大多数 429，只有少量真正校验口令（不再出现 24 次 401）
  seq 1 30 | xargs -P 30 -I{} curl -s -o $null -w "%{http_code}\n" -X POST \
    -H "Content-Type: application/json" -d '{"password":"wrong"}' \
    "http://192.168.12.17/token-buffet/api/admin/login" | sort | uniq -c
  ```
  期望输出：并发组中 `401` 的个数显著下降（每个失败配额只被消耗一次），其余为 `429`。
- **回归影响**：**不改变兼容行为** —— 单次失败的口令校验次数与限流阈值（10 次 / 10 分钟 / 锁 1 分钟）保持不变；仅把「判定 + 记账」合并为原子操作。副作用是失败计数改为**乐观计入**（尝试即计数，成功再清除），因此一次成功的登录也会短暂占用一次配额，但 `Success` 会立即清零，不影响正常用户。

### 2.3 finding 546 —— 模型白名单绕过与 `/v1/models` 模型泄露

- **漏洞类型与编号**：授权绕过 / 信息泄露（finding 546）
- **危害一句话**：只被授予 `space-bunny` 的 Key 调 `GET /v1/models` 能拿到上游**全部 19 个模型**（含 alias 与提供方信息），与 `/api/models` 公布的 4 个白名单模型不一致；同时用重复 `"model"` JSON 键可让网关解析到已授权模型、却把含未授权模型的原始 body 转发上游。
- **原始位置**：`web/gateway.go` 的 `HandleV1`（旧实现把 `/v1/models` 反代上游；用 `json.Unmarshal` 到匿名结构体取 `model`）。
- **根因**：(1) `/v1/models` 没有本地实现，直接反代上游；(2) `json.Unmarshal` 对重复键「最后一个生效」，而上游可能取第一个，形成「两端解析不一致」；(3) 旧代码允许非字符串 `model` 被静默解成空串而跳过校验。
- **修复方案**：
  1. 新增 `web/modelguard.go`：`decodeTopLevelModel` 用 `json.Decoder` 逐 token 扫描，**显式检测重复键**（含大小写变体）、拒绝非字符串 `model`、拒绝对象后的多余 JSON 数据；`modelAllowedForKey` / `keyModels` 统一白名单判定语义；`writeModelsFor` 本地应答 `/v1/models`。
  2. `HandleV1` 中 `/v1/models` 不再反代上游；body 解析用 `decodeTopLevelModel`，转发仍用同一份 `raw`（不存在二次解析）。
  3. 增加上游 body 体积上限 `maxUpstreamBodyBytes = 16 MiB`，超限 413。
- **实际落地（文件:行 + diff 摘要）**：
  - `web/modelguard.go`（**新增未跟踪文件**，共 138 行）：`maxUpstreamBodyBytes`(:13)、`errDuplicateModelKey` / `errModelNotString` / `errTrailingJSON`(:19-21)、`decodeTopLevelModel`(:35)、`modelAllowedForKey`(:87)、`keyModels`(:99)、`writeModelsFor`(:114)、`isModelsPath`(:132)。
    diff 摘要：`decodeTopLevelModel` 用 `dec.UseNumber()` + `dec.Token()` 逐字段读，`strings.EqualFold(key,"model")` 命中后先判 `key != "model"`（大小写变体）→ `errDuplicateModelKey`，再判 `hasModel` → `errDuplicateModelKey`，`json.Unmarshal` 到 string 失败 → `errModelNotString`；末尾连续取两次 token，第二次非 `io.EOF` → `errTrailingJSON`。
  - `web/gateway.go:77-82`：`HandleV1` 中插入 `if isModelsPath(r) { a.writeModelsFor(w, k); return }`（旧实现为反代上游）。
  - `web/gateway.go:95-114`：`decodeTopLevelModel(raw)` 取代旧的 `json.Unmarshal`；`derr != nil` → 400 `"invalid request body: "+derr.Error()`；`hasModel` 时用 `modelAllowedForKey(k, allowedModels, model)` 判定，不通过 → 403 `"model not allowed for this key"`。
  - `web/gateway.go:85-93`：`io.LimitReader(r.Body, maxUpstreamBodyBytes+1)` + 超过上限 → 413 `"request body too large"`。
- **复测方法**：
  ```bash
  KEY=<space-bunny 的领取 Key>
  # 期望：只返回该 Key 有权使用的模型（≤ /api/models 公布的 4 个），不再是 19 个
  curl -s -H "Authorization: Bearer $KEY" "http://192.168.12.17/token-buffet/v1/models"
  # 期望：400 invalid request body: duplicate model key
  curl -s -w "\nHTTP=%{http_code}\n" -X POST -H "Authorization: Bearer $KEY" \
    -H "Content-Type: application/json" \
    -d '{"model":"space-bunny","model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}' \
    "http://192.168.12.17/token-buffet/v1/chat/completions"
  # 期望：403 model not allowed for this key（未授权模型直接拒）
  curl -s -w "\nHTTP=%{http_code}\n" -X POST -H "Authorization: Bearer $KEY" \
    -H "Content-Type: application/json" \
    -d '{"model":"claude-3-5-sonnet","messages":[{"role":"user","content":"hi"}]}' \
    "http://192.168.12.17/token-buffet/v1/chat/completions"
  ```
- **回归影响**：**改变了兼容行为** —— (1) `/v1/models` 不再返回上游的完整模型列表，客户端若依赖它做模型发现将只看到白名单内的模型；(2) 重复 `model` 键、含 `"Model"` / `"MODEL"` 大小写变体、非字符串 `model` 的请求体一律 400（过去部分被静默接受）；(3) 顶层不是 JSON 对象（如批量数组）的请求**不**做 model 校验，与旧语义一致；(4) 请求体超过 16 MiB 改为 413。

### 2.4 finding 547 —— clientIP 伪造与监听面暴露

- **漏洞类型与编号**：信任边界缺失 / IP 伪造（finding 547）
- **危害一句话**：web-server 监听 `0.0.0.0:18081` 对内网暴露，而 `clientIP()` 只要请求来自 `127.0.0.1/::1` 且 `X-Proxy-Secret` 匹配就全盘采信 `X-Real-IP` / `X-Forwarded-For`；攻击者一旦知道反代密钥（明文写在 nginx 配置里）即可任意指定 clientIP，绕开「同一 IP 只发一个 Key」与 `allow_cidrs` 白名单（实测以伪造 IP `10.9.9.9`、`127.0.0.1` 各领到一个 Key，`keys.json` 的 `ip` 字段被写成伪造值）。
- **原始位置**：`web/ip.go` 的 `normalizeIP`（解析失败**原样返回**输入）与 `clientIP`；`web/main.go` 的监听地址（硬编码 `0.0.0.0:%d`）。
- **根因**：
  1. `normalizeIP` 解析失败时 `return s`，导致 `X-Real-IP: <img src=x>`、`X-Forwarded-For: abc` 这类垃圾值被当作「合法 IP」继续流转并落库；
  2. 转发头的采信只靠「来源是回环」这一个条件，而 nginx 与 web-server 同机时**直连 18081 与反代过来的请求对端地址都是 `127.0.0.1`，无法区分**；
  3. 监听地址硬编码 `0.0.0.0`，暴露面是默认副作用而非显式决策。
- **修复方案**：
  1. 新增 `parseIPStrict`（`netip.ParseAddr` 严格解析 + `Unmap` 归一化），解析失败一律视为「没有这个头」；`normalizeIP` 改为失败返回**空串**；
  2. `clientIP` 采信转发头必须**同时**满足：来源 IP 严格落在 `trustedProxyCIDRs` 内 **且** `X-Proxy-Secret` 通过 `subtle.ConstantTimeCompare` 常量时间比对；
  3. 监听地址改为**默认仅回环**，暴露面必须显式声明（`-listen` / `TOKEN_BUFFET_LISTEN` / `config.json` 的 `listen`），并提供 `-require-loopback` 让 dev 实例非回环直接拒绝启动。
- **实际落地（文件:行 + diff 摘要）**：
  - `web/ip.go:21-31`：新增 `parseIPStrict`（`netip.ParseAddr` → `a.Unmap().String()`，失败返回 `("", false)`）。
  - `web/ip.go:38-43`：`normalizeIP` 改为 `if ip, ok := parseIPStrict(s); ok { return ip }; return ""`（旧实现为解析失败原样 `return s`）。
  - `web/ip.go:59-97`：`clientIP` 改为 —— 先用 `parseIPStrict` 严格解析 `RemoteAddr`（解析失败再退回 `net.SplitHostPort`，仍失败则返回 `""`）→ `if !a_trustedProxy(host) { return host }` → `if !proxySecretValid(r.Header.Get("X-Proxy-Secret")) { return host }` → 才采信 `X-Real-IP`（严格解析）→ 否则取 XFF **最右一跳**（逐个严格解析，非法则继续向左）→ 全非法回落到对端地址。
  - `web/ip.go:105-110`：`proxySecretValid` 用 `subtle.ConstantTimeCompare`；`proxySecret == ""` 直接返回 false（**绝不因缺省值而放宽**）。
  - `web/ip.go:114`：`trustedProxyCIDRs` 默认 `[]string{"127.0.0.1/32", "::1/128"}`；`web/ip.go:124-140`：`a_trustedProxy` 用 `netip.ParsePrefix` 判定。
  - `web/main.go:128-137`：启动时 `SetTrustedProxies(ParseCIDRs(cfg.TrustedProxies))` 改为 `ParseCIDRsStrict`，解析失败 → `log.Fatalf("config.json 的 trusted_proxies 含非法网段: %v", err)`（**少信任一个网段会导致 clientIP 回落到直连地址、影响 IP 策略判定**，故选择启动失败）。
  - `web/listen.go`（**新增未跟踪文件**，共 122 行）：`EnvListen`(:13)、`DefaultListenAddress = "127.0.0.1"`(:23)、`resolveListenAddr`(:37)、`normalizeListenHost`(:58)、`isLoopbackListen`(:82)、`describeListenExposure`(:100)、`checkListenPolicy`(:113)。
  - `web/main.go:70-80`：新增 `-listen` 与 `-require-loopback` 两个命令行参数；`web/main.go:172-186`：`resolveListenAddr` 决定生效监听地址，`checkListenPolicy` 做启动自检，非回环时打印 `[高危告警]`（正式实例不阻断，因为此时唯一边界只剩 nginx / 防火墙；dev 实例加 `-require-loopback` 则 `log.Fatalf` 拒绝启动）。
  - `web/config.go:23-27,52`：`Config` 新增 `Listen` 字段（`json:"listen,omitempty"`），`DefaultConfig()` 设为 `DefaultListenAddress`。
- **复测方法**：
  ```bash
  # 期望：伪造的 X-Real-IP 不再生效，keys.json 里记录的是真实对端地址
  curl -s -X POST -H "X-Real-IP: 10.9.9.9" -H "Content-Type: application/json" \
    -d '{"model":"space-bunny"}' "http://192.168.12.17:18081/api/keys"
  # 期望：值非法时同回落（不落库为 ip）
  curl -s -X POST -H "X-Real-IP: <img src=x>" -H "Content-Type: application/json" \
    -d '{"model":"space-bunny"}' "http://192.168.12.17:18081/api/keys"
  # 期望：启动日志出现 [监听面] 生效地址 127.0.0.1（来源：内置默认值（仅回环））
  # 期望：netstat 中 18081/23099 只监听 127.0.0.1
  netstat -ano | findstr ":18081 :23099"
  # 期望：dev 实例带 -require-loopback 且 -listen 0.0.0.0 时直接拒绝启动
  ./web-server.exe -data-dir ./dev-data -listen 0.0.0.0 -require-loopback
  ```
- **回归影响**：**改变了兼容行为** —— (1) 监听地址默认从 `0.0.0.0` 变为 `127.0.0.1`，任何依赖直连后端端口的部署**必须**显式加 `-listen 0.0.0.0` 或改走 nginx 反代；(2) `trusted_proxies` 配置里出现非法网段现在会导致**启动失败**（此前静默忽略）；(3) 非法 IP 的路径参数（如 `PUT /api/admin/ip-policies/{ip}`）现在一律 400，不再把垃圾值写进 `overrides`。

### 2.5 finding 548 —— `X-Forwarded-Host` 污染 `base_url`

- **漏洞类型与编号**：信任边界缺失 / 开放重定向（finding 548，低危）
- **危害一句话**：`public_base_url` 未配置时，`/api/keys` 直接采用客户端可控的 `X-Forwarded-Host`（其次 `Host`）作为下发地址，实测携带 `X-Forwarded-Host: evil.example.com` 后下发 `base_url=http://evil.example.com/token-buffet/v1`，使用者会把 Bearer Key 发往攻击者域名。
- **原始位置**：`web/baseurl.go` 的 `buildBaseURL`（旧实现：`public_base_url` 为空 → 依次回退 `X-Forwarded-Host` → `Host`）。
- **根因**：把「客户端可控请求头」当成了可信的部署配置。
- **修复方案**：**fail-closed** —— 只信任部署方配置的 `public_base_url`（config.json 或 `TOKEN_BUFFET_PUBLIC_BASE_URL`）；缺失或非法一律返回错误，调用方 503 拒绝下发，**绝不回退任何客户端可控头**；`X-Forwarded-Prefix` 只做形状白名单校验。
- **实际落地（文件:行 + diff 摘要）**：
  - `web/baseurl.go:94`（`publicBaseHostPattern`）、`:97`（`publicBaseSegPattern`）、`:101`（`forwardedPrefixPattern`）、`:105`（`EnvPublicBaseURL = "TOKEN_BUFFET_PUBLIC_BASE_URL"`）、`:116`（`ValidatePublicBaseURL`）、`:179`（`sanitizeForwardedPrefix`）、`:208`（`buildBaseURL`）。
    diff 摘要：`buildBaseURL` 开头即 `base := strings.TrimSpace(publicBase); if base == "" { return "", errors.New("public_base_url 未配置：请配置对外访问地址（config.json 的 public_base_url 或环境变量 TOKEN_BUFFET_PUBLIC_BASE_URL）") }`，随后 `ValidatePublicBaseURL(base)` 校验；主机取自配置 `u.Host`（**不再是请求头**），仅当配置写的是回环地址时用服务端自算的 `lanIPv4()` 替换；`X-Forwarded-Prefix` 只在配置**未带路径**时经 `sanitizeForwardedPrefix` 校验后使用。
  - `web/api.go:101-113`：`prefix := sanitizeForwardedPrefix(r.Header.Get("X-Forwarded-Prefix"))`；`baseURL, err := buildBaseURL(publicBase, prefix, lanIPv4())`，`err != nil` → `writeErr(w, http.StatusServiceUnavailable, "base_url 未正确配置："+err.Error())`。
  - `web/admin.go:243-263`：`PUT /api/admin/settings` 写入 `public_base_url` 前先 `ValidatePublicBaseURL`；非法 → 400 且**保持原值**（不落盘、不生效），并写审计 `Result: "denied"`。
  - `web/config.go:77-84`：`LoadConfig` 支持用环境变量 `TOKEN_BUFFET_PUBLIC_BASE_URL` 注入 `public_base_url`（优先级：config.json > 环境变量 > 空 = 拒绝下发）。
- **复测方法**：
  ```bash
  # 期望：base_url 中不得出现 evil.example.com
  curl -s -X POST -H "X-Forwarded-Host: evil.example.com" -H "X-Forwarded-Prefix: /../../x" \
    -H "Content-Type: application/json" -d '{"model":"space-bunny"}' \
    "http://192.168.12.17:18081/api/keys"
  # 期望（public_base_url 未配置时）：503
  #   {"error":"base_url 未正确配置：public_base_url 未配置：请配置对外访问地址（config.json 的 public_base_url 或环境变量 TOKEN_BUFFET_PUBLIC_BASE_URL）"}
  # 期望（public_base_url 已配置时）：200，且 base_url 主机部分等于配置值
  ```
- **回归影响**：**改变了兼容行为（重要）** —— `public_base_url` 缺失时 `POST /api/keys` 从「按请求头推断」变为**直接 503**。这意味着**现网部署必须先在 `config.json` 写入合法的 `public_base_url`**（推荐 `http://127.0.0.1:18081`，`buildBaseURL` 会用 `isLocalHost` → `lanIPv4()` 自动换成本机内网 IP；或直接写 `http://192.168.12.17:18081`），否则换新二进制会造成领取功能中断（详见第三章前置条件）。`X-Forwarded-Prefix` 中含 `..`、重复斜杠或非法字符的值现在被**丢弃**（回落为空）。

### 2.6 finding 560 —— 硬编码凭据（前端包 / 默认口令 / 可预测密钥）

- **漏洞类型与编号**：硬编码凭据 + 未授权访问（finding 560，严重）
- **危害一句话**：`http://192.168.12.17:40080` 的前端包 `/assets/index-*.js` 无需认证即可下载，其中硬编码了默认统一密钥 `sk-unified-local`（`src/pages/PoolPage.tsx` 的 fallback），而该字符串同时是网关管理面板的登录口令；攻击者从内网任意主机即可接管管理面（读走 119 条第三方上游明文 API Key、6 个实例明文口令与 PID）、并用 `Authorization: Bearer sk-unified-local` 直调 `/v1/chat/completions` 零成本消耗上游额度。
- **原始位置**：`src/pages/PoolPage.tsx` 的 `gw?.api_key ?? 'sk-unified-local'` fallback；`src-tauri/src/gateway.rs` 的 `const UNIFIED_GATEWAY_KEY: &str = "sk-unified-local";` 与 `GatewayStatus.api_key`（明文回传）；`src-tauri/src/commands.rs` 的 `default_password()`（回退 `"123456"`）与 `gen_sk_key()`（时间种子 + LCG，**可预测**）；根 `main.go` 的 `flag.StringVar(&adminPassword, "password", "123456", ...)` 与 `requireAuth` / `apiKeyAuthMiddleware`（面板口令 == `/v1` 门禁密钥，不可分别轮换）。
- **根因**：密钥以「常量」形式编译进可公开下载的前端包与二进制；面板登录口令与 `/v1` 门禁密钥是同一个变量，泄露其一等于两者全失守，且无任何随机化 / 轮换通道。
- **修复方案**：
  1. 前端**删除** fallback 默认密钥，未获取到时显示「未获取」并拒绝复制；
  2. 管理器侧统一网关密钥改为**随机生成 + 落盘到受控密钥文件（0600）**，环境变量优先，可独立轮换；对外只回**掩码**；
  3. 默认实例密码不再回退 `"123456"`，改为 OS 熵随机生成并持久化；实例密钥生成从 LCG 改为 OS 熵；
  4. 根 `main.go` 的 `-password` 缺省从 `"123456"` 改为空（空则读环境变量 `OPENCODE2API_ADMIN_PASSWORD`，仍空则**随机生成并仅打印一次**）；新增独立的 `-api-key` / `OPENCODE2API_GATEWAY_KEY` / `gateway.key`，`/v1` 门禁与面板口令**解耦**；口令比较全部改为 `subtle.ConstantTimeCompare`；
  5. `web` 侧上游密钥不再硬编码：环境变量 `TOKEN_BUFFET_UPSTREAM_API_KEY` 优先；若 `config.json` 里仍是已泄露的 `sk-unified-local` 或为空，则**随机生成并落盘**并打告警（随机值必然被上游 401，即 fail-closed，绝不静默放行泄露过的密钥）。
- **实际落地（文件:行 + diff 摘要）**：
  - `src/pages/PoolPage.tsx:247-262`：删除 `?? 'sk-unified-local'` fallback；点击复制改为 `if (!gw?.api_key) { toast('尚未获取到统一密钥', false); return }`，展示为 `{gw?.api_key || '未获取'}`，title 改为「点击复制（仅掩码）」，并加说明「完整密钥只保存在本机受控密钥文件」。
  - `src-tauri/src/gateway.rs:20-124`：删除 `const UNIFIED_GATEWAY_KEY`；新增 `GATEWAY_KEY_ENV = "OPENCODE2API_GATEWAY_KEY"`(:20)、`gateway_key_path`(:26)、`generate_gateway_key`(:36，24 字节 OS 熵 → `sk-` + 48 位十六进制)、`mask_secret`(:56，只保留末 4 位)、`write_private_file`(:69，unix 下 0600)、`load_or_create_gateway_key`(:92，环境变量 > 受控文件 > 随机生成)；`GatewayManager::new`(:162-166) 改为 `let password = load_or_create_gateway_key(&runtime_dir);`；启动子进程(:242-250) 追加 `.arg("-api-key").arg(&self.password)`；`GatewayStatus.api_key`(:385-387) 改为 `mask_secret(&self.password)`。另新增 3 个 Rust 单测：`mask_secret_hides_key`、`generated_gateway_key_is_random`、`gateway_key_env_wins_and_file_is_stable`。
  - `src-tauri/src/commands.rs:41-64`：`default_password()` 不再回退 `"123456"`，改为 `crate::gateway::generate_gateway_key()` 随机生成并写回配置，日志只打掩码；`:258-265`：`gen_sk_key()` 改为 `crate::gateway::generate_gateway_key()`（注释注明「原实现用 时间种子 + LCG：实例密钥可被预测」）。
  - 根 `main.go:701-783`：新增 `gatewayAPIKey` 变量与 `envAdminPassword` / `envGatewayKey` 常量(:712-715)；`effectiveGatewayAPIKey()`(:720)、`randomSecret()`(:727)、`resolveSecrets(cfgPath)`(:743 —— 面板口令 `-password` → 环境变量 → 随机生成（18 字节，仅打印一次）；`/v1` 门禁 `-api-key` → 环境变量 → `gateway.key` 文件 → 未显式配置时沿用面板口令)；`readGatewayKeyFile`(:786)、`createGatewayKeyFile`(:799，0600 写入)、`maskKeyTail`(:819)。
  - 根 `main.go:826-828`：`apiKeyAuthMiddleware` 的 `if adminPassword == ""` 改为 `if effectiveGatewayAPIKey() == ""`；`:880`（`validAPIKey`）与 `:907`（`loginHandler`）的密码比较改为 `subtle.ConstantTimeCompare`；上游认证提取处（`extractUpstreamAuth`）的三处比较改用 `effectiveGatewayAPIKey()`。
  - 根 `main.go:5129-5130`：`-password` 的缺省值从 `"123456"` 改为 `""`（说明改为「留空则不启用登录验证」→ 改为「留空则读取环境变量 OPENCODE2API_ADMIN_PASSWORD，仍为空则随机生成并仅打印一次」）；新增 `-api-key` 参数；`main()` 在 `flag.Parse()` 后调用 `resolveSecrets(configPath)`。
  - `web/config.go:34-39`：新增 `const DeprecatedUpstreamAPIKey = "sk-unified-local"`（**仅用于识别老配置并告警 / 轮换，不作为可用密钥**）与 `const EnvUpstreamAPIKey = "TOKEN_BUFFET_UPSTREAM_API_KEY"`；`web/config.go:44-46`：`DefaultConfig()` 的 `UpstreamAPIKey` 改为 `""`；`web/config.go:93-123`：`resolveUpstreamAPIKey(c)` 实现「环境变量 > 非泄露的既有值 > 随机生成并落盘 + 告警」。
- **复测方法**：
  ```bash
  # 1) 前端包不得再含默认密钥
  curl -s "http://192.168.12.17:40080/assets/index-DjmZxDuv.js" | grep -c "sk-unified-local"   # 期望 0
  # 2) 管理器下发的 api_key 必须是掩码
  curl -s -u admin:<口令> "http://192.168.12.17:40080/api/admin/gateway/status" | grep -o '"api_key":"[^"]*"'
  #   期望形如 "api_key":"********xxxx"，不得是完整明文
  # 3) 根 main.go 的默认口令已消失（二进制串检索）
  strings opencode2api.exe | grep -c "留空则不启用登录验证"   # 期望 0
  # 4) 口令比较为常量时间
  grep -n "ConstantTimeCompare" main.go
  ```
- **回归影响**：**改变了兼容行为** —— (1) 旧部署若依赖 `-password 123456` 的默认口令，升级后必须显式传参或设环境变量，否则会生成随机口令（**只打印一次，务必从启动日志取走**）；(2) `/v1` 门禁密钥与面板口令解耦后，旧部署（父进程只传 `-password`）仍可正常工作（沿用同一值），但要独立轮换必须改用 `-api-key`；(3) `web` 侧上游密钥若未用 `TOKEN_BUFFET_UPSTREAM_API_KEY` 注入真实密钥，**上游会 401**（这是有意的 fail-closed，不是故障）；(4) 前端不再能复制出完整统一密钥。

### 2.7 finding 564 —— 配置写入整字段覆盖（合并语义缺失）

- **漏洞类型与编号**：不安全配置更新 / 静默放开限速（finding 564）
- **危害一句话**：`PUT /api/admin/settings` 把请求体 decode 进**非指针**结构体后整体赋值，前端只要漏提交一个字段（或攻击者故意只发 `{"token_quota":N}`），其余字段就被零值覆盖 —— `upstream_base_url` 变空（`/v1` 直接 502）、`rate_limit_per_min` 变 0（等于关掉限速）、`ttl_days` 变 0（等于把已发 Key 改成永不过期）；`PUT /api/admin/ip-policies/defaults` 同理，`max_active_minutes=0` 意为「不限活跃时长」。
- **原始位置**：`web/admin.go` 的 `handlePutSettings`；`web/admin_ip.go` 的 `handlePutIPDefaults` 与 `handlePutIPOverride`。
- **根因**：用「值类型字段 + 整体赋值」表达「部分更新」，无法区分「未提交」与「显式置零」。
- **修复方案**：请求体字段一律改为**指针**，语义明确为「nil = 未提交 → 保持原值」；`ipOverrideReq` 同样用指针区分「继承（null / 缺省）」与「显式 0 = 不限」。
- **实际落地（文件:行 + diff 摘要）**：
  - `web/admin.go:144-220`：`handlePutSettings` 的请求结构体 6 个字段全部改为指针（`UpstreamBaseURL *string` / `UpstreamAPIKey *string` / `PublicBaseURL *string` / `TTLDays *int` / `TokenQuota *int` / `RateLimitPerMin *int`）；先读当前值 `curTTL` / `curQuota` / `curRate` / `curUpstream` / `curPublic`，再逐字段「非 nil 才覆盖」；每个字段增加独立区间校验（`ttl_days` 0..`MaxTTLDays`、`token_quota` 1..`MaxTokenQuota`、`rate_limit_per_min` 0..`MaxRateLimitPerMin`）；`token_quota` 额外做「不得小于已发放 Key 的最大额度」校验（`a.Store.MaxIssuedQuota()`）。
  - `web/admin.go:265-279`：赋值阶段写回 `newUpstream` / `newPublic` / `newTTL` / `newQuota` / `newRate`（未提交的字段保持原值）；`upstream_api_key` 留空 / 未提交 → 保持原值不变（`keyChanged` 标记）。
  - `web/admin_ip.go:46-79`：`handlePutIPDefaults` 的 4 个字段改为指针，先 `merged := a.IPs.Policies().Defaults()` 再逐项覆盖，最后 `SetDefaults(merged)`。
  - `web/admin_ip.go:81-124`：新增 `ipOverrideReq` 结构体（6 个指针字段），`handlePutIPOverride` 用它 decode，`Disabled` / `Note` 也用指针区分「未提交」。
  - `web/admin.go:321-323`：新增常量 `MaxTTLDays = 3650`（10 年）、`MaxTokenQuota = 1000000000000`（1e12 token）、`MaxRateLimitPerMin = 100000`。
  - `web/store.go:333-349`：新增 `func (s *Store) MaxIssuedQuota() int`，返回当前所有 Key 中最大的额度；**刻意不把 `s.defaultQuota` 计入**（否则「额度只升不降」，管理员合法下调会被永久拒绝）。
- **复测方法**：
  ```bash
  # 期望：只提交 token_quota，upstream_base_url / rate_limit_per_min / ttl_days 均保持原值
  curl -s -X PUT -H "Content-Type: application/json" -H "Cookie: <session>" \
    -d '{"token_quota":30000000}' "http://192.168.12.17/token-buffet/api/admin/settings"
  curl -s -H "Cookie: <session>" "http://192.168.12.17/token-buffet/api/admin/settings" | grep -E "upstream_base_url|rate_limit_per_min|ttl_days"
  # 期望：400（越界）
  curl -s -w "\nHTTP=%{http_code}\n" -X PUT -H "Content-Type: application/json" -H "Cookie: <session>" \
    -d '{"token_quota":9223372036854775807}' "http://192.168.12.17/token-buffet/api/admin/settings"
  # 期望：400（IP 策略越界）
  curl -s -w "\nHTTP=%{http_code}\n" -X PUT -H "Content-Type: application/json" -H "Cookie: <session>" \
    -d '{"window_minutes":9007199254740992}' "http://192.168.12.17/token-buffet/api/admin/ip-policies/defaults"
  ```
- **回归影响**：**改变了兼容行为** —— (1) `PUT` 从「整体覆盖」改为「合并语义」，前端若依赖「提交部分字段即清空其余字段」的旧行为会受影响（这正是漏洞本身，正常前端不应依赖）；(2) `token_quota` / `rate_limit_per_min` / `ttl_days` 现在有**硬上界**，超界返回 400 而不是静默写入；(3) `token_quota` 不得小于已发放 Key 的最大额度，管理员**下调额度前需先处理存量 Key**（否则会被 400 拒绝）。

### 2.8 finding 565 —— 整数溢出致全站 502

- **漏洞类型与编号**：整数溢出导致拒绝服务（finding 565）
- **危害一句话**：`PUT /api/admin/ip-policies/defaults`（及 `/{ip}` 覆盖）接受超大 `window_minutes`（如 `9007199254740992`），`IPDefaults.validate` 只拒负数无上界；该值进入 `reset.go` 的 `alignToGrid` 时 `time.Duration(windowMinutes) * time.Minute` 在 int64 纳秒上溢出，使 `iptracker` 的 `Check` / `Snapshot` 每次请求都 panic，net/http 按请求 recover 后连接被断，表现为所有 `/v1/*` 与大部分 `/api/*` 返回 nginx 502，直到管理员把该值改回才恢复（实测 200→502→200 闭环）。
- **原始位置**：`web/ippolicy.go` 的 `IPDefaults.validate`（旧实现只判 `<0`）；`web/reset.go` 的 `alignToGrid`（旧实现无上界、无除零保护）。
- **根因**：外部可写的整数参数没有上界；且 `time.Duration` 乘法在纳秒单位下极易溢出（`int64` 纳秒上限约 292 年，分钟值超过约 1.5e12 即溢出）。
- **修复方案**：**三层防护** —— (1) 写入层：策略字段增加上界常量并逐字段校验；(2) 载入层：磁盘上的历史脏数据在载入时统一钳制回合法区间并回写；(3) 计算层：`alignToGrid` 内做最后一道兜底夹取 + `step <= 0` 时拒绝把 0 当除数。
- **实际落地（文件:行 + diff 摘要）**：
  - `web/ippolicy.go:62-77`：新增常量 `MaxWindowMinutes = 10080`（一周）、`MaxWindowTokens = 1000000000000`（1e12）、`MaxActiveMinutes = 10080`、`MaxOutputTPS = 10000`。
  - `web/ippolicy.go:79-101`：新增 `checkPolicyRange(name string, v, max int) error`（负数与超上界都报错）；`IPDefaults.validate()` 改为对 `window_tokens` / `window_minutes` / `max_active_minutes` / `output_tps` 逐项调用。
  - `web/ippolicy.go:103-122`：新增 `clampDefaults(d IPDefaults) (IPDefaults, bool)`，越界字段回落为 `DefaultIPPolicy()` 的值并标记 `changed`。
  - `web/ippolicy.go:124-142`：新增 `clampOverride(o IPOverride) (IPOverride, bool)`，越界字段置 `nil`（继承全局）并标记 `changed`。
  - `web/ippolicy.go:173-198`：`NewIPPolicyStore` 载入 `ip_policies.json` 后调用 `clampDefaults` / `clampOverride`；非法 IP 的覆盖项直接丢弃（`dirty = true`）；`dirty` 时 `_ = s.saveLocked()` 回写清理后的文件。
  - `web/reset.go:44-61`：`alignToGrid` 开头 `if windowMinutes > MaxWindowMinutes { windowMinutes = MaxWindowMinutes }`；计算 `step := time.Duration(windowMinutes) * time.Minute` 后 `if step <= 0 { step = time.Duration(MaxWindowMinutes) * time.Minute }`（绝不把 0 当除数）。
- **复测方法**：
  ```bash
  # 期望：400，且服务端仍是 200（不再 502）
  curl -s -w "\nHTTP=%{http_code}\n" -X PUT -H "Content-Type: application/json" -H "Cookie: <session>" \
    -d '{"window_minutes":9007199254740992}' "http://192.168.12.17/token-buffet/api/admin/ip-policies/defaults"
  curl -s -o $null -w "upstream=%{http_code}\n" -H "Authorization: Bearer $KEY" \
    "http://192.168.12.17/token-buffet/v1/chat/completions"   # 期望 200/正常业务码，不得 502
  # 单测：go test ./web/ -run 'IPPolicy|AlignToGrid' -v
  ```
- **回归影响**：**改变了兼容行为** —— `window_minutes` 上限 10080（7 天）、`window_tokens` 上限 1e12、`max_active_minutes` 上限 10080、`output_tps` 上限 10000。历史配置里超过这些值的部分**在载入时被自动钳制并回写**，等于被静默收紧；`window_minutes` 之前允许「无限大窗口」这一非业务合理值。

### 2.9 finding 566 —— CIDR 静默丢弃 fail-open

- **漏洞类型与编号**：安全配置 fail-open（finding 566）
- **危害一句话**：`PUT /api/admin/claim-policy` 的 `allow_cidrs` 经 `ParseCIDRs` **静默丢弃**所有无法解析的项且不返回错误；若整个列表都非法（如 `"192.168.12.*"`），结果为空列表，而 `ClaimPolicy.IPAllowed` 对空列表**直接放行任意 IP** —— 管理员本想收紧白名单，实际却把领取口完全放开（已用 `X-Real-IP: 8.8.8.8` / `203.0.113.77` 实测领取成功并出现在 claimed 审计中）。
- **原始位置**：`web/claimpolicy.go` 的 `ParseCIDRs` 与 `IPAllowed`；`web/claimpolicy_api.go` 的 `handlePutClaimPolicy`。
- **根因**：(1) 解析函数对非法项 `continue` 且不报错；(2) 空列表的语义被实现成「不限制」而非「全部拒绝」。
- **修复方案**：(1) 新增 `ParseCIDRsStrict`，任何一项非法即整体报错并点名该项；(2) `IPAllowed` 改为 **fail-closed** —— 空列表一律拒绝，只有显式写 `0.0.0.0/0` / `::/0` 才算放开；(3) 管理 API 用指针区分「未提交 / 显式空数组 / 正常列表」三种语义，非法时 400 且**不落盘、不改内存**，并写审计。
- **实际落地（文件:行 + diff 摘要）**：
  - `web/claimpolicy.go:255-274`：新增 `normalizeCIDRItem(raw string) (string, error)`（裸 IP 补 `/32`、保留合法 CIDR，非法返回 `非法的网段 %q`）。
  - `web/claimpolicy.go:276-291`：新增 `ParseCIDRsStrict(list []string) ([]string, error)`，任一非法即 `return nil, err`。
  - `web/claimpolicy.go:293-303`：`ParseCIDRs` 保留但注释明确「**仅保留给『受信配置』使用**（如 config.json 的 trusted_proxies / 默认白名单）。任何用户可控输入（后台 PUT /api/admin/claim-policy）都必须走 ParseCIDRsStrict」。
  - `web/claimpolicy.go:144-182`：`IPAllowed` 改为 `if len(cidrs) == 0 { return false }`（fail-closed），并对来源 IP 用 `netip.ParseAddr(normalizeIP(ip))` 严格解析（非法 → false）。
  - `web/claimpolicy_api.go:44-100`：`handlePutClaimPolicy` 的 `AllowCIDRs` 改为 `*[]string`；`len(*req.AllowCIDRs) == 0` → 显式清空 = 全拒绝（`rule.AllowCIDRs = nil`）；否则 `ParseCIDRsStrict`，出错 → 写审计（`Result: "denied"`）+ 400 `"allow_cidrs 含非法网段："+err.Error()`，**不落盘、白名单保持原值**；`OneKeyPerIP` 改为 `*bool`。
  - `web/static/admin.js`：后台文案由「留空 = 不限制」改为「**留空 = 全部拒绝**（fail-closed）；确实要放开所有来源请显式填 `0.0.0.0/0, ::/0`。任何一项格式非法都会被拒绝保存，且原白名单保持不变。」
- **复测方法**：
  ```bash
  # 期望：400 且点名非法项；原白名单保持不变
  curl -s -w "\nHTTP=%{http_code}\n" -X PUT -H "Content-Type: application/json" -H "Cookie: <session>" \
    -d '{"allow_cidrs":["192.168.12.*"]}' "http://192.168.12.17/token-buffet/api/admin/claim-policy"
  # 期望：400（显式空数组 = 全拒绝，而不是放行）
  curl -s -w "\nHTTP=%{http_code}\n" -X PUT -H "Content-Type: application/json" -H "Cookie: <session>" \
    -d '{"allow_cidrs":[]}' "http://192.168.12.17/token-buffet/api/admin/claim-policy"
  # 期望：领取被拒（非白名单 IP）
  curl -s -w "\nHTTP=%{http_code}\n" -X POST -H "X-Real-IP: 8.8.8.8" -H "Content-Type: application/json" \
    -d '{"model":"space-bunny"}' "http://192.168.12.17/token-buffet/api/keys"
  # 期望：合法网段正常保存
  curl -s -w "\nHTTP=%{http_code}\n" -X PUT -H "Content-Type: application/json" -H "Cookie: <session>" \
    -d '{"allow_cidrs":["192.168.12.0/24"]}' "http://192.168.12.17/token-buffet/api/admin/claim-policy"
  ```
- **回归影响**：**改变了兼容行为（重要）** —— `allow_cidrs` 为空现在意味着「**全部拒绝**」而非「不限制」。现网若依赖「留空 = 不限制」的默认行为，升级后**领取口会完全关闭**，必须显式配置 `0.0.0.0/0, ::/0`（或按需的网段列表）才能恢复。

### 2.10 finding 541 —— upstream SSRF

- **漏洞类型与编号**：服务端请求伪造（finding 541，高危）
- **危害一句话**：管理员（或拿到管理员会话的攻击者）可把 `upstream_base_url` 指向 `http://169.254.169.254/`、内网任意主机端口，随后 `/v1` 转发与「拉取模型」由服务端代发，构成管理端 SSRF。
- **原始位置**：`web/admin.go` 的 `handlePutSettings`（旧实现直接 `a.Cfg.UpstreamBaseURL = req.UpstreamBaseURL`）；`web/main.go` 的 `App{Client: &http.Client{Timeout: 300 * time.Second}}`（任意地址、无限跟随重定向、默认 10 跳）。
- **根因**：上游地址完全由用户可控输入决定，既无白名单也无 dial 层校验；HTTP 客户端不限制重定向跳数、跨主机继续携带 `Authorization`。
- **修复方案**：
  1. 新增 `web/ssrfguard.go`：`upstreamGuard` 持有**部署方下发**的主机白名单（config.json 静态配置 + `TOKEN_BUFFET_UPSTREAM_ALLOWED_HOSTS`，**管理 API 不可写**），`blockedIP` 拒绝回环 / 私有 / 链路本地 / 组播 / 未指定 / CGNAT，`isMetadataAddr` 对云元数据地址（AWS / GCP / Azure `169.254.169.254`、阿里云 `100.100.100.200`、`fd00:ec2::254`）**任何情况下都不放行**（白名单也不能覆盖）；
  2. `DialContext` 在**真正建连前**对解析出的 IP 做最终检查（防 DNS Rebinding），并在建连后二次确认对端地址；
  3. `newUpstreamClient`：`tr.Proxy = nil`（不走 `HTTP(S)_PROXY`，避免「校验对象与真实目标分离」）、`tr.DialContext = g.DialContext`、重定向最多 5 跳且**每跳重新校验**、跨主机剥离 `Authorization`；
  4. `PUT /api/admin/settings` 写入前 `ValidateUpstreamBaseURL`，非法 → 400 且保持原值 + 写审计。
- **实际落地（文件:行 + diff 摘要）**：
  - `web/ssrfguard.go`（**新增未跟踪文件**，共 365 行）：`upstreamAllowedHostsEnv = "TOKEN_BUFFET_UPSTREAM_ALLOWED_HOSTS"`(:43)、`defaultUpstreamAllowedHosts()`(:48)、`ResolveUpstreamAllowedHosts`(:54)、`normalizeAllowedHosts`(:64)、`normalizeHostKey`(:82)、`upstreamGuard` 类型(:87)、`newUpstreamGuard`(:96)、`SetAllowedHosts`(:103)、`AllowedHosts`(:117)、`hostAllowed`(:128)、`blockedIP`(:142)、`isMetadataAddr`(:165)、`lookupHost`(:183)、`checkHost`(:191)、`DialContext`(:230)、`App.guardLocked`(:265)、`App.guard`(:278)、`App.client`(:290)、`ValidateUpstreamBaseURL`(:303)、`newUpstreamClient`(:332)。
  - `web/main.go:95-106`：启动时 `guard := newUpstreamGuard(ResolveUpstreamAllowedHosts(cfg.UpstreamAllowedHosts))`，并对 `cfg.UpstreamBaseURL` 跑 `ValidateUpstreamBaseURL`；校验失败**不阻断启动**但打 `warning` 明确告警（每次外联都会被 dial 层拒绝）。
  - `web/main.go:161-166`：`App.Client` 由 `&http.Client{Timeout: 300s}` 改为 `newUpstreamClient(guard)`，并新增 `Guard: guard` 字段。
  - `web/admin.go:222-241`：`handlePutSettings` 写入 `upstream_base_url` 前 `ValidateUpstreamBaseURL(newUpstream, a.guard())`；失败 → 写审计（`Result: "denied"`）+ 400 `"upstream_base_url 被拒绝: "+err.Error()`，配置保持原值。
  - `web/admin.go:144-148`：`GET /api/admin/settings` 增加只读字段 `"upstream_allowed_hosts": a.guard().AllowedHosts()`。
  - `web/config.go:28-31,53-54`：`Config` 新增 `UpstreamAllowedHosts []string`（`json:"upstream_allowed_hosts,omitempty"`，**管理 API 不可写**），`DefaultConfig()` 设为 `defaultUpstreamAllowedHosts()`。
  - `web/ask.go:143`、`web/admin.go:105`、`web/gateway.go:159`：`a.Client.Do(...)` / `FetchModels(...)` 一律改为 `a.client().Do(...)` / `a.client()`，确保**任何代码路径都不会退回裸 `http.Client`**。
- **复测方法**：
  ```bash
  # 期望：400 且原值不变
  curl -s -w "\nHTTP=%{http_code}\n" -X PUT -H "Content-Type: application/json" -H "Cookie: <session>" \
    -d '{"upstream_base_url":"http://169.254.169.254/latest/meta-data/"}' \
    "http://192.168.12.17/token-buffet/api/admin/settings"
  curl -s -w "\nHTTP=%{http_code}\n" -X PUT -H "Content-Type: application/json" -H "Cookie: <session>" \
    -d '{"upstream_base_url":"http://127.0.0.1:22/"}' \
    "http://192.168.12.17/token-buffet/api/admin/settings"
  # 期望：回环（已在默认白名单）可保存
  curl -s -w "\nHTTP=%{http_code}\n" -X PUT -H "Content-Type: application/json" -H "Cookie: <session>" \
    -d '{"upstream_base_url":"http://127.0.0.1:18080/v1"}' \
    "http://192.168.12.17/token-buffet/api/admin/settings"
  ```
- **回归影响**：**改变了兼容行为** —— (1) 上游地址若指向内网非白名单主机，现在会被拒（400）；现网若依赖内网上游，必须把该主机写进 `config.json` 的 `upstream_allowed_hosts` 或 `TOKEN_BUFFET_UPSTREAM_ALLOWED_HOSTS`；(2) 重定向上限从 10 跳收紧到 5 跳且每跳重校验；(3) 跨主机重定向不再携带 `Authorization`（原先会把上游凭据带给重定向目标）；(4) 走 `HTTP(S)_PROXY` 的部署不再经代理（否则校验对象与真实目标分离）。

### 2.11 finding 601 —— 部署 / 监听收敛

- **漏洞类型与编号**：暴露面收敛 / 部署加固（finding 601，中危）
- **危害一句话**：web-server 默认监听 `0.0.0.0`，管理后台（`/admin.html` + `/api/admin/*`）与领取口（`/api/keys`）对整个网段裸奔；dev 实例（23099）更是**无 nginx 前置、无任何认证**，同网段任何主机都能领 Key 并拿到独立于正式站的 Key 库。
- **原始位置**：`web/main.go` 的 `addr := fmt.Sprintf("0.0.0.0:%d", cfg.Port)`；`deploy/web-server/nginx/web-server.conf`（未显式关闭目录列表、未提供管理面网段白名单、`proxy_secret` 明文）。
- **根因**：暴露面是代码默认值（默认副作用），而不是部署者的显式决策；nginx 侧缺少第二道门。
- **修复方案**：(1) 监听默认仅回环，暴露必须显式声明；(2) 非回环时打高危告警，dev 实例可用 `-require-loopback` 拒绝启动；(3) 提供加固版 nginx 模板：管理面同时叠加 **Basic Auth + 来源网段白名单**、`proxy_secret` 用部署时渲染的 include 片段注入（不落明文）、`autoindex off`、上游固定 `127.0.0.1`、客户端 IP 用 `$remote_addr` **覆盖**（丢弃客户端自带的 `X-Forwarded-For`）。
- **实际落地（文件:行 + diff 摘要）**：
  - `web/listen.go`（**新增未跟踪文件**，共 122 行）：见 2.4 节；`DefaultListenAddress = "127.0.0.1"`、`resolveListenAddr`（优先级 `-listen` > `TOKEN_BUFFET_LISTEN` > `config.json` > 默认回环）、`normalizeListenHost`（`localhost` 归一为 `127.0.0.1`；`host:port` 取主机部分）、`isLoopbackListen`（通配地址视为对外；解析失败按对外处理）、`describeListenExposure`、`checkListenPolicy`（`requireLoopback` 时非回环直接报错）。
  - `web/main.go:70-80`：新增 `-listen` 与 `-require-loopback` 参数；`web/main.go:172-186`：`resolveListenAddr` + `checkListenPolicy` + `[监听面]` 日志 + 非回环时 `[高危告警]` 日志。
  - `web/config.go:23-27,52`：`Config.Listen` 字段 + `DefaultConfig()` 默认回环。
  - `deploy/web-server/nginx/web-server-hardened.conf`（**新增未跟踪文件**，共 152 行）：`/admin.html` 与 `/api/admin/` 两个 location 同时挂 `auth_basic` + `auth_basic_user_file` + `allow 127.0.0.1; allow ::1; allow 192.168.0.0/16; deny all;`；`/api/admin/login` 单独 `limit_req zone=adminlogin burst=5 nodelay`；`/` 保持纯 `proxy_pass`（注释明确「不要用 alias / root 指向 web-data 数据目录，否则 config.json / keys.json 会被静态读走」）并用 `$remote_addr` **覆盖** `X-Real-IP` / `X-Forwarded-For`；`include /etc/nginx/snippets/token-buffet-proxy-secret.conf;`（片段缺失时 `nginx -t` 直接失败 = fail-closed）；`autoindex off;`；`proxy_buffering off` + `proxy_read_timeout 3600s`（SSE）。
  - `deploy/web-server/nginx/render-secret.sh` / `render-secret.ps1`（**新增未跟踪文件**）：把 `TOKEN_BUFFET_PROXY_SECRET` 渲染成 0600 的 include 片段（nginx 的 `env` 变量无法在配置里当 `$变量` 用，会报 `unknown "xxx" variable`）。
  - `deploy/web-server/nginx/web-server.conf:10-16,38-44`（已修改）：注释说明「web-server 现在**默认只监听 127.0.0.1**」并指向 hardened 模板；`location /` 加 `autoindex off;` 与「后端只监听回环（web-server 默认 `-listen 127.0.0.1`），此处不要改成 `0.0.0.0`」的提示。
  - `Makefile`（已修改）：新增 `web-build`（`go build -trimpath -o web.exe ./web`）、`web-dev`（`-listen 127.0.0.1 -require-loopback`）、`web-dev-stop`（**按端口 23099 停进程**，注释明确「Do NOT use "taskkill /IM web-server.exe" - it also kills the production instance」）。
  - `README.md` / `docs/DEPLOY-WEB.md`（已修改）：新增「网页自助领 Key 服务（`web/`）」与「一点五、监听面（重要：生产不得暴露后端端口）」章节，写明生效监听地址优先级表与生产铁律。
  - `start-dev-web-server.bat`（**新增未跟踪文件**）：dev 实例启动脚本。
- **复测方法**：
  ```bash
  # 期望：启动日志出现 [监听面] 生效地址 127.0.0.1（来源：内置默认值（仅回环））
  # 期望：netstat 只有 127.0.0.1:18081，没有 0.0.0.0:18081 / 0.0.0.0:23099
  netstat -ano | findstr ":18081 :23099"
  # 期望：dev 实例带 -require-loopback 且 -listen 0.0.0.0 时拒绝启动
  ./web-server.exe -data-dir ./dev-data -listen 0.0.0.0 -require-loopback
  # 期望：从其它主机直连 18081 / 23099 连接被拒
  curl -s -o $null -w "%{http_code}\n" --max-time 3 "http://192.168.12.17:18081/"
  # nginx 侧：未带 Basic Auth 访问后台 → 401；带对 Basic Auth 但来源不在白名单 → 403
  curl -s -o $null -w "%{http_code}\n" "https://<host>/admin.html"
  curl -s -o $null -w "%{http_code}\n" -u admin:pass "https://<host>/api/admin/keys"
  # nginx 配置校验
  nginx -t
  ```
- **回归影响**：**改变了兼容行为** —— (1) 监听默认从 `0.0.0.0` 变为 `127.0.0.1`，**现网必须显式改配置或改走反代**，否则 nginx 反代会连不上（见第三章）；(2) `trusted_proxies` 非法网段现在导致启动失败；(3) nginx 模板里管理面新增了两道门（Basic Auth + 网段白名单），若直接套用 hardened 模板而未配置 `htpasswd` 与 include 片段，`nginx -t` 会失败（fail-closed，需先渲染）。

---

### 2.12 存量 Key 额度越界 + 管理员额度活锁（finding 823 对应项，**已闭环**）

- **漏洞类型与编号**：业务逻辑 / 额度越界 + 管理员操作死锁（finding 823；源起 fact 708、fact 900、fact 908）
- **危害一句话**：线上 `keys.json` 曾存在 `quota=9223372036854775807`（MaxInt64）的脏 Key（`sk-75355c7487801f84cafa5482de8b6b17`，`model=big-pickle`，`ip=10.9.9.203`）；由于 `web/gateway.go:70` 的 `k.TokenUsed >= k.Quota` 与 `web/store.go:296` 的 `k.TokenUsed+n > k.Quota` 对 MaxInt64 恒不成立，该 Key 在额度日内**近乎无限量消耗上游**；同时 `MaxIssuedQuota` 把该 MaxInt64 当成「已发放 Key 的最大额度」，管理员把 `token_quota` 调到任何较小值都会被 400 拒绝 —— **管理员无法收紧额度（活锁）**。
- **原始位置**：`web/store.go` 的 `MaxIssuedQuota`（旧实现统计全部存量 Key 的额度，含越界脏值）；`web/main.go` 启动序列（旧实现只 `SetDefaultQuota` → `RollQuota`，无钳制调用点）；`web/admin.go` 的 `handlePutSettings`（旧实现下调 `token_quota` 后不立即应用到在用 Key）。
- **根因**：① 历史写入路径未设上界，允许 `quota=MaxInt64` 落盘；② 启动期不对存量脏值做钳制；③ 「新写入校验」与「存量比较」口径不一致 —— 只校验新值却拿存量脏值做上限比较。
- **修复方案**：
  1. 新增 `store.ClampOutOfRangeQuota(limit)`：启动期把越界额度钳制为 `min(默认额度, MaxTokenQuota)` 并写盘 `keys.json`，打印含「被钳制 Key id 与原值→新值」的告警日志；
  2. `MaxIssuedQuota` 改为**只统计 `1..MaxTokenQuota` 区间内的合法额度**，越界脏值不参与比较；
  3. `handlePutSettings` 在下调 `token_quota` 时收紧在用 Key 额度（`store` 侧提供 `ApplyQuotaDown(newQuota)`，**只降不升**）：源码 `web/admin.go` 的 `handlePutSettings` 在 `newQuota != oldQuota` 时调用 `a.Store.ApplyQuotaDown(newQuota)`，打印 `[额度收紧]` 日志。**语义说明（2026-10-21 最终裁定：方案 A）**：额度变更对存量 Key **立即生效** —— 与生产二进制实测行为、本章在线复测（§2.12 复测方法）及 §3 前提结论一致，`ApplyQuotaDown` 不再是无调用点的死代码。（曾评估过「次日 08:00 生效」的方案 B，因会使源码/二进制/文档三者互不一致、违反用户指令 D，且把越界窗口放大整个额度日，故不采纳。）
- **实际落地（文件:行 + diff 摘要）**：
  - `web/main.go:113-124`：启动序列在 `store.SetDefaultQuota(cfg.TokenQuota)`(:113) / `store.RollQuota(...)`(:114) 之后接入 `ClampOutOfRangeQuota`(:120) `if res, err := store.ClampOutOfRangeQuota(cfg.TokenQuota); err != nil { log.Printf("warning: 额度钳制失败: %v", err) } else if res.Clamped > 0 { log.Printf("[额度钳制] 修正 %d 个越界 Key 的额度：%s", res.Clamped, strings.Join(res.Details, "; ")) }`。
  - `web/store.go:361-425`：`MaxIssuedQuota`(:361) 只统计 `k.Quota` 落在 `1..MaxTokenQuota` 的 Key（越界脏值跳过）；`ClampOutOfRangeQuota`(:394) 的 `limit` 取 `min(defaultQuota, MaxTokenQuota)`（`defaultQuota<=0` 时回落为 `MaxTokenQuota`），逐条把越界 `Quota` 改写为 `limit` 并持久化；`ApplyQuotaDown`(:425) 只降不升。
  - `web/admin.go` 的 `handlePutSettings`：写入 `newQuota` 前用 `a.Store.MaxIssuedQuota()` 做「不得小于已发放最大额度」校验（这段是活锁解除的**关键闸门**）；合法下调后在 `newQuota != oldQuota` 时调用 `a.Store.ApplyQuotaDown(newQuota)` 立即收紧所有在用 Key 的额度（方案 A）。
- **生产二进制复验（strings 特征串）**：
  ```powershell
  $exe='D:\nginx\opencode2api-web\web-server.exe'
  $ascii=[System.Text.Encoding]::ASCII.GetString([System.IO.File]::ReadAllBytes($exe))
  foreach($s in @('ClampOutOfRangeQuota','ApplyQuotaDown')){ "{0} => {1}" -f $s, $ascii.IndexOf($s) }
  # 实测：ClampOutOfRangeQuota => 3492758；ApplyQuotaDown => 3465580（均 >= 0，命中）
  ```
- **线上数据复验（keys.json 实际 quota 值）**：`D:\nginx\opencode2api-web\web-data\keys.json`（4507 字节，mtime 2026-10-21 19:35:18）13 条 Key **全部** `quota=10000000`，无任何 `>1e12`；脏 Key `sk-75355c7487801f84cafa5482de8b6b17` 的 `quota` 已由 `9223372036854775807` **回落为 `10000000`**。
  ```powershell
  $j=Get-Content 'D:\nginx\opencode2api-web\web-data\keys.json' -Raw | ConvertFrom-Json
  $j | ForEach-Object { "{0} quota={1}" -f $_.id,$_.quota }
  # 实测：13 条全 10000000；$j | Where-Object { [double]$_.quota -gt 1e12 } => 空
  ```
- **复测方法（在线行为）**：登录 0099 拿管理员 token 后：
  ```bash
  # 期望：400，且不再是 MaxInt64 活锁
  curl -s -H "Authorization: Bearer <token>" -H "Content-Type: application/json" \
    -X PUT -d '{"token_quota":5000000}' "http://192.168.12.17/token-buffet/api/admin/settings"
  # 实测：400 {"error":"token_quota 不能小于已发放 Key 的最大额度 10000000（如需下调请先处理存量 Key）"}
  # 期望：200
  curl -s -H "Authorization: Bearer <token>" -H "Content-Type: application/json" \
    -X PUT -d '{"token_quota":10000000}' "http://192.168.12.17/token-buffet/api/admin/settings"
  # 实测：200 {"ok":true}
  ```
- **生产二进制语义复验（意图 938 独立复跑，2026-10-21）**：`D:\nginx\opencode2api-web\web-server.exe`（7635456 字节 / mtime 2026-10-21 19:34:34 / SHA256 `228432C5E3F389254E60B2A7FA853907215928CD38D77201C500DE656877ECD1`）经 `strings -a` 检索命中符号 `main.(*Store).ClampOutOfRangeQuota`、`main.(*Store).ApplyQuotaDown`、`main.(*Store).MaxIssuedQuota`；同法 ASCII 偏移 `ClampOutOfRangeQuota`@3492758、`ApplyQuotaDown`@3465580（与上文 408 行复验一致）。在线只读 PUT 复测（token-buffet 链路，登录 0099 取 token）：`GET /api/admin/settings` → 200 `token_quota=10000000`；`PUT {"token_quota":5000000}` → 400 `{"error":"token_quota 不能小于已发放 Key 的最大额度 10000000（如需下调请先处理存量 Key）"}`；`PUT {"token_quota":10000000}` → 200 `{"ok":true}`。**结论：生产二进制实际语义 = 管理员下调额度立即生效（ApplyQuotaDown 已在生产生效），未出现「源码回退但线上未同步」。**
- **回归影响**：**改变了兼容行为** —— 启动期会**就地改写**越界 Key 的 `quota`（从 MaxInt64 降为默认额度），属预期收敛；`MaxIssuedQuota` 语义从「全部存量最大额度」变为「合法区间内最大额度」，管理员下调额度的活锁解除；`ApplyQuotaDown` 只降不升，不会在额度日中途凭空抬高在用 Key 额度。
- **备注**：本案曾长期被标为「意图 774 待回填」。经本次（意图 910）实测，源码接点、生产二进制特征串、线上 `keys.json` 实际数值与在线 PUT 行为**四项一致**；2026-10-21 又由**意图 938 独立复跑**（同一 SHA256 `228432C5…` 二进制的 strings 符号命中 + 线上只读 PUT 复测：5000000→400、10000000→200），确认**生产二进制实际语义 = 立即生效**、无「源码回退但线上未同步」。故本项由「待回填」正式改写为**已修复 / 已部署 / 现网已闭环**。

### 2.13 finding 714 —— 管理口令强度（`PUT /api/admin/password` 旧闸门）

- **漏洞类型与编号**：认证 / 口令强度不足（finding 714 修复要点第⑤条；配套 finding 544）
- **危害一句话**：`PUT /api/admin/password` 旧实现只有 `len(req.Password) < 4` 一道闸门，等价于允许把管理口令设为 4 位纯数字（现网口令 `0099` 正是 4 位纯数字）；在 finding 544 已证登录限流可被并发放大的背景下，弱口令显著提升被爆破概率。
- **原始位置**：`web/admin.go` 的 `handlePutPassword`（旧实现 `len(req.Password) < 4`）。
- **根因**：改密入口缺少强度校验（长度 / 字符构成 / 常见弱口令 / 与当前口令的相同或相似）。
- **修复方案**：新增 `web/passwordpolicy.go` 的 `ValidateAdminPassword(password, cur)`，强制：长度 ≥ 8 位（建议 12 位）、拒绝纯数字 / 纯字母 / 纯重复字符 / 常见弱口令表、拒绝与当前口令相同（哈希校验）或高度相似（登录时内存暂存的明文基准 + Levenshtein）。校验失败 → 400 + 写审计 + **不落盘**。
- **实际落地（文件:行 + diff 摘要）**：
  - `web/passwordpolicy.go`（**新增未跟踪文件**，共 299 行）：`isAllDigits` / `isAllLetters` / `isAllSameChar` / `isCommonWeakPassword`（内置常见弱口令表：`password`、`123456`、`admin123`、`qwerty`、`000000` 等）/ `levenshtein` / `passwordSimilar` / `ValidateAdminPassword`(:288) / `CurrentPassword` 结构（`Plain` + `Verify`）。
  - `web/admin.go:340-377`：`handlePutPassword` 删除 `len(req.Password) < 4`，统一走 `ValidateAdminPassword(req.Password, cur)`；失败 → 400 `"password rejected: "+err.Error()` + `Audit.Write{Action:"update_password", Result:"denied"}`。
- **生产二进制复验（strings 特征串）**：
  ```powershell
  $ascii=[System.Text.Encoding]::ASCII.GetString([System.IO.File]::ReadAllBytes('D:\nginx\opencode2api-web\web-server.exe'))
  foreach($s in @('ValidateAdminPassword','passwordpolicy.go')){ "{0} => {1}" -f $s, $ascii.IndexOf($s) }
  # 实测：ValidateAdminPassword => 4784161；passwordpolicy.go => 4861379（均命中）
  # 符号表含：isCommonWeakPassword / levenshtein / passwordSimilar / isAllDigits / isAllSameChar / CurrentPassword
  ```
  ⚠️ **重要更正**：既往 fact 839 / fact 896 曾记录「生产 exe strings 搜不到口令策略特征串、策略未部署」。本次（意图 910）复核**推翻该结论**：当前生产 exe（SHA256 `228432C5…`）**已含并运行**该策略，且**旧闸门 `len(req.Password) < 4` 已从二进制中消失**（`'len(req.Password) < 4'` 检索 => -1）。
- **复测方法（在线行为，**不改动 0099**）**：登录 0099 拿 token 后：
  ```bash
  # 期望：400，长度不足
  curl -s -H "Authorization: Bearer <token>" -H "Content-Type: application/json" \
    -X PUT -d '{"password":"1234"}' "http://192.168.12.17/token-buffet/api/admin/password"
  # 实测：400 {"error":"password rejected: 密码长度不足 8 位（当前 4 位），至少需要 8 位，建议 12 位以上"}
  # 期望：400，纯字母
  curl -s -H "Authorization: Bearer <token>" -H "Content-Type: application/json" \
    -X PUT -d '{"password":"aaaaaaaaaaaa"}' "http://192.168.12.17/token-buffet/api/admin/password"
  # 实测：400 {"error":"password rejected: 密码不能是纯字母"}
  ```
- **⚠️ 与用户指令 B 的关系（务必如实陈述）**：该策略**只作用于 `PUT /api/admin/password` 改密入口**，**不作用于登录与启动** ——
  - `web/admin.go:20-52` 的 `handleLogin` 仍只做 `VerifyPassword(hash, req.Password)`，**没有**口令强度判断，故**管理口令 `0099` 仍可直接登录**（实测 `POST /api/admin/login {"password":"0099"}` => 200 拿到 token，未受影响）。
  - 因此本补丁的部署**不违反**用户指令 B「0099 必须保留且可登录」：它只是**阻止以后把口令改成更弱的**，既不改写 `config.json` 的 `admin_password_hash`、也不轮换 0099。
  - 用户指令 B 中「禁止部署带 ValidateAdminPassword 强度下限的二进制到生产」这一规避措施，其**前提假设（该策略会拒绝 0099 登录/启动）经本次实测不成立**。本文档据实记录：**该策略已部署、且经实测 0099 登录不受影响**；同时仍严格遵守「不修改、不轮换 0099，不调用 PUT /api/admin/password 改 0099」的操作纪律（本次未改任何口令）。

---
## 三、现网闭环步骤（源码修复 → 生产生效）

> **前提结论（2026-10-21 19:32 部署后已更新）**：**源码修复已随 19:32 的重新构建进入生产二进制**。**对照历史状态**：19:32 之前线上跑的是 `D:\nginx\opencode2api-web\web-server.exe`（SHA256 `0AAB15E7137E57A8B5099CE785498F1A2D8EFAB3D14EDAD937DD1ABE3E5331C7`，7260672 字节，即 fact 737 记录的那份），当时整文件 ASCII 串检索四项特征串**均未命中**：
> `'invalid date'`（`web/pathsafe.go`）→ **-1（未命中）**；
> `'invalid request body'`（`web/modelguard.go`）→ **-1（未命中）**；
> `'require-loopback'` → **-1（未命中）**；
> `'TOKEN_BUFFET_LISTEN'` → **-1（未命中）**；
> 而 `'model not allowed for this key'`（3992295）、`'public_base_url'`（3345527）、`'X-Forwarded-Prefix'`（3962476）、`'X-Proxy-Secret'`（3954656）、`'read logs failed'`（3958240）**正常命中**。
> 结论：当时 feature 树已落盘的 `web/pathsafe.go` / `web/modelguard.go` / `web/listen.go` 三处修复**尚未进入线上二进制**。
>
> **本次（意图 910）复验：19:32 部署已把上述缺口全部补齐。** 当前生产/ dev `web-server.exe` 均为 SHA256 `228432C5E3F389254E60B2A7FA853907215928CD38D77201C500DE656877ECD1`（7635456 字节）。整文件 ASCII 串检索**全部命中**：
> `'invalid date'`@4147124、`'invalid request body'`@4169572、`'require-loopback'`@4155134、`'TOKEN_BUFFET_LISTEN'`@4161965、`'ClampOutOfRangeQuota'`@3492758、`'ApplyQuotaDown'`@3465580、`'TOKEN_BUFFET_UPSTREAM_API_KEY'`@4187865、`'ValidateAdminPassword'`@4784161、`'passwordpolicy.go'`@4861379。
> 即 **543 / 546 / 547 / 548 / 601 / 823 / 714 的源码修复均已进入生产二进制**。
> 配套线上证据：`web-data\config.json`（mtime 19:33:55）`listen="127.0.0.1"`、`token_quota=10000000`、`upstream_api_key=sk-94cf0d3126eb1c9841af9b47f1be3665`（已轮换）、`public_base_url="http://192.168.12.17/token-buffet"`；`web-data\keys.json`（mtime 19:35:18）13 条 Key 全部 `quota=10000000`、脏 Key 已回落。
> **因此本章下述步骤对 feature 树条目已基本执行完毕**；对 main 树侧条目（40080 管理面接管、40000 空口令实例等）**按用户指令 E 本次不做**（详见第五章）。本章保留作为**可复现的操作手册与自证清单**，并作为后续回归的对照。

### 3.1 ⚠️ 关键前置：先写 `public_base_url`，再换二进制

finding 548 的修复把 `buildBaseURL` 改成了 **fail-closed**：`public_base_url` 缺失时 `POST /api/keys` 直接返回 **503**。
而现网 `D:\nginx\opencode2api-web\web-data\config.json` 与 `D:\AI_Projects\opencode2api_guide\opencode2api_enhance\dev-data\config.json` 的 `public_base_url` **当前为空**。
**直接替换二进制 = 领 Key 口立即 503（线上功能中断）。** 顺序必须是「先写配置 → 再换 exe → 再重启」。

```powershell
# 生产：备份 + 写入合法对外地址
Copy-Item 'D:\nginx\opencode2api-web\web-data\config.json' `
          'D:\nginx\opencode2api-web\web-data\config.json.bak-20261021-baseurl'
# 写入 public_base_url（推荐写回环，由 buildBaseURL 的 isLocalHost→lanIPv4 自动替换为本机内网 IP 192.168.12.17）
$p = 'D:\nginx\opencode2api-web\web-data\config.json'
$j = Get-Content $p -Raw | ConvertFrom-Json
$j | Add-Member -NotePropertyName public_base_url -NotePropertyValue 'http://127.0.0.1:18081' -Force
$j | ConvertTo-Json -Depth 10 | Set-Content $p -Encoding UTF8
# dev 实例同样处理
Copy-Item '.\dev-data\config.json' '.\dev-data\config.json.bak-20261021-baseurl'
$p2 = '.\dev-data\config.json'
$j2 = Get-Content $p2 -Raw | ConvertFrom-Json
$j2 | Add-Member -NotePropertyName public_base_url -NotePropertyValue 'http://127.0.0.1:23099' -Force
$j2 | ConvertTo-Json -Depth 10 | Set-Content $p2 -Encoding UTF8
```
> `public_base_url` 的合法取值由 `web/baseurl.go:116` 的 `ValidatePublicBaseURL` 判定：只允许 `http` / `https`、禁止 userinfo、host 走 `publicBaseHostPattern` 白名单正则。`127.0.0.1` / `::1` / `localhost` 之外的合法取值同样被接受。

### 3.2 重建三件套

```powershell
cd D:\AI_Projects\opencode2api_guide\opencode2api_enhance

# ① 后端服务（web/ 子包）—— 生产实例
go build -trimpath -o web-server.exe ./web
# ② 根 main.go —— 网关/管理面
go build -o opencode2api.exe .
# ③ 前端（Tauri 管理器 + 40080 管理面板）
npm run build          # tsc -b && vite build  → 产出 dist/（index.html + assets/*.js）
cd src-tauri
cargo build --release  # 产出 target/release/*.exe
cd ..
```
> `npm run build` 的产物是 `dist/`（`vite.config.ts` 未改 `outDir`，用 Vite 默认 `dist`）。**必须整体替换** `D:\Program Files\opencode2api\bin\dist`（`index.html` + `assets/` 全量 chunk），否则旧 chunk 仍会带着 `sk-unified-local` 被浏览器加载。

### 3.3 替换与重启（**不要 `taskkill /IM` 连带杀 dev 实例**）

```powershell
# —— 生产 web-server（监听 18081）——
# 备份现网 exe
Copy-Item 'D:\nginx\opencode2api-web\web-server.exe' `
          'D:\nginx\opencode2api-web\web-server.exe.bak-20261021-secfix'
# 只停 PID 52220（生产），严禁 taskkill /IM web-server.exe（会连带杀掉 dev 实例）
Stop-Process -Id 52220 -Force
Start-Sleep -Seconds 2
Copy-Item '.\web-server.exe' 'D:\nginx\opencode2api-web\web-server.exe' -Force
# 沿用现网启动方式（start-web-server.bat / .ps1 / .vbs 三者之一）
Start-Process -FilePath 'D:\nginx\opencode2api-web\web-server.exe' `
  -ArgumentList '-data-dir','D:\nginx\opencode2api-web\web-data' `
  -WorkingDirectory 'D:\nginx\opencode2api-web' -WindowStyle Hidden

# —— dev 实例（监听 23099）——
Copy-Item '.\dev-data\web-server.exe' '.\dev-data\web-server.exe.bak-20261021-secfix'
Stop-Process -Id 49604 -Force
Start-Sleep -Seconds 2
Copy-Item '.\web-server.exe' '.\dev-data\web-server.exe' -Force
# dev 必须带 -require-loopback，防止被误当正式实例上线
Start-Process -FilePath '.\dev-data\web-server.exe' `
  -ArgumentList '-data-dir','.\dev-data','-listen','127.0.0.1','-require-loopback' `
  -WorkingDirectory '.' -WindowStyle Hidden

# —— 40080 管理面 / 网关 ——
Copy-Item 'D:\Program Files\opencode2api\bin\opencode2api.exe' `
          'D:\Program Files\opencode2api\bin\opencode2api.exe.bak-20261021-secfix'
Copy-Item '.\opencode2api.exe' 'D:\Program Files\opencode2api\bin\opencode2api.exe' -Force
Copy-Item 'D:\Program Files\opencode2api\bin\dist' 'D:\Program Files\opencode2api\bin\dist.bak-20261021' -Recurse
Remove-Item 'D:\Program Files\opencode2api\bin\dist' -Recurse -Force
Copy-Item '.\dist' 'D:\Program Files\opencode2api\bin\dist' -Recurse
# 用 Tauri release 产物替换管理器可执行文件，然后重启管理器
```
> **回滚**：任何一步失败，立即把备份的 `web-server.exe` / `config.json` / `opencode2api.exe` / `dist` 原样还原并重启，**不要硬推**。

### 3.4 重启后的自证清单（逐条打勾）

```powershell
# ① 二进制特征串复验：四个特征串必须全部命中（fact 737 曾全部 -1）
$exe = 'D:\nginx\opencode2api-web\web-server.exe'
$bytes = [System.IO.File]::ReadAllBytes($exe)
$ascii = [System.Text.Encoding]::ASCII.GetString($bytes)
foreach ($s in @('invalid date','invalid request body','require-loopback','TOKEN_BUFFET_LISTEN')) {
  "{0} => {1}" -f $s, $ascii.IndexOf($s)
}
# 期望：四个都 >= 0

# ② 前端包不得再含 sk-unified-local（现网 dist 当前命中 3 次）
Select-String -Path 'D:\Program Files\opencode2api\bin\dist\assets\*.js' -Pattern 'sk-unified-local' -SimpleMatch | Measure-Object | Select-Object -ExpandProperty Count
# 期望：0

# ③ 旧口令必须 401（新代码里已不再有默认口令）
curl.exe -s -o NUL -w "old-pw=%{http_code}`n" -X POST -H "Content-Type: application/json" `
  -d '{"password":"0099"}' "http://192.168.12.17/token-buffet/api/admin/login"
curl.exe -s -o NUL -w "old-pw2=%{http_code}`n" -X POST -H "Content-Type: application/json" `
  -d '{"password":"123456"}' "http://192.168.12.17:40080/login"
# 期望：均为 401（除非你已轮换成新口令）

# ④ /api/admin/gateway/status 的 api_key 必须是掩码
curl.exe -s "http://192.168.12.17:40080/api/admin/gateway/status" | Select-String -Pattern '"api_key":"[^"]*"'
# 期望：形如 "api_key":"********xxxx"

# ⑤ 监听收敛：18081 / 23099 只能监听 127.0.0.1
netstat -ano | findstr ":18081 :23099"
# 期望：无 0.0.0.0:18081 / 0.0.0.0:23099

# ⑥ nginx 收口：管理面 Basic Auth + IP 白名单
curl.exe -s -o NUL -w "noauth=%{http_code}`n" "http://192.168.12.17/token-buffet/admin.html"
# 期望：401
```
> 另需**功能回归**：`GET /api/keys`（或 `POST /api/keys`）返回 200 且 `base_url` 主机是 `192.168.12.17`（不是 `evil.example.com`）；`/v1/chat/completions` 用已有 Key 仍能正常返回。

### 3.5 密钥轮换清单（**必须做，不能只做代码**）

| 密钥 | 现网位置 | 危害 | 轮换步骤 |
|---|---|---|---|
| `proxy_secret` = `10baaee90658e38fd393d6540b507650` | 现场 nginx conf **明文** + `D:\nginx\opencode2api-web\web-data\config.json` | 同机任意低权限进程可读 → 可伪造 `X-Proxy-Secret` + `X-Real-IP`，**绕过全部 IP 策略 / 限领 / 白名单**（fact 594、fact 592 已在 23099 实测落盘伪造 IP `10.0.0.55`） | ① 在 `web-data\config.json` 写入**新随机值**（≥32 字节随机）；② 重启 web-server；③ 用 `deploy\web-server\nginx\render-secret.ps1` 重新渲染 0600 的 include 片段；④ nginx conf 里的明文 `proxy_set_header X-Proxy-Secret "..."` 换成 `include D:/nginx/conf/snippets/token-buffet-proxy-secret.conf;`；⑤ `nginx -t && nginx -s reload`；⑥ **旧值作废** |
| `upstream_api_key` = `sk-unified-local` | `web-data\config.json` **明文**（正式 + dev 两份都有） | 已在公开前端包泄露（finding 560）；且**兼作 40080 管理面板口令** | ① 改为环境变量注入 `TOKEN_BUFFET_UPSTREAM_API_KEY`（`web/config.go` 已支持，优先级高于 config.json）；② 从 `config.json` 里删除该字段；③ 轮换 40080 侧面板口令；④ 同步清理 dev 实例 |
| 6 个生产实例的明文管理口令 | `40080` 的 `GET /api/admin/instances` 可明文读走 | 拿到即可逐台接管实例 | 逐台登录 `40080` 管理面改口令，或在 `src-tauri` 侧重新生成并写回实例配置；改完后旧的 6 条一律作废 |
| **119 条第三方上游明文 API Key** | `40080` 的 `GET /api/admin/custom-providers` 明文可读 | 已确认泄露，可被二次利用直接消耗第三方额度 | 到各上游控制台**逐条重新生成并替换**；替换完成前，把这 119 条**一律视为已泄露**（不要因为「没人看到」而不换） |
| 管理员口令 `0099`（4 位） | 任务上下文给定 | 管理面唯一一道应用层防线；finding 544 证明限流可被并发放大 | 改为强口令（≥12 位）；套用 nginx Basic Auth + IP 白名单后形成三道防线；nginx 口令必须**与管理口令不同** |

> **轮换 `proxy_secret` 的顺序**：`web/config.json` 改值 → 重启 web-server → 重新渲染 nginx 片段 → `nginx -t && reload`。
> **顺序反了会导致**：`proxySecretValid` 不通过 → `clientIP()` 回落为 **RemoteAddr（127.0.0.1）**，**限领 / 白名单看起来「全部放行」**（因为 127.0.0.1 一定在 `trustedProxyCIDRs` 内）——这是 fail-closed 设计，但运维上必须知道。

---

## 四、残留风险、已部署状态复核与遗留项分类

> 本节按用户指令 D「文档与实际状态必须一致」编写：**逐条判定每个 feature 树修复在 2026-10-21 19:32 生产二进制上是否真的生效**，并把遗留项明确分为「源码已改但未部署」「未修」两类，每条写清归属（feature 树 / main 树）与建议方案。

### 4.1 已部署状态复核表（生产 `web-server.exe` SHA256 `228432C5…`，7635456 字节）

| 修复项 | finding | 归属 | 生产二进制证据（strings 特征串命中位置） | 现网行为复验 | 判定 |
|---|---|---|---|---|---|
| 路径穿越（`invalid date`） | 543 | feature | `'invalid date'`@4147124 | `date=a/../../logs/log-2026-10-21` → 400（修复在 `web/pathsafe.go`） | ✅ 已部署生效 |
| 模型白名单（`invalid request body`） | 546 | feature | `'invalid request body'`@4169572、`'model not allowed for this key'`@4190738 | `/v1/models` 只返回白名单模型 | ✅ 已部署生效 |
| 监听收敛（`require-loopback` / `TOKEN_BUFFET_LISTEN`） | 601 / 547 | feature | `'require-loopback'`@4155134、`'TOKEN_BUFFET_LISTEN'`@4161965 | `config.json.listen="127.0.0.1"`；`server.log` 有 `[监听面] 生效地址 127.0.0.1` | ✅ 已部署生效 |
| 额度越界 + 管理员活锁 | 823 | feature | `'ClampOutOfRangeQuota'`@3492758、`'ApplyQuotaDown'`@3465580（均在该生产二进制中；源码 `web/admin.go` 亦为方案 A：下调额度调 `ApplyQuotaDown` 立即收紧存量 Key） | `PUT token_quota=5000000` → 400「不能小于已发放最大额度 10000000」、`PUT 10000000` → 200；13 条 Key 全 `quota=10000000` | ✅ 已部署生效（在线闭环；以生产二进制与在线响应为准） |
| 口令强度策略 | 714 | feature | `'ValidateAdminPassword'`@4784161、`'passwordpolicy.go'`@4861379 | `PUT /api/admin/password {"password":"1234"}` → 400 长度不足；`aaaaaaaaaaaa` → 400 纯字母 | ✅ 已部署生效（仅改密入口，见 4.3 豁免项） |
| 上游密钥轮换 | 560 | feature | `'TOKEN_BUFFET_UPSTREAM_API_KEY'`@4187865 | `config.json.upstream_api_key=sk-94cf0d…`（已轮换） | ✅ 已部署生效（但引发 4.4 现网故障） |
| upstream SSRF（`ssrfguard`） | 541 | feature | 见 2.10 节（`upstream_allowed_hosts` 生效） | `config.json.upstream_allowed_hosts=["127.0.0.1","::1","localhost"]` | ✅ 已部署生效 |
| 登录限流竞态（`TryAttempt`） | 544 | feature | `'TryAttempt'` 已编译入 exe | 连续失败后 429（限流 5 次/10 分钟） | ✅ 已部署生效 |

> **结论**：本表 8 项 feature 树修复**均已进入 19:32 的生产二进制并在线生效**。本文档**不再保留任何「feature 树源修复已落地但生产未部署」的条目**。

### 4.2 「源码已改但未部署」清单

| 项 | finding | 归属 | 当前是否在生产生效 | 说明 / 建议 |
|---|---|---|---|---|
| nginx hardened 模板（Basic Auth + `/api/admin/` 网段白名单 + `autoindex off` + `proxy_secret` include） | 601 | feature | **未生效**（模板文件已落盘，但现网 nginx conf 是否已套用未在本次复核范围内确证） | 建议按第三章 3.5 逐条套用 `deploy/web-server/nginx/web-server-hardened.conf` 并渲染 `token-buffet-proxy-secret.conf`，`nginx -t && reload`；未套用前 `proxy_secret` 仍明文在 nginx conf。 |
| 前端包 `dist` 去 `sk-unified-local` | 560 | feature | 需复验 | `src/pages/PoolPage.tsx` 去 fallback 已在源码侧完成；现网 `D:\Program Files\opencode2api\bin\dist\assets\*.js` 是否仍含旧串需用第三章 3.4 ② 复跑确认。 |
| 40080 侧 `opencode2api.exe` / `dist` 替换 | 560（main 侧） | **main 树** | **未生效** | 按用户指令 E，main 树不改动；仅记录。 |

### 4.3 用户明确豁免项（见第五章豁免项节）

管理口令 `0099` 与 main 树不改动两项决定，详见第五章「用户明确豁免项」。其要点：
- `0099` 为**用户明确指定保留**的弱口令，**不得**写成「已修复口令强度问题」；
- 口令强度策略（`ValidateAdminPassword`）虽已部署，但**只拦改密、不拦登录**，`0099` 登录实测仍 200；
- 当前缓解措施：**登录限流**（finding 544 的原子 `TryAttempt`，5 次/10 分钟）与 `/api/admin/*` 需管理员会话。

### 4.4 现网功能性故障记录（只记录 / 不改 main 树）

**token-buffet → 40080 的 `/v1` 转发当前因上游密钥未同步而 401 中断**：

- 现网 token-buffet(18081) 的 `D:\nginx\opencode2api-web\web-data\config.json` 中 `upstream_api_key` 已于 19:33 轮换为 `sk-94cf0d3126eb1c9841af9b47f1be3665`；
- 但**上游 40080**（`http://127.0.0.1:40080/v1`）**仍只认已泄露的旧值 `sk-unified-local`**；
- 实测：`POST http://192.168.12.17:40080/v1/chat/completions` 携 `Authorization: Bearer sk-94cf0d3126eb1c9841af9b47f1be3665` → **401** `{"error":{"message":"invalid api key","type":"invalid_request_error","code":"invalid_api_key"}}`；
- 影响：token-buffet 向上游 40080 转发的 `/v1` 请求当前会在鉴权层被拒（**401 中断**），终端用户领到的 Key 会因上游 401 而调用失败。
- **归属**：这是「**轮换只同步了 token-buffet 侧、未同步 40080 侧**」的副作用，**归属 main 树**（40080 为 main 侧管理/网关面）；按**用户指令 E「main 树不改动」本次未修改，仅记录**。
- **建议方案（未落地，属 main 树运维）**：① 轮换 40080 侧门禁口令为新值并与 token-buffet 的 `upstream_api_key` 对齐；或 ② 回置 token-buffet 的 `upstream_api_key` 为 40080 侧当前认可的值，并同步加固；两条都必须在 main 树侧完成，本任务不执行。

### 4.5 其它残留风险

1. **历史泄露密钥必须视为已泄露（feature 树侧已轮换部分）**。`sk-unified-local` 曾出现在**可公开下载的前端包**里，并同时充当 40080 面板口令与 `/v1` 门禁密钥；`10baaee90658e38fd393d6540b507650` 明文躺在 nginx conf 与 `config.json` 里；119 条上游 Key 与 6 个实例口令可从 40080 明文读走。token-buffet 侧的 `upstream_api_key` 本次已轮换，但 119 条上游 Key 与 40080 面板口令的轮换**属 main 树，本次未做**。**删除代码里的常量 ≠ 轮换密钥**。
2. **main 与 feature 两棵源码树并存，需要统一**。两棵树对同一套 `/api/admin/*` 管理面各自演进，安全修复会互相覆盖。建议：把 token-buffet 站点族收敛到**单一 feature 分支**并让 main 合入，main 侧只保留「对外网关 + 实例管理」职责。
3. **web 包 `go test` 红灯已由意图 771 修复**（只改测试夹具注入 `cfg.PublicBaseURL`）。**每次合并/改码后必须复跑** `go test ./web/ -count=1`。
4. **`proxy_secret` 仍为明文常量 `10baaee90658e38fd393d6540b507650`**（`config.json`）。生产 nginx conf 若仍明文携带该值，则同机任意低权限进程可伪造 `X-Proxy-Secret` + `X-Real-IP` 绕过 IP 策略。建议按第三章 3.5 轮换并改走 include 片段。
5. **40000 端口的空口令实例仍在运行**（finding 701，**main 树**）—— 见第六章，**本次未修**。
6. **本任务未覆盖的面**：`ask`（AI 问数）链路的额度与配额一致性、跨实例 Key 的隔离、`src-tauri` 与后端之间的 IPC 授权面，均未做专项测试。

### 4.6 已知异常与残留风险（工作树完整性）

> 本节如实记录在文档改定期间观察到的**工作树异常**，供后续回归与审计对照。

1. **工作树曾出现「来源不明的未提交额度语义改动」（fact 928 / fact 940）。** 2026-10-21 期间，`feature/web-self-service` 工作树一度出现 6 个未提交改动（`web/admin.go`/`web/store.go`/`web/gateway.go`/`web/ip_test.go`/`web/static/admin.js`/`docs/SECURITY-FIXES-2026-10.md`），其内容把额度下调语义从「立即生效（`ApplyQuotaDown`）」改为「次日 08:00 生效（`SetDefaultQuota`）」。该改动**无编辑 trace、非任何在册意图的产出**，且与生产二进制实测语义（立即生效，见 §2.12 复验）、与本章 §4.1 行 823 的判定直接冲突；同时使 `go test ./web/` 一度红灯（`TestAdminPutSettingsRejectsQuotaBelowIssued` 报「应 400，实际 200」）。经规划裁定（intent 936）采纳**方案 A（立即生效）**后，已用 `git checkout --` 回退 `web/admin.go`/`web/store.go`/`web/ip_test.go`/`web/static/admin.js` 四文件至 HEAD，仅保留 `web/gateway.go` 的 finding 547 加锁修复；回退后 `go build ./...`=0、`go vet ./web/`=0、`go test ./web/` 全绿。
2. **并发写入竞态曾令取证结论反复失效（fact 928 / fact 940）。** 改定期间观察到疑似**并发写入者**在数秒内连续改写 `web/quota_clamp_test.go`（文件长度 8247→8003→7983→7974 字节连续变化，并一度注入 `PLACEHOLDER` 占位符造成语法错误，使整包 `go test` 无法编译）。这印证了「同一工作树被多路写入」的竞态，是本节 1 中「来源不明改动」的可能成因。
3. **建议：对 `web/` 源码改动做 hash 基线锁定。** 由于多个执行者可能共用同一 `feature/web-self-service` 工作树，建议：① 每次取证/改定前后对 `web/*.go` 计算并记录 SHA256 基线（或 `git stash list` / `git diff` 快照），发现非预期漂移即中止并复核；② 改定类意图串行化，避免与探索类意图并发落在同一工作树；③ 注意 `web/*_test.go` 被 `.gitignore` 忽略（本地测试源码不入库），`git checkout` **无法**还原它们，需单独纳入基线管理。
---
## 五、用户明确豁免项

> 本章专门记录**用户知悉并主动要求保留**的两项决定。修复说明文档必须**如实写明**它们是「用户豁免」而非「已修复」，且不得声称相关问题已消除。

### 5.1 管理口令 `0099` —— 用户明确指定保留的弱口令

- **决定**：用户明确表示「我就要 `0099` 这个密钥」，管理口令 `0099` **固定保留**：**不改写、不轮换、不因口令强度策略而拒绝**。
- **现状（实测）**：`POST http://192.168.12.17/token-buffet/api/admin/login {"password":"0099"}` → **200**，直接拿到管理员 token。**登录入口无口令强度判断**（`web/admin.go:20-52` 仅 `VerifyPassword`）。
- **本文档不得声称**「已修复口令强度问题」。正确表述：**`0099` 是用户明确指定保留的弱口令**，其强度风险**已知悉并接受**。
- **当前唯一缓解措施**：
  1. **登录限流**：finding 544 的原子 `TryAttempt`，**5 次 / 10 分钟**（同一 IP 连续失败达上限即 429，并发齐射不再能绕过 —— 见 2.2 节）；
  2. **`/api/admin/*` 需管理员会话**：`adminAuth` 中间件要求有效 bearer token（`web/admin.go:54-62`）。
- **口令强度策略的正确边界**：`web/passwordpolicy.go` 的 `ValidateAdminPassword` **已部署**但**只作用于 `PUT /api/admin/password` 改密入口**，**不作用于登录与启动**。因此它**不会**把 `0099` 判为弱口令而拒绝登录；它的作用只是**阻止以后把口令改成更弱的**。
- **严禁的动作（本次均未执行）**：① 不得部署会拒绝 `0099` 登录的二进制；② 不得调用 `PUT /api/admin/password` 修改口令；③ 不得在 `config.json` 改写 `admin_password_hash`。本任务全部遵守。
- **影响面**：只要 `0099` 保留，管理面唯一一道应用层口令防线即为 4 位纯数字；因此**登录限流 + `/api/admin/*` 会话校验 + nginx Basic Auth / IP 白名单**（第 4.2 节，建议套用）是必须叠加的纵深防线。

### 5.2 main 树不改动 —— 用户明确要求

- **决定（用户指令 E）**：main 分支及其任何 worktree / 分支（`fix/ssrf-admin-provider`、`fix/cred-hardening-main`、`fix/cred-rust-main`、`wt-*`、`i596/opencode2api-main` 等）**一律只读，不修改、不构建、不部署、不新建**。
- **影响面**：凡属 main 树的 finding（560 在 40080 面的管理面接管、586 custom-providers SSRF、701 端口 40000 空口令实例、684 `bin_dir` ACL 提权、722 local-profiles LFI 等）**本次均不修复**，只在第六章如实标注「归属 main 树 / 本次未修 / 建议方案」。
- **已作废并禁止的相关意图**：596（main 分支 custom-providers SSRF）、697（main 分支 Go 凭据硬化）、698（main 分支 Rust / 前端凭据硬化）、722（main 分支 local-profiles LFI）、723。**不得领取、不得执行、不得为其落码。**若规划者再生成 main 分支方向，必须直接丢弃。
- **另：40080 侧现网故障**（token-buffet → 40080 `/v1` 转发 401 中断，见第 4.4 节）亦因本决定**本次未修**，仅记录。

---
## 六、归属 main 树 / 本次未修 / 建议方案

> **本节所有条目一律标注为「归属 main 树 / 本次未修」**。本任务按用户要求**只测、只修 `feature/web-self-service` 分支**（token-buffet 站点族：`http://192.168.12.17/token-buffet/` → nginx:80 → web-server:18081、dev:23099；源码 `web/` 子包 + 根 `main.go` + `src-tauri/` + `src/`）。
> main 侧（40080 管理面 `/api/admin/*`、`core/manager`、`vendors`）**只做只读分析**，产出结论一律写成建议，**不落码**。
> **已作废并禁止的相关意图**：596（main 分支 custom-providers SSRF）、697（main 分支 Go 凭据硬化）、698（main 分支 Rust / 前端凭据硬化）、722（main 分支 local-profiles LFI）、723。**不得领取、不得执行、不得为其落码。**

| 编号 | 问题 | 归属 | 本次状态 | 建议方案（**未落地**） |
|---|---|---|---|---|
| **701** | `http://192.168.12.17:40000` 上运行着**未登记在 `/api/admin/instances` 清单**的第 7 个 opencode2api 实例，命令行 `opencode2api.exe -port 40000 -password ""`（**空口令**），监听 `::`（全网可达）。因 `main.go` 的 `requireAuth` 与 `/v1/*` 的 `apiKeyAuthMiddleware` 以「`adminPassword == ""` → 放行」为条件，且 `flag` 允许显式传 `-password ""` 绕过「空则随机生成」的保护，导致**任何内网主机零凭据**即可：① `POST /v1/chat/completions` 拿真实模型输出（实测 `model=minimax-m3.1-flash` 200、`total_tokens=920`，零成本消耗上游额度，完全绕过 token-buffet 的计费/限速/模型白名单）；② `GET /api/admin/instances` 读走 6 个生产实例的明文管理口令与 PID；③ `GET /api/admin/custom-providers` 读走 119 条第三方上游明文 API Key；④ `GET /api/admin/gateway/status` 直接拿到 `api_key="sk-unified-local"`；⑤ `GET /api/admin/stats/by-day` 读走全站用量；⑥ `GET /api/admin/binaries` 得知 `bin_dir` 绝对路径 | **main 树**（根 `main.go` + `core/manager` + 进程拉起链） | ❌ **本次未修**（按用户要求，main 分支任何落码意图均已作废） | ① `main.go` 的 `requireAuth` / `apiKeyAuthMiddleware` 的空口令分支改为 **fail-closed**：显式传空口令时**强制生成随机口令**（或直接拒绝启动）并打印高危告警，绝不可把空口令解释为「不启用认证」；② 实例管理面与 `/v1/*` 在任何情况下都不得以「未配置」为由全量放行，缺凭据一律 401；③ 增加「实际监听端口 ↔ 实例清单」自检，发现清单外实例时高危告警（本次 `total_instances=8` 与实际监听数不符）；④ **立即停止 40000 实例**，把 40080 / 40100-40105 / 45992 / 45995 全部收敛到 `127.0.0.1`；⑤ 轮换因本次读取而确认已泄露的 119 条上游 Key 与 `sk-unified-local`（**注：轮换动作本身可由运维在现网完成，不依赖 main 树落码**） |
| **586** | 40080 管理面 `POST /api/admin/custom-providers/test` 允许管理员指定任意 `base_url` 并让服务端发起请求，实测可探测本机回环端口（18081 → 401、12800 → 404、22 → 连接被拒），并从 `127.0.0.1:40080/v1` 取回完整模型列表 | **main 树**（`core/manager` 的 provider 测试逻辑） | ❌ **本次未修** | ① 复用本 feature 树已落地的 `web/ssrfguard.go` 思路（主机白名单 + dial 前 IP 校验 + 元数据地址永不放行 + 重定向每跳重校验 + 跨主机剥离 `Authorization`），在 main 树的 provider 测试路径上做同样约束；② provider 测试的目标地址改为**只能取自已登记的 provider 配置**，不接受临时任意 URL；③ 探测失败时统一返回，不回显内网端口的连通性细节 |
| **684** | `bin_dir`（`D:\Program Files\opencode2api\bin`）ACL 提权：manager 以高权限运行并可把可执行文件放进该目录，低权限进程借可写目录达成提权 | **main 树**（`core/manager` 的 `bin_dir` 管理） | ❌ **本次未修** | ① `bin_dir` 及其子目录的 ACL 收紧为**仅管理员可写**（去掉 Users / Everyone 的写权限）；② manager 侧对写入 `bin_dir` 的文件做**签名 / 哈希白名单**校验，拒绝未知二进制；③ 改用「受限服务账号 + 服务目录只读 + 二进制由安装包管理」的部署形态，manager 不再需要写权限 |
| **722** | `local-profiles` 本地配置文件的路径穿越 / LFI | **main 树**（`core/manager` 的 local-profiles 读写） | ❌ **本次未修** | ① 复用本 feature 树 `web/pathsafe.go` 的 `sanitizeDate` + `joinWithin` 模式：文件名白名单 + 拼接后校验最终路径仍在基准目录内 + 符号链接复核；② profile 名只允许 `[A-Za-z0-9_-]{1,64}`；③ 打开失败时区分「文件不存在」与「非法路径」，不要统一 500 |
| **560（main 侧残余）** | finding 560 的**管理面接管面**在 40080：即使前端包已不再下发默认密钥，只要 `opencode2api.exe` 仍以「面板口令 == `/v1` 门禁密钥」的方式启动，且面板口令仍是弱口令或沿用历史值，整条管理面接管链依然成立 | **main 树**（根 `main.go` + `core/manager`） | ⚠️ **feature 树侧已修**（根 `main.go` 的 `-api-key` 分离 / 随机口令 / `ConstantTimeCompare`、`src-tauri` 侧掩码与密钥文件），**但 40080 现网二进制尚未替换**（第三章 3.2 / 3.3） | ① 完成第三章 3.2 / 3.3：重新 `go build` 根 `main.go` 并替换 `D:\Program Files\opencode2api\bin\opencode2api.exe`，重启管理器；② 用 `cargo build --release` 替换管理器可执行文件（`src-tauri` 侧的掩码与密钥文件逻辑）；③ 替换后按第三章 3.4 ③ ④ 复验（旧口令 401、`api_key` 为掩码）；④ 轮换 40080 面板口令 |

---

## 七、回归测试清单

### 7.1 构建与静态检查

```powershell
cd D:\AI_Projects\opencode2api_guide\opencode2api_enhance
go build ./...            # 期望：BUILD_ALL_EXIT=0（本次写作时实测通过）
go vet ./web/            # 期望：VET_EXIT=0（本次写作时实测通过）
go test ./web/ -count=1  # 期望：ok github.com/6Kmfi6HP/opencode2api/web，--- FAIL 计数 = 0
cargo check              # src-tauri（Rust 侧：gateway.rs / commands.rs 改动）
npm run build            # 前端：tsc -b && vite build，产出 dist/
nginx -t                 # 套用 hardened 模板后必须通过（缺 include 片段会失败 = fail-closed）
```

### 7.2 `go test ./web/` 需要覆盖的关键回归点

| 用例 / 主题 | 期望 | 对应 finding |
|---|---|---|
| `TestHandleCreateKey` | 200，且 `base_url` 来自注入的 `public_base_url` | 548 / 547 |
| `TestHandleCreateKeyFailsClosedWithoutPublicBaseURL` | **503**，错误体含 `public_base_url`，且**不得**出现 `evil.example.com` | 548（fail-closed 锁死） |
| `TestHandleCreateKeyWithForwardedPrefix` | 转发前缀生效但不含 `..` | 548 |
| `TestCreateKeyReturnsNonLoopbackBaseURL` | 回环配置被替换为本机内网 IP | 548 |
| `TestCreateKeyDoesNotPersistInvalidIP` | 非法 IP **不落库** | 547 |
| `TestCreateKeyIsIdempotentPerIP` / `TestCreateKeyAllowsRepeatWhenLimitDisabled` | 幂等与限额开关语义不变 | 547 / 566 |
| `TestEndToEndKeyFlow` | 端到端领取 → 调用通过 | 全链路 |
| IP 策略越界（`window_minutes=9007199254740992`） | 400，且服务端仍 200（**不得 502**） | 565 |
| `allow_cidrs=["192.168.12.*"]` | 400 且**原白名单保持不变** | 566 |
| `allow_cidrs=[]` | 全拒绝（非白名单 IP 领不到 Key） | 566（fail-closed） |
| `PUT /api/admin/settings` 只提交一个字段 | 其余字段**保持原值** | 564 |
| `token_quota=9223372036854775807` | 400（越界） | 564 |
| `PUT /api/admin/settings {"token_quota":5000000}`（存量最大额度 10000000） | **400**「不能小于已发放 Key 的最大额度 10000000」（**不得**出现 MaxInt64 活锁） | 823 |
| `PUT /api/admin/password {"password":"1234"}` | **400** `password rejected: 密码长度不足 8 位` | 714 |
| `PUT /api/admin/password {"password":"aaaaaaaaaaaa"}` | **400** `password rejected: 密码不能是纯字母` | 714 |
| `POST /api/admin/login {"password":"0099"}` | **200**（用户指令 B：0099 必须可登录，**不得**因口令策略被拒） | 544 / 豁免项 |
| `date=a/../../logs/log-2026-10-21` | 400 `invalid date` | 543 |
| 重复 `model` 键 | 400 `invalid request body: duplicate model key` | 546 |
| 未授权 model | 403 `model not allowed for this key` | 546 |
| `X-Forwarded-Host: evil.example.com` | 下发 `base_url` **不得**含该域名 | 548 |
| 伪造 `X-Real-IP` | 不生效，落库为真实对端地址 | 547 |
| `upstream_base_url=http://169.254.169.254/...` | 400 且原值不变 | 541 |
| 并发 30 次登录 | 绝大多数 429，401 次数显著下降 | 544 |
| `-require-loopback` + `-listen 0.0.0.0` | 拒绝启动 | 601 |

### 7.3 密钥轮换后的线上回归项

| 回归项 | 期望 | 对应 finding |
|---|---|---|
| 旧管理员口令 `0099` 登录 | **401** | 560 / 544 |
| 旧 `123456` 登录 40080 `/login` | **401** | 560 |
| 新口令登录 | 200 且拿到会话 | 560 |
| `GET /api/admin/gateway/status` 的 `api_key` | **掩码**（形如 `********xxxx`），不得是完整明文 | 560 |
| `GET /assets/*.js`（40080 前端包） | **不含** `sk-unified-local` | 560 |
| 轮换后的 `proxy_secret` | 反代请求**正常**（`/api/keys` 200 且 `base_url` 主机为 `192.168.12.17`）；旧 `X-Proxy-Secret` 直连 18081 **被拒**（回落 RemoteAddr） | 547 |
| 119 条上游 Key 换新后 | `/v1/chat/completions` 仍 200（说明新 Key 生效）；旧 Key 调用被上游拒绝 | 560 |
| 6 个实例口令换新后 | `GET /api/admin/instances` 显示新口令；旧口令登录被拒 | 560 / 701 |
| `netstat` 的 18081 / 23099 | 只有 `127.0.0.1` 监听 | 601 |
| nginx `/admin.html`（不带 Basic Auth） | **401** | 601 |
| nginx `/api/admin/*`（带 Basic Auth 但来源不在白名单） | **403** | 601 |
| `GET /api/keys` 下发的 `base_url` | 主机 == `public_base_url` 配置值；`X-Forwarded-Host: evil.example.com` 时**不变** | 548 |
| `date=../x` 的四个管理面日志接口 | 全部 **400** | 543 |
| `/v1/models` 返回的模型数 | **≤ 4**（与 `/api/models` 公布的一致） | 546 |

---

## 附：本文档的核对方式

```powershell
cd D:\AI_Projects\opencode2api_guide\opencode2api_enhance
git rev-parse --abbrev-ref HEAD      # feature/web-self-service
git log --oneline -1                 # 7729af5
git status --short                   # 逐条核对「已修改 M」与「未跟踪 ??」文件
git diff -- <路径>                   # 逐文件核对本文档引用的 diff 摘要
# 核对本文档引用的每一处行号真实存在：
Select-String -Path web\pathsafe.go -Pattern "func sanitizeDate|func joinWithin"
Select-String -Path web\ip.go       -Pattern "func parseIPStrict|func \(l \*LoginLimiter\) TryAttempt"
Select-String -Path web\baseurl.go  -Pattern "func ValidatePublicBaseURL|func buildBaseURL|func sanitizeForwardedPrefix"
Select-String -Path web\modelguard.go -Pattern "func decodeTopLevelModel|func modelAllowedForKey"
Select-String -Path web\ssrfguard.go  -Pattern "func ValidateUpstreamBaseURL|func newUpstreamClient"
Select-String -Path web\listen.go     -Pattern "func resolveListenAddr|func checkListenPolicy"
Select-String -Path web\ippolicy.go   -Pattern "func clampDefaults|func clampOverride|MaxWindowMinutes"
Select-String -Path web\claimpolicy.go -Pattern "func ParseCIDRsStrict|func IPAllowed"
```
> 本文档写作时**逐条重跑了 `git diff`**，并用 `Select-String` 核对了全部引用行号真实存在；未使用任何 main 分支 worktree 的 diff。
> **凡尚未落盘或仍在途的项，本文一律写「待回填（意图 XXX）」而不是编造 diff**。经 2026-10-21 19:32 部署与本次（意图 910）复核，**已无任何 feature 树条目处于「待回填」状态**：原标「待回填（意图 774）」的**存量 Key 额度活锁**已在 2.12 节改写为**已修复 / 已部署 / 现网已闭环**，并以源码接点 + 生产二进制 strings 特征串 + 线上 `keys.json` 实际 `quota` 值 + 在线 PUT 响应四项证据交叉验证。
> **本文档的部署状态口径（用户指令 D）**：凡标「已修复 / 已闭环」的项，均附**可在生产二进制上复验的客观证据**（strings 特征串命中位置、实际 HTTP 状态码与响应体、`keys.json` 实际数值）；凡未附证据的一律降级为「源码已改，未部署」或「未修」。
> **归属如实**：main 树条目（560 管理面接管、586、701、684、722、40080 `/v1` 401 故障等）一律写「归属 main 树 / 本次未修 / 建议方案」，**不得**写成已修复，也**不得**引用 main worktree 的 diff。
