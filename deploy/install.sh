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
command -v curl >/dev/null || die "curl is required (used to verify hindsight started)"
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

# --- can this binary actually run here? -------------------------------------
# The whole reason release.yml builds inside debian:bookworm is that Raspberry
# Pi OS Bookworm ships glibc 2.36 while the arm64 runner links 2.39. If that
# ever regresses, the only symptom further down is "service did not come up"
# plus 30 journal lines, which points at everything except the real cause. Run
# the binary once and say so plainly instead. release.yml runs the same check
# on the build side.
#
# This has to come after the apt-get above -- the binary is dynamically linked
# against libportaudio2 -- and before the migration below, so a failure here
# does not leave the machine with the old service already stopped.
"$SRC/bin/hindsight" --version >/dev/null \
  || die "the release binary does not run on this machine (glibc mismatch?)"

# --- migrate an existing audio-dashcam install ------------------------------
OLD_ROOT="$HOME/audio-dashcam"
if systemctl --user list-unit-files 2>/dev/null | grep -q '^audio-dashcam\.service'; then
  say "found an existing audio-dashcam.service"
  # `|| reply=n` so a closed/non-interactive stdin (EOF) degrades to "no"
  # instead of letting `read`'s non-zero exit take set -e down with it.
  read -r -p "    stop and disable it, and copy its takes across? [y/N] " reply || reply=n
  if [ "${reply:-n}" = "y" ] || [ "${reply:-n}" = "Y" ]; then
    systemctl --user disable --now audio-dashcam.service || true
    if [ -d "$OLD_ROOT/jam_saves" ]; then
      mkdir -p "$ROOT/jam_saves"
      # Not `cp -n`: coreutils 9.2 made it exit nonzero (with a diagnostic)
      # when it skips an existing destination instead of silently succeeding,
      # and its replacement, `--update=none`, only exists from 9.3 -- so
      # there's no single flag that means "merge, don't clobber" across both
      # Raspberry Pi OS Bookworm (9.1) and Trixie (9.5+). Per-file is
      # explicit, portable across both, and -- unlike a directory-level
      # `mv`/`cp` into an already-existing $ROOT/jam_saves -- cannot nest.
      copied=0 kept=0
      while IFS= read -r -d '' f; do
        if [ -e "$ROOT/jam_saves/$(basename "$f")" ]; then
          kept=$((kept + 1))
        elif cp -a "$f" "$ROOT/jam_saves/"; then
          # -a preserves mtime: ListTakes derives each take's Created from it.
          copied=$((copied + 1))
        else
          die "could not copy $(basename "$f"); your originals are untouched in $OLD_ROOT/jam_saves"
        fi
      done < <(find "$OLD_ROOT/jam_saves" -maxdepth 1 -type f -print0)
      say "copied $copied take file(s), $kept already present — originals left in $OLD_ROOT/jam_saves, delete them once you are happy"
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
# Poll for ~15s instead of one shot after a fixed sleep: Type=simple means
# systemd considers the unit started the instant the process forks, well
# before it's actually listening, and a slow Pi needs more than 3s for that.
body=""
tries=0
until body="$(curl -fsS http://127.0.0.1:5000/api/status 2>/dev/null)"; do
  tries=$((tries + 1))
  if [ "$tries" -ge 8 ]; then
    body=""
    break
  fi
  sleep 2
done

if [ -z "$body" ]; then
  echo
  echo "service did not come up. The log:" >&2
  journalctl --user -u hindsight.service -n 30 --no-pager >&2
  exit 1
fi

# capture_healthy/last_error are internal/api/api.go statusResponse fields
# (json tags "capture_healthy" / "last_error", neither omitempty); Go's encoder
# writes them with no space after the colon, so both literal matches are exact
# today.
#
# The two degrade differently if a field is ever renamed or given omitempty.
# capture_healthy just stops matching and falls into the "not confirmed
# recording" branch below -- a false "nothing is recording" rather than a false
# "success", which is the safer direction to be wrong in. last_error is inside
# a command substitution, so an absent field makes grep exit 1, pipefail
# propagates it, and set -e would kill the installer outright -- silently, at
# the exact moment it has something useful to say. Hence the `|| true`: a
# missing field costs the reason, printed as "(none reported)", not the
# message.
if printf '%s' "$body" | grep -q '"capture_healthy":true'; then
  say "running: http://$(hostname).local:5000"
  say "next: set SAVE_CHANNELS in $ROOT/hindsight.env — the default assumes an EP-136"
else
  last_error="$(printf '%s' "$body" | grep -o '"last_error":"[^"]*"' | sed -e 's/^"last_error":"//' -e 's/"$//' || true)"
  say "running: http://$(hostname).local:5000 — but not recording"
  say "capture error: ${last_error:-(none reported)}"
  say "hindsight will keep retrying on its own; plug the interface in (or fix the error above) — check with: systemctl --user status hindsight.service"
fi
