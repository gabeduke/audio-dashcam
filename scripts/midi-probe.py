#!/usr/bin/env python3
"""Find out what the EP-136 actually sends over MIDI, and when.

Teenage Engineering shipped a firmware update that reportedly emits clock over
MIDI. Before any of it can be designed against, three questions need real
answers, because two of them can invalidate the feature outright:

  1. Does a MIDI port appear at all under the new firmware?
  2. Does clock flow when the sequencer is STOPPED? The dashcam exists to catch
     unplanned playing. If clock only runs while the sequencer runs, then tempo
     is absent in exactly the case the product is for, and bar-based capture
     lengths have nothing to resolve against.
  3. Is there any time signature anywhere? MIDI clock is 24 pulses per quarter
     note and song-position counts 16th notes; neither carries a meter. Turning
     either into BARS assumes 4/4 unless TE sends something extra -- most
     plausibly as SysEx, which this logs verbatim.

Run it ON THE PI, with the EP-136 plugged in. It walks three phases and prints
a verdict for each question. Nothing is written and nothing is changed; the
dashcam service can keep running, because MIDI is a separate USB interface from
the audio endpoint it holds open.

Usage:
    ssh "$DASHCAM_HOST"
    python3 ~/audio-dashcam/scripts/midi-probe.py

    # or, without deploying, straight from a checkout on your laptop:
    ssh "$DASHCAM_HOST" 'cat > /tmp/midi-probe.py' < scripts/midi-probe.py
    ssh -t "$DASHCAM_HOST" 'python3 /tmp/midi-probe.py'

Options:
    --match EP-136   substring matched against the amidi port list
    --port hw:1,0,0  skip discovery and use this port
    --seconds 12     how long each phase runs
"""

import argparse
import collections
import os
import re
import select
import shutil
import subprocess
import sys
import time

# MIDI System Realtime and System Common status bytes. Realtime messages are
# single bytes and may appear anywhere, even interleaved inside another
# message's data, which is why the parser below checks for them first.
CLOCK = 0xF8
START = 0xFA
CONTINUE = 0xFB
STOP = 0xFC
ACTIVE_SENSE = 0xFE
RESET = 0xFF
SPP = 0xF2  # Song Position Pointer, 2 data bytes, 14-bit, counts 16th notes
MTC_QF = 0xF1  # MIDI Time Code quarter frame, 1 data byte
SONG_SELECT = 0xF3  # 1 data byte
SYSEX_START = 0xF0
SYSEX_END = 0xF7

CLOCKS_PER_QUARTER = 24

NAMES = {
    CLOCK: "clock",
    START: "start",
    CONTINUE: "continue",
    STOP: "stop",
    ACTIVE_SENSE: "active-sensing",
    RESET: "reset",
}


def find_port(match):
    """Pick the first amidi port whose name contains `match`.

    amidi -l prints a header line then one row per port:
        Dir Device    Name
        IO  hw:1,0,0  EP-136 MIDI 1
    """
    try:
        out = subprocess.run(
            ["amidi", "-l"], capture_output=True, text=True, timeout=10
        ).stdout
    except FileNotFoundError:
        sys.exit("amidi not found. Install alsa-utils.")
    except subprocess.TimeoutExpired:
        sys.exit("amidi -l hung.")

    rows = []
    for line in out.splitlines()[1:]:
        m = re.match(r"\s*(\S+)\s+(hw:\S+)\s+(.*\S)\s*$", line)
        if m:
            rows.append((m.group(1), m.group(2), m.group(3)))

    if not rows:
        sys.exit(
            "No MIDI ports at all.\n"
            "  Either the EP-136 is unplugged, or this firmware does not expose\n"
            "  a MIDI interface. Check `lsusb` and `dmesg | tail` after plugging in.\n"
            "  If no port appears, every clock-based feature is dead on arrival."
        )

    for direction, dev, name in rows:
        if match.lower() in name.lower():
            print(f"port: {dev}  ({name}, dir {direction})")
            return dev

    print(f"No port matching {match!r}. Ports present:", file=sys.stderr)
    for direction, dev, name in rows:
        print(f"    {dev:12} {name}  (dir {direction})", file=sys.stderr)
    sys.exit("Re-run with --port to force one of the above.")


