# Build the OpenCode free-tier Bun sidecar (Windows).
# Output: bin/free-sidecar.exe (~100MB, not committed).
#
# Usage:
#   powershell -ExecutionPolicy Bypass -File scripts/build-free-sidecar.ps1
#   powershell -ExecutionPolicy Bypass -File scripts/build-free-sidecar.ps1 -BunVersion 1.4.4
param(
  [string]$BunVersion = "1.4.4"
)

$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot
Set-Location $Root

$BunExe = $null
$cmd = Get-Command bun -ErrorAction SilentlyContinue
if ($cmd) {
  $BunExe = $cmd.Source
} else {
  $Cache = Join-Path $Root ".scratch/bun"
  $BunExe = Join-Path $Cache "bun-windows-x64/bun.exe"
  if (-not (Test-Path $BunExe)) {
    New-Item -ItemType Directory -Force -Path $Cache | Out-Null
    $Zip = Join-Path $Cache "bun.zip"
    $Urls = @(
      "https://github.com/oven-sh/bun/releases/download/bun-v$BunVersion/bun-windows-x64.zip",
      "https://github.com/oven-sh/bun/releases/latest/download/bun-windows-x64.zip"
    )
    $ok = $false
    foreach ($Url in $Urls) {
      try {
        Write-Host "downloading $Url"
        Invoke-WebRequest -Uri $Url -OutFile $Zip -UseBasicParsing
        $ok = $true
        break
      } catch {
        Write-Warning "download failed: $Url"
      }
    }
    if (-not $ok) { throw "Cannot download Bun; install bun or place .scratch/bun/bun-windows-x64/bun.exe manually" }
    Expand-Archive -Force -Path $Zip -DestinationPath $Cache
  }
}

if (-not (Test-Path $BunExe)) { throw "Bun not found: $BunExe" }
New-Item -ItemType Directory -Force -Path (Join-Path $Root "bin") | Out-Null
$Out = Join-Path $Root "bin/free-sidecar.exe"
Write-Host "building $Out"
& $BunExe build --compile (Join-Path $Root "sidecar/free-sidecar.mjs") --outfile $Out
if (-not (Test-Path $Out)) { throw "sidecar build failed: $Out missing" }
$Size = [math]::Round((Get-Item $Out).Length / 1MB, 1)
Write-Host "built $Out ($Size MB)"
