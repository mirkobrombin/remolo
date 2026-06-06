package session

import (
	"path/filepath"

	"github.com/mirkobrombin/remolo/internal/protocol"
)

// scope is the host-enforced capability restriction for a session. The host
// mints and enforces it, so a client cannot widen its own access. An empty
// scope allows everything.
type scope struct {
	kinds    map[protocol.Kind]bool // allowed channel kinds (nil = all)
	cmds     map[string]bool        // allowed command basenames (nil = all)
	readOnly bool                   // forbid write operations
}

func newScope(caps, cmdAllow []string, readOnly bool) scope {
	s := scope{readOnly: readOnly}
	if len(caps) > 0 {
		s.kinds = map[protocol.Kind]bool{protocol.KindControl: true}
		for _, c := range caps {
			s.kinds[protocol.Kind(c)] = true
		}
	}
	if len(cmdAllow) > 0 {
		s.cmds = map[string]bool{}
		for _, c := range cmdAllow {
			s.cmds[c] = true
		}
	}
	return s
}

// writeKinds are channel kinds that can modify the host; refused under read-only.
var writeKinds = map[protocol.Kind]bool{
	protocol.KindPTY:     true,
	protocol.KindExec:    true,
	protocol.KindForward: true,
}

// allowsKind reports whether a channel of kind k may be opened.
func (s scope) allowsKind(k protocol.Kind) bool {
	if s.readOnly && writeKinds[k] {
		return false
	}
	if s.kinds == nil {
		return true
	}
	return s.kinds[k]
}

// allowsCmd reports whether a command may be run (by basename of argv[0]).
func (s scope) allowsCmd(argv []string) bool {
	if s.cmds == nil {
		return true
	}
	if len(argv) == 0 {
		return false
	}
	return s.cmds[filepath.Base(argv[0])]
}
