// Package holepunch implements the building blocks for UDP NAT traversal:
// STUN reflexive address discovery and simple UDP hole punching.
//
// The two ReflexiveAddr helpers ask a STUN server what public ip:port it
// observes for our UDP socket. ReflexiveAddrFrom reuses a caller-owned
// *net.UDPConn so the discovered port matches a socket that will later be
// reused (for example, by a QUIC transport). Punch sprays a few small
// datagrams at a peer to open the NAT pinhole in both directions.
package holepunch

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/pion/stun/v3"
)

// defaultSTUNTimeout bounds a single BINDING transaction when the caller's
// context carries no deadline.
const defaultSTUNTimeout = 5 * time.Second

// ReflexiveAddr discovers the public ip:port as seen from stunServer by
// opening a fresh UDP socket, performing a STUN BINDING transaction and
// returning the XOR-MAPPED-ADDRESS as "ip:port".
//
// The returned address belongs to the throwaway socket created here; use
// ReflexiveAddrFrom when the reflexive port must match a socket you keep.
func ReflexiveAddr(ctx context.Context, stunServer string) (string, error) {
	conn, err := net.ListenUDP("udp", nil)
	if err != nil {
		return "", fmt.Errorf("holepunch: open udp: %w", err)
	}
	defer conn.Close()
	return ReflexiveAddrFrom(ctx, conn, stunServer)
}

// ReflexiveAddrFrom runs a STUN BINDING transaction over an existing UDPConn
// and returns the observed public "ip:port". The conn stays open so the
// caller can reuse the very same socket (and therefore the same NAT mapping)
// afterwards.
func ReflexiveAddrFrom(ctx context.Context, conn *net.UDPConn, stunServer string) (string, error) {
	if conn == nil {
		return "", errors.New("holepunch: nil conn")
	}

	srvAddr, err := net.ResolveUDPAddr("udp", stunServer)
	if err != nil {
		return "", fmt.Errorf("holepunch: resolve stun server: %w", err)
	}

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(defaultSTUNTimeout)
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return "", fmt.Errorf("holepunch: set deadline: %w", err)
	}
	// Clear the deadline before returning so the caller can reuse the conn.
	defer conn.SetDeadline(time.Time{})

	req := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
	if _, err := conn.WriteToUDP(req.Raw, srvAddr); err != nil {
		return "", fmt.Errorf("holepunch: send binding request: %w", err)
	}

	buf := make([]byte, 1500)
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			return "", fmt.Errorf("holepunch: read binding response: %w", err)
		}
		if !stun.IsMessage(buf[:n]) {
			// Some other traffic on this socket; keep waiting.
			continue
		}

		resp := &stun.Message{Raw: append([]byte{}, buf[:n]...)}
		if err := resp.Decode(); err != nil {
			return "", fmt.Errorf("holepunch: decode stun response: %w", err)
		}

		var xor stun.XORMappedAddress
		if err := xor.GetFrom(resp); err != nil {
			// Fall back to plain MAPPED-ADDRESS if present.
			var mapped stun.MappedAddress
			if err2 := mapped.GetFrom(resp); err2 != nil {
				return "", fmt.Errorf("holepunch: no mapped address: %w", err)
			}
			return net.JoinHostPort(mapped.IP.String(), fmt.Sprintf("%d", mapped.Port)), nil
		}
		return net.JoinHostPort(xor.IP.String(), fmt.Sprintf("%d", xor.Port)), nil
	}
}

// Punch opens a NAT pinhole towards peerAddr by sending a handful of tiny UDP
// datagrams from conn. It does rounds sends spaced interval apart. Sending is
// best-effort: transient write errors are ignored (the remote pinhole may not
// be open yet), but a fatal setup error (bad address) is returned immediately.
func Punch(conn *net.UDPConn, peerAddr string, rounds int, interval time.Duration) error {
	if conn == nil {
		return errors.New("holepunch: nil conn")
	}
	if rounds <= 0 {
		rounds = 1
	}
	if interval <= 0 {
		interval = 200 * time.Millisecond
	}

	dst, err := net.ResolveUDPAddr("udp", peerAddr)
	if err != nil {
		return fmt.Errorf("holepunch: resolve peer: %w", err)
	}

	// A short, recognisable marker so a peer can distinguish punch packets.
	payload := []byte("remolo-punch")
	for i := 0; i < rounds; i++ {
		// Ignore write errors: the path may not be ready yet.
		_, _ = conn.WriteToUDP(payload, dst)
		if i < rounds-1 {
			time.Sleep(interval)
		}
	}
	return nil
}
