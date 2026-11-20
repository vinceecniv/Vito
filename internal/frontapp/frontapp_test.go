package frontapp

import "testing"

// Prints what the system says now; it cannot assert which program that is.
func TestCurrent(t *testing.T) {
	t.Logf("%+v", Current())
}
