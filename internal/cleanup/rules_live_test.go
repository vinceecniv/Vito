package cleanup

// Live checks of Vito's default rules against the cleanup service in the
// user's own config — the scenarios of the competitor test (s4, s5, s7, s9)
// plus sentences that must come through untouched. Guarded, since it spends
// the user's tokens:
//
//	VITO_TEST_RULES=1 go test ./internal/cleanup/ -run LiveRules -v
//
// A model is not deterministic: run it a few times before trusting a pass.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vito/internal/config"
)

func TestLiveRules(t *testing.T) {
	if os.Getenv("VITO_TEST_RULES") == "" {
		t.Skip("VITO_TEST_RULES not set")
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "vito", "config.json"))
	if err != nil {
		t.Skip("no Vito config: ", err)
	}
	var c struct{ Cleanup config.Cleanup }
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	cfg := c.Cleanup
	cfg.ActivePrompt, cfg.TimeoutMS = "", 30000 // Vito's own rules
	cl := NewCleaner(cfg)

	cases := []struct {
		name, lang, raw string
		has, hasNot     []string
	}{
		{"self-correction nl", "nl", "Laten we dinsdag om tien uur afspreken, nee wacht, woensdag om elf uur, op kantoor in Utrecht.",
			[]string{"woensdag", "Utrecht"}, []string{"dinsdag", "wacht"}},
		{"self-correction en", "en", "Let's meet at three, actually no, make it four thirty, in the main conference room.",
			[]string{"conference room"}, []string{"three", "at 3", "actually"}},
		{"self-correction sorry", "nl", "Het kost vijftig euro, sorry, vijfenzestig euro per maand.",
			[]string{"per maand"}, []string{"50 ", "vijftig ", "sorry"}},
		{"list", "nl", "Ik heb drie punten voor de vergadering. Ten eerste het budget voor volgend jaar. Ten tweede de planning van het project. Ten derde de vacature voor de nieuwe ontwikkelaar.",
			[]string{":\n", "\n1", "\n2", "\n3"}, nil},
		{"letter", "nl", "Beste Mark, bedankt voor je bericht van gisteren. Ik stuur je de stukken uiterlijk vrijdag toe via mark punt jansen apenstaartje voorbeeld punt nl. Met vriendelijke groet, Vincent.",
			[]string{"Beste Mark,\n\n", "mark.jansen@voorbeeld.nl", "groet,\nVincent"}, nil},
		{"nee as a word", "nl", "Hij zei nee, wacht maar tot morgen, dan weten we meer.",
			[]string{"nee", "morgen"}, []string{"\n"}},
		{"actually as a word", "en", "Actually, I think the plan is fine as it is.",
			[]string{"ctually", "fine"}, []string{"\n"}},
		{"snippet in a sentence", "nl", "Eh stuur hem gewoon ⟦1⟧ even door, dan kan hij je vinden.",
			[]string{"⟦1⟧ "}, []string{"eh ", "⟦2⟧"}},
		{"snippets in a letter", "nl", "Beste Mark, bedankt voor je bericht. Mijn gegevens staan hieronder. Met vriendelijke groet, ⟦1⟧ en mijn site is ⟦2⟧.",
			[]string{"⟦1⟧", "⟦2⟧", "bedankt"}, []string{"⟦3⟧"}},
		{"snippet after a self-correction", "en", "Send him my email, no wait, ⟦1⟧ instead, thanks.",
			[]string{"⟦1⟧"}, []string{"wait"}},
		{"plain sentence", "nl", "We hebben eerst gegeten en daarna zijn we naar de film gegaan.",
			[]string{"eerst", "daarna"}, []string{"\n"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			got, _, err := cl.Clean(ctx, tc.raw, tc.lang, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("%q", got)
			low := strings.ToLower(got)
			for _, w := range tc.has {
				if !strings.Contains(got, w) && !strings.Contains(low, strings.ToLower(w)) {
					t.Errorf("missing %q", w)
				}
			}
			for _, w := range tc.hasNot {
				if strings.Contains(low, strings.ToLower(w)) {
					t.Errorf("still has %q", w)
				}
			}
			time.Sleep(5 * time.Second) // a free tier counts tokens per minute
		})
	}
}
