package stt

import "testing"

func TestSonioxTurnWords(t *testing.T) {
	tk := []sonioxTimedToken{
		{Text: "Hal", StartMS: 1000, Speaker: "1"}, {Text: "lo", StartMS: 1200, Speaker: "1"},
		{Text: " daar", StartMS: 1500, Speaker: "1"}, {Text: ".", StartMS: 1800, Speaker: "1"},
		{Text: " Ja", StartMS: 3000, Speaker: "2"}, {Text: " zeker", StartMS: 3400, Speaker: "2"},
		{Text: " nog", StartMS: 5000, Speaker: "1"},
	}
	turns := renumber(sonioxTurns(tk))
	if len(turns) != 3 {
		t.Fatalf("turns: %+v", turns)
	}
	want := [][]WordTime{{{0, 1}, {6, 1.5}}, {{0, 3}, {3, 3.4}}, {{0, 5}}}
	for i, tu := range turns {
		if len(tu.Words) != len(want[i]) {
			t.Fatalf("turn %d %q words %+v", i, tu.Text, tu.Words)
		}
		for j, w := range tu.Words {
			if w != want[i][j] {
				t.Errorf("turn %d %q word %d = %+v, want %+v", i, tu.Text, j, w, want[i][j])
			}
		}
	}
	// A speaker's turns that follow each other are joined, words shifted.
	j := renumber([]Turn{{Speaker: "A", Text: "een twee", Words: []WordTime{{0, 1}, {4, 2}}}, {Speaker: "A", Text: " drie", Words: []WordTime{{1, 3}}}})
	if len(j) != 1 || j[0].Text != "een twee drie" || len(j[0].Words) != 3 || j[0].Words[2] != (WordTime{9, 3}) {
		t.Fatalf("joined: %+v", j)
	}
}
