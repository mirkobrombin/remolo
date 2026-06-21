// Command remolo-rendezvous runs a standalone, self-hostable rendezvous broker
// that lets remolo peers discover each other's candidate endpoints by session
// id. It keeps state in memory and expires entries after a TTL.
package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/mirkobrombin/remolo/internal/rendezvous"
)

func main() {
	addr := flag.String("addr", ":8787", "address to listen on")
	ttl := flag.Duration("ttl", 10*time.Minute, "how long a registration stays valid")
	flag.Parse()

	srv := rendezvous.NewServer(*ttl)

	httpServer := &http.Server{
		Addr:    *addr,
		Handler: srv.Handler(),
	}

	log.Printf("remolo-rendezvous listening on %s (ttl %s)", *addr, ttl.String())
	if err := httpServer.ListenAndServe(); err != nil {
		log.Fatalf("remolo-rendezvous: %v", err)
	}
}
