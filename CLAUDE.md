# Vito — notes for Claude Code

Vito is a personal voice-dictation daemon in Go with an embedded web UI.

## The UI is embedded in the binary

`web/web.go` uses `go:embed`, so editing `web/index.html` changes nothing until
the binary is rebuilt and the daemon restarted. That is automated: a PostToolUse
hook on `Write|Edit` (`.claude/settings.json` → `.claude/hooks/rebuild.ps1`)
rebuilds `dist/vito.exe` and restarts the daemon after every edit. If the UI
seems not to change, check the hook ran — don't debug the CSS first.

The user usually views the app as an installed PWA, which caches aggressively.

## Translations

The interface ships in 60 languages. English is the source language: the code
calls `t("<English string>")` and that English string *is* the lookup key. That
includes strings the Go side hands to the UI (the chart labels in
`internal/history/stats.go`).

**Every new user-facing string needs an entry in the inline `TR.nl` table in
`web/index.html`, in the same edit** — English key, Dutch translation —
including `title`, `aria-label` and `data-tip-text` attributes. That table is
the contract against which all 58 language files are checked; a string missing
from it can never be translated.

To fill the other languages, follow **`web/i18n/TRANSLATING.md`**: export the
work order with `pwsh -File scripts/i18n-missing.ps1 -Export`, run one subagent
per language (batches of at most 20) with the brief in
`scripts/TRANSLATION-BRIEF.md`, then verify with `pwsh -File
scripts/i18n-missing.ps1` — every language must report `missing 0`.

## Local speech recognition

Two ways to keep audio on the machine, both in `internal/stt` as the `openai`
provider (any endpoint speaking `POST /audio/transcriptions`):

- **Vito local** (`stt.provider = "local"`, `internal/localstt`): Vito downloads
  a pinned release of mudler's parakeet.cpp `parakeet-server` (one static
  binary per OS/arch/variant) plus the Parakeet TDT 0.6B v3 q8_0 model into
  `<UserCacheDir>/vito/local-stt/`, verifies both against hashes pinned in
  `localstt.go`, and runs it as a subprocess on a free localhost port. The
  daemon rewrites the provider to `openai` with that port (`resolveSTT`), so
  the transcription code has one path. Bumping the release means re-pinning
  every asset digest (GitHub's release API carries them) and the model's LFS
  sha256. A warm-up request runs before the engine counts as `running`: on a
  Vulkan build the first request compiles shaders and takes ~30 s. Language is
  detected by the model, keyterms are ignored, uploads must be WAV.
- **Bring-your-own endpoint** (`stt.provider = "openai"`): whisper-server,
  Speaches, LocalAI, Groq, OpenAI. For whisper.cpp Vito sends `audio_ctx` sized
  to the recording (halves CPU time — Whisper always encodes 30 s otherwise),
  falls back to `/inference` on a 404, and joins whisper.cpp's newline-separated
  segments back into one line.

Measured on this machine (i9-10920X, 12 threads): Parakeet ≈ 1 s per dictation
on CPU, 0.25 s on Vulkan; Whisper large-v3-turbo ≈ 4–9 s on CPU, 0.4 s on CUDA.
Whisper is the better engine for Dutch with English jargon; Parakeet for CPU.
Benchmark only on an idle machine — a background encode inflated numbers 2×.

## Releases (Windows)

Versions are calendar-based: `year.month`, plus a counter for further releases
in the same month — `2026.7`, then `2026.7.1`, `2026.7.2`, then `2026.8`. The
number is stamped into the binary at link time (`-X main.version=…`), so `vito
version`, the About page and the installer can never disagree.

```sh
pwsh -File packaging/build-installer.ps1            # next free number this month
pwsh -File packaging/build-installer.ps1 -Version 2026.7.2 -Tag
```

That produces `dist/Vito-Setup-<version>.exe` and a `.sha256` beside it. One-time
tooling, both installable without admin:
`winget install --id BrechtSanders.WinLibs.POSIX.UCRT` (mingw-w64, needed for
cgo) and `winget install --id JRSoftware.InnoSetup`.

The installer is per-user (no UAC), silent-capable
(`/VERYSILENT /SUPPRESSMSGBOXES /NORESTART`), and asks a running Vito to `quit`
before replacing its files. `packaging/vito.ico` is generated from the app icon
with `go run ./packaging/mkicon`; `cmd/vito/rsrc_windows.syso` embeds that icon
**and** the exe's VERSIONINFO (CompanyName/ProductName/FileVersion…, from
`packaging/versioninfo.json`) — proper metadata makes an unsigned binary trip
antivirus ML heuristics (Wacatac!ml) less. `build-installer.ps1` regenerates the
`.syso` with the exact release version on every build; to refresh the committed
one by hand:
`go run github.com/josephspurrier/goversioninfo/cmd/goversioninfo@v1.4.1 -64 -o cmd/vito/rsrc_windows.syso -icon packaging/vito.ico packaging/versioninfo.json`.
The build is not code-signed yet; when a certificate is available, wire
`signtool` into `build-installer.ps1` (that is the real fix for AV false
positives — metadata only softens the heuristic).

