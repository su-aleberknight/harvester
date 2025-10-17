#!/bin/sh
set -e

BRIDGE="${BRIDGE:-br-dummy}"
SLEEP="${SLEEP:-30}"

# Ensure required utilities exist
if ! command -v ip >/dev/null 2>&1; then
  echo "ip command not found; exiting"
  exit 1
fi

while true; do
  if ip link show "$BRIDGE" >/dev/null 2>&1; then
    ip link set "$BRIDGE" up || true
  else
    ip link add name "$BRIDGE" type bridge || true
    ip link set "$BRIDGE" up || true
  fi
  sleep "$SLEEP"
done