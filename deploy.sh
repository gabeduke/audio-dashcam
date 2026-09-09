#!/bin/bash
# Sync to the Pi, build there (PortAudio is cgo), and restart the service.
set -euo pipefail

# Host/user live in an untracked file so this repo carries no personal config.
# Create deploy.local.env with e.g.  DASHCAM_HOST=pi@dashcam.local
[ -f "$(dirname "$0")/deploy.local.env" ] && . "$(dirname "$0")/deploy.local.env"

HOST="${DASHCAM_HOST:?set DASHCAM_HOST (e.g. pi@dashcam.local), or put it in deploy.local.env}"
DEST="${DASHCAM_DEST:-audio-dashcam}"

# Set DASHCAM_NO_AGENT=1 in deploy.local.env on a machine where an ssh agent
# holds the key hostage.
#
# 1Password installs `Host * IdentityAgent ...` in ~/.ssh/config, so every host
# goes through its agent. Listing keys does not need the vault but *signing*
# does, so every deploy fails whenever 1Password is locked -- which reads like
# the agent going stale mid-session. Unsetting SSH_AUTH_SOCK does not help: the
# agent is chosen by ssh_config, not the environment. A throwaway config is the
# only thing that overrides it, and it has to be handed to rsync too.
SSH=(ssh)
if [ "${DASHCAM_NO_AGENT:-}" = "1" ]; then
  KEY="${DASHCAM_SSH_KEY:-$HOME/.ssh/id_rsa}"
  CFG="$(mktemp)"
  trap 'rm -f "$CFG"' EXIT
  # Note the space form: scp and rsync reject `IdentityAgent=none`.
  printf 'Host *\n  IdentityAgent none\n  IdentitiesOnly yes\n  IdentityFile %s\n' "$KEY" > "$CFG"
  SSH=(ssh -F "$CFG")
  echo "[*] bypassing the ssh agent, using $KEY"
fi

# Static-only fast path. main.go serves the UI with
# http.FileServer(http.Dir(staticDir())), so HTML/CSS/JS changes are picked up
# from disk on the next request -- no rebuild, no restart. That turns the
# layout iteration loop from about a minute into about a second.
if [ "${1:-}" = "--static" ]; then
  echo "[*] syncing v2-go/static only to $HOST:~/$DEST"
  rsync -az --delete -e "${SSH[*]}" ./v2-go/static/ "$HOST:~/$DEST/v2-go/static/"
  echo "[*] done (no rebuild, no restart)"
  exit 0
fi

echo "[*] syncing to $HOST:~/$DEST"
rsync -az --delete -e "${SSH[*]}" \
  --exclude 'jam_saves' \
  --exclude '.venv' \
  --exclude '.git' \
  --exclude '*.log' \
  --exclude 'v2-go/v2-go-bin' \
  --exclude 'dashcam.env' \
  --exclude 'deploy.local.env' \
  ./ "$HOST:~/$DEST/"

echo "[*] building on the Pi"
"${SSH[@]}" "$HOST" "cd ~/$DEST/v2-go && go build -o v2-go-bin ."

echo "[*] restarting"
"${SSH[@]}" "$HOST" "systemctl --user restart audio-dashcam.service"
sleep 3
"${SSH[@]}" "$HOST" "systemctl --user is-active audio-dashcam.service && \
  curl -fsS http://127.0.0.1:5000/api/status | head -c 200 && echo"

echo "[*] done"
