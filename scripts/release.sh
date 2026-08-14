#!/usr/bin/env bash
# Cut a Vito release: verify, tag and build the Linux AppImage.
# Usage:  scripts/release.sh 2026.7.3
set -euo pipefail
V="${1:?usage: scripts/release.sh <version>   e.g. 2026.7.3}"
export CGO_ENABLED=1

# 1. must be on main, clean and in sync with origin
branch=$(git rev-parse --abbrev-ref HEAD)
[ "$branch" = "main" ] || { echo "not on main (on $branch)"; exit 1; }
git diff --quiet && git diff --cached --quiet || { echo "working tree not clean"; exit 1; }
git fetch -q origin main
[ "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)" ] \
  || { echo "main is not in sync with origin/main - pull/push first"; exit 1; }

# 2. pre-flight (same gates as CI)
echo "==> checks"
bad=$(gofmt -l $(git ls-files '*.go')); [ -z "$bad" ] || { echo "gofmt needed:"; echo "$bad"; exit 1; }
go vet ./...
go build ./...
go test ./...

# 3. tag and push it
git tag -a "v$V" -m "Vito $V"
git push origin "v$V"

# 4. build this host's artifact. Each platform's package can only be built on
#    that platform — cgo needs its SDK, and the packagers (appimagetool, Inno
#    Setup, hdiutil) exist nowhere else — so a release is assembled from runs on
#    each OS, not from one machine.
case "$(uname -s)" in
  Darwin)
    bash packaging/build-macos.sh "$V"
    built="macOS artifact:  dist/Vito-$V.dmg (+ .sha256)"
    todo="  * Linux:    bash packaging/build-appimage.sh $V"
    ;;
  *)
    bash packaging/build-appimage.sh "$V"
    built="Linux artifact:  dist/Vito-$V-x86_64.AppImage (+ .sha256)"
    todo="  * macOS:    bash packaging/build-macos.sh $V"
    ;;
esac

cat <<EOF

Release v$V prepared.
  $built

Next:
$todo
  * Windows:  pwsh -File packaging/build-installer.ps1 -Version $V
  * GitHub:   Releases -> Draft a new release -> choose tag v$V ->
              "Generate release notes" -> attach the 6 files -> Publish
EOF
