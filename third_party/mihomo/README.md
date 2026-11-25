# third_party/mihomo

节点池（WARP 注册源）使用的内核二进制。**发布包必须内置**，避免依赖用户机器上的 Clash。

## 许可

- mihomo（原 Clash.Meta）为 **GPL-3.0**。
- 随安装包分发时必须同时提供：
  - 本目录下的 `LICENSE`（mihomo 原始许可证）
  - `SOURCE.txt`：上游仓库、tag/commit、构建命令、本地补丁说明（如无补丁写「无」）
- 发行说明中注明「本项目以独立进程方式使用 mihomo（mere aggregation）」。

## 目录约定

```
third_party/mihomo/
  LICENSE            # GPL-3.0 原文
  SOURCE.txt         # 上游仓库 + tag + 构建方式
  <platform>/mihomo  # 构建时放入，不提交 git（体积大）
```

## 构建/获取

发布 CI 按平台执行（示例，版本号固定）：

```powershell
$tag = "v1.19.15"   # 固定 tag，不跟 latest
# 下载对应平台 release 包，取出 mihomo 二进制后放到 bin/ 再打包
```

或从源码构建：

```bash
git clone --depth 1 --branch <tag> https://github.com/MetaCubeX/mihomo.git
cd mihomo && go build -trimpath -ldflags "-s -w" -o mihomo ./  # 具体入口以该 tag 为准
```

## 运行时查找顺序

1. 配置 `node_pool_mihomo_path`
2. `<安装目录>/bin/mihomo(.exe)`
3. `<dataDir>/node-pool/mihomo(.exe)`

开发期可临时把任意 mihomo 兼容二进制拷成 `bin/mihomo.exe` 使用；发布包必须放官方构建。
