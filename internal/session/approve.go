package session

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"sync"
)

// approver serialises interactive connection-approval prompts on the host's
// terminal. Prompts are mutually exclusive so concurrent connections do not
// interleave on stdin.
type approver struct {
	mu sync.Mutex
	in *bufio.Reader
}

func newApprover() *approver { return &approver{in: bufio.NewReader(os.Stdin)} }

// approve asks the host operator to accept a connection from remote. It returns
// true only on an explicit yes. If stdin is not interactive (EOF), it denies.
func (a *approver) approve(remote string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	fmt.Fprintf(os.Stderr, "remolo: accept connection from %s? [y/N]: ", remote)
	line, err := a.in.ReadString('\n')
	if err != nil {
		fmt.Fprintln(os.Stderr, "(no input, denied)")
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}
