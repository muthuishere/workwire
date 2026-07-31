package main

import (
	"testing"

	"github.com/muthuishere/workwire/internal/config"
)

// skill.json's hubUrl / tokenEnv were unmarshalled and never read: setting
// them changed nothing, silently, while help and the docs advertised them.
// They are now resolved in exactly one place, with a stated precedence:
// flag > WORKWIRE_* env > skill.json > workwire.json > defaults.
func TestSkillConfigPrecedence(t *testing.T) {
	base := func() config.Config {
		c := config.Defaults()
		c.HubURL = "http://127.0.0.1:14411" // as if from workwire.json
		c.TokenEnv = "WORKWIRE_TOKEN"
		return c
	}

	t.Run("skill.json beats workwire.json", func(t *testing.T) {
		cfg := base()
		applySkillConfig(&cfg, skillConfig{HubURL: "https://team-hub.example.com", TokenEnv: "TEAM_HUB_TOKEN"})
		if cfg.HubURL != "https://team-hub.example.com" {
			t.Fatalf("skill.json hubUrl ignored: %q", cfg.HubURL)
		}
		if cfg.TokenEnv != "TEAM_HUB_TOKEN" {
			t.Fatalf("skill.json tokenEnv ignored: %q", cfg.TokenEnv)
		}
	})

	t.Run("env beats skill.json", func(t *testing.T) {
		t.Setenv("WORKWIRE_HUB_URL", "http://127.0.0.1:19999")
		t.Setenv("WORKWIRE_TOKEN_ENV", "ENV_NAMED_TOKEN")
		cfg := base()
		cfg.HubURL = "http://127.0.0.1:19999" // config.Load already applied the env
		cfg.TokenEnv = "ENV_NAMED_TOKEN"
		applySkillConfig(&cfg, skillConfig{HubURL: "https://team-hub.example.com", TokenEnv: "TEAM_HUB_TOKEN"})
		if cfg.HubURL != "http://127.0.0.1:19999" || cfg.TokenEnv != "ENV_NAMED_TOKEN" {
			t.Fatalf("skill.json overrode a deliberate env override: %+v", cfg)
		}
	})

	t.Run("empty keys change nothing", func(t *testing.T) {
		cfg := base()
		applySkillConfig(&cfg, skillConfig{AutoJoin: true})
		if cfg.HubURL != "http://127.0.0.1:14411" || cfg.TokenEnv != "WORKWIRE_TOKEN" {
			t.Fatalf("empty skill.json keys must not touch the config: %+v", cfg)
		}
	})

	// The whole point of wiring it: a remote hub named in skill.json is
	// governed by the same credential rule as one named anywhere else.
	t.Run("a remote hub from skill.json still refuses the local admin token", func(t *testing.T) {
		dir := t.TempDir()
		seedAdminToken(t, dir)
		cfg := base()
		cfg.ConfigDir = dir
		applySkillConfig(&cfg, skillConfig{HubURL: "https://team-hub.example.com"})
		c := newClient(cfg)
		if c.token != "" || c.authErr == nil {
			t.Fatalf("skill.json's remote hub was handed the local admin token: %q", c.token)
		}
	})
}
