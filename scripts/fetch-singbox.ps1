<#
.SYNOPSIS
  Download sing-box for Windows into bin\sing-box.exe (needed before packaging).

.DESCRIPTION
  scripts/fetch-singbox.sh builds a .tar.gz URL for every platform, but SagerNet
  only publishes .zip for Windows -- so the .sh script 404s on Windows hosts.
  This is the Windows-native equivalent: it fetches the .zip release asset.

  bin\ is git-ignored, so a fresh clone has no sing-box and
  scripts\prepare-portable.ps1 aborts with "bin\sing-box.exe missing".

.PARAMETER Version
  sing-box release tag without the leading "v". Defaults to the version pinned
  in the CI matrix (.github/workflows/build-release.yml) so local and CI builds
  embed the same binary.

.EXAMPLE
  powershell -ExecutionPolicy Bypass -File scripts\fetch-singbox.ps1
  powershell -ExecutionPolicy Bypass -File scripts\fetch-singbox.ps1 -Version 1.13.16

.NOTES
  ASCII only: PowerShell 5.1 parses no-BOM UTF-8 as ANSI.
#>
param(
    [string]$Version = "",
    [string]$Arch = "amd64"
)

$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot
$Bin = Join-Path $Root "bin"

# Pin default to the CI matrix so local == CI. Keep this in sync with
# .github/workflows/build-release.yml (sb_url).
if (-not $Version) {
    $Version = "1.13.16"
    $wf = Join-Path $Root ".github\workflows\build-release.yml"
    if (Test-Path $wf) {
        $m = [regex]::Match((Get-Content -Raw $wf), 'sing-box-([0-9.]+)-windows')
        if ($m.Success) { $Version = $m.Groups[1].Value }
    }
}

$asset = "sing-box-$Version-windows-$Arch.zip"
$url = "https://github.com/SagerNet/sing-box/releases/download/v$Version/$asset"

Write-Host "== fetch sing-box =="
Write-Host "  version : $Version"
Write-Host "  asset   : $asset"
Write-Host "  target  : $Bin\sing-box.exe"

if (-not (Test-Path $Bin)) { New-Item -ItemType Directory -Force -Path $Bin | Out-Null }
$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("sb-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Force -Path $tmp | Out-Null

try {
    $zip = Join-Path $tmp "sb.zip"
    $ok = $false
    # Direct first, then mirrors: GitHub release downloads flake in CN networks.
    foreach ($base in @("", "https://gh-proxy.com/", "https://ghfast.top/")) {
        $u = "$base$url"
        Write-Host "  trying  : $u"
        try {
            & curl.exe -fsSL --retry 3 --retry-all-errors --retry-delay 2 --max-time 300 -o $zip $u
            if ($LASTEXITCODE -eq 0 -and (Test-Path $zip) -and (Get-Item $zip).Length -gt 0) { $ok = $true; break }
        } catch {
            Write-Host "            failed: $($_.Exception.Message)"
        }
    }
    if (-not $ok) { throw "download failed for all mirrors: $url" }

    Expand-Archive -Path $zip -DestinationPath $tmp -Force
    $found = Get-ChildItem -Path $tmp -Recurse -Filter "sing-box.exe" -ErrorAction SilentlyContinue |
             Select-Object -First 1
    if (-not $found) { throw "sing-box.exe not found inside $asset" }

    Copy-Item $found.FullName (Join-Path $Bin "sing-box.exe") -Force
    $size = (Get-Item (Join-Path $Bin "sing-box.exe")).Length
    Write-Host ""
    Write-Host "== OK =="
    Write-Host "  wrote  : $Bin\sing-box.exe ($size bytes)"
} finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
