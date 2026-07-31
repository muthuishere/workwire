// Package hubaddr answers two questions about a hub base URL that the
// credential rules depend on: is it loopback (may the locally-minted admin
// token go there?), and what is its stable identity (which hub issued this
// credential?).
package hubaddr

import (
	"net"
	"net/url"
	"strings"
)

// IsLoopback reports whether the base URL names this machine. The locally
// minted 0600 admin token is a credential for THIS hub and is never sent
// anywhere else (auth R10) — everything hangs off this answer, so it is
// deliberately conservative: anything unparseable, or any name that does not
// resolve to a loopback literal, is NOT loopback.
func IsLoopback(base string) bool {
	host := hostOf(base)
	if host == "" {
		return false
	}
	switch strings.ToLower(host) {
	case "localhost":
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// Key is the normalised identity of a hub: lowercase scheme://host:port with
// the default port made explicit, so `http://127.0.0.1:14411` and
// `http://127.0.0.1:14411/` are one hub and a path or query never splits it.
// An unparseable base is returned trimmed and lowercased so it still keys
// consistently.
func Key(base string) string {
	b := strings.TrimSpace(base)
	u, err := url.Parse(b)
	if err != nil || u.Host == "" {
		return strings.ToLower(strings.TrimSuffix(b, "/"))
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme == "" {
		scheme = "http"
	}
	host := strings.ToLower(u.Hostname())
	// Every loopback spelling is the same hub: `localhost`, `127.0.0.1` and
	// `::1` on one port must not split a credential store in three.
	if IsLoopback(b) {
		host = "127.0.0.1"
	}
	port := u.Port()
	if port == "" {
		switch scheme {
		case "https":
			port = "443"
		default:
			port = "80"
		}
	}
	if strings.Contains(host, ":") { // IPv6 literal
		host = "[" + host + "]"
	}
	return scheme + "://" + host + ":" + port
}

func hostOf(base string) string {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Hostname()
}
