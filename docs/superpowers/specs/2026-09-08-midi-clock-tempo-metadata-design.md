# MIDI Clock and Tempo Metadata Design

**Date:** 2026-09-08
**Status:** awaiting owner review

## Goal

Stamp each take with the tempo it was played at, so takes can be sorted and
recognised when the owner comes back to organise them later. The value is a
starting point, not a fact: **it must be editable.**

Underneath that sits a MIDI clock listener and a clock ring. Only the tempo
field consumes it now. It exists in this shape because the next project — a
buffer scrubber that lets the owner "scoop" a region out of the ring — needs
the same data at higher fidelity, and because proving the listener in
production before anything depends on it was the owner's explicit choice.

## What the hardware actually does

Measured on 2026-09-08 across repeated hardware runs, including one fully
interactive. These are facts, not assumptions, and three of them killed
features:

- **The EP-136 sends MIDI clock over USB continuously, whether or not the
  sequencer is running.** This was the question that decided whether to build
  anything.
- **It sends nothing else.** A clean 60-second capture was 3373 bytes, every
  one of them `F8`. No notes, no CC, no active sensing, no SysEx.
- **Clock-send is off by default** and must be enabled on the device.
- **Tempo is stable to ±0.3 BPM when the device is left alone**, and genuinely
  varies while playing — one free-play window spread 91.6–95.3 BPM with the
  clock running unbroken.
- **The free-running clock does not reliably match the loaded project tempo.**
  One idle window read 129.87 BPM, rock-stable, while the project was set to
  92. Stability does not distinguish a correct reading from a confidently
  wrong one, and there is no in-band way to tell them apart.

That last point is why editability is a requirement rather than a convenience.

## Correction, 2026-09-09: the EP has no sequencer

**The owner confirmed during hardware verification that the EP-136 has no
sequencer at all.** The 1010music Bento is connected over 3.5mm audio only, and
carries no MIDI. The EP is *inferring* a tempo with an on-device algorithm —
the same figure it presumably uses to time its FX — and transmitting that as
clock.

This changes three things in the section above, none of them the design:

- **"Whether the EP sends Start was never validly tested" is now moot.** There
  is no transport to send. The beat grid is closed off at the source rather
  than merely untested, and no future retest can reopen it from this device.
- **It explains the finding that made editability a requirement.** "One idle
  window read 129.87 BPM, rock-stable, while the project was set to 92" was
  never a device misreporting a project tempo — there is no project tempo on
  the wire. That number was the inference engine's current belief, and it is
  free to disagree with anything the player thinks is true.
- **It reframes the field.** The BPM is the EP's guess at the room's tempo, not
  ground truth. Measured on a silent room it still reported a rock-steady
  100.67, so the value can be stale or arbitrary with no signal to infer from.
  Editability is no longer a safeguard against a rare wrong reading; it is the
  entire point of the field.

The architecture, the estimator, and the failure behaviour are unaffected. A
clock is a clock regardless of what generates it.

## What was rejected, and why

**Transport-triggered capture.** Dead by the owner's decision: a start/stop
trigger is not realistic when the tempo moves as much as it does. Note that
this is *not* a hardware limitation — whether the EP sends Start was never
validly tested, because the probe's drain discards anything buffered before a
phase begins and the device was operated before each phase started. If the
beat grid is ever revived, retest this first; Start is what would supply a
downbeat.

**A bar-aligned beat grid.** Needs a downbeat, which needs transport. Also
blocked behind Phase 2 trim, which does not exist yet.

**Bar-length capture buttons ("last 16 bars").** Rejected on the owner's
observation, which is correct: without a downbeat, "last 16 bars" means 1536
pulses counted back from whenever the button was pressed. That is the right
*duration* at an arbitrary *phase* — sixteen bars' worth starting and ending
mid-bar, which is not a loop. No amount of clock data fixes it, because the
misalignment is in the moment of pressing. The buffer scrubber dissolves the
problem instead: when regions are chosen by eye against a waveform, the
downbeat is wherever the owner says it is.

**A meter setting.** Wanted, but it belongs to the scrubber. Tempo metadata
needs no meter; only bar counts do.

**Storing a stability or spread figure alongside the BPM.** Considered,
because a wide spread is detectable even though a wrong-but-stable reading is
not. Rejected as YAGNI: the field is additive, so a signal can be added later
without a schema break.

