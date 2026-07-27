# Linux: XDG desktop portals (and the road to Flathub)

Vito's two most system-level features on Linux — pressing keys in *other* apps and
grabbing a system-wide hotkey — are exactly what an application sandbox exists to
prevent. Today both go around the sandbox:

* injection shells out to **`ydotool`**, which needs a running `ydotoold` and a
  udev rule for `/dev/uinput`;
* the hotkey is a **signal** the desktop sends to the process
  (`pkill -USR2 -f 'vito serve'`).

Neither works inside a Flatpak, and both ask the user to set up plumbing before
Vito works at all.

Wayland grew first-class answers to both, delivered over D-Bus by
`xdg-desktop-portal`. Moving to them is not only what makes a Flatpak (and thus
Flathub) possible — it also removes the `ydotoold` + udev setup on a normal
install. That is the real reason to do it; Flathub is the bonus.

## What is actually available

Probed on the development machine (Fedora 44, niri, `xdg-desktop-portal` 1.22.1
with `xdg-desktop-portal-gnome` 50):

| Portal | Version | What we use |
|---|---|---|
| `org.freedesktop.portal.RemoteDesktop` | 2 | `CreateSession`, `SelectDevices`, `Start`, **`NotifyKeyboardKeycode`**, `NotifyKeyboardKeysym` |
| `org.freedesktop.portal.GlobalShortcuts` | 1 | `CreateSession`, `BindShortcuts`, `ListShortcuts`, signals **`Activated`** / **`Deactivated`** |
| `org.freedesktop.portal.Clipboard` | 1 | `RequestClipboard`, `SetSelection`, `SelectionWrite` (tied to a RemoteDesktop session) |

Two consequences worth stating plainly:

1. **No libei, no CGO.** `NotifyKeyboardKeycode` is a plain D-Bus call, so the
   whole injection path can be written in pure Go. libei/`ConnectToEIS` is the
   newer transport, but the D-Bus notify methods are supported and are far less
   machinery for our use (a handful of keystrokes, not a remote-desktop stream).
2. **Push-to-talk survives.** `GlobalShortcuts` emits `Deactivated` on release,
   so hold-to-talk keeps working — it is not toggle-only.

`github.com/godbus/dbus/v5` is already in the module (indirect, via systray), so
this adds no new dependency tree.

## Architecture: two backends, one interface

`internal/inject` already funnels everything through a single `injectPlatform`,
so the portal work slots in behind the existing interface. Linux grows a backend
choice rather than a rewrite:

```
inject.Inject(cfg, text)
└── injectPlatform (linux)
    ├── portal backend   — RemoteDesktop: NotifyKeyboardKeycode / Keysym
    └── ydotool backend  — ydotool + wl-copy (today's code, unchanged)
```

Selection (`config.Injection.Backend`): `auto` (default) | `portal` | `ydotool`.

`auto` prefers the portal when the interface is present on the bus *and* a
session can be established, and falls back to `ydotool` otherwise. Inside a
Flatpak the portal is the only option, so `auto` resolves there by construction.
That keeps **backwards compatibility with older desktops** — X11, or a Wayland
compositor without the portals — which is the whole point of keeping both.

The same split applies to the hotkey: a `GlobalShortcuts` listener in the daemon,
with the existing SIGUSR1/SIGUSR2 signals kept permanently as the fallback (they
cost nothing and remain the documented route for scripts and older setups).

### Things the portal changes about behaviour

* **A permission prompt, once per session.** RemoteDesktop asks the user to allow
  remote control. The portal can persist that (`persist_mode`), so it should be
  a first-run dialog, not a per-dictation one — this is the single biggest UX
  risk and needs real testing per compositor.
* **The session must stay alive.** Both portals are session-based: create once at
  daemon start, hold the handle, tear down on shutdown. This is different from
  today's fire-and-forget `exec ydotool`.
* **Keycodes, not key names.** `NotifyKeyboardKeycode` takes evdev keycodes (the
  same numbers ydotool uses today: 29 = LEFTCTRL, 47 = V), so the paste path maps
  over almost verbatim. Typing arbitrary text is better served by
  `NotifyKeyboardKeysym`.

## Phases

1. **Backend seam** — add the `Backend` setting, split today's Linux code into a
   `ydotool` backend, add portal detection. No behaviour change yet.
2. **RemoteDesktop injection** — session + `NotifyKeyboardKeycode` for paste,
   `NotifyKeyboardKeysym` for type mode; wire persistence of the permission.
3. **GlobalShortcuts hotkey** — bind toggle + cancel, handle `Activated` /
   `Deactivated` (push-to-talk), surface the binding in the settings UI.
4. **Clipboard without `wl-copy`** — either the Clipboard portal or bundling
   `wl-clipboard` in the Flatpak; decide once 2 and 3 are proven.
5. **Flatpak** — `flatpak-builder` manifest (Go SDK extension or vendored deps),
   AppStream metainfo, desktop file, no `--device=all`; then Flathub submission.

Phases 1–3 are worth doing on their own merit even if Flathub never happens.

## Open risks

* **Compositor variance.** GNOME, KDE and wlroots-based compositors implement
  these portals to differing depths; niri (the dev machine) is a good canary but
  not representative. Each needs a manual pass.
* **Permission fatigue.** If a compositor refuses to persist the RemoteDesktop
  grant, the flow becomes unusable for a dictation tool. Fallback: keep
  `ydotool` selectable, and clipboard-only mode always works.
* **Flathub review** expects portals and no broad device access, plus
  reproducible builds — the manifest work is not the hard part, the injection
  path is.
