// Command remolo-relay runs a standalone remolo relay server. The relay pairs
// a host and a client that share a session id and copies opaque bytes between
// them; it never sees plaintext, since encryption is end-to-end between the
// two peers.
package main

import (
	"flag"
	"log"
	"net"

	"github.com/mirkobrombin/remolo/internal/relay"
)

func main() {
	addr := flag.String("addr", ":8788", "TCP address to listen on (host:port)")
	flag.Parse()

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("remolo-relay: listen %s: %v", *addr, err)
	}
	log.Printf("remolo-relay: listening on %s", ln.Addr())

	srv := relay.NewServer()
	if err := srv.Serve(ln); err != nil {
		log.Fatalf("remolo-relay: serve: %v", err)
	}
}
