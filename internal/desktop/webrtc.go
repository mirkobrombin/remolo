package desktop

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"sync"
	"time"

	"github.com/mirkobrombin/remolo/internal/protocol"
	"github.com/pion/webrtc/v4"
)

// This file implements ModeWebRTC: a real pion/webrtc/v4 transport for desktop
// frames and input.
//
// Signaling design (non-trickle, robust on LAN/localhost):
//
//   - The two peers exchange a tiny JSON envelope (signalMsg) over the existing
//     `stream`, carried as protocol.FrameJSON frames. The session layer has
//     already negotiated Hello/Select before WebRTC starts, so the very first
//     WebRTC envelope is the host's offer.
//   - Each side creates its SessionDescription, calls SetLocalDescription, then
//     WAITS for ICE gathering to complete (GatheringCompletePromise) before
//     sending pc.LocalDescription() over the stream. Because gathering is
//     finished, the SDP already embeds every local ICE candidate, so no separate
//     trickle messages are needed. This is simpler and reliable on localhost,
//     where host candidates pair directly.
//   - ICEServers is empty (no STUN/TURN): host candidates suffice on a LAN or
//     loopback. This keeps the transport self-contained with no external deps.
//
// Media transport: a single reliable, ordered DataChannel named "data" is opened
// by the host. Frames (host -> client) and InputEvents (client -> host) both
// travel on it, distinguished by a 1-byte channel tag. Encoded frame payloads
// can exceed a single SCTP message, so they are split into chunks with a small
// reassembly header.

// signalMsg is the SDP signaling envelope exchanged over `stream`.
type signalMsg struct {
	Kind string                     `json:"kind"` // "offer" or "answer"
	SDP  *webrtc.SessionDescription `json:"sdp"`
}

// DataChannel message tags.
const (
	dcTagFrame byte = 1 // host -> client: a frame chunk
	dcTagInput byte = 2 // client -> host: an InputEvent JSON
)

// frame chunking: a frame payload is split into chunks small enough for SCTP.
// Each chunk on the wire is:
//
//	[1 byte dcTagFrame]
//	[4 byte big-endian uint32 frame sequence]
//	[4 byte big-endian uint32 total payload length]
//	[4 byte big-endian uint32 chunk offset]
//	[chunk bytes]
//
// The receiver buffers chunks per sequence until offset+len == total, then
// hands the full payload to the Decoder.
const (
	dcChunkHeaderLen = 1 + 4 + 4 + 4
	dcMaxChunk       = 16 * 1024 // keep well under SCTP message limits
)

// webrtcConnectTimeout bounds how long we wait for the data channel to open.
const webrtcConnectTimeout = 30 * time.Second

// newPeerConnection builds a STUN-free PeerConnection.
func newPeerConnection() (*webrtc.PeerConnection, error) {
	return webrtc.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{},
	})
}

// gatherAndSend waits for ICE gathering to complete and then writes the local
// description to the stream under the given kind.
func gatherAndSend(stream io.Writer, pc *webrtc.PeerConnection, kind string) error {
	<-webrtc.GatheringCompletePromise(pc)
	msg := signalMsg{Kind: kind, SDP: pc.LocalDescription()}
	return protocol.WriteJSON(stream, msg)
}

// readSignal reads one signaling envelope from the stream.
func readSignal(stream io.Reader) (signalMsg, error) {
	t, payload, err := protocol.ReadFrame(stream)
	if err != nil {
		return signalMsg{}, err
	}
	if t != protocol.FrameJSON {
		return signalMsg{}, fmt.Errorf("desktop: webrtc: expected signaling JSON, got frame type %d", t)
	}
	var m signalMsg
	if err := json.Unmarshal(payload, &m); err != nil {
		return signalMsg{}, fmt.Errorf("desktop: webrtc: decode signal: %w", err)
	}
	if m.SDP == nil {
		return signalMsg{}, fmt.Errorf("desktop: webrtc: signal missing sdp")
	}
	return m, nil
}

