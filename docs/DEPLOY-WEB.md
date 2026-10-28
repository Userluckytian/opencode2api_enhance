# 网页自助领 Key 服务部署指南

本文面向把 `web/` 网页版部署到公网 / 内网服务器的场景。网页版是一个**单二进制、零外部依赖**（纯 Go 标准库）的 HTTP 服务：

- 默认端口 `23081`（`web-data/config.json` 的 `port` 字段可改）
- **默认只监听 `127.0.0.1`**：管理面与领取口只经 nginx 反代对外暴露（见「一点五、监听面」）
- 数据目录默认 `./web-data`（`-data-dir` flag 可改），含 `config.json` / `keys.json` / `logs/`
- 前端页面已通过 `go:embed` 内嵌进二进制，**部署只需一个文件**

## 一、快速开始（本机/内网）

```bash
# 构建（Windows 用 go build -o web-server.exe ./web）
go build -o web-server ./web

# 运行
./web-server -data-dir ./web-data
```

首次启动会在控制台打印管理员初始密码，形如：

```
[首次启动] 管理员初始密码：admin-xxxxxxxx（请尽快登录后台修改）
```

浏览器打开 `http://<服务器IP>:23081`：

1. 右上角「进入管理员入口」→ 输入初始密码登录后台；
2. 「上游管理」新增上游（至少一个），填 ID、Base URL（如 `http://127.0.0.1:18080/v1`）与上游密钥；
3. 「模型管理」按上游分组勾选允许分发的模型并保存；
4. 回到领 Key 页，选模型 → 获取 Key → 把「访问地址 + Key」发给用户。

## 一点六、多上游与 model 前缀路由

后台支持配置多个上游，请求按 **`model` 的第一段** 路由：

| 请求的 model | 转发到 | 上游实际收到的 model |
| --- | --- | --- |
| `backup/gpt-4o` | ID 为 `backup` 的上游 | `gpt-4o`（前缀被剥离） |
| `gpt-4o` | 默认上游 | `gpt-4o`（原样） |
| `or/minimax/minimax-m3.1-flash:free` | 默认上游 | 原样（`or` 不是上游 ID，不剥前缀） |

关键规则：

- **只有第一段精确等于某个「已启用」上游的 ID 时才剥离前缀**。模型名本身含斜杠很常见
  （`or/minimax/...`、`vibex/openai/...`），盲切第一个 `/` 会把它们误判成上游前缀。
- 上游被**停用**后不再参与路由：`backup/xxx` 会整体当作普通模型名走默认上游，
  不会出现「停用备用上游导致请求全挂」。
- 默认上游在「上游管理」里标记为「默认」；停用默认上游时自动改指第一个启用项。
- 不允许停用或删除**最后一个启用中的上游**，也不允许直接删除默认上游（先把默认改到别处）。

### config.json 结构

```json
{
  "upstreams": [
    {
      "id": "main",
      "name": "主上游",
      "base_url": "http://127.0.0.1:18080/v1",
      "api_key": "sk-xxx",
      "enabled": true
    },
    {
      "id": "backup",
      "name": "备用上游",
      "base_url": "http://10.0.0.2:18080/v1",
      "api_key": "sk-yyy",
      "enabled": true
    }
  ],
  "default_upstream_id": "main"
}
```

**升级兼容**：老配置只有 `upstream_base_url` / `upstream_api_key` 时无需手工改动——
首次读取会自动视为一条 ID 为 `default` 的上游；第一次在后台改动上游列表时会把它
落成实体条目，不会丢失。注意一旦配置了 `upstreams` 列表，环境变量
`TOKEN_BUFFET_UPSTREAM_API_KEY` 不再生效，请改用「上游管理」逐条设置密钥。

### 安全约束（不变）

每个上游的 `base_url` 在写入前都要过 SSRF 校验（scheme 仅 http/https、禁止 userinfo、
拒绝内网/云元数据地址，除非该主机在部署方下发的 `upstream_allowed_hosts` 白名单里）。
白名单仍只由 `config.json` / 环境变量下发，**管理 API 不可写**。

