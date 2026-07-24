# Build the Windows installer: dist\Vito-Setup-<version>.exe
#
# Versions are calendar-based — year.month, plus a counter for further releases
# in the same month:
#
#   2026.7      first release of July 2026
#   2026.7.1    the next one that month
#   2026.8      first release of August
#
# You can see at a glance how old a build is, and there is nothing to decide at
# release time. Without -Version the next free number for this month is used.
#
# One-time toolchain (both without admin):
#   winget install --id BrechtSanders.WinLibs.POSIX.UCRT   # mingw-w64, for cgo
#   winget install --id JRSoftware.InnoSetup               # the installer compiler
#
# Usage (from the repo root):
#   pwsh -File packaging/build-installer.ps1
#   pwsh -File packaging/build-installer.ps1 -Version 2026.7.2
#   pwsh -File packaging/build-installer.ps1 -Tag        # also create the git tag

[CmdletBinding()]
param(
    [string]$Version,
    [switch]$Tag
)

$ErrorActionPreference = "Stop"
$repo = Split-Path $PSScriptRoot -Parent
Push-Location $repo
try {
    # ---- version ----------------------------------------------------------
    if (-not $Version) {
        $now = Get-Date
        $base = "$($now.Year).$($now.Month)"
        # Existing tags for this month decide the counter: v2026.7, v2026.7.1, …
        $taken = @(git tag --list "v$base" "v$base.*" 2>$null)
        if ($taken.Count -eq 0) {
            $Version = $base
        } else {
            $highest = 0
            foreach ($t in $taken) {
                $suffix = $t -replace "^v$([regex]::Escape($base))\.?", ""
                $n = 0
                if ($suffix -eq "") { $n = 0 } elseif ([int]::TryParse($suffix, [ref]$n)) { } else { continue }
                if ($n -gt $highest) { $highest = $n }
            }
            $Version = "$base.$($highest + 1)"
        }
    }
    if ($Version -notmatch '^\d{4}\.\d{1,2}(\.\d+)?$') {
        throw "version '$Version' does not look like year.month[.n], e.g. 2026.7 or 2026.7.2"
    }
    # Windows' version resource wants four numbers; pad the display form out.
    $parts = @($Version -split '\.') + @('0', '0', '0')
    $verInfo = ($parts[0..3]) -join '.'
    Write-Host "Version: $Version (resource $verInfo)" -ForegroundColor Cyan

    # ---- exe resources: icon + version metadata --------------------------
    # Regenerate cmd\vito\rsrc_windows.syso so the compiled exe carries proper
    # VERSIONINFO (CompanyName / ProductName / FileVersion …) alongside the icon,
    # stamped with this exact version. A metadata-less unsigned binary trips
    # antivirus ML heuristics (Wacatac!ml and friends) more readily; it is also
    # simply correct for a shipped executable. The committed .syso already carries
    # this metadata for plain `go build`; here we refresh its version to match.
    $vi = Get-Content (Join-Path $PSScriptRoot "versioninfo.json") -Raw | ConvertFrom-Json
    $vi.FixedFileInfo.FileVersion.Major = [int]$parts[0]
    $vi.FixedFileInfo.FileVersion.Minor = [int]$parts[1]
    $vi.FixedFileInfo.FileVersion.Patch = [int]$parts[2]
    $vi.FixedFileInfo.FileVersion.Build = [int]$parts[3]
    $vi.FixedFileInfo.ProductVersion = $vi.FixedFileInfo.FileVersion
    $vi.StringFileInfo.FileVersion = $Version
    $vi.StringFileInfo.ProductVersion = $Version
    $viJson = Join-Path ([System.IO.Path]::GetTempPath()) "vito-versioninfo.json"
    $vi | ConvertTo-Json -Depth 8 | Set-Content $viJson -Encoding utf8
    go run github.com/josephspurrier/goversioninfo/cmd/goversioninfo@v1.4.1 `
        -64 -o (Join-Path $repo "cmd\vito\rsrc_windows.syso") `
        -icon (Join-Path $PSScriptRoot "vito.ico") $viJson
    if ($LASTEXITCODE -ne 0) { throw "goversioninfo failed" }
    Remove-Item $viJson -ErrorAction SilentlyContinue

    # ---- the binary -------------------------------------------------------
    # Built here rather than reusing dist\vito.exe, so the version stamped into
    # the installer and the one the app reports can never disagree.
    $exe = Join-Path $repo "dist\vito.exe"
    New-Item -ItemType Directory -Force (Join-Path $repo "dist") | Out-Null
    & (Join-Path $PSScriptRoot "build-windows.ps1") -Output $exe -Ldflags "-X main.version=$Version"
    if ($LASTEXITCODE -ne 0) { throw "go build failed" }

    # ---- the installer ----------------------------------------------------
    $iscc = (Get-Command iscc.exe -ErrorAction SilentlyContinue)?.Source
    if (-not $iscc) {
        foreach ($c in @("$env:ProgramFiles(x86)\Inno Setup 6\ISCC.exe",
                         "$env:ProgramFiles\Inno Setup 6\ISCC.exe",
                         "$env:LOCALAPPDATA\Programs\Inno Setup 6\ISCC.exe")) {
            if (Test-Path $c) { $iscc = $c; break }
        }
    }
    if (-not $iscc) {
        throw "Inno Setup not found. Install it with:  winget install --id JRSoftware.InnoSetup"
    }

    & $iscc "/DAppVersion=$Version" "/DVerInfo=$verInfo" (Join-Path $PSScriptRoot "vito.iss")
    if ($LASTEXITCODE -ne 0) { throw "iscc failed" }

    $setup = Join-Path $repo "dist\Vito-Setup-$Version.exe"
    if (-not (Test-Path $setup)) { throw "expected $setup" }

    # A checksum next to the installer: the in-app updater verifies what it
    # downloaded against this before running it.
    $hash = (Get-FileHash $setup -Algorithm SHA256).Hash.ToLower()
    "$hash  $(Split-Path $setup -Leaf)" | Set-Content "$setup.sha256" -Encoding ascii

    if ($Tag) {
        git tag -a "v$Version" -m "Vito $Version"
        Write-Host "Tagged v$Version (push it with: git push origin v$Version)" -ForegroundColor Green
    }

    Write-Host ""
    Write-Host "Built $setup" -ForegroundColor Green
    Write-Host "  $([math]::Round((Get-Item $setup).Length / 1MB, 1)) MB   sha256 $hash"
} finally {
    Pop-Location
}
