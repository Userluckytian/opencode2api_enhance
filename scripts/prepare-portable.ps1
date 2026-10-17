# prepare-portable.ps1
# ============================================================
# One-command portable test package builder for opencode2api.
# The portable build runs on the isolated 48200 slot and its own
# data dir, so it never touches the production install (40000 slot).
#
# Usage:
#   powershell -ExecutionPolicy Bypass -File scripts\prepare-portable.ps1
#   powershell ... -OutDir "D:\tmp\oc-beta"
#   powershell ... -SkipCoreBuild          # reuse existing Go core binary
#   powershell ... -SkipRustBuild          # reuse existing release shell exe
#
# Prerequisites:
#   - go on PATH          (builds the core)
#   - cargo + MSVC        (builds the shell; script bootstraps MSVC env itself)
#   - bin\sing-box.exe    (checked into CI, NOT in git; fetch one first --
#                          scripts\fetch-singbox.ps1, or the .zip from the
#                          sing-box release page for windows-amd64)
#   - bin\dist\           (run: npm run build)
#
# Output layout (portable contract; Go core locates frontend via
# os.Executable() -> exe-adjacent bin\dist):
#   <OutDir>/
#   |-- opencode2api.exe     # Rust shell (embeds core + sing-box; releases bin/ on run)
#   |-- WebView2Loader.dll   # required by tauri runtime (NOT auto-copied for bare exe)
#   |-- portable.txt         # empty marker -> 48200 slot + isolated data dir
#   `-- bin/
#       `-- dist/            # latest frontend build (served by core)
#
# Isolation guarantees:
#   - admin:  http://127.0.0.1:48200   (gateway 48280 / instances 48300+)
#   - data:   %APPDATA%\opencode2api-manager-test (independent from production)
#   - remove: delete <OutDir> to uninstall; data dir may be removed manually.
#
# NOTE: PowerShell 5.1 reads UTF-8 files without BOM as ANSI; keep this
#       file ASCII-only (no non-ASCII comments) to stay parsing-safe.
#       $ErrorActionPreference is NOT set to Stop: external commands
#       (cargo/go) writing to stderr would then abort the script.
# ============================================================

param(
    [string]$OutDir = "",
    [switch]$SkipCoreBuild,
    [switch]$SkipRustBuild,
    # Empty = read "version" from package.json (single source of truth).
    # Do NOT hardcode a default here: it silently drifts from package.json.
    [string]$Version = ""
)

# ---- MSVC bootstrap helpers (ASCII only: PS 5.1 reads no-BOM UTF-8 as ANSI) ----
# cargo's *-pc-windows-msvc target needs link.exe on PATH. VsDevCmd.bat is the
# canonical entry point, but some VS installs ship the toolset WITHOUT vcvars
# scripts -- there VsDevCmd prints its banner, exits 0, and sets nothing. So we
# try VsDevCmd first, then assemble PATH/INCLUDE/LIB by hand from disk.
# cl.exe is required alongside link.exe: Go ships its own link.exe and a bare
# "Get-Command link.exe" could match it.

function Test-MsvcLinker {
    $l = Get-Command link.exe -ErrorAction SilentlyContinue
    $c = Get-Command cl.exe   -ErrorAction SilentlyContinue
    if (-not $l -or -not $c) { return $false }
    return ($l.Source -match 'Visual Studio|MSVC')
}

function Get-EnvMapFromVsDevCmd {
    param([string]$VsDevCmd)
    $line = 'call "' + $VsDevCmd + '" -arch=x64 -host_arch=x64 >nul 2>&1 & set'
    $raw = & cmd.exe /c $line 2>$null
    if (-not $raw) { return $null }
    $map = @{}
    foreach ($row in $raw) {
        $i = $row.IndexOf('=')
        if ($i -gt 0) { $map[$row.Substring(0, $i)] = $row.Substring($i + 1) }
    }
    return $map
}

