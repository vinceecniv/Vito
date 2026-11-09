#!/usr/bin/env bash
# Which systems a release is built for: all three, unless said otherwise.
#
#   release-platforms.sh [list] [tag]
#
# list is a run by hand's own choice ("windows", "windows,macos"). Without it,
# the annotated tag's message may name them in a line of its own:
#
#   git tag -a v2026.12 -m "Vito 2026.12" -m "Platforms: windows"
#
# and without that line it is every system. The updater follows what a release
# carries (internal/update, PlatformAsset): a system left out sees no update.
#
# Writes windows=, linux=, macos= (true/false) and platforms= to $GITHUB_OUTPUT
# when it is set, and prints them either way.
set -euo pipefail

list="${1:-}"
tag="${2:-}"
if [ -z "$list" ] && [ -n "$tag" ]; then
  # A checkout leaves a tag lightweight; fetch the tag object for its message.
  git fetch --quiet --force origin "refs/tags/$tag:refs/tags/$tag" 2>/dev/null || true
  line=$(git for-each-ref "refs/tags/$tag" --format='%(contents)' |
    grep -iE '^[[:space:]]*platforms[[:space:]]*:' | tail -n1 || true)
  list="${line#*:}"
fi
list=$(printf '%s' "${list:-windows linux macos}" | tr 'A-Z,' 'a-z ')

windows=false linux=false macos=false
for p in $list; do
  case "$p" in
    windows) windows=true ;;
    linux) linux=true ;;
    macos | mac) macos=true ;;
    *) echo "::error::unknown platform '$p' (use windows, linux, macos)"; exit 1 ;;
  esac
done
if [ "$windows$linux$macos" = falsefalsefalse ]; then
  echo "::error::no platform to build"
  exit 1
fi

platforms=""
for p in windows linux macos; do
  [ "${!p}" = true ] && platforms="$platforms $p"
done
out="windows=$windows
linux=$linux
macos=$macos
platforms=${platforms# }"
echo "$out"
if [ -n "${GITHUB_OUTPUT:-}" ]; then
  echo "$out" >> "$GITHUB_OUTPUT"
fi
