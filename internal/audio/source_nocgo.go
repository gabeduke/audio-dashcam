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
