package daemon

import (
	"reflect"
	"testing"
)

// A dismissed out-of-credit warning must stay dismissed for as long as that
// account is still empty — including across repeat failures, which is the whole
// point — and must come back the next time it actually runs dry.
func TestDismissCreditLastsUntilItRunsOutAgain(t *testing.T) {
	d := &Daemon{}

	d.markCredit("Anthropic", true)
	if got := d.CreditOut(); !reflect.DeepEqual(got, []string{"Anthropic"}) {
		t.Fatalf("after running out: CreditOut() = %v, want [Anthropic]", got)
	}

	d.DismissCredit("")
	if got := d.CreditOut(); len(got) != 0 {
		t.Fatalf("after dismissing: CreditOut() = %v, want none", got)
	}

	// Dictating again with the same empty account must not bring the card back.
	d.markCredit("Anthropic", true)
	if got := d.CreditOut(); len(got) != 0 {
		t.Fatalf("after a repeat failure: CreditOut() = %v, want none", got)
	}

	// A second provider is news of its own, and says so.
	d.markCredit("Soniox", true)
	if got := d.CreditOut(); !reflect.DeepEqual(got, []string{"Soniox"}) {
		t.Fatalf("after a second provider ran out: CreditOut() = %v, want [Soniox]", got)
	}

	// Credit is back, then gone again: the warning has earned its place anew.
	d.markCredit("Anthropic", false)
	d.markCredit("Anthropic", true)
	if got := d.CreditOut(); !reflect.DeepEqual(got, []string{"Anthropic", "Soniox"}) {
		t.Fatalf("after topping up and running out again: CreditOut() = %v, want [Anthropic Soniox]", got)
	}
}

// Dismissing one provider must leave the others' warnings standing.
func TestDismissCreditOneProvider(t *testing.T) {
	d := &Daemon{}
	d.markCredit("Anthropic", true)
	d.markCredit("Soniox", true)
	d.DismissCredit("Anthropic")
	if got := d.CreditOut(); !reflect.DeepEqual(got, []string{"Soniox"}) {
		t.Fatalf("CreditOut() = %v, want [Soniox]", got)
	}
}
