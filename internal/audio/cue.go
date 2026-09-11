package audio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
)

// cueRecordBytes is the size of one RIFF cue point record.
const cueRecordBytes = 24

// cueChunkMaxBytes bounds how large a declared "cue " chunk we'll trust
// enough to read. Writes here cap at 512 flags (about 12KB); anything far
// larger cannot be a cue chunk this app wrote. Reading it anyway would size
// an allocation off a corrupted (or adversarial) disk value -- for example a
// "data" chunk id with one bit flipped into "cue ", carrying the data
// chunk's own, possibly gigabyte-scale, size along with it.
const cueChunkMaxBytes = 1 << 20 // 1 MiB

// ReadCues returns the sample offsets of a WAV's cue points, ascending. A file
// with no cue chunk yields an empty slice and no error.
func ReadCues(path string) ([]uint64, error) {
	pts, err := ReadCuePoints(path)
	if err != nil {
		return nil, err
	}
	out := make([]uint64, 0, len(pts))
	for _, p := range pts {
		out = append(out, uint64(p.Frame))
	}
	return out, nil
}

// ReadCuePoints returns a WAV's cue points with their labels, ascending by
// frame. Labels come from a LIST/adtl chunk's "labl" records, matched to cue
// points by dwIdentifier; a cue without one has an empty label. A file with
// no cue chunk yields an empty slice and no error.
func ReadCuePoints(path string) ([]Flag, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	end, err := riffExtent(f)
	if err != nil {
		return nil, err
	}

	le := binary.LittleEndian
	var hdr [8]byte
	pos := int64(12)
	var recs []cueRecord
	labels := map[uint32]string{}
	seenCue := false
	for pos+8 <= end {
		if _, err := f.Seek(pos, io.SeekStart); err != nil {
			return nil, err
		}
		if _, err := io.ReadFull(f, hdr[:]); err != nil {
			break
		}
		id := string(hdr[0:4])
		size := int64(le.Uint32(hdr[4:8]))
		if size < 0 || pos+8+size > end {
			break
		}
		// A cue chunk larger than any legitimate write is treated as noise
		// rather than read, so a corrupted or flipped chunk id can't turn
		// into a multi-hundred-megabyte allocation. Fall through to the same
		// skip-and-keep-walking path used for chunks we don't recognise.
		switch {
		case id == "cue " && size <= cueChunkMaxBytes && !seenCue:
			buf := make([]byte, size)
			if _, err := io.ReadFull(f, buf); err != nil {
				return nil, fmt.Errorf("reading cue chunk: %w", err)
			}
			recs, err = parseCueRecords(buf)
			if err != nil {
				return nil, err
			}
			seenCue = true
		case id == "LIST" && size <= cueChunkMaxBytes:
			buf := make([]byte, size)
			if _, err := io.ReadFull(f, buf); err != nil {
				return nil, fmt.Errorf("reading LIST chunk: %w", err)
			}
			parseLabelChunk(buf, labels)
		}
		pos += 8 + size + size%2
	}

	out := make([]Flag, 0, len(recs))
	for _, r := range recs {
		out = append(out, Flag{Frame: int64(r.offset), Label: labels[r.id]})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Frame < out[j].Frame })
	return out, nil
}

type cueRecord struct {
	id     uint32
	offset uint32
}

// parseLabelChunk collects "labl" records from a LIST chunk body into labels,
// keyed by cue id. Anything but an adtl list, and any malformed record, is
// ignored: labels are decoration, never a reason to fail a read.
func parseLabelChunk(buf []byte, labels map[uint32]string) {
	if len(buf) < 4 || string(buf[0:4]) != "adtl" {
		return
	}
	le := binary.LittleEndian
	pos := 4
	for pos+8 <= len(buf) {
		id := string(buf[pos : pos+4])
		size := int(le.Uint32(buf[pos+4 : pos+8]))
		body := pos + 8
		if size < 0 || body+size > len(buf) {
			return
		}
		if id == "labl" && size >= 4 {
			cueID := le.Uint32(buf[body : body+4])
			text := buf[body+4 : body+size]
			if i := bytes.IndexByte(text, 0); i >= 0 {
				text = text[:i]
			}
			labels[cueID] = string(text)
		}
		pos = body + size + size%2
	}
}

