# Buffer Ribbon Design

**Date:** 2026-09-08
**Status:** approved, ready to plan

## Goal

Render the capture ring as a waveform with checkpoint markers at the 30s / 2m /
7m / Full boundaries, so you can see *before* pressing Capture which button
catches the take you just played. Tapping an option shades the span it would
save.

Validated against a real 15-minute capture (`jam_2026-09-08_233045.wav`) rather
than a designed example. In that buffer all signal sat between 60s and 270s
ago — **15.6% of the ring**. Selecting 30s reads `SILENT — this would save
nothing`; selecting 7m catches the whole take without saving the eight minutes
of silence around it. Today there is no way to know either fact before pressing
the button.

This needs no MIDI clock and is blocked by nothing.

**Consequence for the backlog:** this reduces the need for Phase 2 trim, which
exists partly to clean up defensive over-capture. Weigh before planning Phase 2.

## What it replaces, and why that is not a loss

The ribbon **takes the live visualiser's slot** — the existing `.viz-wrap`, at
its existing per-breakpoint heights — so it costs no new vertical space, and
the `Visualizer` class in `meter.js` is deleted.

That trade is favourable but not free, and both halves should be stated:

- **Resolution improves.** The log axis is densest at the right edge:
  `W / (A · ln(T/A))` ≈ 50 px/s at the phone width, against the old
  visualiser's flat 37.6 px/s (900 bins at 100/s = 9 s spread over 338 px).
- **Latency worsens.** Ages below `A = 1 s` clamp to the right edge, and the
  poll is 1 Hz, so the ribbon runs **1–2 s behind** at that edge. The old
  visualiser was live at ~25 fps.

Live level therefore stops being the ribbon's job. The meters keep it, still on
the websocket at 25 fps, and they are what the eye already uses for "is signal
arriving now". The ribbon is an overview and is specified as one.

## What was rejected, and why

**Enlarging the client-side ring in `Visualizer`.** It only fills while the page
is open, and a dashcam is something you open *after* the moment. This is the
single fact that forces server-side work; nothing else here would be needed
without it.

**Retaining per-channel min/max.** Both mockups draw one waveform mirrored about
the midline, not a stereo pair — and at ≥1.6 s/px across most of the width, min
and max are both saturated and asymmetry is invisible. Storing a **mono peak
magnitude, one byte per bin** costs 88 KiB instead of 1.4 MB and matches the
encoding `scripts/take-envelope.py` already established.

**Appending live bins from the websocket instead of polling.** On a log-*age*
axis every bucket's position moves each tick, so an append still recomputes the
whole mapping — and to re-bucket locally the client would need the raw bins,
which it can only get from the server as a ~120 KB fetch on a link already
flagged as slow. Polling ~1.6 KB at 1 Hz is both simpler and cheaper.

**Canvas rendering.** At 1 Hz canvas buys nothing, and DOM/SVG drops the DPR and
`ResizeObserver` sizing machinery that `meter.js`'s own comments record as a
past bug. Labels stay real text.

**A configurable `edgeSeconds`.** Added while the ribbon was 522 px and
unnecessary at 1246. `A` is fixed at **1 s — the poll interval**; a smaller `A`
would draw detail the poll rate cannot deliver. One named constant, no env var.

**A seconds-of-signal readout.** `112s of signal` rests on an arbitrary −46 dBFS
gate: in a room with an HVAC floor at −40 dBFS every span reads as signal, the
number becomes meaningless, and the warning stops firing. The readout therefore
**warns only when a span is empty** and otherwise names the span alone. The API
returns the seconds regardless, so the fuller wording stays one line of JS away.

**A linear or equal-per-region axis.** Settled earlier: linear puts the 30s
marker 13 px from the edge at 390 px; equal-per-region jumps scale at every
boundary, so the same 5 s hit looks 16x wider in the 30s region than in Full.

## The axis

One formula, stated once and implemented on both sides:

```
left% = (1 − ln(age / A) / ln(T / A)) × 100      A = 1 s, T = ring_seconds
```

The server buckets on it; the client positions markers on it using the
`edge_seconds` and `ring_seconds` it gets back in the response. Bucket `i` of
`N` draws at `x = i/N`, so the client inverts nothing for the waveform itself.

At T = 900 s (`ln T = 6.802`):

| marker | left edge | note |
|---|---|---|
| 15m (Full) | 0% | |
| 7m | 11.2% | |
| 2m | 29.6% | |
| 30s | 50.0% | exactly half only because 30² = 900; it moves if the ring changes |
| now | 100% | |

Resolution at the oldest edge is `T · ln(T/A) / W`, which is what makes the
tablet span necessary — an 8-bar phrase at ~21 s is invisible until the ribbon
gets the full width:

