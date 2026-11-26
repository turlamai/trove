#!/bin/bash
# Trove daemon starter. Fixed paths, no client-controlled input; safe to run
# repeatedly (idempotent). Called by the PHP gateway on demand and by the
# emhttp "started" event.
set -u

BIN=/usr/local/sbin/troved
SOCK=/run/trove/troved.sock
RUNDIR=/run/trove
CFG=/boot/config/plugins/trove/trove.cfg
LOG=/var/log/troved.log

mkdir -p "$RUNDIR"

# Already healthy? (skipped with --restart: install/upgrade must replace
# the running binary — a live daemon keeps its old inode otherwise)
if [ "${1:-}" != "--restart" ] && [ -S "$SOCK" ]; then
  TOKEN=$(cat "$RUNDIR/token" 2>/dev/null || true)
  if [ -n "$TOKEN" ] && curl --unix-socket "$SOCK" -sf -m 3 \
       -H "X-Trove-Token: $TOKEN" http://troved/healthz >/dev/null 2>&1; then
    exit 0
  fi
fi

# Stop a running instance and wait for it to let go of the socket.
pkill -TERM -x troved 2>/dev/null
for _ in $(seq 1 20); do
  pgrep -x troved >/dev/null 2>&1 || break
  sleep 0.3
done
pkill -KILL -x troved 2>/dev/null
rm -f "$SOCK"

if [ ! -x "$BIN" ]; then
  echo "troved binary missing: $BIN" >> "$LOG" 2>/dev/null
  exit 1
fi

nohup setsid "$BIN" \
  --socket "$SOCK" \
  --token-file "$RUNDIR/token" \
  --config "$CFG" \
  >> "$LOG" 2>&1 &

# Wait briefly for the socket to appear.
for _ in $(seq 1 24); do
  [ -S "$SOCK" ] && exit 0
  sleep 0.25
done

echo "troved failed to create $SOCK (see $LOG)" >> "$LOG" 2>/dev/null
exit 1
