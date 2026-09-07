#!/bin/bash
# Launcher for the audio dashcam service.
#
# The blue/green Python/Go toggle is gone: the Go implementation is the only
# one now. The Python original was removed; see git history at 0219975.
set -euo pipefail

APP_DIR="${APP_DIR:-$HOME/audio-dashcam}"

# Optional tuning knobs live here so they survive redeploys. See README.
if [ -f "$APP_DIR/dashcam.env" ]; then
    set -a
    # shellcheck disable=SC1091
    . "$APP_DIR/dashcam.env"
    set +a
fi

exec "$APP_DIR/v2-go/v2-go-bin"
