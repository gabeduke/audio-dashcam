# Channel Routing and Level Snapshot Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Settle which EP-136 USB channel pair carries the main mix, set `SAVE_CHANNELS` to it, and fix a real bug that makes the Input-channels meters drop to −∞ roughly a quarter of the time.

**Architecture:** Two independent halves. The code half removes a race between `Levels.Snapshot` and `Levels.Drain` by keeping the last bin's levels in a dedicated field. The empirical half adds a small polling probe and a two-run experiment that identifies the four stereo pairs by moving the source between physical inputs.

**Tech Stack:** Go 1.x, Python 3 (stdlib only), the EP-136's `/api/status` endpoint.

---

## What was reported, and what is already known

The owner reported, while testing on real hardware:

> it only seems to be picking up channel 1 output. Additionally it is not
> picking up the leveled values

### Established by measurement, before writing any code

**The capture path works, and `SAVE_CHANNELS=3,4` records real audio.** The two
takes saved during that session were analysed with `ffmpeg astats`:

| take | ch3 (L) | ch4 (R) |
|---|---|---|
| `jam_2026-09-07_235352.wav` | peak −3.15 dBFS, RMS −18.52 | peak −3.15, RMS −18.49 |
| `jam_2026-09-07_235926.wav` | peak −3.15 dBFS, RMS −25.15 | peak −3.15, RMS −25.10 |

A null test (`0.5*c0 − 0.5*c1`) gives a residual 14.6 dB below the signal on the
first take and 33 dB down on the second, so the pair is a genuine — if
centre-heavy — stereo pair, not one mono source duplicated. **Whatever is on USB
3/4 is a real, healthy, correctly captured stereo signal.** Any theory that the
audio path is broken is already refuted.

Worth noting: both takes peak at essentially exactly −3.15 dBFS despite RMS
differing by 6.6 dB, which is what a bus with a fixed ceiling looks like. That
is weak evidence for 3/4 being MAIN rather than a raw channel send, but it is
not proof — a limiter on the source would look the same.

### Established from the vendor documentation

The device is a **Teenage Engineering EP-136 K.O. Sidekick** (not Sonicware —
the earlier assumption in `dashcam.env` comments was wrong). Its documented USB
audio is **8 in / 4 out**, and the 8 inputs are **four stereo record pairs:
channel one, channel two, aux, and the main output**.

Teenage Engineering does **not** publish which USB channel number each pair
lands on. The signal flow diagram
(`https://teenage.engineering/guides/ep-136/signal-flow-diagram`) shows the
routing — `CH → GAIN → COMP → 3-BAND EQ → FADER → FX`, summed with `AUX` into a
single bus that feeds both `OUTPUT` and `USB OUT` — but the diagram has one
undifferentiated `USB OUT` block and no channel numbering. Its text is
flattened to paths, so there is nothing more to extract from the asset.

**So the channel map has to be determined experimentally.** That is Task 3.

### The hypotheses this plan discriminates

| # | Hypothesis | Status |
|---|---|---|
| H1 | `SAVE_CHANNELS=3,4` points at silence | **Refuted** — the WAVs above are healthy |
| H2 | USB 3/4 is a single mixer channel, not the main mix, so the take misses everything else in the mix | **Open** — Task 3 decides |
| H3 | USB 3/4 *is* MAIN and the report was about the meter display, not the recording | **Open** — Task 3 decides |
| H4 | The Input-channels meters drop out intermittently | **Confirmed by inspection** — Task 1 fixes it |
| H5 | The WebSocket level stream is broken by nginx | **Refuted** — `map $http_upgrade $connection_upgrade` is defined in `/etc/nginx/conf.d/websocket-upgrade.conf` and `nginx -t` passes |

---

## File Structure

| File | Responsibility |
|---|---|
| `v2-go/audio/levels.go` (modify) | Add a `lastRMS` field written on every bin flush; `Snapshot` reads it instead of `pending`. |
| `v2-go/audio/levels_test.go` (create) | Tests that `Snapshot` is independent of `Drain`, and that it reads the floor before any audio. |
| `scripts/channel-probe.py` (create) | Polls `/api/status` and reports peak dBFS per channel plus a pair summary. Stdlib only, runs from the Mac. |
| `deploy/dashcam.env.example` (modify) | Record the real channel map once measured, and correct the vendor name. |

---

### Task 1: `Snapshot` must not depend on `Drain`

**Files:**
- Modify: `v2-go/audio/levels.go`
- Test: `v2-go/audio/levels_test.go` (create)

**The bug.** `Snapshot()` reads `l.pending[len(l.pending)-1].RMS` and returns
`FloorDB` for every channel when `pending` is empty. `Broadcast()` calls
`Drain()`, which sets `pending = nil`, and the broadcaster ticks every 40 ms
whenever a websocket client is connected. Bins flush every 10 ms. So for the
~10 ms after each drain, `pending` is empty and `/api/status` reports −∞ on all
eight channels — about **25% of polls**, but *only while the UI is open*, which
is exactly when someone is looking at it.