function Get-VsInstallPaths {
    $out = @()
    $vswhere = Join-Path ${env:ProgramFiles(x86)} 'Microsoft Visual Studio\Installer\vswhere.exe'
    if (Test-Path $vswhere) {
        # No -requires filter: on toolset-only installs it returns nothing even
        # though a usable MSVC tree is present (observed on VS 18 BuildTools).
        $found = & $vswhere -latest -products * -property installationPath 2>$null
        foreach ($f in $found) { if ($f -and (Test-Path $f)) { $out += $f } }
    }
    foreach ($base in @(${env:ProgramFiles(x86)}, $env:ProgramFiles)) {
        if (-not $base) { continue }
        $vsRoot = Join-Path $base 'Microsoft Visual Studio'
        if (-not (Test-Path $vsRoot)) { continue }
        foreach ($ed in (Get-ChildItem -Path $vsRoot -Directory -ErrorAction SilentlyContinue)) {
            foreach ($prod in (Get-ChildItem -Path $ed.FullName -Directory -ErrorAction SilentlyContinue)) {
                if (Test-Path (Join-Path $prod.FullName 'VC\Tools\MSVC')) { $out += $prod.FullName }
            }
        }
    }
    return ($out | Select-Object -Unique)
}

function Find-MsvcToolset {
    $best = $null
    foreach ($inst in (Get-VsInstallPaths)) {
        $msvcRoot = Join-Path $inst 'VC\Tools\MSVC'
        if (-not (Test-Path $msvcRoot)) { continue }
        foreach ($v in (Get-ChildItem -Path $msvcRoot -Directory -ErrorAction SilentlyContinue)) {
            $bin = Join-Path $v.FullName 'bin\Hostx64\x64'
            if (-not (Test-Path (Join-Path $bin 'link.exe'))) { continue }
            if (-not (Test-Path (Join-Path $bin 'cl.exe')))   { continue }
            $ver = $null
            try { $ver = [version]$v.Name } catch { $ver = $null }
            if (-not $best -or ($ver -and (-not $best.Ver -or $ver -gt $best.Ver))) {
                $best = @{ Dir = $v.FullName; Bin = $bin; Ver = $ver; Name = $v.Name }
            }
        }
    }
    return $best
}

function Find-WindowsSdk {
    foreach ($base in @(${env:ProgramFiles(x86)}, $env:ProgramFiles)) {
        if (-not $base) { continue }
        $libRoot = Join-Path $base 'Windows Kits\10\Lib'
        if (-not (Test-Path $libRoot)) { continue }
        $best = $null
        foreach ($d in (Get-ChildItem -Path $libRoot -Directory -ErrorAction SilentlyContinue)) {
            if (-not (Test-Path (Join-Path $d.FullName 'ucrt\x64'))) { continue }
            if (-not (Test-Path (Join-Path $d.FullName 'um\x64')))   { continue }
            $ver = $null
            try { $ver = [version]$d.Name } catch { $ver = $null }
            if (-not $best -or ($ver -and (-not $best.Ver -or $ver -gt $best.Ver))) {
                $best = @{ Dir = $d.FullName; Ver = $ver; Name = $d.Name }
            }
        }
        if ($best) { return $best }
    }
    return $null
}

function Initialize-MsvcEnvironment {
    if (Test-MsvcLinker) {
        Write-Host "      MSVC already available on PATH"
        return $true
    }

    # 1) canonical path: VsDevCmd.bat (works when vcvars scripts are installed)
    foreach ($inst in (Get-VsInstallPaths)) {
        $vsdev = Join-Path $inst 'Common7\Tools\VsDevCmd.bat'
        if (-not (Test-Path $vsdev)) { continue }
        $map = Get-EnvMapFromVsDevCmd $vsdev
        if (-not $map) { continue }
        if (-not $map['VCToolsInstallDir']) { continue }
        foreach ($k in @('PATH', 'INCLUDE', 'LIB', 'VCToolsInstallDir', 'WindowsSdkDir', 'WindowsSDKVersion')) {
            if ($map.ContainsKey($k)) { Set-Item -Path ("env:" + $k) -Value $map[$k] }
        }
        if (Test-MsvcLinker) {
            Write-Host "      MSVC env via VsDevCmd: $inst"
            return $true
        }
    }

    # 2) fallback: build the environment from disk (toolset-only installs)
    $toolset = Find-MsvcToolset
    if (-not $toolset) {
        Write-Host "      ERROR: no MSVC toolset with link.exe/cl.exe found" -ForegroundColor Red
        Write-Host "      install 'Desktop development with C++' in Visual Studio Installer" -ForegroundColor Red
        return $false
    }
    $sdk = Find-WindowsSdk
    if (-not $sdk) {
        Write-Host "      ERROR: Windows 10/11 SDK Lib not found" -ForegroundColor Red
        return $false
    }

    $parts = @($toolset.Bin)
    $sdkBin = Join-Path $sdk.Dir 'bin\x64'
    if (Test-Path $sdkBin) { $parts += $sdkBin }
    if ($env:PATH) { $parts += $env:PATH }
    $env:PATH = ($parts -join ';')

    $env:INCLUDE = (@(
        (Join-Path $toolset.Dir 'include'),
        (Join-Path $sdk.Dir 'ucrt'),
        (Join-Path $sdk.Dir 'um'),
        (Join-Path $sdk.Dir 'shared'),
        (Join-Path $sdk.Dir 'winrt'),
        (Join-Path $sdk.Dir 'cppwinrt')
    ) | Where-Object { Test-Path $_ }) -join ';'

    $env:LIB = (@(
        (Join-Path $toolset.Dir 'lib\x64'),
        (Join-Path $sdk.Dir 'ucrt\x64'),
        (Join-Path $sdk.Dir 'um\x64')
    ) | Where-Object { Test-Path $_ }) -join ';'

    if (-not (Test-MsvcLinker)) {
        Write-Host "      ERROR: MSVC env assembled but link.exe still not resolvable" -ForegroundColor Red
        return $false
    }
    Write-Host "      MSVC env assembled from disk: $($toolset.Name) / SDK $($sdk.Name)"
    return $true
}

