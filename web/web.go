// Package web embeds the settings/status UI served by the daemon.
//
// Files is the whole interface as one tree. It is the fallback the daemon
// always has; a newer, signed copy of the same tree can be downloaded from
// vito.talk and served in its place (internal/uibundle), so every handler
// reads the UI through an fs.FS rather than from these variables.
package web

import "embed"

// Files holds every UI asset, at the paths it is served under:
//
//   - index.html — the single-page UI; the daemon injects the auth token by
//     replacing __VITO_TOKEN__ before serving it.
//   - manifest.webmanifest, sw.js, favicon.svg, icon-*.png — PWA assets, served
//     as-is (no token) so the app is installable and offline-capable.
//   - fonts-*.woff2 — self-hosted so the local-first UI renders identically
//     offline and without the Google Fonts CDN (blocked by many browsers'
//     tracking protection). Both are variable fonts, latin subset only.
//   - flags/<cc>.svg — country flags (lipis/flag-icons, 4x3) for the language
//     pickers, so the 60-language list renders real flags offline.
//   - logos/<name>.png — the speech providers' marks for the model cards.
//   - i18n/<code>.json — the per-language UI translations (nl/en live in
//     index.html), loaded on demand.
//   - achievements/ — the medal art: a PNG per achievement (Noto Emoji), the
//     Lottie animations played on unlock/hover, and the small Lottie player.
//   - standalone/ — the browser version's stand-in for the daemon (vito.talk/app;
//     unused when the daemon serves the page).
//
//go:embed index.html manifest.webmanifest sw.js favicon.svg icon-192.png icon-512.png
//go:embed fonts-baloo2.woff2 fonts-sora.woff2 flags logos i18n achievements standalone
var Files embed.FS

// I18n is Files under the name the tray uses for its translations.
var I18n = Files

// Index and Icon512 are read once for the Go code that needs them directly:
// the tray (TR.nl) and the Linux launcher entry.
var (
	Index   = mustRead("index.html")
	Icon512 = mustRead("icon-512.png")
)

func mustRead(name string) []byte {
	b, err := Files.ReadFile(name)
	if err != nil {
		panic(err)
	}
	return b
}
