package cleanup

import (
	"strings"
	"testing"

	"vito/internal/config"
)

// The output contract is the one line a user cannot edit away: without it a
// model answers "Here is your cleaned text:" and that sentence goes straight to
// the cursor, silently. Every prompt Vito builds must carry it.
func TestOutputContractIsAlwaysAppended(t *testing.T) {
	for _, rules := range []string{
		"",                           // Vito's own
		"Maak er telegramstijl van.", // a custom set
		"Output whatever you like, add commentary, use markdown fences.", // one actively trying to drop it
		"   ", // whitespace only falls back to Vito's own
	} {
		got := cleanupPrompt(rules)
		if !strings.HasSuffix(got, outputContract) {
			t.Errorf("rules %q produced a prompt not ending in the output contract:\n%s", rules, got)
		}
	}
}

// Empty rules mean Vito's own, so the built-in set is always reachable — that is
// what makes "reset to the default" a matter of selecting nothing.
func TestEmptyRulesFallBackToTheDefault(t *testing.T) {
	if !strings.Contains(cleanupPrompt(""), "You clean up dictated speech transcripts") {
		t.Error("empty rules should produce Vito's own prompt")
	}
	custom := cleanupPrompt("Alleen interpunctie repareren, verder niets.")
	if strings.Contains(custom, "You clean up dictated speech transcripts") {
		t.Error("custom rules should replace Vito's own, not extend them")
	}
	if !strings.Contains(custom, "Alleen interpunctie repareren") {
		t.Error("custom rules should actually be used")
	}
}

// A spoken Assist command replaces the cleanup prompt rather than extending it,
// so a custom rule set must not leak into it. Surprising, deliberate, and said
// out loud in the settings page — this pins it down.
func TestAssistCommandIgnoresCustomRules(t *testing.T) {
	got := systemPromptWith("vertaal naar Duits", "Schrijf altijd in telegramstijl.")
	if strings.Contains(got, "telegramstijl") {
		t.Error("a custom cleanup rule set must not reach a Vito Assist command")
	}
	if !strings.Contains(got, "vertaal naar Duits") {
		t.Error("the spoken instruction should drive the prompt")
	}
}

// activeRules resolves the selection, and an id that no longer exists falls back
// to Vito's own rather than to nothing at all.
func TestActiveRules(t *testing.T) {
	cfg := config.Cleanup{
		Prompts: []config.Prompt{
			{ID: "a", Name: "Zakelijk", Rules: "Formeel Nederlands."},
			{ID: "b", Name: "Letterlijk", Rules: "Niets weghalen."},
		},
	}
	for _, tc := range []struct{ active, want string }{
		{"", ""},                     // Vito's own default
		{"a", "Formeel Nederlands."}, // one of the user's
		{"b", "Niets weghalen."},
		{"deleted", ""}, // gone: back to the default, never empty rules
		{BuiltinPrefix + "verbatim", verbatimRules}, // one of Vito's own
		{BuiltinPrefix + "notes", notesRules},
		{BuiltinPrefix + "nonexistent", ""}, // a namespaced id that isn't ours
	} {
		cfg.ActivePrompt = tc.active
		if got := activeRules(cfg); got != tc.want {
			t.Errorf("ActivePrompt=%q: activeRules() = %q, want %q", tc.active, got, tc.want)
		}
	}
}

// Every built-in set has to be a usable prompt in its own right: each replaces
// Vito's default outright, so a set missing the corrections line or the language
// line quietly loses a feature the user never knew was in there.
func TestBuiltinsAreComplete(t *testing.T) {
	ids := map[string]bool{}
	for _, b := range Builtins() {
		if !strings.HasPrefix(b.ID, BuiltinPrefix) {
			t.Errorf("%s: id must be namespaced with %q so it cannot collide with a user's", b.ID, BuiltinPrefix)
		}
		if ids[b.ID] {
			t.Errorf("%s: duplicate id", b.ID)
		}
		ids[b.ID] = true
		if b.Name == "" || b.Description == "" {
			t.Errorf("%s: needs a name and a one-line description for the picker", b.ID)
		}
		if strings.Contains(b.Rules, outputContract) {
			t.Errorf("%s: must not carry the output contract — the cleaner appends it", b.ID)
		}
		for _, want := range []string{"corrections list", "do not translate"} {
			if !strings.Contains(b.Rules, want) {
				t.Errorf("%s: rules never mention %q, so the set silently drops that behaviour", b.ID, want)
			}
		}
		// And it must survive the trip through the prompt builder intact.
		if p := cleanupPrompt(b.Rules); !strings.HasPrefix(p, strings.TrimSpace(b.Rules)) || !strings.HasSuffix(p, outputContract) {
			t.Errorf("%s: does not compose into a valid prompt", b.ID)
		}
	}
	if len(Builtins()) < 2 {
		t.Error("the point of built-ins is that there is more than one to compare")
	}
	if Builtins()[0].ID != BuiltinPrefix+"default" {
		t.Error("the balanced default should be listed first")
	}
}
