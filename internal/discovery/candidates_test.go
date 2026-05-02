package discovery

import (
	"net"
	"testing"
)

func TestEnumerateIncludesLoopbackWhenAsked(t *testing.T) {
	with := EnumerateCandidates(41000, true)
	hasLoopback := false
	for _, c := range with {
		if c.Port != 41000 {
			t.Errorf("candidate %s has wrong port %d", c.IP, c.Port)
		}
		if ip := net.ParseIP(c.IP); ip != nil && ip.IsLoopback() {
			hasLoopback = true
		}
	}
	if !hasLoopback {
		t.Skip("no loopback interface visible in this environment")
	}
}

func TestEnumerateSkipsLoopbackByDefault(t *testing.T) {
	for _, c := range EnumerateCandidates(41000, false) {
		ip := net.ParseIP(c.IP)
		if ip == nil {
			t.Fatalf("invalid candidate ip %q", c.IP)
		}
		if ip.IsLoopback() {
			t.Errorf("loopback %s should be excluded by default", c.IP)
		}
		if ip.IsLinkLocalUnicast() {
			t.Errorf("link-local %s should never be a candidate", c.IP)
		}
	}
}

func TestNoDuplicates(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range EnumerateCandidates(41000, true) {
		if seen[c.IP] {
			t.Errorf("duplicate candidate %s", c.IP)
		}
		seen[c.IP] = true
	}
}
