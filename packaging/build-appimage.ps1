# Build the Linux AppImage from Windows, using Docker.
#
# The container is the point: it fixes the glibc the binary links against.
# Debian bookworm gives glibc 2.36, so the result runs on anything from roughly
# Ubuntu 22.10 / Debian 12 onwards. Build on something newer and it would refuse
# to start on older systems, because glibc is only forwards compatible.
#
# Usage (from the repo root):
#   pwsh -File packaging/build-appimage.ps1
#   pwsh -File packaging/build-appimage.ps1 -Version 2026.7

[CmdletBinding()]
param(
    [string]$Version = "dev",
    [string]$Image = "golang:1.26-bookworm"
)

$ErrorActionPreference = "Stop"
$repo = (Resolve-Path (Split-Path $PSScriptRoot -Parent)).Path

docker version --format '{{.Server.Os}}' *> $null
if ($LASTEXITCODE -ne 0) { throw "Docker is not running — start Docker Desktop and try again." }

Write-Host "Building Vito $Version for Linux in $Image" -ForegroundColor Cyan

# gcc for cgo (malgo/miniaudio), curl to fetch appimagetool, file because
# appimagetool shells out to it.
$script = @'
set -e
apt-get update -qq
apt-get install -y -qq --no-install-recommends gcc libc6-dev curl ca-certificates file >/dev/null
git config --global --add safe.directory /src
cd /src
bash packaging/build-appimage.sh "$VITO_VERSION"
'@ -replace "`r`n", "`n"

docker run --rm `
    -v "${repo}:/src" `
    -e "VITO_VERSION=$Version" `
    -e "GOFLAGS=-buildvcs=false" `
    -w /src $Image bash -c $script
if ($LASTEXITCODE -ne 0) { throw "AppImage build failed" }

$out = Join-Path $repo "dist\Vito-$Version-x86_64.AppImage"
if (Test-Path $out) {
    Write-Host ""
    Write-Host "Built $out" -ForegroundColor Green
    Write-Host "  $([math]::Round((Get-Item $out).Length / 1MB, 1)) MB"
}
