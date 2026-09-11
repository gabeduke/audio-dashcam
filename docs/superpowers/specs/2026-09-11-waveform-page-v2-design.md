# Waveform page v2 — the late-night share flow

**Date:** 2026-09-11 · **Status:** approved by the owner in chat, not planned · **Repo:** `hindsight`

## Goal

Make the waveform page something a musician uses on a phone, on the couch,
half-watching a game, to grab the good bit of tonight's take and text it to
his brother before bed. Every control on the default screen must earn its
place in that flow; everything else hides behind one disclosure.

This supersedes the interaction model of the v1 page
(`docs/superpowers/specs/2026-09-10-waveform-page-design.md`). The server
primitives, tile cache, and playback clock from v1 stay; the page, the view's
gestures, and the layout change.

## What the owner said about v1

- The two unlabeled −/+ pairs (region start and end nudges) are baffling.
- The region row wraps badly on a phone once timestamps appear.
- Pinch-to-zoom works on the phone; on desktop there is no obvious way to zoom.
- "Loop regions" should just happen.

## What the owner decided

| Question | Decision |
|---|---|
| Share target | **An MP3 of the region**; the whole take when there is no region |
| Region creation | **Drag on the waveform** |
| Looping | **On by default** when a region exists; no toggle |
| Share | **One tap** into the phone's share sheet |
| Fine controls | **Hidden** behind a disclosure |
| Navigation model | **Approach A**: an overview strip plus drag-select |

Rejected: a Select/Navigate mode toggle (a mode is the thing that makes
people think); long-press to select (invisible, and fights the iOS text
callout).

**Amended 2026-09-11 after phone testing.** Drag-select on the phone was a
hair trigger: every scrub and every thumb-wobble left a region behind. The
owner asked for touch-and-hold instead, and that is now the model. A press
that holds still for 350ms turns into a region drag, announcing itself with a
band under the finger — which answers the "invisible" objection above — and
the iOS callout is suppressed in CSS (`-webkit-touch-callout`/`user-select` on
`.wave-canvas`), which answers the other. A plain one-finger drag goes back to
**panning**, the gesture a waveform invites; tap still seeks, double-tap still
flags, and the overview strip stays for coarse navigation.

## Facts this design rests on

Read out of the code on 2026-09-11.

- **The brother is not on the tailnet.** Sharing a link to the Pi is useless;
  sharing must hand the phone a file. The Web Share API accepts files on iOS
  Safari 15+ and Android Chrome, in a secure context only. The install guide
  already puts the Pi behind `tailscale serve` for HTTPS; on the plain LAN
  address the page falls back to a download.
- **The preview MP3 is made by ffmpeg** in `MakePreview` (`save.go`):
  `nice -n 10 ffmpeg -i <wav> [pan] -c:a libmp3lame -b:a 128k -f mp3 <tmp>`,
  with a `pan` filter when the take has more than two channels. The render
  endpoint reuses those arguments.
- **`ReadFrames` + `applyFades` + `FadeFrames`** already define the cut's 3ms
  declick; the render must sound identical, so it fades the same way.
- **`view.js` gestures today:** one-finger drag on empty waveform = pan; pinch
  = zoom; tap = seek; double-tap = flag; handles resize; region body moves;
  wheel pans, ctrl-wheel zooms. Hit-test order: chips → downbeat → handles →
  region → flags → wave.
- **`page.js` today** owns `loopOn` and an `applyLoop(region)` that awaits
  `clock.setLoop` and then reads `clock.loop` back, because `setLoop` resolves
  even when it fell back to the preview engine. That function stays; the
  toggle goes.
- **`clock.js`** already loops a region through the slice engine (under 60s)
  or the preview engine (over); nothing there changes.
- **The file peaks** (1024 buckets) are fetched before the page draws. The
  overview strip needs nothing more.

## Architecture

One new server endpoint, one new client module, and a reworked page. The
tile cache, clock, geometry, and every v1 server primitive are unchanged.

### 1. Navigation

**Overview strip.** A second canvas, 36px tall on the phone, above the main
waveform. It draws the whole take from the file peaks, the region as a
band, flags as ticks, the cursor, and the current viewport as a translucent
window with a 1px outline. The window is never narrower than 24px, so it
stays grabbable on a 15-minute take.

Gestures on the strip:

| Gesture | Effect |
|---|---|
| drag the window | pan the main view; the window follows the finger by its grab offset |
| tap outside the window | center the main view on that frame, keeping the zoom |
| double-tap | fit the whole take |

**Main waveform.** Pinch zooms about the pinch midpoint, unchanged. A
two-finger drag with unchanging spread is the same gesture with a moving
midpoint and pans, which pinch already does; no new code, but the spec
names it so the checklist tests it. One-finger drag no longer pans; see §2.

