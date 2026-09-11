# Waveform page — design

**Date:** 2026-09-10 · **Status:** approved by the owner in chat, not planned · **Repo:** `hindsight`

## Goal

A dedicated page for one take: zoom and scrub it precisely, manage its flags,
set one region, hear exactly what that region will become, and export it as a
new take. The owner's stated second priority after flags, and the first step
toward slicing and splicing.

The owner's framing: bento's slicer is a starting point, not code to lift. Its
behaviour spec (one audio clock, an explicit grid, never fight a drag) is the
target; its Go primitives are copied where useful, never depended on (see the
flags spec for why a dependency is off the table).

## What the owner decided

| Question | Decision |
|---|---|
| Core job v1 | **Find and cut** — zoom, scrub, flags, one region, audition, export |
| Where a cut goes | **A new take beside the original**, source untouched |
| Device | **Phone first, desktop works** |
| Processing on export | **Declick fades only**, ~3ms per edge, no level change |
| Beat grid | **Lines from the take's BPM and a draggable downbeat, no snap** |
| Approach | **A**: custom canvas + on-demand range peaks + Web Audio audition |

Rejected: a WaveSurfer page zooming one full-resolution peaks array (does not
scale to a 15-minute take on a phone; regions plugin not vendored), and
server-rendered PNG tiles (a dead end for interaction).

## Facts this design rests on

Read out of the code on 2026-09-10.

- **Takes are 32-bit integer PCM, canonical 44-byte header, 2 channels** by
  default (`WriteWAV`, `wav.go`). A 15-minute take is ~345MB, so the browser
  can never decode a whole take; every zoomed view must come from the server.
- **Peaks are a fixed 1024-bucket file** written at save (`peakBuckets`,
  `WritePeaks`). `PeakData` is `{version, channels, sample_rate, duration,
  buckets, data: [[min,max,...] per channel]}`. `GET /api/peaks?file=` serves
  that file verbatim.
- **Trim is stored but inert.** `Meta.Trim {start_frame, end_frame}` round-trips
  through `PATCH /api/take`, and nothing draws it or applies it on download.
- **Flags with labels work end to end** (branch `flag-labels`): sidecar,
  PATCH, RIFF `cue ` + `LIST/adtl`.
- **BPM lives in the sidecar** as `*float64`, from the MIDI clock at save,
  editable. Absent means no tempo.
- **`Meta.Version` is 1** and `WriteMeta` refuses to overwrite a newer version.
  Adding optional fields does not need a bump.
- **`safeMediaPath`** already validates a `file=` name and confines it to the
  output directory. Every new endpoint uses it.
- **The preview MP3** is a stereo mix made by ffmpeg after save; `<audio>` can
  stream it via `/api/download?file=<preview>`.
- **The service worker precaches a fixed `SHELL` list**; a new page and its
  modules must be added there and the cache version bumped, or offline breaks
  silently.
- **There is no JavaScript test harness** in the repo.

## Architecture

Three server additions, one new page. Nothing in the capture path changes.

### 1. Range peaks — `GET /api/peaks`

Existing parameters unchanged. New optional parameters:

| Param | Meaning |
|---|---|
| `from` | first frame, inclusive, `>= 0` |
| `to` | last frame, exclusive, `<= file frames` and `> from` |
| `buckets` | bucket count, `1..4096` |

All three must be given together. With them, the handler computes min/max per
channel per bucket over exactly `[from, to)` by seeking to those bytes in the
data chunk and reading them once; it never reads outside the range. The
response has the same shape as the peaks file, with `duration` describing the
range and a new `from` field carrying the first frame back, so one renderer
draws both. The last bucket absorbs any remainder frames.

The coarse whole-take view always uses the file; the client only asks for
ranges when zoomed in past the file's resolution, so a range request reads at
most a viewport's worth of audio. No server-side cache: the client caches
tiles.

Errors: 400 for a missing, inverted, or out-of-range window or a bad bucket
count; 404 when the take is gone.

### 2. Cut — `POST /api/cut?file=`

Body: `{"start_frame": n, "end_frame": n, "label": "optional"}`.

Validation: frames within the file; `end - start >= 2 * fadeFrames + 1`;
the same `MinFreeGB` guard as `Saver.Save`, returning 507 like save does.

