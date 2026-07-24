# PostToolUse hook: rebuild dist\vito.exe after Claude edits a source file, and
# restart the running daemon. The UI lives inside the binary (web/web.go embeds
# index.html), so without a rebuild + restart an edit is invisible in the PWA.
$ErrorActionPreference = 'Stop'

$repo = if ($env:CLAUDE_PROJECT_DIR) { $env:CLAUDE_PROJECT_DIR } else { Split-Path (Split-Path $PSScriptRoot) }

$raw = [Console]::In.ReadToEnd()
try { $in = $raw | ConvertFrom-Json } catch { exit 0 }

$f = $in.tool_input.file_path
if (-not $f) { exit 0 }
if ($f -notmatch '\.(go|html|js|css|webmanifest|mod|sum)$') { exit 0 }
if (-not $f.StartsWith($repo, [System.StringComparison]::OrdinalIgnoreCase)) { exit 0 }

$out = & go build -o (Join-Path $repo 'dist\vito.exe') (Join-Path $repo 'cmd\vito') 2>&1
if ($LASTEXITCODE -ne 0) {
  # Exit 2 feeds the compiler output back to Claude so it can fix the break.
  [Console]::Error.WriteLine("go build failed:`n$out")
  exit 2
}

# Only restart if a daemon was already running — never start one unasked.
# Match on the executable path, not the process name: go build renames the
# running binary out of the way (dist\vito.exe~), which changes the name
# Get-Process reports and would leave a stale daemon serving the old UI.
$exe = Join-Path $repo 'dist\vito.exe'
$running = Get-Process -ErrorAction SilentlyContinue |
  Where-Object { $_.Path -and ($_.Path -eq $exe -or $_.Path -eq "$exe~") }
if ($running) {
  $running | Stop-Process -Force
  Start-Sleep -Milliseconds 400
  Remove-Item "$exe~" -Force -ErrorAction SilentlyContinue # the renamed-away old build
  Start-Process -FilePath $exe -ArgumentList 'serve' -WindowStyle Hidden
  $msg = 'Vito herbouwd + daemon herstart — ververs de PWA (Ctrl+R)'
} else {
  $msg = 'Vito herbouwd (daemon draaide niet)'
}

@{ systemMessage = $msg; suppressOutput = $true } | ConvertTo-Json -Compress