| | ribbon width | oldest edge | a ~21 s phrase there |
|---|---|---|---|
| phone | 338 px | 18.1 s/px | 1.2 px — invisible |
| tablet, inside a column | 522 px | 11.7 s/px | 1.8 px — invisible |
| tablet, spanning both | 1246 px | 4.9 s/px | 4.3 px — visible |

Widening inside a column was never going to work: width is linear and the
compression logarithmic, so 1.54x of pixels fights a curve needing ~10x.

## Architecture

### 1. Envelope retention — `audio/envelope.go`

`Levels.flushBinLocked` already computes every 10 ms bin and discards it in
`Drain()`. It gains one line: push the bin to an `*Envelope` when one is set.

`Envelope` is a fixed ring of **one byte per bin**: peak magnitude across the
save channels, dB-coded over −60..0 as `round((db + 60) × 4.25)` — exactly
`meter.js`'s `(db − FLOOR_DB) / −FLOOR_DB` scaled to 0..255, so a consumer
divides by 255. Peak, never mean: a peak survives downsampling where a mean
washes out.

- **Capacity derives from `cfg.RingSeconds`**, not a new env var — `900 s /
  10 ms = 90,000 bytes = 88 KiB`. This matters because ring length already
  lives in two places (the Go default and the Pi's untracked `dashcam.env`);
  deriving it adds no third.
- **The write is one array store, allocation-free.** It runs on the PortAudio
  callback thread, where `flushBinLocked` today allocates three slices per bin,
  so this is lighter than what surrounds it.
- **`Envelope` owns the save-channel indices and the dB coding**, exposing
  `PushBin(Bin)`. `Levels` learns nothing about channel routing.
- **Lock order is `Levels.mu` → `Envelope.mu`.** Readers take only
  `Envelope.mu`. No cycle.
- It counts total pushes, so it knows how much of itself is real.

Methods: `PushBin(Bin)`, `Buckets(n int) []byte`, `SignalSeconds(span float64)
float64`, `BufferedSeconds() float64`.

### 2. `GET /api/envelope`

`/api/peaks` is already taken by the per-take sidecar, hence a new name.

Request: `?buckets=400&spans=30,120,420,900`

```json
{
  "ring_seconds": 900,
  "buffered_seconds": 412.3,
  "edge_seconds": 1,
  "buckets": [0, 0, 12, 47, 201, ...],
  "signal_seconds": [0, 0, 112.4, 112.4]
}
```

- `buckets` runs **oldest → newest**, 0..255, peak per bucket, log-spaced by the
  formula above. The client asks for roughly one bucket per CSS pixel,
  `min(600, round(width))`; ~1.6 KB of JSON at 400. Buckets covering ages
  the buffer has not reached yet return **0**, and the client derives the hatch
  boundary from `buffered_seconds` rather than from the byte values — a zero
  byte means silence and must not be overloaded to also mean "no data".
- `signal_seconds` is parallel to `spans`, counting bins at or above **byte
  60** — the −46 dBFS gate, which quantises to −45.9 — times the bin width. One linear scan over bytes already held.
- **`spans` comes from the client** so tier policy stays in `buildDurations()`;
  a span of `0` (Full) maps to `ring_seconds`.
- No `from`/`to` yet. Leaving them out now means a **linear range variant can be
  added later for the scrubber and Phase 2 trim** without breaking this shape.

### 3. The ribbon — `static/lib/ribbon.js`

DOM and SVG, matching the mockup: absolutely-positioned band divs, 1 px marker
rules, label spans under a top scrim, and one `<svg><path>` with
`preserveAspectRatio="none"`.

- Owns its own 1 Hz timer and pauses on `document.hidden`, resuming with an
  immediate fetch — the pattern `Visualizer` already established.
- `setSpans(spans)` and `setSelected(seconds)`; `app.js` calls the latter from
  the existing tier-button handler in `buildDurations()`.
- The readout sits **inside the ribbon, bottom-left**, under its own scrim. This
  costs no height on any layout and keeps the text next to the band it
  describes — which the mockup's placement under the buttons does not, least of
  all on tablet, where the buttons are in the left column and the ribbon spans
  the top.

Readout wording:

| state | text |
|---|---|
| span has signal | `last 7m` |
| span is empty | `last 2m — SILENT, this would save nothing` |
| span exceeds what is buffered | `last 7m — only 6m buffered` |
| nothing buffered | `buffer empty` |

Empty wins over under-buffered when both hold: if the buffer holds 90 s and the
selected span is 7m and that 90 s is silent, `SILENT` is the more actionable of
the two true statements.

`Visualizer` is deleted from `meter.js` (~95 lines); `Meters`, `FLOOR_DB` and
`dbToFrac` stay. `app.js` drops the import, the `viz` variable and the
`viz.setChannels(sel)` call in `applyStatus`.

### 4. Layout

`.viz-wrap` **moves out of the monitor panel** and becomes a direct child of
`main`, first in source order. CSS cannot reparent an element, so there is one
DOM for every viewport and the breakpoints only change how it is laid out.

**On phone** `main` is a flex column, so the ribbon stacks as a standalone
bordered block above the monitor panel — about 14 px more gap than today, and
slightly wider, since it no longer sits inside the panel's 14 px padding.
`.viz-wrap` already carries its own border and radius, so it stands alone.

**At the two-column breakpoints** it takes `grid-column: 1 / -1` and spans both
columns; everything else keeps the existing `5fr/6fr` split. `main` is a grid
only at `min-width: 860px` (or landscape `≥700px` and `≤560px` tall), so the
span rule belongs in those same media queries. `.col-monitor` stays sticky and
`align-items: start` still holds; the ribbon occupies row 1 and the columns
row 2.

The per-breakpoint `.viz-wrap` heights (132 / 172 / 160 / 190 / 96 / 64 px) are
inherited unchanged. At the ≤440px-tall landscape tier the 64 px ribbon has no
room for the bottom readout, which is hidden there — the same tier already
drops `.last` and the channel diagnostic.

**Standing rule:** `/lib/ribbon.js` must be added to `SHELL` in `sw.js` and
`CACHE` bumped to `dashcam-shell-v3`. `addAll` is atomic — miss this and
precaching fails entirely and offline dies silently.

## States

**Partial ring.** The service restarts on every deploy and on USB hot-plug
recovery, so a part-filled ring is routine, not an edge case. Bins never written
**must not draw as a flat line** — that reads as "twelve minutes of silence"
when the truth is "nothing there yet". Everything older than `buffered_seconds`
renders as a dim diagonal hatch (a `repeating-linear-gradient`, no new asset), and a span reaching into it says so in the readout. The
axis stays pinned to `ring_seconds` so the markers do not dance as the buffer
fills.

**Stale.** `.viz-wrap.stale::after` — today's red `no signal` scrim — must be
preserved; the EP is unplugged often enough that losing it is a regression. It
will need **`z-index: 2`**: it is currently the only positioned thing in the
wrap, and the ribbon's absolutely-positioned children would otherwise paint over
it.

**Ages are captured-audio time, not wall clock.** When capture stalls, the audio
ring and the envelope stall together, so what the ribbon shows is always what
Capture would actually write. This is the same property the audio ring has and
is deliberate.

## Failure behaviour

- **Poll fails:** keep the last ribbon drawn rather than blanking it. The health
  dot and its toast already report unreachability; a second indicator would add
  nothing.
- **`buffered_seconds == 0`:** the whole ribbon is hatch, readout `buffer empty`.
- **A span longer than the buffer:** allowed — Capture already clamps to what is
  buffered — and named in the readout.
- **`buckets` out of range:** clamp server-side to 1..600.

## Verification

**Go, no cgo, no Pi** — `envelope_test.go` runs on the Mac like `levels_test.go`:
ring wrap past capacity, bucket boundaries against the formula, peak-not-mean
aggregation, `SignalSeconds` either side of byte 60, and partial fill reporting
less than capacity. Plus an `api_test.go` case for the endpoint shape,
`spans=0` mapping to `ring_seconds`, and bucket clamping.

**Verify each regression test actually fails against the unfixed code.** This
project has shipped a test that could not fail and caught it only by reverting
the fix.

**Client:** a throwaway Playwright script in the session scratchpad — the
established pattern, not committed — across phone portrait, tablet landscape and
the ≤440px-tall landscape tier, checking the span rule, the stale scrim still
painting on top, and the hatch after a restart.

**On hardware:** deploy, restart the service, and confirm the hatch shrinks as
the buffer fills; then confirm against a real take that the 30s span reads
SILENT and 7m does not.

## Honest gaps

- **The far left (7m–15m) has never been looked at with real data.** The
  validation capture held nothing older than 270 s. That region is defensible
  only on the s/px arithmetic in the table above.
- **The 1–2 s lag at the right edge** is a real regression against the live
  visualiser, accepted because the meters carry live level.

## Out of scope

- **Region selection** — dragging out a span and saving exactly that. The ribbon
  is a step toward it. Two things already established: WaveSurfer is vendored
  but **Regions is not** (core only, 28 KB), and the scrubber and Phase 2 trim
  are the same component. Unsolved there: the buffer moves while you look at it,
  so a selection near the old edge can age out before you save.
- **Phase 2 trim** and the linear `?from=&to=` range endpoint it needs.
- Any change to what Capture writes. This feature only tells you what a button
  would save; it does not change it.
