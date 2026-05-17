package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
)

// authNonceLen is the size of each side's challenge nonce.
const authNonceLen = 32

// Transcript labels bind each signature to a role so a signature produced for
// one direction can never be replayed in the other.
const (
	labelAuthClient = "remolo-clientkey-client-v1"
	labelAuthHost   = "remolo-clientkey-host-v1"
)

// ClientAuth proves, over rw, that the dialer holds clientPriv (whose public
// half clientPub the host must have authorized) and confirms the host owns
// hostPub. It is the asymmetric, signature-based analogue of the PSK
// ClientHandshake.
//
// Wire sequence (every frame is length-prefixed with a uint16 big-endian
// length, see writeFrame/readFrame):
//
//	client -> host : clientPub                (frame 1)
//	client -> host : nonceC                   (frame 2)
//	host   -> client: nonceH                  (frame 3)
//	host   -> client: sigH                    (frame 4)
//	client -> host : sigC                     (frame 5)
//
// sigH is hostPriv's signature over transcript(labelAuthHost), sigC is
// clientPriv's signature over transcript(labelAuthClient); each transcript is
// label || hostPub || clientPub || nonceC || nonceH, so both signatures are
// bound to the host key and to both nonces, defeating replay and relay.
func ClientAuth(rw io.ReadWriter, clientPriv ed25519.PrivateKey, clientPub, hostPub ed25519.PublicKey) error {
	if len(clientPub) != ed25519.PublicKeySize || len(hostPub) != ed25519.PublicKeySize {
		return fmt.Errorf("identity: invalid public key size")
	}

	nonceC, err := freshAuthNonce()
	if err != nil {
		return err
	}
	if err := writeFrame(rw, clientPub); err != nil {
		return fmt.Errorf("identity: send client key: %w", err)
	}
	if err := writeFrame(rw, nonceC); err != nil {
		return fmt.Errorf("identity: send client nonce: %w", err)
	}

	nonceH, err := readFrame(rw, authNonceLen)
	if err != nil {
		return fmt.Errorf("identity: read host nonce: %w", err)
	}
	sigH, err := readFrame(rw, ed25519.SignatureSize)
	if err != nil {
		return fmt.Errorf("identity: read host signature: %w", err)
	}

	hostMsg := transcript(labelAuthHost, hostPub, clientPub, nonceC, nonceH)
	if !ed25519.Verify(hostPub, hostMsg, sigH) {
		return fmt.Errorf("identity: host authentication failed (wrong host key or impersonation)")
	}

	clientMsg := transcript(labelAuthClient, hostPub, clientPub, nonceC, nonceH)
	sigC := ed25519.Sign(clientPriv, clientMsg)
	if err := writeFrame(rw, sigC); err != nil {
		return fmt.Errorf("identity: send client signature: %w", err)
	}
	return nil
}

// ServerAuth runs the host side of the client-key handshake over rw. It reads
// the client's offered public key, rejects early if isAuthorized returns false,
// signs its own challenge so the client can confirm the host key, and verifies
// the client's signature. On success it returns the authenticated client public
// key.
func ServerAuth(rw io.ReadWriter, hostPriv ed25519.PrivateKey, hostPub ed25519.PublicKey, isAuthorized func(clientPub ed25519.PublicKey) bool) (ed25519.PublicKey, error) {
	if len(hostPub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("identity: invalid host public key size")
	}

	clientPub, err := readFrame(rw, ed25519.PublicKeySize)
	if err != nil {
		return nil, fmt.Errorf("identity: read client key: %w", err)
	}
	clientKey := ed25519.PublicKey(clientPub)

	// Reject unauthorized clients before doing any crypto work or revealing a
	// host signature.
	if isAuthorized == nil || !isAuthorized(clientKey) {
		return nil, fmt.Errorf("identity: client key not authorized")
	}

	nonceC, err := readFrame(rw, authNonceLen)
	if err != nil {
		return nil, fmt.Errorf("identity: read client nonce: %w", err)
	}
	nonceH, err := freshAuthNonce()
	if err != nil {
		return nil, err
	}

	hostMsg := transcript(labelAuthHost, hostPub, clientKey, nonceC, nonceH)
	sigH := ed25519.Sign(hostPriv, hostMsg)
	if err := writeFrame(rw, nonceH); err != nil {
		return nil, fmt.Errorf("identity: send host nonce: %w", err)
	}
	if err := writeFrame(rw, sigH); err != nil {
		return nil, fmt.Errorf("identity: send host signature: %w", err)
	}

	sigC, err := readFrame(rw, ed25519.SignatureSize)
	if err != nil {
		return nil, fmt.Errorf("identity: read client signature: %w", err)
	}
	clientMsg := transcript(labelAuthClient, hostPub, clientKey, nonceC, nonceH)
	if !ed25519.Verify(clientKey, clientMsg, sigC) {
		return nil, fmt.Errorf("identity: client authentication failed (bad signature)")
	}
	return append(ed25519.PublicKey(nil), clientKey...), nil
}

// transcript concatenates the signed message for a given role: the role label
// followed by the host key, client key, and both nonces in a fixed order.
func transcript(label string, hostPub, clientPub ed25519.PublicKey, nonceC, nonceH []byte) []byte {
	msg := make([]byte, 0, len(label)+len(hostPub)+len(clientPub)+len(nonceC)+len(nonceH))
	msg = append(msg, label...)
	msg = append(msg, hostPub...)
	msg = append(msg, clientPub...)
	msg = append(msg, nonceC...)
	msg = append(msg, nonceH...)
	return msg
}

func freshAuthNonce() ([]byte, error) {
	n := make([]byte, authNonceLen)
	if _, err := rand.Read(n); err != nil {
		return nil, fmt.Errorf("identity: nonce: %w", err)
	}
	return n, nil
}

// writeFrame writes a uint16 big-endian length followed by b.
func writeFrame(w io.Writer, b []byte) error {
	if len(b) > 0xffff {
		return fmt.Errorf("identity: frame too large (%d bytes)", len(b))
	}
	var hdr [2]byte
	binary.BigEndian.PutUint16(hdr[:], uint16(len(b)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(b)
	return err
}

// readFrame reads one length-prefixed frame and requires it to be exactly
// wantLen bytes, guarding against framing confusion on fixed-size fields.
func readFrame(r io.Reader, wantLen int) ([]byte, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(hdr[:]))
	if n != wantLen {
		return nil, fmt.Errorf("identity: unexpected frame length %d (want %d)", n, wantLen)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}
