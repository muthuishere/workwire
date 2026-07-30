package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAutoCreateAndEnvOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WORKWIRE_CONFIG_DIR", dir)
	t.Setenv("WORKWIRE_LAST_MESSAGES", "3")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	// file auto-created with defaults on first run
	b, err := os.ReadFile(filepath.Join(dir, "workwire.json"))
	if err != nil {
		t.Fatalf("workwire.json not auto-created: %v", err)
	}
	var onDisk map[string]any
	if err := json.Unmarshal(b, &onDisk); err != nil {
		t.Fatal(err)
	}
	if onDisk["authMode"] != "token" {
		t.Fatalf("default authMode: %v", onDisk["authMode"])
	}
	if onDisk["port"].(float64) != 14411 {
		t.Fatalf("default port: %v", onDisk["port"])
	}
	if onDisk["lastMessages"].(float64) != 5 {
		t.Fatalf("file keeps documented default: %v", onDisk["lastMessages"])
	}
	// env override beats file
	if cfg.LastMessages != 3 {
		t.Fatalf("env override lost: %d", cfg.LastMessages)
	}
	if cfg.WaitDefault != 25 || cfg.TTLSeconds != 120 || cfg.HeartbeatSeconds != 30 || cfg.ContextCap != 20 {
		t.Fatalf("defaults wrong: %+v", cfg)
	}
}

func TestFileValueUsedWithoutEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WORKWIRE_CONFIG_DIR", dir)
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "workwire.json"), []byte(`{"lastMessages": 7, "port": 15000}`), 0o644)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LastMessages != 7 || cfg.Port != 15000 {
		t.Fatalf("file values not honored: %+v", cfg)
	}
}

func TestOpenPlusExposedFailsClosed(t *testing.T) {
	cases := []struct {
		name    string
		mode    string
		exposed bool
		wantErr bool
	}{
		{"open not exposed ok", "open", false, false},
		{"open exposed refused", "open", true, true},
		{"token exposed ok", "token", true, false},
		{"invalid mode refused", "yolo", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := Defaults()
			cfg.AuthMode = c.mode
			cfg.Exposed = c.exposed
			err := cfg.Validate()
			if (err != nil) != c.wantErr {
				t.Fatalf("Validate() err=%v wantErr=%v", err, c.wantErr)
			}
		})
	}
}
