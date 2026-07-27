package daemon

import "testing"

func TestParseCommand(t *testing.T) {
	cases := []struct {
		in    string
		instr string
		ok    bool
	}{
		{"Vito, vertaal naar Duits", "vertaal naar Duits", true},
		{"Vito vertaal naar Duits", "vertaal naar Duits", true},
		{"vito maak hier markdown van", "maak hier markdown van", true},
		{"Vido, vertaal naar Engels", "vertaal naar Engels", true}, // recogniser may hear "Vido"
		{"Vito", "", false},                         // wake word only, no instruction
		{"Vitowski is een naam", "", false},         // no boundary after the wake word
		{"Ik vroeg Vito om te vertalen", "", false}, // wake word not at the start
		{"Dit is gewoon dicteertekst.", "", false},
		{"Vito, dit is een veel te lange uiting die eigenlijk gewoon gedicteerde tekst is en dus geen commando kan zijn hoor", "", false}, // > 15 words
	}
	for _, c := range cases {
		instr, ok := parseCommand(c.in)
		if ok != c.ok || instr != c.instr {
			t.Errorf("parseCommand(%q) = (%q, %v), want (%q, %v)", c.in, instr, ok, c.instr, c.ok)
		}
	}
}
