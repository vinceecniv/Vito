package dictionary

import (
	"testing"

	"vito/internal/config"
)

func TestSnippets(t *testing.T) {
	list := []config.Snippet{
		{Trigger: "mijn LinkedIn", Text: "https://www.linkedin.com/in/voorbeeld"},
		{Trigger: "mijn adres", Text: "Dorpsstraat 1\n1234 AB Ergens"},
		{Trigger: "adres", Text: "should lose to the longer trigger"},
		{Trigger: "leeg", Text: ""},
	}
	tests := []struct{ in, marked, out string }{
		{"Mijn LinkedIn.", "⟦1⟧", "https://www.linkedin.com/in/voorbeeld"},
		{"m'n linked in", "⟦1⟧", "https://www.linkedin.com/in/voorbeeld"},
		{"Stuur hem mijn LinkedIn, dan weet hij het.", "Stuur hem ⟦1⟧, dan weet hij het.",
			"Stuur hem https://www.linkedin.com/in/voorbeeld, dan weet hij het."},
		{"Mijn adres is mijn adres.", "⟦1⟧ is ⟦2⟧.", "Dorpsstraat 1\n1234 AB Ergens is Dorpsstraat 1\n1234 AB Ergens."},
		{"Het adresboek blijft.", "Het adresboek blijft.", "Het adresboek blijft."},
		{"Dit is leeg.", "Dit is leeg.", "Dit is leeg."},
		{"mijnLinkedInprofiel", "mijnLinkedInprofiel", "mijnLinkedInprofiel"},
	}
	for _, tt := range tests {
		marked, texts := Mark(tt.in, list)
		if marked != tt.marked {
			t.Errorf("Mark(%q) = %q, want %q", tt.in, marked, tt.marked)
		}
		if got := Expand(marked, texts); got != tt.out {
			t.Errorf("Expand(Mark(%q)) = %q, want %q", tt.in, got, tt.out)
		}
	}
}

func TestRestore(t *testing.T) {
	texts := []string{"A", "B"}
	if got, ok := Restore("x ⟦2⟧ y ⟦1⟧", texts); !ok || got != "x B y A" {
		t.Fatalf("got %q %v", got, ok)
	}
	for _, bad := range []string{"x ⟦1⟧", "⟦1⟧ ⟦1⟧ ⟦2⟧", "⟦1⟧ ⟦2⟧ ⟦3⟧"} {
		if _, ok := Restore(bad, texts); ok {
			t.Errorf("Restore(%q) accepted", bad)
		}
	}
	if _, ok := Restore("no snippets", nil); !ok {
		t.Error("plain text rejected")
	}
}

func TestOnlySnippets(t *testing.T) {
	for in, want := range map[string]bool{"⟦1⟧": true, " ⟦1⟧, ⟦2⟧.": true, "zie ⟦1⟧": false, "niets": false} {
		if OnlySnippets(in) != want {
			t.Errorf("OnlySnippets(%q) != %v", in, want)
		}
	}
}
