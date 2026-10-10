#!/usr/bin/env bash
# Build dist/Vito-<version>-x86_64.AppImage.
#
# Run this inside Linux. From Windows, packaging/build-appimage.ps1 does that for
# you with Docker; the container also pins the glibc floor, which is the thing
# that decides which distributions the result will run on. cgo links against
# glibc and glibc is only forwards compatible, so building on something old is
# what makes it work on something new — not the other way round.
#
# What is deliberately NOT bundled:
#
#   * ALSA/PulseAudio client libraries. miniaudio dlopens them at runtime, so
#     there is no link-time dependency, and a bundled sound client that doesn't
#     match the host's sound server is a classic source of silent failures.
#   * ydotool, wl-clipboard and friends. They need a daemon and a udev rule for
#     /dev/uinput, so shipping copies would not save anyone the install. Vito's
#     settings page checks for them and says what is missing.
set -euo pipefail

VERSION="${1:-dev}"
ARCH="${ARCH:-x86_64}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="$ROOT/dist"
APPDIR="$ROOT/build/AppDir"

echo "==> building vito $VERSION"
mkdir -p "$OUT" "$ROOT/build"
rm -rf "$APPDIR"
mkdir -p "$APPDIR/usr/bin" "$APPDIR/usr/share/applications" \
         "$APPDIR/usr/share/icons/hicolor/512x512/apps" "$APPDIR/usr/share/metainfo"

cd "$ROOT"
CGO_ENABLED=1 go build -trimpath \
  -ldflags "-s -w -X main.version=$VERSION" \
  -o "$APPDIR/usr/bin/vito" ./cmd/vito

# ---- AppDir contents ---------------------------------------------------------
# X-AppImage-Version is where AppImage tools (AppImageLauncher, Gear Lever,
# appimaged) read the version from — without it the version lives only in the
# file name, and is gone once the file is renamed to Vito.AppImage.
cp packaging/vito.desktop "$APPDIR/vito.desktop"
echo "X-AppImage-Version=$VERSION" >> "$APPDIR/vito.desktop"
cp "$APPDIR/vito.desktop" "$APPDIR/usr/share/applications/vito.desktop"
cp web/icon-512.png "$APPDIR/usr/share/icons/hicolor/512x512/apps/vito.png"
cp web/icon-512.png "$APPDIR/vito.png"
# The metainfo filename must match the component id (io.github.vinceecniv.vito),
# or appstreamcli — which appimagetool runs when present — fails validation.
cp packaging/io.github.vinceecniv.vito.metainfo.xml "$APPDIR/usr/share/metainfo/io.github.vinceecniv.vito.metainfo.xml"
# ...and name this release in it, which is where software centres look.
sh packaging/stamp-metainfo.sh "$APPDIR/usr/share/metainfo/io.github.vinceecniv.vito.metainfo.xml" "$VERSION"

# AppRun passes its arguments straight through, so every subcommand works:
#   ./Vito.AppImage serve | toggle | status | quit
# With none, it runs the daemon — that is what launching it from a desktop does.
cat > "$APPDIR/AppRun" <<'APPRUN'
#!/bin/sh
HERE="$(dirname "$(readlink -f "$0")")"
export PATH="$HERE/usr/bin:$PATH"
if [ $# -eq 0 ]; then
  exec "$HERE/usr/bin/vito" serve
fi
exec "$HERE/usr/bin/vito" "$@"
APPRUN
chmod +x "$APPDIR/AppRun"

# ---- appimagetool ------------------------------------------------------------
TOOL="$ROOT/build/appimagetool-$ARCH.AppImage"
if [ ! -x "$TOOL" ]; then
  echo "==> fetching appimagetool"
  curl -fsSL -o "$TOOL" \
    "https://github.com/AppImage/appimagetool/releases/download/continuous/appimagetool-$ARCH.AppImage"
  chmod +x "$TOOL"
fi

# Update information (the AppImage spec's .upd_info section) tells
# AppImageUpdate, Gear Lever and AppImageLauncher where newer releases live,
# and lets them fetch only the changed blocks through the .zsync file that
# appimagetool writes beside the image when zsyncmake is installed. Vito still
# updates itself (internal/update); this is for the tools people manage their
# AppImages with. Only release builds get it: a dev build pointing at
# "latest" would offer to replace itself with something else.
UPDATE=()
if printf '%s' "$VERSION" | grep -Eq '^[0-9]{4}\.[0-9]{1,2}(\.[0-9]+)?$' && [ "$VERSION" != 0000.0 ]; then
  UPDATE=(-u "gh-releases-zsync|vinceecniv|Vito|latest|Vito-*-$ARCH.AppImage.zsync")
  command -v zsyncmake >/dev/null || echo "    note: zsyncmake not found; no .zsync is written (install zsync)"
fi

# --appimage-extract-and-run: containers and minimal images have no FUSE, and
# the tool is an AppImage itself. VERSION is read by appimagetool too.
export ARCH VERSION
rm -f "$OUT/Vito-$VERSION-$ARCH.AppImage.zsync"
(cd "$OUT" && "$TOOL" --appimage-extract-and-run "${UPDATE[@]}" "$APPDIR" "Vito-$VERSION-$ARCH.AppImage")

cd "$OUT"
sha256sum "Vito-$VERSION-$ARCH.AppImage" > "Vito-$VERSION-$ARCH.AppImage.sha256"
echo "==> built $OUT/Vito-$VERSION-$ARCH.AppImage"
