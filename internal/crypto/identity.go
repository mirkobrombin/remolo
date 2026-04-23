// Package crypto provides remolo's host identity and the mutual,
// token-anchored authentication handshake.
//
// Two mechanisms work together:
//
//  1. Host authentication. The host owns an ephemeral Ed25519 key whose public
//     half travels inside the token. The QUIC/TLS layer presents a self-signed
//     certificate built from that key; the client pins the certificate's public
//     key against the token, defeating man-in-the-middle even when the IP is
//     attacker-supplied.
//
//  2. Client authentication. After the channel is up, both sides run a PSK
//     challenge-response (HMAC over fresh nonces, bound to the host key). This
//     proves the client holds the token's secret and re-confirms the host,
//     and yields a derived session key.
package crypto

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"time"
)

// ALPN is the application-layer protocol negotiated over TLS for remolo.
const ALPN = "remolo/1"

// Identity is a host's ephemeral cryptographic identity.
type Identity struct {
	priv   ed25519.PrivateKey
	pub    ed25519.PublicKey
	cert   tls.Certificate
	rawPub []byte
}

// GenerateIdentity creates a fresh Ed25519 identity and a self-signed
// certificate derived from it.
func GenerateIdentity() (*Identity, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("crypto: generate key: %w", err)
	}
	cert, err := selfSignedCert(pub, priv)
	if err != nil {
		return nil, err
	}
	return &Identity{priv: priv, pub: pub, cert: cert, rawPub: append([]byte(nil), pub...)}, nil
}

// IdentityFromKey builds an Identity from an existing Ed25519 private key, so a
// host can present a STABLE identity across restarts (enabling known-hosts trust
// and client enrollment) instead of a fresh ephemeral one each launch.
func IdentityFromKey(priv ed25519.PrivateKey) (*Identity, error) {
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("crypto: not an Ed25519 private key")
	}
	cert, err := selfSignedCert(pub, priv)
	if err != nil {
		return nil, err
	}
	return &Identity{priv: priv, pub: pub, cert: cert, rawPub: append([]byte(nil), pub...)}, nil
}

// PublicKey returns the raw Ed25519 public key to embed in a token.
func (id *Identity) PublicKey() []byte { return append([]byte(nil), id.rawPub...) }

// PrivateKey returns the host's Ed25519 private key (for client-key auth).
func (id *Identity) PrivateKey() ed25519.PrivateKey { return id.priv }

// PublicKeyEd returns the host public key as an ed25519.PublicKey.
func (id *Identity) PublicKeyEd() ed25519.PublicKey { return id.pub }

// ServerTLSConfig returns the TLS config the host presents to dialers.
func (id *Identity) ServerTLSConfig() *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{id.cert},
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{ALPN},
	}
}

// ClientTLSConfig returns a TLS config that pins the host's certificate to the
// public key carried in the token. Verification of the standard chain is
// disabled on purpose (the cert is self-signed and ephemeral); trust is
// established solely by matching the pinned key.
func ClientTLSConfig(expectedPub []byte) *tls.Config {
	pinned := append([]byte(nil), expectedPub...)
	return &tls.Config{
		InsecureSkipVerify: true, // we verify by pinning, see below
		MinVersion:         tls.VersionTLS13,
		NextProtos:         []string{ALPN},
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return fmt.Errorf("crypto: host presented no certificate")
			}
			leaf, err := x509.ParseCertificate(rawCerts[0])
			if err != nil {
				return fmt.Errorf("crypto: parsing host certificate: %w", err)
			}
			got, ok := leaf.PublicKey.(ed25519.PublicKey)
			if !ok {
				return fmt.Errorf("crypto: host certificate is not Ed25519")
			}
			if !ed25519.PublicKey(pinned).Equal(got) {
				return fmt.Errorf("crypto: host key mismatch (possible impersonation), refusing")
			}
			return nil
		},
	}
}

func selfSignedCert(pub ed25519.PublicKey, priv ed25519.PrivateKey) (tls.Certificate, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("crypto: serial: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "remolo-host"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * 365 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("crypto: create certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("crypto: parse certificate: %w", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv, Leaf: leaf}, nil
}
