//go:build windows

package pty

import (
	"fmt"
	"os"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Supported reports whether this platform can allocate a PTY.
func Supported() bool { return true }

// winPTY is a ConPTY-backed pseudo-terminal. The pseudo console owns two pairs
// of anonymous pipes: the child reads its stdin from inputRead and writes its
// stdout/stderr to outputWrite, while we drive it through inputWrite and read
// its output from outputRead.
type winPTY struct {
	hpc windows.Handle // the pseudo console handle (HPCON)

	// in is the write end of the input pipe: bytes we send to the child.
	in *os.File
	// out is the read end of the output pipe: bytes the child produced.
	out *os.File

	proc   windows.Handle // child process handle
	thread windows.Handle // child primary thread handle

	closeOnce sync.Once
	closeErr  error
}

// Start launches the configured command attached to a fresh ConPTY.
func Start(cfg Config) (PTY, error) {
	// Two anonymous pipes. ConPTY reads the child's stdin from inputRead and
	// writes the child's output to outputWrite; we keep the opposite ends.
	var inputRead, inputWrite windows.Handle
	var outputRead, outputWrite windows.Handle

	if err := windows.CreatePipe(&inputRead, &inputWrite, nil, 0); err != nil {
		return nil, fmt.Errorf("pty: create input pipe: %w", err)
	}
	if err := windows.CreatePipe(&outputRead, &outputWrite, nil, 0); err != nil {
		windows.CloseHandle(inputRead)
		windows.CloseHandle(inputWrite)
		return nil, fmt.Errorf("pty: create output pipe: %w", err)
	}

	// Any failure past this point must release the handles allocated above.
	cleanup := func() {
		windows.CloseHandle(inputRead)
		windows.CloseHandle(inputWrite)
		windows.CloseHandle(outputRead)
		windows.CloseHandle(outputWrite)
	}

	size := windows.Coord{
		X: int16(orDefault(cfg.Cols, 80)),
		Y: int16(orDefault(cfg.Rows, 24)),
	}

	var hpc windows.Handle
	if err := windows.CreatePseudoConsole(size, inputRead, outputWrite, 0, &hpc); err != nil {
		cleanup()
		return nil, fmt.Errorf("pty: create pseudo console: %w", err)
	}

	// The pseudo console has duplicated the handles it needs (inputRead and
	// outputWrite). We hand those ends entirely to ConPTY, so close our copies
	// now to avoid leaking them and to let EOF propagate correctly.
	windows.CloseHandle(inputRead)
	windows.CloseHandle(outputWrite)
	inputRead = windows.InvalidHandle
	outputWrite = windows.InvalidHandle

	closeRemaining := func() {
		windows.ClosePseudoConsole(hpc)
		windows.CloseHandle(inputWrite)
		windows.CloseHandle(outputRead)
	}

	proc, thread, err := startProcess(cfg, hpc)
	if err != nil {
		closeRemaining()
		return nil, err
	}

	p := &winPTY{
		hpc:    hpc,
		in:     os.NewFile(uintptr(inputWrite), "conpty-in"),
		out:    os.NewFile(uintptr(outputRead), "conpty-out"),
		proc:   proc,
		thread: thread,
	}
	return p, nil
}

// startProcess spawns cfg.Command attached to the pseudo console hpc and returns
// the process and thread handles.
func startProcess(cfg Config, hpc windows.Handle) (proc, thread windows.Handle, err error) {
	argv := cfg.Command
	if len(argv) == 0 {
		argv = []string{loginShell()}
	}

	cmdLine, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(argv))
	if err != nil {
		return 0, 0, fmt.Errorf("pty: build command line: %w", err)
	}

	var dirPtr *uint16
	dir := cfg.Dir
	if dir == "" {
		if home, herr := os.UserHomeDir(); herr == nil {
			dir = home
		}
	}
	if dir != "" {
		dirPtr, err = windows.UTF16PtrFromString(dir)
		if err != nil {
			return 0, 0, fmt.Errorf("pty: build working directory: %w", err)
		}
	}

	envBlock, err := buildEnvBlock(cfg)
	if err != nil {
		return 0, 0, err
	}

	// Build the thread attribute list and point it at the pseudo console so the
	// new process is wired to ConPTY rather than inheriting our console.
	attrList, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return 0, 0, fmt.Errorf("pty: alloc attribute list: %w", err)
	}
	defer attrList.Delete()

	// For PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE the attribute value is the HPCON
	// handle itself, passed by value in the lpValue slot (not a pointer to it).
	// windows.Handle is a uintptr, so reinterpret its bits as an unsafe.Pointer
	// without an integer-to-pointer conversion that go vet would flag.
	hpcValue := *(*unsafe.Pointer)(unsafe.Pointer(&hpc))
	if err := attrList.Update(
		windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE,
		hpcValue,
		unsafe.Sizeof(hpc),
	); err != nil {
		return 0, 0, fmt.Errorf("pty: set pseudo console attribute: %w", err)
	}

	si := new(windows.StartupInfoEx)
	si.Cb = uint32(unsafe.Sizeof(*si))
	si.Flags = windows.STARTF_USESTDHANDLES
	si.ProcThreadAttributeList = attrList.List()

	var pi windows.ProcessInformation

	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT)

	// CreateProcess wants a *StartupInfo; StartupInfoEx embeds it as the first
	// field, so the pointer is layout-compatible and the extended flag tells the
	// kernel to read the trailing attribute list.
	if err := windows.CreateProcess(
		nil,
		cmdLine,
		nil,
		nil,
		false, // do not inherit handles; ConPTY owns the relevant ones
		flags,
		envBlock,
		dirPtr,
		&si.StartupInfo,
		&pi,
	); err != nil {
		return 0, 0, fmt.Errorf("pty: create process %q: %w", argv[0], err)
	}

	return pi.Process, pi.Thread, nil
}

