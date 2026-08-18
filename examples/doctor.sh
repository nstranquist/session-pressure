#!/bin/sh
set -eu
root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
bin="$root/bin/session-pressure"
if [ ! -x "$bin" ]; then
  make -C "$root" build
fi
exec "$bin" --json doctor
