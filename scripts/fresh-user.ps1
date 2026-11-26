# Starts the dev build as a brand-new user: an empty profile (config, history,
# achievements, dictionary, cache) in %TEMP%\vito-fresh, on its own port, next
# to the installed Vito, which is not touched. Each run starts from scratch, on
# a new port: the page keeps things in the browser per address (localStorage),
# so a new address is a browser that has never seen Vito either.
#
#   pwsh -File scripts/fresh-user.ps1          # rebuild, wipe, start, open the browser
#   pwsh -File scripts/fresh-user.ps1 -Keep    # start again with the profile as it was
#   pwsh -File scripts/fresh-user.ps1 -Stop    # stop it (the profile stays until the next run)
#
# The config written holds only the port and token. Without any config file the
# daemon would take this for a first install and point autostart at the dev
# build; everything else is left to the defaults, welcome_done included, so the
# page shows what a new user sees.
#
# The fresh daemon registers the default dictation key too. If the installed
# Vito uses the same key, both answer it: quit the installed one from its tray
# icon while you test, and start it again afterwards.
param([switch]$Stop, [switch]$Keep, [int]$Port = 0)
$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent
$dir = Join-Path $env:TEMP 'vito-fresh'
$exe = Join-Path $root 'dist\vito.exe'

function Stop-Fresh {
  Get-Process vito -ErrorAction SilentlyContinue | Where-Object { $_.Path -eq $exe } | Stop-Process -Force
}

Stop-Fresh
if ($Stop) { 'Stopped.'; return }

if (-not $Keep) {
  # Rebuild through the hook, which also restarts a daemon running from dist;
  # that one is stopped again below.
  '{"tool_input":{"file_path":"' + ($root -replace '\\', '\\') + '\\web\\index.html"}}' |
    pwsh -NoProfile -File (Join-Path $root '.claude\hooks\rebuild.ps1') | Out-Null
  Stop-Fresh
  if (Test-Path $dir) { Remove-Item -Recurse -Force $dir }
}
New-Item -ItemType Directory -Force (Join-Path $dir 'roaming\vito'), (Join-Path $dir 'local') | Out-Null
$cfgPath = Join-Path $dir 'roaming\vito\config.json'
if (-not (Test-Path $cfgPath)) {
  $token = -join ((48..57) + (97..122) | Get-Random -Count 24 | ForEach-Object { [char]$_ })
  if (-not $Port) { $Port = Get-Random -Minimum 45000 -Maximum 46000 }
  @{ server = @{ port = $Port; token = $token } } | ConvertTo-Json | Set-Content -Encoding utf8NoBOM $cfgPath
}
$token = (Get-Content $cfgPath | ConvertFrom-Json).server.token
$Port = (Get-Content $cfgPath | ConvertFrom-Json).server.port

$env:APPDATA = Join-Path $dir 'roaming'
$env:LOCALAPPDATA = Join-Path $dir 'local'
Start-Process -FilePath $exe -ArgumentList 'serve' -WindowStyle Hidden
$url = "http://127.0.0.1:$Port/?token=$token"
for ($i = 0; $i -lt 50; $i++) {
  try { Invoke-WebRequest $url -UseBasicParsing -TimeoutSec 2 | Out-Null; break } catch { Start-Sleep -Milliseconds 200 }
}
Start-Process $url
"Fresh Vito on $url"
"Profile: $dir   (stop with: pwsh -File scripts/fresh-user.ps1 -Stop)"