// serveWebRTC is the host side of ModeWebRTC. It creates the PeerConnection and
// data channel, performs the offer/answer exchange over `stream`, then captures
// and sends encoded frames while applying incoming InputEvents via inj.
func serveWebRTC(stream io.ReadWriter, cap Capturer, inj Injector) (err error) {
	if inj == nil {
		inj = NopInjector{}
	}

	pc, err := newPeerConnection()
	if err != nil {
		return fmt.Errorf("desktop: webrtc: new peer connection: %w", err)
	}
	defer pc.Close()

	ordered := true
	dc, err := pc.CreateDataChannel("data", &webrtc.DataChannelInit{Ordered: &ordered})
	if err != nil {
		return fmt.Errorf("desktop: webrtc: create data channel: %w", err)
	}

	opened := make(chan struct{})
	var openOnce sync.Once
	dc.OnOpen(func() { openOnce.Do(func() { close(opened) }) })

	// Apply input events from the client.
	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		if len(msg.Data) < 1 || msg.Data[0] != dcTagInput {
			return
		}
		var ev InputEvent
		if json.Unmarshal(msg.Data[1:], &ev) == nil {
			applyInput(inj, ev)
		}
	})

	// Offer/answer.
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return fmt.Errorf("desktop: webrtc: create offer: %w", err)
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		return fmt.Errorf("desktop: webrtc: set local offer: %w", err)
	}
	if err := gatherAndSend(stream, pc, "offer"); err != nil {
		return fmt.Errorf("desktop: webrtc: send offer: %w", err)
	}

	answer, err := readSignal(stream)
	if err != nil {
		return fmt.Errorf("desktop: webrtc: read answer: %w", err)
	}
	if err := pc.SetRemoteDescription(*answer.SDP); err != nil {
		return fmt.Errorf("desktop: webrtc: set remote answer: %w", err)
	}

	closed := make(chan struct{})
	var closeOnce sync.Once
	shutdown := func() { closeOnce.Do(func() { close(closed) }) }
	dc.OnClose(func() { shutdown() })
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		if s == webrtc.PeerConnectionStateFailed ||
			s == webrtc.PeerConnectionStateClosed ||
			s == webrtc.PeerConnectionStateDisconnected {
			shutdown()
		}
	})
	// Closing the signaling stream is the subsystem-wide teardown signal (the
	// session/CLI closes it to end a desktop session). Since signaling is done,
	// any read returns only when the stream closes; that triggers shutdown.
	go watchStreamClose(stream, shutdown)

	select {
	case <-opened:
	case <-closed:
		return nil
	case <-time.After(webrtcConnectTimeout):
		return fmt.Errorf("desktop: webrtc: data channel did not open in time")
	}

	// Stream frames until the channel closes. The default FPS matches the rest of
	// the subsystem; WebRTC mode does not renegotiate FPS in this build.
	enc := NewEncoder(ModeDiff)
	ticker := newRateTicker(defaultFPS)
	defer ticker.Stop()

	for {
		select {
		case <-closed:
			return nil
		case <-ticker.C:
		}

		img, cerr := cap.Capture()
		if cerr != nil {
			return fmt.Errorf("desktop: webrtc: capture: %w", cerr)
		}
		payload, eerr := enc.Encode(img)
		if eerr != nil {
			return eerr
		}
		if serr := sendFrame(dc, enc.seq, payload); serr != nil {
			// A send error usually means the channel closed; treat as clean end.
			return nil
		}
	}
}

// watchStreamClose blocks reading the signaling stream and invokes onClose when
// a read fails, which happens when the peer closes the stream. After signaling
// no further frames are expected, so this read simply parks until teardown.
func watchStreamClose(stream io.Reader, onClose func()) {
	for {
		if _, _, err := protocol.ReadFrame(stream); err != nil {
			onClose()
			return
		}
	}
}

// sendFrame splits payload into chunks and sends them on the data channel.
func sendFrame(dc *webrtc.DataChannel, seq uint32, payload []byte) error {
	total := uint32(len(payload))
	for off := 0; off < len(payload) || off == 0; off += dcMaxChunk {
		end := off + dcMaxChunk
		if end > len(payload) {
			end = len(payload)
		}
		chunk := payload[off:end]
		buf := make([]byte, dcChunkHeaderLen+len(chunk))
		buf[0] = dcTagFrame
		binary.BigEndian.PutUint32(buf[1:5], seq)
		binary.BigEndian.PutUint32(buf[5:9], total)
		binary.BigEndian.PutUint32(buf[9:13], uint32(off))
		copy(buf[dcChunkHeaderLen:], chunk)
		if err := dc.Send(buf); err != nil {
			return err
		}
		if end >= len(payload) {
			break
		}
	}
	return nil
}

// frameReassembler buffers frame chunks until a full payload is available.
type frameReassembler struct {
	seq   uint32
	total uint32
	buf   []byte
	have  uint32
}

