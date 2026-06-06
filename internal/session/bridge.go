package session

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/mirkobrombin/remolo/internal/protocol"
	"github.com/mirkobrombin/remolo/internal/transport"
)

// BridgeIO connects a local byte stream (a terminal in raw mode, or stdin/
// stdout for exec) to a remote channel using the frame protocol. It returns the
// remote process's exit code.
//
//   - bytes read from in are sent as FrameData;
//   - FrameData received from the channel is written to out;
//   - resize events from the resize channel become FrameResize;
//   - a FrameExit ends the bridge with the reported code.
//
// in may be nil (no input side). resize may be nil (no resize support).
func BridgeIO(stream transport.Stream, in io.Reader, out io.Writer, resize <-chan protocol.Resize) (int, error) {
	return BridgeIOErr(stream, in, out, nil, resize)
}

// BridgeIOErr is like BridgeIO but routes FrameStderr to a separate writer. If
// errOut is nil, stderr is merged into out.
func BridgeIOErr(stream transport.Stream, in io.Reader, out, errOut io.Writer, resize <-chan protocol.Resize) (int, error) {
	if errOut == nil {
		errOut = out
	}
	// Input pump: local -> remote.
	if in != nil {
		go func() {
			buf := make([]byte, copyBuf)
			for {
				n, err := in.Read(buf)
				if n > 0 {
					if werr := protocol.WriteFrame(stream, protocol.FrameData, buf[:n]); werr != nil {
						return
					}
				}
				if err != nil {
					_ = protocol.WriteFrame(stream, protocol.FrameEOF, nil)
					return
				}
			}
		}()
	}

	// Resize pump: forward window changes.
	if resize != nil {
		go func() {
			for r := range resize {
				b, _ := json.Marshal(r)
				if err := protocol.WriteFrame(stream, protocol.FrameResize, b); err != nil {
					return
				}
			}
		}()
	}

	// Output pump (this goroutine): remote -> local, until exit.
	for {
		ft, payload, err := protocol.ReadFrame(stream)
		if err != nil {
			return -1, err
		}
		switch ft {
		case protocol.FrameData:
			if _, err := out.Write(payload); err != nil {
				return -1, err
			}
		case protocol.FrameStderr:
			if _, err := errOut.Write(payload); err != nil {
				return -1, err
			}
		case protocol.FrameExit:
			var ex protocol.Exit
			_ = json.Unmarshal(payload, &ex)
			if ex.Err != "" {
				return ex.Code, fmt.Errorf("remote: %s", ex.Err)
			}
			return ex.Code, nil
		}
	}
}
