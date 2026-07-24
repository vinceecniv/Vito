# Lists the UI strings that are missing from the translation files.
#
# The inline English table in web/index.html (`TR = { en: {...} }`) is the
# canonical set of translatable strings: its keys are the Dutch source strings
# the code looks up, its values the English translation. A language file is
# complete when it has every one of those keys.
#
# Usage (pwsh works on Windows, Linux and macOS), from the repo root:
#   pwsh -File scripts/i18n-missing.ps1              # summary per language
#   pwsh -File scripts/i18n-missing.ps1 -Export      # + write missing-keys.json
#   pwsh -File scripts/i18n-missing.ps1 -Lang de     # one language, list the keys
#
# -Export writes missing-keys.json (English source string -> itself) next to this
# script; that file is what the translation subagents read. See
# web/i18n/TRANSLATING.md for the whole procedure.

param(
    [string]$Lang = "",
    [switch]$Export
)

$ErrorActionPreference = "Stop"
$root = Split-Path $PSScriptRoot -Parent

# Pull the Dutch table out of index.html and parse its "key":"value" pairs.
$html = Get-Content (Join-Path $root "web/index.html") -Raw
$start = $html.IndexOf('TR = { nl:')
$end = $html.IndexOf('}, en: {} };', $start)
if ($start -lt 0 -or $end -lt 0) { throw "could not find the TR.nl table in web/index.html" }
$block = $html.Substring($start, $end - $start)

$src = [ordered]@{}
foreach ($m in [regex]::Matches($block, '"((?:[^"\\]|\\.)*)"\s*:\s*"((?:[^"\\]|\\.)*)"')) {
    $src[$m.Groups[1].Value -replace '\\"', '"'] = $m.Groups[1].Value -replace '\\"', '"'
}

$files = Get-ChildItem (Join-Path $root "web/i18n/*.json")
if ($Lang) { $files = $files | Where-Object BaseName -eq $Lang }

$allMissing = [ordered]@{}
$rows = foreach ($f in $files) {
    try { $j = Get-Content $f.FullName -Raw | ConvertFrom-Json -AsHashtable }
    catch { [pscustomobject]@{ lang = $f.BaseName; missing = -1; note = "INVALID JSON" }; continue }

    $missing = @($src.Keys | Where-Object { -not $j.strings.ContainsKey($_) })
    foreach ($k in $missing) { $allMissing[$k] = $src[$k] }
    [pscustomobject]@{ lang = $f.BaseName; strings = $j.strings.Count; missing = $missing.Count; note = "" }

    if ($Lang -and $missing.Count) {
        Write-Host "`nMissing in $($f.BaseName):" -ForegroundColor Yellow
        $missing | ForEach-Object { Write-Host "  $_" }
    }
}

$rows | Sort-Object missing -Descending | Format-Table -AutoSize
Write-Host ("source strings: {0}   files: {1}   incomplete: {2}" -f
    $src.Count, @($rows).Count, @($rows | Where-Object missing -ne 0).Count)

if ($Export) {
    $out = Join-Path $PSScriptRoot "missing-keys.json"
    $allMissing | ConvertTo-Json -Depth 3 | Set-Content $out -Encoding UTF8
    Write-Host "wrote $($allMissing.Count) keys to $out" -ForegroundColor Green
}
