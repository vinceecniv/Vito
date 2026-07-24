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
libm), and `ydotool`/`wl-clipboard` need a daemon and a udev rule for
`/dev/uinput` — shipping copies would save nobody the install, so the settings
page checks for them instead.

The hotkey binds to a **signal**, not a command that launches Vito: `pkill -USR2
-f 'vito serve'` toggles a dictation and `pkill -USR1 -f 'vito serve'` cancels it
(`cmd/vito/signals_unix.go`). That needs no stable binary path and never mounts a
squashfs, so it is identical for an AppImage, a distro package or a plain binary —
no `~/.local/bin` copy step. The per-desktop commands are shown in the app under
Settings → Activation (`linuxActivationHTML` in `web/index.html`).

Where a stable path *is* baked in — the XDG autostart entry and the `vito://` URL
handler that relaunches the daemon for the PWA — use `internal/selfexe`, which
prefers `$APPIMAGE` over `os.Executable()`; the latter points into the throwaway
`/tmp/.mount_*` squashfs and would break after a reboot.

## Language

The user writes in Dutch and the UI's source strings are Dutch. Answer in Dutch.
