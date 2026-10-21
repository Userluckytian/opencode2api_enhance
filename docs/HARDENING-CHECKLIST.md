# token 自助餐厅「现网加固 Checklist」

> 适用范围：`web/` 网页自助领 Key 服务（nginx:80 → 18081 / 23099）。
> 对应目标：526（落地修复）。对应已核实事实：fact 594（现场 nginx 无 Basic Auth / 无 IP 白名单）、
> fact 589 / 592（0.0.0.0 监听、23099 dev 实例暴露面）。
>
> **核心原则：管理面只经反代暴露，后端端口一律不对外。**
> web-server 现在默认只监听 `127.0.0.1`（`web/main.go` 的 `-listen`），
> 正式实例请保持默认；确需直连调试时才用 `-listen 0.0.0.0`，且必须配合防火墙。

---

## 0. 修复前后的对照（先看这个）

| 项 | 修复前（已核实） | 修复后（本次落地） |
| --- | --- | --- |
| 后端监听 | `web/main.go` 硬编码 `0.0.0.0:<port>`，同网段直连可达 | 默认 `127.0.0.1`；对外必须显式 `-listen 0.0.0.0` / `TOKEN_BUFFET_LISTEN` / config.json `listen` |
| 启动可见性 | 只打印 `监听 0.0.0.0:port` | 打印生效地址 + 来源层级 + 暴露面描述；非回环额外打「高危告警」 |
| dev 实例 | 与正式实例同样绑 0.0.0.0，23099 直连可领 Key / 进后台 | `-require-loopback` 启动自检：非回环直接拒绝启动 |
| nginx 管理面 | 现场无 `auth_basic`、无 `allow/deny` | 加固模板：admin.html 与 /api/admin/ 同时 Basic Auth + 来源网段白名单 |
| proxy_secret | 明文写在 nginx conf 与 config.json | nginx 侧改为**部署时渲染的 include 片段**（0600），不再明文入库 |
| autoindex | 未显式关闭 | server 块显式 `autoindex off` |

---

## 1. 收敛后端监听（18081 / 23099）

### 1.1 正式实例（18081）

- [ ] `web-data/config.json` 里**不要**出现 `"listen": "0.0.0.0"`；
      如存在，删除该字段（默认即回环）。
- [ ] 启动命令**不要**带 `-listen 0.0.0.0`。
- [ ] 系统环境变量里没有残留的 `TOKEN_BUFFET_LISTEN`（`set TOKEN_BUFFET_LISTEN` 确认为空）。
- [ ] 启动日志出现：`[监听面] 生效地址 127.0.0.1（来源：内置默认值（仅回环））— 127.0.0.1（仅本机可访问…）`
- [ ] 启动日志**没有**出现 `[高危告警] 当前监听地址 … 不是回环`。
- [ ] 防火墙：只放行 80/443，**不放行** 18081。

**验证（在本机执行）**

```bash
# 1) 监听地址只应是 127.0.0.1，不应有 0.0.0.0
netstat -ano | grep ':18081'
#   期望：TCP    127.0.0.1:18081   0.0.0.0:0   LISTENING
#   若出现 0.0.0.0:18081 → 未收敛，回到 1.1 前三条

# 2) 从**别的机器**直连应连接被拒
curl -s -o $null -w '%{http_code}\n' --max-time 3 http://192.168.12.17:18081/
#   期望：000（连接被拒/超时），不是 200
```

### 1.2 开发实例（23099）

- [ ] 改用仓库提供的启动器：`start-dev-web-server.bat`（或 `make web-dev`），
      它固定带 `-listen 127.0.0.1 -require-loopback`。
- [ ] `dev-data/config.json` 里不要有 `"listen": "0.0.0.0"`。
- [ ] 停止 dev 实例用 `make web-dev-stop`（**按端口**停），
      不要用 `taskkill /IM web-server.exe`——那会连正式实例一起杀掉
      （历史上已发生过一次 18081 随 dev 实例一起下线，见 docs/issue-log/2026-10-21.md）。

**验证**

```bash
netstat -ano | grep ':23099'
#   期望：127.0.0.1:23099  LISTENING
```

> ⚠️ **dev 实例绝不可上线**：它没有 nginx 前置、没有 Basic Auth，
> 领取口（`POST /api/keys`）对任何来源开放，且 `dev-data/config.json` 与正式实例
> 共用同一个上游凭据 `sk-unified-local`（fact 589）。

---

## 2. nginx 管理面收口（80 端口）

