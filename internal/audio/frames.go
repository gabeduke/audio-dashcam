// internal/audio/frames.go
package audio

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

var (
	// ErrRange is a frame window outside the file or inverted.
	ErrRange = errors.New("frame range out of bounds")
	// ErrBitDepth is a WAV this app did not write; every take is 32-bit.
	ErrBitDepth = errors.New("only 32-bit PCM takes are supported")
)

// ReadFrames streams the interleaved int32 samples of frames [from, to) to
// fn in blocks of at most blockFrames frames, in order. It reads only those
// bytes -- never the whole file -- so a 15-minute take costs the same memory
// as a 1-second one. The block passed to fn is reused between calls; copy it
// if it must outlive the call.
func ReadFrames(path string, from, to int64, blockFrames int, fn func(block []int32, firstFrame int64) error) (WAVInfo, error) {
	info, err := ReadWAVInfo(path)
	if err != nil {
		return info, err
	}
	if info.BitsPerSample != 32 {
		return info, ErrBitDepth
	}
	if from < 0 || to <= from || to > info.Frames() {
		return info, fmt.Errorf("%w: [%d, %d) of %d frames", ErrRange, from, to, info.Frames())
	}
	if blockFrames <= 0 {
		blockFrames = 1 << 14
	}

	f, err := os.Open(path)
	if err != nil {
		return info, err
	}
	defer f.Close()

	ch := int64(info.Channels)
	if _, err := f.Seek(info.DataOffset+from*ch*4, io.SeekStart); err != nil {
		return info, err
	}
	r := bufio.NewReaderSize(f, 1<<18)
	le := binary.LittleEndian

	block := make([]int32, blockFrames*int(ch))
	raw := make([]byte, len(block)*4)
	for pos := from; pos < to; {
		n := int64(blockFrames)
		if to-pos < n {
			n = to - pos
		}
		nb := int(n * ch * 4)
		if _, err := io.ReadFull(r, raw[:nb]); err != nil {
			return info, fmt.Errorf("reading frames at %d: %w", pos, err)
		}
		for i := 0; i < int(n*ch); i++ {
			block[i] = int32(le.Uint32(raw[i*4:]))
		}
		if err := fn(block[:n*ch], pos); err != nil {
			return info, err
		}
		pos += n
	}
	return info, nil
}
