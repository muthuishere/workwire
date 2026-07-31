package main

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/muthuishere/workwire/internal/config"
)

// skillConfigName is the CLIENT-side config. The hub's config (workwire.json)
// is a different thing with a different lifecycle; mixing them would tie a
// session's client preferences to how the hub is served.
const skillConfigName = "skill.json"

// skillConfig is ~/.config/workwire/skill.json.
//
// There is deliberately NO auto-join key here any more. A session joins
// because its own repo's CLAUDE.md / AGENTS.md says to — per-repo by
// construction, visible in version control, no installer and no hook that can
// join a repo nobody opted in. Delivery never depended on it: the hub queues
// against the recipient's cursor, so a peer that is away loses nothing and
// drains the backlog the moment it joins.
type skillConfig struct {
	// AgentName pins the peer name; empty means "derive from the folder".
	AgentName string `json:"agentName"`
	// HubURL overrides the hub for this client; empty means the hub config.
	HubURL string `json:"hubUrl"`
	// TokenEnv NAMES (never holds) the env var carrying a bearer token for the
	// configured hub.
	TokenEnv string `json:"tokenEnv"`
	// Token is an OPTIONAL literal bearer token for the configured hub. Empty
	// by default and NEVER auto-populated — nothing copies the minted admin
	// token, or any other secret, in here; a value lands here only because a
	// human typed it. A file carrying one must be 0600 (config.LiteralToken
	// refuses a token out of a file others can read), and the value is never
	// printed anywhere.
	Token string `json:"token"`
}

func skillConfigPath(cfg config.Config) string {
	return filepath.Join(cfg.ConfigDir, skillConfigName)
}

// loadSkillConfig reads skill.json. A missing OR corrupt file is not an error
// anywhere this is used: these are client preferences, and the safe default is
// "the configured hub". Unknown keys — including an `autoJoin` left behind by
// an older install — are ignored, never a failure.
func loadSkillConfig(path string) skillConfig {
	sc, _ := loadSkillConfigWarn(path)
	return sc
}

// loadSkillConfigWarn also reports a literal token this process refused to
// use because the file is readable by others. The warning never carries the
// token value.
func loadSkillConfigWarn(path string) (skillConfig, string) {
	sc := skillConfig{}
	b, err := os.ReadFile(path)
	if err != nil {
		return sc, ""
	}
	_ = json.Unmarshal(b, &sc)
	var warn string
	sc.Token, warn = config.LiteralToken(path, sc.Token)
	return sc, warn
}

// ensureSkillConfig creates an empty skill.json when it does not exist. An
// existing file is NEVER overwritten — somebody's hub or token setting must
// survive a re-install.
func ensureSkillConfig(path string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	b, _ := json.MarshalIndent(skillConfig{}, "", "  ")
	// 0600 from birth: the file has a `token` key, so it must never be the
	// thing that makes a secret readable by others.
	return true, os.WriteFile(path, append(b, '\n'), config.SecretFileMode)
}

// applySkillConfig folds the CLIENT config into the resolved configuration.
//
// Precedence, one place, highest first:
//
//	command-line flag  >  WORKWIRE_* env  >  skill.json  >  workwire.json  >  defaults
//
// workwire.json is the HUB's file and a client setting there is a machine
// default; skill.json is the client's own, so it wins over it. The env stays
// above both (a container, or a one-off `WORKWIRE_HUB_URL=... workwire peers`,
// must not be overridden by a file), and an explicit flag beats everything.
//
// `tokenEnv` NAMES an env var; it never holds a secret value.
func applySkillConfig(cfg *config.Config, sc skillConfig) {
	if sc.HubURL != "" && os.Getenv("WORKWIRE_HUB_URL") == "" {
		cfg.HubURL = sc.HubURL
	}
	if sc.TokenEnv != "" && os.Getenv("WORKWIRE_TOKEN_ENV") == "" {
		cfg.TokenEnv = sc.TokenEnv
	}
	// The client's own literal token outranks the hub file's, and an env-set
	// literal outranks both.
	if sc.Token != "" && os.Getenv("WORKWIRE_TOKEN_LITERAL") == "" {
		cfg.Token = sc.Token
	}
}
