package server

import (
	"encoding/json"
	"net/http"

	"github.com/muthuishere/workwire/internal/contacts"
)

// GET /contacts?q= — fuzzy lookup, best match first; without q, all
// non-tombstoned entries (contacts R3).
func (s *Server) handleListContacts(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.identify(w, r); !ok {
		return
	}
	res := s.contacts.Search(r.URL.Query().Get("q"))
	if res == nil {
		res = []contacts.Contact{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"contacts": res})
}

// POST /contacts — explicit add/merge, verified:true (contacts R2).
func (s *Server) handleAddContact(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.identify(w, r); !ok {
		return
	}
	var req struct {
		Name    string   `json:"name"`
		Peer    string   `json:"peer"`
		ID      string   `json:"id"`
		Aliases []string `json:"aliases"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, "name required")
		return
	}
	if req.Peer == "" || req.ID == "" {
		writeErr(w, http.StatusBadRequest, "peer and id required")
		return
	}
	c, err := s.contacts.Add(req.Name, req.Peer, req.ID, req.Aliases)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "contact write failed")
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

// POST /contacts/{id}/verify — idempotent verification (contacts R6).
func (s *Server) handleVerifyContact(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.identify(w, r); !ok {
		return
	}
	c, ok := s.contacts.Verify(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "contact not found")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// DELETE /contacts/{id} — tombstone purge, idempotent 200 (contacts R7).
func (s *Server) handleDeleteContact(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.identify(w, r); !ok {
		return
	}
	id := r.PathValue("id")
	switch s.contacts.Delete(id) {
	case contacts.Deleted:
		writeJSON(w, http.StatusOK, map[string]any{"contactId": id, "deleted": true})
	default:
		writeErr(w, http.StatusNotFound, "contact not found")
	}
}
