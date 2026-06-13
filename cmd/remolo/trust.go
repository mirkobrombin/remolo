package main

import (
	"crypto/ed25519"
	"fmt"

	"github.com/mirkobrombin/remolo/internal/identity"
)

// verifyKnownHost implements trust-on-first-use for stable host identities. When
// connecting by a stable id (an alias), it remembers the host's public key the
// first time and refuses loudly if it ever changes (the SSH anti-swap warning).
// For raw tokens the id is empty and the check is skipped, since the token
// itself pins the host key.
func verifyKnownHost(id string, hostPub []byte) error {
	if id == "" {
		return nil
	}
	kh, err := identity.LoadKnownHosts(identity.DefaultKnownHostsPath())
	if err != nil {
		return nil // do not block connecting if the store is unreadable
	}
	pub := ed25519.PublicKey(hostPub)
	switch kh.Check(id, pub) {
	case identity.TrustMismatch:
		return fmt.Errorf(
			"HOST KEY CHANGED for %q (possible impersonation). Refusing.\n"+
				"  If you trust this change, remove the line for %q from %s and retry.",
			id, id, identity.DefaultKnownHostsPath())
	case identity.TrustNew:
		if err := kh.Remember(id, pub); err == nil {
			fmt.Printf("remolo: remembered host key for %q (fingerprint %s)\n", id, identity.FingerprintBase64(pub))
		}
	}
	return nil
}
