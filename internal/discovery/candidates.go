// Package discovery turns the host's network reality into the candidate list a
// token carries. Enumerating every reachable interface, across every subnet, is
// what lets a client on a different subnet of the same network connect without
// anyone typing an IP: the knowledge of where the host lives travels inside the
// token, and the client races all candidates in parallel.
package discovery

import (
	"net"
	"sort"

	"github.com/mirkobrombin/remolo/internal/token"
)

// EnumerateCandidates lists every locally-bound address suitable for a dialer,
// pairing each with the given UDP port. Link-local and multicast addresses are
// skipped because they are not directly dialable without a zone; loopback is
// included only when includeLoopback is set (useful for same-machine testing).
//
// Results are ordered so that the most broadly useful addresses (private LAN,
// then global) come first, improving happy-eyeballs head starts.
func EnumerateCandidates(port uint16, includeLoopback bool) []token.Candidate {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []token.Candidate
	seen := map[string]bool{}

	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ip := addrIP(a)
			if ip == nil {
				continue
			}
			if ip.IsLoopback() {
				if !includeLoopback {
					continue
				}
			} else if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
				continue
			}
			s := ip.String()
			if seen[s] {
				continue
			}
			seen[s] = true
			out = append(out, token.Candidate{IP: s, Port: port})
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		return rank(out[i].IP) < rank(out[j].IP)
	})
	return out
}

func addrIP(a net.Addr) net.IP {
	switch v := a.(type) {
	case *net.IPNet:
		return v.IP
	case *net.IPAddr:
		return v.IP
	}
	return nil
}

// rank orders addresses: private IPv4 first (most likely reachable on a LAN),
// then other IPv4, then IPv6, then loopback last.
func rank(s string) int {
	ip := net.ParseIP(s)
	if ip == nil {
		return 100
	}
	switch {
	case ip.IsLoopback():
		return 90
	case ip.To4() != nil && ip.IsPrivate():
		return 0
	case ip.To4() != nil:
		return 10
	case ip.IsPrivate():
		return 20
	default:
		return 30
	}
}
