# Linux 开发与构建指南

> 本文档记录 opencode2api_enhance 在 Linux 平台上的开发、构建、部署与排障信息，
> 供其他开发者参考。Windows 便携包构建见 `scripts/prepare-portable.ps1`；通用架构见 README.md。

## 1. 部署形态

同一套 Go core 二进制（`opencode2api`）同时支撑所有终端形态：

| 形态 | 运行方式 | 说明 |
|---|---|---|
| 桌面 GUI | `.deb` / AppImage 安装后运行 `opencode2api` | Tauri 2 (Rust) 壳 + 内嵌 Go core + sing-box |
| Headless（壳模式） | `opencode2api --headless [-port 40000] [-listen 127.0.0.1]` | 无桌面/SSH 场景；释放内嵌组件、拉起 core、清理退出 |
| Headless（裸 core） | `./opencode2api -port 40000 -password "" -listen 127.0.0.1` | 直接运行 Go core，不经过 Rust 壳（文档化历史方式，相当于 serve 模式） |
| Docker | `docker compose up` | core 直跑，镜像内已含前端 dist |

数据目录默认 `~/.config/opencode2api-manager/`（可用 `OPCODE2API_DATA_DIR` 环境变量覆盖），
其中 `config.json` 同时由 Go 侧 `manager.Config` 与 `AppConfig` 读写（相互保留未知字段，见 docs）。

## 2. Linux 侧代码机制（src-tauri/src/）

### 2.1 平台二进制解析 `resolve_platform_bin`（instance.rs:28）

```rust
pub(crate) fn resolve_platform_bin(bin_dir: &Path, name: &str) -> PathBuf
```

Windows 返回 `<name>.exe`，Linux/macOS 返回 `<name>`（无扩展名），带 `exists()` 检查。

**历史教训**：早期 `spawn_core_manager` 硬编码 `opencode2api.exe`，Linux 上 `bin/` 释放的是
无扩展名二进制，导致「启动 core 管理器失败: bin/opencode2api.exe 不存在（未释放内嵌组件）」。
现在 `spawn_core_manager`（lib.rs）与 `gateway.rs:start_child` 都改用此函数。

### 2.2 可写目录回退 `writable_dir`（commands.rs:manager_paths 附近）

```rust
fn writable_dir(dir: &Path) -> bool  // create_dir_all + 写删 .write_probe 探针
```

- 二进制目录默认取 `current_exe()/bin`（deb 安装到 `/usr/bin` 时普通用户不可写）。
- 探测失败回退到 `config_dir/bin`（通常 `~/.config/opencode2api-manager/bin`）。
- 子程序（core + sing-box）在启动时按需释放到该目录。

### 2.3 前端 dist 复制（lib.rs `.setup()` + `copy_tree`）

资源目录（deb 为 `/usr/lib/opencode2api/bin/dist`）在回退目录缺 `dist/index.html` 时，
递归复制到 `binary_dir/dist`，保证浏览器访问 core WebUI 可用。

### 2.4 `--headless` 壳模式（lib.rs run()）

```
opencode2api --headless [-port 端口] [-listen 地址] [其它 core 参数...]
```

- 解析：`std::env::args().skip(1)` 中存在 `--headless` 即进入无头模式；其余参数剥离后透传给 core。
- 透传：`spawn_core_manager` 追加 extra_args（Go flag 重复参数后者覆盖 → 用户 `-port/-listen` 自动生效）。
- 流程：释放内嵌组件 → 拉 core（管理器模式）→ 打印 `[headless] 管理地址: http://127.0.0.1:{port}/` → 阻塞等待 → Ctrl+C 后清理网关与实例进程。
- 验证：无窗口（MainWindowHandle=0）、core 监听透传端口、退出后端口全部释放。
- **注意**：Windows PowerShell `Start-Process -ArgumentList` 数组 / dotnet `ArgumentList.Add` 传参方式
  曾导致 `--headless` 未识别（行为异常），单字符串传参与真实 cmd/双击场景正常。验证请用单字符串传参。

### 2.5 内嵌子程序选择（embed.rs + build.rs）

