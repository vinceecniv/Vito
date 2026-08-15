#!/usr/bin/env bash
# Build dist/Vito-<version>.dmg containing Vito.app (universal: arm64 + x86_64).
#
# Run this on macOS: cgo needs the real SDK and the frameworks, and the icon,
# signature and disk image are all made with Apple's own tools (sips, iconutil,
# codesign, hdiutil), which only exist here. There is no container equivalent.
#
# The build is ad-hoc signed (`codesign --sign -`), not Developer ID signed and
# not notarised. Two consequences worth knowing, both documented in the README:
#
#   * Gatekeeper blocks the first launch of a downloaded copy. Right-click →
#     Open once (or clear the quarantine attribute) and macOS remembers it.
#   * An ad-hoc signature changes on every rebuild, and macOS keys the granted
#     Accessibility/microphone permissions to the signature. So an update means
#     granting Accessibility again — a Developer ID certificate is what fixes
#     that, not anything in this script.
#
# What is deliberately NOT bundled: nothing beyond the binary is needed. Audio
# goes through CoreAudio and text injection through CoreGraphics, both part of
# macOS, so unlike the Linux build there are no external helpers to check for.
set -euo pipefail

VERSION="${1:-dev}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="$ROOT/dist"
BUILD="$ROOT/build/macos"
APP="$BUILD/Vito.app"
BUNDLE_ID="io.github.vinceecniv.vito"
# The oldest macOS the result is expected to run on. CoreAudio, the event tap
# and UserNotifications are all far older than this; the floor is really about
# what Go and the SDK still support.
MIN_MACOS="11.0"

echo "==> building vito $VERSION"
mkdir -p "$OUT"
rm -rf "$BUILD"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"

cd "$ROOT"

# ---- universal binary --------------------------------------------------------
# Built once per architecture and merged with lipo, so one download runs native
# on both Apple Silicon and Intel. Cross-compiling the cgo half only needs the
# right -arch handed to clang; the SDK ships both slices.
build_arch() {
  local goarch="$1" cc_arch="$2" out="$3"
  echo "==> compiling darwin/$goarch"
  CGO_ENABLED=1 GOOS=darwin GOARCH="$goarch" \
    CC="clang -arch $cc_arch" \
    CGO_CFLAGS="-mmacosx-version-min=$MIN_MACOS" \
    CGO_LDFLAGS="-mmacosx-version-min=$MIN_MACOS" \
    go build -trimpath \
      -ldflags "-s -w -X main.version=$VERSION" \
      -o "$out" ./cmd/vito
}

build_arch arm64 arm64 "$BUILD/vito-arm64"
build_arch amd64 x86_64 "$BUILD/vito-amd64"
lipo -create -output "$APP/Contents/MacOS/vito" "$BUILD/vito-arm64" "$BUILD/vito-amd64"
lipo -info "$APP/Contents/MacOS/vito"

# ---- icon --------------------------------------------------------------------
# iconutil wants a directory of exact sizes. Everything is derived from the same
# 512px source the web UI uses; only the 1024px @2x entry is an upscale, which
# is invisible on the flat logo but would be worth replacing with real 1024px
# artwork if that ever exists.
echo "==> building icon"
ICONSET="$BUILD/vito.iconset"
mkdir -p "$ICONSET"
make_icon() { sips -z "$1" "$1" web/icon-512.png --out "$ICONSET/$2" >/dev/null; }
make_icon 16   icon_16x16.png
make_icon 32   icon_16x16@2x.png
make_icon 32   icon_32x32.png
make_icon 64   icon_32x32@2x.png
make_icon 128  icon_128x128.png
make_icon 256  icon_128x128@2x.png
make_icon 256  icon_256x256.png
make_icon 512  icon_256x256@2x.png
make_icon 512  icon_512x512.png
make_icon 1024 icon_512x512@2x.png
iconutil -c icns "$ICONSET" -o "$APP/Contents/Resources/vito.icns"

# ---- bundle metadata ---------------------------------------------------------
# NSMicrophoneUsageDescription is not optional: without it macOS denies the
# microphone outright rather than asking, and dictation records silence.
#
# LSUIElement keeps Vito out of the Dock and the app switcher — it lives in the
# menu bar, like the tray icon on the other platforms.
#
# The vito:// scheme is what the installed web UI uses to relaunch the daemon
# when it finds it down; registering it here is the macOS counterpart of
# registerLaunchProtocol on Windows and Linux.
cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleName</key>
	<string>Vito</string>
	<key>CFBundleDisplayName</key>
	<string>Vito</string>
	<key>CFBundleIdentifier</key>
	<string>$BUNDLE_ID</string>
	<key>CFBundleExecutable</key>
	<string>vito</string>
	<key>CFBundleIconFile</key>
	<string>vito</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>CFBundleShortVersionString</key>
	<string>$VERSION</string>
	<key>CFBundleVersion</key>
	<string>$VERSION</string>
	<key>LSMinimumSystemVersion</key>
	<string>$MIN_MACOS</string>
	<key>LSUIElement</key>
	<true/>
	<key>NSHighResolutionCapable</key>
	<true/>
	<key>NSMicrophoneUsageDescription</key>
	<string>Vito records your voice while you dictate, and sends the audio to the speech-to-text provider you configured.</string>
	<key>CFBundleURLTypes</key>
	<array>
		<dict>
			<key>CFBundleURLName</key>
			<string>Vito</string>
			<key>CFBundleURLSchemes</key>
			<array>
				<string>vito</string>
			</array>
		</dict>
	</array>
</dict>
</plist>
PLIST

printf 'APPL????' > "$APP/Contents/PkgInfo"

