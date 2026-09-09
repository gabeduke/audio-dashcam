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
	// where a source re-enumerates hardware. The caller guarantees nothing is
	// live when it calls Reset -- it runs only after an Open that returned an
	// error -- so an implementation may assume there is nothing to tear down
	// first. That guarantee only holds if Open itself never leaves the source
	// open on any of its error paths.
	Reset() error

	// Shutdown releases process-wide resources. Called once, from Stop.
	Shutdown() error
}
