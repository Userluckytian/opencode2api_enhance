# E4: can the engine switch WARP OFF without a process restart?
#   start 40188 with warp.enabled=true + anonymous=true (webui 40189)
#   confirm /healthz has a warp block
#   flip disk config to warp.enabled=false + anonymous=false
#   POST /api/config/reload -> expect applied:true restart_required:false
#   re-check /healthz: warp block gone, keys.anonymous=false
#   send 3 requests: expect 200 via 7890 proxy + key
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$dir = Join-Path $env:TEMP 'oc-research\e4'
New-Item -ItemType Directory -Force -Path $dir | Out-Null
$bin = 'D:\Program Files\opencode2api\bin\opencode2api-win.exe'
# throwaway credential for this ephemeral loopback-only instance; never put a real secret here
$adminPass = -join ((48..57) + (97..122) | Get-Random -Count 24 | ForEach-Object { [char]$_ })
$base = Get-Content (Join-Path $env:TEMP 'oc-research\e1\config.json') -Raw | ConvertFrom-Json
$base.listen = '127.0.0.1:40188'
$base.anonymous = $true
$base.warp = [pscustomobject]@{ enabled = $true; pool_size = 1; stack = 'ipv6' }
$base.webui = [pscustomobject]@{ enabled = $true; listen = '127.0.0.1:40189'; username = 'admin'; password = $adminPass; session_ttl_minutes = 60 }
$cfgPath = Join-Path $dir 'config.json'
$base | ConvertTo-Json -Depth 10 | Set-Content $cfgPath -Encoding UTF8

$p = Start-Process -FilePath $bin -ArgumentList @('-config', $cfgPath) -WorkingDirectory $dir -WindowStyle Hidden -PassThru `
    -RedirectStandardOutput (Join-Path $dir 'stdout.log') -RedirectStandardError (Join-Path $dir 'stderr.log')
Write-Output "log: engine pid=$($p.Id) listen=40188 webui=40189 warp=ON"

$ready = $false
for ($i = 0; $i -lt 200; $i++) {
    Start-Sleep -Seconds 1
    if (Get-NetTCPConnection -LocalPort 40188 -State Listen -ErrorAction SilentlyContinue) { $ready = $true; Write-Output "log: listening after $($i+1)s"; break }
}
if (-not $ready) { Write-Output 'FAIL: engine never listened within 200s'; Stop-Process -Id $p.Id -Force; exit 1 }

$h1 = Invoke-RestMethod -Uri 'http://127.0.0.1:40188/healthz' -TimeoutSec 10
Write-Output "warp-before: present=$($null -ne $h1.warp) status=$($h1.warp.status) active=$($h1.warp.active) keys.anonymous=$($h1.keys.anonymous)"

$cfg = Get-Content $cfgPath -Raw | ConvertFrom-Json
$cfg.warp.enabled = $false
$cfg.anonymous = $false
$cfg | ConvertTo-Json -Depth 10 | Set-Content $cfgPath -Encoding UTF8
Write-Output 'log: disk config flipped to warp.enabled=false anonymous=false'

$login = Invoke-RestMethod -Uri 'http://127.0.0.1:40189/api/auth/login' -Method Post -ContentType 'application/json' `
    -Body (@{ username = 'admin'; password = $adminPass } | ConvertTo-Json) -SessionVariable sess -TimeoutSec 15
$res = Invoke-RestMethod -Uri 'http://127.0.0.1:40189/api/config/reload' -Method Post -Headers @{ 'X-CSRF-Token' = $login.csrf_token } -WebSession $sess -TimeoutSec 120
Write-Output "log: reload applied=$($res.applied) restart_required=$($res.restart_required) fields=$($res.restart_fields -join ',')"

Start-Sleep -Seconds 2
$h2 = Invoke-RestMethod -Uri 'http://127.0.0.1:40188/healthz' -TimeoutSec 10
Write-Output "warp-after: present=$($null -ne $h2.warp) keys.anonymous=$($h2.keys.anonymous) keys.zen=$($h2.keys.zen) status=$($h2.status)"

for ($i = 1; $i -le 3; $i++) {
    $body = @{ model = 'beam-free'; input = "e4 test $i"; stream = $false } | ConvertTo-Json -Compress
    $t0 = Get-Date
    try {
        $r = Invoke-WebRequest -UseBasicParsing -Uri 'http://127.0.0.1:40188/v1/responses' -Method Post `
            -Headers @{ Authorization = 'Bearer sk-unified-local' } -ContentType 'application/json' -Body $body -TimeoutSec 90
        Write-Output "#$i HTTP $($r.StatusCode) $([int]((Get-Date) - $t0).TotalMilliseconds)ms"
    } catch {
        $ms = [int]((Get-Date) - $t0).TotalMilliseconds
        Write-Output "#$i ERR ${ms}ms $($_.Exception.Message)"
        if ($_.ErrorDetails) { Write-Output $_.ErrorDetails.Message.Substring(0, [Math]::Min(250, $_.ErrorDetails.Message.Length)) }
    }
}

Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue
Write-Output 'log: engine killed, done'
