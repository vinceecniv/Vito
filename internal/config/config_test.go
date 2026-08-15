package config

import (
	"path/filepath"
	"testing"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"defaults are valid", func(c *Config) {}, false},
		{"port zero", func(c *Config) { c.Server.Port = 0 }, true},
		{"port too high", func(c *Config) { c.Server.Port = 70000 }, true},
		{"gain too low", func(c *Config) { c.Audio.InputGain = 0 }, true},
		{"gain too high", func(c *Config) { c.Audio.InputGain = 11 }, true},
		{"gain edge low ok", func(c *Config) { c.Audio.InputGain = 0.1 }, false},
		{"injection mode paste", func(c *Config) { c.Injection.Mode = "paste" }, false},
		{"injection mode type", func(c *Config) { c.Injection.Mode = "type" }, false},
		{"injection mode clipboard_only", func(c *Config) { c.Injection.Mode = "clipboard_only" }, false},
		{"injection mode bogus", func(c *Config) { c.Injection.Mode = "teleport" }, true},
		{"empty language", func(c *Config) { c.STT.Language = "" }, true},
		{"auto language ok", func(c *Config) { c.STT.Language = "auto" }, false},
		{"media_action duck", func(c *Config) { c.Audio.MediaAction = "duck" }, false},
		{"media_action pause", func(c *Config) { c.Audio.MediaAction = "pause" }, false},
		{"media_action off", func(c *Config) { c.Audio.MediaAction = "off" }, false},
		{"media_action empty ok", func(c *Config) { c.Audio.MediaAction = "" }, false},
		{"media_action bogus", func(c *Config) { c.Audio.MediaAction = "mute" }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.mutate(cfg)
			err := cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoadCreatesConfigWithToken(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("VITO_CONFIG", p)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() on fresh path: %v", err)
	}
	if len(cfg.Server.Token) != 64 {
		t.Fatalf("expected 64-char hex token, got %q", cfg.Server.Token)
	}

	again, err := Load()
	if err != nil {
		t.Fatalf("Load() second time: %v", err)
	}
	if again.Server.Token != cfg.Server.Token {
		t.Fatal("token changed between loads")
	}
}

func TestRetireGroqModel(t *testing.T) {
	const groq = "https://api.groq.com/openai/v1"
	tests := []struct {
		name        string
		base, model string
		want        string
		wantMoved   bool
	}{
		{"the old default moves to its named replacement", groq, "llama-3.3-70b-versatile", "openai/gpt-oss-120b", true},
		{"the small Llama moves to the small replacement", groq, "llama-3.1-8b-instant", "openai/gpt-oss-20b", true},
		{"a model Groq still serves is left alone", groq, "openai/gpt-oss-120b", "openai/gpt-oss-120b", false},
		{"an unknown model on Groq is left alone", groq, "something-else", "something-else", false},
		// The same names elsewhere are still valid: a local Ollama serving
		// llama-3.3-70b has nothing to do with Groq's decommission, and renaming
		// it would break a working setup to fix one that isn't broken.
		{"the same name on another endpoint is untouched", "http://localhost:11434/v1", "llama-3.3-70b-versatile", "llama-3.3-70b-versatile", false},
		{"an empty endpoint is untouched", "", "llama-3.3-70b-versatile", "llama-3.3-70b-versatile", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Cleanup{OpenAIBaseURL: tt.base, OpenAIModel: tt.model}
			if moved := retireGroqModel(&c); moved != tt.wantMoved {
				t.Errorf("moved = %v, want %v", moved, tt.wantMoved)
			}
			if c.OpenAIModel != tt.want {
				t.Errorf("model = %q, want %q", c.OpenAIModel, tt.want)
			}
		})
	}
}

// A config written before the decommission must come back with a model that
// still exists — including Assist's own, which is configured separately.
func TestLoadRetiresGroqModels(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("VITO_CONFIG", p)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	cfg.Cleanup.OpenAIModel = "llama-3.3-70b-versatile"
	cfg.Assist.Cleanup.OpenAIBaseURL = "https://api.groq.com/openai/v1"
	cfg.Assist.Cleanup.OpenAIModel = "llama-3.1-8b-instant"
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save(): %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() after write: %v", err)
	}
	if got.Cleanup.OpenAIModel != "openai/gpt-oss-120b" {
		t.Errorf("cleanup model = %q, want openai/gpt-oss-120b", got.Cleanup.OpenAIModel)
	}
	if got.Assist.Cleanup.OpenAIModel != "openai/gpt-oss-20b" {
		t.Errorf("assist model = %q, want openai/gpt-oss-20b", got.Assist.Cleanup.OpenAIModel)
	}
}
