package rendezvous

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mirkobrombin/remolo/internal/token"
)

func TestRegisterLookupRoundTrip(t *testing.T) {
	srv := NewServer(time.Minute)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	want := []token.Candidate{
		{IP: "192.168.1.10", Port: 4242},
		{IP: "2001:db8::1", Port: 5353},
	}
	const session = "deadbeefcafef00d"

	if err := Register(ts.URL, session, want); err != nil {
		t.Fatalf("Register: %v", err)
	}

	got, err := Lookup(ts.URL, session)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d candidates, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("candidate %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestLookupMissing(t *testing.T) {
	srv := NewServer(time.Minute)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	if _, err := Lookup(ts.URL, "nope"); err == nil {
		t.Fatal("expected error for missing session, got nil")
	}
}

func TestExpiry(t *testing.T) {
	srv := NewServer(time.Minute)

	// Drive a controllable clock so we do not have to sleep.
	base := time.Now()
	current := base
	srv.now = func() time.Time { return current }

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	const session = "abc123"
	if err := Register(ts.URL, session, []token.Candidate{{IP: "10.0.0.1", Port: 1}}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Still valid just before the TTL.
	current = base.Add(59 * time.Second)
	if _, err := Lookup(ts.URL, session); err != nil {
		t.Fatalf("Lookup before expiry: %v", err)
	}

	// Expired past the TTL.
	current = base.Add(2 * time.Minute)
	if _, err := Lookup(ts.URL, session); err == nil {
		t.Fatal("expected expiry error, got nil")
	}
}
