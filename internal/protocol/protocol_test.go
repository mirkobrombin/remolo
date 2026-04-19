package protocol

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	payloads := map[FrameType][]byte{
		FrameData: []byte("some output"),
		FramePing: nil,
		FrameEOF:  nil,
	}
	order := []FrameType{FrameData, FramePing, FrameEOF}
	for _, ty := range order {
		if err := WriteFrame(&buf, ty, payloads[ty]); err != nil {
			t.Fatalf("write %d: %v", ty, err)
		}
	}
	for _, ty := range order {
		gotType, gotPayload, err := ReadFrame(&buf)
		if err != nil {
			t.Fatalf("read %d: %v", ty, err)
		}
		if gotType != ty {
			t.Fatalf("type mismatch: want %d got %d", ty, gotType)
		}
		if !bytes.Equal(gotPayload, payloads[ty]) {
			t.Fatalf("payload mismatch for %d", ty)
		}
	}
}

func TestOpenRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	want := Open{Kind: KindPTY, Cols: 120, Rows: 40, Term: "xterm-256color", Command: []string{"bash", "-l"}}
	if err := WriteOpen(&buf, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadOpen(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != want.Kind || got.Cols != want.Cols || got.Rows != want.Rows || got.Term != want.Term {
		t.Fatalf("open mismatch: %+v", got)
	}
}

func TestReadOpenRejectsWrongFirstFrame(t *testing.T) {
	var buf bytes.Buffer
	WriteFrame(&buf, FrameData, []byte("not an open"))
	if _, err := ReadOpen(&buf); err == nil {
		t.Fatal("expected error when first frame is not an Open")
	}
}

func TestOversizedFrameRejected(t *testing.T) {
	// Hand-craft a header claiming a payload larger than the cap.
	var hdr [5]byte
	hdr[0] = byte(FrameData)
	binary.BigEndian.PutUint32(hdr[1:], MaxFramePayload+1)
	if _, _, err := ReadFrame(bytes.NewReader(hdr[:])); err == nil {
		t.Fatal("expected oversized frame to be rejected")
	}
	if err := WriteFrame(&bytes.Buffer{}, FrameData, make([]byte, MaxFramePayload+1)); err == nil {
		t.Fatal("expected oversized write to be rejected")
	}
}