class Reader:
    """Timestamped byte stream from `amidi -d`.

    amidi block-buffers its stdout when it is a pipe rather than a terminal,
    which would batch a second of clock into one read and make every derived
    tempo meaningless. stdbuf -o0 defeats that. Without stdbuf the timing
    numbers below are not trustworthy, so its absence is a loud warning rather
    than a silent degradation.
    """

    def __init__(self, port):
        # -c and -a are LOAD-BEARING. amidi excludes clock (F8) and active
        # sensing (FE) from a dump unless asked, on the reasoning that they are
        # noise. For this probe they are the entire signal. Without -c, amidi
        # exits 0 with an empty stderr having silently dropped every clock byte
        # -- which reads as "the EP sends nothing" and would retire the feature
        # on a false negative. This exact mistake cost one hardware run.
        cmd = ["amidi", "-d", "-c", "-a", "-p", port]
        if shutil.which("stdbuf"):
            cmd = ["stdbuf", "-o0"] + cmd
        else:
            print(
                "WARNING: stdbuf not found; amidi output will be block-buffered\n"
                "         and every tempo figure below should be ignored.",
                file=sys.stderr,
            )
        self.proc = subprocess.Popen(
            cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE
        )
        self.partial = ""

    def check_alive(self):
        """Raise if amidi died, surfacing its stderr.

        amidi's stderr is piped and otherwise never read, so a failure to open
        the port -- or an older amidi that rejects -c -- would look exactly
        like a device sending nothing. A probe whose only job is to decide
        whether hardware is silent must never confuse "no data" with "the tool
        broke".
        """
        if self.proc.poll() is None:
            return
        err = (self.proc.stderr.read() or b"").decode("ascii", "replace").strip()
        raise RuntimeError(
            f"amidi exited early (status {self.proc.returncode}): "
            f"{err or 'no stderr output'}"
        )

    def drain(self):
        """Discard whatever is already buffered, so a phase measures itself.

        amidi keeps writing during the prompt between phases, and that backlog
        is delivered to whichever phase reads next. Without this, clock from
        the sequencer-running phase lands in the free-play phase's tally and
        the verdict is drawn from the wrong window.
        """
        self.check_alive()
        deadline = time.monotonic() + 1.0
        while time.monotonic() < deadline:
            r, _, _ = select.select([self.proc.stdout], [], [], 0)
            if not r:
                break
            if not os.read(self.proc.stdout.fileno(), 65536):
                break
        self.partial = ""

    def read(self, timeout):
        """Yield (timestamp, byte) pairs available within `timeout` seconds.

        The timestamp is when this process saw the chunk, not when the byte
        left the EP: it carries USB, kernel, and pipe latency. Over a 24-clock
        averaging window that jitter cancels well enough to identify a tempo,
        but it is why the report prints spread alongside the median rather than
        a single confident number.
        """
        r, _, _ = select.select([self.proc.stdout], [], [], timeout)
        if not r:
            return
        chunk = os.read(self.proc.stdout.fileno(), 65536)
        now = time.monotonic()
        if not chunk:
            return
        for b in self.feed_text(chunk.decode("ascii", "replace")):
            yield now, b

    def feed_text(self, text):
        """Hex tokens out of a text chunk, holding back a split token.

        A read can land mid-token -- "F" now, "8" next time -- so a trailing
        fragment is only emitted once whitespace proves it complete. Without
        this, one unlucky read boundary turns a clock pulse into two bogus
        bytes.
        """
        self.partial += text
        tokens = self.partial.split()
        if self.partial and not self.partial[-1].isspace():
            self.partial = tokens.pop() if tokens else ""
        else:
            self.partial = ""
        out = []
        for tok in tokens:
            try:
                out.append(int(tok, 16))
            except ValueError:
                pass
        return out

    def close(self):
        self.proc.terminate()
        try:
            self.proc.wait(timeout=2)
        except subprocess.TimeoutExpired:
            self.proc.kill()


