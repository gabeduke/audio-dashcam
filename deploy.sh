#!/bin/bash
# Sync to the Pi, build there (PortAudio is cgo), and restart the service.
set -euo pipefail

# Host/user live in an untracked file so this repo carries no personal config.
# Create deploy.local.env with e.g.  DASHCAM_HOST=pi@dashcam.local
[ -f "$(dirname "$0")/deploy.local.env" ] && . "$(dirname "$0")/deploy.local.env"

HOST="${DASHCAM_HOST:?set DASHCAM_HOST (e.g. pi@dashcam.local), or put it in deploy.local.env}"
DEST="${DASHCAM_DEST:-audio-dashcam}"

echo "[*] syncing to $HOST:~/$DEST"
rsync -az --delete \
  --exclude 'jam_saves' \
  --exclude '.venv' \
  --exclude '.git' \
  --exclude '*.log' \
  --exclude 'v2-go/v2-go-bin' \
  --exclude 'dashcam.env' \
  --exclude 'deploy.local.env' \
  ./ "$HOST:~/$DEST/"

echo "[*] building on the Pi"
ssh "$HOST" "cd ~/$DEST/v2-go && go build -o v2-go-bin ."

echo "[*] restarting"
ssh "$HOST" "systemctl --user restart audio-dashcam.service"
sleep 3
ssh "$HOST" "systemctl --user is-active audio-dashcam.service && \
  curl -fsS http://127.0.0.1:5000/api/status | head -c 200 && echo"

echo "[*] done"
