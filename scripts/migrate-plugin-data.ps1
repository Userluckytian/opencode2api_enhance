# migrate-plugin-data.ps1 — 插件数据目录迁移（默认 DryRun）
#
# 处理三类历史遗留：
#   1) LobsterAI：provider.json 的 auth_dir 指向源码仓库 → 迁到 <插件>/auths，并改成相对路径 "auths"
#   2) LobsterHub：%APPDATA%\LobsterHub → <插件>/data（与桌面版分家）
#   3) MiniMax：<插件>/accounts.json → <插件>/data/accounts.json（代码也会自动迁移，这里只报告）
#
# 安全规则：
#   - 默认只打印计划，不移动任何文件；必须显式 -Execute。
#   - 迁移前要求插件进程已停止（lobsterai2api / lobsterhub / mmxproxy-provider）。
#   - 一次性 refresh token：只能“移动”，不能复制两份；脚本先备份到 <name>.bak-<时间>。
#   - 不处理 Flonno/RunBios 的 1GB profile（本轮方案是临时 profile，不迁移）。
#
# 用法：
#   pwsh -File scripts/migrate-plugin-data.ps1                 # DryRun
#   pwsh -File scripts/migrate-plugin-data.ps1 -Execute        # 真执行（先停插件）
#   pwsh -File scripts/migrate-plugin-data.ps1 -ProvidersRoot "D:\...\providers" -Execute

[CmdletBinding()]
param(
  [string]$ProvidersRoot = 'D:\Program Files\opencode2api\bin\providers',
  [switch]$Execute,
  # 仅用于沙箱演练：跳过“插件进程必须停止”检查；真机不要用。
  [switch]$SkipProcessCheck
)

$ErrorActionPreference = 'Stop'
$stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
$script:actions = New-Object System.Collections.Generic.List[string]

function Plan([string]$text) {
  $script:actions.Add($text) | Out-Null
  Write-Host ("[{0}] {1}" -f $(if ($Execute) { 'EXEC' } else { 'DRY ' }), $text)
}

function Fail([string]$text) { throw "[migrate] $text" }

if (-not (Test-Path -LiteralPath $ProvidersRoot)) { Fail "providers 根目录不存在: $ProvidersRoot" }

# 1) 真执行时必须已停止（DryRun 只读，不要求停）
$running = Get-Process -ErrorAction SilentlyContinue | Where-Object {
  $_.ProcessName -in @('lobsterai2api', 'lobsterhub', 'mmxproxy-provider')
}
if ($Execute -and $running -and -not $SkipProcessCheck) {
  Fail ("请先在宿主面板停用相关插件（检测到进程: {0}）" -f (($running | Select-Object -ExpandProperty ProcessName) -join ', '))
}
if (-not $Execute -and $running) {
  Write-Host ("[DRY ] 注意：检测到运行中进程 {0}；真执行前需先停用插件。" -f (($running | Select-Object -ExpandProperty ProcessName) -join ', '))
}

function Move-WithBackup([string]$src, [string]$dst) {
  if (-not (Test-Path -LiteralPath $src)) { return }
  Plan ("移动 {0} -> {1}" -f $src, $dst)
  if (-not $Execute) { return }
  $parent = Split-Path -Parent $dst
  if (-not (Test-Path -LiteralPath $parent)) { New-Item -ItemType Directory -Force -Path $parent | Out-Null }
  $backup = "$src.bak-$stamp"
  Copy-Item -LiteralPath $src -Destination $backup -Force
  Move-Item -LiteralPath $src -Destination $dst -Force
}

