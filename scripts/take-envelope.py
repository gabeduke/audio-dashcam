#!/usr/bin/env python3
"""Extract a take's amplitude envelope as compact base64, for the buffer ribbon.

The ribbon design needs to be judged against real audio, not synthetic noise,
and a 15-minute take is ~346 MB of WAV that has no business being copied to a
laptop. This reduces one to a few tens of kilobytes on the Pi and prints the
base64 to stdout.

The encoding deliberately matches what the UI already does. `meter.js` plots on
a dB scale:

    const db = 20 * Math.log10(a);
    const t  = (db - FLOOR_DB) / -FLOOR_DB;   // FLOOR_DB = -60

so each byte here is that same `t` scaled to 0..255. A consumer divides by 255
and has exactly the value the live visualiser would have drawn. Storing linear
amplitude instead would make everything below about -20 dBFS look like silence.

Peak is taken per bin rather than RMS, because the question the ribbon answers
is "was something happening here", and a peak survives downsampling where a
mean washes out.

Usage:
    python3 scripts/take-envelope.py ~/audio-dashcam/jam_saves/jam_*.wav
    python3 scripts/take-envelope.py TAKE.wav --bin-ms 20 --out /tmp/env.b64
"""

import argparse
import array
import base64
import math
import sys
import wave

FLOOR_DB = -60.0

# Subsample within each bin. At 48 kHz a 20 ms bin holds 960 frames, so 1-in-8
# still leaves 120 samples to take a peak from -- ample, and it turns a slow
# Python loop into a C-speed slice.
STRIDE = 8


def main():
    p = argparse.ArgumentParser(description=__doc__,
                                formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("wav")
    p.add_argument("--bin-ms", type=int, default=20,
                   help="envelope resolution in milliseconds (default 20)")
    p.add_argument("--out", help="write base64 here instead of stdout")
    args = p.parse_args()

    w = wave.open(args.wav, "rb")
    ch, sw, sr, n = w.getnchannels(), w.getsampwidth(), w.getframerate(), w.getnframes()
    if sw != 4:
        sys.exit(f"expected 32-bit samples, got {sw * 8}-bit")
    if array.array("i").itemsize != 4:
        sys.exit("this platform's 'i' is not 4 bytes")

    fpb = max(1, sr * args.bin_ms // 1000)
    full = 2147483648.0
    out = bytearray()

    # Read in ~10-second blocks so a 15-minute take never lands in memory whole.
    block = fpb * (1000 // args.bin_ms) * 10
    carry = array.array("i")
    remaining = n
    while remaining > 0:
        want = min(block, remaining)
        buf = array.array("i")
        buf.frombytes(w.readframes(want))
        remaining -= want
        carry.extend(buf)

        usable = (len(carry) // (fpb * ch)) * (fpb * ch)
        for s in range(0, usable, fpb * ch):
            seg = carry[s:s + fpb * ch:STRIDE]
            if not seg:
                continue
            mx = max(max(seg), -min(seg))
            frac = mx / full
            if frac <= 0.0:
                out.append(0)
                continue
            db = 20 * math.log10(frac)
            t = (db - FLOOR_DB) / -FLOOR_DB
            out.append(max(0, min(255, round(t * 255))))
        del carry[:usable]

    b64 = base64.b64encode(bytes(out)).decode()
    loud = sum(1 for b in out if b > 60)  # above about -46 dBFS
    print(f"# {args.wav}", file=sys.stderr)
    print(f"# {ch}ch {sr}Hz {n / sr:.1f}s -> {len(out)} bins at {args.bin_ms}ms "
          f"({len(b64) / 1024:.1f} KB base64)", file=sys.stderr)
    print(f"# {loud} bins above -46 dBFS ({100.0 * loud / max(1, len(out)):.1f}% "
          f"of the take has signal)", file=sys.stderr)

    if args.out:
        open(args.out, "w").write(b64)
        print(f"# written to {args.out}", file=sys.stderr)
    else:
        print(b64)


if __name__ == "__main__":
    main()
