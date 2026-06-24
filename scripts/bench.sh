#!/usr/bin/env bash
# Reproducible micro-benchmarks for remolo: connect+exec latency, file transfer
# throughput and an incremental sync no-op pass, all over loopback.
# Numbers are environment-dependent; run this on your own LAN/WAN to fill the
# table in docs/benchmarks.md.
set -euo pipefail
cd "$(dirname "$0")/.."

CGO_ENABLED=0 go build -o remolo ./cmd/remolo

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"; kill "${HOST_PID:-0}" 2>/dev/null || true' EXIT
export XDG_RUNTIME_DIR="$TMP/run"; mkdir -p "$XDG_RUNTIME_DIR"

./remolo host --loopback --ttl 10m --file-root "$TMP" > "$TMP/host.out" 2>&1 &
HOST_PID=$!
sleep 2
TOK="$(grep -oE 'REMOLO1-[A-Z0-9-]+' "$TMP/host.out" | head -1)"

echo "== remolo micro-benchmarks (loopback) =="

# Connect + exec round trip (cold: no mux daemon reuse).
N=10
start=$(date +%s.%N)
for _ in $(seq "$N"); do ./remolo exec "$TOK" -- true >/dev/null 2>&1; done
end=$(date +%s.%N)
printf "connect+exec (cold):   %.0f ms/op over %d ops\n" "$(echo "($end-$start)/$N*1000" | bc -l)" "$N"

# File transfer throughput.
SIZE_MB=100
head -c $((SIZE_MB*1024*1024)) /dev/urandom > "$TMP/payload.bin"
start=$(date +%s.%N)
./remolo put "$TOK" "$TMP/payload.bin" "uploaded.bin" >/dev/null 2>&1
end=$(date +%s.%N)
printf "put %d MB:              %.1f MB/s\n" "$SIZE_MB" "$(echo "$SIZE_MB/($end-$start)" | bc -l)"

start=$(date +%s.%N)
./remolo get "$TOK" "uploaded.bin" "$TMP/downloaded.bin" >/dev/null 2>&1
end=$(date +%s.%N)
printf "get %d MB:              %.1f MB/s\n" "$SIZE_MB" "$(echo "$SIZE_MB/($end-$start)" | bc -l)"

# Incremental sync no-op (second pass should move ~nothing).
mkdir -p "$TMP/tree"; head -c $((10*1024*1024)) /dev/urandom > "$TMP/tree/big.bin"
./remolo sync "$TOK" "$TMP/tree" "synced" >/dev/null 2>&1
start=$(date +%s.%N)
./remolo sync "$TOK" "$TMP/tree" "synced" >/dev/null 2>&1
end=$(date +%s.%N)
printf "sync (unchanged tree): %.0f ms (no-op pass)\n" "$(echo "($end-$start)*1000" | bc -l)"

echo
echo "Tip: compare against 'scp'/'rsync' over your own ssh on the same path."
