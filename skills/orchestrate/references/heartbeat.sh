#!/usr/bin/env bash
# Compatibility entrypoint for existing run directories. Periodic model
# heartbeats were replaced by the deterministic change-only supervisor.
set -euo pipefail
D="$(cd "$(dirname "$0")" && pwd -P)"
[ -x "$D/supervisor.sh" ] || {
  echo "heartbeat.sh: missing $D/supervisor.sh" >&2
  exit 2
}
exec "$D/supervisor.sh" run "$D"
