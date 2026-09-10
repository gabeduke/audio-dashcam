package audio

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
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

// --- Regression coverage added after review: unbounded allocation from
// file-controlled fields (parseCueChunk's record count, and ReadCues'
// chunk-size-driven read), and an untested write order for WriteCues. ---

// A corrupted record count must not drive parseCueChunk's allocation: on
// real disk corruption the count field can be any 32-bit value, and sizing a
// slice's capacity directly off it would ask for up to ~32GB before the
// per-record bounds check inside the loop ever runs -- a daemon-killing
// allocation, not a recoverable error. The chunk here is otherwise tiny (just
// the 4-byte count field, no room for even one record), so if this test ever
// regresses to allocating off the raw count again, it will do so at the
// worst-case value a single corrupted disk byte could produce.
func TestReadCuesToleratesACorruptRecordCount(t *testing.T) {
	p := testWAV(t, 100)
	dataEnd, err := cueInsertPoint(p)
	if err != nil {
		t.Fatalf("cueInsertPoint: %v", err)
	}

	chunk := make([]byte, 8+4)
	copy(chunk[0:4], "cue ")
	binary.LittleEndian.PutUint32(chunk[4:8], 4)           // declared payload: 4 bytes
	binary.LittleEndian.PutUint32(chunk[8:12], 0xFFFFFFFF) // count: the worst case

	f, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.WriteAt(chunk, dataEnd); err != nil {
		t.Fatalf("write chunk: %v", err)
	}
	var sz [4]byte
	binary.LittleEndian.PutUint32(sz[:], uint32(dataEnd+int64(len(chunk))-8))
	if _, err := f.WriteAt(sz[:], 4); err != nil {
		t.Fatalf("patch size: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	got, err := ReadCues(p)
	if err != nil {
		t.Fatalf("ReadCues: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
	// The 4-byte chunk can hold zero records, so a correctly clamped count
	// yields cap(got) == 0. An implementation that sized the allocation off
	// the raw count would report a capacity near 4 billion -- this bound
	// only needs to distinguish "clamped" from "not clamped".
	if cap(got) > 4 {
		t.Errorf("cap(got) = %d: the corrupted count leaked into the allocation size", cap(got))
	}
}

// A "cue " chunk larger than any legitimate write (512 flags, ~12KB) is
// refused rather than read, so a corrupted chunk id -- for example a "data"
// chunk's id with one bit flipped into "cue ", carrying the data chunk's own,
// possibly gigabyte-scale, size along with it -- can't turn into a
// multi-hundred-megabyte read and allocation. The chunk here is deliberately
// built to look valid (a real, well-formed one-record chunk, just padded out
// past the trust threshold): if ReadCues trusted it instead of refusing it,
// it would decode a spurious cue point at offset 42, so this test would catch
// a regression even if the cap check were merely loosened rather than
// removed outright.
func TestReadCuesSkipsAnImplausiblyLargeCueChunk(t *testing.T) {
	p := testWAV(t, 100)
	dataEnd, err := cueInsertPoint(p)
	if err != nil {
		t.Fatalf("cueInsertPoint: %v", err)
	}

	small := buildCueChunk([]uint64{42}) // 8-byte header + 4-byte count + one real record
	oversized := cueChunkMaxBytes + 1024
	chunk := make([]byte, 8+oversized)
	copy(chunk, small)
	binary.LittleEndian.PutUint32(chunk[4:8], uint32(oversized)) // lie about the size

	f, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.WriteAt(chunk, dataEnd); err != nil {
		t.Fatalf("write chunk: %v", err)
	}
	var sz [4]byte
	binary.LittleEndian.PutUint32(sz[:], uint32(dataEnd+int64(len(chunk))-8))
	if _, err := f.WriteAt(sz[:], 4); err != nil {
		t.Fatalf("patch size: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	got, err := ReadCues(p)
	if err != nil {
		t.Fatalf("ReadCues: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty: an oversized cue chunk must be refused, not trusted", got)
	}
}

// journalFile is a cueFile double that records every call it receives, in
// order, so a test can assert the exact write sequence writeCueChunk
// performs. That sequence is the entire crash-safety property (see
// writeCueChunk's doc comment): every other test in this file inspects the
// file only after WriteCues has already returned successfully, so none of
// them can observe it -- deleting the down-patch and reordering to
// write-then-patch-once left all nine of them green (verified by hand while
// fixing this; see the task report).
type journalFile struct {
	log  []string
	size int64
}

func (j *journalFile) WriteAt(b []byte, off int64) (int, error) {
	if off == 4 && len(b) == 4 {
		// This is always a RIFF-size patch: decode the size it encodes back
		// to the "end" it declares, so the log reads as intent rather than
		// as raw bytes.
		end := int64(binary.LittleEndian.Uint32(b)) + 8
		j.log = append(j.log, fmt.Sprintf("patch(end=%d)", end))
	} else {
		j.log = append(j.log, fmt.Sprintf("write@%d(len=%d)", off, len(b)))
	}
	if grow := off + int64(len(b)); grow > j.size {
		j.size = grow
	}
	return len(b), nil
}

func (j *journalFile) Truncate(size int64) error {
	j.log = append(j.log, "truncate")
	j.size = size
	return nil
}

func (j *journalFile) Sync() error {
	j.log = append(j.log, "sync")
	return nil
}

func (j *journalFile) Stat() (os.FileInfo, error) {
	return journalFileInfo{size: j.size}, nil
}

type journalFileInfo struct{ size int64 }

func (fi journalFileInfo) Name() string       { return "journal" }
func (fi journalFileInfo) Size() int64        { return fi.size }
func (fi journalFileInfo) Mode() os.FileMode  { return 0 }
func (fi journalFileInfo) ModTime() time.Time { return time.Time{} }
func (fi journalFileInfo) IsDir() bool        { return false }
func (fi journalFileInfo) Sys() any           { return nil }

// TestWriteCueChunkPatchesDownBeforeWritingTheChunk pins the write order that
// is the entire crash-safety story: the RIFF size must be patched down (and
// fsynced) before the cue chunk is written, and patched up (and fsynced)
// only after that.
func TestWriteCueChunkPatchesDownBeforeWritingTheChunk(t *testing.T) {
	const dataEnd = int64(1000)
	jf := &journalFile{size: dataEnd}

	if err := writeCueChunk(jf, dataEnd, []uint64{5, 10}); err != nil {
		t.Fatalf("writeCueChunk: %v", err)
	}

	chunkLen := int64(len(buildCueChunk([]uint64{5, 10})))
	newEnd := dataEnd + chunkLen
	want := []string{
		fmt.Sprintf("patch(end=%d)", dataEnd),
		"sync",
		fmt.Sprintf("write@%d(len=%d)", dataEnd, chunkLen),
		"sync",
		fmt.Sprintf("patch(end=%d)", newEnd),
		"sync",
		"sync", // unconditional final sync; nothing grew past newEnd, so no truncate
	}
	if !slices.Equal(jf.log, want) {
		t.Fatalf("write order:\n got  %v\n want %v", jf.log, want)
	}

	firstPatch, firstBigWrite := -1, -1
	bigWritePrefix := fmt.Sprintf("write@%d", dataEnd)
	for i, e := range jf.log {
		if firstPatch < 0 && strings.HasPrefix(e, "patch(") {
			firstPatch = i
		}
		if firstBigWrite < 0 && strings.HasPrefix(e, bigWritePrefix) {
			firstBigWrite = i
		}
	}
	if firstPatch < 0 || firstBigWrite < 0 || firstPatch > firstBigWrite {
		t.Fatalf("a write at or past dataEnd must not precede the first down-patch: log = %v", jf.log)
	}
}

// TestWriteCueChunkWithNoOffsetsNeverWritesAChunk covers the offs-empty path
// separately: it must still patch the RIFF size down and fsync, and it must
// truncate away any tail a previous, larger cue chunk left behind -- but it
// must never write a chunk.
func TestWriteCueChunkWithNoOffsetsNeverWritesAChunk(t *testing.T) {
	const dataEnd = int64(1000)
	jf := &journalFile{size: dataEnd + 50} // as if a previous cue chunk left a tail

	if err := writeCueChunk(jf, dataEnd, nil); err != nil {
		t.Fatalf("writeCueChunk: %v", err)
	}

	want := []string{
		fmt.Sprintf("patch(end=%d)", dataEnd),
		"sync",
		"truncate",
		"sync",
	}
	if !slices.Equal(jf.log, want) {
		t.Fatalf("write order:\n got  %v\n want %v", jf.log, want)
	}
	for _, e := range jf.log {
		if strings.HasPrefix(e, "write@") {
			t.Errorf("wrote a chunk with no offsets: log = %v", jf.log)
		}
	}
}