# ---- signature ---------------------------------------------------------------
# Prefer the self-signed identity from packaging/make-signing-cert.sh. It is not
# a Developer ID and changes nothing about Gatekeeper, but it decides what the
# permissions macOS stores get tied to. Signed ad-hoc, an Accessibility approval
# is bound to one exact binary and every update silently invalidates it; signed
# with a certificate that stays the same, the approval keeps matching. The build
# works either way — the printed requirement below is which one you got.
SIGN_ID="${VITO_SIGN_IDENTITY:-Vito Self-Signed}"
if security find-certificate -c "$SIGN_ID" >/dev/null 2>&1; then
  echo "==> signing as '$SIGN_ID'"
else
  echo "==> signing ad-hoc ('$SIGN_ID' not in the keychain)"
  echo "    Run packaging/make-signing-cert.sh to stop updates resetting permissions."
  SIGN_ID="-"
fi
codesign --force --sign "$SIGN_ID" --timestamp=none "$APP"
codesign --verify --verbose=2 "$APP"
codesign -d -r- "$APP" 2>&1 | grep designated || true

# ---- disk image --------------------------------------------------------------
# The window the user sees when they open the DMG — app on the left, an
# Applications alias on the right, an arrow between them — is not a property of
# the image. It lives in the volume's .DS_Store, so the only way to set it is to
# have Finder open the volume, arrange it, and write the result out. That means
# building a read-write image first and compressing it afterwards.
echo "==> building disk image"
VOLNAME="Vito"
DMGROOT="$BUILD/dmg"
mkdir -p "$DMGROOT/.background"
cp -R "$APP" "$DMGROOT/Vito.app"
ln -s /Applications "$DMGROOT/Applications"

# One multi-representation TIFF: Finder takes the window size in points from the
# 1x image and draws the @2x one on a Retina display. Regenerate the sources
# with `go run ./packaging/mkdmgbg` after changing the artwork.
tiffutil -cathidpicheck packaging/dmg-background.png "packaging/dmg-background@2x.png" \
  -out "$DMGROOT/.background/background.tiff" >/dev/null 2>&1

# NB: the volume icon deliberately is NOT staged here. hdiutil create
# -srcfolder silently drops .VolumeIcon.icns, so it has to be written to the
# mounted image instead — see below.

DMG="$OUT/Vito-$VERSION.dmg"
RW="$BUILD/rw.dmg"
rm -f "$DMG" "$RW"

# Leftover mount from an interrupted earlier run: styling would otherwise be
# applied to a volume called "Vito 1" while the new one goes out unstyled.
if [ -d "/Volumes/$VOLNAME" ]; then
  hdiutil detach "/Volumes/$VOLNAME" -force >/dev/null 2>&1 || true
fi

# Sized with slack rather than fitted to the source, so there is room to write
# the volume icon and the layout into the mounted image. The slack costs nothing
# in the shipped file: empty space compresses away in the UDZO conversion.
SLACK_MB=$(( $(du -sm "$DMGROOT" | cut -f1) + 20 ))
hdiutil create -volname "$VOLNAME" -srcfolder "$DMGROOT" -ov -format UDRW \
  -size "${SLACK_MB}m" "$RW" >/dev/null
hdiutil attach -readwrite -noverify -noautoopen "$RW" >/dev/null

# Arranging the window needs permission to control Finder. macOS asks for that
# the first time, and a headless machine can never grant it — but losing it
# costs only the styling, so this is a warning rather than a failed build.
if osascript packaging/dmg-layout.applescript "$VOLNAME" >/dev/null 2>&1; then
  echo "    window layout applied"
else
  echo "    note: could not style the window (Finder automation was refused)." >&2
  echo "    The disk image still works; it just opens as a plain folder." >&2
fi

# Give the mounted volume the app's own icon instead of a generic disk. Both
# halves are needed: the icns file, and the "has custom icon" flag on the volume
# root that tells Finder to look at it.
#
# This has to happen *after* the layout step, not before: applying the window
# settings makes Finder rewrite the volume's Finder info, and doing so deletes
# .VolumeIcon.icns and clears the flag again. Staging the file into the source
# folder does not work either — hdiutil create -srcfolder drops it.
cp "$APP/Contents/Resources/vito.icns" "/Volumes/$VOLNAME/.VolumeIcon.icns"
SetFile -a C "/Volumes/$VOLNAME"

sync
hdiutil detach "/Volumes/$VOLNAME" >/dev/null 2>&1 ||
  hdiutil detach "/Volumes/$VOLNAME" -force >/dev/null
hdiutil convert "$RW" -format UDZO -imagekey zlib-level=9 -o "$DMG" >/dev/null
rm -f "$RW"

# Give the .dmg file itself the app's icon in Finder rather than the generic
# disk-image one. A *file's* custom icon lives in its resource fork — the
# .VolumeIcon.icns above only covers the volume once mounted — so this goes
# through the old Rez toolchain: sips turns the icns into an icon resource,
# DeRez extracts it, Rez appends it to the image, and the flag tells Finder to
# look. It has to run before the checksum, since it modifies the file.
cp "$APP/Contents/Resources/vito.icns" "$BUILD/dmgicon.icns"
sips -i "$BUILD/dmgicon.icns" >/dev/null
DeRez -only icns "$BUILD/dmgicon.icns" > "$BUILD/dmgicon.rsrc"
Rez -append "$BUILD/dmgicon.rsrc" -o "$DMG"
SetFile -a C "$DMG"

cd "$OUT"
shasum -a 256 "Vito-$VERSION.dmg" > "Vito-$VERSION.dmg.sha256"
echo "==> built $DMG"