$Root = Split-Path -Parent $PSScriptRoot   # repo root
Set-Location $Root

if (-not $OutDir) { $OutDir = Join-Path $Root "portable-out" }
$OutDir = [System.IO.Path]::GetFullPath($OutDir)

Write-Host "== opencode2api portable packager =="
Write-Host "  repo   : $Root"
Write-Host "  output : $OutDir"

# ---- 1. Build latest Go core into bin\opencode2api.exe (embedded by Rust shell) ----
if (-not $SkipCoreBuild) {
    Write-Host "[1/4] building Go core ..."

    # Version: package.json is the single source of truth. The old hardcoded
    # "v1.3.0" default silently drifted (shipped 1.7.6-beta stamped as 1.3.0).
    if (-not $Version) {
        $pkgPath = Join-Path $Root "package.json"
        if (-not (Test-Path $pkgPath)) { throw "package.json not found (needed for -Version)" }
        $Version = (Get-Content -Raw -Encoding UTF8 $pkgPath | ConvertFrom-Json).version
        if (-not $Version) { throw "package.json has no 'version' field" }
    }

    # Commit / branch: report a real warning instead of silently using "unknown".
    # A silent "unknown" once shipped a package with no traceable revision.
    $commit = "unknown"
    $branch = "unknown"
    $gitOut = & git rev-parse --short HEAD 2>$null
    if ($LASTEXITCODE -eq 0 -and $gitOut) {
        $commit = ($gitOut | Out-String).Trim()
        $brOut = & git rev-parse --abbrev-ref HEAD 2>$null
        if ($LASTEXITCODE -eq 0 -and $brOut) { $branch = ($brOut | Out-String).Trim() }
    } else {
        Write-Warning "git metadata unavailable; stamping commit=unknown (is git on PATH?)"
    }

    $date = Get-Date -Format "yyyy-MM-dd"
    $ldflags = "-X main.version=$Version -X main.commit=$commit -X main.date=$date -X main.branch=$branch"
    go build -ldflags $ldflags -o bin\opencode2api.exe .
    if ($LASTEXITCODE -ne 0) { throw "Go core build FAILED" }
    Write-Host "      core ok: $Version @ $commit ($branch)"
} else {
    Write-Host "[1/4] skip Go core build (reuse bin\opencode2api.exe)"
}
if (-not (Test-Path bin\opencode2api.exe)) { throw "bin\opencode2api.exe missing" }
if (-not (Test-Path bin\sing-box.exe))     { throw "bin\sing-box.exe missing (place sing-box prebuilt)" }

