package cleanup

import "testing"

// A reasoning model that puts its monologue inline in the message content (Qwen
// does; gpt-oss uses a separate field) would otherwise have that monologue
// pasted as the "cleaned" transcript.
func TestStripThinking(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain text is untouched", "Dit is gewoon opgeschoonde tekst.", "Dit is gewoon opgeschoonde tekst."},
		{"qwen block", "<think>\nThe user wants X. Let me...\n</think>\n\nDit is de opgeschoonde tekst.", "Dit is de opgeschoonde tekst."},
		{"thinking tag spelling", "<thinking>hmm</thinking> Klaar.", "Klaar."},
		{"leading whitespace before the block", "  <think>hmm</think>Klaar.", "Klaar."},
		{"two blocks", "<think>a</think>Eerst.<think>b</think> Tweede.", "Eerst. Tweede."},
		// An unclosed block is a truncated answer: nothing is stripped, and the
		// empty/garbled result is caught by the checks around this.
		{"unclosed block stays", "<think>ik denk nog steeds", "<think>ik denk nog steeds"},
		{"angle brackets that aren't a block", "Gebruik <think> als voorbeeld.", "Gebruik <think> als voorbeeld."},
	}
	for _, c := range cases {
		if got := stripThinking(c.in); got != c.want {
			t.Errorf("%s: stripThinking(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// The output budget has to cover a reasoning model's monologue, which does not
// scale with the input: measured on Groq, one 40-word Dutch dictation cost
// gpt-oss-20b 374 output tokens and qwen3.6-27b 1574.
func TestMaxTokensForLeavesReasoningHeadroom(t *testing.T) {
	short := maxTokensFor("Dit is een kort dictaat van een stuk of tien woorden ongeveer.")
	if short < 1024 {
		t.Errorf("a short dictation gets %d output tokens; a reasoning model needs far more before it writes a word", short)
	}
	long := maxTokensFor(string(make([]byte, 100_000)))
	if long != 4096 {
		t.Errorf("maxTokensFor caps at 4096, got %d", long)
	}
	if maxTokensFor("aa") > maxTokensFor("aaaa") {
		t.Error("the budget must still grow with the input")
	}
}
