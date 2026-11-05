# verify-events-e2e.ps1 — 事件日志端到端（真进程 → 触发动作 → 读 API）
#
# 只验证「写盘 + API 读回」这条链路真的通，不依赖上游账号：
#   - 触发动作选的是**不需要上游成功**的管理端点（同步/刷新），它们无论如何都会落事件。
#
# 用法：pwsh -File scripts/verify-events-e2e.ps1

[CmdletBinding()]
param(
  [string]$WorkRoot = 'D:\AI_Projects\_plugin-work',
  [string]$SmokeRoot = 'D:\AI_Projects\_plugin-work\smoke'
)

$ErrorActionPreference = 'Stop'
New-Item -ItemType Directory -Force -Path $SmokeRoot | Out-Null

# trigger：一次「必然写事件」的调用；eventsApi：读事件的端点；expect：事件 type 里应出现的片段
$cases = @(
  @{ id = 'mmxproxy'; dir = "$WorkRoot\phase3\mmxproxy"; build = '.'; panelPort = 18993;
     # MiniMax 的管理 API 在独立面板端口上（契约端口只有 /v1/models、/v1/chat、/v1/health）
     trigger = @{ method = 'POST'; path = '/v1/accounts/sync'; body = $null };
     eventsApi = '/v1/events?lines=50'; expect = 'quota.sync' },
  @{ id = 'qw2api'; dir = "$WorkRoot\phase3\qw2api"; build = './cmd/qw2api-plugin';
     eventsApi = '/api/events?lines=50'; expect = ''
     # qw2api 的 events 只由 /api/* 动作触发；空池下 refresh 仍会记录一次调用
     trigger = @{ method = 'POST'; path = '/api/accounts/refresh'; body = $null } },
  @{ id = 'lobsterhub'; dir = "$WorkRoot\phase5\lobsterhub"; build = '.';
     # disable 一个不存在的 uid：ToggleAccount 找不到账号、不写事件，所以这里
     # 用 clear-suspend 也不行（同样要求账号存在）。先建一个最小 vault 记录，
     # 再调 disable——只有这样才能真触发 account.disable。
     seedVault = $true;
     trigger = @{ method = 'POST'; path = '/api/accounts/1/disable'; body = $null };
     eventsApi = '/api/events?lines=20'; expect = 'account.disable' },
  @{ id = 'buddyhub'; dir = "$WorkRoot\phase5\buddyhub"; build = './cmd/pluginbuddy';
     # RefreshCredits 已在 cmd/pluginbuddy 注入；空池下调用写一条 credits.refresh。
     trigger = @{ method = 'POST'; path = '/api/credits/refresh'; body = $null };
     eventsApi = '/api/events?lines=20'; expect = 'credits.refresh' },
  @{ id = 'lobsterai'; dir = "$WorkRoot\phase5\lobsterai"; build = './cmd/server';
     # /admin/refresh 已注入 RefreshCredits（cmd/server 传 sch.RunCheckinNow），
     # 空池下调用仍会写一条 credits.refresh 事件。
     trigger = @{ method = 'POST'; path = '/admin/refresh'; body = $null };
     eventsApi = '/admin/events?lines=20'; expect = 'credits.refresh' }
)

$results = @()

