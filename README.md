<div align="center">
  <h1>remolo</h1>
  <p>Get a shell, your files and the screen of another machine by pasting one token. Works behind NAT.</p>
</div>

---

On the machine you want to reach:

```
$ remolo host
remolo host active, listening on UDP port 49213

Session token:
   REMOLO1-MR0G20-JR430T-XCD75X-1YFVSH-...-0EBP8N-KC

Share this token. It expires in 24h0m0s. (use --ttl to change)
```

On yours:

```
$ remolo connect REMOLO1-MR0G20-...-KC
remolo: resolving host... [direct 10.0.0.42:49213 ✓ RTT 0.4ms] connected via direct 10.0.0.42:49213
host:~$ _
```

That is the whole setup. No account, no VPN, no port forwarding, no config file,
no IP to look up, nothing to install on a server.

## Install

```
curl -fsSL https://raw.githubusercontent.com/mirkobrombin/remolo/master/install.sh | sh
```

Linux and macOS, amd64 and arm64. On Linux this also installs a `systemd --user`
service so the machine stays reachable after a reboot; set `REMOLO_NO_SERVICE=1`
if you just want the binary. On Windows, grab the `.exe` from
[releases](https://github.com/mirkobrombin/remolo/releases). Or
`go install github.com/mirkobrombin/remolo/cmd/remolo@latest`.

There is no server component. Both sides are the same binary.

## The same token does more than a shell

- `remolo connect <token>` - interactive shell; `--resume` survives network changes
- `remolo exec <token> -- <cmd>` - one-shot command, with real exit codes and stderr
- `remolo put` / `remolo get` - file transfer, resumable and checksummed
- `remolo sync <token> <local> <remote>` - rsync-style sync, moves only what changed
- `remolo mount <token> <dir>` - the remote filesystem as a local folder
- `remolo forward <token> -L 8080:localhost:80` - port forwarding, and SOCKS5 with `-D`
- `remolo desktop <token>` - the remote screen in your browser, mouse and keyboard included
- `remolo webterm <token>` - a terminal in your browser

Open as many as you like at once. The second command reuses the connection the
first one made, so it starts instantly.

Tired of pasting tokens? `remolo enroll <token> --as work` registers your key on
that host, and from then on `remolo connect work` is enough.

Running a fleet? `remolo group set web a b c`, then `remolo exec @web -- uptime`
fans out in parallel.

## When the direct path is blocked

The token carries every address the host has, so remolo tries them all at once
and keeps the first that answers. If none do, it works its way down: mDNS on the
local network, then a rendezvous broker, then a relay, then a tunnel through an
existing sshd. It always prints which rung won.

The two rungs that need a public address are yours to host: `remolo-rendezvous`
and `remolo-relay` ship as separate binaries, and neither can read your traffic.
The relay only ever moves ciphertext.

## Security

Sessions are end-to-end encrypted with TLS 1.3. The client pins the host's key
from the token, so nobody can impersonate the host even on a hostile network,
and the host verifies the client really holds the token's secret.

**Treat the token like a password.** Whoever has it gets a shell as you, with
your environment. Tokens expire (24h by default, `--ttl` to shorten) and `--once`
makes one single-use. You can also hand out a narrower one: `--ro` for read-only,
`--cap` to allow only certain channels, `--cmd` to allow only certain commands,
`--file-root` to confine it to one directory. `--approve` asks you before letting
anyone in.

remolo is for machines you own or have explicit consent to control.
[docs/security.md](docs/security.md) has the threat model and the known limits.

## Docs

- [Operator guide](docs/operator.md) - same LAN, across subnets, across the internet behind NAT
- [Security model](docs/security.md) - what protects a session, and what does not
- [Architecture](docs/architecture.md) - how it is put together
- [Benchmarks](docs/benchmarks.md) - a harness to measure it on your own network

## Build

```
make build   # single binary, no cgo
make test
```

Go 1.25+. No cgo, no runtime dependencies.

## License

MIT. See [LICENSE](LICENSE).