- Windows：内嵌 `bin/opencode2api.exe` + `bin/sing-box.exe`（`embed_windows_bin`）。
- Linux/macOS：若 `bin/opencode2api` + `bin/sing-box`（无扩展名）存在则内嵌无扩展名（`embed_unix_bin`），缺失回退 `.exe`（保证本地可编译）。
- 释放时按 `platform_name` 决定目标文件名；Unix 下释放后补 `chmod 755` 可执行位。
- 内容级校验：首/中/尾三段 FNV-1a 哈希比较，避免旧版残留。

## 3. 构建流程（Linux）

### 3.1 前置条件

- Go ≥ 1.21（core）
- Node.js ≥ 18 + npm（前端）
- Rust + Cargo（Tauri 壳；构建 .deb 需要 Linux 环境，Windows 上可用 WSL）
- Linux 图形依赖（deb 构建需要 webkit2gtk4.1 / gtk3 / librsvg / appindicator 等 devel 包）

### 3.2 交叉编译 Go core（可在 Windows 上执行）

```powershell
$env:GOOS="linux"; $env:GOARCH="amd64"; $env:CGO_ENABLED="0"
go build -trimpath -ldflags "-s -w -X main.version=v1.5.1" -o bin\opencode2api .
```

产物约 9MB（无扩展名）。sing-box 用 `./scripts/fetch-singbox.sh linux-amd64`（默认 v1.13.16，
`SINGBOX_VERSION` 可覆盖；也可 Windows 下载 zip 解压，约 45.5MB）。

### 3.3 WSL 构建 Tauri deb

```bash
# Fedora44 示例；构建目录含 bin/（Linux core + sing-box + dist）与 src-tauri/
export PATH=$HOME/.cargo/bin:$PATH
source ~/.cargo/env
npx tauri build --bundles deb
```

- 脚本方式调用避免引号/$ 转义：`wsl -d FedoraLinux44 -- bash /mnt/c/<path>/script.sh`。
- 产物拷回 `linux-out/opencode2api_<version>_amd64.deb`（约 27MB；内嵌主二进制约 75MB）。

### 3.4 deb 安装与启动（Debian/Ubuntu）

```bash
sudo dpkg -i opencode2api_1.5.1_amd64.deb
opencode2api                  # 桌面 GUI
opencode2api --headless       # 无桌面 / SSH（浏览器访问 http://127.0.0.1:40000/）
```

## 4. 验证与测试纪律

- **go test**：改动后 `go test -count=1 ./...` 全绿才提交。本机 core/manager 有 4 个既有环境失败
  （TestPortIsolationE2E / TestHTTPRequestRaw / TestTestInstanceNotRunning / TestVendorConfigInjected，
  硬编码 `C:\Users\ASUS` 路径 mkdir 拒绝 + flaky timeout），非本次引入，可忽略但需确认无新增。
- **deb 内容验证**（Fedora 无 dpkg-deb 时）：`ar x <deb>` + `tar xf data.tar.*` 解包，
  grep 二进制关键标记（如 `[headless]` / `MergeConfigJSON` / `gateway_key` / `write_probe`）确认修复已进入。
- **环境隔离**：真实服务启动前检查端口占用（正式 40000 / dev 44100 / portable 48200 槽位）；
  测试用 `OPCODE2API_DATA_DIR` + `OPCODE2API_MANAGER_PORT` + `OPCODE2API_INSTANCE_BASE_PORT` 三件套隔离；
  禁止 kill 非自己启动的进程。

## 5. 常见问题

| 现象 | 原因 | 处理 |
|---|---|---|
| `opencode2api` 报「启动 core 管理器失败: bin/opencode2api.exe 不存在」 | 旧版硬编码 .exe | 更新到含 `resolve_platform_bin` 的版本（v1.5.1 之后） |
| panic `Failed to initialize gtk backend!` | 无图形会话（SSH/headless） | 用 `opencode2api --headless` 或直接跑 `bin/opencode2api -port 40000 -listen 127.0.0.1` |
| deb 安装后普通用户无法运行（写 /usr/bin/bin 失败） | binary_dir 不可写 | `writable_dir` 探针自动回退 `~/.config/opencode2api-manager/bin` |
| 浏览器访问 core WebUI 404 | 回退目录缺 dist | `.setup()` 自动从资源目录复制 dist |
| PowerShell 传参启动带 `--headless` 无效 | PS ArgumentList 数组传参坑 | 用单字符串传参或直接 cmd 运行 |