`app.js` calls `chanMeters.update(s.channel_rms, s.channel_rms, null)` on every
2-second status poll, so the Input-channels strip visibly drops to −∞ and
snaps back. This is the strongest candidate for "not picking up the leveled
values".

- [ ] **Step 1: Write the failing tests**

Create `v2-go/audio/levels_test.go`:

```go
package audio

import "testing"

// oneBinOfConstant builds an interleaved block of exactly one bin's worth of
// frames, every sample at the same amplitude. 1<<29 against the 1/2^31 scale
// is 0.25, i.e. about -12.04 dBFS, comfortably above FloorDB.
func oneBinOfConstant(channels, binFrames int, v int32) []int32 {
	block := make([]int32, binFrames*channels)
	for i := range block {
		block[i] = v
	}
	return block
}

func TestSnapshotSurvivesDrain(t *testing.T) {
	l := NewLevels(2, 48000, 10) // 480 frames per bin
	l.Accumulate(oneBinOfConstant(2, 480, 1<<29))

	before := l.Snapshot()
	if before[0] <= FloorDB {
		t.Fatalf("expected a real level before Drain, got %v", before)
	}

	// Broadcast drains every 40ms whenever the UI is open. A status poll that
	// lands just after a drain must still report the last known levels.
	l.Drain()

	after := l.Snapshot()
	if after[0] <= FloorDB {
		t.Fatalf("Snapshot fell to the floor after Drain: %v (it must not depend on pending)", after)
	}
	if after[0] != before[0] || after[1] != before[1] {
		t.Fatalf("levels changed across Drain: %v -> %v", before, after)
	}
}

func TestSnapshotReadsFloorBeforeAnyAudio(t *testing.T) {
	// A freshly made []float32 is all zeros, and 0 dBFS is full scale. If the
	// new field is not seeded with FloorDB the meters peg on startup.
	l := NewLevels(4, 48000, 10)
	got := l.Snapshot()
	if len(got) != 4 {
		t.Fatalf("want 4 channels, got %d", len(got))
	}
	for i, v := range got {
		if v != FloorDB {
			t.Fatalf("channel %d reads %v before any audio, want FloorDB (%v)", i, v, FloorDB)
		}
	}
}

func TestSnapshotTracksTheMostRecentBin(t *testing.T) {
	l := NewLevels(1, 48000, 10)
	l.Accumulate(oneBinOfConstant(1, 480, 1<<29)) // ~ -12 dBFS
	loud := l.Snapshot()[0]

	l.Accumulate(oneBinOfConstant(1, 480, 1<<20)) // ~ -66 dBFS, below the floor
	quiet := l.Snapshot()[0]

	if quiet >= loud {
		t.Fatalf("Snapshot did not follow the newest bin: %v then %v", loud, quiet)
	}
	if quiet != FloorDB {
		t.Fatalf("a sub-floor bin should clamp to FloorDB, got %v", quiet)
	}
}
```

- [ ] **Step 2: Run the tests and confirm the first one actually fails**

Run:
```bash
cd /Users/gabeduke/projects/audio-dashcam/v2-go && go test ./audio/ -run TestSnapshot -v
```
Expected: `TestSnapshotSurvivesDrain` **FAILS** with
`Snapshot fell to the floor after Drain: [-60 -60]`.
`TestSnapshotReadsFloorBeforeAnyAudio` passes already (the current code seeds
the output with `FloorDB` by hand).

**Do not skip this step.** A regression test that passes against the unfixed
code is worse than no test; this project has already shipped one such test and
caught it only by reverting the fix. If `TestSnapshotSurvivesDrain` passes here,
stop and work out why before changing anything.

- [ ] **Step 3: Add the field**

In `v2-go/audio/levels.go`, add `lastRMS` to the `Levels` struct, after `clip`:

```go
	mu       sync.Mutex
	curMin   []float32
	curMax   []float32
	curSumSq []float64
	curCount int
	pending  []Bin
	peakHold []float32
	clip     []bool
	lastRMS  []float32 // newest bin's per-channel dBFS, held independently of
	                   // pending so a drain does not blank /api/status
```

- [ ] **Step 4: Seed it in NewLevels**

Add `lastRMS` to the struct literal in `NewLevels`:

```go
	l := &Levels{
		channels:   channels,
		binFrames:  binFrames,
		sampleRate: sampleRate,
		curMin:     make([]float32, channels),
		curMax:     make([]float32, channels),
		curSumSq:   make([]float64, channels),
		peakHold:   make([]float32, channels),
		clip:       make([]bool, channels),
		lastRMS:    make([]float32, channels),
		subs:       map[chan Frame]struct{}{},
	}
	l.resetBin()
	for i := range l.peakHold {
		l.peakHold[i] = FloorDB
	}
	for i := range l.lastRMS {
		l.lastRMS[i] = FloorDB
	}
	return l
```

- [ ] **Step 5: Write it on every flush**

In `flushBinLocked`, immediately after the `for c := 0; c < l.channels; c++`
loop that fills `b` and before `l.pending = append(l.pending, b)`:

```go
	copy(l.lastRMS, b.RMS)
	l.pending = append(l.pending, b)
```

- [ ] **Step 6: Read it in Snapshot**

Replace the whole body of `Snapshot`:

```go
// Snapshot reports the current per-channel RMS in dBFS without consuming bins.
// Used by /api/status and for the channel-routing diagnostic.
//
// It reads lastRMS rather than pending because Broadcast drains pending every
// 40ms whenever a websocket client is connected, which used to leave a status
// poll landing in that gap reporting silence on every channel.
func (l *Levels) Snapshot() []float32 {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]float32, l.channels)
	copy(out, l.lastRMS)
	return out
}
```

- [ ] **Step 7: Run the tests to verify they pass**

Run:
```bash
cd /Users/gabeduke/projects/audio-dashcam/v2-go && go test ./audio/ -run TestSnapshot -v && go test ./...
```
Expected: all three new tests PASS, and the full suite is green.

- [ ] **Step 8: Commit**

```bash
cd /Users/gabeduke/projects/audio-dashcam
git add v2-go/audio/levels.go v2-go/audio/levels_test.go
git commit -m "Hold the last bin's levels so a drain cannot blank /api/status

Snapshot read pending, which Broadcast empties every 40ms whenever the UI has
a websocket open. Bins flush every 10ms, so roughly a quarter of status polls
reported -inf on every channel and the Input-channels strip flickered to
silence -- but only while someone was watching it. Keep the newest bin's RMS
in its own field instead."
```

---

### Task 2: A probe that reports peak level per channel

**Files:**
- Create: `scripts/channel-probe.py`

- [ ] **Step 1: Write the script**

Create `scripts/channel-probe.py`:

```python
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
    python3 scripts/channel-probe.py ${DASHCAM_HOST#*@} --seconds 20
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
```

- [ ] **Step 2: Make it executable and check it runs**

```bash
cd /Users/gabeduke/projects/audio-dashcam
chmod +x scripts/channel-probe.py
python3 scripts/channel-probe.py --help
```
Expected: the usage text prints, no traceback.

- [ ] **Step 3: Confirm it refuses to run against a dead capture**

With the interface still off:
```bash
python3 scripts/channel-probe.py ${DASHCAM_HOST#*@} --seconds 2
```
Expected: exits with
`capture is not healthy (open "EP-136: USB Audio (hw:2,0)": Illegal combination of I/O devices) -- power the interface on ...`

This confirms the guard works and saves a confusing all-silent report later.

- [ ] **Step 4: Commit**

```bash
cd /Users/gabeduke/projects/audio-dashcam
git add scripts/channel-probe.py
git commit -m "Add a channel probe for identifying the EP-136 USB pair map

TE documents four stereo record pairs -- ch1, ch2, aux, main -- but not which
USB channel each lands on, so it has to be measured."
```

---

### Task 3: Run the experiment (needs the hardware)

**Files:** none — a procedure. Requires the EP-136 powered on and a sound source.

**The discriminator.** A source plugged into mixer input CH1 appears on two USB
pairs: CH1's own pair, and MAIN. Moving the same source to mixer input CH2
moves it to CH2's pair — but it is still in MAIN. **So the pair that is active
in both runs is MAIN**, and the pair that changes identifies CH1 versus CH2.
The pair silent in both runs is AUX.

- [ ] **Step 1: Power the EP-136 on and confirm the dashcam sees it**

```bash
curl -s http://${DASHCAM_HOST#*@}/api/status | python3 -m json.tool | grep -E 'capture_healthy|device'
```
Expected: `"capture_healthy": true`.

If it is still false, the hot-plug fix
(`docs/superpowers/plans/2026-09-08-usb-hotplug-recovery.md`) has not been
deployed yet — restart the service by hand and carry on:
```bash
ssh "$DASHCAM_HOST" 'systemctl --user restart audio-dashcam.service'
```

- [ ] **Step 2: Run A — source in mixer input CH1 only**

Plug the sound source into the EP-136's **channel 1** input. Nothing in
channel 2 or aux. Start playing something continuous and reasonably loud.

```bash
cd /Users/gabeduke/projects/audio-dashcam
python3 scripts/channel-probe.py ${DASHCAM_HOST#*@} --seconds 20
```

