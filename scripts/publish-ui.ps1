# Builds the signed interface bundle into the website repository, where daemons
# fetch it from (https://vito.talk/ui/, see internal/uibundle).
#
#   pwsh -File scripts/publish-ui.ps1                 # build + commit in ../Vito-Web
#   pwsh -File scripts/publish-ui.ps1 -Push           # ...and push: live within minutes
#
# Every daemon whose update check is on picks it up within six hours, so publish
# from a commit that is on main. The signing key is read from
# ~/.vito-signing/ui-ed25519.key (or VITO_UI_KEY); it never enters a repository.
param(
  [string]$Site = (Join-Path $PSScriptRoot "..\..\Vito-Web"),
  [string]$Version = "",
  [switch]$Push
)
$ErrorActionPreference = "Stop"
$repo = Resolve-Path (Join-Path $PSScriptRoot "..")
$Site = Resolve-Path $Site
$out = Join-Path $Site "ui"

Push-Location $repo
try {
  $dirty = git status --porcelain -- web
  if ($dirty) { throw "web/ has uncommitted changes; publish from a clean tree" }
  $commit = (git rev-parse --short=12 HEAD).Trim()
  $goArgs = @("run", "./packaging/uibundle", "build", "-out", $out)
  if ($Version) { $goArgs += @("-version", $Version) }
  & go @goArgs
  if ($LASTEXITCODE -ne 0) { throw "uibundle build failed" }
} finally { Pop-Location }

Push-Location $Site
try {
  $ver = (Get-Content (Join-Path $out "ui.json") -Raw | ConvertFrom-Json).version
  git add -A -- ui
  git commit -m "Publish interface $ver (vito $commit)"
  if ($LASTEXITCODE -ne 0) { throw "commit failed" }
  if ($Push) { git push; if ($LASTEXITCODE -ne 0) { throw "push failed" } }
  else { Write-Host "Committed in $Site; push it to publish (or re-run with -Push)." }
} finally { Pop-Location }
