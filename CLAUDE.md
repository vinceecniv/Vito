# Vito — notes for Claude Code

Vito is a personal voice-dictation daemon in Go with an embedded web UI.

## The UI is embedded in the binary

`web/web.go` uses `go:embed`, so editing `web/index.html` changes nothing until
the binary is rebuilt and the daemon restarted. That is automated: a PostToolUse
hook on `Write|Edit` (`.claude/settings.json` → `.claude/hooks/rebuild.sh`)
rebuilds `dist/vito` and restarts a daemon running from it after every edit.
Hooks run through Git Bash on Windows, where `rebuild.sh` hands over to
`rebuild.ps1` (`dist/vito.exe`). An installed Vito (AppImage, setup) is never
touched. If the UI seems not to change, check the hook ran — don't debug the
CSS first.

A test daemon next to the installed one needs its own config (and so its own
port): `XDG_CONFIG_HOME=<dir> dist/vito serve` with
`{"server":{"port":4599,"token":"…"}}` in `<dir>/vito/config.json`. It also
shows its own tray icon — stop it when done.

The user usually views the app as an installed PWA, which caches aggressively.

## The UI can also be downloaded

A release daemon serves a newer interface between releases: `internal/uibundle`
fetches `https://vito.talk/ui/ui.json` (+ `.sig`, ed25519) at start and every 6 h,
verifies it and the zip's sha256, unpacks to `<UserCacheDir>/vito/ui/<version>/`
and serves it from the next page load. The embedded `web.Files` is the fallback;
`/?ui=builtin` pins a browser to it, `/?ui=auto` releases it. Every UI handler
reads through `s.uiFS(r)` — never from `web.*` directly.

A `dev` build never serves a download (set `VITO_UI_URL` to test one), so local
edits stay visible. A bundle names the release it updates (`app`, default the
latest `v*` tag) and is served only by that exact version, so after the next
release the newer built-in interface wins until a bundle for it is published.
It also needs `min_api <= uibundle.API <= api`: **raise `uibundle.API` when the UI starts depending on something new in
the daemon**, or a published interface will call routes older daemons lack.

Publish with `pwsh -File scripts/publish-ui.ps1 [-Push]` (builds into
`../Vito-Web/ui` via `go run ./packaging/uibundle build`). The private key is
`~/.vito-signing/ui-ed25519.key` on the Windows build machine; losing it means
shipping a release with a new public key.

## Vito in the browser (vito.talk/app)

The same `index.html` runs without the app. When the page is not served by the
daemon, `__VITO_TOKEN__` is still the placeholder; then `STANDALONE` is true,
`standalone/backend.js` answers every `api()` call and stands in for the
WebSocket (localStorage for config, history and day sums), and
`standalone/whistle-worker.js` runs Whistle as WebAssembly, cut at pauses like
`internal/stt/whistle_stream.go` (the engine's own streaming mode is ~2× slower
than realtime in WASM). Its stats/streaks/achievements are ports of
`internal/history` — keep them in step. Settings that need the app are hidden by
CSS under `.standalone`; `.web-only`/`.helper-only` swap text.

Asset URLs in `index.html` must stay **relative** (`i18n/…`, not `/i18n/…`): the
browser version lives under `/app/`.

Build the site with `go run ./packaging/uibundle app -out <dir>` (adds the
default config, the achievement list and the pinned needle WASM). Test it
locally by serving `<dir>` under `/app/` and opening it in headless Edge with
`--use-file-for-fake-audio-capture=<wav>` as the microphone.

From the browser to the app: the daemon answers `GET /api/hello` to
`https://vito.talk` only (plus `VITO_WEB_ORIGIN`), and `POST /handover` parks
the browser's data for the app's own page to confirm (`internal/server/handover.go`,
`history.Import`, idempotent per source).

## Sync

