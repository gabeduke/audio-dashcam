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
