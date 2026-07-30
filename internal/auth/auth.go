// Package auth implements identity resolution: explicit authMode, the
// auto-minted 0600 local admin token, per-agent secret auth, and the
// 401-vs-403 discipline (auth spec; ADR-007).
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/muthuishere/workwire/internal/registry"
)

// TokenFileName is the admin token file under the config dir.
const TokenFileName = "admin-token"

// EnsureAdminToken reads or mints the local admin token file (mode 0600) in
// configDir (auth R2). Returns the token value (kept in memory only).
func EnsureAdminToken(configDir string) (string, error) {
	if configDir == "" {
		return "", errors.New("no config dir for admin token")
	}
	path := filepath.Join(configDir, TokenFileName)
	if b, err := os.ReadFile(path); err == nil {
		t := strings.TrimSpace(string(b))
		if t != "" {
			return t, nil
		}
	}
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return "", err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", err
	}
	return token, nil
}

// Kind of authenticated actor.
type Kind int

const (
	// KindNone: no credential presented (only valid in open mode).
	KindNone Kind = iota
	// KindAdmin: the local admin token.
	KindAdmin
	// KindAgent: a hub-issued per-agent secret.
	KindAgent
)

// Identity is the resolved actor of a request.
type Identity struct {
	Kind  Kind
	Agent *registry.Agent
}

// Name is the server-stamped `from` for this actor.
func (id Identity) Name() string {
	switch id.Kind {
	case KindAdmin:
		return "admin"
	case KindAgent:
		return id.Agent.Name
	default:
		return "anonymous"
	}
}

// PeerKind is the provenance marker delivered in meta.peerKind (auth R9).
func (id Identity) PeerKind() string {
	switch id.Kind {
	case KindAdmin:
		return "admin"
	case KindAgent:
		return "agent"
	default:
		return "external"
	}
}

// ErrUnauthorized maps to 401 {"error":"unauthorized"}.
var ErrUnauthorized = errors.New("unauthorized")

// Authenticator resolves request credentials.
type Authenticator struct {
	Mode       string // "token" | "open"
	AdminToken string
	Registry   *registry.Registry
}

// BearerToken extracts the Authorization: Bearer value.
func BearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const p = "Bearer "
	if strings.HasPrefix(h, p) {
		return strings.TrimSpace(h[len(p):])
	}
	return ""
}

// Identify resolves the request's identity. In token mode a missing or
// invalid credential returns ErrUnauthorized (401); acting as a different
// agent is the caller's 403 concern. Any authenticated agent request
// refreshes that agent's liveness (registry-a2a R3).
func (a *Authenticator) Identify(r *http.Request) (Identity, error) {
	tok := BearerToken(r)
	if tok != "" {
		if a.AdminToken != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(a.AdminToken)) == 1 {
			return Identity{Kind: KindAdmin}, nil
		}
		if ag, ok := a.Registry.Authenticate(tok); ok {
			a.Registry.Touch(ag.Name)
			return Identity{Kind: KindAgent, Agent: ag}, nil
		}
		if a.Mode == "open" {
			return Identity{Kind: KindNone}, nil
		}
		return Identity{}, ErrUnauthorized
	}
	if a.Mode == "open" {
		return Identity{Kind: KindNone}, nil
	}
	return Identity{}, ErrUnauthorized
}
