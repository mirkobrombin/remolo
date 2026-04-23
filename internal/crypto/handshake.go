package crypto

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
)

const (
	nonceLen = 32
	proofLen = sha256.Size
	keyLen   = 32

	labelHost    = "remolo-host-v1"
	labelClient  = "remolo-client-v1"
	labelSession = "remolo-session-v1"
)

// HandshakeResult carries the negotiated session key, derived from the PSK and
// both nonces. It is identical on the two peers after a successful exchange.
type HandshakeResult struct {
	SessionKey []byte
}

// ClientHandshake runs the PSK challenge-response from the dialer's side.
//
// Wire exchange (all fixed-size, length implied):
//
//	client -> host : nonceC
//	host   -> client: nonceH || proofH
//	client -> host : proofC
//
// proofH/proofC are HMAC-SHA256 keyed by the PSK over a role label, the host
// public key (channel binding) and both nonces in a fixed order.
func ClientHandshake(rw io.ReadWriter, psk, hostPub []byte) (*HandshakeResult, error) {
	nonceC, err := freshNonce()
	if err != nil {
		return nil, err
	}
	if err := writeFull(rw, nonceC); err != nil {
		return nil, fmt.Errorf("crypto: send client nonce: %w", err)
	}

	resp := make([]byte, nonceLen+proofLen)
	if err := readFull(rw, resp); err != nil {
		return nil, fmt.Errorf("crypto: read host proof: %w", err)
	}
	nonceH := resp[:nonceLen]
	gotProofH := resp[nonceLen:]
	wantProofH := mac(psk, labelHost, hostPub, nonceC, nonceH)
	if !hmac.Equal(gotProofH, wantProofH) {
		return nil, fmt.Errorf("crypto: host authentication failed (wrong token or impersonation)")
	}

	proofC := mac(psk, labelClient, hostPub, nonceH, nonceC)
	if err := writeFull(rw, proofC); err != nil {
		return nil, fmt.Errorf("crypto: send client proof: %w", err)
	}
	return &HandshakeResult{SessionKey: mac(psk, labelSession, hostPub, nonceC, nonceH)[:keyLen]}, nil
}

// ServerHandshake runs the PSK challenge-response from the host's side.
func ServerHandshake(rw io.ReadWriter, psk, hostPub []byte) (*HandshakeResult, error) {
	nonceC := make([]byte, nonceLen)
	if err := readFull(rw, nonceC); err != nil {
		return nil, fmt.Errorf("crypto: read client nonce: %w", err)
	}
	nonceH, err := freshNonce()
	if err != nil {
		return nil, err
	}
	proofH := mac(psk, labelHost, hostPub, nonceC, nonceH)
	resp := make([]byte, 0, nonceLen+proofLen)
	resp = append(resp, nonceH...)
	resp = append(resp, proofH...)
	if err := writeFull(rw, resp); err != nil {
		return nil, fmt.Errorf("crypto: send host proof: %w", err)
	}

	gotProofC := make([]byte, proofLen)
	if err := readFull(rw, gotProofC); err != nil {
		return nil, fmt.Errorf("crypto: read client proof: %w", err)
	}
	wantProofC := mac(psk, labelClient, hostPub, nonceH, nonceC)
	if !hmac.Equal(gotProofC, wantProofC) {
		return nil, fmt.Errorf("crypto: client authentication failed (wrong token)")
	}
	return &HandshakeResult{SessionKey: mac(psk, labelSession, hostPub, nonceC, nonceH)[:keyLen]}, nil
}

func mac(psk []byte, label string, parts ...[]byte) []byte {
	h := hmac.New(sha256.New, psk)
	h.Write([]byte(label))
	for _, p := range parts {
		h.Write(p)
	}
	return h.Sum(nil)
}

func freshNonce() ([]byte, error) {
	n := make([]byte, nonceLen)
	if _, err := rand.Read(n); err != nil {
		return nil, fmt.Errorf("crypto: nonce: %w", err)
	}
	return n, nil
}

func writeFull(w io.Writer, b []byte) error {
	_, err := w.Write(b)
	return err
}

func readFull(r io.Reader, b []byte) error {
	_, err := io.ReadFull(r, b)
	return err
}
