package cleanup

import "testing"

func TestPlain(t *testing.T) {
	cases := map[string]string{
		"single sign\u2011on":           "single sign-on",
		"10\u00A0km, 5\u202F%":          "10 km, 5 %",
		"af\u00ADbreken\u200B":          "afbreken",
		"zo \u2013 en zo \u2014 klaar":  "zo \u2013 en zo \u2014 klaar",
		"\uFEFFgewone tekst, niets mis": "gewone tekst, niets mis",
	}
	for in, want := range cases {
		if got := Plain(in); got != want {
			t.Errorf("Plain(%q) = %q, want %q", in, got, want)
		}
	}
}
