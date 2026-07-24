# Build vito.exe on Windows.
#
# vito depends on malgo (miniaudio), which uses cgo, so a C compiler is
# required — the pure `go build` you may be used to is not enough. This script
# locates a mingw-w64 gcc, enables cgo, and builds a release binary.
#
# One-time toolchain install (pick one):
#   winget install --id BrechtSanders.WinLibs.POSIX.UCRT   # mingw-w64 (no admin)
#   choco install mingw                                     # needs admin
#   or unzip w64devkit from https://github.com/skeeto/w64devkit/releases
#
# Usage (from the repo root):
#   pwsh -File packaging/build-windows.ps1
#   pwsh -File packaging/build-windows.ps1 -Output dist/vito.exe

param(
    [string]$Output = "vito.exe",
    # Extra linker flags, e.g. "-X main.version=2026.7" from build-installer.ps1.
    [string]$Ldflags = ""
)

$ErrorActionPreference = "Stop"

# Find a gcc: PATH first, then the common winget/w64devkit install locations.
$gcc = (Get-Command gcc -ErrorAction SilentlyContinue)?.Source
if (-not $gcc) {
    $candidates = @(
        "$env:LOCALAPPDATA\Microsoft\WinGet\Links\gcc.exe",
        "$env:LOCALAPPDATA\Programs\mingw64\bin\gcc.exe",
        "C:\mingw64\bin\gcc.exe",
        "C:\w64devkit\bin\gcc.exe",
        "C:\ProgramData\chocolatey\bin\gcc.exe"
    )
    # winget installs winlibs under Packages\<id>\mingw64\bin without a PATH shim.
    $candidates += (Get-ChildItem -Path "$env:LOCALAPPDATA\Microsoft\WinGet\Packages" `
        -Filter gcc.exe -Recurse -ErrorAction SilentlyContinue |
        Where-Object { $_.FullName -match 'mingw64\\bin\\gcc.exe$' } |
        Select-Object -ExpandProperty FullName)
    foreach ($c in $candidates) {
        if ($c -and (Test-Path $c)) { $gcc = $c; break }
    }
}
if (-not $gcc) {
    Write-Error @"
No C compiler (gcc) found. vito needs cgo for audio (malgo/miniaudio).
Install mingw-w64, e.g.:
  winget install --id BrechtSanders.WinLibs.POSIX.UCRT
then open a new shell and re-run this script.
"@
    exit 1
}

Write-Host "Using C compiler: $gcc"
$env:CGO_ENABLED = "1"
$env:CC = $gcc
$env:GOOS = "windows"
$env:GOARCH = "amd64"

# -H windowsgui would hide the console; keep it so `vito serve` logs are
# visible. -s -w strips debug info for a smaller binary.
go build -trimpath -ldflags "-s -w $Ldflags" -o $Output ./cmd/vito
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
Write-Host "Built $Output"