## 一点五、监听面（重要：生产不得暴露后端端口）

web-server 的监听地址**默认只绑定回环 `127.0.0.1`**。管理后台（`/admin.html`、
`/api/admin/*`）与领取口（`/api/keys`）都跑在同一个进程里，一旦绑到 `0.0.0.0`，
同网段任何主机都能直连——历史上就出现过 dev 实例（23099）无任何前置、
可被任意内网来源领 Key 并拿到独立于正式站的 Key 库。

生效监听地址的优先级：

| 优先级 | 来源 | 说明 |
| --- | --- | --- |
| 1 | 命令行 `-listen <addr>` | 最高优先级 |
| 2 | 环境变量 `TOKEN_BUFFET_LISTEN` | 适合容器/systemd 注入 |
| 3 | `web-data/config.json` 的 `listen` 字段 | 静态配置 |
| 4 | 内置默认 `127.0.0.1` | 兜底，永远安全 |

```bash
# 默认（推荐）：只监听回环
./web-server -data-dir ./web-data

# 确需对外直连（不推荐，必须配防火墙）：显式声明
./web-server -data-dir ./web-data -listen 0.0.0.0

# 开发/测试实例：非回环直接拒绝启动（防误上线）
./web-server -data-dir ./dev-data -listen 127.0.0.1 -require-loopback
```

启动日志会打印生效监听面与来源，例如：

```
[监听面] 生效地址 127.0.0.1（来源：内置默认值（仅回环））— 127.0.0.1（仅本机可访问，管理面不会直接暴露到网络）
```

若生效地址不是回环，会额外打**高危告警**（不阻断启动，因为此时唯一边界是
nginx/防火墙）；开发实例请加 `-require-loopback`，让进程直接拒绝启动。

> **生产铁律**：正式实例只监听 `127.0.0.1`，公网流量一律走 nginx/HTTPS；
> **不得**直接暴露 18081 / 23099；管理面只经反代暴露，并在反代上加
> Basic Auth + 来源网段白名单（模板见 `deploy/web-server/nginx/web-server-hardened.conf`）。
## 二、部署到 Linux 服务器（VPS）

网页版纯标准库，可交叉编译，无需在服务器装 Go：

```bash
# 在任意机器上交叉编译
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o web-server-linux-amd64 ./web
# ARM 服务器（如 Oracle 免费机）用 GOARCH=arm64
```

上传二进制到服务器后，用 systemd 托管（开机自启 + 崩溃自动拉起）：

```bash
sudo mkdir -p /opt/opencode2api-web
sudo cp web-server-linux-amd64 /opt/opencode2api-web/web-server
sudo chmod +x /opt/opencode2api-web/web-server

sudo useradd -r -s /usr/sbin/nologin webuser   # 以非 root 运行
sudo chown -R webuser:webuser /opt/opencode2api-web

sudo cp deploy/web-server/systemd/web-server.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now web-server
```

查看初始密码与日志：

```bash
sudo journalctl -u web-server -n 20 | grep 首次启动
sudo journalctl -u web-server -f
```

## 三、Docker 部署

```bash
docker compose -f deploy/web-server/compose.yml up -d
```

数据持久化在 `deploy/web-server/web-data/`，取初始密码：

```bash
docker compose -f deploy/web-server/compose.yml logs web-server | grep 首次启动
```

也可以直接用 GitHub Actions 构建好的镜像/二进制（见下文「CI 自动发布」）。

## 四、公网部署：nginx + HTTPS（强烈建议）

网页版自身是纯 HTTP，公网暴露必须套 HTTPS 反代。完整配置见
`deploy/web-server/nginx/web-server.conf`，要点：