## Releases (Linux)

```sh
pwsh -File packaging/build-appimage.ps1 -Version 2026.7   # from Windows, via Docker
bash packaging/build-appimage.sh 2026.7                    # from Linux
```

The container is not a convenience, it is the compatibility contract: cgo links
against glibc and glibc is only forwards compatible, so the build image decides
the oldest system the result runs on. `golang:1.26-bookworm` puts the floor at
glibc 2.34 — Ubuntu 22.04, Debian 12, Fedora 35 and newer. Lower it by building
on an older image; raise it by accident and older distributions stop working.

Nothing is bundled beyond the binary. miniaudio dlopens ALSA/PulseAudio at
runtime, so there is no link-time audio dependency (`ldd` shows only libc and
libm), and the external helpers are checked for by the settings page rather than
shipped — copies would save nobody the install.

There is also a **Flatpak**, published to a self-hosted repository on GitHub
Pages by `.github/workflows/flatpak.yml` on every `v*` tag. Pages must be set to
"GitHub Actions" as its source and its `github-pages` environment must allow
`v*` tags to deploy, or the workflow builds and then fails at publish. See
`docs/flatpak.md`.

**Both the hotkey and text delivery go through the XDG portals first**, which is
what removed the `ydotoold` + `/dev/uinput` setup for every Linux user, sandboxed
or not. Read `docs/linux-portals.md` before touching any of it — in particular
the rule that the portal frontend advertises interfaces its backend may not
implement, so only a real attempt tells you anything.

Injection tries, in order: the **RemoteDesktop portal** (GNOME, KDE), then
**wtype**, then **ydotool**, then clipboard-only. `inject.ActiveBackend` reports
which one won, and the settings page shows it — with a fallback chain, "it works"
and "it works the way you think" are different claims.

The hotkey prefers the **GlobalShortcuts portal** (`internal/hotkey/hotkey_linux.go`).
The portal refuses callers it cannot identify, so `cmd/vito/scope_linux.go`
re-executes Vito inside a systemd app scope when it is not already in one; it
fails open. The **signals stay supported permanently** as the fallback and the
documented route for scripts: `pkill -USR2 -f 'vito serve'` toggles a dictation
and `pkill -USR1 -f 'vito serve'` cancels it (`cmd/vito/signals_unix.go`). They
need no stable binary path and never mount a squashfs, so they are identical for
a Flatpak, an AppImage, a distro package or a plain binary. The per-desktop
commands are shown in the app under Settings → Activation
(`linuxActivationHTML` in `web/index.html`).

Two portal paths cannot be tested on a compositor that lacks a backend — niri is
the canary here, and it has neither. Each has a manual test behind an environment
variable, to be run on GNOME or KDE:

```sh
VITO_PORTAL_TEST=1 go test ./internal/inject/ -run TestPortalSession -v
VITO_GS_TEST=1     go test ./internal/hotkey/ -run TestGlobalShortcutsSession -v
```

Where a stable path *is* baked in — the XDG autostart entry and the `vito://` URL
handler that relaunches the daemon for the PWA — use `internal/selfexe`, which
prefers `$APPIMAGE` over `os.Executable()`; the latter points into the throwaway
`/tmp/.mount_*` squashfs and would break after a reboot.

## Releases (macOS)

```sh
bash packaging/build-macos.sh 2026.8    # -> dist/Vito-2026.8.dmg (+ .sha256)
```

Must run on macOS; only the Xcode Command Line Tools are needed. The script
builds both architectures and merges them with `lipo`, so one download is
native on Apple Silicon and Intel.

Two `Info.plist` keys carry the behaviour the binary cannot have on its own:
`NSMicrophoneUsageDescription` (without it macOS denies the microphone outright
instead of asking, and dictation records silence) and `LSUIElement` (keeps Vito
out of the Dock — it lives in the menu bar).