// add ingests one chunk and returns the complete payload when the frame is whole
// (ok=true), or nil/false while still assembling.
func (r *frameReassembler) add(data []byte) (payload []byte, ok bool) {
	if len(data) < dcChunkHeaderLen {
		return nil, false
	}
	seq := binary.BigEndian.Uint32(data[1:5])
	total := binary.BigEndian.Uint32(data[5:9])
	off := binary.BigEndian.Uint32(data[9:13])
	body := data[dcChunkHeaderLen:]

	if seq != r.seq || r.buf == nil || uint32(len(r.buf)) != total {
		// New frame (or first chunk): reset the buffer.
		r.seq = seq
		r.total = total
		r.buf = make([]byte, total)
		r.have = 0
	}
	if int(off)+len(body) > len(r.buf) {
		return nil, false
	}
	copy(r.buf[off:], body)
	r.have += uint32(len(body))
	if r.have >= r.total {
		out := r.buf
		r.buf = nil
		return out, true
	}
	return nil, false
}

// runWebRTC is the client side of ModeWebRTC. It answers the host's offer over
// `stream`, then decodes frames received on the data channel and forwards input
// events.
func runWebRTC(stream io.ReadWriter, fps int, onFrame func(*image.RGBA), inputs <-chan InputEvent) (err error) {
	pc, err := newPeerConnection()
	if err != nil {
		return fmt.Errorf("desktop: webrtc: new peer connection: %w", err)
	}
	defer pc.Close()

	dec := NewDecoder()
	var reasm frameReassembler

	dcReady := make(chan *webrtc.DataChannel, 1)
	frameErr := make(chan error, 1)
	closed := make(chan struct{})
	var closeOnce sync.Once

	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		if dc.Label() != "data" {
			return
		}
		dc.OnOpen(func() {
			select {
			case dcReady <- dc:
			default:
			}
		})
		dc.OnClose(func() { closeOnce.Do(func() { close(closed) }) })
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			if len(msg.Data) < 1 || msg.Data[0] != dcTagFrame {
				return
			}
			payload, ok := reasm.add(msg.Data)
			if !ok {
				return
			}
			img, derr := dec.Decode(payload)
			if derr != nil {
				select {
				case frameErr <- derr:
				default:
				}
				return
			}
			if onFrame != nil {
				onFrame(img)
			}
		})
	})

	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		if s == webrtc.PeerConnectionStateFailed ||
			s == webrtc.PeerConnectionStateClosed ||
			s == webrtc.PeerConnectionStateDisconnected {
			closeOnce.Do(func() { close(closed) })
		}
	})

	// Receive offer, answer it.
	offer, err := readSignal(stream)
	if err != nil {
		return fmt.Errorf("desktop: webrtc: read offer: %w", err)
	}
	if err := pc.SetRemoteDescription(*offer.SDP); err != nil {
		return fmt.Errorf("desktop: webrtc: set remote offer: %w", err)
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return fmt.Errorf("desktop: webrtc: create answer: %w", err)
	}
	if err := pc.SetLocalDescription(answer); err != nil {
		return fmt.Errorf("desktop: webrtc: set local answer: %w", err)
	}
	if err := gatherAndSend(stream, pc, "answer"); err != nil {
		return fmt.Errorf("desktop: webrtc: send answer: %w", err)
	}

	// Closing the signaling stream is the subsystem-wide teardown signal.
	go watchStreamClose(stream, func() { closeOnce.Do(func() { close(closed) }) })

	var dc *webrtc.DataChannel
	select {
	case dc = <-dcReady:
	case <-time.After(webrtcConnectTimeout):
		return fmt.Errorf("desktop: webrtc: data channel did not open in time")
	case <-closed:
		return nil
	}

	// Forward input events on the same channel.
	done := make(chan struct{})
	var fwdWG sync.WaitGroup
	if inputs != nil {
		fwdWG.Add(1)
		go func() {
			defer fwdWG.Done()
			for {
				select {
				case <-done:
					return
				case <-closed:
					return
				case ev, ok := <-inputs:
					if !ok {
						return
					}
					b, merr := json.Marshal(ev)
					if merr != nil {
						continue
					}
					out := make([]byte, 1+len(b))
					out[0] = dcTagInput
					copy(out[1:], b)
					_ = dc.Send(out)
				}
			}
		}()
	}
	defer func() {
		close(done)
		fwdWG.Wait()
	}()

	select {
	case <-closed:
		return nil
	case ferr := <-frameErr:
		return ferr
	}
}
