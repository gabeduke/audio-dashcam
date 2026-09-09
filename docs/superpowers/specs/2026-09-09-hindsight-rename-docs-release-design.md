# Hindsight: rename, restructure, docs, installer, releases

**Date:** 2026-09-09
**Status:** approved design, pending implementation plan

## Context

The project is a public repo whose front door is a `PLAN.md` describing a
Python stack that was deleted in `0219975`. The only real documentation is
`v2-go/README.md` — good prose, in a directory nobody would look in, and stale
in three places. Nothing in the repo lets a stranger install it: the systemd
unit that `deploy.sh` restarts is not tracked, and neither is the apt list. No
CI, no tags, no releases, no licence.

The code itself is in good shape: 112 tests green, `go vet` and `gofmt` clean,
sound package boundaries, and the expensive lessons (PortAudio busy-polling,
ffmpeg's 7.1 downmix trap, the ALSA card scan) are written down.

This design covers making the project legible and installable by someone who
finds it, under a new name.

## Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Name | **Hindsight** | Says what the tool gives you: the last N minutes, in retrospect. Replaces `audio-dashcam`. |
| Layout | `cmd/` + `internal/` + `web/` | Conventional Go; `internal/` states these are not a public API. |
| Demo | Synthetic audio source | Makes the README's demo a real one-liner and unblocks screenshots without hardware. |
| Releases | On every merge to `master` | Auto CalVer tag, arm64 tarball, `CHANGELOG.md`. |
| Licence | MIT | Permissive, matches a hobby project meant to be copied onto someone else's Pi. |
| Branching | `midi-clock` merged to `master` first | The restructure moves every Go file; rebasing 13 commits across that is worse. Done: `master` is `f63b271`, pushed. |

## §1 — Rename and restructure

Module `github.com/gabeduke/audio-dashcam/v2-go` becomes
`github.com/gabeduke/hindsight`. The GitHub repo is renamed too; GitHub
redirects the old URL, so existing clones and links keep working.

```
hindsight/
├── cmd/hindsight/main.go          # was v2-go/main.go; gains flag parsing
├── internal/
│   ├── api/                       # was v2-go/api/
│   ├── audio/                     # was v2-go/audio/
│   ├── config/                    # was v2-go/config/
│   └── midi/                      # was v2-go/midi/
├── web/static/                    # was v2-go/static/
├── deploy/
│   ├── hindsight.env.example      # was deploy/dashcam.env.example
│   ├── hindsight.service          # NEW — the unit deploy.sh already assumes
│   └── install.sh                 # NEW
├── scripts/                       # unchanged; Python hardware probes
├── docs/
├── .github/workflows/
├── deploy.sh
├── CHANGELOG.md                   # NEW
├── LICENSE                        # NEW (MIT)
└── README.md                      # NEW
```

Names that change: binary `hindsight`, unit `hindsight.service`, install root
`~/hindsight/`, config `~/hindsight/hindsight.env`, `OUTPUT_DIR` default
`~/hindsight/jam_saves`.

Names that do **not** change, deliberately:

- `jam_saves/` and `GET /api/jams`. Renaming the endpoint breaks any installed
  PWA on next load, for no benefit.
- The tuning variables (`RING_SECONDS`, `SAVE_CHANNELS`, …) stay unprefixed.
  They are read from an app-specific env file, so a prefix buys nothing.
- `deploy.sh` reads `HINDSIGHT_HOST`, falling back to `DASHCAM_HOST`, so an
  existing untracked `deploy.local.env` keeps working.

`staticDir()` resolves in order: `$STATIC_DIR`, `<exe>/../web/static`,
`<exe>/static`, `<exe>/../static`, `~/hindsight/web/static`. A repo checkout
and an unpacked release tarball both resolve with no configuration.

This section is behaviour-preserving. The 112 existing tests staying green is
the proof.

## §2 — Demo mode

`main.go` currently `log.Fatalf`s when no input device offers ≥8 channels, so
the app cannot run anywhere but the rig. That blocks the README's demo, CI
smoke tests, and screenshots.

Introduce a seam in `internal/audio`:

```go
// Source is a live audio input feeding blocks into the capture pipeline.
type Source interface {
    Init() error                                  // process-wide setup
    Open(sink func([]int32)) (name string, err error)
    Close()
    Reset() error                                 // after a failed Open
}
```

Three implementations:

- `source_portaudio.go` (`//go:build cgo`) — wraps today's `paLifecycle`,
  `pickDevice`, `openStream`, `closeStream` verbatim. `Reset` is the existing
  `pa.Rescan()`. No behaviour change.
- `source_nocgo.go` (`//go:build !cgo`) — `Open` returns an error naming
  `--demo` as the alternative.
- `source_demo.go` (untagged) — synthetic generator.

`Capture.supervise()` keeps its exact structure (backoff, staleness watchdog,
restart) and calls the interface instead of the PortAudio functions. All
PortAudio imports move into the cgo-tagged file, so `CGO_ENABLED=0 go build`
succeeds and `go run ./cmd/hindsight --demo` needs no system libraries on any
platform. This is what makes the demo a genuine one-liner rather than
"first install portaudio".

**The synthetic signal.** A 4-bar loop at 96 BPM on the `SAVE_CHANNELS` pair:
decaying-sine kick on beats, filtered-noise hats on eighths, a bass line and a
chord pad, under an 8-bar amplitude arc so the ribbon shows real structure
rather than a flat band. The remaining channels carry low-level bleed, so the
"Input channels" panel looks like the hardware does. Generated from a
monotonic sample counter, not wall-clock, so screenshots are reproducible.

Demo mode also supplies a fixed 96 BPM tempo source; otherwise the tempo tile
reads `–` in every screenshot.

`cmd/hindsight` gains `--demo` and `--version`.

## §3 — Documentation

`README.md` is the front door and stays short: what it is, a screenshot, the
one-line demo, install on a Pi, hardware notes, a configuration summary, the
data-flow diagram, and links out. Depth lives in `docs/`:

| File | Contents |
|---|---|
| `install-raspberry-pi.md` | Full install, EP-136 wiring, finding `SAVE_CHANNELS`, optional nginx/Tailscale, migrating an existing `audio-dashcam` install |
| `configuration.md` | Every variable with its rationale — the RAM arithmetic, the `INPUT_LATENCY_MS` busy-poll story |
| `api.md` | All nine endpoints, including `PATCH /api/take` and `GET /api/envelope`, which today's table omits |
| `architecture.md` | The ring/callback design, the ffmpeg `pan` trap, the MIDI clock estimator |
| `development.md` | Build, test, `deploy.sh`, `deploy.sh --static`, the Python probes in `scripts/` |

`v2-go/README.md`'s prose is harvested into these and corrected. `PLAN.md` is
deleted — its roadmap is finished or obsolete, and the history is in git and in
`docs/superpowers/`.

`scripts/*.py` are documented as hardware diagnostics, not leftovers of the old
runtime.

**Constraint:** the repo is public and its history was once squashed to remove
home network details. No tracked file may contain the tailnet name, MagicDNS
hostnames, or LAN/CGNAT addresses. Docs use `$HINDSIGHT_HOST` and placeholders
such as `<pi-host>.<tailnet>.ts.net`.

## §4 — Installability

`deploy/hindsight.service`, a systemd **user** unit:

```ini
[Unit]
Description=Hindsight — always-listening audio buffer
After=sound.target network.target

[Service]
EnvironmentFile=-%h/hindsight/hindsight.env
ExecStart=%h/hindsight/bin/hindsight
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
```

`EnvironmentFile` replaces `run_dashcam.sh`'s only job, so that script is
deleted.

`deploy/install.sh`, run on the Pi against an unpacked release:

1. Check `aarch64` and a usable systemd user session; fail with a clear message.
2. `apt-get install -y libportaudio2 libasound2 ffmpeg` — runtime only; a
   prebuilt binary does not need `portaudio19-dev`.
3. Install `bin/` and `web/` under `~/hindsight/`.
4. Seed `~/hindsight/hindsight.env` from the example if absent; never overwrite.
5. Install the unit, `daemon-reload`, `enable --now`.
6. `loginctl enable-linger "$USER"` so it survives logout.
7. Detect an existing `audio-dashcam.service` and offer to stop/disable it and
   move `~/audio-dashcam/jam_saves` across.
8. Print the URL.

## §5 — Release CI

**`ci.yml`** — pull requests and pushes. Its `test` job is declared
`workflow_call`able so the release can reuse it rather than duplicate it. On
`ubuntu-latest` with `portaudio19-dev`: `gofmt -l` (fails on any output),
`go vet ./...`, `go test -race ./...`, then `CGO_ENABLED=0 go build`, boot
`--demo`, and assert `/api/status` returns 200. That last step is what stops
the demo path from rotting.

**`release.yml`** — push to `master`, skipped when the head commit contains
`[skip ci]`. It calls `ci.yml`'s `test` job and the build job `needs:` it, so a
red `master` cannot publish a release.

Built on an `ubuntu-24.04-arm` runner inside a `debian:bookworm` container.
The container is not incidental: Ubuntu 24.04 links glibc 2.39, and Raspberry
Pi OS Bookworm ships 2.36, so a binary built on the bare runner will not start
on the Pi. Building against the target distro removes the class of problem.

Steps: compute the tag `vYYYY.MM.DD.N` where `N` is one more than the number of
existing tags with that date prefix; build with
`-ldflags "-X main.version=$TAG"`; assemble
`hindsight_<tag>_linux_arm64.tar.gz`; write `SHA256SUMS`; prepend the commit
range to `CHANGELOG.md` and push it back with `[skip ci]`; publish the GitHub
release.

The tarball unpacks to a directory laid out as the installer expects, with
`install.sh` at its root so the documented sequence is unpack, `./install.sh`:

```
hindsight_<tag>_linux_arm64/
├── install.sh              # copied from deploy/install.sh
├── bin/hindsight
├── web/static/
├── deploy/hindsight.service
├── deploy/hindsight.env.example
└── README.md  CHANGELOG.md  LICENSE
```

`version` is surfaced on `GET /api/status` and by `--version`, so an installed
Pi can be identified.

**Assumptions to validate on the first run:** that `ubuntu-24.04-arm` hosted
runners are available to this public repo, and that `actions/setup-go` works
inside the container (it needs `git config --global --add safe.directory`).
If either fails, the fallback is `ubuntu-22.04-arm` on the bare runner —
glibc 2.35 is older than the Pi's 2.36, so the binary still runs.

## §6 — Screenshots

Captured locally in this session against `--demo`: PortAudio and ffmpeg are
both installed on the workstation, and everything is `localhost`, so there is
no hostname to leak. Playwright drives desktop, tablet, and phone viewports
after letting the ring fill enough for the ribbon to show structure, and
triggers a capture so the takes list is populated.

PNGs land in `docs/images/`; the capture script is committed so they are
reproducible. Screenshots are not regenerated in CI — committing binaries back
on every merge is noise for no gain.

## §7 — Correctness fixes folded in

- **`SAVE_CHANNELS` default.** `config.go` defaults to `3,4`, but the
  measurement recorded in the env example (2026-09-08, in the file §1 renames
  from `deploy/dashcam.env.example` to `deploy/hindsight.env.example`) shows USB
  `1/2` is the post-fader MAIN and `3/4` is a pre-fader CH1 tap — which ignores
  the mixer entirely. A fresh install with no env file therefore records the
  wrong pair. Default becomes `1,2`, with the comment citing the measurement.
  No-op for the existing rig, whose env file sets it explicitly.
- `v2-go/README.md:44` asserts the opposite; the claim dies with the file.
- `/api/status` gains `version`.

## Testing

- The 112 existing tests stay green across the move — the restructure's proof.
- New: the demo source fills the ring and moves the meters; `staticDir()`
  resolution, table-driven across both layouts; the tag generator's daily
  rollover; the changelog renderer.
- CI smoke test as described in §5.
- Installer and release workflow are verified by running them.

## Out of scope

- Embedding `web/static` with `embed.FS`. It would break `deploy.sh --static`,
  which turns UI iteration from a minute into a second.
- Renaming `/api/jams` or `jam_saves/`.
- Automating nginx and Tailscale. Documented, not scripted — they are
  deployment choices, not part of the app.
- A JavaScript test harness. The established pattern is a throwaway Playwright
  script; changing that is its own decision.