## Architecture

Four pieces. The first two are new, the last two extend what exists.

### 1. MIDI reader

ALSA rawmidi devices are plain character devices. Discovery scans
`/proc/asound/cards` for a card whose name matches the existing `DEVICE_MATCH`
config value; the reader then opens `/dev/snd/midiC<N>D0` and reads bytes in
its own goroutine.

**No cgo, no subprocess, no library.** Verified on hardware: a direct read of
the device node returned 625 bytes in 10 seconds, all `F8`, while the dashcam
kept capturing with zero xruns. MIDI and audio are separate USB interfaces and
do not contend.

**Do not shell out to `amidi`.** Beyond the process dependency, `amidi -d`
*excludes clock bytes* unless given `-c`, exits 0, and writes nothing to
stderr. That already cost one hardware run and a false negative. The raw
device node has no such filter.

The parser handles System Realtime only: `F8` clock, and `FA`/`FB`/`FC`
transport recorded but unused. **Realtime bytes must be tested before anything
else** — they are single bytes that may arrive inside another message, and
mishandling that corrupts both.

### 2. Clock ring

A ring of pulse timestamps covering the audio ring's window. At the highest
rate observed (~62/sec) fifteen minutes is roughly 56,000 timestamps, well
under a megabyte.

It exposes one query: **the tempo over a wall-clock interval.** The estimator
is the median of rolling one-quarter-note windows, which is deliberately not
the mean — during free play the overall rate sat above the median, the
signature of a rate that climbed mid-window, and the mean inherits that skew.

**Wall-clock correlation, not frame-exact alignment.** This is the deliberate
simplification of this slice. Tempo over "roughly this window" needs only wall
time. Frame-exact alignment is what a *bar overlay* needs, and that is the
scrubber's problem. Design the ring so it can be made frame-exact later
without redesign; do not build it now.

### 3. Tempo at save time

`Saver.Save` already knows the captured window. It converts that to a
wall-clock interval, asks the clock ring for the median, and writes the
result.

Fewer than two quarter notes of pulses in the window yields **no BPM** —
absent, not zero.

**A save must never fail because of MIDI.** No device, no clock, a parse
error, an unplugged interface: all of these produce a take without a BPM. The
dashcam's job is audio.

### 4. Sidecar, API, and UI

`Meta.BPM *float64` with `omitempty`. A pointer so that absent is distinct
from zero.

**No `MetaVersion` bump.** The existing comment sets the rule — bump only for
a change older readers cannot tolerate — and an optional additive field is
tolerable.

Editing follows the existing label-rename path in `api.go`: same shape, same
validation posture. **Accept 20-400 BPM inclusive, reject anything outside it,
and treat an empty submission as clearing the field** rather than as zero. The
range is deliberately wider than the EP will ever produce, because the point of
the field is that the owner overrides it — including for takes whose clock
reading was wrong.

The takes list shows the BPM beside the label and edits it inline, phone-first
like everything else.

## Failure behaviour

**The hard rule: MIDI failure must never disturb audio capture.** The reader
owns its own lifecycle in its own goroutine. Errors are logged, never
propagated into the capture path.

The MIDI device disappears when the interface is unplugged — the same class of
problem `paLifecycle` and the device rescan already solved for audio, and the
same recovery shape applies: detect the error, back off, rescan, reopen. A
reader that cannot find its device is a normal state, not an error state; the
EP is frequently absent.

## Verification

- The BPM estimator against synthetic input at a known tempo, exact to two
  decimals, including the chunked-arrival case that distorts a rolling median
  while leaving the overall figure intact.
- Realtime bytes arriving inside another message are counted without
  corrupting the enclosing message.
- A save with no MIDI device present produces a valid take with no BPM field.
- A save with clock present produces a BPM within 1 BPM of a reference
  measured by `scripts/midi-probe.py` over the same period.
- Sidecars written before this change still load, and sidecars carrying a BPM
  load in a build that predates it.
- On hardware: unplug the interface mid-run and confirm capture is undisturbed
  and the reader recovers.

## Out of scope

The buffer scrubber and everything it implies — whole-buffer waveform, region
selection, bar overlay, meter setting. That is the next project, and the
reason this one exists in this shape.

Phase 2 trim, Phase 3 egress, and the beat grid are unchanged by this work.
