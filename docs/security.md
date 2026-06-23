# remolo security model

remolo is a remote-control tool. An insecure one is a weapon, so security is
part of the design, not an afterthought.

## Authorized use only

remolo is for machines you own or control with explicit consent. The token is
shared deliberately by the host operator; possession of a valid token is
authorization to connect. The host prints a consent reminder on start.

## What protects a session

- **End-to-end encryption.** Every transport carries TLS 1.3 (native to QUIC,
  layered explicitly on the TCP, relay and SSH rungs). Even when traffic passes
  through a relay, the relay only ever sees ciphertext.
- **Host authentication (anti-MITM).** The host owns an Ed25519 key whose public
  half is in the token. The client pins the presented certificate against that
  key. An attacker who knows the host's IP, or who controls the network path,
  cannot impersonate the host: the pin will not match.
- **Client authentication.** After the channel is up, both sides run a PSK
  challenge-response (HMAC-SHA256 over fresh random nonces, bound to the host
  public key). This proves the client holds the token's secret, re-confirms the
  host, and derives a session key. Fresh nonces make it replay-safe.
- **Known-hosts memory.** Connecting by alias records the host key on first use
  and refuses loudly if it ever changes, the way SSH does.

## The token is the credential

Treat a token like a password.

- Tokens expire (`--ttl`, default 24h). The expiry is enforced on both sides:
  the client refuses an expired token, and the host refuses a PSK handshake once
  the window has passed.
- `--once` makes a token single-use: the host admits one authenticated peer and
  then refuses further connections.
- Secrets are wiped from memory when no longer needed (host token PSK on close).

## Narrowing what a token can do

Enforcement is entirely host-side, so a client cannot widen its own access.

- `--cap <kind>` restricts the session to specific channel kinds (pty, exec,
  file, sync, rpc, desktop, forward).
- `--cmd <name>` is an allow-list of exec/pty commands by basename.
- `--ro` is read-only: no exec, pty, forward, upload or sync push.
- `--file-root <dir>` confines file transfer and the FUSE mount to one directory.
- `--approve` prompts the operator before admitting each new connection.

## Long-lived identities

- `remolo host --identity <path>` keeps a stable host key across restarts.
- `remolo enroll` registers a client public key in the host's authorized list, so
  later connections need no token and authenticate with an Ed25519
  challenge-response bound to both nonces and the host key.
- `remolo revoke <fingerprint>` removes an enrolled key, on the host.

## Hardening

- Per-source rate limiting on authentication attempts blunts brute-force against
  the PSK.
- The frame parser rejects oversized frames, so a malicious peer cannot force a
  huge allocation.
- The token parser is fuzzed in CI (it is an attacker-reachable surface) and
  validates defensively with a checksum.
- File transfer enforces a root directory and rejects path traversal.
- The host runs with the privileges of the user who launched it; there is no
  implicit escalation.
- Channel opens, with the command for exec and pty, are logged. Client sessions
  can be recorded to asciinema with `remolo connect --record`.
- `remolo connect --jump <bastion>` routes through an intermediate host that only
  ever relays ciphertext; the end-to-end guarantee with the target is preserved.

## Threat model

Defended against:

- Passive eavesdropping (E2E encryption).
- Active man-in-the-middle (certificate pinning to the token key).
- Host impersonation (same pinning).
- Replay of the authentication handshake (fresh nonces each time).
- Brute-force of the PSK (rate limiting plus a 256-bit secret).

Out of scope:

- An attacker who legitimately holds the token. The token is the credential; if
  it leaks, rotate it (restart the host, share a new one) and prefer short TTLs
  and `--once`.
- A host already compromised at the OS level.

## Known limits

These are deliberate v0.1 boundaries, not oversights. Know them before you share
a token.

- **A token is your shell, environment included.** The PTY inherits the host
  process environment, so `SSH_AUTH_SOCK`, `AWS_*`, `KUBECONFIG` and anything
  else exported to the host are visible to whoever connects. Start the host from
  a clean environment when that matters, or restrict the session with `--ro` and
  `--cap`.
- **`remolo forward` reaches whatever the host reaches.** A token holder can
  forward to `localhost:6379`, an internal subnet, or anything else routable from
  the host. There is no per-target allow-list yet; use `--cap` to withhold the
  forward channel entirely when that is a concern.
- **The rendezvous client does not require HTTPS.** Pointed at an `http://`
  broker, the session id and candidate endpoints travel in cleartext. That leaks
  where a host is, not the ability to reach it (authentication still needs the
  token), but run your broker behind TLS.
- **Input injection is Linux-only.** On macOS and Windows the desktop channel is
  view-only.

## Reporting

Security issues should be reported privately to the maintainer rather than filed
as public issues.

## A note on the derived session key

The PSK handshake derives a session key in addition to authenticating. Every
remolo channel already runs inside TLS 1.3 (QUIC natively, or explicit TLS on
the TCP/relay/SSH rungs), so that derived key is presently reserved for future
channel-binding and is not used as the sole confidentiality mechanism.
