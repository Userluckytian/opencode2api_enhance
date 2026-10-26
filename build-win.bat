@echo off
setlocal EnableExtensions

set "ROOT=%~dp0"
if "%ROOT:~-1%"=="\" set "ROOT=%ROOT:~0,-1%"
set "TARGET_TRIPLE=x86_64-pc-windows-msvc"
set "TARGET_DIR=%ROOT%\src-tauri\target-win-direct"
set "LOG_FILE=%TEMP%\opencode2api-build-win.log"

> "%LOG_FILE%" echo [%date% %time%] Windows build started
echo Windows build log: %LOG_FILE%
echo Project root: %ROOT%
echo Target directory: %TARGET_DIR%

call :try_init_msvc

where cargo >nul 2>&1
if errorlevel 1 (
  echo ERROR: cargo was not found in the Windows environment.
  >> "%LOG_FILE%" echo ERROR=cargo-not-found
  endlocal & exit /b 1
)

rustup target list --installed 2>nul | findstr /c:"%TARGET_TRIPLE%" >nul
if errorlevel 1 (
  echo ERROR: Rust target %TARGET_TRIPLE% is not installed.
  >> "%LOG_FILE%" echo ERROR=rust-target-not-found
  endlocal & exit /b 1
)

cd /d "%ROOT%\src-tauri"
if errorlevel 1 (
  echo ERROR: cannot enter src-tauri.
  >> "%LOG_FILE%" echo ERROR=src-tauri-not-found
  endlocal & exit /b 1
)

if not exist "%ROOT%\bin\free-sidecar.exe" (
  echo Building free-tier Bun sidecar...
  powershell -NoProfile -ExecutionPolicy Bypass -File "%ROOT%\scripts\build-free-sidecar.ps1"
  if errorlevel 1 echo WARNING: free sidecar build failed; app will fall back to legacy free-tier path
)

echo Building Windows release executable...
cargo build --release --target "%TARGET_TRIPLE%" --target-dir "%TARGET_DIR%"
set "BUILD_RC=%ERRORLEVEL%"
>> "%LOG_FILE%" echo BUILD_RC=%BUILD_RC%

if exist "%TARGET_DIR%\%TARGET_TRIPLE%\release\opencode2api.exe" (
  echo EXE_PRODUCED=YES
  >> "%LOG_FILE%" echo EXE_PRODUCED=YES
) else (
  echo EXE_PRODUCED=NO
  >> "%LOG_FILE%" echo EXE_PRODUCED=NO
)

endlocal & exit /b %BUILD_RC%

:try_init_msvc
set "VSWHERE="
if exist "%ProgramFiles(x86)%\Microsoft Visual Studio\Installer\vswhere.exe" set "VSWHERE=%ProgramFiles(x86)%\Microsoft Visual Studio\Installer\vswhere.exe"
if not defined VSWHERE if exist "%ProgramFiles%\Microsoft Visual Studio\Installer\vswhere.exe" set "VSWHERE=%ProgramFiles%\Microsoft Visual Studio\Installer\vswhere.exe"

set "VSINSTALL="
if defined VSWHERE (
  for /f "usebackq delims=" %%I in (`"%VSWHERE%" -latest -products * -requires Microsoft.VisualStudio.Component.VC.Tools.x86.x64 -property installationPath`) do if not defined VSINSTALL set "VSINSTALL=%%I"
)

if defined VSINSTALL if exist "%VSINSTALL%\Common7\Tools\VsDevCmd.bat" (
  call "%VSINSTALL%\Common7\Tools\VsDevCmd.bat" -arch=x64 -host_arch=x64 >nul
  if not errorlevel 1 echo MSVC environment initialized from %VSINSTALL%
)
exit /b 0