class Tally:
    """Accumulates one phase's worth of traffic."""

    def __init__(self):
        self.counts = collections.Counter()
        self.clock_times = []
        self.events = []  # (offset_seconds, description)
        self.sysex = []
        self.channel_msgs = 0
        self._sysex_buf = None
        self._pending = None  # (status, bytes_wanted, collected)
        self.t0 = time.monotonic()

    def feed(self, ts, b):
        off = ts - self.t0

        # Realtime bytes preempt everything, including a SysEx in flight.
        if b >= 0xF8:
            self.counts[b] += 1
            if b == CLOCK:
                self.clock_times.append(ts)
            elif b in (START, CONTINUE, STOP, RESET):
                self.events.append((off, NAMES.get(b, hex(b))))
            return

        if self._sysex_buf is not None:
            if b == SYSEX_END:
                self._sysex_buf.append(b)
                self.sysex.append((off, bytes(self._sysex_buf)))
                self._sysex_buf = None
            else:
                self._sysex_buf.append(b)
                if len(self._sysex_buf) > 512:  # runaway guard
                    self.sysex.append((off, bytes(self._sysex_buf)))
                    self._sysex_buf = None
            return

        if b == SYSEX_START:
            self._sysex_buf = bytearray([b])
            return

        if self._pending is not None:
            status, want, got = self._pending
            got.append(b)
            if len(got) == want:
                self._finish(off, status, got)
                self._pending = None
            return

        if b == SPP:
            self._pending = (SPP, 2, [])
        elif b in (MTC_QF, SONG_SELECT):
            self._pending = (b, 1, [])
        elif b >= 0x80:
            self.counts[b & 0xF0] += 1
            self.channel_msgs += 1

    def _finish(self, off, status, data):
        if status == SPP:
            sixteenths = data[0] | (data[1] << 7)
            self.events.append(
                (off, f"song-position {sixteenths} sixteenths "
                      f"(bar {sixteenths // 16 + 1} if 4/4)")
            )
        elif status == MTC_QF:
            self.counts[MTC_QF] += 1
        elif status == SONG_SELECT:
            self.events.append((off, f"song-select {data[0]}"))

    def gaps(self):
        """Pauses in the clock stream, as (offset, seconds) pairs.

        A count alone cannot tell a continuous stream from one that stopped and
        restarted -- 403 pulses where 443 were expected looks identical either
        way. Anything beyond 3x the median spacing is a real pause, which is
        how a clock that halts with the transport gives itself away.
        """
        times = self.clock_times
        if len(times) < 3:
            return []
        deltas = sorted(times[i] - times[i - 1] for i in range(1, len(times)))
        median = deltas[len(deltas) // 2]
        if median <= 0:
            return []
        return [
            (times[i - 1] - self.t0, times[i] - times[i - 1])
            for i in range(1, len(times))
            if times[i] - times[i - 1] > 3 * median
        ]

    def bpm_stats(self):
        """Tempo two ways, because one of them is an artifact of this script.

        `overall` divides the whole phase's clock count by its span. It touches
        only the first and last timestamp, so batching bytes into chunks cannot
        distort it -- this is the number to trust.

        `median` is a rolling one-quarter-note window, kept because it exposes
        drift that a single average would hide. Its outliers are meaningless:
        every byte in one read shares a timestamp, so a window landing inside a
        single chunk shows near-zero elapsed time and a tempo in the thousands.
        `degenerate` counts windows that collapsed to exactly zero, which is a
        direct measure of how coarse the timestamping was. Spread is reported
        as p5-p95 rather than min-max so it describes the EP rather than that
        quantization.
        """
        times = self.clock_times
        if len(times) < CLOCKS_PER_QUARTER + 1:
            return None
        span = times[-1] - times[0]
        if span <= 0:
            return None

        bpms, degenerate = [], 0
        w = CLOCKS_PER_QUARTER
        for i in range(w, len(times)):
            dt = times[i] - times[i - w]
            if dt > 0:
                bpms.append(60.0 / dt)
            else:
                degenerate += 1
        if not bpms:
            return None
        bpms.sort()

        def pct(p):
            return bpms[min(len(bpms) - 1, int(p * len(bpms)))]

        return {
            "overall": 60.0 * (len(times) - 1) / (CLOCKS_PER_QUARTER * span),
            "median": bpms[len(bpms) // 2],
            "p5": pct(0.05),
            "p95": pct(0.95),
            "degenerate": degenerate,
            "n": len(bpms),
        }


def run_phase(reader, seconds, title, instruction):
    print()
    print("=" * 68)
    print(f"PHASE: {title}")
    print(f"  {instruction}")
    if sys.stdin.isatty():
        input("  Press Enter when ready...")
    else:
        print("  (not a tty -- starting in 3s)")
        time.sleep(3)

    reader.drain()
    tally = Tally()
    print(f"  listening {seconds}s", end="", flush=True)
    deadline = time.monotonic() + seconds
    next_dot = time.monotonic() + 1
    while time.monotonic() < deadline:
        for ts, b in reader.read(timeout=0.2):
            tally.feed(ts, b)
        if time.monotonic() >= next_dot:
            print(".", end="", flush=True)
            next_dot += 1
    print(" done")

    report(tally, seconds)
    return tally


def report(t, seconds):
    clocks = t.counts[CLOCK]
    print(f"  clock pulses : {clocks}  ({clocks / seconds:.1f}/s)")
    bpm = t.bpm_stats()
    if bpm:
        print(f"  tempo        : {bpm['overall']:.2f} BPM  <- trust this one")
        print(
            f"                 rolling median {bpm['median']:.2f}, "
            f"p5-p95 {bpm['p5']:.1f}-{bpm['p95']:.1f}"
        )
        if bpm["degenerate"]:
            print(
                f"                 ({bpm['degenerate']} of {bpm['n'] + bpm['degenerate']}"
                " windows collapsed to zero elapsed time -- read batching, not the EP)"
            )
    elif clocks:
        print("  tempo        : too few pulses to estimate")
    for b in (START, CONTINUE, STOP, RESET, ACTIVE_SENSE, MTC_QF):
        if t.counts[b]:
            print(f"  {NAMES.get(b, hex(b)):13}: {t.counts[b]}")
    gaps = t.gaps()
    if gaps:
        total = sum(g for _, g in gaps)
        print(f"  clock GAPS   : {len(gaps)}, {total:.2f}s total -- the stream is NOT continuous")
        for off, g in gaps[:5]:
            print(f"    +{off:5.2f}s  paused {g:.3f}s")
    elif clocks:
        print("  continuity   : no gaps, clock ran unbroken")
    if t.channel_msgs:
        print(f"  channel msgs : {t.channel_msgs} (notes/CC -- the EP is sending performance data too)")
    for off, desc in t.events[:20]:
        print(f"    +{off:5.2f}s  {desc}")
    if len(t.events) > 20:
        print(f"    ... {len(t.events) - 20} more")
    for off, raw in t.sysex:
        print(f"    +{off:5.2f}s  SYSEX {raw.hex(' ')}")
    if not clocks and not t.events and not t.channel_msgs and not t.sysex:
        print("  (silence -- nothing received at all)")


def verdict(idle, running, freeplay):
    print()
    print("=" * 68)
    print("VERDICT")
    print()

    print("1. Does a MIDI port exist?")
    print("   Yes -- a port was found and opened. (If it had not been, this")
    print("   script would have exited before any phase ran.)")
    print()

    print("2. Does clock flow with the sequencer STOPPED?")
    idle_c, free_c = idle.counts[CLOCK], freeplay.counts[CLOCK]
    if idle_c or free_c:
        print(f"   YES -- {idle_c} pulses at rest, {free_c} while playing freely.")
        print("   Tempo is available even when you are just jamming, so bar-based")
        print("   capture and tempo metadata work for the case the dashcam is for.")
    else:
        run_c = running.counts[CLOCK]
        if run_c:
            print(f"   NO -- {run_c} pulses only while the sequencer ran; zero otherwise.")
            print("   This is the bad outcome. Tempo would be absent for unplanned")
            print("   playing, which is most of what the dashcam catches. Bar-based")
            print("   capture lengths would have nothing to resolve against, and")
            print("   tempo metadata would be blank on the takes you care about.")
        else:
            print("   NO CLOCK ANYWHERE -- not even with the sequencer running.")
            print("   Either the firmware does not send clock, or it must be enabled")
            print("   in the EP's settings, or clock output is off by default.")
            print("   Check the EP's MIDI settings before concluding anything.")
    print()

    print("3. Is there a DOWNBEAT? (transport and song position)")
    phases = (idle, running, freeplay)
    transport = sum(p.counts[b] for p in phases for b in (START, CONTINUE, STOP))
    spp = sum(1 for p in phases for _, d in p.events if d.startswith("song-position"))
    notes = sum(p.channel_msgs for p in phases)
    if transport or spp:
        print(f"   YES -- {transport} transport message(s), {spp} song-position message(s).")
        print("   Tempo AND phase are both available, so bar lines can be anchored to")
        print("   a real downbeat. A bar-aligned grid is possible.")
    else:
        print("   NO -- no start/stop/continue and no song position in any phase.")
        print("   Clock free-runs, which gives TEMPO but not PHASE: there is no way")
        print("   to know where bar 1 falls. Consequences, and they differ:")
        print("     - Capture lengths  UNAFFECTED. '16 bars' is a duration, and")
        print("       duration needs only tempo.")
        print("     - Tempo metadata   UNAFFECTED.")
        print("     - Bar-aligned grid IMPOSSIBLE. It needs an origin this does not")
        print("       provide; a grid on an arbitrary origin is worse than none.")
    print(f"   Note/CC over USB: {notes} message(s)"
          + ("" if notes else " -- the EP sends no performance data over USB MIDI."))
    print()

    print("4. Is there a time signature anywhere?")
    allsx = idle.sysex + running.sysex + freeplay.sysex
    if allsx:
        print(f"   MAYBE -- {len(allsx)} SysEx message(s) captured, dumped above.")
        print("   Decode them before assuming 4/4; a meter would most likely live")
        print("   there. Otherwise every 'bars' feature is a 4/4 assumption.")
    else:
        print("   NO -- no SysEx seen. Nothing in standard MIDI clock or song")
        print("   position carries a meter, so any bar count is a hardcoded 4/4")
        print("   assumption. Decide whether that is acceptable or whether the")
        print("   meter should be a user setting.")
    print()
    print("Paste this whole output back into the session.")


def main():
    p = argparse.ArgumentParser(
        description="Probe what the EP-136 sends over MIDI.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    p.add_argument("--match", default="EP-136", help="port name substring")
    p.add_argument("--port", help="explicit amidi port, skips discovery")
    p.add_argument("--seconds", type=int, default=12, help="seconds per phase")
    p.add_argument("--log", default="/tmp/midi-probe.log",
                   help="mirror all output here so the per-phase detail survives")
    args = p.parse_args()

    # Everything is mirrored to a log, because the interesting detail is the
    # per-phase output and that is exactly the part that scrolls away. The
    # verdict is what gets copied; the evidence behind it should survive
    # without depending on anyone's terminal buffer.
    log = open(args.log, "w")

    class Tee:
        def write(self, s):
            sys.__stdout__.write(s)
            log.write(s)

        def flush(self):
            sys.__stdout__.flush()
            log.flush()

    sys.stdout = Tee()
    print(f"(full log: {args.log})")

    port = args.port or find_port(args.match)
    reader = Reader(port)
    try:
        idle = run_phase(
            reader, args.seconds,
            "idle",
            "EP-136 plugged in, sequencer STOPPED. Do not touch it.",
        )
        running = run_phase(
            reader, args.seconds,
            "sequencer running",
            "Press Enter FIRST, then start the sequencer once it says\n"
            "  listening. Anything sent before the phase begins is discarded\n"
            "  by the drain, so starting early hides the Start message -- which\n"
            "  is the whole point of this phase.",
        )
        freeplay = run_phase(
            reader, args.seconds,
            "free play, sequencer stopped",
            "Press Enter FIRST. Once it says listening, STOP the sequencer,\n"
            "  then play the pads freely. Same reason as above: a stop sent\n"
            "  before the phase starts is drained and never counted.",
        )
        verdict(idle, running, freeplay)
    except KeyboardInterrupt:
        print("\ninterrupted")
    finally:
        reader.close()


if __name__ == "__main__":
    main()
