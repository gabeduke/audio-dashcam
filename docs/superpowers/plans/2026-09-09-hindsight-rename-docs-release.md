# Hindsight Rename, Docs and Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rename `audio-dashcam` to Hindsight, move it to a conventional Go layout, and make it something a stranger can install on their own Raspberry Pi — with a demo that runs without hardware, real documentation, screenshots, an installer, and automatic releases.

**Architecture:** The Go code moves to `cmd/hindsight` + `internal/*` + `web/static` under module `github.com/gabeduke/hindsight`. A new `audio.Source` interface splits the PortAudio device behind a `//go:build cgo` tag, so a `CGO_ENABLED=0` build with a synthetic signal generator runs anywhere. That unlocks the README demo, a CI smoke test, and reproducible screenshots. Releases are built on an arm64 runner inside a `debian:bookworm` container and published on every merge to `master`.

**Tech Stack:** Go 1.23, PortAudio (cgo), gorilla/mux, gorilla/websocket, gopsutil, ffmpeg (runtime), systemd user units, GitHub Actions, Playwright (screenshots only).

**Spec:** `docs/superpowers/specs/2026-09-09-hindsight-rename-docs-release-design.md`

## Global Constraints

- Module path is exactly `github.com/gabeduke/hindsight`. Internal packages are `github.com/gabeduke/hindsight/internal/{api,audio,config,midi}`.
- Go directive stays `go 1.23.0`.
- **The repo is public and its history was once squashed to remove home network details.** No tracked file may contain the tailnet name, MagicDNS hostnames, or LAN/CGNAT addresses. Docs use `$HINDSIGHT_HOST` and placeholders such as `<pi-host>.<tailnet>.ts.net`.
- Every task ends green: `gofmt -l .` prints nothing, `go vet ./...` passes, `go test ./...` passes.
- From Task 4 onward, `CGO_ENABLED=0 go build ./...` must also succeed.
- Binary `hindsight`; unit `hindsight.service`; install root `~/hindsight/`; config `~/hindsight/hindsight.env`.
- `jam_saves/` and `GET /api/jams` keep their names — renaming the endpoint would break installed PWAs.
- `sed -i` on macOS requires an argument: use `sed -i ''`.
- Commit messages end with:
  ```
  Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_015NBvK3VetFgPNaB767toLG
  ```

---

### Task 1: Move and rename the tree

Pure mechanical move. There is no new behaviour to drive with a test — **the existing 112 tests are the test**, and they must pass unchanged afterwards. Do not fix anything else in this task; a mixed diff makes the move unreviewable.

**Files:**
- Move: `v2-go/main.go` → `cmd/hindsight/main.go`
- Move: `v2-go/{api,audio,config,midi}` → `internal/{api,audio,config,midi}`
- Move: `v2-go/static` → `web/static`
- Move: `v2-go/go.mod`, `v2-go/go.sum` → repo root
- Move: `deploy/dashcam.env.example` → `deploy/hindsight.env.example`
- Delete: `v2-go/README.md`, `PLAN.md`, `run_dashcam.sh`

**Interfaces:**
- Consumes: nothing.
- Produces: the import prefix `github.com/gabeduke/hindsight/internal/` that every later task uses.

- [ ] **Step 1: Record the baseline test count**

```bash
go test ./... 2>&1 | tee /tmp/hindsight-baseline.txt
grep -c '^func Test' $(find . -name '*_test.go') | awk -F: '{s+=$2} END {print s" test functions"}'
```

Expected: `ok` for `api`, `audio`, `midi`; 112 test functions.

- [ ] **Step 2: Move the files**

```bash
mkdir -p cmd/hindsight internal web
git mv v2-go/main.go cmd/hindsight/main.go
git mv v2-go/api internal/api
git mv v2-go/audio internal/audio
git mv v2-go/config internal/config
git mv v2-go/midi internal/midi
git mv v2-go/static web/static
git mv v2-go/go.mod go.mod
git mv v2-go/go.sum go.sum
git mv deploy/dashcam.env.example deploy/hindsight.env.example
```

- [ ] **Step 3: Delete the files the docs task replaces**

`v2-go/README.md` has prose worth keeping. It is preserved in git — Task 10 harvests it with `git show f63b271:v2-go/README.md`. `PLAN.md` describes a Python stack deleted in `0219975`. `run_dashcam.sh` exists only to source an env file, which Task 8's unit does with `EnvironmentFile=`.

```bash
git rm -q v2-go/README.md PLAN.md run_dashcam.sh
rmdir v2-go
```

- [ ] **Step 4: Rewrite the module path**

Rewrite imports in `.go` files only. The `go.mod` module line must **not** go through this substitution — it would become `github.com/gabeduke/hindsight/internal`.

```bash
grep -rl 'github.com/gabeduke/audio-dashcam/v2-go' --include='*.go' . \
  | xargs sed -i '' 's|github.com/gabeduke/audio-dashcam/v2-go|github.com/gabeduke/hindsight/internal|g'
sed -i '' '1s|^module .*|module github.com/gabeduke/hindsight|' go.mod
head -1 go.mod
```

Expected: `module github.com/gabeduke/hindsight`

- [ ] **Step 5: Verify nothing references the old path**

```bash
grep -rn 'audio-dashcam/v2-go' --include='*.go' . ; echo "exit=$?"
```

Expected: no output, `exit=1`.

- [ ] **Step 6: Run the full suite**

```bash
gofmt -l . && go vet ./... && go test ./...
```

Expected: `gofmt` prints nothing; `vet` silent; `ok` for `internal/api`, `internal/audio`, `internal/midi`. Same three packages as the baseline.

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -F - <<'MSG'
Move the Go tree to a conventional layout under the name Hindsight

v2-go was a blue/green marker from when a Python implementation still
existed. It has outlived that, and Go tooling reads a /vN suffix as a major
version marker, which this is not.

Nothing here changes behaviour: the 112 existing tests are the guard, and
they pass unchanged. PLAN.md and run_dashcam.sh go with it -- the first
describes a Python stack deleted in 0219975, the second only sources an env
file, which a systemd EnvironmentFile= does directly.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_015NBvK3VetFgPNaB767toLG
MSG
```

---

### Task 2: Resolve the UI directory for both layouts

`staticDir()` hardcodes `~/audio-dashcam/v2-go/static` as its fallback and cannot be tested, because it calls `os.Executable()` directly. Three layouts must now work: an unpacked release (`~/hindsight/bin/hindsight` beside `~/hindsight/web/static`), the deploy.sh checkout on the Pi (identical shape), and a local `go run` from a repo checkout, where the binary is in a temp build directory and only the working directory locates the UI.

**Files:**
- Modify: `cmd/hindsight/main.go` (replace `staticDir` and `dirExists`)
- Create: `cmd/hindsight/main_test.go`
- Modify: `deploy.sh`
- Modify: `.gitignore`

**Interfaces:**
- Consumes: nothing from Task 1 beyond the moved paths.
- Produces: `resolveStaticDir(envDir, exePath, cwd, home string, exists func(string) bool) string` in `package main`.

- [ ] **Step 1: Write the failing test**

Create `cmd/hindsight/main_test.go`:

```go
package main

import (
	"path/filepath"
	"testing"
)

// existsIn returns a predicate reporting true for exactly the given paths,
// so a layout can be described without touching the filesystem.
func existsIn(paths ...string) func(string) bool {
	set := make(map[string]bool, len(paths))
	for _, p := range paths {
		set[filepath.Clean(p)] = true
	}
	return func(p string) bool { return set[filepath.Clean(p)] }
}

