package apierr

import (
	"errors"
	"fmt"
	"testing"
)

func TestLooksLikeCredit(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   bool
	}{
		{402, "", true},         // Payment Required is decisive
		{402, "anything", true}, //
		{400, `{"error":{"message":"Your credit balance is too low"}}`, true},
		{400, "insufficient funds to complete this request", true},
		{403, "please update your billing information", true},
		{401, "invalid api key", false},           // bad key, not billing
		{429, "quota exceeded, slow down", false}, // rate limit, not empty wallet
		{200, "credit balance is fine", false},    // success isn't a failure
		{500, "insufficient memory", false},       // server error, ignore keywords
		{400, "malformed request", false},         // generic 400
	}
	for _, c := range cases {
		if got := LooksLikeCredit(c.status, c.body); got != c.want {
			t.Errorf("LooksLikeCredit(%d, %q) = %v, want %v", c.status, c.body, got, c.want)
		}
	}
}

func TestCreditProviderUnwraps(t *testing.T) {
	base := &OutOfCredit{Provider: "AssemblyAI"}
	wrapped := fmt.Errorf("transcription failed (async): %w", base)
	p, ok := CreditProvider(wrapped)
	if !ok || p != "AssemblyAI" {
		t.Fatalf("CreditProvider(wrapped) = %q,%v; want AssemblyAI,true", p, ok)
	}
	if _, ok := CreditProvider(errors.New("plain error")); ok {
		t.Fatal("plain error should not be classified as out of credit")
	}
}

func TestFromMessage(t *testing.T) {
	if e := FromMessage("Anthropic", "400 invalid_request_error: Your credit balance is too low to access the Anthropic API"); e == nil || e.Provider != "Anthropic" {
		t.Fatalf("expected Anthropic OutOfCredit, got %v", e)
	}
	if e := FromMessage("Anthropic", "overloaded_error: the service is temporarily overloaded"); e != nil {
		t.Fatalf("overload should not be credit, got %v", e)
	}
}