`internal/cloudsync` syncs through a **folder** the user's own sync app keeps
the same everywhere (Dropbox, OneDrive, Nextcloud, iCloud Drive, Syncthing, a
network share) — no accounts, tokens or server. `Candidates()` finds the usual
folders; `PickFolder` opens the system's picker. Vito works in `<folder>/Vito
Sync`, writing beside the target and renaming into place.

Each computer writes only `devices/<id>.json` and `history/<id>/<yyyy-mm>.json`
and imports the others' (`history.Import` with `SyncSource`), so a sync app
never sees two computers change one file. Entries taken over are marked by
`origin` and never re-shared. The device file also carries all day_stats
columns (costs, Assist, uploads), the user's deletions and star changes
(`sync_deleted`/`sync_favorite`; automatic pruning is not synced), and the
dictionary (with its text snippets) and cleanup rule sets (newest wins after a
first merge).

## Text snippets

`dictionary.snippets` maps a spoken trigger ("mijn LinkedIn") to fixed text.
`internal/dictionary/snippets.go` swaps each trigger for a placeholder (`⟦1⟧`)
before the cleanup pass and the text in after (`Mark`/`Restore`), so the model
never rewrites it; `cleanup.SnippetNote` tells it to keep them. When the model
loses or doubles a placeholder, the raw transcript (with every snippet) is
pasted instead. `web/standalone/backend.js` has the same in JavaScript — keep
the two in step. Measured with Groq gpt-oss-120b: placeholders survive
self-corrections and letter layout (`rules_live_test.go`).

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
  detected by the model, uploads must be WAV.
- **Bring-your-own endpoint** (`stt.provider = "openai"`): whisper-server,
  Speaches, LocalAI, Groq, OpenAI. For whisper.cpp Vito sends `audio_ctx` sized
  to the recording (halves CPU time — Whisper always encodes 30 s otherwise),
  falls back to `/inference` on a 404, and joins whisper.cpp's newline-separated
  segments back into one line.

Measured on this machine (i9-10920X, 12 threads): Parakeet ≈ 1 s per dictation
on CPU, 0.25 s on Vulkan; Whisper large-v3-turbo ≈ 4–9 s on CPU, 0.4 s on CUDA.
Whisper is the better engine for Dutch with English jargon; Parakeet for CPU.
Benchmark only on an idle machine — a background encode inflated numbers 2×.

## Vito updates itself

`internal/update.Auto` (setting `update.auto`, nil = on, only while the check is
on) downloads a newer release in the background, verifies it against the
`<asset>.sha256` published beside it, and installs it once the daemon has been
idle for 3 minutes: Windows runs the setup silently (only for a copy with
`unins*.exe` beside it; `[Run]` restarts Vito), Linux swaps `$APPIMAGE` and
re-execs, macOS replaces the `.app` from the `.dmg` and `open -n`s it. Flatpak,
distro packages and `dev` builds are left alone. The replaced version is noted
in `<UserConfigDir>/vito/updated-from` and reported once as `updated_from` in
`/api/update`. Keep the asset names (`Vito-Setup-<v>.exe`,
`Vito-<v>-<arch>.AppImage`, `Vito-<v>.dmg`, each with `.sha256`) stable — the
updater looks them up by name. The check (`update.Checker`) also uses those
names to decide what counts as an update: it lists the recent releases and
takes the newest one carrying this system's download (`PlatformAsset`), so a
release with only a Windows setup is no news on a Mac or Linux.

## Releases on GitHub

`.github/workflows/release.yml` builds all three downloads on GitHub's runners
when a `v*` tag is pushed — Windows (`build-installer.ps1`), Linux
(`build-appimage.sh` in `golang:1.26-bookworm`) and macOS (`build-macos.sh`) —
and puts them with their `.sha256` in a **draft** release "Vito <version>",
the checksums filled into the notes. Write the story at the top, then publish.
If a release for the tag already exists, its files are replaced and only its
checksum lines are touched. It can also be started by hand (Actions → Release)
with a version, with or without filling the release.

A release is for all three systems unless its annotated tag says otherwise
in a line of its message — `git tag -a v2026.12 -m "Vito 2026.12" -m
"Platforms: windows"` — or a run by hand names them (`platforms` input);
`scripts/release-platforms.sh` reads both. Only those are built, attached
and listed in the notes, the Flatpak follows only with linux, and the
updater announces the release only where it carries that system's download. The Flatpak follows from
`flatpak.yml`; the signed interface bundle stays a manual `publish-ui.ps1`,
because its key must not live in the repository's secrets.

The local scripts below remain the same builds, for testing or when CI is down.

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

The version is recorded where Linux tools look for it, not only in the file
name (which is gone once a user renames the file to `Vito.AppImage`):
`X-AppImage-Version` in the embedded `.desktop` files (AppImageLauncher, Gear
Lever, appimaged), a `<release>` entry in the AppStream metainfo (software
centres, `flatpak info`; added at build time by `packaging/stamp-metainfo.sh`
for the AppImage *and* the Flatpak), and update information
(`gh-releases-zsync|vinceecniv|Vito|latest|Vito-*-x86_64.AppImage.zsync`, read
with `--appimage-updateinformation`) with a `.zsync` beside the image, which
the release workflow uploads. Only release versions get the last two; dev
builds keep the committed metainfo and carry no update information.

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

Injection tries, in order: **XTEST** in an X11 session (`internal/x11`), then the
**RemoteDesktop portal** (GNOME, KDE), then **wtype**, then **ydotool**, then
clipboard-only. `inject.ActiveBackend` reports
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

A compositor that reports press and release can send `pkill -RTMIN+1` and
`-RTMIN+2` instead: key down and up, read by `hotkey.Gesture` as hold to dictate,
double-tap for hands-free, and a lone tap dropped with a hint. That is how Caps
Lock works on niri. A bind on `Caps_Lock` itself is not enough, because niri
updates its XKB state before it looks for binds, so capitals lock anyway. Use
`caps:menu` in the xkb options and bind `Menu` (measured in a nested niri).
Settings → Activation can write both into the user's `config.kdl` after
asking (`internal/hotkey/niri_caps_linux.go`): into the existing `binds`
block (niri allows one per file) between `// Vito: Caps Lock` and
`// Vito: end`, a dated `.bak` beside it, and `niri validate` on a copy in the
same folder (so includes resolve) before anything is replaced.
The Activation tab opens with "Your dictation key" (`dictKeyHTML`): the key in
use and the one-key option per system; own combinations stay below it.

