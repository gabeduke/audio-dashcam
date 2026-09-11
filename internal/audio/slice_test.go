package audio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"path/filepath"
	"testing"
)

func TestWriteSlice16ProducesAFadedSixteenBitWAV(t *testing.T) {
	p := filepath.Join(t.TempDir(), "jam_s.wav")
	data := make([]int32, 48000*2)
	for i := range data {
		data[i] = 1 << 30 // half scale
	}
	if _, err := WriteWAV(p, data, 2, []int{0, 1}, 48000); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteSlice16(&buf, p, 1000, 3000); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	if int64(len(b)) != SliceBytes(WAVInfo{Channels: 2, SampleRate: 48000, BitsPerSample: 32}, 1000, 3000) {
		t.Errorf("len = %d, want SliceBytes = %d", len(b), SliceBytes(WAVInfo{Channels: 2, SampleRate: 48000, BitsPerSample: 32}, 1000, 3000))
	}
	le := binary.LittleEndian
	if string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" || le.Uint16(b[34:36]) != 16 || le.Uint16(b[22:24]) != 2 {
		t.Fatalf("bad header: bits=%d ch=%d", le.Uint16(b[34:36]), le.Uint16(b[22:24]))
	}
	if le.Uint32(b[40:44]) != 2000*2*2 {
		t.Errorf("data size = %d, want %d", le.Uint32(b[40:44]), 2000*2*2)
	}
	// Frame 0 silent, mid exactly half scale in 16-bit (1<<14), last faded.
	s := func(frame int) int16 { return int16(le.Uint16(b[44+frame*4:])) }
	if s(0) != 0 || s(1000) != 1<<14 || s(1999) >= 1<<14 || s(1999) == 0 {
		t.Errorf("samples: first=%d mid=%d last=%d", s(0), s(1000), s(1999))
	}
}

func TestWriteSlice16Limits(t *testing.T) {
	p := filepath.Join(t.TempDir(), "jam_s.wav")
	if _, err := WriteWAV(p, make([]int32, 48000*61*2), 2, []int{0, 1}, 48000); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteSlice16(&buf, p, 0, 48000*60+1); !errors.Is(err, ErrTooLong) {
		t.Errorf("61s: err = %v, want ErrTooLong", err)
	}
	if err := WriteSlice16(&buf, p, 48000*61-10, 48000*61+1); !errors.Is(err, ErrRange) {
		t.Errorf("past end: err = %v, want ErrRange", err)
	}
}