**Desktop.** Plain wheel zooms about the pointer; shift-wheel, or a
horizontal wheel from a trackpad, pans. The three zoom buttons are removed.
Keyboard: `+`/`-` zoom, arrows pan, `0` fits, unchanged otherwise.

### 2. Region and loop

One-finger gestures on the main waveform, by where the press lands:

| Press lands on | Drag does | Tap does |
|---|---|---|
| empty waveform, no region | creates a region from the press frame to the release frame | seeks |
| empty waveform, region exists | creates a new region, replacing the old | seeks |
| inside the region | moves it | seeks |
| a handle | resizes that edge | nothing |
| a flag tick or chip | nothing | opens the flag sheet |
| the downbeat marker | moves it | nothing |

A drag that creates a region emits `regionChange` with `final: false` as it
grows and `final: true` on release, so the page can draw it live and save it
once. A region under `2 * fade + 1` frames on release is discarded rather
than created, so a slightly-moved tap does not leave a sliver.

Double-tap still adds a flag. The grab-offset rule from v1 holds: nothing
snaps to the pointer on the first move.

**Loop is implicit.** When the page's region becomes non-null, the page
calls `applyLoop(region)`; when it becomes null, `applyLoop(null)`. Play
starts whatever the clock is set to: the loop when there is a region, the
take from the cursor otherwise. The Loop button and `loopOn` are removed.
The region still autosaves to the sidecar's `trim` field, so it survives a
reload and comes back looping.

**Clearing.** The region text in the bottom row carries an × that clears
the region; Delete region under Fine tune does the same. Clearing returns
to plain playback at the cursor, as `clock.setLoop(null)` already does.

### 3. Layout

Phone, top to bottom, all widths stable so a changing number never reflows
a row:

1. **Top bar:** back, the take's label or stem, its BPM.
2. **Overview strip**, 36px.
3. **Main waveform**, about 36vh, minimum 160px.
4. **Action row:** `Play` · region text · `Share`. The region text is a
   monospace span of fixed width showing `0:12.3 – 0:41.8 ×` or
   `whole take`. Share is the primary button.
5. **Fine tune** disclosure, collapsed by default, remembered per browser in
   `localStorage`. Inside, a two-column grid:
   - `Start` with `−` `+` and `End` with `−` `+`; one beat when there is a
     BPM, else 10 ms; hold to repeat, as v1.
   - Position readout: `bar.beat` when there is a BPM, `m:ss.mmm` always.
   - `Downbeat: drag the marker on the waveform` as a hint line, and a
     `Reset` button that clears `downbeat_frame`.
   - `Export as take` — writes a real cut for the DAW hand-off, exactly
     v1's Export.
   - `Delete region`.

Desktop is the same layout at a wider canvas (main waveform 55vh). The
flag sheet is unchanged.

### 4. Share

**Endpoint:** `GET /api/render?file=&from=&to=`

Streams an MP3 of frames `[from, to)` straight from ffmpeg's stdout to the
response. Nothing is written to disk. Parameters and validation match
`/api/slice`: `file` through `safeTakeName`, integer `0 <= from < to <=
frames`, 404 for a missing take, 400 for a non-32-bit take. Cap:
`to - from <= 600 * sampleRate` (10 minutes); 400 beyond it with the
message "region longer than 10 minutes".

ffmpeg arguments, in order:

```
nice -n 10 ffmpeg -hide_banner -loglevel error -i <wav>
  -af "atrim=start_sample=<from>:end_sample=<to>,asetpts=PTS-STARTPTS,afade=t=in:st=0:d=0.003,afade=t=out:st=<dur-0.003>:d=0.003"
  [pan, as MakePreview]
  -c:a libmp3lame -b:a 128k -f mp3 -
```

`atrim` by sample is frame-exact, which is why the trim is a filter rather
than `-ss`/`-t`. The two `afade`s reproduce the cut's 3ms linear declick;
`d=0.003` at 48kHz is 144 samples, the same as `FadeFrames`. When
`MakePreview` would add a `pan` filter, it is appended to the same
`-af` chain after the fades.

