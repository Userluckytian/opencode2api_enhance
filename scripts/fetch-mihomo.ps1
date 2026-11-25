# fetch-mihomo.ps1
# ============================================================
# 获取「WARP 注册节点池」内核 mihomo 到 bin/，供桌面壳内嵌。
# 与 CI（.github/workflows/build-release.yml 的 Fetch mihomo 步骤）一致：
#   下载官方 release 资产 → 校验 SHA256 → 解压 → bin/mihomo[.exe]
#
# 版本与哈希登记在 third_party/mihomo/SOURCE.txt，改版本时两处同步。
# mihomo 是 GPL-3.0 独立程序，本项目以独立子进程方式使用（mere aggregation）。
#
# 用法：
#   powershell -ExecutionPolicy Bypass -File scripts\fetch-mihomo.ps1
#   powershell ... -Force            # 已存在也重新下载
#   powershell ... -Platform linux -Arch amd64   # 交叉准备
#
# 输出：
#   bin/mihomo.exe  （Windows）
#   bin/mihomo      （Linux / macOS，带执行位）
#
# 本脚本只下载、不运行；不含任何密钥。
# NOTE: PS 5.1 按 ANSI 读无 BOM 的 UTF-8；本文件带 UTF-8 BOM 保存。
# ============================================================

param(
    [string]$Version = "v1.19.33",
    [string]$Platform = "",
    [string]$Arch = "",
    [switch]$Force
)

$ErrorActionPreference = "Stop"

$Root = Split-Path -Parent $PSScriptRoot
Set-Location $Root

# 平台：Windows / macOS / Linux（PS 5.1 没有 $IsMacOS，用 uname 兜底）
if ($Platform -eq "") {
    if ($env:OS -eq "Windows_NT") {
        $Platform = "windows"
    } elseif ((Test-Path variable:IsMacOS) -and $IsMacOS) {
        $Platform = "darwin"
    } elseif ((Get-Command uname -ErrorAction SilentlyContinue) -and ((uname) -eq "Darwin")) {
        $Platform = "darwin"
    } else {
        $Platform = "linux"
    }
}

if ($Arch -eq "") {
    if ($env:PROCESSOR_ARCHITECTURE -match "ARM64" -or $env:PROCESSOR_ARCHITEW6432 -match "ARM64") {
        $Arch = "arm64"
    } else {
        $Arch = "amd64"
    }
}

# 平台 → 资产名 / SHA256（与 SOURCE.txt 一致）
$assets = @{
    "windows/amd64" = @("mihomo-windows-amd64-$Version.zip",   "6ab2180875f9f6fff8b19386d760daec257f66b45f72b3afc650634272fa837a")
    "linux/amd64"   = @("mihomo-linux-amd64-$Version.gz",      "132f6f3edf9a65e62f04853aed3358472e0b2b7666daa8419edfa2ddf5bb6323")
    "darwin/arm64"  = @("mihomo-darwin-arm64-$Version.gz",     "060b64b0f2c9a310831db0fe4adb2878de67bfa606c311813c1f13966aa504b6")
    "darwin/amd64"  = @("mihomo-darwin-amd64-$Version.gz",     "551ae54ef8d77a5e21f7a8185a59b441fb13a0fb0514fcdef5ca2816f6f50015")
}

$key = "$Platform/$Arch"
if (-not $assets.ContainsKey($key)) {
    throw "不支持的平台组合：$key（可选：$($assets.Keys -join ', ')）"
}
$asset, $sha = $assets[$key]

$outName = if ($Platform -eq "windows") { "mihomo.exe" } else { "mihomo" }
$dest = Join-Path (Join-Path $Root "bin") $outName

if ((Test-Path $dest) -and (-not $Force)) {
    Write-Host "已存在 $dest（如需重新下载加 -Force）"
    exit 0
}

Write-Host "== 获取 mihomo $Version ($key) =="
$url = "https://github.com/MetaCubeX/mihomo/releases/download/$Version/$asset"
$tmpDir = Join-Path ([IO.Path]::GetTempPath()) ("mihomo-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $tmpDir | Out-Null
$tmpFile = Join-Path $tmpDir $asset

try {
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
    Write-Host "  下载 $url"
    Invoke-WebRequest -Uri $url -OutFile $tmpFile -UseBasicParsing

    $actual = (Get-FileHash -Algorithm SHA256 -Path $tmpFile).Hash.ToLower()
    if ($actual -ne $sha) {
        throw "SHA256 不匹配：期望 $sha，实际 $actual（下载可能被劫持或版本资产已变）"
    }
    Write-Host "  SHA256 校验通过"

    New-Item -ItemType Directory -Path (Split-Path -Parent $dest) -Force | Out-Null

    if ($asset.EndsWith(".zip")) {
        Expand-Archive -Path $tmpFile -DestinationPath $tmpDir -Force
        # 官方 zip 里的文件名带平台后缀（mihomo-windows-amd64.exe），不叫 mihomo.exe
        $found = Get-ChildItem -Path $tmpDir -Recurse -File -Filter "*.exe" | Select-Object -First 1
        if (-not $found) { throw "压缩包里没有 .exe" }
        Copy-Item -Path $found.FullName -Destination $dest -Force
    } else {
        $in = [IO.File]::OpenRead($tmpFile)
        try {
            $out = [IO.File]::Create($dest)
            try {
                $gz = New-Object IO.Compression.GZipStream($in, [IO.Compression.CompressionMode]::Decompress)
                try { $gz.CopyTo($out) } finally { $gz.Dispose() }
            } finally { $out.Dispose() }
        } finally { $in.Dispose() }
        if (Get-Command chmod -ErrorAction SilentlyContinue) { & chmod +x $dest }
    }

    $size = (Get-Item $dest).Length
    Write-Host "  完成：$dest（$([math]::Round($size / 1MB, 1)) MB）"
} finally {
    Remove-Item -Recurse -Force $tmpDir -ErrorAction SilentlyContinue
}
