package dirsync

import (
	"bytes"
	"crypto/rand"
	"testing"
)

// TestRollByteMatchesWeakSum verifies the incremental roller stays in sync with
// the from-scratch weakSum as the window slides across data.
func TestRollByteMatchesWeakSum(t *testing.T) {
	data := make([]byte, 8192)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	bs := uint32(BlockSize)
	a, b := initRoll(data[0:BlockSize])
	for i := 0; i+BlockSize < len(data); i++ {
		want := weakSum(data[i : i+BlockSize])
		got := (b << 16) | a
		if got != want {
			t.Fatalf("at offset %d: rolled %x want %x", i, got, want)
		}
		a, b = rollByte(a, b, uint32(data[i]), uint32(data[i+BlockSize]), bs)
	}
}

// TestDeltaRoundTrip checks that a delta against a base reconstructs the source
// and that an edit moves far less than the whole file.
func TestDeltaRoundTrip(t *testing.T) {
	base := make([]byte, 64*1024)
	if _, err := rand.Read(base); err != nil {
		t.Fatal(err)
	}
	src := append([]byte(nil), base...)
	src[40000] ^= 0xFF // one-byte edit

	sig, err := BuildSignature(bytes.NewReader(base))
	if err != nil {
		t.Fatal(err)
	}
	var delta bytes.Buffer
	literal, err := WriteDelta(&delta, bytes.NewReader(src), sig)
	if err != nil {
		t.Fatal(err)
	}
	if literal >= int64(len(src)/2) {
		t.Fatalf("literal bytes %d too high for a 1-byte edit", literal)
	}
	var out bytes.Buffer
	if err := ApplyDelta(&out, bytes.NewReader(base), int64(len(base)), BlockSize, &delta); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), src) {
		t.Fatalf("reconstructed content differs from source")
	}
}
