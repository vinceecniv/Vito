// Package web embeds the settings/status UI served by the daemon.
package web

import "embed"

// Index is the single-page UI; the daemon injects the auth token by
// replacing __VITO_TOKEN__ before serving it.
//
//go:embed index.html
var Index []byte

// PWA assets served as-is (no token) so the app is installable and offline-capable.
var (
	//go:embed manifest.webmanifest
	Manifest []byte
	//go:embed sw.js
	ServiceWorker []byte
	//go:embed favicon.svg
	Favicon []byte
	//go:embed icon-192.png
	Icon192 []byte
	//go:embed icon-512.png
	Icon512 []byte
	// Fonts self-hosted so the local-first UI renders identically offline and
	// without depending on the Google Fonts CDN (blocked by many browsers'
	// tracking protection). Both are variable fonts, latin subset only.
	//go:embed fonts-baloo2.woff2
	FontBaloo2 []byte
	//go:embed fonts-sora.woff2
	FontSora []byte
)

// Flags holds country-flag SVGs (lipis/flag-icons, 4x3) served at /flags/<cc>.svg
// for the language pickers — self-hosted so the 60-language Soniox list renders
// real flags offline and consistently (no emoji-flag fallbacks).
//
//go:embed flags/*.svg
var Flags embed.FS

// Logos holds the speech providers' marks, served at /logos/<name>.png for the
// model cards — 64 px PNGs made from the providers' own site icons, self-hosted
// for the same reason as the flags.
//
//go:embed logos/*.png
var Logos embed.FS

// I18n holds the frozen per-language UI translations (nl/en live in index.html
// and stay continuously maintained; these are generated on request), served at
// /i18n/<code>.json and loaded by the web UI on demand.
//
//go:embed i18n/*.json
var I18n embed.FS

// Achievements holds the medal art: a static PNG per achievement (Noto Emoji,
// so medals render identically on every platform instead of depending on the OS
// emoji font), the Lottie animations played on unlock/hover for the emoji that
// have one, and the small Lottie player. Served under /achievements/. Dropping a
// PNG named for an achievement id (see internal/achievements) overrides that
// medal, so custom art can replace the default without code changes.
//
//go:embed achievements
var Achievements embed.FS
