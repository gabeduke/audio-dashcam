// internal/audio/slice.go
package audio

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// MaxSliceSeconds caps an audition slice. 60s of stereo decodes to ~23MB of
// float32 in a browser, which a phone handles; the page auditions longer
// regions through the mp3 preview instead.
const MaxSliceSeconds = 60

// ErrTooLong is a slice over MaxSliceSeconds.
var ErrTooLong = errors.New("slice longer than the audition cap")

// SliceBytes is the byte length of the WAV WriteSlice16 emits for [from, to).
func SliceBytes(info WAVInfo, from, to int64) int64 {
	return 44 + (to-from)*int64(info.Channels)*2
}

// WriteSlice16 writes frames [from, to) of a take to w as a complete 16-bit
// PCM WAV with the same 3ms fades a cut applies, so what the page auditions
// is exactly what a cut will produce. 16-bit because browsers -- iOS Safari
// in particular -- do not reliably decode 32-bit integer WAV; the audition
// is not archival. Conversion is an arithmetic shift, no dither.
func WriteSlice16(w io.Writer, path string, from, to int64) error {
	info, err := ReadWAVInfo(path)
	if err != nil {
		return err
	}
	if info.BitsPerSample != 32 {
		return ErrBitDepth
	}
	if from < 0 || to <= from || to > info.Frames() {
		return fmt.Errorf("%w: [%d, %d) of %d frames", ErrRange, from, to, info.Frames())
	}
	if to-from > int64(MaxSliceSeconds*info.SampleRate) {
		return ErrTooLong
	}
	ch := info.Channels
	total := to - from
	fade := FadeFrames(info.SampleRate)

	bw := bufio.NewWriterSize(w, 1<<16)
	le := binary.LittleEndian
	dataBytes := uint32(total * int64(ch) * 2)
	if err := writeWAVHeader(bw, dataBytes, ch, info.SampleRate, 16); err != nil {
		return err
	}

	var s [2]byte
	_, err = ReadFrames(path, from, to, 1<<14, func(block []int32, first int64) error {
		applyFades(block, first-from, total, ch, fade)
		for _, v := range block {
			le.PutUint16(s[:], uint16(int16(v>>16)))
			if _, err := bw.Write(s[:]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return bw.Flush()
}
