# 网页自助领 Key 服务（opencode2api 网页版）

> 完整部署指南（Linux VPS / Docker / nginx+HTTPS / CI 自动发布）见 **[部署指南](DEPLOY-WEB.md)**。

## 构建

```
go build -o bin/web-server.exe ./web
```

## 运行

```
bin/web-server.exe -data-dir ./web-data
```

首次运行会在控制台打印管理员初始密码，请立即登录后台修改。

## 使用

- 浏览器打开 http://<服务器IP>:23081 领取 Key。
- 右上角「管理员入口」输入密码进入后台。
- 后台需先填上游实例地址，并勾选要分发的模型。
- 用户把「访问地址 + Key」填到自己的客户端（OpenCode / Cherry Studio / Cursor 等）即可调用。

## 数据

数据存在 -data-dir 指定目录：config.json / keys.json / logs/

## 部署资产

| 资产 | 位置 | 说明 |
| --- | --- | --- |
| CI 构建发布 | `.github/workflows/build-web-server.yml` | 提交信息含「CI」自动测试 + 四平台交叉编译 + Release |
| Docker | `deploy/web-server/Dockerfile`、`compose.yml` | 多阶段构建，运行时镜像约 15MB |
| systemd | `deploy/web-server/systemd/web-server.service` | Linux 开机自启、崩溃重启 |
| nginx+HTTPS | `deploy/web-server/nginx/web-server.conf` | 公网反代、SSE 透传、管理后台限速/白名单 |
