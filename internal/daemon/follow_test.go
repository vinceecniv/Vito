package daemon

import "testing"

// A hands-free chain types one text: each part joins onto the one before,
// and a closing full stop waits until it is clear whether more follows.
func TestJoinFollow(t *testing.T) {
	d := &Daemon{}
	first := &session{chain: true}
	first.followed.Store(true)
	if got := d.joinFollow(first, "Ik kom morgen.", false); got != "Ik kom morgen" {
		t.Fatalf("first part: %q", got)
	}
	if !d.follow.pendingDot {
		t.Fatal("the full stop should be held back")
	}

	// the model says the sentence goes on
	cont := &session{chain: true, continuation: true}
	cont.followed.Store(true)
	if got := d.joinFollow(cont, "rond een uur of drie.", true); got != " rond een uur of drie" {
		t.Fatalf("continued sentence: %q", got)
	}

	// the model starts a new sentence and closes the earlier one itself
	last := &session{chain: true, continuation: true}
	if got := d.joinFollow(last, ". Neem jij de taart mee?", true); got != ". Neem jij de taart mee?" {
		t.Fatalf("new sentence: %q", got)
	}
	if d.follow.pendingDot {
		t.Fatal("nothing should be held after a question mark")
	}
	if d.follow.before != "Ik kom morgen rond een uur of drie. Neem jij de taart mee?" {
		t.Fatalf("context: %q", d.follow.before)
	}

	// an open part's dangling dash or comma waits for what follows too
	d3 := &Daemon{}
	o := &session{chain: true}
	o.followed.Store(true)
	if got := d3.joinFollow(o, "en dan wacht ik even, en dan—", true); got != "en dan wacht ik even, en dan" || !d3.follow.pendingDot {
		t.Fatalf("open part: %q pending %v", got, d3.follow.pendingDot)
	}
	o2 := &session{chain: true, continuation: true}
	o2.followed.Store(true)
	if got := d3.joinFollow(o2, "praat ik verder,", true); got != " praat ik verder" || !d3.follow.pendingDot {
		t.Fatalf("open continuation: %q", got)
	}

	// without the model each part is its own sentence
	d2 := &Daemon{}
	a := &session{chain: true}
	a.followed.Store(true)
	d2.joinFollow(a, "Eerste zin.", false)
	b := &session{chain: true, continuation: true}
	if got := d2.joinFollow(b, "Tweede zin.", false); got != ". Tweede zin." {
		t.Fatalf("plain continuation: %q", got)
	}
}
