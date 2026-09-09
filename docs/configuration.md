# Configuration

Hindsight is configured entirely through environment variables. Nothing needs a
rebuild. On a Pi they live in `~/hindsight/hindsight.env`, which the systemd
unit reads with `EnvironmentFile=-%h/hindsight/hindsight.env` — the leading `-`
means a missing file is not an error, because every value has a default.

`deploy/hindsight.env.example` is the annotated copy the installer writes.

## The variables in the example file

| Variable | Default | What it does |
|---|---|---|
| `RING_SECONDS` | `900` | Length of the memory ring, and therefore the longest possible capture |
| `DEVICE_MATCH` | `EP-136` | Substring matched against PortAudio device names to pick the input |
| `CHANNELS` | `8` | How many channels to open on that device |
| `SAMPLE_RATE` | `48000` | Capture sample rate, in Hz |
| `SAVE_CHANNELS` | `1,2` | 1-indexed channel pair written to a take |
| `SAVE_ALL_CHANNELS` | `false` | Write every channel instead of the pair above |
| `MIN_FREE_GB` | `1.0` | Refuse to save below this much free disk |
| `MAX_SAVES` | `0` | Keep at most this many takes, deleting the oldest. `0` disables pruning |
| `INPUT_LATENCY_MS` | `100` | Input latency requested from PortAudio. Do not lower it |

## The rest

These are real and occasionally useful, but they are not in the example file
because the defaults are almost always right.

| Variable | Default | What it does |
|---|---|---|
| `PORT` | `5000` | HTTP listen port |
| `OUTPUT_DIR` | `~/hindsight/jam_saves` | Where takes are written |
| `STATIC_DIR` | *(auto)* | UI directory. Resolved relative to the binary, then the working directory; set it only if you have moved `web/static` somewhere unusual |
| `FRAMES_PER_BUFFER` | `2048` | Frames per PortAudio callback |

`PORT` is the one you will actually reach for, because **macOS occupies 5000
with ControlCenter's AirPlay Receiver**, so running the demo on a Mac needs
`PORT=5173` or similar.

---

## `RING_SECONDS` and RAM

The ring holds raw interleaved 32-bit samples. There is no compression, and
none is wanted — this is the buffer a capture is copied out of.

```
bytes = seconds × 48000 × channels × 4
```

At the default 8 channels:

| `RING_SECONDS` | Resident |
|---|---|
| `30` | 44 MB |
| `120` | 176 MB |
| `420` | 615 MB |
| `900` | 1.3 GB |

**A full-ring save briefly doubles it.** The snapshot is copied out of the ring
before it is encoded, so 900 seconds peaks around 2.6 GB. That is comfortable
on an 8 GB Pi 5 and is not on a smaller one.

The UI offers 30s / 2m / 7m and a **Full** button, and hides any tier longer
than the ring. Shortening `RING_SECONDS` therefore quietly removes buttons: at
`RING_SECONDS=60` you get `30s` and `Full 1m` and nothing else.

## `SAVE_CHANNELS` and the pre-fader trap

An interface can present many inputs while the mix you want lives on exactly
one pair. Getting this wrong does not produce silence — it produces a take that
sounds plausible and is missing most of the band.

The EP-136 presents 8 inputs as four stereo record pairs. Teenage Engineering
does not document which USB pair is which, so it was measured on 2026-09-08 by
playing into mixer channel 1 and comparing levels with the fader up and down:

```
USB 1/2  MAIN      post-fader  -- moved 20.8 dB with the fader
USB 3/4  CH1 tap   pre-fader   -- did not move at all
USB 5/6  CH2 or AUX            -- silent in both runs, not disambiguated
USB 7/8  CH2 or AUX            -- silent in both runs, not disambiguated
```

**Use `1,2`.** A pre-fader tap ignores the mixer entirely: it records one
channel strip at that strip's own limiter ceiling, so the faders — and anything
plugged into the other inputs — are missing from the take.

`scripts/channel-probe.py` re-runs that measurement if the map is ever in
doubt; "Finding the right `SAVE_CHANNELS`" in
[install-raspberry-pi.md](install-raspberry-pi.md) shows how.

The value is 1-indexed because that is how hardware labels its inputs.
Internally it is stored zero-based, and `/api/status` reports it 1-indexed
again so the UI and the env file agree.

`SAVE_ALL_CHANNELS=true` writes every channel instead, which is useful for
stems and costs four times the disk. The mp3 preview still folds down to the
`SAVE_CHANNELS` pair.

## Why `INPUT_LATENCY_MS` is not small

`portaudio.LowLatencyParameters` asks a USB device for a deadline it cannot
meet, and PortAudio responds by busy-polling. On this Pi that burned **84% of a
core**, continuously, for no benefit.

A ring buffer has no latency requirement at all. Nothing downstream is
listening in real time; the meters are a display and the ring is fifteen
minutes deep. So the input latency is set explicitly and generously, and the
current draw is around 1% of a core.

Raising it further is harmless. Lowering it is how you get the 84% back.

## Disk and pruning

`MIN_FREE_GB` is a pre-flight floor, not a prediction. A save checks the free
space **as it stands** and refuses with HTTP 507 if it is already below the
threshold; it does not compare the threshold against the size of the write it
is about to make. With `MIN_FREE_GB=1.0`, 1.1 GB free and a 1.3 GB full-ring
save, the check passes and the write proceeds — and can fill the volume. Set it
comfortably above one full-ring take, not just above zero.

`MAX_SAVES` prunes in the background after a successful save. It keeps the
first `MAX_SAVES` takes in the order `/api/jams` lists them — starred first,
then newest first — and deletes the rest along with their sidecars. Starring a
take therefore keeps it out of the pruner's reach, until the starred takes
alone exceed `MAX_SAVES`.

Each take is a `.wav` plus up to three sidecars in the same directory: a
`_preview.mp3`, a `.peaks.json`, and a `.meta.json` holding the label, star,
trim and BPM. Deleting a take through the API removes all of them.