现场 `D:\nginx\conf\opencode2api-web.conf` 此前**只有**一条
`location /token-buffet/ { proxy_pass http://127.0.0.1:18081/; }`，
没有 `auth_basic`、没有 `/api/admin/` 的 `allow/deny`（fact 594 已核实），
等于管理后台对全内网只靠应用层一道口令。

### 2.1 套用加固模板

仓库提供两份模板：

| 文件 | 用途 |
| --- | --- |
| `deploy/web-server/nginx/web-server.conf` | 最小示例（保留原有结构，仅补 `autoindex off` 与「后端只监听回环」说明） |
| `deploy/web-server/nginx/web-server-hardened.conf` | **加固版（推荐现网套用）**：admin.html 与 /api/admin/ 同时 Basic Auth + IP 白名单，proxy_secret 不落明文 |

步骤（Linux 示例；Windows 同理，见 2.4）：

```bash
# 1) Basic Auth 口令文件（与 web-server 管理员口令分开设置）
sudo apt install apache2-utils
sudo htpasswd -c /etc/nginx/.htpasswd-web admin
sudo chmod 640 /etc/nginx/.htpasswd-web
sudo chown root:www-data /etc/nginx/.htpasswd-web

# 2) 渲染反代共享密钥片段（**不要**把密钥写进 nginx conf）
$env:TOKEN_BUFFET_PROXY_SECRET='<web-data/config.json 里的 proxy_secret，或已轮换的新值>'
sudo bash deploy/web-server/nginx/render-secret.sh /etc/nginx/snippets/token-buffet-proxy-secret.conf

# 3) 放置 + 校验 + 重载
sudo cp deploy/web-server/nginx/web-server-hardened.conf /etc/nginx/sites-available/opencode2api-web.conf
sudo ln -s /etc/nginx/sites-available/opencode2api-web.conf /etc/nginx/sites-enabled/
sudo nginx -t && sudo systemctl reload nginx
```

> **为什么用 include 片段而不是环境变量**：nginx 的 `env` 指令只把变量继承给
> worker 进程，**不能**在配置里当 `$变量` 用——本机 nginx 1.30.4 实测
> `nginx -t` 报 `unknown "testvar" variable`。改成部署时生成 0600 的片段文件，
> 密钥同样不入库、不进版本控制，且可用文件权限限制读取。

### 2.2 改 `allow/deny` 网段

模板里默认是 `allow 192.168.0.0/16;`（整个内网）——**必须改成运维网段**，
例如 `allow 192.168.12.0/24;` 或干脆只留 `allow 127.0.0.1;`。
`allow/deny` 与 `auth_basic` 是两道独立的门：

- `auth_basic` 管「你是谁」：口令泄露即失守；
- `allow/deny` 管「你从哪来」：口令没泄露也挡住内网横向扫描。

### 2.3 保持 location 是纯 proxy_pass

模板里的 `location /token-buffet/` **必须**保持纯 `proxy_pass`：

- 不要用 `alias` / `root` 指向 `web-data` / `dev-data`；
  否则 `config.json`、`keys.json`（内含上游密钥与**全部 Key 明文**）会被静态读走。
- 本条已被 fact 589 证伪过一次（22 条穿越路径全 404），但要作为**配置基线**固化，
  防止后续有人为了"方便看日志"改成 alias。

### 2.4 Windows 现场（当前部署形态）

```powershell
# 1) 生成 htpasswd（Windows nginx 用 apc/apr1 格式，htpasswd 工具可生成）
#    或用： & 'D:\nginx\nginx.exe' 无法生成，需装 apache2-utils / 用 wsl htpasswd
# 2) 渲染密钥片段
$env:TOKEN_BUFFET_PROXY_SECRET = '<proxy_secret>'
& 'D:\AI_Projects\opencode2api_guide\opencode2api_enhance\deploy\web-server\nginx\render-secret.ps1' `
    -Out 'D:\nginx\conf\snippets\token-buffet-proxy-secret.conf'
