package dictionary

import (
	"reflect"
	"testing"

	"vito/internal/config"
)

func TestApply(t *testing.T) {
	corrections := []config.Correction{
		{Wrong: "focus", Right: "Acme"},
		{Wrong: "instagrate", Right: "Instagr8"},
		{Wrong: "sap", Right: "SAP"},
	}
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"simple replacement", "ik werk bij focus", "ik werk bij Acme"},
		{"case insensitive", "Focus is een bedrijf", "Acme is een bedrijf"},
		{"word boundary respected", "focussen op het werk", "focussen op het werk"},
		{"multiple corrections", "focus gebruikt instagrate en sap", "Acme gebruikt Instagr8 en SAP"},
		{"punctuation adjacent", "dat doet focus, toch?", "dat doet Acme, toch?"},
		{"no match unchanged", "niets te corrigeren hier", "niets te corrigeren hier"},
		{"empty text", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Apply(tt.in, corrections); got != tt.want {
				t.Fatalf("Apply(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestApplyEmptyCorrectionSkipped(t *testing.T) {
	got := Apply("tekst blijft gelijk", []config.Correction{{Wrong: "", Right: "x"}, {Wrong: "y", Right: ""}})
	if got != "tekst blijft gelijk" {
		t.Fatalf("empty corrections must be ignored, got %q", got)
	}
}

func TestKeyterms(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"dedup case insensitive", []string{"Acme", "acme", "SAP"}, []string{"Acme", "SAP"}},
		{"trims and drops empties", []string{" Instagr8 ", "", "  "}, []string{"Instagr8"}},
		{"nil in nil out", nil, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Keyterms(config.Dictionary{Keyterms: tt.in})
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Keyterms(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestKeytermsCap(t *testing.T) {
	terms := make([]string, 150)
	for i := range terms {
		terms[i] = "term" + string(rune('a'+i%26)) + string(rune('0'+i/26))
	}
	got := Keyterms(config.Dictionary{Keyterms: terms})
	if len(got) > 100 {
		t.Fatalf("keyterms must be capped at 100, got %d", len(got))
	}
}