1. 只让 web-server 监听回环：保持默认的 `127.0.0.1` 即可（**不要**加 `-listen 0.0.0.0`），公网只放行 443；
2. certbot 签发证书后按模板配置 `proxy_pass http://127.0.0.1:23081`，**`proxy_buffering off`** 保证 `/v1` 流式响应（SSE）即时到达客户端；
3. 模板里对管理后台 `/admin.html` 额外加了 nginx Basic Auth、对 `/api/admin/` 限制来源 IP、对登录接口按 IP 限速——请按需启用。
   需更严格的现网加固（管理面同时加来源网段白名单、proxy_secret 不落明文）请直接用
   `deploy/web-server/nginx/web-server-hardened.conf`，密钥用 `render-secret.sh` / `render-secret.ps1`
   渲染成 0600 的 include 片段（nginx 的 `env` 变量无法在配置里当 `$变量` 引用，实测会报
   `unknown "xxx" variable`）。

> 现网逐项收口清单见 **[HARDENING-CHECKLIST.md](HARDENING-CHECKLIST.md)**。

部署顺序建议：

```bash
sudo apt install nginx certbot python3-certbot-nginx
sudo certbot --nginx -d example.com                      # 先拿到证书
sudo cp deploy/web-server/nginx/web-server.conf /etc/nginx/sites-available/opencode2api-web.conf
# 编辑模板：替换 example.com、启用 adminlogin 限速 zone、按需开 Basic Auth / IP 白名单
sudo ln -s /etc/nginx/sites-available/opencode2api-web.conf /etc/nginx/sites-enabled/
sudo nginx -t && sudo systemctl reload nginx
```

## 五、CI 自动发布（GitHub Actions）

推送提交信息含「CI」到 `main` 或 `feature/web-self-service` 分支时，
`.github/workflows/build-web-server.yml` 会自动：

- 跑全部 `go test ./web/...`（含 `-race`）；
- 交叉编译 **linux/amd64、linux/arm64、windows/amd64、darwin/amd64** 四个产物；
- 发布 GitHub Release（tag 形如 `web-v<run_number>`），附件可直接下载部署。

## 六、数据备份与安全清单

- **备份**：定期备份 `web-data/`（config.json + keys.json + logs/），恢复时整体拷贝回原路径即可；
- **改密码**：首次登录后立即在后台「参数设置」修改管理员密码；忘记密码可删除 `web-data/config.json` 中的 `admin_password_hash` 字段后重启，重新生成初始密码（会丢失其它设置，先备份）；
- **上游地址**：在「上游管理」逐条填写你自己的 opencode2api / 兼容网关地址，Key 校验在网页版完成，**不要**把上游的 Key 泄露给用户；多个上游按 `model` 前缀路由（见「一点六」）；
- **防火墙**：公网只放行 80/443；23081 只允许本机（反代）访问。
- **监听面**：web-server 默认只监听 `127.0.0.1`；**不要**用 `-listen 0.0.0.0` 把它开到外网，
  也不要对外暴露 18081 / 23099（dev 实例尤其危险：无 nginx 前置、领取口对任何来源开放）。
- **反代密钥**：`proxy_secret` 不要写进 nginx 配置文件明文，用 `render-secret.sh` 渲染成 0600 的
  include 片段；轮换 `proxy_secret` 时先改 `web-data/config.json` 并重启 web-server，
  再重渲染片段并 `nginx -t && reload`（顺序反了会导致客户端 IP 全部退化成 127.0.0.1）。

## 七、常见问题

| 现象 | 处理 |
| --- | --- |
| 领 Key 页模型下拉框为空 | 后台「模型管理」还没勾选并保存白名单 |
| 调用报 `upstream unreachable` | 「上游管理」里的 Base URL 填错或该上游没启动 |
| 流式输出卡顿/不实时 | nginx 反代漏了 `proxy_buffering off` |
| 忘记管理员密码 | 见「安全清单」重置方法 |
| 想改端口 | 改 `web-data/config.json` 的 `port` 后重启服务 |
