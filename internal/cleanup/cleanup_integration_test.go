package cleanup

// Thin integration test, guarded by VITO_TEST_ANTHROPIC_KEY:
//
//	VITO_TEST_ANTHROPIC_KEY=<key> go test ./internal/cleanup/ -run Integration -v

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"vito/internal/config"
)

func TestIntegrationClean(t *testing.T) {
	key := os.Getenv("VITO_TEST_ANTHROPIC_KEY")
	if key == "" {
		t.Skip("VITO_TEST_ANTHROPIC_KEY not set")
	}
	cfg := config.Cleanup{APIKey: key, Model: "claude-haiku-4-5", TimeoutMS: 10000}
	c := NewAnthropicCleaner(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	raw := "eh nou ja ik wilde eigenlijk eh zeggen dat het project bij focus goed loopt nieuwe regel groeten vincent"
	got, _, err := c.Clean(ctx, raw, "nl", []config.Correction{{Wrong: "focus", Right: "Acme"}})
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	t.Logf("cleaned: %q", got)
	if strings.Contains(strings.ToLower(got), "eh ") {
		t.Errorf("fillers not removed: %q", got)
	}
	if !strings.Contains(got, "Acme") {
		t.Errorf("correction not applied: %q", got)
	}
	if !strings.Contains(got, "\n") {
		t.Errorf("spoken 'nieuwe regel' not interpreted as newline: %q", got)
	}
}