func TestResolveStaticDir(t *testing.T) {
	const home = "/home/pi"

	tests := []struct {
		name   string
		env    string
		exe    string
		cwd    string
		exists func(string) bool
		want   string
	}{
		{
			name: "STATIC_DIR wins over every layout",
			env:  "/srv/ui",
			exe:  "/home/pi/hindsight/bin/hindsight",
			cwd:  "/home/pi/hindsight",
			// Deliberately a real layout too: the override must still win.
			exists: existsIn("/home/pi/hindsight/web/static"),
			want:   "/srv/ui",
		},
		{
			name:   "unpacked release: web/static beside bin/",
			exe:    "/home/pi/hindsight/bin/hindsight",
			cwd:    "/home/pi",
			exists: existsIn("/home/pi/hindsight/web/static"),
			want:   "/home/pi/hindsight/web/static",
		},
		{
			name:   "flat layout: static/ beside the binary",
			exe:    "/opt/hindsight/hindsight",
			cwd:    "/",
			exists: existsIn("/opt/hindsight/static"),
			want:   "/opt/hindsight/static",
		},
		{
			name:   "go run from a checkout: only the cwd locates the UI",
			exe:    "/var/folders/T/go-build123/b001/exe/hindsight",
			cwd:    "/Users/dev/hindsight",
			exists: existsIn("/Users/dev/hindsight/web/static"),
			want:   "/Users/dev/hindsight/web/static",
		},
		{
			name:   "nothing found: the install root is the last word",
			exe:    "/usr/bin/hindsight",
			cwd:    "/tmp",
			exists: existsIn(),
			want:   "/home/pi/hindsight/web/static",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveStaticDir(tt.env, tt.exe, tt.cwd, home, tt.exists)
			if got != tt.want {
				t.Errorf("resolveStaticDir() = %q, want %q", got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./cmd/hindsight/ -run TestResolveStaticDir -v
```

Expected: FAIL — `undefined: resolveStaticDir`.

- [ ] **Step 3: Implement it**

In `cmd/hindsight/main.go`, replace `staticDir()` and `dirExists()` with:

```go
// staticDir resolves the UI directory. It is a thin wrapper so that the
// decision itself stays pure and testable.
func staticDir() string {
	exe, _ := os.Executable()
	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	return resolveStaticDir(os.Getenv("STATIC_DIR"), exe, cwd, home, dirExists)
}

// resolveStaticDir picks the UI directory from the layouts this ships in.
//
// Executable-relative candidates come first so an installed binary is never
// confused by whatever directory systemd happened to start it in. The
// working directory is consulted only afterwards, which is what makes
// `go run ./cmd/hindsight` work from a checkout -- there the binary lives in
// a temporary build directory with no UI anywhere near it.
func resolveStaticDir(envDir, exePath, cwd, home string, exists func(string) bool) string {
	if envDir != "" {
		return envDir
	}
	dir := filepath.Dir(exePath)
	candidates := []string{
		filepath.Join(dir, "..", "web", "static"), // release / deploy.sh
		filepath.Join(dir, "static"),              // flat
		filepath.Join(dir, "..", "static"),        // flat, one level down
		filepath.Join(cwd, "web", "static"),       // go run from a checkout
	}
	for _, c := range candidates {
		if exists(c) {
			return filepath.Clean(c)
		}
	}
	return filepath.Join(home, "hindsight", "web", "static")
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
go test ./cmd/hindsight/ -run TestResolveStaticDir -v
```

Expected: PASS, all five subtests.

- [ ] **Step 5: Point deploy.sh at the new layout**

Apply these edits to `deploy.sh`:

```bash
# host resolution — keep an existing deploy.local.env working
HOST="${HINDSIGHT_HOST:-${DASHCAM_HOST:-}}"
HOST="${HOST:?set HINDSIGHT_HOST (e.g. pi@hindsight.local), or put it in deploy.local.env}"
DEST="${HINDSIGHT_DEST:-${DASHCAM_DEST:-hindsight}}"

# agent bypass — same rename
if [ "${HINDSIGHT_NO_AGENT:-${DASHCAM_NO_AGENT:-}}" = "1" ]; then
  KEY="${HINDSIGHT_SSH_KEY:-${DASHCAM_SSH_KEY:-$HOME/.ssh/id_rsa}}"
```

The `--static` fast path becomes:

```bash
  echo "[*] syncing web/static only to $HOST:~/$DEST"
  rsync -az --delete -e "${SSH[*]}" ./web/static/ "$HOST:~/$DEST/web/static/"
```

The rsync excludes become `--exclude 'bin'` and `--exclude 'hindsight.env'` (replacing `v2-go/v2-go-bin` and `dashcam.env`), and the build and restart lines become:

```bash
"${SSH[@]}" "$HOST" "cd ~/$DEST && go build -o bin/hindsight ./cmd/hindsight"
"${SSH[@]}" "$HOST" "systemctl --user restart hindsight.service"
"${SSH[@]}" "$HOST" "systemctl --user is-active hindsight.service && \
  curl -fsS http://127.0.0.1:5000/api/status | head -c 200 && echo"
```

Also update the comment above the `--static` block: it names `main.go` and `staticDir()`, which are still accurate, but the path it cites is now `web/static`.

- [ ] **Step 6: Update .gitignore**

Replace the Go build output block:

```
# Go build output
/bin/
/vendor/
```

- [ ] **Step 7: Verify the script parses and the suite is green**

```bash
bash -n deploy.sh && echo "deploy.sh: syntax ok"
gofmt -l . && go vet ./... && go test ./...
```

Expected: `deploy.sh: syntax ok`, then a clean run.

- [ ] **Step 8: Commit**

```bash
git add cmd/hindsight/main.go cmd/hindsight/main_test.go deploy.sh .gitignore
git commit -F - <<'MSG'
Resolve the UI directory for a checkout as well as an install

staticDir hardcoded ~/audio-dashcam/v2-go/static and called os.Executable
inline, so it was both wrong after the move and untestable. The decision is
now a pure function over four candidates, covering an unpacked release, the
deploy.sh checkout on the Pi, a flat layout, and `go run` from a source tree
-- where the binary sits in a temp build directory and only the working
directory can find the UI. That last case is what the README's demo needs.

deploy.sh takes HINDSIGHT_HOST but still answers to DASHCAM_HOST, so an
existing untracked deploy.local.env keeps working.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_015NBvK3VetFgPNaB767toLG
MSG
```

---

### Task 3: Correct the SAVE_CHANNELS default

`config.go` defaults to `3,4`. The measurement recorded in the env example on 2026-09-08 shows USB `1/2` is the post-fader MAIN and `3/4` is a pre-fader tap on channel one — which ignores the mixer entirely, so a take made on the default records one channel strip and no faders. A fresh install with no env file therefore captures the wrong thing, silently.

**Files:**
- Create: `internal/config/config_test.go`
- Modify: `internal/config/config.go:56` (the `parseChannels` default) and `OutputDir`

**Interfaces:**
- Consumes: nothing.
- Produces: `Config.SaveChannels` defaulting to `[]int{0, 1}`; `Config.OutputDir` defaulting to `~/hindsight/jam_saves`.

- [ ] **Step 1: Write the failing test**

Create `internal/config/config_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

// clearEnv blanks every variable Load reads, so a developer's own shell
// cannot change what the defaults appear to be.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"DEVICE_MATCH", "CHANNELS", "SAMPLE_RATE", "FRAMES_PER_BUFFER",
		"INPUT_LATENCY_MS", "RING_SECONDS", "OUTPUT_DIR", "SAVE_CHANNELS",
		"SAVE_ALL_CHANNELS", "MIN_FREE_GB", "MAX_SAVES", "PORT",
	} {
		t.Setenv(k, "")
	}
}

// The EP-136 presents four stereo record pairs. Measured 2026-09-08: USB 1/2
// is the post-fader MAIN, USB 3/4 a pre-fader tap on channel one. Defaulting
// to the tap records one strip at its own limiter ceiling, with the mixer and
// every other input missing -- and does so without complaining.
func TestLoadDefaultsToThePostFaderMain(t *testing.T) {
	clearEnv(t)

	c, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	want := []int{0, 1} // stored zero-based; 1,2 as the hardware labels them
	if len(c.SaveChannels) != len(want) {
		t.Fatalf("SaveChannels = %v, want %v", c.SaveChannels, want)
	}
	for i := range want {
		if c.SaveChannels[i] != want[i] {
			t.Errorf("SaveChannels = %v, want %v", c.SaveChannels, want)
			break
		}
	}
}

func TestLoadDefaultOutputDirIsUnderTheInstallRoot(t *testing.T) {
	clearEnv(t)

	c, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	home, _ := os.UserHomeDir()
	want := filepath.Join(home, "hindsight", "jam_saves")
	if c.OutputDir != want {
		t.Errorf("OutputDir = %q, want %q", c.OutputDir, want)
	}
}

func TestLoadRejectsAChannelOutsideTheDevice(t *testing.T) {
	clearEnv(t)
	t.Setenv("CHANNELS", "2")
	t.Setenv("SAVE_CHANNELS", "3,4")

	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded with SAVE_CHANNELS beyond CHANNELS; want an error")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./internal/config/ -v
```

Expected: `TestLoadDefaultsToThePostFaderMain` FAILs with `SaveChannels = [2 3], want [0 1]`, and `TestLoadDefaultOutputDirIsUnderTheInstallRoot` FAILs on the `audio-dashcam` path. The third test passes already — it guards behaviour worth keeping, not behaviour being added.

- [ ] **Step 3: Change the defaults**

In `internal/config/config.go`, replace the `SAVE_CHANNELS` block and its comment:

```go
	// SAVE_CHANNELS is 1-indexed in the environment because that is how the
	// hardware labels them; store zero-based.
	//
	// The EP-136 presents its eight inputs as four stereo record pairs and
	// Teenage Engineering does not document which USB pair is which, so it was
	// measured (2026-09-08) by playing into mixer channel 1 and comparing
	// levels with the fader up and down: USB 1/2 moved 20.8 dB, USB 3/4 did
	// not move at all. 1/2 is the post-fader MAIN; 3/4 is a pre-fader tap on
	// one strip, which records at that strip's limiter ceiling with the mixer
	// -- and everything plugged into the other inputs -- missing.
	//
	// scripts/channel-probe.py re-runs the measurement if this is ever in doubt.
	ch, err := parseChannels(env("SAVE_CHANNELS", "1,2"), c.Channels)
```

And the output directory:

```go
		OutputDir:       env("OUTPUT_DIR", filepath.Join(home, "hindsight", "jam_saves")),
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test ./internal/config/ -v && go test ./...
```

Expected: all three config tests PASS; the whole suite still green.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -F - <<'MSG'
Default to the post-fader main, not the channel-one tap

The default was 3,4. The measurement in the env example, made 2026-09-08 by
comparing levels with the fader up and down, says USB 1/2 is the post-fader
MAIN and USB 3/4 is a pre-fader tap on one strip. Anyone installing this
without writing an env file was therefore recording one channel at its own
limiter ceiling, with the mixer and every other input absent -- and nothing
about the take would say so.

config had no tests at all; these also pin the output directory and the
range check on SAVE_CHANNELS.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_015NBvK3VetFgPNaB767toLG
MSG
```

---

### Task 4: Split the audio device behind an interface

`Capture` owns PortAudio directly, so the whole `audio` package needs cgo and a real 8-channel interface. Extracting a `Source` lets a synthetic implementation reuse the entire pipeline — ring writer, level meters, staleness watchdog, restart backoff — unchanged, and moves every PortAudio import behind a build tag so `CGO_ENABLED=0` compiles.

Behaviour-preserving: the existing `audio` tests are the guard.

**Files:**
- Create: `internal/audio/source.go`
- Create: `internal/audio/source_portaudio.go` (build tag `cgo`)
- Create: `internal/audio/source_nocgo.go` (build tag `!cgo`)
- Modify: `internal/audio/capture.go` (remove PortAudio; take a `Source`)
- Modify: `cmd/hindsight/main.go:33`

**Interfaces:**
- Consumes: `paLifecycle` from `internal/audio/palifecycle.go` (unchanged; `Init` is idempotent, `Rescan` is `Term` then `Init`).
- Produces:
  - `type Source interface { Open(sink func([]int32)) (string, error); Close(); Reset() error; Shutdown() error }`
  - `func NewDeviceSource(cfg *config.Config) Source` — defined in both tagged files.
  - `func NewCapture(cfg *config.Config, src Source) *Capture` — signature change.

- [ ] **Step 1: Define the interface**

Create `internal/audio/source.go`:

```go
package audio

// Source is a live audio input feeding interleaved int32 frames into the
// capture pipeline.
//
// It exists so that Capture's supervision -- the restart backoff, the
// staleness watchdog, the block pool -- is written once and shared by the
// real device and the demo generator, and so that every PortAudio symbol can
// sit behind a cgo build tag. A build without cgo still compiles and still
// runs, with the demo source.
type Source interface {
	// Open starts delivering blocks to sink and reports a human-readable
	// device name. sink must be called from a single goroutine and must not
	// be called after Close returns.
	Open(sink func([]int32)) (name string, err error)

	// Close stops delivery. It is safe to call when Open failed or was never
	// called, and safe to call twice.
	Close()

	// Reset is called after a failed Open, before the next attempt. It is
	// where a source re-enumerates hardware.
	Reset() error

	// Shutdown releases process-wide resources. Called once, from Stop.
	Shutdown() error
}
```

- [ ] **Step 2: Move the PortAudio code behind a cgo tag**

Create `internal/audio/source_portaudio.go`. The bodies of `pickDevice`, `deviceScore`, `openStream` and `closeStream` move here **verbatim** from `capture.go` — only the receiver changes from `*Capture` to `*deviceSource`, and the `c.cfg` references become `s.cfg`.

```go
//go:build cgo

package audio

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/gabeduke/hindsight/internal/config"
	"github.com/gordonklaus/portaudio"
)

// deviceSource is the real audio interface, opened through PortAudio.
type deviceSource struct {
	cfg *config.Config
	pa  *paLifecycle

	mu     sync.Mutex
	stream *portaudio.Stream
}

func NewDeviceSource(cfg *config.Config) Source {
	return &deviceSource{
		cfg: cfg,
		pa:  newPALifecycle(portaudio.Initialize, portaudio.Terminate),
	}
}

func (s *deviceSource) Open(sink func([]int32)) (string, error) {
	// Init is idempotent, so opening repeatedly costs nothing and no caller
	// has to track whether PortAudio is up.
	if err := s.pa.Init(); err != nil {
		return "", err
	}

	dev, err := s.pickDevice()
	if err != nil {
		return "", err
	}

	p := portaudio.HighLatencyParameters(dev, nil)
	p.Input.Channels = s.cfg.Channels
	p.SampleRate = float64(s.cfg.SampleRate)
	p.FramesPerBuffer = s.cfg.FramesPerBuf
	// An explicit, generous latency is the fix for the busy-poll that pegged a
	// core: LowLatencyParameters asks a USB device for a deadline it cannot
	// meet, so PortAudio spins. A ring buffer has no latency requirement.
	p.Input.Latency = time.Duration(s.cfg.InputLatencyMS) * time.Millisecond

	stream, err := portaudio.OpenStream(p, sink)
	if err != nil {
		return "", fmt.Errorf("open %q: %w", dev.Name, err)
	}
	if err := stream.Start(); err != nil {
		stream.Close()
		return "", fmt.Errorf("start %q: %w", dev.Name, err)
	}

	s.mu.Lock()
	s.stream = stream
	s.mu.Unlock()

	return dev.Name, nil
}

func (s *deviceSource) Close() {
	s.mu.Lock()
	st := s.stream
	s.stream = nil
	s.mu.Unlock()

	if st != nil {
		_ = st.Stop()
		_ = st.Close()
	}
}

// Reset re-enumerates devices. PortAudio's device list is frozen at
// Pa_Initialize and never rescans, so an interface that was power-cycled is
// invisible until the library is torn down and brought back up.
func (s *deviceSource) Reset() error { return s.pa.Rescan() }

func (s *deviceSource) Shutdown() error { return s.pa.Term() }

// pickDevice ... (move the existing body from capture.go verbatim,
// receiver *deviceSource, c.cfg -> s.cfg)

// deviceScore ... (move the existing body from capture.go verbatim)
```

- [ ] **Step 3: Add the no-cgo stub**

Create `internal/audio/source_nocgo.go`:

```go
//go:build !cgo

package audio

import (
	"errors"

	"github.com/gabeduke/hindsight/internal/config"
)

// deviceSource without cgo cannot exist: PortAudio is a C library. The type
// is still defined so that a CGO_ENABLED=0 build compiles and gives a useful
// message at runtime rather than failing to link.
type deviceSource struct{}

func NewDeviceSource(_ *config.Config) Source { return &deviceSource{} }

func (s *deviceSource) Open(func([]int32)) (string, error) {
	return "", errors.New("built without cgo: no audio hardware support, run with --demo")
}

func (s *deviceSource) Close()          {}
func (s *deviceSource) Reset() error    { return nil }
func (s *deviceSource) Shutdown() error { return nil }
```

- [ ] **Step 4: Rewrite Capture to use the interface**

In `internal/audio/capture.go`: delete the `pa`, `mu` and `stream` fields, delete `openStream`, `closeStream`, `pickDevice` and `deviceScore` (now in the tagged file), and add a `src Source` field.

Then drop the imports those functions took with them — `portaudio`, `strings`, and `fmt`, which was used only by their error paths. The compiler rejects an unused import, so it will name any you miss; `sync` stays, for `stopOnce` and `wg`.

```go
func NewCapture(cfg *config.Config, src Source) *Capture {
	c := &Capture{
		cfg:    cfg,
		src:    src,
		ring:   NewRing(cfg.RingFrames(), cfg.Channels),
		levels: NewLevels(cfg.Channels, cfg.SampleRate, levelBinMillis),
		free:   make(chan []int32, blockPoolSize),
		filled: make(chan []int32, blockPoolSize),
		stop:   make(chan struct{}),
	}
	// ... rest of the body unchanged
}
```

`Start` no longer initialises PortAudio — `Open` does:

```go
func (c *Capture) Start() error {
	c.wg.Add(3)
	go c.ringWriter()
	go c.broadcaster()
	go c.supervise()
	return nil
}
```

In `supervise`, three substitutions. The `openStream` call:

```go
		name, err := c.src.Open(c.processAudio)
		if err != nil {
```

the rescan inside the retry branch:

```go
			if err := c.src.Reset(); err != nil {
				log.Printf("[!] device rescan: %v", err)
			}
```

and, on success, store the name the source reported:

```go
		backoff = time.Second
		c.lastErr.Store("")
		c.deviceName.Store(name)
		c.lastCallback.Store(time.Now().UnixNano())
		c.healthy.Store(true)
```

Every remaining `c.closeStream()` becomes:

```go
		c.src.Close()
		c.healthy.Store(false)
```

And `Stop`:

```go
func (c *Capture) Stop() {
	c.stopOnce.Do(func() {
		close(c.stop)
		c.wg.Wait()
		c.src.Close()
		_ = c.src.Shutdown()
	})
}
```

- [ ] **Step 5: Update the one call site**

In `cmd/hindsight/main.go`, replace line 33:

```go
	cap := audio.NewCapture(cfg, audio.NewDeviceSource(cfg))
```

- [ ] **Step 6: Verify both build modes and the suite**

```bash
gofmt -l . && go vet ./... && go test ./...
CGO_ENABLED=0 go build ./... && echo "nocgo build: ok"
CGO_ENABLED=0 go vet ./... && echo "nocgo vet: ok"
```

Expected: suite green (same three packages), then `nocgo build: ok` and `nocgo vet: ok`. If the nocgo build fails with an undefined PortAudio symbol, a reference was left in an untagged file — find it with `grep -rn portaudio internal/`.

- [ ] **Step 7: Commit**

```bash
git add internal/audio/ cmd/hindsight/main.go
git commit -F - <<'MSG'
Put the audio device behind an interface so a build needs no hardware

Capture owned PortAudio directly, so the package needed cgo, a C library and
a real 8-channel interface just to compile -- which is why the app could not
run anywhere but the rig, and why there was no way to demo it, smoke-test it
in CI, or screenshot it.

Source is the seam. The real device keeps every line it had, moved verbatim
behind //go:build cgo; supervision, the block pool, the staleness watchdog
and the restart backoff stay in Capture and are now shared. A CGO_ENABLED=0
build compiles and links.

No behaviour change; the existing audio tests are the guard.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_015NBvK3VetFgPNaB767toLG
MSG
```

---

### Task 5: The synthetic source

A generator that fills the ring with something that looks like music, so the meters, the buffer ribbon and a saved take all show real structure in a screenshot. Deterministic: driven by a sample counter, not wall-clock, so two runs produce the same waveform.

**Files:**
- Create: `internal/audio/source_demo.go`
- Create: `internal/audio/source_demo_test.go`

**Interfaces:**
- Consumes: `Source` from Task 4; `config.Config.{Channels,SampleRate,FramesPerBuf,SaveChannels}`.
- Produces: `func NewDemoSource(cfg *config.Config) Source`, `const DemoBPM = 96.0`.

- [ ] **Step 1: Write the failing test**

Create `internal/audio/source_demo_test.go`:

```go
package audio

import (
	"math"
	"sync"
	"testing"
	"time"

	"github.com/gabeduke/hindsight/internal/config"
)

func demoConfig() *config.Config {
	return &config.Config{
		Channels:     8,
		SampleRate:   48000,
		FramesPerBuf: 2048,
		SaveChannels: []int{0, 1},
	}
}

func TestDemoSourceDeliversBlocksOfTheRightShape(t *testing.T) {
	cfg := demoConfig()
	src := NewDemoSource(cfg)

	var mu sync.Mutex
	var blocks [][]int32

	name, err := src.Open(func(in []int32) {
		mu.Lock()
		defer mu.Unlock()
		if len(blocks) < 8 {
			cp := make([]int32, len(in))
			copy(cp, in)
			blocks = append(blocks, cp)
		}
	})
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer src.Close()

	if name == "" {
		t.Error("Open() returned an empty device name; the UI shows this")
	}

	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		n := len(blocks)
		mu.Unlock()
		if n >= 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("got %d blocks in 2s, want at least 3", n)
		case <-time.After(10 * time.Millisecond):
		}
	}

	mu.Lock()
	defer mu.Unlock()
	want := cfg.FramesPerBuf * cfg.Channels
	for i, b := range blocks {
		if len(b) != want {
			t.Errorf("block %d has %d samples, want %d", i, len(b), want)
		}
	}
}

// The point of the demo is that a screenshot looks like audio. A signal that
// never moves would satisfy every structural check above and still be useless.
func TestDemoSourceProducesMovingAudioOnTheSavedPair(t *testing.T) {
	cfg := demoConfig()
	src := NewDemoSource(cfg)

	var mu sync.Mutex
	var minL, maxL float64 = 1, -1
	var maxOther float64

	_, err := src.Open(func(in []int32) {
		mu.Lock()
		defer mu.Unlock()
		for i := 0; i+cfg.Channels <= len(in); i += cfg.Channels {
			v := float64(in[i]) / 2147483648.0
			minL, maxL = math.Min(minL, v), math.Max(maxL, v)
			for c := 2; c < cfg.Channels; c++ {
				maxOther = math.Max(maxOther, math.Abs(float64(in[i+c])/2147483648.0))
			}
		}
	})
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer src.Close()

	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	if maxL < 0.05 {
		t.Errorf("peak on the saved pair is %.4f, want a signal above 0.05", maxL)
	}
	if maxL > 1.0 || minL < -1.0 {
		t.Errorf("signal clips: range [%.4f, %.4f]", minL, maxL)
	}
	if maxL-minL < 0.05 {
		t.Errorf("signal barely moves: range [%.4f, %.4f]", minL, maxL)
	}
	// Unused inputs carry bleed, as the hardware does, so the channel panel
	// looks real -- but they must stay well below the master.
	if maxOther > maxL/4 {
		t.Errorf("bleed on unused channels is %.4f against a master of %.4f; too loud", maxOther, maxL)
	}
}

func TestDemoSourceCloseIsIdempotent(t *testing.T) {
	src := NewDemoSource(demoConfig())
	if _, err := src.Open(func([]int32) {}); err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	src.Close()
	src.Close() // must not panic or block
	if err := src.Shutdown(); err != nil {
		t.Errorf("Shutdown() error: %v", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./internal/audio/ -run TestDemoSource -v
```

Expected: FAIL — `undefined: NewDemoSource`.

- [ ] **Step 3: Implement the generator**

Create `internal/audio/source_demo.go`:

```go
package audio

import (
	"math"
	"math/rand"
	"sync"
	"time"

	"github.com/gabeduke/hindsight/internal/config"
)

// DemoBPM is the tempo of the synthetic loop. It is exported because the demo
// also stands in for the MIDI clock, and the two must agree.
const DemoBPM = 96.0

// demoSource generates a loop that looks like music: a screenshot of a flat
// sine tells a reader nothing about what the meters, the ribbon or a take
// actually look like.
//
// Everything is derived from a monotonic sample counter rather than the clock,
// so the waveform is identical run to run and screenshots are reproducible.
type demoSource struct {
	cfg *config.Config

	// Guards the channel pair rather than a sync.Once: Open must be able to
	// arm a fresh generator after Close, and re-assigning a sync.Once copies
	// a lock, which go vet rejects.
	mu   sync.Mutex
	stop chan struct{}
	done chan struct{}
}

func NewDemoSource(cfg *config.Config) Source {
	return &demoSource{cfg: cfg}
}

func (s *demoSource) Open(sink func([]int32)) (string, error) {
	s.mu.Lock()
	s.stop = make(chan struct{})
	s.done = make(chan struct{})
	stop, done := s.stop, s.done
	s.mu.Unlock()

	block := make([]int32, s.cfg.FramesPerBuf*s.cfg.Channels)
	period := time.Duration(float64(s.cfg.FramesPerBuf) / float64(s.cfg.SampleRate) * float64(time.Second))

	go func() {
		defer close(done)
		t := time.NewTicker(period)
		defer t.Stop()

		var n int64 // frames generated so far
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				s.fill(block, n)
				n += int64(s.cfg.FramesPerBuf)
				sink(block)
			}
		}
	}()

	return "Demo signal generator (synthetic, 96 BPM)", nil
}

// Close stops the generator and waits for it, so no sink call is in flight
// when it returns. Calling it twice, or without Open, is a no-op.
func (s *demoSource) Close() {
	s.mu.Lock()
	stop, done := s.stop, s.done
	s.stop, s.done = nil, nil
	s.mu.Unlock()

	if stop == nil {
		return
	}
	close(stop)
	<-done
}

func (s *demoSource) Reset() error    { return nil }
func (s *demoSource) Shutdown() error { return nil }

// fill writes one interleaved block starting at absolute frame n.
func (s *demoSource) fill(block []int32, n int64) {
	const fullScale = 2147483648.0

	rate := float64(s.cfg.SampleRate)
	saved := make(map[int]bool, len(s.cfg.SaveChannels))
	for _, c := range s.cfg.SaveChannels {
		saved[c] = true
	}

	// A fixed-seed source per block keeps the hats from being identical every
	// bar while staying deterministic for a given n.
	noise := rand.New(rand.NewSource(n))

	for i := 0; i < s.cfg.FramesPerBuf; i++ {
		t := float64(n+int64(i)) / rate
		beat := t * DemoBPM / 60.0

		// An 8-bar arc (32 beats) so the ribbon shows structure rather than a
		// uniform band.
		arc := 0.55 + 0.45*math.Sin(2*math.Pi*beat/32.0)

		// Kick on every beat: a decaying low sine.
		kb := beat - math.Floor(beat)
		kick := math.Exp(-9*kb) * math.Sin(2*math.Pi*55*t)

		// Hats on eighths: a short noise burst.
		hb := beat*2 - math.Floor(beat*2)
		hat := math.Exp(-45*hb) * (noise.Float64()*2 - 1) * 0.35

		// Bass: one note per bar, walking a minor pentatonic.
		bar := int(math.Floor(beat / 4))
		bassHz := []float64{82.41, 98.00, 110.00, 73.42}[bar%4]
		bass := 0.45 * math.Sin(2*math.Pi*bassHz*t)

		// Pad: a triad two octaves up, quiet enough to sit under everything.
		pad := 0.12 * (math.Sin(2*math.Pi*bassHz*4*t) +
			math.Sin(2*math.Pi*bassHz*4.75*t) +
			math.Sin(2*math.Pi*bassHz*6*t))

		mix := arc * (0.55*kick + hat + bass + pad)
		// Soft clip, then leave ~6 dB of headroom so nothing reads as pinned.
		mix = math.Tanh(mix) * 0.5

		for c := 0; c < s.cfg.Channels; c++ {
			v := mix
			if !saved[c] {
				// Bleed, as the real interface has on its unused pairs.
				v *= 0.03
			}
			block[i*s.cfg.Channels+c] = int32(v * (fullScale - 1))
		}
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test ./internal/audio/ -run TestDemoSource -v
go test -race ./internal/audio/
```

Expected: all four demo tests PASS, and the race detector is clean — the generator goroutine and the test's callback share state through the caller's mutex only.

- [ ] **Step 5: Commit**

```bash
git add internal/audio/source_demo.go internal/audio/source_demo_test.go
git commit -F - <<'MSG'
Generate a synthetic jam so the app runs with no interface attached

A flat sine would satisfy any structural test and still make a useless
screenshot: the meters, the buffer ribbon and a saved take all exist to show
structure, so the demo signal has to have some. This is a 96 BPM loop -- kick
on beats, hats on eighths, a bass note per bar and a quiet pad -- under an
8-bar amplitude arc, with light bleed on the unused inputs so the channel
panel looks like the hardware does.

Driven by a sample counter rather than the clock, so the waveform is the same
on every run and screenshots are reproducible.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_015NBvK3VetFgPNaB767toLG
MSG
```

---

### Task 6: Wire up --demo, --version and the reported version

**Files:**
- Create: `internal/midi/fixed.go`
- Modify: `cmd/hindsight/main.go`
- Modify: `internal/config/config.go` (add `Version`)
- Modify: `internal/api/api.go` (report it)
- Modify: `internal/config/config_test.go` (pin the default)

**Interfaces:**
- Consumes: `audio.NewDemoSource`, `audio.DemoBPM` (Task 5); `audio.NewDeviceSource` (Task 4).
- Produces: `midi.NewFixedClock(bpm float64) *FixedClock` satisfying both `api.MIDISource` and `audio.TempoSource`; `config.Config.Version`; `main.version` as the `-ldflags -X` target.

- [ ] **Step 1: Write the failing test for the fixed clock**

Create `internal/midi/fixed_test.go`:

```go
package midi

import (
	"testing"
	"time"

	"github.com/gabeduke/hindsight/internal/audio"
	"github.com/gabeduke/hindsight/internal/api"
)

func TestFixedClockSatisfiesBothConsumers(t *testing.T) {
	var _ api.MIDISource = NewFixedClock(96)
	var _ audio.TempoSource = NewFixedClock(96)
}

func TestFixedClockAlwaysReports(t *testing.T) {
	c := NewFixedClock(96)

	if !c.Connected() {
		t.Error("Connected() = false; the demo clock is always present")
	}
	bpm, ok := c.BPM(time.Now().Add(-time.Minute), time.Now())
	if !ok {
		t.Fatal("BPM() reported no reading")
	}
	if bpm != 96 {
		t.Errorf("BPM() = %v, want 96", bpm)
	}
}
```

Note: `internal/midi` importing `internal/api` in a test creates no cycle — `api` imports `audio` and `config`, never `midi`; it holds the clock through its own `MIDISource` interface.

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./internal/midi/ -run TestFixedClock -v
```

Expected: FAIL — `undefined: NewFixedClock`.

- [ ] **Step 3: Implement the fixed clock**

Create `internal/midi/fixed.go`:

```go
package midi

import "time"

// FixedClock reports one tempo, always. It stands in for a real MIDI clock in
// demo mode: without it the tempo tile reads "–" and a screenshot shows three
// working stats and one dead one.
type FixedClock struct{ bpm float64 }

func NewFixedClock(bpm float64) *FixedClock { return &FixedClock{bpm: bpm} }

func (c *FixedClock) Connected() bool { return true }

func (c *FixedClock) Device() string { return "demo clock" }

func (c *FixedClock) BPM(_, _ time.Time) (float64, bool) { return c.bpm, true }
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
go test ./internal/midi/ -run TestFixedClock -v
```

Expected: PASS.

- [ ] **Step 5: Add the version to config and the status payload**

In `internal/config/config.go`, add to the `Config` struct under `// Server`:

```go
	// Version is stamped by the build (-ldflags -X main.version) and reported
	// on /api/status, so an installed Pi can say which release it is running.
	Version string
```

and in `Load()`:

```go
		Version:         "dev",
```

In `internal/api/api.go`, add `"version": a.cfg.Version,` to the map that `handleStatus` writes. Find it with `grep -n 'func (a \*API) handleStatus' -A 30 internal/api/api.go` and add the field alongside the existing top-level keys.

Add to `internal/config/config_test.go`:

```go
func TestLoadDefaultVersionIsDev(t *testing.T) {
	clearEnv(t)

	c, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if c.Version != "dev" {
		t.Errorf("Version = %q, want %q", c.Version, "dev")
	}
}
```

- [ ] **Step 6: Add the flags to main**

In `cmd/hindsight/main.go`, add the `flag` import and, at the top of the file:

```go
// version is stamped at build time with -ldflags "-X main.version=v2026.09.09.1".
var version = "dev"
```

At the top of `main()`, before `config.Load()`:

```go
	demo := flag.Bool("demo", false, "run with a synthetic audio source and no hardware")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}
```

After `config.Load()` succeeds:

```go
	cfg.Version = version
```

Then select the source and the clock. Replace the `NewCapture`, `midi.NewReader` and `SetTempoSource` block with:

```go
	// FixedClock and Reader each satisfy both consumers, so both are held
	// through their interfaces rather than asserted back out of one.
	var (
		src   audio.Source
		clock api.MIDISource
		tempo audio.TempoSource
	)
	if *demo {
		fc := midi.NewFixedClock(audio.DemoBPM)
		src, clock, tempo = audio.NewDemoSource(cfg), fc, fc
		log.Printf("[*] demo mode — synthetic audio, no hardware")
	} else {
		src = audio.NewDeviceSource(cfg)
	}

	cap := audio.NewCapture(cfg, src)
	if err := cap.Start(); err != nil {
		log.Fatalf("capture: %v", err)
	}
	defer cap.Stop()

	saver := audio.NewSaver(cap)

	if !*demo {
		// The clock ring covers the same window as the audio ring, so a
		// full-ring save can still ask about its oldest end. Sized in pulses
		// at the fastest tempo the BPM field accepts.
		mc := midi.NewClock(midi.CapacityFor(cfg.RingSeconds))
		reader := midi.NewReader(cfg.DeviceMatch, mc)
		reader.Start()
		defer reader.Stop()
		clock, tempo = reader, reader
	}
	saver.SetTempoSource(tempo)
```

and pass `clock` where `reader` was passed to `api.New`.

- [ ] **Step 7: Verify the demo actually boots**

```bash
go build -o /tmp/hindsight ./cmd/hindsight
/tmp/hindsight --version
RING_SECONDS=60 OUTPUT_DIR=/tmp/hindsight-takes /tmp/hindsight --demo &
sleep 4
curl -fsS http://127.0.0.1:5000/api/status | python3 -m json.tool | head -30
kill %1
```

Expected: `--version` prints `dev`; the status payload has `"version": "dev"`, a non-zero buffered figure, `"bpm": 96`, and a device name of `Demo signal generator (synthetic, 96 BPM)`.

- [ ] **Step 8: Verify it boots without cgo too**

```bash
CGO_ENABLED=0 go build -o /tmp/hindsight-nocgo ./cmd/hindsight
RING_SECONDS=60 OUTPUT_DIR=/tmp/hindsight-takes /tmp/hindsight-nocgo --demo &
sleep 4
curl -fsS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:5000/api/status
kill %1
```

Expected: `200`. This is the exact check `ci.yml` will run.

- [ ] **Step 9: Run the full suite and commit**

```bash
gofmt -l . && go vet ./... && go test ./...
git add cmd/hindsight/main.go internal/midi/fixed.go internal/midi/fixed_test.go internal/config/ internal/api/api.go
git commit -F - <<'MSG'
Add --demo and --version, and report the version on /api/status

--demo swaps in the synthetic source and a fixed 96 BPM clock, so the app
runs on a laptop with no interface and no PortAudio at all: with
CGO_ENABLED=0 it needs no system libraries whatsoever. That is what makes
the README's one-line demo real, and it is the boot check CI runs.

The fixed clock exists because the tempo tile would otherwise read "–" --
three working stats and one dead one in every screenshot.

Version is stamped by -ldflags at release time and surfaced on /api/status,
so an installed Pi can say which release it is running.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_015NBvK3VetFgPNaB767toLG
MSG
```

---

### Task 7: Screenshots

Run the demo locally and capture the UI at three viewports. Everything is `localhost`, so no hostname reaches a committed PNG.

**Files:**
- Create: `scripts/screenshots.mjs`
- Create: `docs/images/{desktop,tablet,phone}.png`

**Interfaces:**
- Consumes: `--demo` from Task 6.
- Produces: the three image paths Task 10's README embeds.

- [ ] **Step 1: Write the capture script**

Create `scripts/screenshots.mjs`:

```js
// Capture the README screenshots against a running demo instance.
//
//   go run ./cmd/hindsight --demo &
//   npx --yes playwright@1.49.0 install chromium
//   node scripts/screenshots.mjs
//
// Everything is localhost on purpose: this repo is public, and a screenshot
// of the real rig would put its hostname in the URL bar.
import { chromium } from 'playwright';

const BASE = process.env.HINDSIGHT_URL ?? 'http://127.0.0.1:5000';

const VIEWPORTS = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'tablet', width: 1024, height: 768 },
  { name: 'phone', width: 390, height: 844 },
];

// Long enough for the ring to hold several bars, so the ribbon and the take
// waveform show the loop's structure rather than a sliver.
const FILL_MS = 45_000;

const browser = await chromium.launch();

const warm = await browser.newPage();
await warm.goto(BASE);
console.log(`filling the ring for ${FILL_MS / 1000}s...`);
await warm.waitForTimeout(FILL_MS);

// One take, so the library panel is not empty.
const res = await warm.request.post(`${BASE}/api/trigger?seconds=30`);
if (!res.ok()) throw new Error(`capture failed: ${res.status()}`);
await warm.waitForTimeout(4000);
await warm.close();

for (const vp of VIEWPORTS) {
  const page = await browser.newPage({
    viewport: { width: vp.width, height: vp.height },
    deviceScaleFactor: 2,
  });
  await page.goto(BASE);
  await page.waitForSelector('#takes .take', { timeout: 15_000 });
  await page.waitForTimeout(2500); // let the meters settle mid-swing
  await page.screenshot({ path: `docs/images/${vp.name}.png` });
  console.log(`docs/images/${vp.name}.png`);
  await page.close();
}

await browser.close();
```

If `#takes .take` does not match, check the class the takes list actually renders with: `grep -n "class=" web/static/lib/takes.js | head`. Use the real selector rather than adding a wait.

- [ ] **Step 2: Capture**

```bash
mkdir -p docs/images
RING_SECONDS=120 OUTPUT_DIR=/tmp/hindsight-shots go run ./cmd/hindsight --demo &
sleep 3
npx --yes playwright@1.49.0 install chromium
node scripts/screenshots.mjs
kill %1
```

- [ ] **Step 3: Inspect every image before committing**

```bash
ls -la docs/images/
```

Open each one and confirm: the meters show signal, the ribbon shows the loop's arc rather than a flat band, the takes list has an entry, the tempo tile reads 96, and **no hostname other than `127.0.0.1` appears anywhere**. Re-run if any is wrong — a screenshot that misrepresents the UI is worse than none.

- [ ] **Step 4: Commit**

```bash
git add scripts/screenshots.mjs docs/images/
git commit -F - <<'MSG'
Capture the UI at three viewports against the demo source

Taken against --demo on localhost rather than the real rig: this repo is
public and its history was squashed once already to remove home network
details, so a screenshot with the tailnet hostname in the URL bar is a leak,
not a nice picture.

The script is committed because the images will go stale, and regenerating
them should not mean rediscovering how.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_015NBvK3VetFgPNaB767toLG
MSG
```

---

### Task 8: The systemd unit and the installer

`deploy.sh` has always restarted a unit that was never tracked. Ship it, plus a script that turns an unpacked release into a running service.

**Files:**
- Create: `deploy/hindsight.service`
- Create: `deploy/install.sh` (mode 755)

**Interfaces:**
- Consumes: the binary name and install root from the Global Constraints.
- Produces: the layout Task 9's tarball must match — `install.sh` at the root of the archive, `bin/hindsight`, `web/static/`, `deploy/`.

- [ ] **Step 1: Write the unit**

Create `deploy/hindsight.service`:

```ini
[Unit]
Description=Hindsight — always-listening audio buffer
Documentation=https://github.com/gabeduke/hindsight
After=sound.target network.target

[Service]
Type=simple
# Leading "-" so a missing file is not a startup failure: every value in it
# is optional and the binary has defaults for all of them.
EnvironmentFile=-%h/hindsight/hindsight.env
ExecStart=%h/hindsight/bin/hindsight
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
```

- [ ] **Step 2: Write the installer**

Create `deploy/install.sh`:

```bash
#!/usr/bin/env bash
# Install Hindsight from an unpacked release onto a Raspberry Pi.
#
#   tar xzf hindsight_<version>_linux_arm64.tar.gz
#   cd hindsight_<version>_linux_arm64
#   ./install.sh
set -euo pipefail

SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="${HINDSIGHT_ROOT:-$HOME/hindsight}"
UNIT_DIR="$HOME/.config/systemd/user"

die() { echo "error: $*" >&2; exit 1; }
say() { echo "[*] $*"; }

# --- checks -----------------------------------------------------------------
[ "$(uname -s)" = "Linux" ] || die "this installer targets Linux; found $(uname -s)"
if [ "$(uname -m)" != "aarch64" ]; then
  die "the release binary is arm64; this machine is $(uname -m). Build from source instead: go build -o bin/hindsight ./cmd/hindsight"
fi
command -v systemctl >/dev/null || die "systemd is required"
systemctl --user show-environment >/dev/null 2>&1 \
  || die "no systemd user session. Log in over SSH as the user that will run this, not via sudo."
[ -x "$SRC/bin/hindsight" ] || die "no bin/hindsight next to this script; run it from inside the unpacked release"

# --- runtime dependencies ---------------------------------------------------
# The binary is prebuilt, so only the runtime libraries are needed -- not
# portaudio19-dev. ffmpeg builds the mp3 previews the UI streams.
say "installing runtime dependencies"
sudo apt-get update -qq
sudo apt-get install -y libportaudio2 libasound2 ffmpeg

# --- migrate an existing audio-dashcam install ------------------------------
if systemctl --user list-unit-files 2>/dev/null | grep -q '^audio-dashcam\.service'; then
  say "found an existing audio-dashcam.service"
  read -r -p "    stop and disable it, and move its takes across? [y/N] " reply
  if [ "${reply:-n}" = "y" ] || [ "${reply:-n}" = "Y" ]; then
    systemctl --user disable --now audio-dashcam.service || true
    if [ -d "$HOME/audio-dashcam/jam_saves" ]; then
      mkdir -p "$ROOT"
      # -n so an interrupted run never overwrites a take that already moved.
      mv -n "$HOME/audio-dashcam/jam_saves" "$ROOT/jam_saves"
      say "moved takes to $ROOT/jam_saves"
    fi
  fi
fi

# --- install ----------------------------------------------------------------
say "installing to $ROOT"
mkdir -p "$ROOT/bin" "$ROOT/web" "$ROOT/jam_saves"
install -m 755 "$SRC/bin/hindsight" "$ROOT/bin/hindsight"
rm -rf "$ROOT/web/static"
cp -R "$SRC/web/static" "$ROOT/web/static"

if [ -f "$ROOT/hindsight.env" ]; then
  say "keeping your existing hindsight.env"
else
  cp "$SRC/deploy/hindsight.env.example" "$ROOT/hindsight.env"
  say "wrote $ROOT/hindsight.env from the example — read it before a real session"
fi

# --- service ----------------------------------------------------------------
say "installing the user service"
mkdir -p "$UNIT_DIR"
install -m 644 "$SRC/deploy/hindsight.service" "$UNIT_DIR/hindsight.service"
systemctl --user daemon-reload
systemctl --user enable --now hindsight.service

# Without lingering the service dies at logout, which for a headless Pi means
# it dies as soon as you close the SSH session that started it.
say "enabling lingering so it survives logout"
sudo loginctl enable-linger "$USER"

sleep 3
if systemctl --user is-active --quiet hindsight.service; then
  say "running: http://$(hostname):5000"
  say "next: set SAVE_CHANNELS in $ROOT/hindsight.env — the default assumes an EP-136"
else
  echo
  echo "service did not come up. The log:" >&2
  journalctl --user -u hindsight.service -n 30 --no-pager >&2
  exit 1
fi
```

- [ ] **Step 3: Check it before it ever runs as root**

```bash
chmod 755 deploy/install.sh
bash -n deploy/install.sh && echo "syntax ok"
command -v shellcheck >/dev/null && shellcheck deploy/install.sh || echo "shellcheck not installed; skipped"
```

Expected: `syntax ok`. Fix anything shellcheck reports if it is available.

- [ ] **Step 4: Rehearse the guard clauses on this machine**

The installer must fail clearly rather than half-run. On macOS the first check fires:

```bash
./deploy/install.sh; echo "exit=$?"
```

Expected: `error: this installer targets Linux; found Darwin`, `exit=1`. Nothing was installed, nothing was asked for a password.

- [ ] **Step 5: Commit**

```bash
git add deploy/hindsight.service deploy/install.sh
git commit -F - <<'MSG'
Ship the service unit and an installer

deploy.sh has always restarted a unit that was never in the repo, so the one
thing standing between a stranger and a running install was a file only this
machine had. Here it is, plus the script that unpacks a release into it.

The installer refuses early and loudly -- wrong OS, wrong architecture, no
systemd user session, run from outside the archive -- because the failure
mode worth avoiding is a half-install on someone's Pi. It also detects an
existing audio-dashcam.service and offers to move the takes across, since
this rename orphans anyone already running it.

EnvironmentFile= carries a leading "-": every value is optional.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_015NBvK3VetFgPNaB767toLG
MSG
```

---

### Task 9: CI and releases

**Files:**
- Create: `.github/workflows/ci.yml`
- Create: `.github/workflows/release.yml`
- Create: `scripts/next-tag.sh`, `scripts/changelog-entry.sh`, `scripts/test-release-scripts.sh`
- Create: `CHANGELOG.md`, `LICENSE`

**Interfaces:**
- Consumes: `--demo` (Task 6); the archive layout (Task 8).
- Produces: releases named `hindsight_<tag>_linux_arm64.tar.gz`.

The two pieces of real logic — picking the next tag and rendering the changelog entry — live in shell scripts rather than inline YAML, because logic buried in a workflow can only be tested by pushing to `master`.

- [ ] **Step 1: Write the failing test for the release scripts**

Create `scripts/test-release-scripts.sh`:

```bash
#!/usr/bin/env bash
# Exercise the release helper scripts without pushing anything.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
fail=0

check() {
  local name="$1" want="$2" got="$3"
  if [ "$want" = "$got" ]; then
    echo "ok   - $name"
  else
    echo "FAIL - $name"
    echo "       want: $want"
    echo "       got:  $got"
    fail=1
  fi
}

# next-tag.sh reads existing tags on stdin and takes the date as $1.
check "first release of the day" \
  "v2026.09.09.1" \
  "$(printf '' | "$HERE/next-tag.sh" 2026.09.09)"

check "second release of the day increments" \
  "v2026.09.09.2" \
  "$(printf 'v2026.09.09.1\n' | "$HERE/next-tag.sh" 2026.09.09)"

check "counts only today's tags" \
  "v2026.09.09.1" \
  "$(printf 'v2026.09.08.1\nv2026.09.08.2\n' | "$HERE/next-tag.sh" 2026.09.09)"

check "does not trip over a double-digit count" \
  "v2026.09.09.11" \
  "$(printf 'v2026.09.09.%s\n' 1 2 3 4 5 6 7 8 9 10 | "$HERE/next-tag.sh" 2026.09.09)"

# changelog-entry.sh renders a section from lines of "subject (sha)" on stdin.
entry="$(printf -- '- Do a thing (abc1234)\n- Do another (def5678)\n' \
  | "$HERE/changelog-entry.sh" v2026.09.09.1 2026-09-09)"
case "$entry" in
  "## v2026.09.09.1 — 2026-09-09"*) echo "ok   - entry starts with the heading" ;;
  *) echo "FAIL - entry heading: got ${entry%%$'\n'*}"; fail=1 ;;
esac
case "$entry" in
  *"- Do another (def5678)"*) echo "ok   - entry carries every commit" ;;
  *) echo "FAIL - entry dropped a commit"; fail=1 ;;
esac

exit "$fail"
```

- [ ] **Step 2: Run it to verify it fails**

```bash
chmod 755 scripts/test-release-scripts.sh
./scripts/test-release-scripts.sh; echo "exit=$?"
```

Expected: `No such file or directory` for `next-tag.sh`, non-zero exit.

- [ ] **Step 3: Write the two scripts**

Create `scripts/next-tag.sh`:

```bash
#!/usr/bin/env bash
# Print the next CalVer release tag for a date, given the existing tags on
# stdin. Releases are cut per merge, so several can land on one day.
#
#   git tag -l | scripts/next-tag.sh 2026.09.09   ->   v2026.09.09.3
set -euo pipefail

date="${1:?usage: next-tag.sh YYYY.MM.DD  (existing tags on stdin)}"

# grep -c would count matches; grep then wc keeps an empty input at 0 without
# tripping the pipeline's errexit on grep's exit status 1.
n=$(grep -c "^v${date}\.[0-9][0-9]*$" || true)
echo "v${date}.$((n + 1))"
```

Create `scripts/changelog-entry.sh`:

```bash
#!/usr/bin/env bash
# Render one CHANGELOG section from pre-formatted commit lines on stdin.
#
#   git log --no-merges --pretty='- %s (%h)' "$range" \
#     | scripts/changelog-entry.sh v2026.09.09.1 2026-09-09
set -euo pipefail

tag="${1:?usage: changelog-entry.sh TAG DATE  (commit lines on stdin)}"
date="${2:?usage: changelog-entry.sh TAG DATE  (commit lines on stdin)}"

body="$(cat)"
[ -n "$body" ] || body="- No changes recorded."

printf '## %s — %s\n\n%s\n\n' "$tag" "$date" "$body"
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
chmod 755 scripts/next-tag.sh scripts/changelog-entry.sh
./scripts/test-release-scripts.sh; echo "exit=$?"
```

Expected: six `ok` lines, `exit=0`.

- [ ] **Step 5: Seed the changelog and the licence**

Create `CHANGELOG.md`:

```markdown
# Changelog

Releases are cut automatically on every merge to `master`. Versions are dated:
`vYYYY.MM.DD.N`, where `N` counts the releases made that day.

<!-- new releases are inserted directly below this line -->
```

Create `LICENSE` — the MIT text, `Copyright (c) 2026 Gabriel Duke`.

- [ ] **Step 6: Write ci.yml**

Create `.github/workflows/ci.yml`:

```yaml
name: CI

on:
  pull_request:
  push:
    # master is covered by release.yml, which calls this workflow. Without
    # this, every merge would run the suite twice.
    branches-ignore: [master]
  workflow_call:

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod

      # audio/capture.go is cgo against PortAudio, so the headers are needed
      # to compile the tests -- not to run them. No audio hardware is touched.
      - name: Install build dependencies
        run: |
          sudo apt-get update -qq
          sudo apt-get install -y portaudio19-dev

      - name: gofmt
        run: |
          unformatted="$(gofmt -l .)"
          if [ -n "$unformatted" ]; then
            echo "not gofmt'd:"; echo "$unformatted"; exit 1
          fi

      - name: go vet
        run: go vet ./...

      - name: go test
        run: go test -race ./...

      - name: Release script tests
        run: ./scripts/test-release-scripts.sh

      # The demo path is what lets anyone run this without hardware. If it
      # breaks, nothing else in this workflow would notice.
      - name: Demo boots without cgo
        run: |
          CGO_ENABLED=0 go build -o /tmp/hindsight ./cmd/hindsight
          RING_SECONDS=30 OUTPUT_DIR=/tmp/takes /tmp/hindsight --demo &
          pid=$!
          trap 'kill $pid' EXIT
          for i in $(seq 1 20); do
            if curl -fsS http://127.0.0.1:5000/api/status >/tmp/status.json; then break; fi
            sleep 1
          done
          cat /tmp/status.json
          test -s /tmp/status.json
```

- [ ] **Step 7: Write release.yml**

Create `.github/workflows/release.yml`:

```yaml
name: Release

on:
  push:
    branches: [master]

permissions:
  contents: write

jobs:
  test:
    # Reused rather than duplicated, so a red master cannot publish.
    if: ${{ !contains(github.event.head_commit.message, '[skip ci]') }}
    uses: ./.github/workflows/ci.yml

  release:
    needs: test
    runs-on: ubuntu-24.04-arm
    # Raspberry Pi OS Bookworm ships glibc 2.36; Ubuntu 24.04 links 2.39. A
    # binary built on the bare runner will not start on the Pi, so build
    # against the target distribution instead.
    container: debian:bookworm
    steps:
      - name: Install build dependencies
        run: |
          apt-get update -qq
          apt-get install -y build-essential portaudio19-dev git curl ca-certificates jq

      - uses: actions/checkout@v4
        with:
          fetch-depth: 0

      # The checkout is owned by a different uid inside the container, which
      # makes git refuse to read it.
      - name: Trust the workspace
        run: git config --global --add safe.directory "$GITHUB_WORKSPACE"

      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod

      - name: Choose the version
        id: version
        run: |
          date="$(date -u +%Y.%m.%d)"
          tag="$(git tag -l | ./scripts/next-tag.sh "$date")"
          echo "tag=$tag" >> "$GITHUB_OUTPUT"
          echo "date=$(date -u +%Y-%m-%d)" >> "$GITHUB_OUTPUT"
          echo "building $tag"

      - name: Build
        env:
          TAG: ${{ steps.version.outputs.tag }}
        run: |
          mkdir -p dist/bin
          go build -trimpath -ldflags "-s -w -X main.version=$TAG" \
            -o dist/bin/hindsight ./cmd/hindsight
          ls -lh dist/bin/hindsight
          ./dist/bin/hindsight --version

      - name: Assemble the archive
        env:
          TAG: ${{ steps.version.outputs.tag }}
        run: |
          name="hindsight_${TAG}_linux_arm64"
          mkdir -p "dist/$name/bin" "dist/$name/web" "dist/$name/deploy"
          cp dist/bin/hindsight              "dist/$name/bin/"
          cp -R web/static                   "dist/$name/web/static"
          cp deploy/hindsight.service        "dist/$name/deploy/"
          cp deploy/hindsight.env.example    "dist/$name/deploy/"
          cp deploy/install.sh               "dist/$name/install.sh"
          chmod 755 "dist/$name/install.sh"
          cp README.md CHANGELOG.md LICENSE  "dist/$name/"
          tar -C dist -czf "dist/$name.tar.gz" "$name"
          cd dist && sha256sum "$name.tar.gz" > SHA256SUMS && cat SHA256SUMS

      - name: Update the changelog
        id: notes
        env:
          TAG: ${{ steps.version.outputs.tag }}
          DATE: ${{ steps.version.outputs.date }}
        run: |
          prev="$(git tag -l 'v*' --sort=-creatordate | head -1)"
          range="${prev:+$prev..}HEAD"
          git log --no-merges --pretty='- %s (%h)' "$range" \
            | ./scripts/changelog-entry.sh "$TAG" "$DATE" > /tmp/entry.md
          cat /tmp/entry.md

          # Insert after the marker so the file's header survives.
          awk '
            { print }
            /^<!-- new releases are inserted directly below this line -->$/ && !done {
              print ""
              while ((getline line < "/tmp/entry.md") > 0) print line
              done = 1
            }
          ' CHANGELOG.md > /tmp/CHANGELOG.md
          mv /tmp/CHANGELOG.md CHANGELOG.md

      - name: Tag and push the changelog
        env:
          TAG: ${{ steps.version.outputs.tag }}
        run: |
          git config user.name  "github-actions[bot]"
          git config user.email "41898282+github-actions[bot]@users.noreply.github.com"
          git add CHANGELOG.md
          git commit -m "Record $TAG in the changelog [skip ci]"
          git tag "$TAG"
          git push origin HEAD:master
          git push origin "$TAG"

      - name: Publish
        uses: softprops/action-gh-release@v2
        with:
          tag_name: ${{ steps.version.outputs.tag }}
          body_path: /tmp/entry.md
          files: |
            dist/hindsight_${{ steps.version.outputs.tag }}_linux_arm64.tar.gz
            dist/SHA256SUMS
```

- [ ] **Step 8: Validate the workflow files parse**

```bash
python3 -c "import yaml,sys; [yaml.safe_load(open(f)) for f in ('.github/workflows/ci.yml','.github/workflows/release.yml')]; print('yaml ok')"
```

Expected: `yaml ok`.

- [ ] **Step 9: Commit**

```bash
git add .github/ scripts/next-tag.sh scripts/changelog-entry.sh scripts/test-release-scripts.sh CHANGELOG.md LICENSE
git commit -F - <<'MSG'
Test on every push and cut a release on every merge to master

Nothing enforced the green suite, there were no tags, and the repo was
unlicensed -- which for a public repo means all rights reserved, so nobody
could legally copy this onto their own Pi. MIT.

Releases build inside debian:bookworm on an arm64 runner. That is not
incidental: Pi OS Bookworm ships glibc 2.36 and Ubuntu 24.04 links 2.39, so
a binary from the bare runner will not start on the Pi.

Tag selection and changelog rendering are shell scripts with their own
tests, not YAML: logic buried in a workflow can only be tested by pushing to
master, and this logic decides what a release is called.

The release reuses ci.yml's test job rather than duplicating it, so a red
master cannot publish.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_015NBvK3VetFgPNaB767toLG
MSG
```

---

### Task 10: The documentation

Last, so it describes what exists. `v2-go/README.md`'s prose is the raw material; recover it with `git show f63b271:v2-go/README.md`.

**Files:**
- Create: `README.md`
- Create: `docs/install-raspberry-pi.md`, `docs/configuration.md`, `docs/api.md`, `docs/architecture.md`, `docs/development.md`

**Interfaces:**
- Consumes: everything above — flags (Task 6), images (Task 7), installer (Task 8), releases (Task 9).
- Produces: nothing code depends on.

- [ ] **Step 1: Recover the old prose**

```bash
git show f63b271:v2-go/README.md > /tmp/old-readme.md
git show f63b271:PLAN.md > /tmp/old-plan.md
wc -l /tmp/old-readme.md /tmp/old-plan.md
```

Three things in it are **wrong** and must not be carried over: it says the master pair is 3 & 4 (Task 3 established it is 1 & 2), its API table omits `PATCH /api/take` and `GET /api/envelope`, and it says nothing about MIDI tempo or the buffer ribbon.

- [ ] **Step 2: Write README.md**

The front door, kept short — depth goes in `docs/`. Cover, in order:

1. **Title and one-line pitch.** What it is: an always-listening buffer that writes the last N minutes to disk when you ask, so a jam you liked is not gone.
2. `![](docs/images/desktop.png)` from Task 7.
3. **Try it** — the demo, verbatim:
   ```bash
   git clone https://github.com/gabeduke/hindsight && cd hindsight
   go run ./cmd/hindsight --demo
   ```
   Say explicitly that it needs no hardware and no PortAudio.
4. **Install on a Raspberry Pi** — download the latest release, unpack, `./install.sh`, link to `docs/install-raspberry-pi.md`.
5. **Hardware** — a Pi 5 (8 GB for a 15-minute ring), a USB audio interface with the master on a known channel pair. Developed against a Teenage Engineering EP-136; anything PortAudio enumerates should work, with `DEVICE_MATCH` and `SAVE_CHANNELS` as the two knobs.
6. **How it works** — the ASCII data-flow diagram from the old README, with `EP-136` generalised.
7. **Configuration** — the four variables most people touch (`RING_SECONDS`, `SAVE_CHANNELS`, `DEVICE_MATCH`, `MAX_SAVES`), then link out.
8. **Documentation** — a table linking the five `docs/` pages.
9. **License** — MIT.

Add the phone screenshot beside the tablet one in a short "On a phone" note covering iOS Add to Home Screen, and that Android needs HTTPS for a real install.

- [ ] **Step 3: Write docs/configuration.md**

Every variable from `deploy/hindsight.env.example` as a table, then the reasoning that does not fit in a table — all of it already written, in the example file and the old README:

- The `RING_SECONDS` RAM arithmetic (`seconds × 48000 × channels × 4` bytes) and that a full-ring save briefly doubles it.
- That the UI hides any tier longer than the ring, so shortening it removes buttons.
- The `SAVE_CHANNELS` measurement table from the env example, verbatim, including that USB 3/4 is a pre-fader tap.
- Why `INPUT_LATENCY_MS` is not small: `LowLatencyParameters` makes PortAudio busy-poll a USB device — 84% of a core on the Pi, against about 1% now.

- [ ] **Step 4: Write docs/api.md**

All nine routes from `internal/api/api.go:69-77`. The old README's table is the starting point; it is missing two:

| Endpoint | Purpose |
|---|---|
| `GET /api/status` | Health, ring fill, per-channel dB, disk, live tempo, version |
| `GET /api/live` | WebSocket: min/max peak bins (~100/s) plus peak-hold |
| `POST /api/trigger?seconds=N` | Save the last N seconds; `0` is the whole ring. 507 when disk is low |
| `GET /api/jams` | Takes, newest first. Sends an ETag |
| `PATCH /api/take` | Edit a take's title and BPM. 409 if it changed underneath you |
| `GET /api/peaks?file=` | Precomputed waveform, so phones do not download audio to draw one |
| `GET /api/envelope` | The buffer ribbon's amplitude envelope over the ring |
| `GET /api/download?file=[&dl=1]` | Stream inline, or force a download |
| `DELETE /api/delete?file=` | Remove a take and its sidecars |

Confirm each description against the handler before writing it down.

- [ ] **Step 5: Write docs/architecture.md**

From the old README plus the code comments:

- The data-flow diagram.
- Why the callback never blocks: it computes levels and hands the block to a pooled channel; one writer goroutine owns the ring, so `Snapshot` can memcpy under a mutex without stalling the device.
- The ffmpeg trap: `-ac 2` on an 8-channel file assumes 7.1, folding channel 3 to a mono centre and discarding channel 4 as LFE. Previews use an explicit `pan` map (`internal/audio/save.go:191`).
- MIDI: the clock is read from a rawmidi node found by scanning `/proc/asound/cards` (`internal/midi/device.go`), in its own goroutine, and `audio` never imports `midi` — it takes a `TempoSource` interface, so a MIDI failure has no path into the capture thread.
- The `Source` seam from Task 4 and what the demo source is for.

- [ ] **Step 6: Write docs/install-raspberry-pi.md**

- Prerequisites: Pi 5 (8 GB for a 900-second ring), 64-bit Pi OS Bookworm, an SSH login that is **not** `sudo` (the service is a user unit).
- Install from a release: download, verify against `SHA256SUMS`, unpack, `./install.sh`.
- Plugging in the interface; confirming it with `arecord -l` and `lsusb`.
- Finding the right `SAVE_CHANNELS`: open the UI, expand **Input channels**, play something, watch which pair moves — or run `scripts/channel-probe.py`, which measures it.
- Where things live: `~/hindsight/{bin,web,jam_saves,hindsight.env}`, unit at `~/.config/systemd/user/hindsight.service`.
- Operating it: `systemctl --user status|restart hindsight`, and reading the log with the ALSA/JACK probe noise filtered out:
  ```bash
  journalctl --user -u hindsight.service -f | grep -viE "ALSA lib|jack server|JackShm|Cannot connect to server"
  ```
- Optional: nginx on port 80 (needs the WebSocket `Upgrade` headers proxied, or `/api/live` fails), and `tailscale serve` for HTTPS — which Android requires before it will offer a real PWA install, and which service workers require before they register at all.
- Migrating from `audio-dashcam`: the installer offers it; describe what it does.
- **Use `$HINDSIGHT_HOST` and `<pi-host>.<tailnet>.ts.net` throughout. No real hostnames.**

- [ ] **Step 7: Write docs/development.md**

- Building: PortAudio is cgo, so `brew install portaudio` on macOS or `apt install portaudio19-dev` on Debian; `go build ./cmd/hindsight`.
- `go run ./cmd/hindsight --demo` for everything that does not need real audio, and that `CGO_ENABLED=0` works for the demo.
- Testing: `go test -race ./...`; `./scripts/test-release-scripts.sh`.
- Deploying: `./deploy.sh` needs an untracked `deploy.local.env` holding `HINDSIGHT_HOST=user@host`; `./deploy.sh --static` syncs `web/static` only — about a second, no rebuild, no restart, because the UI is served from disk per request. Use the full deploy only when Go code changed.
- The Python probes in `scripts/`: `channel-probe.py` (which USB pair carries the master), `midi-probe.py` (what the interface actually sends over MIDI), `take-envelope.py` (a take's envelope as compact base64). They are diagnostics, not part of the runtime.
- Regenerating screenshots with `scripts/screenshots.mjs`.
- **There are no JS tests.** The pattern is a throwaway Playwright script in a scratch directory, not committed.
- The convention that no tracked file carries home network details.

- [ ] **Step 8: Check every link and command**

```bash
grep -oh '](\([^)#][^)]*\)' -r README.md docs/*.md | sed 's/](//' | sort -u | while read -r p; do
  case "$p" in http*) continue ;; esac
  [ -e "$p" ] || echo "BROKEN: $p"
done
echo "link check done"
```

Expected: `link check done` with no `BROKEN` lines. Then re-read every shell command in the docs and confirm the paths match the tree this branch actually has.

- [ ] **Step 9: Verify no network details leaked**

```bash
grep -rniE 'ts\.net|\.local|192\.168\.|10\.[0-9]+\.|100\.(6[4-9]|[7-9][0-9]|1[01][0-9]|12[0-7])\.' \
  README.md docs/*.md deploy/ scripts/ .github/ || echo "clean"
```

Any hit must be a placeholder (`<pi-host>.<tailnet>.ts.net`, `hindsight.local` in an example) and nothing else. This repo's history was squashed once to remove exactly this.

- [ ] **Step 10: Commit**

```bash
git add README.md docs/
git commit -F - <<'MSG'
Document the project for someone who just found it

The front door was a PLAN.md describing a Python stack deleted in 0219975,
with the only real documentation one directory down in v2-go/ -- where it was
also wrong about which channel pair carries the master, and silent on the
MIDI tempo and the buffer ribbon.

README is the pitch, a screenshot, and the two paths in: the demo, and a Pi.
Depth moves to docs/, where the expensive lessons keep their reasoning --
the RAM arithmetic, the pre-fader tap, the busy-poll, ffmpeg folding an
8-channel file as 7.1.

Every host is a placeholder. This history was squashed once to remove home
network details; nothing here reintroduces them.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_015NBvK3VetFgPNaB767toLG
MSG
```

---

## Final verification

- [ ] **Full suite, both build modes**

```bash
gofmt -l . && go vet ./... && go test -race ./...
CGO_ENABLED=0 go build ./... && echo "nocgo: ok"
./scripts/test-release-scripts.sh
```

- [ ] **No trace of the old name in tracked files**

```bash
grep -rn 'audio-dashcam\|v2-go\|dashcam.env' --exclude-dir=.git --exclude-dir=docs . \
  | grep -v 'DASHCAM_HOST\|DASHCAM_DEST\|DASHCAM_NO_AGENT\|DASHCAM_SSH_KEY'
```

Expected: no output. The `DASHCAM_*` variables survive on purpose — `deploy.sh` falls back to them so an existing untracked `deploy.local.env` keeps working. Hits under `docs/` are expected too: the migration notes name the old service.

- [ ] **The demo still runs end to end**

```bash
RING_SECONDS=60 OUTPUT_DIR=/tmp/hindsight-final go run ./cmd/hindsight --demo &
sleep 5
curl -fsS http://127.0.0.1:5000/api/status | python3 -m json.tool
curl -fsS -X POST 'http://127.0.0.1:5000/api/trigger?seconds=10' | python3 -m json.tool
curl -fsS http://127.0.0.1:5000/api/jams | python3 -m json.tool
kill %1
```

Expected: status healthy with `"bpm": 96`; the trigger returns a filename; `/api/jams` lists it.

- [ ] **State plainly what this branch cannot verify**

Two deliverables are only exercised by running them somewhere this branch
cannot reach, and the handover must say so rather than imply they are tested:

- **`deploy/install.sh` end to end.** Only its guard clauses are rehearsed
  (Task 8, Step 4). The apt install, the unit install, lingering, and the
  `audio-dashcam` migration need a real arm64 Pi. Until then it is written but
  unproven.
- **`release.yml`.** It cannot run before it is on `master`, so the first merge
  is also its first test. The two assumptions most likely to break it are named
  in the spec: whether `ubuntu-24.04-arm` runners are available to this repo,
  and whether `actions/setup-go` works inside the container. If the first run
  fails, the fallback is `ubuntu-22.04-arm` on the bare runner — glibc 2.35 is
  older than the Pi's 2.36, so the binary still runs.

- [ ] **Hand back to the user for the two things only they can do**

1. `gh repo rename hindsight` (or Settings → Rename). GitHub redirects the old URL.
2. Confirm before merging, since the first push to `master` cuts a real release.
