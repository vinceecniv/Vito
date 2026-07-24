// Package apierr classifies provider API failures that the UI needs to treat
// specially — currently a depleted balance ("out of credit"), which is a
// billing problem the user must fix at the provider, not a bug in Vito.
package apierr

import (
	"errors"
	"strings"
)

// OutOfCredit means a provider rejected the request because the account has no
// remaining balance/quota. Provider is a display name ("AssemblyAI", "Soniox",
// "Anthropic") so the UI can name it and link to the right billing page.
type OutOfCredit struct {
	Provider string
	// Detail is the raw provider message, kept for logs (not shown to the user).
	Detail string
}

func (e *OutOfCredit) Error() string {
	if e.Detail != "" {
		return e.Provider + " account is out of credit: " + e.Detail
	}
	return e.Provider + " account is out of credit"
}

// CreditProvider reports the provider name if err is (or wraps) an OutOfCredit.
func CreditProvider(err error) (string, bool) {
	var e *OutOfCredit
	if errors.As(err, &e) {
		return e.Provider, true
	}
	return "", false
}

// billingWords are strings a provider puts in an out-of-balance error. Kept
// narrow so a transient rate-limit ("quota exceeded, retry") isn't mistaken for
// an empty wallet — a 402 is the unambiguous signal and matches on its own.
var billingWords = []string{
	"insufficient", "credit balance", "out of credit", "no credit",
	"insufficient funds", "billing", "payment required", "top up", "top-up",
}

// LooksLikeCredit reports whether an HTTP status and body indicate a depleted
// balance. HTTP 402 (Payment Required) is decisive; otherwise a 4xx whose body
// mentions billing counts.
func LooksLikeCredit(status int, body string) bool {
	if status == 402 {
		return true
	}
	if status < 400 || status >= 500 {
		return false
	}
	b := strings.ToLower(body)
	for _, w := range billingWords {
		if strings.Contains(b, w) {
			return true
		}
	}
	return false
}

// FromHTTP returns an *OutOfCredit when the status/body looks like a billing
// problem, otherwise nil — so callers can `if e := FromHTTP(...); e != nil`.
func FromHTTP(provider string, status int, body string) *OutOfCredit {
	if LooksLikeCredit(status, body) {
		return &OutOfCredit{Provider: provider, Detail: strings.TrimSpace(body)}
	}
	return nil
}

// FromMessage classifies an error whose only signal is its message text — an
// SDK error that has already formatted the provider's response, where no HTTP
// status is separately available. Returns nil when it doesn't read as billing.
func FromMessage(provider, msg string) *OutOfCredit {
	b := strings.ToLower(msg)
	for _, w := range billingWords {
		if strings.Contains(b, w) {
			return &OutOfCredit{Provider: provider, Detail: strings.TrimSpace(msg)}
		}
	}
	return nil
}