Two portal paths cannot be tested on a compositor that lacks a backend — niri is
the canary here, and it has neither. Each has a manual test behind an environment
variable, to be run on GNOME or KDE:

```sh
VITO_PORTAL_TEST=1 go test ./internal/inject/ -run TestPortalSession -v
VITO_GS_TEST=1     go test ./internal/hotkey/ -run TestGlobalShortcutsSession -v
```

Opening Vito's page (tray, island, first run) goes through `internal/webapp`:
an installed Chromium-family web app is started instead of a browser tab. Its
id comes from the browser's web-app sync store (`<profile>/Sync Data/LevelDB`,
`web_apps-dt-<id>` followed by the start URL), because the id alone does not
say which page it is; the launcher is found by that id — a desktop entry's
`--app-id` on Linux, a Start menu `.lnk` (arguments in UTF-16) on Windows, an
app shim's `CrAppModeShortcutID` in `~/Applications/*/` on macOS. Only the
Linux path has met a real browser (Brave); the others are tested on fakes.

Where a stable path *is* baked in — the XDG autostart entry and the `vito://` URL
handler that relaunches the daemon for the PWA — use `internal/selfexe`, which
prefers `$APPIMAGE` over `os.Executable()`; the latter points into the throwaway
`/tmp/.mount_*` squashfs and would break after a reboot.

