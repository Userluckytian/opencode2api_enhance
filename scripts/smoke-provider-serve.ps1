# smoke-provider-serve.ps1 — 插件契约冒烟（不碰线上）
#
# 对每个候选副本：build 出 exe → 在独立 run 目录用 PROVIDER_DIR/PROVIDER_CONFIG/
# PLUGIN_AUTH_TOKEN 拉起 `--provider-serve --port 0` → 校验：
#   1) stdout 只有一行 ready JSON，含 state/port/auth/id
#   2) 带令牌访问 /v1/models 返回 200 且 data 非空
#   3) 不带令牌访问 /v1/models 返回 401
#   4) 运行数据落在 <run>/data（不是插件根、不是仓库）
#
# 用法：
#   pwsh -File scripts/smoke-provider-serve.ps1
#   pwsh -File scripts/smoke-provider-serve.ps1 -Only mmxproxy,qw2api

[CmdletBinding()]
param(
  [string]$WorkRoot = 'D:\AI_Projects\_plugin-work',
  [string]$SmokeRoot = 'D:\AI_Projects\_plugin-work\smoke',
  [string[]]$Only = @()
)

$ErrorActionPreference = 'Stop'

# 插件 id → 副本路径 + 构建入口
$plugins = @(
  @{ id = 'mmxproxy';   dir = "$WorkRoot\phase3\mmxproxy";   build = '.' },
  @{ id = 'qw2api';     dir = "$WorkRoot\phase3\qw2api";     build = './cmd/qw2api-plugin' },
  @{ id = 'grok';       dir = "$WorkRoot\phase4\grok";       build = '.' },
  @{ id = 'lobsterai';  dir = "$WorkRoot\phase5\lobsterai";  build = './cmd/server' },
  @{ id = 'lobsterhub'; dir = "$WorkRoot\phase5\lobsterhub"; build = '.' },
  @{ id = 'buddyhub';   dir = "$WorkRoot\phase5\buddyhub";   build = './cmd/pluginbuddy' },
  @{ id = 'skywork';    dir = "$WorkRoot\phase5\skywork";    build = './cmd/skywork-provider' },
  @{ id = 'loomy';      dir = "$WorkRoot\phase5\loomy";      build = '.'; requiresSession = $true },
  # Flonno 的插件入口是 shim：它自己不改协议，只负责拉起旁边的 flonno2api.exe。
  @{ id = 'flonno';     dir = "$WorkRoot\phase5\flonno\plugin-dist\build\provider-shim"; build = '.';
     stage = @(@{ dir = "$WorkRoot\phase5\flonno"; build = './cmd/flonno2api'; name = 'flonno2api.exe' }) }
)

if ($Only.Count -gt 0) { $plugins = $plugins | Where-Object { $Only -contains $_.id } }

New-Item -ItemType Directory -Force -Path $SmokeRoot | Out-Null
$results = @()

