# The Flatpak, served from GitHub

Vito ships a Flatpak, but not through Flathub — the repository is hosted from
GitHub Pages instead. What that costs is the store listing: Vito will not turn up
by searching in GNOME Software or KDE Discover. What it does *not* cost is the
thing that made packaging worthwhile — one command to install, and `flatpak
update` from then on, exactly like any other application.

Why bother at all, given the AppImage already works: see `docs/linux-portals.md`.
Short version — the sandbox forced the move to the XDG portals, and that move
removed the `ydotoold` + `/dev/uinput` setup for *every* Linux user, Flatpak or
not.

## Installing (what to tell users)

```sh
flatpak remote-add --user --if-not-exists vito https://vinceecniv.github.io/Vito/vito.flatpakrepo
flatpak install --user vito io.github.vinceecniv.vito
```

Until the repository is signed, that second command needs the remote to have been
added with `--no-gpg-verify`; see below.

## What is in the sandbox, and what is not

| | |
|---|---|
| `--socket=wayland` | all the virtual-keyboard backend needs |
| `--socket=pulseaudio` | microphone capture (PipeWire serves this) |
| `--share=network` | the user's own STT/AI providers, and the local settings server |
| `--talk-name=org.freedesktop.Notifications` | desktop notifications |
| `--talk-name=org.kde.StatusNotifierWatcher` | tray icon |
| `--talk-name=org.mpris.MediaPlayer2.*` | pausing media while recording |

No `--device=all`, no uinput, no broad filesystem access. Text delivery goes
through the RemoteDesktop portal (GNOME, KDE) or the bundled `wtype` over the
virtual-keyboard protocol (niri, wlroots); the hotkey through GlobalShortcuts.

**The ydotool fallback is absent by design.** It needs a daemon and a udev rule
outside the sandbox, so it cannot work here. On a desktop that offers neither
portal nor virtual-keyboard protocol, the Flatpak can still transcribe and copy
to the clipboard — it just cannot press Ctrl+V for you. Settings → Linux always
names the delivery route actually in use.

Bundled: `wl-clipboard` (the clipboard is used by paste mode, clipboard-only mode
and Vito Assist's clipboard commands) and `wtype`.

`notify-send` and `pactl` turn out to ship in the freedesktop runtime already, so
notifications and the microphone-level control work without bundling anything —
and pausing media needs no binary at all since it speaks MPRIS directly. Nothing
is missing in the sandbox except ydotool, which cannot work there by definition.

### Verified in the sandbox

Running the built Flatpak on GNOME 50:

* the GlobalShortcuts portal binds the hotkey — and unlike the host build, it
  needs no systemd-scope trick, because a Flatpak's app id is intrinsic;
* the delivery route resolves to `portal`, with `wtype` present but refused by
  Mutter, exactly as on the host;
* `wl-copy` sets the real clipboard from inside the sandbox;
* config lands in `~/.var/app/io.github.vinceecniv.vito/config/vito/`;
* a full dictation works end to end — microphone, the user's STT and AI
  providers over the network, and injection through the portal. The first one
  costs ~6s while the RemoteDesktop session is established, the next ~900ms,
  which is what the host build does too.

The RemoteDesktop permission is asked once more after switching from the
AppImage: to the portal the Flatpak is a different application, so it does not
inherit the host build's grant.

## Config lives somewhere else

A Flatpak's home is redirected, so settings and history land in
`~/.var/app/io.github.vinceecniv.vito/config/vito/` rather than
`~/.config/vito/`. Someone moving from the AppImage to the Flatpak keeps neither
their keys nor their history unless they copy that directory across.

## Building it

```sh
sudo dnf install flatpak-builder                     # once
flatpak install flathub org.freedesktop.Sdk//25.08 \
                        org.freedesktop.Platform//25.08 \
                        org.freedesktop.Sdk.Extension.golang//25.08

# Substitute the version into a copy, so the placeholder survives in git —
# committing the substituted manifest would freeze that version into every later
# CI build. The copy must sit beside the original: the vito module's source is
# `path: ../..`, which is resolved relative to the manifest.
sed "s/@VITO_VERSION@/2026.7.3/" packaging/flatpak/io.github.vinceecniv.vito.yml \
  > packaging/flatpak/build.yml
flatpak-builder --user --install --force-clean --disable-rofiles-fuse build \
  packaging/flatpak/build.yml
rm packaging/flatpak/build.yml
```

`@VITO_VERSION@` is a placeholder rather than an environment variable on purpose:
flatpak-builder does not pass the outer environment into the build sandbox, so
the version has to be substituted into the manifest before building. CI does the
same thing from the tag name.

The build fetches Go modules from the network (`--share=network` in
`build-options`). Flathub forbids that and would require `go mod vendor`; a
self-hosted repository does not, and not vendoring keeps the tree small.

## Publishing

`.github/workflows/flatpak.yml` runs on a `v*` tag: it builds the app into an
ostree repository, generates static deltas, and publishes the result to GitHub
Pages together with `vito.flatpakrepo`. Publishing a *repository* rather than a
single `.flatpak` bundle is what makes `flatpak update` work at all — a bundle
would have to be downloaded and reinstalled by hand for every release.

One-time repository setup: enable Pages with "GitHub Actions" as the source.

### Signing (recommended)

Without a key the repo is unsigned and users must add the remote with
`--no-gpg-verify`, which means nothing verifies what they are updating to. To
sign:

```sh
gpg --quick-generate-key "Vito Flatpak Repository" default default never
gpg --armor --export-secret-keys <KEY-ID>      # -> secret FLATPAK_GPG_KEY
```

Add `FLATPAK_GPG_KEY` (the armoured private key) and `FLATPAK_GPG_ID` (the key
id) as repository secrets; the workflow signs the repo and exports the public key
to `repo/vito.gpg` when they are present. Then add a `GPGKey=` line to
`vito.flatpakrepo` with the base64 of that public key, and drop the
`--no-gpg-verify` from the install instructions.
