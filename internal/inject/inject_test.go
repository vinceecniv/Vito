package inject

import (
	"testing"

	"vito/internal/config"
)

func TestSelectMode(t *testing.T) {
	tests := []struct {
		name string
		mode string
		want Mode
	}{
		{"paste", "paste", ModePaste},
		{"type", "type", ModeType},
		{"clipboard_only", "clipboard_only", ModeClipboardOnly},
		{"empty falls back to safe mode", "", ModeClipboardOnly},
		{"unknown falls back to safe mode", "teleport", ModeClipboardOnly},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SelectMode(config.Injection{Mode: tt.mode})
			if got != tt.want {
				t.Fatalf("SelectMode(%q) = %q, want %q", tt.mode, got, tt.want)
			}
		})
	}
}
