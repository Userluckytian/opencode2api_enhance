# opencode2api-win（免费层引擎）

本目录存放**第三方组件**的许可证与来源声明：40080 端口上运行的 OpenCode
免费层引擎 `opencode2api-win`，来自 https://github.com/wbi35772/opencode2api-win
（上游提交 9964407，许可证 **GPL-3.0**）。

本项目与引擎的关系是**独立进程 + HTTP 调用**（mere aggregation）：管理器
（40000）只负责生成配置、启停、健康检查与统计；不链接、不修改引擎源码，
因此管理面板自身的许可证不受 GPL 传染，代价是必须随包分发本目录。

| 文件 | 说明 |
|------|------|
| `LICENSE` | 引擎上游 GPL-3.0 原文（逐字节复制） |
| `SOURCE.txt` | 仓库地址、上游 commit、构建命令、聚合关系与源码获取方式 |
| `README.md` | 本说明 |

## 如何更新引擎

1. 取上游新提交（或新构建的 `opencode2api-win.exe`），**覆盖**
   仓库内的 `bin/opencode2api-win.exe`（Linux/macOS 为 `bin/opencode2api-win`）；
   本地没有该文件时可跑 `scripts/fetch-free-tier-engine.ps1` 自动克隆并构建。
2. 把 `SOURCE.txt` 第 3 节的 commit（含短提交）改为新提交的 sha，并同步
   `scripts/fetch-free-tier-engine.ps1` 的 `-Commit` 默认值与
   `.github/workflows/build-release.yml` 的 `ENGINE_COMMIT`。
3. 重新 `cargo build`（`build.rs` 会把新引擎内嵌进主程序，运行时释放到
   exe 同级 `bin/`）；发行包必须继续附带本目录。

## 引擎的运行目录

`%APPDATA%\opencode2api-manager\runtime\free-tier\`
（`config.json` 由管理面板生成，引擎在同目录写状态文件与 stdout/stderr 日志）

详见 `docs/FREE-TIER-ENGINE.md`。