The **dictation pill** (`internal/pill`) floats while dictating: `render.go` draws
it without any platform code, `window_linux.go` is a small hand-written Wayland
client for a wlr-layer-shell surface, connected only while the pill shows. It
must never take the keyboard focus, or it would swallow the paste it announces.
`ui.pill_style` picks how it shows the voice: the glow (default, ""), `bars`, or `vito`,
the mascot redrawn in Go (`mascot.go`, in the SVG's own coordinates — keep it in
step with `mascotSVG` in `web/index.html`).
The live transcript's speech glow (`trGlowPaint` in `web/index.html`, on or
off in that card's menu) is `drawGlow` ported to a canvas — keep the two in step.
Only the pill takes the pointer (an input region, `inputBox`): hovering shows ✕
(cancel) and ✓ (stop), a click on the resting pill starts hands-free on that
screen. Every object id in `window_linux.go` is taken in order, an unused one by
a sync, so the ids handed out later (`newID`, for `setInput`) leave no gap.
The pill also opens into a card (`card.go`, `notes.go`) for a failed dictation
and for a new achievement, which the server announces after each dictation
(`recordAchievements` → `Daemon.Announce`). An achievement's card has a turning
gradient edge (`spinRing`); a click on it opens Vito on the achievements tab
(a `show` event; with no page open the server hub keeps it for the page that
connects next, `keptFor`). Text is drawn with Sora from
`internal/pill/font` (OFL) through `golang.org/x/image`; its words come from
`internal/uitext`, the tray's translation tables, so they belong in TR.nl too.
Pointing at a resting pill for a moment, or right-clicking it, opens the island
(`island.go`): its own window per screen (`openIslandWindow`, lower margin) with
the last dictation to paste again, today's words and saved typing time
(`pill.Sources`, fed from history in `main.go`), the logo that opens Vito, and behind ☰ the quick settings, with
the cleanup rule sets as a list of their own. The pin in its header keeps it open as a window
(`ui.island_pin`): dragged by its free space (an unpinned one pins itself once dragged), the resting pills give way to it
and a dictation draws into it (`embedWindow`, laid where the microphone is).
Two niri findings shaped the drag, both measured in a nested niri: during a
button-held drag niri reports pointer positions from where the surface was when
the button went down, and a layer surface anchored left and right with a fixed
width sits against the left edge instead of centred. So the drag prefers
`zwp_relative_pointer_v1` motion, falls back to start position plus the whole
way since the press, and places the window from the left edge with the screen
width from niri's `Outputs` (`screenSize`), which also keeps it on screen. `ui.pill_live` shows the partial
transcript in a strip over the pill. A lone tap of the dictation key ends in a
card instead of a desktop notification once a pill window has opened
(`Daemon.SetHintShown`). After each dictation the pill asks the desktop whether
a window had the keyboard (`focusSource.window`, niri's `FocusedWindow`); when
none had, a card offers the text for the clipboard. AT-SPI could tell an
editable field from the rest, but on niri the accessibility bus is off, so
browsers and terminals never register there — it was measured, not assumed.
A cancelled dictation keeps its recording for 5 minutes (`internal/daemon/kept.go`,
not transcribed, not in the history; a new dictation or quitting deletes it):
the pill shows a card and the island's last-dictation box offers it, and
`RestoreCancelled` transcribes the file and pastes it like any dictation.
The resting pill (`ui.pill_idle`, on unless set to false,
`idle.go`) is one still window per screen on the Top layer, on its own connection;
a dictation's window opens over it on the Overlay layer. Light or dark follows
`ui.theme`, then the Settings portal's `color-scheme`, live. On niri the resting
pill of the focused screen stands out and the others dim (`focus_niri.go`, the
IPC event stream: layer-shell cannot tell). Another window manager adds a
`focusSource`, another OS a `window_*.go`: the seams are listed in `pill.go`. Try
the window with
`VITO_PILL_TEST=1 go test ./internal/pill/ -run TestWindow -v`.

### The pill on other desktops

All measured in VMs (`~/VMs/linux` on the development machine: `mk.sh` makes a
Fedora 45 or Ubuntu desktop VM from the cloud image, `mk-mint.sh` installs
Linux Mint from its ISO, `run.sh` boots one with ssh, VNC, QMP and the private
`vmmic` microphone; the Windows VM is the research one).

- **Windows** (`window_windows.go`): a layered, topmost, never-activated window
  per picture, on its own locked thread with per-monitor DPI awareness for that
  thread only. The 2× picture is scaled to the screen's DPI (`resample.go`).
  Click-through except over the input box, which a 16 ms timer watches.
  `focus_windows.go`: the screen of the foreground window; `theme_windows.go`:
  `AppsUseLightTheme`. Caps Lock is `hotkey_windows: "capslock"`, read by the
  existing low-level hook and swallowed, so capitals never lock. Capitals
  locked while no hook ran (Vito restarting mid-press) are unlocked when the
  hook starts and at the next press, with a Caps Lock press tagged
  `capsMark` that the hook lets through (`capsOff`).
- **KDE** and **Hyprland** have layer-shell: the Linux window works as is.
  KDE's screen size comes from `kscreen-doctor -j` (`focus_kde.go`; without it a
  dragged island leaves the screen and KWin closes it, "invalid geometry");
  Hyprland's from its IPC (`focus_hyprland.go`). A placed island is anchored at
  the bottom, so it stays clear of a taskbar.
- **GNOME** (51 at the time) has none, so Vito installs its own Shell extension
  (`gnome/extension.js`, embedded, written to the user's extension folder by
  `gnomeext.go` and enabled). Vito sends it the pictures over D-Bus
  (`talk.vito.Pill`, `window_gnome.go`); it shows them as chrome, which never
  takes the keyboard. GNOME loads a new or updated extension only at the next
  login; `/api/linux-tools` reports `gnome_ext` and the settings page says so.
  Bump `version` in `metadata.json` (and `VERSION` in the script) with every
  change, or users keep the old one. GNOME 51 dropped `affectsInputRegion` and
  `global.display.set_cursor`; the script tries both the old and the new way.
