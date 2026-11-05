# restore-plugins.ps1 — 从备份还原插件目录（默认 DryRun）
#
# 备份由 backup-plugins.ps1 生成，四层结构：
#   01-config.zip     所有 provider.json + .plugin-state.json（17.8 KB）
#   02-app-9plugins.zip  9 个插件全目录，排除 chrome-autoreg 与 *.log（163.5 MB）
#   03-manifest-full.txt 全量文件清单（名+大小，用于还原后比对）
#   04-live-data.zip  号池/状态/账号等关键运行数据（249.8 KB）
#
# 还原策略（-Layer）：
#   config  只还原 provider.json（配置改坏了用这个，最快）
#   app     还原 9 个插件的 exe 与代码（排除 profile，不动你的账号数据）
#   data    还原号池/账号/状态（账号丢了这个才用）
#   all     app + data + config
#
# 注意：还原 exe 必须先停插件（Windows 文件锁）。脚本会检查并拒绝继续。
#
# 用法：
#   pwsh -File scripts/restore-plugins.ps1 -Backup <备份目录>              # DryRun
#   pwsh -File scripts/restore-plugins.ps1 -Backup <备份目录> -Layer config -Execute
#   pwsh -File scripts/restore-plugins.ps1 -Backup <备份目录> -Layer all -Execute

[CmdletBinding()]
param(
  [Parameter(Mandatory = $true)][string]$Backup,
  [ValidateSet('config', 'app', 'data', 'all')][string]$Layer = 'app',
  [string]$ProvidersRoot = 'D:\Program Files\opencode2api\bin\providers',
  [switch]$Execute
)

$ErrorActionPreference = 'Stop'

function Step([string]$msg) { Write-Host ("[{0}] {1}" -f $(if ($Execute) { 'EXEC' } else { 'DRY ' }), $msg) }
function Fail([string]$msg) { throw "[restore] $msg" }

if (-not (Test-Path -LiteralPath $Backup)) { Fail "备份目录不存在: $Backup" }
if (-not (Test-Path -LiteralPath $ProvidersRoot)) { Fail "providers 根不存在: $ProvidersRoot" }

$zips = @{
  config = Join-Path $Backup '01-config.zip'
  app    = Join-Path $Backup '02-app-9plugins.zip'
  data   = Join-Path $Backup '04-live-data.zip'
}

$want = switch ($Layer) {
  'config' { @('config') }
  'app'    { @('app') }
  'data'   { @('data') }
  'all'    { @('app', 'data', 'config') }
}

foreach ($k in $want) {
  if (-not (Test-Path -LiteralPath $zips[$k])) { Fail "缺少备份层 $k : $($zips[$k])" }
}

# ---- 1) 插件必须已停（只对 app / data 层要求；config 也建议停）----
$procNames = @('mmxproxy-provider', 'qw2api-provider', 'grok-provider', 'lobsterai2api',
  'lobsterhub', 'buddyhub-provider', 'skywork-provider', 'loomy-provider',
  'flonno2api', 'flonno2api-provider', 'grok2api')
$running = @(Get-Process -ErrorAction SilentlyContinue | Where-Object { $procNames -contains $_.ProcessName })
if ($running.Count -gt 0) {
  $names = ($running | Select-Object -ExpandProperty ProcessName -Unique) -join ', '
  if ($Execute) { Fail "插件仍在运行，请先在宿主停用或结束进程：$names" }
  Write-Host "[DRY ] 注意：检测到运行中进程 $names；-Execute 前必须先停。" -ForegroundColor Yellow
}

# ---- 2) 解压到暂存区（不直接覆盖，先校验）----
$stage = Join-Path $env:TEMP ("restore-" + (Get-Date -Format 'HHmmss'))
if ($Execute) { New-Item -ItemType Directory -Force -Path $stage | Out-Null }

foreach ($k in $want) {
  $dest = Join-Path $stage $k
  Step ("解压 {0} -> {1}" -f (Split-Path $zips[$k] -Leaf), $dest)
  if ($Execute) {
    New-Item -ItemType Directory -Force -Path $dest | Out-Null
    & tar.exe -xf $zips[$k] -C $dest
    if ($LASTEXITCODE -ne 0) { Fail "解压失败: $($zips[$k])" }
  }
}

# ---- 3) 覆盖前把「当前状态」也留一份（防止还原把新的好东西冲掉）----
if ($Execute) {
  $pre = Join-Path $Backup ("pre-restore-" + (Get-Date -Format 'yyyyMMdd-HHmmss'))
  New-Item -ItemType Directory -Force -Path $pre | Out-Null
  Step ("本次覆盖前的现场快照 -> $pre")
  & tar.exe -a -cf (Join-Path $pre 'current-config.zip') -C $ProvidersRoot .plugin-state.json 2>&1 | Out-Null
}

# ---- 4) 应用 ----
function Apply-Layer([string]$key, [string]$srcRoot) {
  if (-not (Test-Path -LiteralPath $srcRoot)) { return }
  foreach ($item in Get-ChildItem -LiteralPath $srcRoot -Force) {
    $target = Join-Path $ProvidersRoot $item.Name
    if ($item.PSIsContainer) {
      Step ("目录 {0} -> {1}" -f $item.Name, $target)
      if ($Execute) { & robocopy $item.FullName $target /E /NFL /NDL /NJH /NJS /NC /NS 2>&1 | Out-Null }
    } else {
      Step ("文件 {0} -> {1}" -f $item.Name, $target)
      if ($Execute) { Copy-Item $item.FullName $target -Force }
    }
  }
}

if ($Execute) {
  if ($want -contains 'app') { Apply-Layer 'app' (Join-Path $stage 'app') }
  if ($want -contains 'data') { Apply-Layer 'data' (Join-Path $stage 'data') }
  if ($want -contains 'config') { Apply-Layer 'config' (Join-Path $stage 'config') }
  Step "还原完成。请在宿主面板确认插件 ready、模型目录与账号数。"
  Step ("暂存目录保留备查: $stage")
} else {
  Write-Host ""
  Write-Host "DryRun 完成，未改动任何文件。将执行：" -ForegroundColor Green
  foreach ($k in $want) {
    Write-Host ("  - 解压 {0} 并覆盖到 {1}" -f (Split-Path $zips[$k] -Leaf), $ProvidersRoot)
  }
  Write-Host "加 -Execute 真正执行；建议先 -Layer config 演练一次。"
}
