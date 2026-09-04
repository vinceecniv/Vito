package cleanup

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vito/internal/config"
)

// serveJSON stands in for an OpenAI-compatible endpoint, handing the request
// body back to the test so it can assert on what Vito actually sent.
func serveJSON(t *testing.T, status int, body string, seen *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var got map[string]any
		_ = json.Unmarshal(raw, &got)
		*seen = got
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
}

const okReply = `{"choices":[{"message":{"content":"Dit is de opgeschoonde tekst."},"finish_reason":"stop"}],"usage":{"prompt_tokens":80,"completion_tokens":9}}`

// reasoning_effort is omitted unless configured: most endpoints have never heard
// of it and answer 400, so an unset value must not reach the wire at all.
func TestReasoningEffortOnlySentWhenSet(t *testing.T) {
	for _, tc := range []struct {
		name, effort string
		wantSent     any
	}{
		{"unset stays off the wire", "", nil},
		{"set is passed through", "none", "none"},
		{"whitespace counts as unset", "   ", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var seen map[string]any
			srv := serveJSON(t, http.StatusOK, okReply, &seen)
			defer srv.Close()
			c := NewOpenAICleaner(config.Cleanup{OpenAIBaseURL: srv.URL, OpenAIModel: "m", ReasoningEffort: tc.effort})
			if _, _, err := c.Clean(context.Background(), "wat tekst", "nl", nil, ""); err != nil {
				t.Fatalf("Clean: %v", err)
			}
			if got := seen["reasoning_effort"]; got != tc.wantSent {
				t.Errorf("request carried reasoning_effort=%v, want %v", got, tc.wantSent)
			}
		})
	}
}

// Which values an endpoint takes is per model — Groq accepts "none" for Qwen and
// rejects it for gpt-oss — so a rejection has to name the setting that caused it
// instead of surfacing as an anonymous HTTP 400.
func TestRejectedReasoningEffortNamesTheSetting(t *testing.T) {
	var seen map[string]any
	srv := serveJSON(t, http.StatusBadRequest,
		`{"error":{"message":"`+"`reasoning_effort`"+` must be one of `+"`low`"+`, `+"`medium`"+`, or `+"`high`"+`"}}`, &seen)
	defer srv.Close()
	c := NewOpenAICleaner(config.Cleanup{OpenAIBaseURL: srv.URL, OpenAIModel: "openai/gpt-oss-120b", ReasoningEffort: "none"})
	_, _, err := c.Clean(context.Background(), "wat tekst", "nl", nil, "")
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"thinking", `"none"`, "must be one of"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// The whole chain, on the wire: a rule set selected in the config decides the
// system message, and the output contract rides along with it whatever the user
// wrote. This is the step between config.Cleanup and the request that no amount
// of unit-testing cleanupPrompt() alone would cover.
func TestActiveRuleSetReachesTheRequest(t *testing.T) {
	var seen map[string]any
	srv := serveJSON(t, http.StatusOK, okReply, &seen)
	defer srv.Close()
	c := NewOpenAICleaner(config.Cleanup{
		OpenAIBaseURL: srv.URL, OpenAIModel: "m",
		Prompts:      []config.Prompt{{ID: "p1", Name: "Letterlijk", Rules: "Alleen interpunctie repareren."}},
		ActivePrompt: "p1",
	})
	if _, _, err := c.Clean(context.Background(), "wat tekst", "nl", nil, ""); err != nil {
		t.Fatalf("Clean: %v", err)
	}
	msgs, _ := seen["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatal("no messages in the request")
	}
	system, _ := msgs[0].(map[string]any)["content"].(string)
	if !strings.Contains(system, "Alleen interpunctie repareren.") {
		t.Errorf("system message does not carry the selected rules:\n%s", system)
	}
	if strings.Contains(system, "You clean up dictated speech transcripts") {
		t.Error("the selected rules should replace Vito's own, not sit alongside them")
	}
	if !strings.HasSuffix(system, outputContract) {
		t.Errorf("system message does not end in the output contract:\n%s", system)
	}
}

// A reasoning model's inline monologue must never reach the clipboard, and a
// reply that ran out of budget must be reported as truncated rather than pasted
// as whatever fragment survived.
func TestOpenAICleanStripsThinkingAndReportsTruncation(t *testing.T) {
	var seen map[string]any
	srv := serveJSON(t, http.StatusOK,
		`{"choices":[{"message":{"content":"<think>Let me consider…</think>\n\nDit is de opgeschoonde tekst."},"finish_reason":"stop"}],"usage":{"prompt_tokens":80,"completion_tokens":900}}`, &seen)
	defer srv.Close()
	c := NewOpenAICleaner(config.Cleanup{OpenAIBaseURL: srv.URL, OpenAIModel: "qwen"})
	got, _, err := c.Clean(context.Background(), "wat tekst", "nl", nil, "")
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if got != "Dit is de opgeschoonde tekst." {
		t.Errorf("cleaned = %q, want the answer without the <think> block", got)
	}

	var seen2 map[string]any
	srv2 := serveJSON(t, http.StatusOK,
		`{"choices":[{"message":{"content":"<think>Still thinking and thinking"},"finish_reason":"length"}],"usage":{"prompt_tokens":80,"completion_tokens":2089}}`, &seen2)
	defer srv2.Close()
	c2 := NewOpenAICleaner(config.Cleanup{OpenAIBaseURL: srv2.URL, OpenAIModel: "qwen"})
	_, usage, err := c2.Clean(context.Background(), "wat tekst", "nl", nil, "")
	if err == nil {
		t.Fatal("a truncated reply must be an error, not a fragment")
	}
	if !strings.Contains(err.Error(), "2089") || !strings.Contains(err.Error(), "thinking") {
		t.Errorf("error %q should report the tokens spent and name the setting", err)
	}
	// The tokens were spent and are billed, so they must still be reported.
	if usage.OutputTokens != 2089 {
		t.Errorf("usage.OutputTokens = %d, want 2089 even on failure", usage.OutputTokens)
	}
}