The DMG opens as a styled installer window — app on the left, Applications on
the right, arrow between them. That layout is not a property of the image but
of the volume's `.DS_Store`, so the build mounts a read-write image, has Finder
arrange it (`packaging/dmg-layout.applescript`), and only then compresses it.
The artwork comes from `go run ./packaging/mkdmgbg`, alongside `mkicon`.

Two ordering traps live in that step, both already worked around:
`hdiutil create -srcfolder` silently drops a staged `.VolumeIcon.icns`, and
applying the window settings makes Finder rewrite the volume's Finder info —
which deletes that file and clears its custom-icon flag. So the volume icon is
written *after* the Finder step, directly onto the mounted image. Moving it
earlier looks tidier and silently loses the icon.

The build is signed with a **self-signed certificate**, created once per machine
by `packaging/make-signing-cert.sh` and picked up automatically; without it the
build falls back to ad-hoc and says so. The certificate is not a Developer ID
and changes nothing about Gatekeeper. What it changes is what the permissions
macOS stores are tied to:

    ad-hoc     designated => cdhash H"…"
    this cert  designated => identifier "io.github.vinceecniv.vito"
                             and certificate root = H"…"

The ad-hoc form names one exact binary, so every rebuild silently invalidated
the user's Accessibility grant. The certificate form survives it — verified by
granting the permission, rebuilding under a new version number, reinstalling,
and finding the hotkey still registered without re-granting. Sign every release
with the same certificate or that guarantee is gone.

If a build asks for the keychain password — twice, once per architecture slice
of the universal binary — the key's **partition list** is the reason.
`security import -T /usr/bin/codesign` only sets the access list, and macOS
gates on both. `make-signing-cert.sh` fixes it, asking for the login keychain
password because `security set-key-partition-list` cannot be run without it.
Re-run the script any time; it repeats that step even when the certificate
already exists.

Gatekeeper is untouched by this: `spctl -a` still rejects the app, the first
launch still needs Open Anyway, and Finder still draws a ⌛ after "Vito".

Do not go hunting for that badge in the packaging; it was measured. A notarised
app placed in the same disk image gets no badge and Vito does; the badge follows
the app when copied out to a local disk, so it is not about the image; and it is
unaffected by `com.apple.quarantine`, present or removed — so approving the app
(right-click → Open) does not clear it either. Only notarisation does. Note it
only shows in Finder's *icon* view, which makes column view a misleading place
to check. A Developer ID certificate plus
`notarytool` fixes both at once; nothing in the code can.

Two permissions gate the platform code, and they fail differently:

- **Microphone** — asked for automatically the first time Vito records.
- **Accessibility, when the toggle is on but Vito still says denied** — the
  stored approval is a *code signing requirement*, and for an ad-hoc signature
  that requirement is the exact cdhash, which changes with every build. The
  switch stays on while pointing at a binary that no longer exists, and running
  the same app from a second path (a mounted DMG, the build tree) adds its own
  entry. Restarting does not help; clearing it does:

  ```sh
  tccutil reset Accessibility io.github.vinceecniv.vito
  ```

  Then let Vito ask again (Settings → Activation → Grant Accessibility
  permission) so the approval attaches to the copy actually running.

- **Accessibility** — never asked for automatically. It gates both the
  CGEventTap in `internal/hotkey` (no hotkey without it) and CGEventPost in
  `internal/inject` (paste and type modes; clipboard-only still works). The
  hotkey status reports `ErrCode: "denied"` so the settings page can explain
  it. `inject.RequestAccessibility()` triggers the system prompt, but macOS
  shows it once per app per login — spend it on a user action, not a startup
  check.

`cmd/vito/launch_darwin.go` is why double-clicking the app works at all: with no
arguments Vito prints its usage, which is useless for a bundle, so inside
`.app/Contents/MacOS/` the default command becomes `serve`.

The two remaining backends are deliberately unalike. `internal/audio/level_darwin`
is cgo against the CoreAudio HAL, because there is no shell tool that reads a
specific capture device's volume. `internal/media/media_darwin` shells out to
`osascript` instead, mirroring how the Linux backend uses pactl/playerctl —
macOS has no public per-app volume, so duck lowers the one system slider, and
pause drives only the scriptable players. Never talk to a player without
checking `is running` first: telling a stopped app to pause *launches* it.

## Language

The user writes in Dutch and the UI's source strings are Dutch. Answer in Dutch.
