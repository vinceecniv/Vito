package stt

import "testing"

func TestSonioxTurns(t *testing.T) {
	tk := []sonioxTimedToken{
		{Text: "Hallo", StartMS: 0, EndMS: 400, Speaker: "2"},
		{Text: " allemaal.", StartMS: 400, EndMS: 900, Speaker: "2"},
		{Text: " Dag", StartMS: 1200, EndMS: 1500, Speaker: "1"},
		{Text: "!", StartMS: 1500, EndMS: 1550},
		{Text: "<end>", StartMS: 1550, EndMS: 1550},
		{Text: " Ja.", StartMS: 2000, EndMS: 2300, Speaker: "2"},
		{Text: " Goed.", StartMS: 2300, EndMS: 2600, Speaker: "2"},
	}
	got := renumber(sonioxTurns(tk))
	if len(got) != 3 {
		t.Fatalf("%d turns: %+v", len(got), got)
	}
	if got[0].Speaker != "1" || got[0].Text != "Hallo allemaal." || got[0].End != 0.9 {
		t.Fatalf("turn 0: %+v", got[0])
	}
	if got[1].Speaker != "2" || got[1].Text != "Dag!" || got[1].Start != 1.2 {
		t.Fatalf("turn 1: %+v", got[1])
	}
	if got[2].Speaker != "1" || got[2].Text != "Ja. Goed." {
		t.Fatalf("turn 2: %+v", got[2])
	}
}

func TestRenumberJoins(t *testing.T) {
	got := renumber([]Turn{{Speaker: "B", Text: "a"}, {Speaker: "B", Text: "b", End: 3}, {Speaker: "A", Text: " "}, {Speaker: "C", Text: "c"}})
	if len(got) != 2 || got[0].Text != "a b" || got[0].End != 3 || got[1].Speaker != "2" {
		t.Fatalf("%+v", got)
	}
}