Record the "pairs" block verbatim. Expect exactly two active pairs.

- [ ] **Step 3: Run B — same source in mixer input CH2 only**

Move the cable to the EP-136's **channel 2** input. Same material, same level.

```bash
python3 scripts/channel-probe.py ${DASHCAM_HOST#*@} --seconds 20
```

Record the pairs block again.

- [ ] **Step 4: Derive the map**

Fill this in from the two runs:

| USB pair | active in run A (src in CH1) | active in run B (src in CH2) | therefore |
|---|---|---|---|
| 1/2 | | | |
| 3/4 | | | |
| 5/6 | | | |
| 7/8 | | | |

- Active in **both** → **MAIN**
- Active in **A only** → **CH1**
- Active in **B only** → **CH2**
- Active in **neither** → **AUX**

- [ ] **Step 5: Set `SAVE_CHANNELS` to the MAIN pair**

If MAIN turns out not to be 3/4, edit the Pi's config:
```bash
ssh "$DASHCAM_HOST" 'nano ~/audio-dashcam/dashcam.env'   # set SAVE_CHANNELS=<main pair>
ssh "$DASHCAM_HOST" 'systemctl --user restart audio-dashcam.service'
```

Then confirm:
```bash
curl -s http://${DASHCAM_HOST#*@}/api/status | python3 -m json.tool | grep -A3 save_channels
```

- [ ] **Step 6: Prove it end-to-end with a real capture**

With something playing through the mixer, hit Capture in the UI, then:
```bash
ssh "$DASHCAM_HOST" 'cd ~/audio-dashcam/jam_saves && ls -t *.wav | head -1'
```
and measure the newest file:
```bash
ssh "$DASHCAM_HOST" 'cd ~/audio-dashcam/jam_saves && ffmpeg -hide_banner -nostats -i "$(ls -t *.wav | head -1)" -af "astats=measure_overall=0:measure_perchannel=Peak_level+RMS_level" -f null - 2>&1 | grep -E "Channel:|Peak level|RMS level"'
```
Expected: both channels well above the floor, and — the point of the whole
exercise — the recording should contain **everything in the mix**, not just one
mixer channel. Play two sources at once through CH1 and CH2 and confirm both
are audible in the take.

This closes the long-standing STATE.md item "Confirm a real signal
end-to-end", which has been open since the Go rework.

- [ ] **Step 7: Write the map into the config example**

Edit `deploy/dashcam.env.example`. Replace the `SAVE_CHANNELS` comment block
with the measured truth, e.g. (adjust the numbers to what Task 3 found):

```
# Which channels carry the stereo master, 1-indexed as the hardware labels
# them. The interface is a Teenage Engineering EP-136 K.O. Sidekick, an
# 8-in/4-out device whose eight inputs are four stereo record pairs. TE does
# not document the numbering; this is what it measured as:
#
#   USB 1/2  ->  <fill in from the experiment>
#   USB 3/4  ->  <fill in>
#   USB 5/6  ->  <fill in>
#   USB 7/8  ->  <fill in>
#
# MAIN is the one you want: it carries the whole mix, not a single input.
# Verify with the "Input channels" panel in the UI, or with
# scripts/channel-probe.py, by playing something and watching which pair moves.
SAVE_CHANNELS=3,4
```

- [ ] **Step 8: Commit**

```bash
cd /Users/gabeduke/projects/audio-dashcam
git add deploy/dashcam.env.example
git commit -m "Record the measured EP-136 USB channel map

TE publishes the pair contents (ch1, ch2, aux, main) but not the numbering,
so it was measured by moving one source between physical inputs and noting
which USB pair stayed hot."
```

---

## If the experiment says 3/4 is already MAIN

Then the recording was never the problem, and "only picking up channel 1"
was about the display. In that case the remaining work is:

1. Confirm Task 1's fix removed the −∞ flicker on the Input-channels strip —
   open the UI, expand **Input channels**, play something, and watch for a full
   two minutes. Before the fix, all eight rows snapped to `−∞` roughly every
   other 2-second poll.
2. Ask the owner to re-describe what "channel 1" referred to — the numbered rows
   in the Input-channels panel, the `L`/`R` meters above them, or the content of
   the saved take. Those are three different displays and the fix differs for
   each.

Do not guess between those three. The measurement in Task 3 is cheap and
settles it.

---

## Self-review notes

- Spec coverage: H4 is fixed in Task 1, H2/H3 are decided by Task 3, H1 and H5
  were refuted before planning and are recorded above so they are not
  re-investigated.
- Placeholders: the only blanks are the experiment's result table and the
  config comment in Task 3 Step 7, which are outputs of the procedure rather
  than undecided design.
- Type consistency: `lastRMS` is introduced, seeded, written and read under
  that exact name in Steps 3–6 of Task 1.