foreach ($c in $cases) {
  $run = Join-Path $SmokeRoot ("ev-" + $c.id)
  if (Test-Path -LiteralPath $run) { Remove-Item -LiteralPath $run -Recurse -Force }
  New-Item -ItemType Directory -Force -Path $run | Out-Null

  $exe = Join-Path $run ($c.id + '.exe')
  Push-Location $c.dir
  try { & go build -o $exe $c.build 2>&1 | Out-Null; $bc = $LASTEXITCODE } finally { Pop-Location }
  if ($bc -ne 0) { $results += [pscustomobject]@{ Plugin = $c.id; Ready = 'build-fail'; Trigger = '-'; Read = '-'; Note = '' }; continue }

  $private = @{ panel = 'sidecar' }
  if ($c.panelPort -gt 0) { $private['panel_port'] = $c.panelPort }
  $cfgJson = @{ id = $c.id; name = $c.id; api_version = 1; entry = (Split-Path $exe -Leaf); expose_all = $false; provider_private_configs = $private } | ConvertTo-Json -Depth 5
  $cfg = Join-Path $run 'provider.json'
  [IO.File]::WriteAllText($cfg, $cfgJson, (New-Object System.Text.UTF8Encoding($false)))

  # 预置一个最小 vault 记录（LobsterHub 的 enable/disable 必须先有账号才写事件）
  if ($c.seedVault) {
    $dataDir = Join-Path $run 'data'
    New-Item -ItemType Directory -Force -Path $dataDir | Out-Null
    $seed = @(@{ userId = 1; label = 'smoke'; email = 'smoke@example.com'; enabled = $true; addedAt = '2026-01-01T00:00:00Z' }) | ConvertTo-Json -Depth 5
    [IO.File]::WriteAllText((Join-Path $dataDir 'vault.json'), $seed, (New-Object System.Text.UTF8Encoding($false)))
  }

  $env:PROVIDER_DIR = $run
  $env:PROVIDER_CONFIG = $cfg
  $env:PLUGIN_AUTH_TOKEN = 'ev-token'
  $out = Join-Path $run 'stdout.txt'
  $proc = Start-Process -FilePath $exe -ArgumentList '--provider-serve', '--port', '0' -WorkingDirectory $run `
    -RedirectStandardOutput $out -RedirectStandardError (Join-Path $run 'stderr.txt') -PassThru
  Start-Sleep -Seconds 5

  $ready = '-'; $trig = '-'; $read = '-'; $note = ''
  try {
    if ($proc.HasExited) { $ready = 'exited'; $note = (Get-Content -LiteralPath $out -ErrorAction SilentlyContinue | Select-Object -First 1) }
    else {
      $line = Get-Content -LiteralPath $out -ErrorAction SilentlyContinue | Select-Object -First 1
      if ($line -match '"state"\s*:\s*"need_config"') { $ready = 'need_config' }
      elseif ($line -match '"port"\s*:\s*(\d+)') {
        $ready = 'yes'
        # 面板类端点走面板端口（MiniMax 的 /v1/accounts、/v1/events 都在面板上）
        $base = if ($c.panelPort -gt 0) { "http://127.0.0.1:$($c.panelPort)" } else { "http://127.0.0.1:$($Matches[1])" }
        $h = @{ Authorization = 'Bearer ev-token'; 'Content-Type' = 'application/json' }

        if ($c.trigger) {
          try {
            Invoke-WebRequest -Uri ($base + $c.trigger.path) -Method $c.trigger.method -Headers $h -TimeoutSec 20 -UseBasicParsing | Out-Null
            $trig = 'ok'
          } catch { $trig = 'ERR ' + [int]$_.Exception.Response.StatusCode }
        } else { $trig = 'skip(readonly)' }

        Start-Sleep -Seconds 1
        try {
          $r = Invoke-WebRequest -Uri ($base + $c.eventsApi) -Headers $h -TimeoutSec 15 -UseBasicParsing
          $j = $r.Content | ConvertFrom-Json
          # 各插件返回的都是「已格式化的一行一条」（events 或 lines 数组），
          # 不是原始 JSON 对象，所以按数组长度计数，不能数 "ts" 出现次数。
          $arr = @()
          if ($j.PSObject.Properties.Name -contains 'events') { $arr = @($j.events) }
          elseif ($j.PSObject.Properties.Name -contains 'lines') { $arr = @($j.lines) }
          $n = @($arr | Where-Object { $_ -ne $null }).Count
          $read = "$($r.StatusCode) events=$n"
          if ($c.expect -and $r.Content -notlike "*$($c.expect)*") { $read += " missing:$($c.expect)" }
        } catch { $read = 'ERR ' + [int]$_.Exception.Response.StatusCode }
      } else { $ready = 'no-ready'; $note = $line }
    }
  } finally {
    if ($proc -and -not $proc.HasExited) { Stop-Process -Id $proc.Id -Force }
  }

  # 落盘核对：data/events.jsonl 是否存在（readOnly 的插件不写事件，允许不存在）
  $evFile = Join-Path $run 'data\events.jsonl'
  if (-not $c.readOnly -and -not (Test-Path -LiteralPath $evFile)) { $read = ($read + ' no-file').Trim() }

  $results += [pscustomobject]@{ Plugin = $c.id; Ready = $ready; Trigger = $trig; Read = $read; Note = $note }
}

$results | Format-Table -AutoSize -Wrap
$bad = @($results | Where-Object { $_.Ready -eq 'exited' -or $_.Ready -eq 'no-ready' -or $_.Ready -eq 'build-fail' -or $_.Read -like 'ERR*' -or $_.Read -like '*missing*' -or $_.Read -like '*no-file*' -or $_.Trigger -like 'ERR*' })
if ($bad.Count -gt 0) {
  Write-Host ("事件链路失败 {0} 个：{1}" -f $bad.Count, (($bad | Select-Object -ExpandProperty Plugin) -join ', ')) -ForegroundColor Red
  exit 1
}
Write-Host ("事件链路通过 {0} 个" -f $results.Count) -ForegroundColor Green
