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
