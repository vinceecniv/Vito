package main

import "testing"

func TestNiriFloatEvent(t *testing.T) {
	f, ok := niriFloatEvent([]byte(`{"WindowsChanged":{"windows":[{"id":1,"pid":9,"is_floating":false},{"id":2,"pid":7,"is_floating":true}]}}`), 7)
	if !ok || !f {
		t.Errorf("WindowsChanged: %v %v", f, ok)
	}
	f, ok = niriFloatEvent([]byte(`{"WindowOpenedOrChanged":{"window":{"id":2,"pid":7,"is_floating":false}}}`), 7)
	if !ok || f {
		t.Errorf("WindowOpenedOrChanged: %v %v", f, ok)
	}
	if _, ok = niriFloatEvent([]byte(`{"WindowFocusChanged":{"id":2}}`), 7); ok {
		t.Error("an unrelated event counted")
	}
}
