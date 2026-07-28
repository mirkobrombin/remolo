package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/mirkobrombin/go-cli-builder/v2/pkg/cli"
	"github.com/mirkobrombin/remolo/internal/brand"
	"github.com/mirkobrombin/remolo/internal/desktop"
	"github.com/mirkobrombin/remolo/internal/protocol"
	"github.com/mirkobrombin/remolo/internal/session"
)

//go:embed viewer.html
var viewerHTML []byte

//go:embed material-symbols.woff2
var iconFont []byte

// DesktopCmd opens a real live interactive remote desktop: it streams the host's
// screen to a local browser window (MJPEG over localhost, carried from the host
// over the remolo connection so it works behind NAT) and forwards mouse and
// keyboard back to the host. For one-off screenshots use 'remolo snapshot'.
type DesktopCmd struct {
	Token  string `arg:"" required:"true" help:"The session token"`
	Addr   string `cli:"addr" help:"Local viewer address (default 127.0.0.1:0, random port)"`
	FPS    int    `cli:"fps" help:"Target capture frame rate (default 15)"`
	NoOpen bool   `cli:"no-open" help:"Do not auto-open the browser"`

	cli.Base
}

func (c *DesktopCmd) Run() error {
	tok, err := decodeToken(c.Token)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs, err := openChannel(ctx, tok, protocol.Open{Kind: protocol.KindDesktop}, session.ConnectOptions{}, c.Logger, true)
	if err != nil {
		return err
	}
	defer cs.cleanup()

	if !cs.reused && !cs.caps.Desktop {
		fmt.Printf("remolo: the host (%s on %s) does not expose a desktop channel.\n", cs.caps.Hostname, cs.caps.OS)
		return nil
	}

	fps := c.FPS
	if fps <= 0 {
		fps = 15
	}

	v := newViewer()
	inputs := make(chan desktop.InputEvent, 256)

	// Pump host frames into the viewer; feed viewer input back to the host.
	go func() {
		_ = desktop.Run(cs.stream, desktop.ModeDiff, fps, v.setFrame, inputs)
		v.close()
	}()

	addr := c.Addr
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("http://%s/", ln.Addr().String())

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(viewerHTML)
	})
	mux.HandleFunc("/stream", v.serveStream)
	mux.HandleFunc("/assets/icons.woff2", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "font/woff2")
		w.Header().Set("Cache-Control", "max-age=86400")
		w.Write(iconFont)
	})
	mux.HandleFunc("/favicon.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "max-age=86400")
		w.Write(brand.Icon)
	})
	mux.HandleFunc("/quality", func(w http.ResponseWriter, r *http.Request) {
		if q, err := strconv.Atoi(r.URL.Query().Get("q")); err == nil {
			if q < 30 {
				q = 30
			} else if q > 95 {
				q = 95
			}
			v.quality.Store(int32(q))
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/input", func(w http.ResponseWriter, r *http.Request) {
		ev, ok := decodeBrowserInput(r.Body)
		if ok {
			select {
			case inputs <- ev:
			default: // drop under backpressure to stay responsive
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})

	srv := &http.Server{Handler: mux}
	go func() { <-ctx.Done(); srv.Close() }()

	fmt.Printf("remolo: live remote desktop at %s\n", url)
	if !c.NoOpen {
		openBrowser(url)
	}
	fmt.Println("remolo: close the browser tab or press Ctrl-C to end the session.")

	err = srv.Serve(ln)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// viewer holds the latest decoded frame and wakes MJPEG writers on each update.
type viewer struct {
	mu      sync.Mutex
	cond    *sync.Cond
	latest  *image.RGBA
	gen     uint64
	done    bool
	quality atomic.Int32 // JPEG quality for the browser MJPEG stream
}

func newViewer() *viewer {
	v := &viewer{}
	v.cond = sync.NewCond(&v.mu)
	v.quality.Store(85)
	return v
}

func (v *viewer) setFrame(img *image.RGBA) {
	v.mu.Lock()
	v.latest = img
	v.gen++
	v.mu.Unlock()
	v.cond.Broadcast()
}

func (v *viewer) close() {
	v.mu.Lock()
	v.done = true
	v.mu.Unlock()
	v.cond.Broadcast()
}

// serveStream writes a multipart/x-mixed-replace MJPEG stream that a browser
// <img> renders natively as live video, with no client-side decoding.
func (v *viewer) serveStream(w http.ResponseWriter, r *http.Request) {
	const boundary = "remoloframe"
	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary="+boundary)
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)

	var lastGen uint64
	for {
		select {
		case <-r.Context().Done():
			return
		default:
		}

		v.mu.Lock()
		for v.gen == lastGen && !v.done {
			v.cond.Wait()
		}
		if v.done && v.gen == lastGen {
			v.mu.Unlock()
			return
		}
		lastGen = v.gen
		img := v.latest
		v.mu.Unlock()

		if img == nil {
			continue
		}
		fmt.Fprintf(w, "--%s\r\nContent-Type: image/jpeg\r\n\r\n", boundary)
		if err := jpeg.Encode(w, img, &jpeg.Options{Quality: int(v.quality.Load())}); err != nil {
			return
		}
		io.WriteString(w, "\r\n")
		if flusher != nil {
			flusher.Flush()
		}
	}
}

// browserInput is the JSON the viewer page POSTs to /input.
type browserInput struct {
	Type   string  `json:"type"`
	FX     float64 `json:"fx"`
	FY     float64 `json:"fy"`
	Button int     `json:"button"`
	Down   bool    `json:"down"`
	Code   string  `json:"code"`
	Dy     float64 `json:"dy"`
}

func decodeBrowserInput(r io.Reader) (desktop.InputEvent, bool) {
	var in browserInput
	if json.NewDecoder(io.LimitReader(r, 4096)).Decode(&in) != nil {
		return desktop.InputEvent{}, false
	}
	// Map fractional (0..1) coordinates onto the absolute pointer range.
	const absMax = 0x7fff
	x := int(in.FX * absMax)
	y := int(in.FY * absMax)
	switch in.Type {
	case "mousemove":
		return desktop.InputEvent{Type: "mousemove", X: x, Y: y}, true
	case "mousebutton":
		return desktop.InputEvent{Type: "mousebutton", Button: mouseButton(in.Button), Down: in.Down, X: x, Y: y}, true
	case "key":
		kc, ok := keycodeFor(in.Code)
		if !ok {
			return desktop.InputEvent{}, false
		}
		return desktop.InputEvent{Type: "key", Key: kc, Down: in.Down}, true
	case "wheel":
		// One wheel click per ~120 browser delta units; invert so scrolling the
		// wheel down scrolls content down.
		step := -1
		if in.Dy < 0 {
			step = 1
		}
		return desktop.InputEvent{Type: "wheel", Dy: step}, true
	}
	return desktop.InputEvent{}, false
}

// mouseButton maps a DOM MouseEvent.button (0 left, 1 middle, 2 right) to the
// Linux BTN_* codes the injector expects.
func mouseButton(domButton int) int {
	switch domButton {
	case 1:
		return desktop.BtnMiddle
	case 2:
		return desktop.BtnRight
	default:
		return desktop.BtnLeft
	}
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
