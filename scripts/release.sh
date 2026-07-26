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

# 4. build the Linux artifact
bash packaging/build-appimage.sh "$V"

cat <<EOF

Release v$V prepared.
  Linux artifact: dist/Vito-$V-x86_64.AppImage (+ .sha256)

Next:
  * Windows:  pwsh -File packaging/build-installer.ps1 -Version $V
  * GitHub:   Releases -> Draft a new release -> choose tag v$V ->
              "Generate release notes" -> attach the 4 files -> Publish
EOF
