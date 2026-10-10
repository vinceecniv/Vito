#!/bin/sh
# Stamp the version being built into an AppStream metainfo file:
#
#   sh packaging/stamp-metainfo.sh <metainfo.xml> <version> [yyyy-mm-dd]
#
# AppStream's <releases> list is where software centres, GNOME Software, Gear
# Lever and `flatpak info` read an app's version from, so it has to name the
# release in hand. Keeping it current by hand failed (it said 2026.7 for
# months); the AppImage and Flatpak builds call this on their copy instead,
# adding <release version date> as the newest entry unless it is already there.
#
# Only real versions (year.month[.n]) are stamped: a dev or test build keeps
# the list as committed. sh and GNU sed only — it also runs inside the
# Flatpak build sandbox.
set -eu
file="${1:?usage: stamp-metainfo.sh <metainfo.xml> <version> [date]}"
version="${2:?usage: stamp-metainfo.sh <metainfo.xml> <version> [date]}"
date="${3:-$(date -u +%Y-%m-%d)}"

if ! printf '%s' "$version" | grep -Eq '^[0-9]{4}\.[0-9]{1,2}(\.[0-9]+)?$' || [ "$version" = 0000.0 ]; then
  echo "stamp-metainfo: '$version' is not a release version; leaving $file as is"
  exit 0
fi
if grep -q "<release version=\"$version\"" "$file"; then
  exit 0
fi
grep -q '<releases>' "$file" || { echo "stamp-metainfo: no <releases> in $file" >&2; exit 1; }
sed -i "s|^\([[:space:]]*\)<releases>|&\n\1  <release version=\"$version\" date=\"$date\"/>|" "$file"
echo "stamp-metainfo: $file now names $version ($date)"
