package rendezvous

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mirkobrombin/remolo/internal/token"
)

// httpClient is the shared client used by Register and Lookup. A modest
// timeout keeps callers from hanging on a dead broker.
var httpClient = &http.Client{Timeout: 10 * time.Second}

// joinURL appends path to baseURL, tolerating a trailing slash on baseURL.
func joinURL(baseURL, path string) string {
	return strings.TrimRight(baseURL, "/") + path
}

// Register publishes candidates for sessionHex to the broker at baseURL.
func Register(baseURL, sessionHex string, candidates []token.Candidate) error {
	body, err := json.Marshal(registerRequest{
		SessionID:  sessionHex,
		Candidates: toDTOs(candidates),
	})
	if err != nil {
		return fmt.Errorf("rendezvous: marshal register: %w", err)
	}

	resp, err := httpClient.Post(joinURL(baseURL, "/register"), "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("rendezvous: register request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("rendezvous: register: unexpected status %s", resp.Status)
	}
	return nil
}

// Lookup fetches the candidates registered for sessionHex from baseURL. A
// missing or expired session is reported as an error.
func Lookup(baseURL, sessionHex string) ([]token.Candidate, error) {
	url := joinURL(baseURL, "/lookup") + "?session_id=" + sessionHex
	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("rendezvous: lookup request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("rendezvous: session %q not found", sessionHex)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rendezvous: lookup: unexpected status %s", resp.Status)
	}

	var lr lookupResponse
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		return nil, fmt.Errorf("rendezvous: decode lookup: %w", err)
	}
	return fromDTOs(lr.Candidates), nil
}