func parseCueChunk(buf []byte) ([]uint64, error) {
	recs, err := parseCueRecords(buf)
	if err != nil {
		return nil, err
	}
	out := make([]uint64, 0, len(recs))
	for _, r := range recs {
		out = append(out, uint64(r.offset))
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func parseCueRecords(buf []byte) ([]cueRecord, error) {
	if len(buf) < 4 {
		return nil, errors.New("malformed cue chunk")
	}
	le := binary.LittleEndian
	n := le.Uint32(buf[0:4])
	// n comes off disk independently of buf's own (already-bounded) length,
	// so a corrupted count must not size this allocation: clamp it to how
	// many records buf could actually hold before reserving capacity for
	// them. Without this, a single corrupted dword can ask for gigabytes.
	if max := (len(buf) - 4) / cueRecordBytes; int64(n) > int64(max) {
		n = uint32(max)
	}
	out := make([]cueRecord, 0, n)
	for i := uint32(0); i < n; i++ {
		rec := 4 + int(i)*cueRecordBytes
		if rec+cueRecordBytes > len(buf) {
			break
		}
		id := le.Uint32(buf[rec : rec+4])
		pos := le.Uint32(buf[rec+4 : rec+8])
		off := le.Uint32(buf[rec+20 : rec+24])
		// Tolerate writers that fill only dwPosition. bento does this too, and
		// matching it is what keeps the two tools interoperable.
		if off == 0 && pos != 0 {
			off = pos
		}
		out = append(out, cueRecord{id: id, offset: off})
	}
	return out, nil
}

// buildCueChunk serialises a RIFF "cue " chunk. Both dwPosition and
// dwSampleOffset are filled so that readers which consult either one -- bento
// reads dwSampleOffset and falls back to dwPosition -- agree about the file.
func buildCueChunk(offsets []uint64) []byte {
	n := len(offsets)
	size := 4 + cueRecordBytes*n
	buf := make([]byte, 8+size)
	le := binary.LittleEndian
	copy(buf[0:4], "cue ")
	le.PutUint32(buf[4:8], uint32(size))
	le.PutUint32(buf[8:12], uint32(n))
	for i, o := range offsets {
		rec := buf[12+cueRecordBytes*i:]
		le.PutUint32(rec[0:4], uint32(i+1)) // dwIdentifier
		le.PutUint32(rec[4:8], uint32(o))   // dwPosition
		copy(rec[8:12], "data")             // fccDataChunk
		// dwChunkStart and dwBlockStart stay zero: the data chunk is not
		// wave-list compressed, which is what those fields address.
		le.PutUint32(rec[20:24], uint32(o)) // dwSampleOffset
	}
	return buf
}

// buildLabelChunk serialises a LIST/adtl chunk with one "labl" record per
// labelled flag, keyed by the same 1-based dwIdentifier buildCueChunk assigns
// to the flag at that index. Flags with an empty label get no record. Returns
// nil when nothing is labelled, so a bare-flag file carries no LIST chunk.
func buildLabelChunk(flags []Flag) []byte {
	le := binary.LittleEndian
	var body []byte
	for i, f := range flags {
		if f.Label == "" {
			continue
		}
		text := append([]byte(f.Label), 0)
		size := 4 + len(text)
		body = append(body, "labl"...)
		body = le.AppendUint32(body, uint32(size))
		body = le.AppendUint32(body, uint32(i+1))
		body = append(body, text...)
		if size%2 == 1 {
			body = append(body, 0)
		}
	}
	if body == nil {
		return nil
	}
	buf := make([]byte, 0, 12+len(body))
	buf = append(buf, "LIST"...)
	buf = le.AppendUint32(buf, uint32(4+len(body)))
	buf = append(buf, "adtl"...)
	return append(buf, body...)
}

// riffExtent returns the byte offset one past the end of the RIFF form.
func riffExtent(f *os.File) (int64, error) {
	var riff [12]byte
	if _, err := f.ReadAt(riff[:], 0); err != nil {
		return 0, fmt.Errorf("not a RIFF/WAVE file: %w", err)
	}
	if string(riff[0:4]) != "RIFF" || string(riff[8:12]) != "WAVE" {
		return 0, errors.New("not a RIFF/WAVE file")
	}
	fi, err := f.Stat()
	if err != nil {
		return 0, err
	}
	end := int64(binary.LittleEndian.Uint32(riff[4:8])) + 8
	if end <= 0 || end > fi.Size() {
		end = fi.Size()
	}
	return end, nil
}

// cueInsertPoint returns the offset just past the data chunk -- where a cue
// chunk belongs. It requires the layout WriteWAV produces: header, data, and at
// most a trailing cue chunk and its LIST/adtl label chunk. Anything else (a foreign file with chunks after
// data, or a cue chunk before it) is refused rather than rewritten, because
// rewriting a multi-hundred-megabyte take in place is not something this app
// ever needs to do. There is no ingest path; every take here is one it wrote.
func cueInsertPoint(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	end, err := riffExtent(f)
	if err != nil {
		return 0, err
	}

	le := binary.LittleEndian
	var hdr [8]byte
	pos := int64(12)
	dataEnd := int64(-1)
	for pos+8 <= end {
		if _, err := f.Seek(pos, io.SeekStart); err != nil {
			return 0, err
		}
		if _, err := io.ReadFull(f, hdr[:]); err != nil {
			break
		}
		id := string(hdr[0:4])
		size := int64(le.Uint32(hdr[4:8]))
		if size < 0 || pos+8+size > end {
			return 0, errors.New("corrupt chunk size")
		}
		next := pos + 8 + size + size%2
		switch id {
		case "data":
			dataEnd = next
		case "cue ", "LIST":
			if dataEnd < 0 {
				return 0, errors.New("cue chunk before data chunk: unsupported layout")
			}
		default:
			if dataEnd >= 0 {
				return 0, fmt.Errorf("unsupported chunk %q after data", id)
			}
		}
		pos = next
	}
	if dataEnd < 0 {
		return 0, errors.New("no data chunk")
	}
	return dataEnd, nil
}

// WriteCues makes the file contain exactly one cue chunk holding the given
// sample offsets, or none when offsets is empty.
//
// The chunk is appended at the end of the data chunk and the 4-byte RIFF size
// is patched, so the cost is a few kilobytes of I/O whatever the take's size --
// a full rewrite would cost gigabytes of memory on the 15-minute takes this app
// produces. The audio bytes are never touched.
//
// The write order makes a crash harmless. The RIFF size is first patched *down*
// to exclude the cue region, so the file immediately reads as a valid cue-less
// WAV; the chunk is written; only then is the size patched *up* to include it.
// A crash at any point leaves either a valid take with no cues or a valid take
// with them, never a corrupt one.
func WriteCues(path string, offsets []uint64) error {
	flags := make([]Flag, 0, len(offsets))
	for _, o := range offsets {
		flags = append(flags, Flag{Frame: int64(o)})
	}
	return WriteCuePoints(path, flags)
}

// WriteCuePoints is WriteCues with labels: labelled flags also get a "labl"
// record in a LIST/adtl chunk immediately after the cue chunk, which is where
// DAWs and samplers look for marker names. Both chunks are written as one
// region, so the crash-safety argument in writeCueChunk covers them together.
func WriteCuePoints(path string, flags []Flag) error {
	info, err := ReadWAVInfo(path)
	if err != nil {
		return err
	}
	frames := int64(0)
	if bpf := int64(info.Channels * info.BitsPerSample / 8); bpf > 0 {
		frames = info.DataBytes / bpf
	}

	for _, f := range flags {
		if f.Frame < 0 || f.Frame >= frames {
			return fmt.Errorf("cue offset %d out of range (file has %d frames)", f.Frame, frames)
		}
	}
	flags = NormalizeFlags(flags)

	dataEnd, err := cueInsertPoint(path)
	if err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()

	return writeCueChunk(f, dataEnd, flags)
}

// cueFile is the subset of *os.File that writeCueChunk needs. The seam
// exists so a test can record the exact call sequence: the write order is
// the entire crash-safety property (see writeCueChunk), and without
// something to intercept the calls nothing pins it -- a refactor could
// silently reorder the writes and every existing test would stay green,
// because they only ever inspect the file after WriteCues returns
// successfully, never the sequence that got it there. *os.File already
// satisfies this.
type cueFile interface {
	WriteAt(b []byte, off int64) (int, error)
	Truncate(size int64) error
	Sync() error
	Stat() (os.FileInfo, error)
}

// writeCueChunk performs the on-disk write sequence that makes a crash
// harmless at any point: patch the RIFF size *down* first (so the file
// immediately reads as a valid cue-less WAV and anything at or past dataEnd
// falls outside the RIFF extent), write the chunk into that now-ignored
// region, then patch the RIFF size back *up* to include it. Each step is
// fsynced before the next begins. Finally, any tail left by a longer
// previous cue chunk is truncated away.
func writeCueChunk(f cueFile, dataEnd int64, flags []Flag) error {
	// 1. Shrink the RIFF extent so anything at or past dataEnd is ignored.
	if err := patchRIFFSize(f, dataEnd); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}

	newEnd := dataEnd
	if len(flags) > 0 {
		offs := make([]uint64, 0, len(flags))
		for _, fl := range flags {
			offs = append(offs, uint64(fl.Frame))
		}
		chunk := append(buildCueChunk(offs), buildLabelChunk(flags)...)
		// 2. Write the chunk into the now-ignored region.
		if _, err := f.WriteAt(chunk, dataEnd); err != nil {
			return err
		}
		if err := f.Sync(); err != nil {
			return err
		}
		newEnd = dataEnd + int64(len(chunk))
		// 3. Grow the RIFF extent to take it in.
		if err := patchRIFFSize(f, newEnd); err != nil {
			return err
		}
		if err := f.Sync(); err != nil {
			return err
		}
	}

	// 4. Drop any tail left by a longer previous cue chunk.
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if fi.Size() > newEnd {
		if err := f.Truncate(newEnd); err != nil {
			return err
		}
	}
	return f.Sync()
}

// patchRIFFSize rewrites the 4-byte size field so the RIFF form ends at end.
func patchRIFFSize(f cueFile, end int64) error {
	var sz [4]byte
	binary.LittleEndian.PutUint32(sz[:], uint32(end-8))
	_, err := f.WriteAt(sz[:], 4)
	return err
}
