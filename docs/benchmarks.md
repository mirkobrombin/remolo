# Benchmarks

remolo aims to match SSH for everyday use and beat it where it counts (NAT
traversal, multi-session reuse, incremental sync, FUSE mount, roaming). There
are no numbers on this page on purpose: throughput and latency depend on your
link far more than on either tool, so the useful thing to ship is the harness.
Run it on your own network.

## Run it

```
./scripts/bench.sh
```

It measures, on loopback (worst case for showing off network wins, best case for
isolating overhead):

- connect + exec round trip (cold, no mux reuse)
- file transfer throughput (put and get, 100 MB)
- incremental sync no-op pass (unchanged tree)

## What to compare against

On a real link (LAN, WAN, behind NAT), compare the same operations to your SSH
toolchain:

| Operation | remolo | ssh / scp / rsync |
|---|---|---|
| First connect (handshake) | `remolo connect` | `ssh host` |
| Subsequent command (warm) | reuses the mux daemon | new ssh, or ControlMaster |
| Copy a large file | `remolo put` | `scp` |
| Re-sync a tree after 1 edit | `remolo sync` | `rsync -a` |
| Reach a host behind NAT | works via the ladder | typically fails without a jump host |
| Survive a network change | (see roaming) | drops the session |

## Reading the results

- These are micro-benchmarks. RTT, loss and NAT topology dominate anything you
  measure on a real link.
- remolo's advantage is least about raw loopback throughput and most about
  connecting at all behind NAT, reusing one connection for many sessions, and
  moving only the bytes that changed on re-sync.
