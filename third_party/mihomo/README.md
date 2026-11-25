# third_party/mihomo

节点池（WARP 注册源）使用的内核二进制。**发布包内置**：CI 与本地脚本下载后放到
`bin/mihomo[.exe]`，由桌面壳 `include_bytes!` 内嵌，首次启动释放到安装目录 `bin/`。

## 许可

- mihomo（原 Clash.Meta）为 **GPL-3.0**。
- 随安装包分发时同时提供：
  - 本目录的 `LICENSE`（上游许可证原文）
  - 本目录的 `SOURCE.txt`（上游仓库、固定版本、SHA256、获取方式）
- `src-tauri/tauri.conf.json` 的 `bundle.resources` 已包含本目录，安装包内即带上述文件。
- 发行说明中注明「本项目以独立进程方式使用 mihomo（mere aggregation）」。

## 目录约定

```
third_party/mihomo/
  LICENSE        # GPL-3.0 原文（随包分发）
  SOURCE.txt     # 上游仓库 + 版本 + SHA256 + 获取方式（随包分发）
  README.md      # 本文件
```

二进制不放在本目录（体积大、不入库），统一放 `bin/`：

```
bin/mihomo.exe   # Windows
bin/mihomo       # Linux / macOS
```

## 获取

```powershell
powershell -ExecutionPolicy Bypass -File scripts\fetch-mihomo.ps1
powershell ... -Force                        # 已存在也重新下载
powershell ... -Platform linux -Arch amd64   # 交叉准备
```

脚本与 CI 一致：下载官方 release 资产 → 校验 SHA256 → 解压 → `bin/mihomo[.exe]`。
版本与哈希以 `SOURCE.txt` 为准；升版本时同步改 `scripts/fetch-mihomo.ps1`、
`SOURCE.txt` 与 CI 的 `Fetch mihomo` 步骤。

缺失不阻断编译：`build.rs` 检测不到 `bin/mihomo[.exe]` 就不设 `embed_mihomo`，
壳不内嵌，节点池提示「未找到 mihomo」，其余功能正常。

## 运行时查找顺序

1. 配置 `node_pool_mihomo_path`
2. `<安装目录>/bin/mihomo(.exe)` ← 壳释放的位置
3. `<dataDir>/node-pool/mihomo(.exe)`

开发期可临时把任意 mihomo 兼容二进制拷成 `bin/mihomo.exe` 使用；发布包必须是
`SOURCE.txt` 登记的那一份官方构建。
