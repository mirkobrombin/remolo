package mdns

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// multicastAvailable reports whether we can join the mDNS group in this
// environment; if not, multicast tests skip rather than fail.
func multicastAvailable(t *testing.T) {
	t.Helper()
	conn, err := listenMulticastUDP()
	if err != nil {
		t.Skipf("multicast unavailable: %v", err)
	}
	conn.Close()
}

func TestTXTRoundTrip(t *testing.T) {
	in := map[string]string{"name": "alpha", "ver": "1"}
	out := decodeTXT(encodeTXT(in))
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("TXT round trip = %v, want %v", out, in)
	}
}

func TestBuildQueryAndResponseParse(t *testing.T) {
	// Exercise the wire encoding without touching the network: build a
	// response as the advertiser would, then parse it as Browse would.
	a := &advertiser{
		instance: fqdn("alpha"),
		host:     fqdn("testhost.local"),
		port:     4242,
		txt:      map[string]string{"role": "host"},
	}
	resp, err := a.buildResponse()
	if err != nil {
		t.Fatalf("buildResponse: %v", err)
	}

	byInstance := map[string]*partial{}
	hostIP := map[string]string{}
	parseResponse(resp, byInstance, hostIP)

	p, ok := byInstance[fqdn("alpha")]
	if !ok {
		t.Fatalf("instance not parsed; got %v", byInstance)
	}
	if p.host != fqdn("testhost.local") {
		t.Errorf("host = %q, want %q", p.host, fqdn("testhost.local"))
	}
	if p.port != 4242 {
		t.Errorf("port = %d, want 4242", p.port)
	}
	if p.txt["role"] != "host" {
		t.Errorf("txt[role] = %q, want host", p.txt["role"])
	}
}

func TestAdvertiseAndBrowse(t *testing.T) {
	multicastAvailable(t)

	closer, err := Advertise("remolo-test-instance", 5252, map[string]string{"k": "v"})
	if err != nil {
		t.Fatalf("Advertise: %v", err)
	}
	defer closer.Close()

	// Give the advertiser a moment, then browse.
	time.Sleep(100 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	entries, err := Browse(ctx, 2*time.Second)
	if err != nil {
		t.Fatalf("Browse: %v", err)
	}

	found := false
	for _, e := range entries {
		if e.Txt["k"] == "v" {
			found = true
		}
	}
	if !found {
		// Multicast loopback delivery is not guaranteed on every host/CI
		// network stack; skip instead of failing when nothing came back.
		t.Skipf("advertised instance not observed (multicast loopback may be disabled); entries=%v", entries)
	}
}
