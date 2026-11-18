package dictionary

import "testing"

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
