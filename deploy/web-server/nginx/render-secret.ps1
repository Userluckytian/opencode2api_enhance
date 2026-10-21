# 渲染 nginx 反代共享密钥片段（Windows 版）。
# 用法：$env:TOKEN_BUFFET_PROXY_SECRET='xxxx'; powershell -File render-secret.ps1 -Out 'D:\nginx\conf\snippets\token-buffet-proxy-secret.conf'
#
# 为什么不用 nginx 的 env 变量：nginx 的 `env` 指令只把变量继承给 worker 进程，
# 不能在配置里当 $变量 引用（实测 nginx -t 报 unknown "xxx" variable）。
param(
  [string]$Out = 'D:\nginx\conf\snippets\token-buffet-proxy-secret.conf',
  [string]$Secret = $env:TOKEN_BUFFET_PROXY_SECRET
)
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($Secret)) {
  throw '错误：请通过环境变量 TOKEN_BUFFET_PROXY_SECRET 传入密钥'
}
if ($Secret -notmatch '^[A-Za-z0-9._-]{16,128}$') {
  throw '错误：密钥格式非法（只允许 16~128 位字母数字 . _ -）'
}
$dir = Split-Path -Parent $Out
if (-not (Test-Path $dir)) { New-Item -ItemType Directory -Force -Path $dir | Out-Null }
$body = @"
# 由 render-secret.ps1 生成，请勿提交到版本库。生成时间：$(Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')
proxy_set_header X-Proxy-Secret "$Secret";
"@
[System.IO.File]::WriteAllText($Out, $body, (New-Object System.Text.UTF8Encoding($false)))
Write-Host "[OK] 已写入 $Out"