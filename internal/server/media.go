package server

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/muthuishere/workwire/internal/envelope"
)

// mediaStore serves attachment bytes from the hub — never by host path
// (hub-core R10). Bytes live under dataDir/media/<id> with a sidecar
// <id>.json for content type and name.
type mediaStore struct {
	dir string
}

type mediaMeta struct {
	ContentType string `json:"contentType"`
	Name        string `json:"name,omitempty"`
	Size        int64  `json:"size"`
}

func newMediaStore(dataDir string) (*mediaStore, error) {
	dir := filepath.Join(dataDir, "media")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &mediaStore{dir: dir}, nil
}

func safeMediaID(id string) bool {
	return id != "" && !strings.ContainsAny(id, "/\\.")
}

// POST /media — raw bytes body, Content-Type honored; returns {"id":"med-..."}.
func (s *Server) handleUploadMedia(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.identify(w, r); !ok {
		return
	}
	id := envelope.NewID("med")
	path := filepath.Join(s.media.dir, id)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "media write failed")
		return
	}
	n, err := io.Copy(f, r.Body)
	f.Close()
	if err != nil {
		os.Remove(path)
		writeErr(w, http.StatusInternalServerError, "media write failed")
		return
	}
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/octet-stream"
	}
	meta := mediaMeta{ContentType: ct, Name: r.URL.Query().Get("name"), Size: n}
	b, _ := json.Marshal(meta)
	_ = os.WriteFile(path+".json", b, 0o644)
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "size": n})
}

// GET /media/{id} — attachment bytes with the stored Content-Type; unknown
// id is 404.
func (s *Server) handleGetMedia(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.identify(w, r); !ok {
		return
	}
	id := r.PathValue("id")
	if !safeMediaID(id) {
		writeErr(w, http.StatusNotFound, "media not found")
		return
	}
	path := filepath.Join(s.media.dir, id)
	f, err := os.Open(path)
	if err != nil {
		writeErr(w, http.StatusNotFound, "media not found")
		return
	}
	defer f.Close()
	ct := "application/octet-stream"
	if b, err := os.ReadFile(path + ".json"); err == nil {
		var m mediaMeta
		if json.Unmarshal(b, &m) == nil && m.ContentType != "" {
			ct = m.ContentType
		}
	}
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}