- **Caps Lock on Linux** reaches the desktop as Menu (XKB `caps:menu`,
  `hotkey.CapsAsMenu`): GNOME takes it live (gsettings), KDE only after a new
  login (kxkbrc; neither its reload signal nor `--notify` applied it), Hyprland
  at runtime with binds that signal Vito (`hyprctl eval` Lua from 0.55, keywords
  before). KDE and GNOME then bind Menu through the GlobalShortcuts portal,
  whose Activated/Deactivated feed `Gesture`. On KDE Menu is also the standard
  "open context menu" key, which its dialog warns about.
- **X11** (Linux Mint's Cinnamon, still X11 by default in 22.3; any X session):
  `internal/x11` does what the portals do on Wayland, with no helper tools —
  a key grab on the root window for the hotkey (press and release, auto-repeat
  pairs dropped, re-grabbed on a keymap change; Vito owns the keys as on
  Windows, so the settings page shows the capture fields), XTEST for the
  keystrokes (`backendX11`), and the clipboard as a selection Vito serves
  itself. The pill (`window_x11.go`) is an override-redirect ARGB window whose
  SHAPE input region is the input box; placed by Vito in the screen's work
  area (`_GTK_WORKAREAS_D0`, else `_NET_WORKAREA`), scaled by `Xft.dpi`.
  xgb's extension `Init` writes package maps every connection reads, so the
  first `x11.Open` readies RANDR/SHAPE/XTEST once and later connections only
  take the opcodes (`Conn.Ext`) — calling `Init` per connection crashed Vito.
  Caps Lock: `caps:menu` in `org.cinnamon.desktop.input-sources` (Cinnamon 6.4+;
  libgnomekbd before; `setxkbmap` elsewhere), applied live, then Menu is grabbed.
- **Ubuntu 26.04** (GNOME 50.1): pill (extension), portal hotkey and pasting
  work. Caps Lock did not: with `caps:menu` set, the GlobalShortcuts binding on
  Menu fired for the real Menu key but never for Caps Lock (Fedora's GNOME 51
  fired for both). Cause not found; F-keys and combinations work.
- **macOS** (`window_darwin.go`/`.m`, written without a Mac, so untested): a
  borderless non-activating `NSPanel` per picture that never becomes key, on
  every Space and over full screen apps (the resting pill one level lower), the
  2× picture as its layer's contents. Click-through except over the input box,
  watched by a 16 ms timer as on Windows. AppKit needs the main thread, which
  is the tray's `NSApplication`: with the tray off there is no pill. The screen
  in use is the frontmost app's window (`CGWindowListCopyWindowInfo`, which
  needs no Screen Recording right for bounds). The dictation key defaults to
  **Fn** (`hotkey_windows: "fn"`), as in Wispr Flow: hold to dictate, double-tap
  or Fn+Space for hands-free, and Fn with any other key cancels the dictation
  it began and lets the key through (`Gesture.HandsFree`/`Abort`). Fn arrives
  as a flags-changed event and is never swallowed, so macOS still acts on 🌐
  unless “Press 🌐 key to” is “Do Nothing” — the settings page says so. CI's
  `macos-latest` job (on a pull request) is the only compile check.
- In the Hyprland VM a release was lost when QEMU's `send-key` and
  `input-send-event` were mixed; with `input-send-event` alone it never was.
  Drive keys in these VMs with one of the two.

The settings page offers the pill's options where `pill.Available()` says it
works. The PWA's detached (Picture-in-Picture) window was removed in its
favour; it is in the git history.

## Releases (macOS)

```sh
bash packaging/build-macos.sh 2026.8    # -> dist/Vito-2026.8.dmg (+ .sha256)
bash packaging/build-macos.sh 2026.8 --release   # also attach it to release v2026.8
```

Must run on macOS; only the Xcode Command Line Tools are needed. The script
builds both architectures and merges them with `lipo`, so one download is
native on Apple Silicon and Intel.

`--release` uploads the `.dmg` and `.sha256` to the existing GitHub release
(normally the draft the Windows build made, with the Mac checksum still a
placeholder such as `MAC_DMG_SHA256`) and writes the real hash into the
"Checksums" block of its notes. Always use it for a release; publishing the
draft stays a separate, deliberate step.

No Mac at hand: the release workflow (see "Releases on GitHub") builds the dmg
on a GitHub macOS runner. A VM is not an option: macOS may only be virtualised
on Apple hardware.

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
