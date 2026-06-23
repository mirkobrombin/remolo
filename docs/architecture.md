# remolo architecture

The parts you cannot infer by reading the tree.

## The token is the whole configuration

The token carries the host's public key, a pre-shared secret, every reachable
endpoint (every IP of every interface, IPv4 + IPv6), a peer session id and an
expiry, as CBOR + CRC32 in Base32-Crockford. That is why there is nothing to
configure: the knowledge of where the host lives, and of who it is, travels with
the credential. The client races every candidate in parallel and keeps the first
that answers.

## The ladder

Strategies are ranked and raced with a small stagger, best first:

| Rung | When it wins |
|---|---|
| Direct QUIC, then TCP, per token candidate | same LAN or routed subnets |
| mDNS discovery | same local segment |
| Rendezvous-resolved candidates / STUN reflexive | across NAT |
| Relay | symmetric NAT, hole-punch failed |
| SSH tunnel | locked-down hosts running sshd |
| Manual IP prompt | everything else failed |

Every rung satisfies the same `transport.Conn`, so the selector never knows which
one it picked. QUIC multiplexes natively; TCP, relay and SSH get yamux. The relay
and the bastion only ever move ciphertext: authentication is between the two
peers, so an intermediary cannot become one.

## One connection, many channels

A channel is a stream whose first frame declares its kind (`control`, `pty`,
`exec`, `file`, `sync`, `rpc`, `forward`, `desktop`, `agent`). They run
concurrently over the same encrypted connection, and one dying does not disturb
the others.

The first `connect` also starts a mux daemon on a unix socket keyed by the peer
session id. Later invocations find it and open a channel on the connection that
already exists, which is why the second command starts instantly instead of
repeating NAT traversal and the handshake.

## Platform support

| | Linux | macOS | Windows |
|---|---|---|---|
| Shell (PTY) | creack/pty | creack/pty | ConPTY |
| Screen capture | X11 (xgb) / Wayland (portal, `grim` fallback) | `screencapture` | GDI |
| Input injection | `/dev/uinput` | not available | not available |
| FUSE mount | yes | yes | not available |

All CGO-free, on amd64 and arm64. With no reachable display the host falls back
to a synthetic frame source so a session degrades instead of dying.

`remolo desktop` sends tile diffs and renders them as MJPEG in the browser. A
WebRTC transport (pion, SDP over the control stream, media over an SCTP data
channel) is implemented and tested, but the command does not select it yet.
