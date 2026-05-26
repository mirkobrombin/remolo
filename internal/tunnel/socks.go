package tunnel

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
)

// SOCKS5 protocol constants (RFC 1928).
const (
	socksVersion5 = 0x05

	socksAuthNone        = 0x00
	socksAuthNoAccept    = 0xFF
	socksCmdConnect      = 0x01
	socksCmdBind         = 0x02
	socksCmdUDPAssociate = 0x03

	socksAddrIPv4   = 0x01
	socksAddrDomain = 0x03
	socksAddrIPv6   = 0x04

	socksReplySuccess           = 0x00
	socksReplyGeneralFailure    = 0x01
	socksReplyCommandNotSupport = 0x07
	socksReplyAddrNotSupport    = 0x08
)

// SocksForward implements SSH-style -D dynamic forwarding: a minimal SOCKS5
// server that a browser can point at. It listens on listen, performs the
// no-auth method negotiation and the CONNECT command (IPv4, IPv6, and
// DOMAINNAME address types), then asks open for a forward channel to the
// requested "host:port" and splices it to the SOCKS client. BIND and
// UDP-ASSOCIATE are rejected with the proper SOCKS error. It returns when ctx
// is cancelled or the listener fails to accept. It logs nothing.
func SocksForward(ctx context.Context, listen string, open OpenForward) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", listen)
	if err != nil {
		return err
	}
	defer ln.Close()

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		go handleSocks(ctx, c, open)
	}
}

// handleSocks drives one SOCKS5 client connection through negotiation and the
// CONNECT request, then splices it to a forward channel.
func handleSocks(ctx context.Context, c net.Conn, open OpenForward) {
	target, err := socksHandshake(c)
	if err != nil {
		c.Close()
		return
	}
	remote, err := open(ctx, target)
	if err != nil {
		// Best-effort failure reply; ignore write errors.
		_ = writeSocksReply(c, socksReplyGeneralFailure)
		c.Close()
		return
	}
	if err := writeSocksReply(c, socksReplySuccess); err != nil {
		remote.Close()
		c.Close()
		return
	}
	Splice(c, remote)
}

// socksHandshake performs method negotiation and parses the CONNECT request,
// returning the requested "host:port" target. On a command it cannot satisfy
// it writes the appropriate SOCKS error reply and returns an error.
func socksHandshake(c net.Conn) (string, error) {
	// Method negotiation: VER, NMETHODS, METHODS...
	header := make([]byte, 2)
	if _, err := io.ReadFull(c, header); err != nil {
		return "", err
	}
	if header[0] != socksVersion5 {
		return "", errors.New("tunnel: unsupported SOCKS version")
	}
	nMethods := int(header[1])
	methods := make([]byte, nMethods)
	if _, err := io.ReadFull(c, methods); err != nil {
		return "", err
	}
	offered := false
	for _, m := range methods {
		if m == socksAuthNone {
			offered = true
			break
		}
	}
	if !offered {
		// Tell the client no acceptable method then bail.
		_, _ = c.Write([]byte{socksVersion5, socksAuthNoAccept})
		return "", errors.New("tunnel: no acceptable SOCKS auth method")
	}
	if _, err := c.Write([]byte{socksVersion5, socksAuthNone}); err != nil {
		return "", err
	}

	// Request: VER, CMD, RSV, ATYP, DST.ADDR, DST.PORT.
	req := make([]byte, 4)
	if _, err := io.ReadFull(c, req); err != nil {
		return "", err
	}
	if req[0] != socksVersion5 {
		return "", errors.New("tunnel: unsupported SOCKS version in request")
	}
	switch req[1] {
	case socksCmdConnect:
		// supported
	case socksCmdBind, socksCmdUDPAssociate:
		_ = writeSocksReply(c, socksReplyCommandNotSupport)
		return "", errors.New("tunnel: SOCKS command not supported")
	default:
		_ = writeSocksReply(c, socksReplyCommandNotSupport)
		return "", errors.New("tunnel: unknown SOCKS command")
	}

	host, err := readSocksAddr(c, req[3])
	if err != nil {
		_ = writeSocksReply(c, socksReplyAddrNotSupport)
		return "", err
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(c, portBytes); err != nil {
		return "", err
	}
	port := binary.BigEndian.Uint16(portBytes)
	return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
}

// readSocksAddr reads the DST.ADDR field for the given ATYP and returns it as a
// host string (literal IP or domain name).
func readSocksAddr(c net.Conn, atyp byte) (string, error) {
	switch atyp {
	case socksAddrIPv4:
		buf := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(c, buf); err != nil {
			return "", err
		}
		return net.IP(buf).String(), nil
	case socksAddrIPv6:
		buf := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(c, buf); err != nil {
			return "", err
		}
		return net.IP(buf).String(), nil
	case socksAddrDomain:
		lenByte := make([]byte, 1)
		if _, err := io.ReadFull(c, lenByte); err != nil {
			return "", err
		}
		name := make([]byte, int(lenByte[0]))
		if _, err := io.ReadFull(c, name); err != nil {
			return "", err
		}
		return string(name), nil
	default:
		return "", errors.New("tunnel: unsupported SOCKS address type")
	}
}

// writeSocksReply sends a SOCKS5 reply with the given status code and a
// zeroed IPv4 bound-address field, which is sufficient for CONNECT.
func writeSocksReply(c net.Conn, status byte) error {
	// VER, REP, RSV, ATYP=IPv4, BND.ADDR(4)=0, BND.PORT(2)=0.
	reply := []byte{socksVersion5, status, 0x00, socksAddrIPv4, 0, 0, 0, 0, 0, 0}
	_, err := c.Write(reply)
	return err
}