# 3) 修改 D:\nginx\conf\opencode2api-web.conf：
#    - 拆出 location = /token-buffet/admin.html 与 location /token-buffet/api/admin/ 两段
#    - 各自加 auth_basic + allow/deny
#    - 主 location 里把明文 proxy_set_header X-Proxy-Secret "10baaee9..." ;
#      换成 include D:/nginx/conf/snippets/token-buffet-proxy-secret.conf;
# 4) 校验 + 重载
& 'D:\nginx\nginx.exe' -t
& 'D:\nginx\nginx.exe' -s reload
```

---

## 3. 密钥轮换清单

| 密钥 | 位置 | 风险 | 处理 |
| --- | --- | --- | --- |
| `proxy_secret` = `10baaee90658e38fd393d6540b507650` | 现场 nginx conf **明文** + `D:\nginx\opencode2api-web\web-data\config.json` | 同机任意低权限进程可读 → 可伪造 `X-Proxy-Secret` + `X-Real-IP`，**绕过全部 IP 策略/限领/白名单**（fact 594、fact 592 已在 23099 实测落盘伪造 IP 10.0.0.55） | ① 用 `web-data/config.json` 里的新值重跑 `render-secret.sh` / `render-secret.ps1` 覆盖片段；② nginx 侧只保留 `include`；③ 旧值作废 |
| `upstream_api_key` = `sk-unified-local` | `web-data/config.json` **明文**（正式 + dev 两份都有） | 已在公开前端包泄露（finding 560）；且**兼作 40080 管理面板口令** | ① 改为环境变量注入 `TOKEN_BUFFET_UPSTREAM_API_KEY`（`web/config.go` 已支持，优先级高于 config.json）；② 从 `config.json` 里删除该字段；③ 轮换 40080 侧口令；④ 同步清理 dev 实例 |
| admin 口令 `0099` | 任务上下文给定 | 管理面唯一一道应用层防线 | 套用 nginx Basic Auth + IP 白名单后形成三道；仍建议定期轮换并与 nginx 口令不同 |

**轮换 proxy_secret 的影响**：`web/config.json` 改值 → 重启 web-server →
重新渲染 nginx 片段 → `nginx -t && reload`。顺序反了会导致
web-server 认不出新的 `X-Proxy-Secret`，此时**所有请求的客户端 IP 都会退化成
RemoteAddr（127.0.0.1）**，限领/白名单看起来「全部放行」。

---

## 4. 上游密钥迁到环境变量（代码已支持）

`web/config.go` 的 `resolveUpstreamAPIKey` 优先级：
`TOKEN_BUFFET_UPSTREAM_API_KEY` 环境变量 > `config.json`。
若 config.json 里仍是历史硬编码 `sk-unified-local`，启动时会被**自动轮换成随机值**并告警
（fail-closed：随机值必然被上游 401，不会静默放行）。

- [ ] 用环境变量注入真实上游密钥（Windows：`setx TOKEN_BUFFET_UPSTREAM_API_KEY "sk-..."`；
      Linux：systemd `Environment=` 或 `/etc/default/`）。
- [ ] 从 `config.json` 删掉 `upstream_api_key` 字段。
- [ ] 重启后确认日志出现：`[安全] 上游密钥取自环境变量 TOKEN_BUFFET_UPSTREAM_API_KEY（末 4 位 xxxx）`。
- [ ] 确认 `GET /api/admin/settings` 的响应里只有 `upstream_api_key_masked`（无明文）——该行为已实现。

---

## 5. 收尾验证清单（全部通过才算完成）

```bash
# ① nginx 配置语法
nginx -t                                  # 期望：syntax is ok / test is successful

# ② 未带 Basic Auth 访问管理后台 → 401
curl -s -o $null -w '%{http_code}\n' http://192.168.12.17/token-buffet/admin.html
#   期望：401

# ③ 带了 Basic Auth 但来源 IP 不在白名单 → 403
curl -s -o $null -w '%{http_code}\n' -u admin:pass http://192.168.12.17/token-buffet/api/admin/keys
#   期望：403（来源不在 allow 网段时）

# ④ 正确 Basic Auth + 允许网段 → 200（说明两道门都通）
curl -s -o $null -w '%{http_code}\n' -u admin:pass http://192.168.12.17/token-buffet/api/admin/overview
#   期望：200

# ⑤ 直连后端端口 → 连接被拒
curl -s -o $null -w '%{http_code}\n' --max-time 3 http://192.168.12.17:18081/
curl -s -o $null -w '%{http_code}\n' --max-time 3 http://192.168.12.17:23099/
#   期望：000（连接被拒）

# ⑥ 领 Key 页仍可用（对外职能没被误伤）
curl -s -o $null -w '%{http_code}\n' http://192.168.12.17/token-buffet/
#   期望：200
```

---

## 6. 不要做的事（回归风险）

- ❌ 不要在 `location /token-buffet/` 里用 `alias`/`root` 指向数据目录。
- ❌ 不要用 `proxy_add_x_forwarded_for`（追加语义 → 客户端可自带 XFF 伪造 IP）。
- ❌ 不要用 `taskkill /IM web-server.exe` 停实例（会连正式实例一起杀）。
- ❌ 不要把 `proxy_secret` / `upstream_api_key` 写进 nginx conf 或提交进版本库。
- ❌ 不要为了「本地方便」把正式实例改成 `-listen 0.0.0.0`（配置已经能改，但要留下明确决策记录）。