The handler streams the range from the source WAV in blocks, applies a linear
fade-in over the first 3ms and a fade-out over the last 3ms (integer math on
the int32 samples, matching what bento's `ApplyBoundaryFades` does), and
writes the new take through the same steps as a save: `writeWAVHeader` and
the peak accumulator, `WritePeaks`, sidecar, `stampFlags`, then `makePreview`
in the background. The audio between the fades is byte-identical to the
source. Nothing is held in memory beyond one block.

Naming: `jam_<now>.wav` in the same timestamp format as a save. The list
already sorts by creation time and `RemoveTake` keys on the name, so a cut is
an ordinary take in every path that exists.

Sidecar of the cut:

| Field | Value |
|---|---|
| `label` | body label, sanitized; else `<source label or stem> cut` |
| `bpm` | copied from the source, if present |
| `flags` | source flags with `start <= frame < end`, rebased to `frame - start`, labels kept |
| `source` | new optional field `{name, start_frame, end_frame}` for lineage |
| `starred`, `trim`, `downbeat_frame` | not copied |

Response: `200 {"name": "jam_….wav"}`. The client refreshes the list and
offers a link to the new take. The source take is never modified by a cut.

The cut does not read the source's `trim`; it takes the body's frames. That
lets the page export without a round trip to save the region first, and lets
a script cut any range.

### 3. Audition slice — `GET /api/slice?file=&from=&to=`

Returns the range as a WAV, **16-bit PCM**, with the same fades the cut
applies, so what the owner hears is exactly what the cut will be. 16-bit
because browsers, iOS Safari in particular, are unreliable at decoding 32-bit
integer WAV through `decodeAudioData`; the audition is not archival.
Conversion is a shift, no dither.

Capped at 60 seconds (`to - from <= 60 * sample_rate`; 400 beyond). A 60s
stereo slice decodes to ~23MB of float32 in the browser, fine on a phone.
Regions longer than that audition through the MP3 preview instead (see the
clock, below), which is coarser but never fails.

Streamed with `Content-Length` set from the frame count so the browser can
show progress. Not cached on the server.

### 4. The page — `/wave.html?file=<take>`

A separate HTML page, not a route inside `app.js`: the main page's module
graph, poll loop and wake lock have nothing to do with editing, and a
separate page keeps the service worker's precache list additive.

Modules under `web/static/lib/wave/`:

| Module | Owns | Depends on |
|---|---|---|
| `geometry.js` | pure math: frame↔pixel, tile ladder and selection, grid line placement, bar.beat formatting | nothing |
| `tiles.js` | tile cache and fetching, backoff, coarse fallback | `geometry.js`, fetch |
| `view.js` | the canvas: drawing, gesture recognition, region handles, flag hit-testing; emits events | `geometry.js`, `tiles.js` |
| `clock.js` | playback: the MP3 `<audio>` element and the Web Audio region loop behind one `position()` | Web Audio, `/api/slice` |
| `page.js` | controller: loads the take, wires view ↔ clock ↔ API, transport and region rows, flag sheet, export | all of the above |

Each module can be understood from its exports without reading the others;
`geometry.js` has no DOM and no fetch so it is testable in Node.

Layout, phone first (~390px), top to bottom:

1. Top bar: back to the list, the take's label or stem, its BPM if any.
2. The canvas, ~40vh, full width. Waveform, grid, flags with label chips,
   the region with two handles, the cursor.
3. Transport row: play/pause, a **Loop region** toggle, position readout as
   `bar.beat` when there is a BPM plus `m:ss.mmm` always.
4. Region row: start and end readouts, nudge buttons (−/+ one beat when
   there is a BPM, else −/+ 10ms; a long press repeats), a **Region** button
   that creates one, and **Export**.

Desktop gets the same layout at a larger canvas, plus keyboard: space
play/pause, `L` loop toggle, `F` add flag at cursor, `[`/`]` set region start
and end at the cursor, `+`/`-` zoom, arrows pan.

Gestures on the canvas:

| Gesture | Effect |
|---|---|
| single-finger drag on waveform | pan |
| pinch | zoom about the pinch midpoint |
| tap | seek |
| double-tap | add a flag at that frame (same as the list) |
| tap a flag tick or chip | open the flag sheet: label input, Delete, Done |
| drag a region handle | move that edge; the other edge stays; edges cannot cross and stop `2 * fade + 1` frames apart |
| drag inside the region | move the whole region |
| drag the downbeat marker | move the downbeat |

Handles have a 48px-wide hit area regardless of zoom, drawn as a thin line
with a grip tab above the waveform so they never hide the audio. A drag
never snaps or reverts: what the finger left is what stays (bento P-3).

Region creation via the **Region** button: four beats centered on the cursor
when there is a BPM, else the visible span. If a region already exists the
button reads **Clear**.

Persistence: on handle release, and on Clear, the region is written to the
sidecar's existing `trim` field through `PATCH /api/take` (debounced 300ms).
On load, an existing `trim` becomes the region. The downbeat is written the
same way to a new optional `downbeat_frame` sidecar field. Flags use the
existing full-replacement `flags` patch.

Export: calls `POST /api/cut` with the region and the source label; disables
the button during the request; on success toasts "Saved as <name>" with a
link to `/wave.html?file=<name>`; on failure toasts the server's error and
keeps the region.

### 5. Tiles and drawing

Zoom is continuous, expressed as frames per CSS pixel. Tiles live on a ladder
of levels where level `k` has `2^k` frames per bucket, `k >= 0`, each tile
holding 1024 buckets, so tile `i` at level `k` covers frames
`[i * 1024 * 2^k, (i+1) * 1024 * 2^k)`. For a given zoom, the client picks
the finest level with at most two buckets per device pixel and requests the
tiles that intersect the viewport, plus one each side. The cache is a map
keyed `k:i`, bounded to 256 tiles, evicting least recently drawn. A level
whose bucket size is coarser than the file's own 1024-bucket peaks is never
fetched: the file serves as the top of the ladder.

While a tile loads, the coarsest cached ancestor (ultimately the file) draws
stretched in its place, so the view is never blank and zooming in refines
rather than flashes. Fetch failures retry with exponential backoff from 500ms
to 8s and keep the fallback; a 404 (take deleted) stops all fetching and
shows a "take is gone" state with a link back.

Drawing, per frame, on a device-pixel-ratio-aware canvas: background, bar
lines then beat lines (beat lines hidden below 8px apart, bars below 4px),
one min/max column per pixel per channel (two channels stacked), the region
as a translucent band with its handles, flag ticks with label chips
(chips collide-avoided by hiding the later one when they overlap), the
downbeat marker, the cursor. Redraw is requested through
`requestAnimationFrame` and coalesced.

Grid: bar and beat positions are `downbeat + n * frames_per_beat` with
`frames_per_beat = sample_rate * 60 / bpm`, four beats to a bar. Without a
BPM the grid, bar.beat readouts, and beat nudges are absent, not zero.

### 6. One clock

`clock.js` exposes `position()` in frames, `play()`, `pause()`,
`seek(frame)`, `setLoop(region | null)`, and a `tick` event. Two engines
sit behind it and exactly one is active:

- **Preview engine**: an `<audio>` element on the take's MP3. Used for plain
  play from the cursor, and for looping a region longer than the slice cap,
  by seeking back to the region start when `currentTime` passes its end.
- **Slice engine**: fetches `/api/slice` for the region, decodes it into an
  `AudioBuffer`, and plays it through an `AudioBufferSourceNode` with `loop`
  set, so the loop point is sample-exact. Position derives from
  `AudioContext.currentTime` minus the start time, modulo the loop length,
  plus the region start.

Turning **Loop region** on with a region under the cap switches to the slice
engine; the preview engine is paused first. Changing the region while looping
refetches the slice, playing the old one until the new one is decoded so
there is no gap of silence. Turning it off, or clearing the region, pauses and
returns to the preview engine at the same position. Every consumer, the
cursor, the readouts, keyboard shortcuts, reads `position()`; nothing keeps
its own time.

The MP3 preview is a lossy stereo mix, so its position is accurate to a
frame of the encoder's granule; that is acceptable for scrubbing and is why
the slice engine exists for the region.

### 7. Error handling

Server: every new endpoint validates through `safeMediaPath`, returns 404
for a missing take, 400 for bad frames or body, 507 on the disk guard, and
logs but does not fail a cut whose preview or peaks step fails, mirroring
save. A cut that fails mid-write removes its partial WAV, as save does.

Client: the page loads the take's entry from `/api/jams` and its peaks file
before drawing; if either fails it shows the error and a link back. Tile
failures degrade to the fallback. A failed sidecar patch (region, downbeat,
flags) toasts and leaves the in-memory state as the user set it, so a retry
is a matter of touching the control again. Web Audio requires a user gesture
to start on iOS; the context is created on the first play tap, and if
`decodeAudioData` rejects the slice the clock falls back to the preview engine
and toasts once.

## Testing

Go, in `internal/audio` and `internal/api`:

- Range peaks over the whole file at 1024 buckets equal the peaks file
  bucket for bucket; a sub-range's buckets equal the same computation over
  a copy of just that range; bounds and bucket-count validation.
- Cut: samples between the fades are byte-identical to the source; the
  first and last 3ms are scaled by the expected ramp; a region shorter than
  the minimum is 400; the disk guard is 507; flags inside the range are
  rebased and labelled, flags outside are absent; BPM and `source` are set;
  `starred` and `trim` are not copied; a mid-write failure leaves no file.
- Slice: 16-bit output, correct length and header, same fades as cut, cap
  enforced.
- Sidecar: `downbeat_frame` and `source` round-trip; a version-1 sidecar
  without them still reads.

JavaScript: `geometry.js` gets `node --test` coverage for frame↔pixel,
tile ladder selection at several zooms, tile index ranges with the one-tile
margin, grid line placement including the hide thresholds, and bar.beat
formatting. This introduces `node --test` as the repo's first JS test
runner; CI runs it beside `go test`.

By hand, against `--demo` in a browser with a touch-emulating viewport: every
gesture in the table, the loop switch between engines, export producing a
listed take whose waveform matches the region, and offline load of the page
after one online visit.

## Out of scope

- Multiple regions, snap-to-grid, region length locking.
- Normalize, gain, any processing beyond the declick fades.
- Applying `trim` at download time.
- Changing the list's row waveforms or WaveSurfer.
- Any change to capture, the ring, or the live flag store.
- Transient detection, key detection, MPC `.xpm` export.
