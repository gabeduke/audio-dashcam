#!/usr/bin/env python3
"""Report the peak level seen on each input channel of a running dashcam.

Teenage Engineering documents the EP-136 as an 8-in/4-out interface whose eight
inputs are four stereo record pairs -- channel one, channel two, aux and the
main output -- but does not publish which USB channel number each pair lands
on. This finds out empirically: play audio and watch which pair moves.

Peaks are held across the whole run rather than sampled instantaneously,
because a status poll only ever reports the newest 10ms bin and quiet moments
in real material would otherwise read as a dead channel.

Usage:
    python3 scripts/channel-probe.py "$DASHCAM_ADDR" --seconds 20
"""

import argparse
import json
import os
import sys
import time
import urllib.error
import urllib.request


def fetch(url, timeout=3.0):
    with urllib.request.urlopen(url, timeout=timeout) as resp:
        return json.load(resp)


def bar(db, floor, width=32):
    """Draw a level bar. dB is logarithmic, so map floor..0 onto the width."""
    if db <= floor:
        return "." * width
    frac = min(1.0, (db - floor) / -floor)
    filled = int(round(frac * width))
    return "#" * filled + "." * (width - filled)


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("host", nargs="?",
                    default=os.environ.get("DASHCAM_ADDR", "dashcam.local"),
                    help="dashcam host or host:port (default: $DASHCAM_ADDR)")
    ap.add_argument("--seconds", type=float, default=20.0,
                    help="how long to sample for (default: 20)")
    ap.add_argument("--interval", type=float, default=0.1,
                    help="seconds between polls (default: 0.1)")
    args = ap.parse_args()

    host = args.host if "://" in args.host else "http://" + args.host
    url = host.rstrip("/") + "/api/status"

    try:
        first = fetch(url)
    except (urllib.error.URLError, OSError) as e:
        sys.exit("cannot reach %s: %s" % (url, e))

    if not first.get("capture_healthy"):
        sys.exit("capture is not healthy (%s) -- power the interface on and "
                 "wait for the log to say 'capture live', then re-run"
                 % (first.get("last_error") or "no error reported"))

    floor = float(first.get("floor_db", -60.0))
    channels = int(first.get("channels", 8))
    peaks = [floor] * channels
    samples = 0
    errors = 0

    print("sampling %s for %.0fs -- play something now" % (url, args.seconds))
    deadline = time.time() + args.seconds
    while time.time() < deadline:
        try:
            s = fetch(url)
        except (urllib.error.URLError, OSError):
            errors += 1
            time.sleep(args.interval)
            continue
        rms = s.get("channel_rms") or []
        for i in range(min(channels, len(rms))):
            v = float(rms[i])
            if v > peaks[i]:
                peaks[i] = v
        samples += 1
        time.sleep(args.interval)

    if samples == 0:
        sys.exit("no successful samples (%d errors)" % errors)

    save = first.get("save_channels") or []
    print("\n%d samples, %d errors, floor %.0f dBFS" % (samples, errors, floor))
    print("device: %s\n" % (first.get("device") or "unknown"))

    print("  ch   peak dBFS   %-32s" % "level")
    for i, db in enumerate(peaks):
        mark = "  <- saved" if (i + 1) in save else ""
        shown = "  -inf" if db <= floor else "%6.1f" % db
        print("  %2d     %s   %s%s" % (i + 1, shown, bar(db, floor), mark))

    print("\npairs:")
    for lo in range(0, channels, 2):
        pair_peak = max(peaks[lo], peaks[lo + 1]) if lo + 1 < channels else peaks[lo]
        state = "SILENT" if pair_peak <= floor else "active (%.1f dBFS)" % pair_peak
        print("  %d/%d  %s" % (lo + 1, lo + 2, state))

    print("\nRecord which pairs are active, then re-run with the source moved "
          "to the other physical input. The pair active in BOTH runs is the "
          "main mix -- that is the one SAVE_CHANNELS should point at.")


if __name__ == "__main__":
    main()