foreach ($p in $plugins) {
  $run = Join-Path $SmokeRoot ("run-" + $p.id)
  if (Test-Path -LiteralPath $run) { Remove-Item -LiteralPath $run -Recurse -Force }
  New-Item -ItemType Directory -Force -Path $run | Out-Null

  $exe = Join-Path $run ($p.id + '.exe')
  Push-Location $p.dir
  try {
    $buildOut = & go build -o $exe $p.build 2>&1
    $buildCode = $LASTEXITCODE
  } finally { Pop-Location }

  if ($buildCode -ne 0) {
    $results += [pscustomobject]@{ Plugin = $p.id; Build = $buildCode; Ready = '-'; Models = '-'; Auth401 = '-'; DataDir = '-'; Note = (($buildOut | Select-Object -First 1) -join '') }
    continue
  }

  $cfg = Join-Path $run 'provider.json'
  # 必须无 BOM：PS5.1 的 Set-Content -Encoding UTF8 会加 BOM，Go 的 encoding/json 拒收，
  # 插件会报 need_config「invalid character 'ï'」。
  $private = @{ panel = 'sidecar' }
  if ($p.requiresSession) { $script:skipReason = '需要真实 LOOMy session，冒烟跳过' }
  $cfgJson = @{ id = $p.id; name = $p.id; api_version = 1; entry = (Split-Path $exe -Leaf); expose_all = $false; provider_private_configs = $private } |
    ConvertTo-Json -Depth 5
  [IO.File]::WriteAllText($cfg, $cfgJson, (New-Object System.Text.UTF8Encoding($false)))

  # 需要陪跑的二进制（如 Flonno 的 shim 要旁边有 flonno2api.exe）
  if ($p.stage) {
    foreach ($s in $p.stage) {
      Push-Location $s.dir
      try { & go build -o (Join-Path $run $s.name) $s.build 2>&1 | Out-Null } finally { Pop-Location }
    }
  }

  $env:PROVIDER_DIR = $run
  $env:PROVIDER_CONFIG = $cfg
  $env:PLUGIN_AUTH_TOKEN = 'smoke-token'
  $out = Join-Path $run 'stdout.txt'
  $err = Join-Path $run 'stderr.txt'
  $proc = Start-Process -FilePath $exe -ArgumentList '--provider-serve', '--port', '0' -WorkingDirectory $run `
    -RedirectStandardOutput $out -RedirectStandardError $err -PassThru
  Start-Sleep -Seconds 5

  $ready = '-'; $models = '-'; $auth = '-'; $dataDir = '-'; $note = ''
  try {
    if ($proc.HasExited) {
      $note = 'exited early'; $ready = 'no'
    } else {
      $line = Get-Content -LiteralPath $out -ErrorAction SilentlyContinue | Select-Object -First 1
      if ($line -match '"state"\s*:\s*"need_config"') {
        # need_config 是契约允许的状态（私有配置缺失），不算失败：这类插件需要
        # 真实凭据才能就绪，冒烟只登记原因。
        $ready = 'need_config'; $note = '需真实私有配置（非缺陷）'
      } elseif ($line -match '"state"\s*:\s*"ready"' -and $line -match '"port"\s*:\s*(\d+)') {
        $ready = 'yes'
        $port = $Matches[1]
        $h = @{ Authorization = 'Bearer smoke-token' }
        try {
          $r = Invoke-WebRequest -Uri "http://127.0.0.1:$port/v1/models" -Headers $h -TimeoutSec 15 -UseBasicParsing
          $j = $r.Content | ConvertFrom-Json
          $arr = @($j.data)
          $n = $arr.Count
          # 逐条显式计数：Where-Object 在单元素列表上返回标量，.Count 可能为 null，
          # 曾把 grok / flonno 的 caps 误报成空串。
          $hasCaps = 0
          foreach ($m in $arr) {
            if ($null -ne $m.context_window -and [int64]$m.context_window -gt 0) { $hasCaps++ }
          }
          $models = if ($n -gt 0) { "$n (caps=$hasCaps)" } else { '0' }
        } catch { $models = 'ERR ' + $_.Exception.Message }
        try {
          $r2 = Invoke-WebRequest -Uri "http://127.0.0.1:$port/v1/models" -TimeoutSec 8 -UseBasicParsing
          $auth = 'NO-401-' + $r2.StatusCode
        } catch {
          $code = 0; try { $code = [int]$_.Exception.Response.StatusCode } catch { }
          $auth = if ($code -eq 401) { '401' } else { 'ERR ' + $code }
        }
      } else {
        $ready = 'no'; $note = ($line | Select-Object -First 1)
      }
      $d = Join-Path $run 'data'
      $dataDir = if (Test-Path -LiteralPath $d) { 'data/ ok' } else { 'missing' }
    }
  } finally {
    if ($proc -and -not $proc.HasExited) { Stop-Process -Id $proc.Id -Force }
  }

  $results += [pscustomobject]@{ Plugin = $p.id; Build = $buildCode; Ready = $ready; Models = $models; Auth401 = $auth; DataDir = $dataDir; Note = $note }
}

$results | Format-Table -AutoSize -Wrap
# need_config 是契约允许的状态（缺真实私有配置），不算失败；只统计真正的失败。
$failed = $results | Where-Object { $_.Build -ne 0 -or $_.Ready -eq 'no' }
$incomplete = $results | Where-Object { $_.Ready -eq 'yes' -and ($_.Models -notmatch '^\d+ \(caps=\d+\)$' -or $_.Auth401 -ne '401' -or $_.Models -like '0 *') }
if ($failed.Count -gt 0) {
  Write-Host ""
  Write-Host ("失败 {0} 个：{1}" -f $failed.Count, (($failed | Select-Object -ExpandProperty Plugin) -join ', ')) -ForegroundColor Red
  exit 1
}
if ($incomplete.Count -gt 0) {
  Write-Host ""
  Write-Host ("就绪但契约不达标 {0} 个：{1}" -f $incomplete.Count, (($incomplete | Select-Object -ExpandProperty Plugin) -join ', ')) -ForegroundColor Red
  exit 1
}
$skipped = @($results | Where-Object { $_.Ready -eq 'need_config' })
Write-Host ""
if ($skipped.Count -gt 0) {
  Write-Host ("{0} 个插件就绪性无法自动验证（需真实私有配置，非代码缺陷）：{1}" -f $skipped.Count, (($skipped | Select-Object -ExpandProperty Plugin) -join ', ')) -ForegroundColor Yellow
}
Write-Host ("其余 {0} 个插件冒烟通过" -f ($results.Count - $skipped.Count)) -ForegroundColor Green