# 2) LobsterAI：auth_dir 绝对路径 → <插件>/auths（相对）
$lobsterDir = Join-Path $ProvidersRoot 'lobsterai'
$lobsterCfg = Join-Path $lobsterDir 'provider.json'
if (Test-Path -LiteralPath $lobsterCfg) {
  $cfg = Get-Content -LiteralPath $lobsterCfg -Raw -Encoding UTF8 | ConvertFrom-Json
  $authDir = [string]$cfg.provider_private_configs.auth_dir
  if ($authDir -and [IO.Path]::IsPathRooted($authDir)) {
    $target = Join-Path $lobsterDir 'auths'
    $targetFiles = @(Get-ChildItem -LiteralPath $target -File -ErrorAction SilentlyContinue)
    if ($targetFiles.Count -gt 0) {
      Plan ("跳过 LobsterAI：目标 {0} 已有 {1} 个文件，避免覆盖" -f $target, $targetFiles.Count)
    } elseif (-not (Test-Path -LiteralPath $authDir)) {
      Plan ("跳过 LobsterAI：旧 auth_dir 不存在 {0}" -f $authDir)
    } else {
      Plan ("LobsterAI auth_dir: {0} -> {1}（并改 provider.json 为相对 auths）" -f $authDir, $target)
      if ($Execute) {
        New-Item -ItemType Directory -Force -Path $target | Out-Null
        Get-ChildItem -LiteralPath $authDir -Force | ForEach-Object {
          $to = Join-Path $target $_.Name
          if ($_.PSIsContainer) { Copy-Item -LiteralPath $_.FullName -Destination $to -Recurse -Force }
          else { Move-WithBackup $_.FullName $to }
        }
        Copy-Item -LiteralPath $lobsterCfg -Destination "$lobsterCfg.bak-$stamp" -Force
        $cfg.provider_private_configs.auth_dir = 'auths'
        # 不能 Set-Content -Encoding UTF8（PS5.1 会写 BOM，Go 的 encoding/json 会拒收）
        [IO.File]::WriteAllText($lobsterCfg, ($cfg | ConvertTo-Json -Depth 20), (New-Object System.Text.UTF8Encoding($false)))
      }
    }
  } else {
    Plan ("LobsterAI auth_dir 已是相对/空（{0}），无需迁移" -f $authDir)
  }
}

# 3) LobsterHub：%APPDATA%\LobsterHub → <插件>/data
$hubDir = Join-Path $ProvidersRoot 'lobsterhub'
$legacyHub = Join-Path $env:APPDATA 'LobsterHub'
$hubTarget = Join-Path $hubDir 'data'
if (Test-Path -LiteralPath $legacyHub) {
  if (Test-Path -LiteralPath (Join-Path $hubTarget 'vault.json')) {
    Plan ("跳过 LobsterHub：{0}\vault.json 已存在（已迁移/已分家）" -f $hubTarget)
  } else {
    Plan ("LobsterHub 数据目录: {0} -> {1}" -f $legacyHub, $hubTarget)
    if ($Execute) {
      New-Item -ItemType Directory -Force -Path $hubTarget | Out-Null
      $backup = "$legacyHub.bak-$stamp"
      Copy-Item -LiteralPath $legacyHub -Destination $backup -Recurse -Force
      Get-ChildItem -LiteralPath $legacyHub -Force | ForEach-Object {
        Move-WithBackup $_.FullName (Join-Path $hubTarget $_.Name)
      }
    }
  }
} else {
  Plan "LobsterHub：%APPDATA%\LobsterHub 不存在，无需迁移"
}

# 4) MiniMax：根 accounts.json → data/accounts.json（代码启动时自动复制，这里只报告）
$mmxDir = Join-Path $ProvidersRoot 'mmxproxy'
$mmxLegacy = Join-Path $mmxDir 'accounts.json'
$mmxTarget = Join-Path $mmxDir 'data\accounts.json'
if ((Test-Path -LiteralPath $mmxLegacy) -and -not (Test-Path -LiteralPath $mmxTarget)) {
  Plan ("MiniMax 号池: {0} -> {1}（插件启动会自动复制，无需手工）" -f $mmxLegacy, $mmxTarget)
} else {
  Plan "MiniMax 号池：已在 data/ 或没有根文件，无需迁移"
}

# 5) Flonno / RunBios：明确不迁移 1GB profile
Plan "Flonno/RunBios profile：本轮不迁移（改用临时 profile 方案）；不要复制 ~/.qw2api/chrome-autoreg"

Write-Host ""
Write-Host ("共 {0} 条计划；{1}" -f $script:actions.Count, $(if ($Execute) { '已执行。请启动插件并在面板核对账号数/额度。' } else { 'DryRun，未改动任何文件。' }))
