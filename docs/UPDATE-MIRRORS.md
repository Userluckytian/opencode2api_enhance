# 更新下载镜像加速

桌面版的应用内更新（Tauri updater）默认直连 GitHub。国内网络下 `latest.json`
能拿到、几十 MB 的安装包却可能很慢。这里加一层**本机清单中转**：只把安装包的
下载地址换成实测最快的公益加速源，验签与安装流程完全不动。

## 链路

```
桌面壳 (Tauri updater)
  │  ① GET 127.0.0.1:40099/api/admin/update/manifest?target=windows&arch=x86_64
  ▼
Go 管理端（本文件负责的模块）
  │  ② 并发探测 22 个公益源 + GitHub 直连（Range 0-0，2.5s 超时）
  │  ③ 拉 latest.json（用排序后的第一个可用源），解析 platforms
  │  ④ 对目标平台的 url 再探测一轮，改写成最快的镜像地址
  ▼
桌面壳
  │  ⑤ 从改写后的 URL 下载安装包
  │  ⑥ 用内置公钥验签（minisign）→ 安装
  ▼
完成
```

任何一步失败（端口被占、全部源不可达、清单不是 JSON……）都会：

- 端口被占 → 本机接口不存在，壳自动用第二个 endpoint（GitHub 直连）；
- 探测/改写失败 → 返回 GitHub 原地址或 502，壳同样回退直连。

## 安全边界

| 项 | 做法 |
|----|------|
| 清单来源 | GitHub（或镜像转发的同一份字节），**不改内容** |
| `signature` | 原样透传；安装包由 Tauri 用 `tauri.conf.json` 里的公钥验签 |
| 镜像能做什么 | 只能影响下载字节；内容对不上签名会被拒绝 |
| 谁能访问中转接口 | 只监听 `127.0.0.1`，非回环来源返回 403 |
| 传输 | 本机 endpoint 用 http（仅回环），需要 `dangerousInsecureTransportProtocol`；真正下载走 https 镜像 |

## 配置

`src-tauri/tauri.conf.json`：

```json
"updater": {
  "dangerousInsecureTransportProtocol": true,
  "endpoints": [
    "http://127.0.0.1:40099/api/admin/update/manifest?target={{target}}&arch={{arch}}",
    "https://github.com/Userluckytian/opencode2api_enhance/releases/download/updater-latest/latest.json"
  ]
}
```

- **第一个**是本机中转（可加速）；**第二个**是 GitHub 直连（兜底，顺序不能反）。
- `{{target}}` / `{{arch}}` 由 Tauri 替换成 `windows` / `x86_64` 之类，管理端据此只探测当前平台的安装包。

端口固定 `40099`（Tauri 的 endpoint 编译期写死，必须可预期）。调试多实例时可用
环境变量覆盖管理端监听端口：

```bash
OPCODE2API_UPDATE_MIRROR_PORT=40199
```

> 注意：壳里的 endpoint 不会跟着变。多实例场景下只有一个实例能占到 40099，
> 其余实例的更新检查走 GitHub 直连（功能正常，只是没加速）。

## 关闭/排障

- **想临时关掉加速**：确保 40099 被别的程序占用，或直接改 `tauri.conf.json`
  删掉第一个 endpoint 后重新打包。
- **日志**：管理端启动时会打印 `update manifest mirror listening addr=127.0.0.1:40099`；
  端口被占则是 `update manifest mirror disabled (port busy)`。
- **验证接口**（本机）：

```bash
curl "http://127.0.0.1:40099/api/admin/update/manifest?target=windows&arch=x86_64"
```

返回的 JSON 里 `platforms.windows-x86_64.url` 应该是某个镜像前缀 + 官方路径；
若仍是 `https://github.com/...`，说明这一轮没有可用镜像（不影响更新）。

## 镜像表

内置 22 个公益加速源（ghproxy.net / ghfast.top / gh-proxy.org / wget.la 等），
来源是 Denia 项目 `crates/server/src/update_mirrors.rs` 整理并实测过的列表
（2026-10-15）。失效的源会在探测阶段被自动排除，不需要改代码；长期大面积失效时
再更新 `core/manager/updatemirror.go` 里的 `updateMirrorTable`。

## 相关代码

- `core/manager/updatemirror.go`：探测、排序、清单改写、本机 HTTP 接口
- `core/manager/updatemirror_test.go`：改写/兜底/回环校验单测 + 真实网络冒烟（默认跳过）
- `src-tauri/tauri.conf.json`：endpoints 与 `dangerousInsecureTransportProtocol`
- `src/lib/nativeUpdate.ts`：前端只负责展示进度，不感知镜像
