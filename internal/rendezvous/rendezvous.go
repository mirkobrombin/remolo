// Package rendezvous implements a tiny, self-hostable broker that lets two
// peers exchange their candidate endpoints without anyone typing an IP by
// hand. A host registers its candidates under a session id; a client looks
// them up by the same id. Entries live in memory and expire after a TTL.
//
// The wire protocol is plain JSON over HTTP:
//
//	POST /register  {"session_id":"hex","candidates":[{"ip":"","port":0}]}
//	GET  /lookup?session_id=hex  ->  {"candidates":[...]}  (404 if missing)
package rendezvous

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/mirkobrombin/remolo/internal/token"
)

// candidateDTO mirrors token.Candidate on the wire with lowercase json keys,
// independent of the cbor tags used by the token package.
type candidateDTO struct {
	IP   string `json:"ip"`
	Port uint16 `json:"port"`
}

func toDTOs(cs []token.Candidate) []candidateDTO {
	out := make([]candidateDTO, len(cs))
	for i, c := range cs {
		out[i] = candidateDTO{IP: c.IP, Port: c.Port}
	}
	return out
}

func fromDTOs(ds []candidateDTO) []token.Candidate {
	out := make([]token.Candidate, len(ds))
	for i, d := range ds {
		out[i] = token.Candidate{IP: d.IP, Port: d.Port}
	}
	return out
}

// registerRequest is the body of POST /register.
type registerRequest struct {
	SessionID  string         `json:"session_id"`
	Candidates []candidateDTO `json:"candidates"`
}

// lookupResponse is the body of GET /lookup.
type lookupResponse struct {
	Candidates []candidateDTO `json:"candidates"`
}

// entry is a single registered session in the in-memory registry.
type entry struct {
	candidates []token.Candidate
	updated    time.Time
}

// Server is an in-memory rendezvous broker. It is safe for concurrent use.
type Server struct {
	ttl time.Duration

	mu      sync.Mutex
	entries map[string]entry

	// now is overridable in tests to exercise expiry deterministically.
	now func() time.Time
}

// NewServer returns a Server whose entries expire ttl after their last update.
func NewServer(ttl time.Duration) *Server {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &Server{
		ttl:     ttl,
		entries: make(map[string]entry),
		now:     time.Now,
	}
}

// Handler returns an http.Handler exposing /register and /lookup.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/register", s.handleRegister)
	mux.HandleFunc("/lookup", s.handleLookup)
	return mux
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if req.SessionID == "" {
		http.Error(w, "missing session_id", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	s.entries[req.SessionID] = entry{
		candidates: fromDTOs(req.Candidates),
		updated:    s.now(),
	}
	s.mu.Unlock()

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleLookup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.URL.Query().Get("session_id")
	if id == "" {
		http.Error(w, "missing session_id", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	e, ok := s.entries[id]
	expired := ok && s.now().Sub(e.updated) > s.ttl
	if expired {
		delete(s.entries, id)
	}
	s.mu.Unlock()

	if !ok || expired {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(lookupResponse{Candidates: toDTOs(e.candidates)})
}