Headers: `Content-Type: audio/mpeg`, `Content-Disposition: inline;
filename="<name>"`, no `Content-Length` (the stream's length is unknown),
`Cache-Control: no-store`. The name is `<label or stem> <m.ss>-<m.ss>.mp3`
with the label sanitized to filename-safe characters (letters, digits,
space, dash, underscore, dot); the whole take is `<label or stem>.mp3`.

The handler pipes stdout to the response with `io.Copy`, kills ffmpeg if
the client disconnects (`r.Context()`), and logs stderr on a non-zero exit.
A failure after the first byte cannot change the status; the client sees a
truncated stream, which the share sheet rejects and the page reports.

Concurrency: renders run under the same `nice -n 10` as previews. There is
no queue; two phones rendering at once is fine on the Pi 5.

**Client.** The Share button:

1. Reads "Share" in a secure context where `navigator.canShare` exists and
   reports files shareable; otherwise "Download" from the start.
2. On tap, disables itself, reads "Rendering…", and fetches
   `/api/render` for the region, or the whole take if there is none.
3. On success, builds a `File` from the blob with the server's filename
   and `audio/mpeg`, then calls `navigator.share({ files: [file], title })`.
   A rejected share (the user cancelled) is silent. On the download path it
   creates an object URL, clicks a hidden anchor with `download` set, and
   revokes the URL.
4. Restores the label either way.

The share sheet is what hands the file to Messages, AirDrop, WhatsApp, or
anything else; the page never knows or cares which.

### 5. Modules

| Module | Change |
|---|---|
| `internal/api/api.go` | `handleRender`, route `GET /api/render` |
| `internal/audio/render.go` (new) | `RenderMP3(ctx, w io.Writer, cfg, path, from, to) error` builds the argument list and runs ffmpeg; `renderArgs(...)` is a pure function so the argument list is testable without ffmpeg |
| `web/static/lib/wave/overview.js` (new) | `class Overview { constructor({canvas, filePeaks, totalFrames, getState, getView, emit}); draw(); destroy() }` emitting `panTo {start}`, `centerOn {frame}`, `fitAll {}` |
| `web/static/lib/wave/view.js` | one-finger drag on empty waveform becomes `select` (new gesture kind); the `pan` kind is removed; wheel semantics flip (plain zooms, shift pans); a `select` drag below the minimum length on release emits nothing |
| `web/static/lib/wave/page.js` | wires the overview; implicit loop; the action row; Fine tune; Share |
| `web/static/wave.html`, `styles.css` | the new layout; fixed-width region text; the disclosure |
| `web/static/sw.js` | `SHELL` gains `/lib/wave/overview.js`; `CACHE` → `hindsight-shell-v4` |
| `docs/api.md`, `README.md` | the render endpoint; the page description |

`geometry.js`, `tiles.js`, `clock.js`, and every v1 server primitive are
untouched.

### 6. Errors

- Render 400/404/500 → toast the server's message, keep the region, restore
  the button.
- A truncated render (ffmpeg died mid-stream) → the share sheet or the
  download yields a broken file; the client checks the blob's size is over
  1KB and its first bytes are an MP3 frame sync or ID3 tag before sharing,
  and toasts "render failed, try again" otherwise.
- Share cancelled by the user → nothing.
- `navigator.share` throws for a reason other than AbortError → fall back
  to the download path once, then toast.
- Region shorter than the minimum on drag release → not created; no toast,
  because the user will see nothing happened and drag again.

## Testing

Go:

- `renderArgs` produces exactly the argument list above for a 2-channel
  take, and appends the pan filter for an 8-channel take with save channels
  1,2; the fade-out start is `(to-from)/sampleRate - 0.003`.
- `handleRender`: validation table (bad file, non-integer, inverted, past
  end, over the cap, missing take, non-32-bit) and the headers on the
  success path.
- An integration test that runs the real endpoint against a 2-second test
  take, skipped with `t.Skip` when `ffmpeg` is not on `PATH`, asserting the
  body starts with an ID3 tag or an MP3 sync word and is at least 4KB.

JavaScript (`node --test 'web/static/lib/wave/*.test.js'`):

- Overview mapping: window rectangle for a given view and total; the 24px
  minimum; grab-offset drag → `panTo` start; tap → `centerOn`; both clamped.
  The overview's math lives in exported pure functions in `overview.js` and
  the test imports those, not the class.
- Region text formatting `0:12.3 – 0:41.8` and `whole take`.
- The MP3 sniff (`looksLikeMP3(bytes)`), as a pure function.

By hand, on a phone over `tailscale serve` and on desktop with the demo:
every gesture in the two tables; pinch, two-finger pan, overview drag and
tap; loop starting the moment a region exists and stopping on ×; Fine tune
collapsing and remembering; Share on the phone landing in Messages as a
playable MP3; Share on desktop downloading; the plain-HTTP button reading
Download; Export as take still producing a listed cut. Then the scenario
itself, end to end, on the couch.

## Out of scope

- Multiple regions, snap, region length locking.
- Normalize, gain, any processing beyond the declick fades.
- A display-only "fit to peak" toggle for quiet takes (separate small
  change if wanted).
- Any change to the takes list, capture, or the live ring.
- Video, waveform images, or anything but an MP3 in the share sheet.
- Queueing or rate-limiting renders.
