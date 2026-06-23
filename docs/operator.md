# remolo operator guide

This guide covers running remolo across real networks: same machine, same LAN,
different subnets, and across the internet behind NAT.

## The mental model

1. The host enumerates every address it has and packs them into the token,
   along with its public key and a one-time secret.
2. The client decodes the token and races every reachable path in parallel,
   keeping the first that connects. It logs each attempt, so you can always see
   which rung won.
3. The first successful connection starts a local mux daemon. Further commands
   with the same token reuse that connection (no second handshake).

## Scenarios

### Same machine (testing)

```
remolo host --loopback
remolo connect <token>
```

`--loopback` adds `127.0.0.1` to the token so a client on the same box connects.

### Same LAN, possibly different subnets

```
remolo host
remolo connect <token>
```

The token already carries every interface address, so a client on another
subnet (if routing allows) connects directly. Add `--mdns` on both sides to also
discover the host on the local segment:

```
remolo host --mdns
remolo connect --mdns <token>
```

### Across the internet, behind NAT

Two building blocks, both self-hostable and both blind to your traffic:

- A rendezvous broker, so the client can find the host by session id even after
  the host's public address changes.
- A relay, used only when a direct path and hole-punching both fail (for example
  on symmetric NAT). The relay forwards ciphertext; it never sees plaintext.

Run them anywhere with a public address:

```
remolo-rendezvous -addr :8787
remolo-relay -addr :8788
```

Host:

```
remolo host \
  --stun stun.l.google.com:19302 \
  --rendezvous http://broker.example:8787 \
  --relay broker.example:8788
```

`--stun` adds a public reflexive candidate to the token (works on full-cone and
port-preserving NAT). `--rendezvous` publishes the host's candidates to the
broker. `--relay` keeps a standing relay registration as the guaranteed
fallback.

Client:

```
remolo connect --rendezvous http://broker.example:8787 --relay broker.example:8788 <token>
```

The client tries, in order: direct (QUIC then TCP) on every token candidate,
mDNS, rendezvous-resolved candidates, then the relay. The winning rung is
printed.

### Locked-down environments (SSH only)

If the host already runs sshd and you cannot open anything else, tunnel remolo's
TCP endpoint through it:

```
remolo connect \
  --ssh-addr host.example:22 \
  --ssh-user alice \
  --ssh-key ~/.ssh/id_ed25519 \
  --ssh-target 127.0.0.1:<remolo-port> \
  <token>
```

### Last resort: type an address

If every automatic rung fails, `remolo connect` asks for the host's IP and
retries. This is the only time remolo asks you for an address.

## Multi-session

Once connected, open more sessions cheaply from any terminal with the same
token; they reuse the existing connection:

```
remolo connect <token>     # shell
remolo desktop <token>     # frames, reuses the connection
remolo exec <token> -- ls  # one-shot, reuses the connection
remolo sessions            # list active reusable connections
```

## Other commands

```
remolo info <token>                 # host system info (RPC)
remolo put  <token> <local> <remote>  # upload (resumable)
remolo get  <token> <remote> <local>  # download (resumable)
remolo rest <token> --addr 127.0.0.1:8790  # local REST proxy: curl /sysinfo, POST /exec
remolo token <token>                # inspect a token without connecting
```

## Useful flags

- `remolo host --ttl 1h` shorten the token's validity.
- `remolo host --once` single-use: admit one authenticated peer, then refuse.
- `remolo host --file-root /srv/share` restrict file transfer to a directory.
- `remolo host --bind 0.0.0.0:41000` pin the port (firewall-friendly).
