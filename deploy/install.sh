#!/usr/bin/env bash
# Install Hindsight from an unpacked release onto a Raspberry Pi.
#
#   tar xzf hindsight_<version>_linux_arm64.tar.gz
#   cd hindsight_<version>_linux_arm64
#   ./install.sh
set -euo pipefail

SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Hardcoded, not overridable: the unit file hardcodes %h/hindsight and the
# binary's own default OutputDir is $HOME/hindsight/jam_saves, so a knob here
# that didn't also thread through both of those would just install to one
# place and run from another.
ROOT="$HOME/hindsight"
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
[ -d "$SRC/web/static" ] || die "no web/static next to this script; run it from inside the unpacked release"
[ -f "$SRC/deploy/hindsight.service" ] || die "no deploy/hindsight.service next to this script; run it from inside the unpacked release"
[ -f "$SRC/deploy/hindsight.env.example" ] || die "no deploy/hindsight.env.example next to this script; run it from inside the unpacked release"

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
  # `|| reply=n` so a closed/non-interactive stdin (EOF) degrades to "no"
  # instead of letting `read`'s non-zero exit take set -e down with it.
  read -r -p "    stop and disable it, and copy its takes across? [y/N] " reply || reply=n
  if [ "${reply:-n}" = "y" ] || [ "${reply:-n}" = "Y" ]; then
    systemctl --user disable --now audio-dashcam.service || true
    if [ -d "$HOME/audio-dashcam/jam_saves" ]; then
      mkdir -p "$ROOT/jam_saves"
      # A copy, not a move, and into an existing directory's contents (the
      # trailing "/." on the source) rather than the directory itself -- a
      # plain `mv src dest` with `dest` already existing would nest the takes
      # one level too deep, where ListTakes' non-recursive os.ReadDir can't
      # see them. `-a` preserves mtimes (ListTakes sorts by them); `-n` never
      # clobbers a file that already landed from a previous run; leaving the
      # originals in place means an interrupted cross-filesystem copy still
      # leaves the user with one intact set of takes.
      cp -an "$HOME/audio-dashcam/jam_saves/." "$ROOT/jam_saves/" \
        || die "could not copy takes; your originals are untouched in $HOME/audio-dashcam/jam_saves"
      say "copied takes to $ROOT/jam_saves — originals left in $HOME/audio-dashcam/jam_saves, delete them once you are happy"
    fi
  elif systemctl --user is-active --quiet audio-dashcam.service; then
    die "audio-dashcam.service is still running and holds the USB audio interface; hindsight would fail to open it and crash-loop fighting it. Stop it first: systemctl --user disable --now audio-dashcam.service (or re-run this installer and accept the migration prompt)"
  fi
fi

# --- install ----------------------------------------------------------------
say "installing to $ROOT"
mkdir -p "$ROOT/bin" "$ROOT/web" "$ROOT/jam_saves"
install -m 755 "$SRC/bin/hindsight" "$ROOT/bin/hindsight"
# Copy to a staging name first and only remove the old UI once the copy has
# fully succeeded -- swapping instead of rm-then-cp means a mid-copy failure
# (disk full, bad archive) never leaves the user without a working web/static.
rm -rf "$ROOT/web/static.new"
cp -R "$SRC/web/static" "$ROOT/web/static.new"
rm -rf "$ROOT/web/static"
mv "$ROOT/web/static.new" "$ROOT/web/static"

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
systemctl --user enable hindsight.service
# `enable --now` is a no-op start on a unit that's already running -- on a
# re-run (an upgrade) that would leave the old binary serving the new
# web/static we just swapped in above. `restart` always picks up the binary
# that was just installed.
systemctl --user restart hindsight.service

# Without lingering the service dies at logout, which for a headless Pi means
# it dies as soon as you close the SSH session that started it. A stale sudo
# cache or a user outside sudoers makes this fail; that shouldn't take down
# an otherwise-successful install, so warn and keep going rather than dying.
say "enabling lingering so it survives logout"
sudo loginctl enable-linger "$(id -un)" \
  || say "warning: could not enable lingering — the service will stop at logout. Run: sudo loginctl enable-linger $(id -un)"

say "waiting for it to come up"
sleep 3
if curl -fsS http://127.0.0.1:5000/api/status >/dev/null 2>&1; then
  say "running: http://$(hostname).local:5000"
  say "next: set SAVE_CHANNELS in $ROOT/hindsight.env — the default assumes an EP-136"
elif journalctl --user -u hindsight.service -n 20 --no-pager 2>/dev/null | grep -q 'capture:'; then
  # main.go's only fatal error from opening the audio device is logged as
  # "capture: <err>". Restart=always means it's already retrying on its own;
  # this isn't the installer failing, it's a Pi with nothing plugged in yet.
  echo
  say "installed, but it can't open an audio interface yet — probably nothing is plugged in."
  say "hindsight will keep retrying on its own (Restart=always); plug the interface in and it should come up within a few seconds."
  say "check with: systemctl --user status hindsight.service"
else
  echo
  echo "service did not come up. The log:" >&2
  journalctl --user -u hindsight.service -n 30 --no-pager >&2
  exit 1
fi