# ---- 2. Build Rust shell (release; embeds latest core + sing-box) ----
if (-not $SkipRustBuild) {
    Write-Host "[2/4] building Rust shell (cargo release) ..."

    # cargo needs link.exe (MSVC). Fail early with an actionable message
    # instead of a cryptic "linker `link.exe` not found" deep in the build.
    if (-not (Initialize-MsvcEnvironment)) { throw "MSVC toolchain not usable (install Desktop development with C++)" }

    # tauri.conf.json lists target/release/WebView2Loader.dll as a bundle
    # resource, so it must exist BEFORE cargo build -- the old script copied
    # it in step 3 and the build failed with "resource path ... doesn't exist".
    $dllSrc = Join-Path $Root "src-tauri\WebView2Loader.dll"
    $dllDstDir = Join-Path $Root "src-tauri\target\release"
    if (-not (Test-Path $dllSrc)) { throw "src-tauri\WebView2Loader.dll missing (checked-in runtime DLL)" }
    if (-not (Test-Path $dllDstDir)) { New-Item -ItemType Directory -Force -Path $dllDstDir | Out-Null }
    Copy-Item $dllSrc (Join-Path $dllDstDir "WebView2Loader.dll") -Force

    Push-Location src-tauri
    cargo build --release
    if ($LASTEXITCODE -ne 0) { Pop-Location; throw "Rust shell build FAILED" }
    Pop-Location
} else {
    Write-Host "[2/4] skip Rust shell build (reuse target\release)"
}
$releaseExe = "src-tauri\target\release\opencode2api.exe"
if (-not (Test-Path $releaseExe)) { throw "release exe missing: $releaseExe" }

# ---- 3. Assemble portable folder ----
Write-Host "[3/4] assembling portable folder ..."
if (Test-Path $OutDir) {
    # Running the packaged exe once releases bin\opencode2api.exe + bin\sing-box.exe
    # next to it (ensure_binaries). Those can be locked by a still-running process,
    # and Remove-Item only WARNS on failure -- the stale files then survive into the
    # new package. Clear it explicitly and fail loudly if anything is still locked.
    try {
        Remove-Item -Recurse -Force $OutDir -ErrorAction Stop
    } catch {
        $locked = Get-Process -Name "opencode2api" -ErrorAction SilentlyContinue
        $hint = if ($locked) { " (stop running opencode2api.exe first, PID: " + (($locked | ForEach-Object { $_.Id }) -join ', ') + ")" } else { "" }
        throw ("cannot clear output dir $OutDir" + $hint + ": " + $_.Exception.Message)
    }
    if (Test-Path $OutDir) { throw "output dir still present after cleanup: $OutDir" }
}
New-Item -ItemType Directory -Force -Path (Join-Path $OutDir "bin") | Out-Null

Copy-Item $releaseExe (Join-Path $OutDir "opencode2api.exe") -Force

$dll = "src-tauri\target\release\WebView2Loader.dll"
if (-not (Test-Path $dll)) {
    # Only reachable with -SkipRustBuild and no prior successful build.
    $dllFallback = Join-Path $Root "src-tauri\WebView2Loader.dll"
    if (-not (Test-Path $dllFallback)) { throw "WebView2Loader.dll missing (src-tauri\WebView2Loader.dll not found)" }
    $dll = $dllFallback
}
Copy-Item $dll (Join-Path $OutDir "WebView2Loader.dll") -Force

New-Item -ItemType File -Path (Join-Path $OutDir "portable.txt") -Force | Out-Null

if (Test-Path (Join-Path $Root "dist\index.html")) {
    Copy-Item -Recurse -Force (Join-Path $Root "dist") (Join-Path $OutDir "bin\dist")
} else {
    throw "dist\index.html missing (run: npm run build)"
}

# ---- 4. Verify ----
Write-Host "[4/4] verifying ..."
$checks = @(
    "opencode2api.exe",
    "WebView2Loader.dll",
    "portable.txt",
    "bin\dist\index.html"
)
foreach ($c in $checks) {
    if (-not (Test-Path (Join-Path $OutDir $c))) { throw "verification FAILED: $c" }
}

Write-Host ""
Write-Host "== PORTABLE PACK READY =="
Write-Host "  dir     : $OutDir"
Write-Host "  admin   : http://127.0.0.1:48200"
Write-Host "  gateway : http://127.0.0.1:48280/v1 (starts when pool has running members)"
Write-Host "  data    : $env:APPDATA\opencode2api-manager-test (isolated)"
Write-Host "  start   : double-click $OutDir\opencode2api.exe"
Write-Host "  remove  : delete $OutDir (and data dir above if desired)"