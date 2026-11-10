package pill

import (
	"fmt"
	"log/slog"
	"strings"

	"vito/internal/achievements"
	"vito/internal/config"
	"vito/internal/daemon"
	"vito/internal/inject"
	"vito/internal/uitext"
	"vito/internal/webapp"
)

// A note is a card waiting to be shown, in words still to be laid out.
type note struct {
	kind        cardKind
	label, main string
	medal       string // achievement id, for its medal
	text        string // cardNoTarget: the dictation, to copy on a click
	restore     func() // cardKept: brings the cancelled dictation back; cardAchievement: opens Vito on it
}

// noteFor turns a daemon event into a note, in the language of the interface.
func (p *pill) noteFor(e daemon.Event) (note, bool) {
	t := uitext.Translator(uitext.ResolveLang(p.d.Config().UI.Lang))
	switch e.Type {
	case "achievement":
		for _, d := range achievements.List {
			if d.ID == e.Text {
				// The names stay English everywhere, as on the page.
				return note{kind: cardAchievement, label: t("Achievement unlocked"), main: d.Name, medal: d.ID,
					restore: p.openAchievements}, true
			}
		}
	case "error":
		if e.Credit != "" {
			return note{kind: cardError, label: t("Out of credit"),
				main: strings.ReplaceAll(t("Top up {p} to keep dictating"), "{p}", e.Credit)}, true
		}
		if e.Error == "" {
			return note{}, false
		}
		return note{kind: cardError, label: t("Dictation failed · audio kept"), main: reason(e.Error, t)}, true
	}
	return note{}, false
}

// reason says in a few words why a dictation failed. The common causes get a
// sentence of their own; anything else is the error itself, without the
// layers of context in front of the part that tells what went wrong.
func reason(err string, t func(string) string) string {
	low := strings.ToLower(err)
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(low, w) {
				return true
			}
		}
		return false
	}
	switch {
	case has("connection refused", "no such host", "network is unreachable", "dial tcp", "i/o timeout", "tls handshake"):
		return t("No connection to the speech service")
	case has("401", "403", "unauthorized", "forbidden", "invalid api key", "api key is empty", "api_key is empty"):
		return t("The API key was refused")
	}
	// "transcription failed (stream): upload: 500 …" → "500 …", capitalised.
	parts := strings.Split(err, ": ")
	r := parts[len(parts)-1]
	if len(parts) > 1 && len(r) < 12 {
		r = strings.Join(parts[len(parts)-2:], ": ")
	}
	if r == "" {
		return err
	}
	return strings.ToUpper(r[:1]) + r[1:]
}

// previewNotes are one example of each kind of card.
func (p *pill) previewNotes() []note {
	var out []note
	for _, e := range []daemon.Event{
		{Type: "achievement", Text: "first"},
		{Type: "error", Error: `transcription failed (stream): dial tcp: connect: connection refused`},
		{Type: "error", Credit: "Soniox"},
	} {
		if n, ok := p.noteFor(e); ok {
			out = append(out, n)
		}
	}
	return out
}

// card lays the note out; pinned, narrower than the island's panel, so a
// card of two lines, which reaches over the panel's foot, stands in front of
// it as a card of its own (under the pill there is no room for it to grow
// downwards).
func (n note) card(pinned bool) (*card, error) {
	maxW := cardMaxW
	if pinned {
		maxW = panelW - 24
	}
	c, err := layCard(n.kind, n.label, n.main, n.medal, maxW)
	if err == nil && n.kind == cardKept {
		c.action = n.restore
	}
	if err == nil && n.kind == cardAchievement {
		c.action = n.restore // opens Vito on its achievements page
	}
	if err == nil && n.kind == cardNoTarget {
		text := n.text
		c.action = func() {
			go func() {
				if _, err := inject.Inject(config.Injection{Mode: "clipboard_only"}, text); err != nil {
					slog.Debug("pill: copy failed", "err", err)
				}
			}()
		}
	}
	return c, err
}

// openAchievements opens Vito on its achievements page: a page that is open
// is told, one the click starts gets it as its first event (server hub).
func (p *pill) openAchievements() {
	p.d.Announce(daemon.Event{Type: "show", Text: "achievements"})
	go func() {
		if err := webapp.Open(fmt.Sprintf("http://127.0.0.1:%d", p.d.Config().Server.Port)); err != nil {
			slog.Debug("pill: open Vito failed", "err", err)
		}
	}()
}

// hintNote explains the dictation key after a tap that did nothing.
func (p *pill) hintNote() note {
	t := uitext.Translator(uitext.ResolveLang(p.d.Config().UI.Lang))
	return note{kind: cardHint, label: t("Hold the key to dictate"), main: t("Double-tap it for hands-free")}
}

// noTargetNote offers a dictation that was pasted while no window had the
// keyboard, so it went nowhere.
func (p *pill) noTargetNote(text string) note {
	t := uitext.Translator(uitext.ResolveLang(p.d.Config().UI.Lang))
	return note{kind: cardNoTarget, label: t("No window had the focus"), main: t("Click to copy the text"), text: text}
}

// keptNote offers a dictation just cancelled, to restore with a click.
func (p *pill) keptNote() note {
	t := uitext.Translator(uitext.ResolveLang(p.d.Config().UI.Lang))
	return note{kind: cardKept, label: t("Dictation cancelled"), main: t("Click to restore it"), restore: func() {
		go func() {
			if err := p.d.RestoreCancelled(); err != nil {
				slog.Debug("pill: restore failed", "err", err)
			}
		}()
	}}
}

func (p *pill) hasNotes() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.notes) > 0
}

func (p *pill) nextNote() (note, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.notes) == 0 {
		return note{}, false
	}
	n := p.notes[0]
	p.notes = p.notes[1:]
	return n, true
}
