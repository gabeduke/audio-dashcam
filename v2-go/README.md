# Audio Dashcam (Go)

An always-listening audio buffer for a Raspberry Pi. It continuously captures
the stereo master from a USB interface into a memory ring; pressing Capture in
the web UI writes the last N seconds to disk. Nothing is recorded to disk until
you ask for it.

## How it works

```
EP-136 (USB, 8ch)
   │  PortAudio callback — computes level bins, hands blocks off, never blocks
   ▼
free/filled block pool ──► ring writer goroutine ──► Ring (RING_SECONDS)
   │                                                   │
   │ Levels (10ms min/max/RMS bins)                    │ Snapshot(seconds)
   ▼                                                   ▼
WebSocket /api/live ──► live waveform + meters    WAV ─┬─► mp3 preview
                                                       └─► .peaks.json
```

The audio callback only computes levels and hands the block to a pooled
channel. A single writer goroutine owns the ring, so a capture can memcpy a
snapshot out under a mutex without ever stalling the device.

## Configuration

Everything is environment driven — see `deploy/dashcam.env.example`. The values
that matter most:

| Variable | Default | Notes |
|---|---|---|
| `RING_SECONDS` | `900` | Buffer length and the maximum capture. Dominates RAM: `seconds x 48000 x channels x 4` bytes, so 900 is ~1.3 GB resident and ~2.6 GB while a full capture is copied out |
| `SAVE_CHANNELS` | `3,4` | 1-indexed pair holding the stereo master |
| `SAVE_ALL_CHANNELS` | `false` | Write all channels instead (4x the disk) |
| `MIN_FREE_GB` | `1.0` | Captures are refused below this, with a 507 |
| `MAX_SAVES` | `0` | Prune oldest takes beyond this count; 0 disables |
| `INPUT_LATENCY_MS` | `100` | Do not lower this — see below |

### Finding the right SAVE_CHANNELS

The interface presents 8 channels but the master is only on one pair. Open the
UI, expand **Input channels**, and play something: the pair that moves is the
one you want. On this rig it is 3 & 4 — channels 1 & 2 carry roughly 15 dB of
bleed and 5–8 are digital silence.

This matters for previews too. `ffmpeg -ac 2` on an 8-channel file assumes a
7.1 layout, folding channel 3 into a mono centre and discarding channel 4 as
LFE. Previews are built with an explicit `pan` map instead.

### Why INPUT_LATENCY_MS is not small

`portaudio.LowLatencyParameters` asks a USB device for a deadline it cannot
meet, and PortAudio then busy-polls: on this Pi that burned 84% of a core
continuously. A ring buffer has no latency requirement, so the input latency is
set explicitly and generously. Current draw is around 1% of a core.

## API

| Endpoint | Purpose |
|---|---|
| `GET /api/status` | Health, ring fill, per-channel dB, disk |
| `GET /api/live` | WebSocket: min/max peak bins (~100/s) + peak-hold |
| `POST /api/trigger?seconds=N` | Save the last N seconds; `0` = whole ring. 507 if disk is low |
| `GET /api/jams` | Takes, newest first. Sends an ETag |
| `GET /api/peaks?file=` | Precomputed waveform, so phones don't download audio to draw one |
| `GET /api/download?file=[&dl=1]` | Stream inline, or force a download |
| `DELETE /api/delete?file=` | Remove a take and its sidecars |

## The UI

Mobile-first, no build step, no npm. Vanilla ES modules plus a vendored copy of
WaveSurfer.js for scrubbing takes.

**Installing it on a phone.** The Pi serves plain HTTP, which constrains what
is possible:

- **iOS** — Share → Add to Home Screen gives a real standalone app, icon and
  all. This works today.
- **Android** — Chrome will only offer a full install over HTTPS; on plain HTTP
  you get a shortcut that opens in the browser.
- **Service workers do not register over plain HTTP at all.** `sw.js` ships and
  registration is guarded behind `isSecureContext`, so it starts working by
  itself if TLS is ever added. Offline caching is close to pointless anyway —
  the Pi is the server.

## Building

PortAudio is cgo, so build on the Pi (or with a matching cross toolchain):

```bash
cd ~/audio-dashcam/v2-go && go build -o v2-go-bin .
```

## Deploying

`./deploy.sh` from a workstation rsyncs the tree, builds on the Pi, and
restarts the service.
