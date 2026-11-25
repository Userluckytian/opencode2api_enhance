# fetch-free-tier-engine.ps1
# ============================================================
# 获取免费层引擎（40080）二进制：github.com/wbi35772/opencode2api-win
# （tag v1.5.1-win.1；该 fork 已合并上游 jasonxu114514/opencode2api v1.5.1
#   全量，并保留 WARP / 会话亲和 / 免费通道增强。GPL-3.0 独立程序，本项目以
#   独立进程 + HTTP 方式使用，见 third_party/opencode2api-win/ 的 LICENSE / SOURCE.txt）
#
# 用法：
#   powershell -ExecutionPolicy Bypass -File scripts\fetch-free-tier-engine.ps1
#   powershell ... -Commit 1a208acbda4346e9492a6ad5266a46b70b21642f
#   powershell ... -SkipClone          # 复用临时目录里已克隆的源码
#
# 为什么是「源码构建」而不是下载 release 资产：上游仓库当前没有可下载的
# release 二进制资产，只能用固定 commit 自行构建，以保证 bin/ 里的引擎与
# SOURCE.txt 登记的提交一致（提交号变更请同步改 -Commit 与 SOURCE.txt）。
#
# 前置条件：
#   - git 在 PATH 上
#   - go  在 PATH 上（上游 go.mod 声明 go 1.24，需 go >= 1.24）
#
# 输出：
#   bin/opencode2api-win.exe   （Windows）
#   bin/opencode2api-win       （非 Windows，带执行位）
#
# 本脚本只构建、不运行引擎；不含任何密钥。
# NOTE: PS 5.1 按 ANSI 读无 BOM 的 UTF-8；本文件带 UTF-8 BOM 保存，
#       以免中文注释在 5.1 下乱码。
# ============================================================

param(
    # 固定提交（pinned）：与 SOURCE.txt 保持一致，勿随意改成 "main"。
    [string]$Commit = "1a208acbda4346e9492a6ad5266a46b70b21642f",
    [string]$Repo = "https://github.com/wbi35772/opencode2api-win.git",
    # 留空 = 按当前 OS 选择（Windows 加 .exe）。
    [string]$OutBin = "",
    # 复用已存在的 engine-src 目录（离线/重复构建时用）。
    [switch]$SkipClone
)

$ErrorActionPreference = "Stop"

$IsWin = [bool]($IsWindows -or ($env:OS -eq "Windows_NT"))

$Root = Split-Path -Parent $PSScriptRoot
Set-Location $Root

Write-Host "== 拉取免费层引擎 opencode2api-win =="
Write-Host "  repo   : $Repo"
Write-Host "  commit : $Commit"

function Require-Cmd {
    param([string]$Name, [string]$Hint)
    if (-not (Get-Command $Name -ErrorAction SilentlyContinue)) {
        throw "未找到 '$Name'：$Hint"
    }
}

Require-Cmd "git" "需要 git 克隆上游引擎源码（安装 Git for Windows 后重试）"
Require-Cmd "go"  "需要 Go 工具链构建引擎（上游要求 go >= 1.24，https://go.dev/dl/）"

# ---- 产物路径 ----
if (-not $OutBin) {
    $OutBin = if ($IsWin) { "bin\opencode2api-win.exe" } else { "bin/opencode2api-win" }
}
$OutBin = [System.IO.Path]::GetFullPath((Join-Path $Root $OutBin))
$outDir = Split-Path -Parent $OutBin
if (-not (Test-Path $outDir)) { New-Item -ItemType Directory -Force -Path $outDir | Out-Null }

# ---- 1. 取源码（优先用仓库内快照，失败才回退 clone）----
# 外部分叉 wbi35772/opencode2api-win 已两次 404，发布链路不能再依赖它：
# 源码快照 third_party\opencode2api-win\src\opencode2api-win-1a208ac.tar.gz
# 随仓库走，解包后再套 patches\ 下的补丁。
# -SkipClone 时复用 <TEMP>\opencode2api-win-engine-src（离线重试方便）。
$tmp = Join-Path ([System.IO.Path]::GetTempPath()) "opencode2api-win-engine-src"
$src = $tmp
$srcTgz = Join-Path $Root "third_party\opencode2api-win\src\opencode2api-win-1a208ac.tar.gz"
if (-not $SkipClone) {
    if (Test-Path $src) { Remove-Item -Recurse -Force $src }
    if (Test-Path $srcTgz) {
        Write-Host "[1/3] 解包仓库内源码快照 ..."
        New-Item -ItemType Directory -Path $src | Out-Null
        & tar -xzf $srcTgz -C $src
        if ($LASTEXITCODE -ne 0) { throw "解包失败：$srcTgz" }
    } else {
        Write-Host "[1/3] 快照缺失，回退 git clone --no-checkout ..."
        & git clone --no-checkout $Repo $src
        if ($LASTEXITCODE -ne 0) { throw "git clone 失败（exit $LASTEXITCODE）：检查网络 / 仓库地址 $Repo" }
        Push-Location $src
        & git checkout $Commit
        if ($LASTEXITCODE -ne 0) {
            Pop-Location
            throw "git checkout $Commit 失败：该提交在上游可能已不可见，请更新 SOURCE.txt 与 -Commit"
        }
        Pop-Location
    }
} elseif (-not (Test-Path (Join-Path $src "go.mod"))) {
    throw ("-SkipClone 但临时源码目录不存在或不完整：{0}；请先不带 -SkipClone 运行一次" -f $src)
}