// buildEnvBlock produces a CREATE_UNICODE_ENVIRONMENT block: the host
// environment with TERM and any cfg.Env overrides appended, encoded as a
// double-NUL-terminated sequence of UTF-16 KEY=VALUE strings. A nil result means
// inherit the parent's environment unchanged.
func buildEnvBlock(cfg Config) (*uint16, error) {
	entries := append([]string{}, os.Environ()...)
	entries = append(entries, "TERM="+defaultTerm(cfg.Term))
	entries = append(entries, cfg.Env...)

	var block []uint16
	for _, e := range entries {
		if e == "" {
			continue
		}
		u, err := windows.UTF16FromString(e)
		if err != nil {
			// Skip entries with embedded NULs rather than failing the launch.
			continue
		}
		// UTF16FromString returns a NUL-terminated slice; keep the terminator as
		// the separator between entries.
		block = append(block, u...)
	}
	// Final extra NUL terminates the whole block.
	block = append(block, 0)

	return &block[0], nil
}

func (p *winPTY) Read(b []byte) (int, error)  { return p.out.Read(b) }
func (p *winPTY) Write(b []byte) (int, error) { return p.in.Write(b) }

// Resize changes the ConPTY window size.
func (p *winPTY) Resize(cols, rows uint16) error {
	size := windows.Coord{X: int16(cols), Y: int16(rows)}
	if err := windows.ResizePseudoConsole(p.hpc, size); err != nil {
		return fmt.Errorf("pty: resize: %w", err)
	}
	return nil
}

// Wait blocks until the child exits and returns its exit code.
func (p *winPTY) Wait() (int, error) {
	event, err := windows.WaitForSingleObject(p.proc, windows.INFINITE)
	if err != nil {
		return -1, fmt.Errorf("pty: wait: %w", err)
	}
	if event != windows.WAIT_OBJECT_0 {
		return -1, fmt.Errorf("pty: wait returned unexpected state %#x", event)
	}

	var code uint32
	if err := windows.GetExitCodeProcess(p.proc, &code); err != nil {
		return -1, fmt.Errorf("pty: exit code: %w", err)
	}
	return int(int32(code)), nil
}

// Close tears down the pseudo console, the pipes, and the process handles.
//
// ClosePseudoConsole is issued first: it severs the child's console, which lets
// any pending Read on the output pipe drain and return EOF instead of hanging.
func (p *winPTY) Close() error {
	p.closeOnce.Do(func() {
		windows.ClosePseudoConsole(p.hpc)

		if p.out != nil {
			if err := p.out.Close(); err != nil && p.closeErr == nil {
				p.closeErr = err
			}
		}
		if p.in != nil {
			if err := p.in.Close(); err != nil && p.closeErr == nil {
				p.closeErr = err
			}
		}
		if p.thread != 0 && p.thread != windows.InvalidHandle {
			windows.CloseHandle(p.thread)
		}
		if p.proc != 0 && p.proc != windows.InvalidHandle {
			windows.CloseHandle(p.proc)
		}
	})
	return p.closeErr
}

// orDefault returns v unless it is zero, in which case it returns d.
func orDefault(v, d uint16) uint16 {
	if v == 0 {
		return d
	}
	return v
}

// loginShell resolves the user's preferred command interpreter, defaulting to
// the value of COMSPEC and ultimately cmd.exe.
func loginShell() string {
	if sh := os.Getenv("COMSPEC"); sh != "" {
		return sh
	}
	return "cmd.exe"
}
