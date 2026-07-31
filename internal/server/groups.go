package server

import (
	"encoding/json"
	"net/http"

	"github.com/muthuishere/workwire/internal/auth"
)

// groupRequest is the join/leave body. `peer` exists ONLY so a mistaken
// attempt to add somebody else fails loudly instead of silently doing
// something else: the hub accepts it only when it names the caller.
type groupRequest struct {
	Peer string `json:"peer"`
}

// callerPeer resolves the peer a group verb acts on. It is ALWAYS the
// authenticated caller (ADR-012): no endpoint may add peer B to a group on
// peer A's say-so, because that would let anyone force-wake anyone else's
// session. Consent to be woken stays with the peer being woken.
func (s *Server) callerPeer(w http.ResponseWriter, r *http.Request, id auth.Identity) (string, bool) {
	var req groupRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	me := id.Name()
	if req.Peer != "" && req.Peer != me {
		writeErr(w, http.StatusForbidden,
			"a peer may only join or leave a group itself — invite it instead (`workwire group invite`), which sends a message and changes nothing")
		return "", false
	}
	return me, true
}

// GET /groups — every audience, its members, and whether the caller is in it
// (ADR-012). A group holds no messages, so there is nothing else to show.
func (s *Server) handleListGroups(w http.ResponseWriter, r *http.Request) {
	id, ok := s.identify(w, r)
	if !ok {
		return
	}
	me := id.Name()
	all := s.registry.Groups()
	out := make([]map[string]any, 0, len(all))
	for _, g := range all {
		member := false
		for _, m := range g.Members {
			if m == me {
				member = true
			}
		}
		out = append(out, map[string]any{
			"name":    g.Name,
			"members": g.Members,
			"count":   len(g.Members),
			"member":  member,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": out})
}

// POST /groups/{name}/join — opt in; creates the group when absent. No
// owner, no admin, no privileges for whoever arrived first.
func (s *Server) handleGroupJoin(w http.ResponseWriter, r *http.Request) {
	id, ok := s.identify(w, r)
	if !ok {
		return
	}
	peer, ok := s.callerPeer(w, r, id)
	if !ok {
		return
	}
	name, err := s.registry.JoinGroup(r.PathValue("name"), peer)
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	members, _ := s.registry.GroupMembers(name)
	writeJSON(w, http.StatusOK, map[string]any{"group": name, "peer": peer, "members": members})
}

// POST /groups/{name}/leave — opt out. Leaving @all is how a peer goes
// quiet; an emptied ad-hoc group is garbage-collected.
func (s *Server) handleGroupLeave(w http.ResponseWriter, r *http.Request) {
	id, ok := s.identify(w, r)
	if !ok {
		return
	}
	peer, ok := s.callerPeer(w, r, id)
	if !ok {
		return
	}
	name, left := s.registry.LeaveGroup(r.PathValue("name"), peer)
	if !left {
		writeErr(w, http.StatusNotFound, "not a member of "+name)
		return
	}
	members, exists := s.registry.GroupMembers(name)
	writeJSON(w, http.StatusOK, map[string]any{
		"group": name, "peer": peer, "members": members, "collected": !exists,
	})
}