# ---- 1b. 应用本地补丁（patches\ 下按文件名顺序全部应用）----
# 上游 1a208ac 既没有 registration_proxies，也没有 per-proxy 注册统计；
# 不补的话节点池只能写 socks_udp、面板也看不到每个节点的注册成败。
# 已应用过（-SkipClone 复用目录）时 git apply --check 会失败，按"已打过"处理。
$patchDir = Join-Path $Root "third_party\opencode2api-win\patches"
$patches = @()
if (Test-Path $patchDir) {
    $patches = @(Get-ChildItem -Path $patchDir -Filter *.patch | Sort-Object Name)
}
if ($patches.Count -gt 0) {
    Push-Location $src
    foreach ($p in $patches) {
        & git apply --check $p.FullName 2>$null
        if ($LASTEXITCODE -eq 0) {
            & git apply $p.FullName
            if ($LASTEXITCODE -ne 0) { Pop-Location; throw "git apply 失败：$($p.Name)" }
            Write-Host "[1b/3] 已应用补丁: $($p.Name)"
        } else {
            Write-Host "[1b/3] 补丁已应用（或源码已包含该改动），跳过: $($p.Name)"
            & git apply --reverse --check $p.FullName 2>$null
            if ($LASTEXITCODE -ne 0) {
                Write-Host "[1b/3] 警告：既打不上也反向打不上，源码可能已被改过；继续按现状构建"
            }
        }
    }
    Pop-Location
    # 补丁是否真的落到源码上：git apply 在 git 工作树内、目标路径又落在当前目录
    # 之外时会**静默跳过并返回 0**（Skipped patch ...）。退出码不可信，按内容验。
    $cfgFile = Join-Path $src "internal\config\config.go"
    if (-not (Select-String -Path $cfgFile -Pattern "RegistrationProxies" -Quiet)) {
        throw "补丁未生效：$cfgFile 里没有 RegistrationProxies（引擎将不认 registration_proxies）"
    }
    $poolFile = Join-Path $src "internal\warp\pool.go"
    if (-not (Select-String -Path $poolFile -Pattern "RegistrarStat" -Quiet)) {
        throw "补丁未生效：$poolFile 里没有 RegistrarStat（面板将看不到每节点注册成败）"
    }
} else {
    Write-Host "[1b/3] 未找到补丁文件，按上游源码构建（节点池将只能写 socks_udp）"
}

Push-Location $src
$sha = & git rev-parse HEAD
$rc = $LASTEXITCODE
Pop-Location
if ($rc -ne 0 -or -not $sha) { throw "git rev-parse HEAD 失败（临时源码目录不是 git 仓库？）" }
$sha = ($sha | Out-String).Trim()
Write-Host "      实际提交: $sha"

# ---- 2. 构建（CGO 关闭：只产宿主平台二进制）----
Write-Host "[2/3] go build (CGO_ENABLED=0) ..."
# GOOS/GOARCH 取宿主值：上游引擎依赖 wireguard / gVisor 的平台实现，
# 本地脚本只负责宿主平台产物；跨平台打包由 CI 矩阵（build-release.yml）完成。
$goos = ((& go env GOOS) | Out-String).Trim()
$goarch = ((& go env GOARCH) | Out-String).Trim()
if (-not $goos -or -not $goarch) { throw "go env GOOS/GOARCH 返回空，Go 环境异常" }

# embed.rs / Go 侧按 platform_name() 解析，构建名不带平台后缀。
$binName = Split-Path -Leaf $OutBin
if ($binName -match '\.exe$') { $binName = $binName -replace '\.exe$', '' }

Push-Location $src
$env:CGO_ENABLED = "0"
$env:GOOS = $goos
$env:GOARCH = $goarch
& go build -trimpath -ldflags "-s -w" -o $binName ./cmd/opencode2api
$rc = $LASTEXITCODE
Pop-Location
if ($rc -ne 0) { throw "引擎构建失败（exit $rc）。宿主 $goos/$goarch，需 go >= 1.24" }

$built = Join-Path $src $binName
if (-not (Test-Path $built)) { throw "构建未产生产物：$built" }

# ---- 3. 安装到 bin/ ----
Write-Host "[3/3] 安装 -> $OutBin"
Copy-Item $built $OutBin -Force
if (-not $IsWin) {
    # 非 Windows 需要执行位；chmod 是 PS 在 Unix 上最稳的写法。
    if (Get-Command chmod -ErrorAction SilentlyContinue) {
        & chmod 755 $OutBin
    }
}
$len = (Get-Item $OutBin).Length
if ($len -lt 1048576) { throw "产物过小（$len 字节），疑似构建异常" }
$hash = "n/a"
try { $hash = (Get-FileHash -Algorithm SHA256 $OutBin).Hash } catch { }

Write-Host ""
Write-Host "== 免费层引擎就绪 =="
Write-Host ("  path   : {0}" -f $OutBin)
Write-Host ("  size   : {0:N2} MB" -f ($len / 1MB))
Write-Host ("  sha256 : {0}" -f $hash)
Write-Host ("  commit : {0}" -f $sha)
Write-Host "  下一步 : 重新 cargo build（build.rs 会自动内嵌 bin/ 下的引擎）"
Write-Host "           并同步 third_party/opencode2api-win/SOURCE.txt 的 commit 号"
