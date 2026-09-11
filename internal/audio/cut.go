package audio

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrTooShort is a region that would be nothing but fades.
var ErrTooShort = errors.New("region shorter than two fades")

// CutRequest names a region of a source take, in the source's frames.
type CutRequest struct {
	Source     string // filename of the source take, e.g. jam_2026-09-10_221441.wav
	StartFrame int64
	EndFrame   int64
	Label      string // optional; defaults to "<source label or stem> cut"
}

// Cut writes frames [StartFrame, EndFrame) of the source as a new take in
// dir with a 3ms declick fade at each edge, plus its peaks file and a
// sidecar that inherits the source's BPM and the flags inside the region,
// rebased. It streams, so memory is one block whatever the region length.
// The source is never modified. The preview mp3 is the caller's job (it
// needs ffmpeg and the capture config); see MakePreview.
//
// Returns the new take's filename. On any failure after the file is created,
// the partial WAV is removed so a half-written take never appears in the list.
func Cut(dir string, req CutRequest, now time.Time) (string, error) {
	srcPath := filepath.Join(dir, filepath.Base(req.Source))
	info, err := ReadWAVInfo(srcPath)
	if err != nil {
		return "", err
	}
	if info.BitsPerSample != 32 {
		return "", ErrBitDepth
	}
	if req.StartFrame < 0 || req.EndFrame <= req.StartFrame || req.EndFrame > info.Frames() {
		return "", fmt.Errorf("%w: [%d, %d) of %d frames", ErrRange, req.StartFrame, req.EndFrame, info.Frames())
	}
	fade := FadeFrames(info.SampleRate)
	total := req.EndFrame - req.StartFrame
	if total < 2*fade+1 {
		return "", ErrTooShort
	}

	name := fmt.Sprintf("jam_%s.wav", now.Format("2006-01-02_150405"))
	outPath := filepath.Join(dir, name)
	// Two cuts in the same second must not collide: the second becomes _2.
	// Overwriting a take is the one failure that loses audio outright.
	for n := 2; exists(outPath); n++ {
		name = fmt.Sprintf("jam_%s_%d.wav", now.Format("2006-01-02_150405"), n)
		outPath = filepath.Join(dir, name)
	}
	if err := writeCutWAV(srcPath, outPath, info, req.StartFrame, req.EndFrame, fade); err != nil {
		os.Remove(outPath)
		os.Remove(peaksPath(outPath))
		return "", err
	}

	srcMeta := ReadMeta(srcPath)
	m := Meta{Version: MetaVersion}
	m.Label = strings.TrimSpace(req.Label)
	if m.Label == "" {
		base := srcMeta.Label
		if base == "" {
			base = strings.TrimSuffix(filepath.Base(req.Source), ".wav")
		}
		m.Label = base + " cut"
	}
	if srcMeta.BPM != nil {
		bpm := *srcMeta.BPM
		m.BPM = &bpm
	}
	m.Source = &CutSource{Name: filepath.Base(req.Source), StartFrame: req.StartFrame, EndFrame: req.EndFrame}
	var flags []Flag
	for _, f := range srcMeta.Flags {
		if f.Frame >= req.StartFrame && f.Frame < req.EndFrame {
			flags = append(flags, Flag{Frame: f.Frame - req.StartFrame, Label: f.Label})
		}
	}
	if err := WriteMeta(outPath, m); err != nil {
		os.Remove(outPath)
		os.Remove(peaksPath(outPath))
		return "", fmt.Errorf("sidecar: %w", err)
	}
	// stampFlags writes the flags into the sidecar and the cue chunk, and
	// never fails the take (it logs), matching a save.
	stampFlags(outPath, flags)
	log.Printf("[*] cut %s from %s [%d, %d) — %.1fs", name, req.Source, req.StartFrame, req.EndFrame,
		float64(total)/float64(info.SampleRate))
	return name, nil
}

// writeCutWAV streams the region through the fades into a canonical 44-byte
// header WAV and writes the peaks file beside it.
func writeCutWAV(srcPath, outPath string, info WAVInfo, from, to, fade int64) error {
	total := to - from
	ch := info.Channels
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriterSize(f, 1<<18)
	if err := writeWAVHeader(w, uint32(total*int64(ch)*4), ch, info.SampleRate); err != nil {
		return err
	}
	acc := newPeakAccumulator(ch, int(total))
	le := binary.LittleEndian
	var raw [4]byte
	_, err = ReadFrames(srcPath, from, to, 1<<14, func(block []int32, first int64) error {
		rel := first - from
		applyFades(block, rel, total, ch, fade)
		n := len(block) / ch
		for i := 0; i < n; i++ {
			for c := 0; c < ch; c++ {
				v := block[i*ch+c]
				le.PutUint32(raw[:], uint32(v))
				if _, err := w.Write(raw[:]); err != nil {
					return err
				}
				// The same divisor WriteWAV uses, so a cut's peaks and a
				// saved take's peaks are computed identically.
				acc.add(c, int(rel)+i, float32(float64(v)/2147483648.0))
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := WritePeaks(peaksPath(outPath), acc.finish(info.SampleRate, int(total))); err != nil {
		log.Printf("[!] peaks for %s: %v", filepath.Base(outPath), err)
	}
	return nil
}
