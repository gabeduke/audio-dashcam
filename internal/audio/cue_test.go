package audio

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// testWAV writes a small real take using the app's own writer, so these tests
// exercise exactly the file layout WriteWAV produces.
func testWAV(t *testing.T, frames int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "jam_cue.wav")
	data := make([]int32, frames*2)
	for i := range data {
		data[i] = int32(i)
	}
	if _, err := WriteWAV(p, data, 2, []int{0, 1}, 48000); err != nil {
		t.Fatalf("WriteWAV: %v", err)
	}
	return p
}

func TestReadCuesOnAFileWithNoneReturnsEmpty(t *testing.T) {
	got, err := ReadCues(testWAV(t, 100))
	if err != nil {
		t.Fatalf("ReadCues: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

func TestWriteThenReadCuesRoundTrips(t *testing.T) {
	p := testWAV(t, 1000)
	want := []uint64{0, 480, 999}
	if err := WriteCues(p, want); err != nil {
		t.Fatalf("WriteCues: %v", err)
	}
	got, err := ReadCues(p)
	if err != nil {
		t.Fatalf("ReadCues: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("cue[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}

func TestWriteCuesSortsAndDeduplicates(t *testing.T) {
	p := testWAV(t, 1000)
	if err := WriteCues(p, []uint64{500, 100, 500}); err != nil {
		t.Fatalf("WriteCues: %v", err)
	}
	got, _ := ReadCues(p)
	if len(got) != 2 || got[0] != 100 || got[1] != 500 {
		t.Errorf("got %v, want [100 500]", got)
	}
}

func TestWriteCuesRejectsAnOffsetPastTheEnd(t *testing.T) {
	p := testWAV(t, 100)
	if err := WriteCues(p, []uint64{100}); err == nil {
		t.Error("WriteCues accepted an offset equal to the frame count, want an error")
	}
}

func TestWriteCuesLeavesTheAudioByteIdentical(t *testing.T) {
	p := testWAV(t, 500)
	before, err := ReadWAVInfo(p)
	if err != nil {
		t.Fatalf("ReadWAVInfo: %v", err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	audioBefore := append([]byte(nil), raw[wavHeaderBytes:wavHeaderBytes+int(before.DataBytes)]...)

	if err := WriteCues(p, []uint64{10, 20}); err != nil {
		t.Fatalf("WriteCues: %v", err)
	}

	after, err := ReadWAVInfo(p)
	if err != nil {
		t.Fatalf("ReadWAVInfo after: %v", err)
	}
	if after.DataBytes != before.DataBytes {
		t.Errorf("DataBytes = %d, want %d", after.DataBytes, before.DataBytes)
	}
	raw2, _ := os.ReadFile(p)
	audioAfter := raw2[wavHeaderBytes : wavHeaderBytes+int(after.DataBytes)]
	for i := range audioBefore {
		if audioBefore[i] != audioAfter[i] {
			t.Fatalf("audio byte %d changed", i)
		}
	}
}

func TestWriteCuesTwiceReplacesRatherThanAppends(t *testing.T) {
	p := testWAV(t, 1000)
	if err := WriteCues(p, []uint64{1, 2, 3, 4}); err != nil {
		t.Fatalf("first WriteCues: %v", err)
	}
	sizeAfterFour, _ := os.Stat(p)

	if err := WriteCues(p, []uint64{7}); err != nil {
		t.Fatalf("second WriteCues: %v", err)
	}
	got, _ := ReadCues(p)
	if len(got) != 1 || got[0] != 7 {
		t.Errorf("got %v, want [7]", got)
	}
	sizeAfterOne, _ := os.Stat(p)
	if sizeAfterOne.Size() >= sizeAfterFour.Size() {
		t.Errorf("file did not shrink: %d then %d", sizeAfterFour.Size(), sizeAfterOne.Size())
	}
}

func TestWriteCuesWithNoOffsetsStripsTheChunk(t *testing.T) {
	p := testWAV(t, 1000)
	if err := WriteCues(p, []uint64{5}); err != nil {
		t.Fatalf("WriteCues: %v", err)
	}
	if err := WriteCues(p, nil); err != nil {
		t.Fatalf("WriteCues(nil): %v", err)
	}
	got, _ := ReadCues(p)
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
	if _, err := ReadWAVInfo(p); err != nil {
		t.Errorf("file no longer parses: %v", err)
	}
}

// Simulates a crash after the cue bytes were written but before the RIFF size
// was patched up. The trailing bytes fall outside the RIFF extent, so the file
// must still read as a valid cue-less take.
func TestAnUnpatchedRIFFSizeLeavesAValidTake(t *testing.T) {
	p := testWAV(t, 1000)
	if err := WriteCues(p, []uint64{10, 20}); err != nil {
		t.Fatalf("WriteCues: %v", err)
	}
	end, err := cueInsertPoint(p)
	if err != nil {
		t.Fatalf("cueInsertPoint: %v", err)
	}

	f, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	var sz [4]byte
	binary.LittleEndian.PutUint32(sz[:], uint32(end-8))
	if _, err := f.WriteAt(sz[:], 4); err != nil {
		t.Fatalf("patch: %v", err)
	}
	f.Close()

	info, err := ReadWAVInfo(p)
	if err != nil {
		t.Fatalf("file no longer parses: %v", err)
	}
	if info.Channels != 2 {
		t.Errorf("Channels = %d, want 2", info.Channels)
	}
	got, err := ReadCues(p)
	if err != nil {
		t.Fatalf("ReadCues: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty: the chunk is outside the RIFF extent", got)
	}
}

func TestReadCuesRejectsANonWAV(t *testing.T) {
	p := filepath.Join(t.TempDir(), "not.wav")
	if err := os.WriteFile(p, []byte("this is not a RIFF file at all"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := ReadCues(p); err == nil {
		t.Error("ReadCues accepted a non-WAV, want an error")
	}
}
