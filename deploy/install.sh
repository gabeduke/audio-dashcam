#!/usr/bin/env bash
# Install Hindsight from an unpacked release onto a Raspberry Pi.
#
#   tar xzf hindsight_<version>_linux_arm64.tar.gz
#   cd hindsight_<version>_linux_arm64
#   ./install.sh
set -euo pipefail

SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="${HINDSIGHT_ROOT:-$HOME/hindsight}"
UNIT_DIR="$HOME/.config/systemd/user"

die() { echo "error: $*" >&2; exit 1; }
say() { echo "[*] $*"; }

# --- checks -----------------------------------------------------------------
[ "$(uname -s)" = "Linux" ] || die "this installer targets Linux; found $(uname -s)"
if [ "$(uname -m)" != "aarch64" ]; then
  die "the release binary is arm64; this machine is $(uname -m). Build from source instead: go build -o bin/hindsight ./cmd/hindsight"
fi
command -v systemctl >/dev/null || die "systemd is required"
systemctl --user show-environment >/dev/null 2>&1 \
  || die "no systemd user session. Log in over SSH as the user that will run this, not via sudo."
[ -x "$SRC/bin/hindsight" ] || die "no bin/hindsight next to this script; run it from inside the unpacked release"

# --- runtime dependencies ---------------------------------------------------
# The binary is prebuilt, so only the runtime libraries are needed -- not
# portaudio19-dev. ffmpeg builds the mp3 previews the UI streams.
say "installing runtime dependencies"
sudo apt-get update -qq \
  || die "apt-get update failed; check network/apt sources and re-run"
sudo apt-get install -y libportaudio2 libasound2 ffmpeg \
  || die "apt-get install failed; install libportaudio2 libasound2 ffmpeg manually and re-run"

# --- migrate an existing audio-dashcam install ------------------------------
if systemctl --user list-unit-files 2>/dev/null | grep -q '^audio-dashcam\.service'; then
  say "found an existing audio-dashcam.service"
  read -r -p "    stop and disable it, and move its takes across? [y/N] " reply
  if [ "${reply:-n}" = "y" ] || [ "${reply:-n}" = "Y" ]; then
    systemctl --user disable --now audio-dashcam.service || true
    if [ -d "$HOME/audio-dashcam/jam_saves" ]; then
      mkdir -p "$ROOT"
      # -n so an interrupted run never overwrites a take that already moved.
      mv -n "$HOME/audio-dashcam/jam_saves" "$ROOT/jam_saves"
      say "moved takes to $ROOT/jam_saves"
    fi
  fi
fi

# --- install ----------------------------------------------------------------
say "installing to $ROOT"
mkdir -p "$ROOT/bin" "$ROOT/web" "$ROOT/jam_saves"
install -m 755 "$SRC/bin/hindsight" "$ROOT/bin/hindsight"
rm -rf "$ROOT/web/static"
cp -R "$SRC/web/static" "$ROOT/web/static"

if [ -f "$ROOT/hindsight.env" ]; then
  say "keeping your existing hindsight.env"
else
  cp "$SRC/deploy/hindsight.env.example" "$ROOT/hindsight.env"
  say "wrote $ROOT/hindsight.env from the example — read it before a real session"
fi

# --- service ----------------------------------------------------------------
say "installing the user service"
mkdir -p "$UNIT_DIR"
install -m 644 "$SRC/deploy/hindsight.service" "$UNIT_DIR/hindsight.service"
systemctl --user daemon-reload
systemctl --user enable --now hindsight.service

# Without lingering the service dies at logout, which for a headless Pi means
# it dies as soon as you close the SSH session that started it.
say "enabling lingering so it survives logout"
sudo loginctl enable-linger "$USER"

sleep 3
if systemctl --user is-active --quiet hindsight.service; then
  say "running: http://$(hostname):5000"
  say "next: set SAVE_CHANNELS in $ROOT/hindsight.env — the default assumes an EP-136"
else
  echo
  echo "service did not come up. The log:" >&2
  journalctl --user -u hindsight.service -n 30 --no-pager >&2
  exit 1
fi
