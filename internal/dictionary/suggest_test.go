package dictionary

import (
	"slices"
	"testing"

	"vito/internal/config"
)

func TestSuggest(t *testing.T) {
	var pairs []Pair
	add := func(n int, raw, cleaned string) {
		for range n {
			pairs = append(pairs, Pair{raw, cleaned})
		}
	}
	// A misheard term the cleanup keeps repairing, also heard right elsewhere.
	add(4, "we sturen het in conciatie naar de klant", "We sturen het in consignatie naar de klant.")
	add(2, "de consignatie loopt af", "De consignatie loopt af.")
	// A common word the cleanup sometimes rephrases: too often left alone.
	add(3, "kan je dat regelen", "Kun je dat regelen?")
	add(10, "dat kan je zelf", "Dat kan je zelf.")
	// Only a hyphen: no suggestion.
	add(5, "stuur een e-mail", "Stuur een e mail.")
	// A name, once, sounding the same.
	add(1, "bel Focas even", "Bel Vocas even.")
	// A name split in two, joined by the cleanup.
	add(1, "de price slurper draait", "De Priceslurper draait.")
	// A one-off rephrasing that is not a name.
	add(1, "het is niks", "Het is niets.")

	got := Suggest(pairs, nil, 0)
	want := map[string]string{"conciatie": "consignatie", "focas": "Vocas", "price slurper": "Priceslurper"}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for _, s := range got {
		if want[s.Wrong] != s.Right {
			t.Errorf("unexpected %+v", s)
		}
	}
	if got[0].Wrong != "conciatie" || got[0].Count != 4 || got[0].Heard != 2 {
		t.Errorf("first = %+v", got[0])
	}
	if s := Suggest(pairs, func(w, r string) bool { return w == "conciatie" }, 0); len(s) != 2 {
		t.Errorf("skip ignored: %+v", s)
	}
}

func TestPhonetic(t *testing.T) {
	for _, c := range [][2]string{{"Focas", "Vocas"}, {"Vokas", "Vocas"}} {
		if phonetic(c[0]) != phonetic(c[1]) {
			t.Errorf("%s ≠ %s: %s %s", c[0], c[1], phonetic(c[0]), phonetic(c[1]))
		}
	}
	if phonetic("Kubernetes") == phonetic("Vocas") {
		t.Error("unlike words share a key")
	}
}

func TestVariants(t *testing.T) {
	var pairs []Pair
	add := func(n int, raw, cleaned string) {
		for range n {
			pairs = append(pairs, Pair{raw, cleaned})
		}
	}
	add(1, "staat Pricelerper op GitHub", "Staat Pricelerper op GitHub?") // left wrong
	add(1, "hoe Kama Express nu draait", "Hoe Kamera-express nu draait.") // repaired
	add(1, "staat in Windmill", "Staat in Windmill.")                     // the term itself
	add(1, "de consignaties", "De consignaties.")                         // contains the term
	add(1, "meer op een toets drukken", "Meer op een toets drukken.")     // sounds alike only
	add(4, "zie de bijlage", "Zie de bijlage.")                           // a word of its own
	add(5, "de reden is de detectie", "De reden is de detectie.")         // a common word added
	terms := []string{"Priceslurper", "Kamera-express", "Windmill", "consignatie", "tray-icon", "bijlagen", "einddetectie"}
	got := map[string]Suggestion{}
	for _, s := range Variants(pairs, terms, nil) {
		got[s.Wrong] = s
	}
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	if s := got["pricelerper"]; s.Right != "Priceslurper" || s.Left != 1 || s.Kind != "variant" {
		t.Errorf("pricelerper: %+v", s)
	}
	if s := got["kama express"]; s.Right != "Kamera-express" || s.Left != 0 {
		t.Errorf("kama express: %+v", s)
	}
	if v := Variants(pairs, terms, func(w, r string) bool { return w == "pricelerper" }); len(v) != 1 {
		t.Errorf("skip ignored: %+v", v)
	}
}

func TestMerge(t *testing.T) {
	a := []Suggestion{{Wrong: "x", Score: 1, Kind: "repair"}, {Wrong: "y", Score: 5}}
	b := []Suggestion{{Wrong: "x", Score: 3, Kind: "variant"}, {Wrong: "z", Score: 2}}
	m := Merge(2, a, b)
	if len(m) != 2 || m[0].Wrong != "y" || m[1].Wrong != "x" || m[1].Kind != "variant" {
		t.Errorf("%+v", m)
	}
}

func TestRelevant(t *testing.T) {
	cs := []config.Correction{
		{Wrong: "focas", Right: "Vocas"},
		{Wrong: "price slurper", Right: "Priceslurper"},
		{Wrong: "kama express", Right: "Kamera-express"},
		{Wrong: "omarchi", Right: "Omarchy"},
		{Wrong: "vito", Right: "Vito"},
		{Wrong: "ai", Right: "AI"},
	}
	got := func(text string) []string {
		var out []string
		for _, c := range Relevant(text, cs) {
			out = append(out, c.Right)
		}
		return out
	}
	cases := map[string][]string{
		"Stuur het naar Vocas en de Pricelerper.":        {"Vocas", "Priceslurper"},
		"Ik heb bij camera express een lens gekocht.":    {"Kamera-express"},
		"Het nieuwe Vocaslogo staat online.":             {"Vocas"},
		"Een gewone zin zonder bijzondere woorden erin.": nil,
		"Vraag het aan AI.":                              {"AI"},
	}
	for text, want := range cases {
		if g := got(text); !slices.Equal(g, want) {
			t.Errorf("%q: got %v, want %v", text, g, want)
		}
	}
}

func TestTermsAndPick(t *testing.T) {
	cs := []config.Correction{
		{Wrong: "focas", Right: "Vocas", Created: 1},
		{Wrong: "vokas", Right: "vocas", Created: 2},
		{Wrong: "omarchi", Right: "Omarchy", Created: 5},
		{Wrong: "kama express", Right: "Kamera-express", Created: 3},
	}
	use := map[string][2]int64{"kama express": {4, 10}, "focas": {2, 50}, "vokas": {3, 20}}
	got := Terms(cs, func(w string) (int, int64) { u := use[w]; return int(u[0]), u[1] })
	if want := []string{"Vocas", "Kamera-express", "Omarchy"}; !slices.Equal(got, want) {
		t.Errorf("Terms = %v, want %v", got, want)
	}
	if p := Pick(got, 2, 0, 0, 0); !slices.Equal(p, []string{"Vocas", "Kamera-express"}) {
		t.Errorf("Pick n = %v", p)
	}
	if p := Pick(got, 0, 7, 0, 0); !slices.Equal(p, []string{"Vocas", "Omarchy"}) {
		t.Errorf("Pick maxLen = %v", p)
	}
	if p := Pick(got, 0, 0, 0, 21); !slices.Equal(p, []string{"Vocas", "Kamera-express"}) {
		t.Errorf("Pick total = %v", p)
	}
}
