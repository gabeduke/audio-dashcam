# USB Hot-Plug Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the dashcam recover on its own when the audio interface is powered off and back on, instead of failing forever until someone restarts the service.

**Architecture:** PortAudio enumerates devices exactly once, inside `Pa_Initialize`, and never rescans. Wrap that global init/terminate pair in a small `paLifecycle` type with a `Rescan()` method, and call it from the supervisor's retry path so each retry sees the current hardware.

**Tech Stack:** Go 1.x, cgo, `github.com/gordonklaus/portaudio`, systemd user service on Debian 13 (arm64).

---

## The bug, and the evidence for it

Observed on the running Pi on 2026-09-08:

```
Sep 07 23:19:19  [*] capture live on "EP-136: USB Audio (hw:2,0)" — ring 120s, 8 ch @ 48000 Hz
Sep 08 00:02:53  [!] capture stalled (no callback for 2s) — restarting
Sep 08 00:02:53  [!] capture: open "EP-136: USB Audio (hw:2,0)": Illegal combination of I/O devices (retry in 1s)
   ... identical line every 16s for the next 50 minutes ...
```

with, at the same moment:

```
$ arecord -l
**** List of CAPTURE Hardware Devices ****
        (nothing — no capture devices at all)

$ lsusb | grep -i ep-136
        (nothing — the interface is off the bus)
```

**Why that combination is conclusive.** `pickDevice` in `audio/capture.go` skips
any device with fewer than `cfg.Channels` inputs, and if nothing matches
`DeviceMatch` it fails with a *different* message — `no input device with >=8
channels`. The log shows the `open "EP-136: ..."` message instead, which means
`portaudio.Devices()` **still returned an EP-136 entry** while ALSA had no
capture devices whatsoever. The list is stale. ALSA then refuses the open
because card 2 is gone, which surfaces as:

```
ALSA lib confmisc.c:165:(snd_config_get_card) Cannot get card index for 2
```

Nothing in `supervise()` ever re-initialises PortAudio, so the stale list is
what every subsequent retry sees, forever.

**Blast radius.** Any power cycle of the interface — which is a normal thing to
do to a piece of music gear — silently bricks capture until someone SSHes in
and restarts the unit. The UI correctly reports the error, so this is a
recovery bug, not a reporting bug.

---

## File Structure

| File | Responsibility |
|---|---|
| `v2-go/audio/palifecycle.go` (create) | Owns PortAudio's global init/terminate pair. Idempotent `Init`/`Term`, plus `Rescan` to force re-enumeration. Injectable init/term funcs so it is testable without hardware. |
| `v2-go/audio/palifecycle_test.go` (create) | Unit tests for the above using fake init/term funcs. No audio device required. |
| `v2-go/audio/capture.go` (modify) | Uses `paLifecycle` instead of calling `portaudio.Initialize`/`Terminate` directly; calls `Rescan()` on the retry path. |

`paLifecycle` is deliberately its own file: it is the only place in the codebase
that reasons about PortAudio's process-global state, and it is the only part of
this fix that can be tested without an audio interface plugged in.

---

### Task 1: `paLifecycle` — idempotent init/terminate with rescan

**Files:**
- Create: `v2-go/audio/palifecycle.go`
- Test: `v2-go/audio/palifecycle_test.go`

- [x] **Step 1: Write the failing tests**

Create `v2-go/audio/palifecycle_test.go`:

```go
package audio

import (
	"errors"
	"testing"
)

// recorder captures the order of init/term calls so a test can assert on the
// sequence, not just the counts. Rescan's whole purpose is that terminate
// happens before initialise, and only an ordered log can prove that.
type recorder struct {
	calls   []string
	initErr error
	termErr error
}

func (r *recorder) init() error {
	r.calls = append(r.calls, "init")
	return r.initErr
}

func (r *recorder) term() error {
	r.calls = append(r.calls, "term")
	return r.termErr
}

func TestInitIsIdempotent(t *testing.T) {
	r := &recorder{}
	p := newPALifecycle(r.init, r.term)

	if err := p.Init(); err != nil {
		t.Fatalf("first Init: %v", err)
	}
	if err := p.Init(); err != nil {
		t.Fatalf("second Init: %v", err)
	}

	if len(r.calls) != 1 || r.calls[0] != "init" {
		t.Fatalf("want exactly one init, got %v", r.calls)
	}
}

func TestTermIsIdempotentAndSkipsWhenDown(t *testing.T) {
	r := &recorder{}
	p := newPALifecycle(r.init, r.term)

	// Terminating a PortAudio that was never initialised is a double free in
	// the C library, so it must not reach termFn at all.
	if err := p.Term(); err != nil {
		t.Fatalf("Term while down: %v", err)
	}
	if len(r.calls) != 0 {
		t.Fatalf("Term while down must not call termFn, got %v", r.calls)
	}

	if err := p.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := p.Term(); err != nil {
		t.Fatalf("Term: %v", err)
	}
	if err := p.Term(); err != nil {
		t.Fatalf("second Term: %v", err)
	}

	want := []string{"init", "term"}
	if len(r.calls) != len(want) {
		t.Fatalf("want %v, got %v", want, r.calls)
	}
	for i := range want {
		if r.calls[i] != want[i] {
			t.Fatalf("want %v, got %v", want, r.calls)
		}
	}
}

func TestRescanTerminatesThenInitialises(t *testing.T) {
	r := &recorder{}
	p := newPALifecycle(r.init, r.term)

	if err := p.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := p.Rescan(); err != nil {
		t.Fatalf("Rescan: %v", err)
	}

	want := []string{"init", "term", "init"}
	if len(r.calls) != len(want) {
		t.Fatalf("want %v, got %v", want, r.calls)
	}
	for i := range want {
		if r.calls[i] != want[i] {
			t.Fatalf("want %v, got %v", want, r.calls)
		}
	}
}

func TestRescanFromDownStateStillInitialises(t *testing.T) {
	// supervise() may call Rescan before anything successfully came up, e.g.
	// when the very first Init failed. It must still leave PortAudio up.
	r := &recorder{}
	p := newPALifecycle(r.init, r.term)

	if err := p.Rescan(); err != nil {
		t.Fatalf("Rescan from down: %v", err)
	}

	want := []string{"init"}
	if len(r.calls) != len(want) || r.calls[0] != want[0] {
		t.Fatalf("want %v, got %v", want, r.calls)
	}
}

func TestInitErrorLeavesLifecycleDownSoRetryWorks(t *testing.T) {
	r := &recorder{initErr: errors.New("boom")}
	p := newPALifecycle(r.init, r.term)

	if err := p.Init(); err == nil {
		t.Fatal("want an error from Init")
	}

	// A failed Init must not latch "up", or every later retry would no-op and
	// the process would never recover.
	r.initErr = nil
	if err := p.Init(); err != nil {
		t.Fatalf("retry Init: %v", err)
	}
	if len(r.calls) != 2 {
		t.Fatalf("want two init attempts, got %v", r.calls)
	}
}
```

- [x] **Step 2: Run the tests to verify they fail**

Run:
```bash
cd /Users/gabeduke/projects/audio-dashcam/v2-go && go test ./audio/ -run 'PALifecycle|Init|Term|Rescan' -v
```
Expected: compile failure — `undefined: newPALifecycle`.

- [x] **Step 3: Write the implementation**

Create `v2-go/audio/palifecycle.go`:

```go
package audio

import (
	"fmt"
	"sync"
)

// paLifecycle owns PortAudio's process-global initialise/terminate pair.
//
// PortAudio enumerates devices exactly once, inside Pa_Initialize, and never
// rescans. An interface that is powered off therefore stays in the device list
// forever, pointing at an ALSA card index that no longer exists, and every
// open attempt fails with "Illegal combination of I/O devices". Terminating
// and initialising again is the only way to see hardware that appeared or
// disappeared after start-up, which is what Rescan is for.
type paLifecycle struct {
	mu     sync.Mutex
	up     bool
	initFn func() error
	termFn func() error
}

func newPALifecycle(initFn, termFn func() error) *paLifecycle {
	return &paLifecycle{initFn: initFn, termFn: termFn}
}

// Init brings PortAudio up. Calling it when it is already up is a no-op, so no
// caller has to track state to stay safe.
func (p *paLifecycle) Init() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.up {
		return nil
	}
	if err := p.initFn(); err != nil {
		// Deliberately stay "down" so the next attempt really retries.
		return fmt.Errorf("portaudio init: %w", err)
	}
	p.up = true
	return nil
}

// Term takes PortAudio down. Terminating twice is a double free in the C
// library, so a second call is deliberately a no-op.
func (p *paLifecycle) Term() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.up {
		return nil
	}
	// Marked down before the call: if Pa_Terminate fails there is nothing
	// useful to do with a half-torn-down library except initialise it again,
	// and latching "up" would block exactly that.
	p.up = false
	if err := p.termFn(); err != nil {
		return fmt.Errorf("portaudio terminate: %w", err)
	}
	return nil
}

// Rescan forces PortAudio to re-enumerate devices, which is what picks up an
// interface that was unplugged or power-cycled while the process was running.
func (p *paLifecycle) Rescan() error {
	if err := p.Term(); err != nil {
		return err
	}
	return p.Init()
}
```

- [x] **Step 4: Run the tests to verify they pass**

Run:
```bash
cd /Users/gabeduke/projects/audio-dashcam/v2-go && go test ./audio/ -run 'Init|Term|Rescan' -v
```
Expected: all five tests PASS.

- [x] **Step 5: Commit**

```bash
cd /Users/gabeduke/projects/audio-dashcam
git add v2-go/audio/palifecycle.go v2-go/audio/palifecycle_test.go
git commit -m "Add paLifecycle to own PortAudio init/terminate

PortAudio enumerates devices once at Pa_Initialize and never rescans, so a
power-cycled USB interface can only be seen again after a terminate and
re-initialise. Wrap that global pair in an idempotent type with a Rescan
method; wiring it into the supervisor is the next commit."
```

---

### Task 2: Use the lifecycle in Capture and rescan on retry

**Files:**
- Modify: `v2-go/audio/capture.go` (struct fields, `NewCapture`, `Start`, `Stop`, `supervise`)

- [x] **Step 1: Add the field to the Capture struct**

In `v2-go/audio/capture.go`, find the `Capture` struct and add `pa` immediately
after the `saver`-adjacent fields — specifically after the `levels` field:

```go
type Capture struct {
	cfg    *config.Config
	ring   *Ring
	levels *Levels
	pa     *paLifecycle

	free   chan []int32
	filled chan []int32
```

- [x] **Step 2: Construct it in NewCapture**

In `NewCapture`, add the `pa` field to the struct literal:

```go
	c := &Capture{
		cfg:    cfg,
		ring:   NewRing(cfg.RingFrames(), cfg.Channels),
		levels: NewLevels(cfg.Channels, cfg.SampleRate, 10),
		pa:     newPALifecycle(portaudio.Initialize, portaudio.Terminate),
		free:   make(chan []int32, blockPoolSize),
		filled: make(chan []int32, blockPoolSize),
		stop:   make(chan struct{}),
	}
```

- [x] **Step 3: Route Start and Stop through the lifecycle**

Replace the opening of `Start`:

```go
func (c *Capture) Start() error {
	if err := portaudio.Initialize(); err != nil {
		return fmt.Errorf("portaudio init: %w", err)
	}
```

with:

```go
func (c *Capture) Start() error {
	if err := c.pa.Init(); err != nil {
		return err
	}
```

and in `Stop`, replace `portaudio.Terminate()` with `_ = c.pa.Term()`:

```go
func (c *Capture) Stop() {
	c.stopOnce.Do(func() {
		close(c.stop)
		c.wg.Wait()
		c.closeStream()
		_ = c.pa.Term()
	})
}
```

- [x] **Step 4: Rescan on the retry path**

In `supervise()`, find the `openStream` failure branch and add the rescan
*after* the backoff sleep, so the next attempt sees a fresh device list:

```go
		if err := c.openStream(); err != nil {
			c.lastErr.Store(err.Error())
			c.healthy.Store(false)
			log.Printf("[!] capture: %v (retry in %s)", err, backoff)
			select {
			case <-c.stop:
				return
			case <-time.After(backoff):
			}
			if backoff < 15*time.Second {
				backoff *= 2
			}
			// PortAudio's device list is frozen at Pa_Initialize, so an
			// interface that was power-cycled is invisible until it is rebuilt.
			// Safe here because the open failed: no stream is live.
			if err := c.pa.Rescan(); err != nil {
				log.Printf("[!] portaudio rescan: %v", err)
			}
			continue
		}
```

Note for the implementer: the first retry after a *stall* still fails once
against the stale list before this rescan runs. That is intentional — it keeps
one code path and avoids rescanning on transient errors that are not about the
device disappearing. The cost is roughly one extra second before recovery.

- [x] **Step 5: Verify the build and the whole suite**

Run:
```bash
cd /Users/gabeduke/projects/audio-dashcam/v2-go && go build ./... && go vet ./... && gofmt -l . && go test ./...
```
Expected: no build errors, no vet output, no files listed by `gofmt -l`, all
tests pass (31 tests: the 26 existing plus the 5 new).

- [x] **Step 6: Confirm `portaudio` is still an import that is used**

`capture.go` still references `portaudio.OpenStream`, `portaudio.Devices`,
`portaudio.HighLatencyParameters`, `portaudio.DeviceInfo` and `portaudio.Stream`,
so the import stays. Confirm:

```bash
cd /Users/gabeduke/projects/audio-dashcam/v2-go && grep -c 'portaudio\.' audio/capture.go
```
Expected: a count of 7 or more.

- [x] **Step 7: Commit**

```bash
cd /Users/gabeduke/projects/audio-dashcam
git add v2-go/audio/capture.go
git commit -m "Rescan PortAudio devices when reopening the stream fails

A power-cycled interface was invisible forever: PortAudio's device list is
built once at Pa_Initialize, so every retry kept opening a stale hw index and
failing with 'Illegal combination of I/O devices'. Observed on the Pi as a
50-minute retry loop while arecord -l listed no capture devices at all.
Rescan after each failed open so the device is picked up when it returns."
```

---

### Task 3: Hardware verification

**Files:** none — this is a procedure run against the Pi.

This cannot be unit tested; it needs the physical interface. Do not mark the
work complete until this passes.

- [x] **Step 1: Deploy**

```bash
cd /Users/gabeduke/projects/audio-dashcam && ./deploy.sh
```
Expected: `[*] done`, and the trailing `curl` prints a status JSON blob.

- [x] **Step 2: Confirm a healthy baseline with the interface ON**

```bash
curl -s http://${DASHCAM_HOST#*@}/api/status | python3 -m json.tool | grep -E 'capture_healthy|last_error|device'
```
Expected:
```
"capture_healthy": true,
"last_error": "",
"device": "EP-136: USB Audio (hw:2,0)",
```

- [x] **Step 3: Start watching the log**

In one terminal:
```bash
ssh "$DASHCAM_HOST" 'journalctl _SYSTEMD_USER_UNIT=audio-dashcam.service -f' \
  | grep -viE "ALSA lib|Expression .* failed|snd_config"
```

