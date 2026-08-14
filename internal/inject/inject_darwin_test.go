//go:build darwin

package inject

import "testing"

// TestClipboardRoundTrip exercises the NSPasteboard bridge end to end. It is
// the one part of the macOS injection path that can be tested without the
// Accessibility right — synthesising keystrokes cannot, since macOS silently
// drops the events until a human grants the permission by hand.
//
// The sample text is deliberately awkward: an em dash and an emoji (a UTF-16
// surrogate pair) catch the encoding mistakes that plain ASCII would hide.
func TestClipboardRoundTrip(t *testing.T) {
	const want = "Vito — dictation test 🎙️ with \"quotes\""

	if err := setClipboardText(want); err != nil {
		t.Fatalf("setClipboardText: %v", err)
	}
	got, ok := ReadClipboard()
	if !ok {
		t.Fatal("ReadClipboard reported no text on the pasteboard")
	}
	if got != want {
		t.Errorf("round trip mismatch:\n got %q\nwant %q", got, want)
	}
}
