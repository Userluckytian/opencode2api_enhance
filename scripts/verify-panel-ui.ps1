# verify-panel-ui.ps1 — 面板可用性检查（不碰线上）
#
# 对每个插件：拉起 → 找到面板端口 → 抓首页 HTML → 检查「事件」入口与关键区块是否
# 真的渲染出来（不是只看 Go 代码里有没有字符串）。
#
# 面板端口来源：
#   - panel="sidecar"：与契约同端口，面板在 `/`
#   - panel_port>0：固定端口（MiniMax），面板在 `http://127.0.0.1:<panel_port>/`
#
# 用法：pwsh -File scripts/verify-panel-ui.ps1

[CmdletBinding()]
param(
  [string]$WorkRoot = 'D:\AI_Projects\_plugin-work',
  [string]$SmokeRoot = 'D:\AI_Projects\_plugin-work\smoke'
)

$ErrorActionPreference = 'Stop'

# markers：面板 HTML 里必须出现的片段（用来证明 UI 真的接上了，而不是只改了 Go）
$plugins = @(
  @{ id = 'mmxproxy';   dir = "$WorkRoot\phase3\mmxproxy";   build = '.';                     panelPort = 18991; markers = @('事件', 'events.jsonl') },
  @{ id = 'qw2api';     dir = "$WorkRoot\phase3\qw2api";     build = './cmd/qw2api-plugin';   panelPort = 0;     markers = @('事件', '/api/events') },
  @{ id = 'grok';       dir = "$WorkRoot\phase4\grok";       build = '.';                     panelPort = 0;     markers = @('事件', 'log-source') },
  @{ id = 'lobsterai';  dir = "$WorkRoot\phase5\lobsterai";  build = './cmd/server';          panelPort = 0;     markers = @('事件', 'data-view="events"'); assets = @('/assets/app.js', 'renderEvents') },
  @{ id = 'lobsterhub'; dir = "$WorkRoot\phase5\lobsterhub"; build = '.';                     panelPort = 0;     markers = @('事件', 'view-events') },
  @{ id = 'buddyhub';   dir = "$WorkRoot\phase5\buddyhub";   build = './cmd/pluginbuddy';     panelPort = 0;     markers = @('事件', 'loadEvents') }
)

New-Item -ItemType Directory -Force -Path $SmokeRoot | Out-Null
$results = @()

foreach ($p in $plugins) {
  $run = Join-Path $SmokeRoot ("ui-" + $p.id)
  if (Test-Path -LiteralPath $run) { Remove-Item -LiteralPath $run -Recurse -Force }
  New-Item -ItemType Directory -Force -Path $run | Out-Null

  $exe = Join-Path $run ($p.id + '.exe')
  Push-Location $p.dir
  try { & go build -o $exe $p.build 2>&1 | Out-Null; $bc = $LASTEXITCODE } finally { Pop-Location }
  if ($bc -ne 0) { $results += [pscustomobject]@{ Plugin = $p.id; Panel = 'build-fail'; Markers = '-'; Detail = '' }; continue }

  $private = @{ panel = 'sidecar' }
  if ($p.panelPort -gt 0) { $private['panel_port'] = $p.panelPort }
  $cfgJson = @{ id = $p.id; name = $p.id; api_version = 1; entry = (Split-Path $exe -Leaf); expose_all = $false; provider_private_configs = $private } | ConvertTo-Json -Depth 5
  $cfg = Join-Path $run 'provider.json'
  [IO.File]::WriteAllText($cfg, $cfgJson, (New-Object System.Text.UTF8Encoding($false)))

  $env:PROVIDER_DIR = $run
  $env:PROVIDER_CONFIG = $cfg
  $env:PLUGIN_AUTH_TOKEN = 'ui-token'
  $out = Join-Path $run 'stdout.txt'
  $proc = Start-Process -FilePath $exe -ArgumentList '--provider-serve', '--port', '0' -WorkingDirectory $run `
    -WindowStyle Hidden -RedirectStandardOutput $out -RedirectStandardError (Join-Path $run 'stderr.txt') -PassThru
  Start-Sleep -Seconds 5

  $panel = '-'; $found = '-'; $detail = ''
  try {
    if ($proc.HasExited) { $panel = 'exited'; $detail = (Get-Content -LiteralPath $out -ErrorAction SilentlyContinue | Select-Object -First 1) }
    else {
      $line = Get-Content -LiteralPath $out -ErrorAction SilentlyContinue | Select-Object -First 1
      if ($line -match '"state"\s*:\s*"need_config"') { $panel = 'need_config' }
      elseif ($line -match '"port"\s*:\s*(\d+)') {
        $svcPort = $Matches[1]
        $target = if ($p.panelPort -gt 0) { "http://127.0.0.1:$($p.panelPort)/" } else { "http://127.0.0.1:$svcPort/" }
        $html = ''
        for ($i = 0; $i -lt 10; $i++) {
          try {
            $r = Invoke-WebRequest -Uri $target -TimeoutSec 8 -UseBasicParsing
            if ($r.StatusCode -eq 200) { $html = $r.Content; break }
          } catch { }
          Start-Sleep -Milliseconds 700
        }
        if ($html -eq '') { $panel = 'no-html'; $detail = $target }
        else {
          $panel = 'ok'
          $hit = @($p.markers | Where-Object { $html -like "*$_*" })
          $found = "$($hit.Count)/$($p.markers.Count)"
          if ($hit.Count -lt $p.markers.Count) {
            $missing = @($p.markers | Where-Object { $html -notlike "*$_*" })
            $detail = 'missing: ' + ($missing -join ',')
          }
          # 额外资源检查（如 app.js 里的 renderEvents）：面板 HTML 只有 <script src>，
          # 逻辑在独立资源里，必须单独抓一次才算验证到。
          if ($p.assets) {
            $assetPath = $p.assets[0]; $assetNeedle = $p.assets[1]
            try {
              $a = Invoke-WebRequest -Uri ($target.TrimEnd('/') + $assetPath) -Headers @{ Authorization = 'Bearer ui-token' } -TimeoutSec 8 -UseBasicParsing
              if ($a.Content -notlike "*$assetNeedle*") { $detail = ($detail + " asset-missing:$assetNeedle").Trim() }
            } catch { $detail = ($detail + " asset-err:$assetPath").Trim() }
          }
        }
      } else { $panel = 'no-ready'; $detail = $line }
    }
  } finally {
    if ($proc -and -not $proc.HasExited) { Stop-Process -Id $proc.Id -Force }
  }

  $results += [pscustomobject]@{ Plugin = $p.id; Panel = $panel; Markers = $found; Detail = $detail }
}

$results | Format-Table -AutoSize -Wrap
$bad = @($results | Where-Object { $_.Panel -eq 'build-fail' -or $_.Panel -eq 'exited' -or $_.Panel -eq 'no-html' -or $_.Panel -eq 'no-ready' -or ($_.Markers -match '^(\d+)/' -and [int]$Matches[1] -lt 2) })
if ($bad.Count -gt 0) { Write-Host ("面板检查失败 {0} 个：{1}" -f $bad.Count, (($bad | Select-Object -ExpandProperty Plugin) -join ', ')) -ForegroundColor Red; exit 1 }
Write-Host ("面板检查通过 {0} 个" -f $results.Count) -ForegroundColor Green