- [x] **Step 4: Power the EP-136 off. Wait 15 seconds.**

Expected in the log:
```
[!] capture stalled (no callback for 2s) — restarting
[!] capture: open "EP-136: USB Audio (hw:2,0)": ... (retry in 1s)
```
and then the retries continue. This part is unchanged from the old behaviour.

- [x] **Step 5: Power the EP-136 back on. Wait up to 40 seconds.**

Expected — the line that proves the fix:
```
[*] capture live on "EP-136: USB Audio (hw:2,0)" — ring 120s, 8 ch @ 48000 Hz
```

**This is the pass/fail criterion.** Before the fix this line never appeared;
the retry loop ran for 50 minutes without recovering.

- [x] **Step 6: Confirm health came back without a restart**

```bash
curl -s http://${DASHCAM_HOST#*@}/api/status | python3 -m json.tool | grep -E 'capture_healthy|last_error'
ssh "$DASHCAM_HOST" 'systemctl --user show audio-dashcam.service -p MainPID --value'
```
Expected: `"capture_healthy": true`, `"last_error": ""`, and the **same PID as
before the power cycle** — proving it recovered rather than being restarted.

- [x] **Step 7: Note the card index**

If the interface came back on a different ALSA card (`hw:1,0` rather than
`hw:2,0`), record that in the commit message below — it confirms the rescan is
doing real work and that matching on the `EP-136` substring rather than a fixed
index is what makes it robust.

- [x] **Step 8: Commit the verification note**

```bash
cd /Users/gabeduke/projects/audio-dashcam
git commit --allow-empty -m "Verify hot-plug recovery on hardware

Powered the EP-136 off and on with the service untouched; capture came back
on its own and the PID was unchanged."
```

---

## Executed and verified 2026-09-08

All three tasks done. Commits `e0421ea` (paLifecycle), `bcff42d` (rescan on the
retry path), `998f8ab` (hardware verification). 34 tests green, `go vet`,
`gofmt` and `-race` clean.

**Hardware pass: recovered on its own in ~30s with the PID unchanged.** The
`[*] capture live on ...` line appeared, which it never did before the fix.

Three things worth carrying forward that the plan did not anticipate:

1. **The error message changes, and that is itself proof.** While the interface
   was off the log now reads `no input device with >=8 channels` rather than
   `open "EP-136: ...": Illegal combination of I/O devices`. That is the exact
   discriminator this plan used to diagnose the bug, running in reverse: the old
   message proved the device list was stale, the new one proves it is accurate.
2. **The card index did not change** (it came back on `hw:2,0`), so Step 7's
   robustness case is still unexercised. Substring matching on `EP-136` should
   cover it, but that is reasoning rather than evidence.
3. **The log is noisier.** Each rescan re-probes the host APIs, so every retry
   emits a block of JACK "cannot connect to server" lines. Harmless, but worth
   filtering if the log is ever read in anger.

The plan predicted 31 tests (26 + 5); the real number is 34, because three
tests were added to `levels_test.go` after this plan was written.

---

## Follow-ups deliberately not in this plan

- **Surfacing "waiting for the interface" distinctly from a hard error in the
  UI.** The top bar currently reads `capture error` for both. Worth doing, but
  it is a UI change, not a recovery change.
- **A `MAX_OPEN_FAILURES` that gives up and exits** so systemd restarts the
  unit. Not needed once rescan works, and it would trade a self-healing process
  for a restart loop.

---

## Self-review notes

- Spec coverage: the root cause (stale device list) is addressed by Task 2's
  `Rescan` call; the safety hazard it introduces (double `Pa_Terminate`) is
  addressed by Task 1's idempotent `Term`; hardware behaviour is Task 3.
- No placeholders: every step has the literal code or the literal command.
- Type consistency: `newPALifecycle`, `Init`, `Term`, `Rescan` and the field
  name `pa` are used identically in Tasks 1 and 2.
