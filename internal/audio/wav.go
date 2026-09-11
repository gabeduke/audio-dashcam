package audio

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
)

const wavHeaderBytes = 44

// PeakData is the precomputed waveform served to the client so a phone can
// draw a take's waveform without downloading the audio itself.
type PeakData struct {
	Version    int     `json:"version"`
	Channels   int     `json:"channels"`
	SampleRate int     `json:"sample_rate"`
	Duration   float64 `json:"duration"`
	Buckets    int     `json:"buckets"`
	// Data holds one array per channel of alternating min,max pairs in -1..1.
	Data [][]float32 `json:"data"`
}

// peakBuckets is the horizontal resolution of a stored waveform. Fixed rather
// than proportional to length so a long take costs no more to draw than a
// short one.
const peakBuckets = 1024

// WriteWAV extracts `pick` (zero-based channel indices) from an interleaved
// int32 block and writes a 32-bit PCM WAV, returning the peak data computed in
// the same pass.
//
// The previous implementation used binary.Write on the whole slice, which goes
// through reflection and is dramatically slower than encoding into a buffered
// writer directly.
func WriteWAV(path string, data []int32, srcChannels int, pick []int, sampleRate int) (*PeakData, error) {
	if srcChannels <= 0 {
		return nil, fmt.Errorf("srcChannels must be positive")
	}
	frames := len(data) / srcChannels
	if frames == 0 {
		return nil, fmt.Errorf("no audio frames to write")
	}
	outCh := len(pick)
	if outCh == 0 {
		return nil, fmt.Errorf("no output channels selected")
	}
	for _, c := range pick {
		if c < 0 || c >= srcChannels {
			return nil, fmt.Errorf("channel %d out of range for %d-channel source", c+1, srcChannels)
		}
	}

	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	w := bufio.NewWriterSize(f, 1<<20)

	dataBytes := uint32(frames * outCh * 4)
	if err := writeWAVHeader(w, dataBytes, outCh, sampleRate); err != nil {
		return nil, err
	}

	pk := newPeakAccumulator(outCh, frames)
	var scratch [4]byte

	for i := 0; i < frames; i++ {
		base := i * srcChannels
		for oc, c := range pick {
			s := data[base+c]
			binary.LittleEndian.PutUint32(scratch[:], uint32(s))
			if _, err := w.Write(scratch[:]); err != nil {
				return nil, err
			}
			pk.add(oc, i, float32(float64(s)/2147483648.0))
		}
	}

	if err := w.Flush(); err != nil {
		return nil, err
	}
	if err := f.Sync(); err != nil {
		return nil, err
	}

	return pk.finish(sampleRate, frames), nil
}

func writeWAVHeader(w *bufio.Writer, dataBytes uint32, channels, sampleRate int) error {
	le := binary.LittleEndian
	var b [wavHeaderBytes]byte

	copy(b[0:4], "RIFF")
	le.PutUint32(b[4:8], dataBytes+36)
	copy(b[8:12], "WAVE")
	copy(b[12:16], "fmt ")
	le.PutUint32(b[16:20], 16)                            // fmt chunk size
	le.PutUint16(b[20:22], 1)                             // PCM
	le.PutUint16(b[22:24], uint16(channels))              //
	le.PutUint32(b[24:28], uint32(sampleRate))            //
	le.PutUint32(b[28:32], uint32(sampleRate*channels*4)) // byte rate
	le.PutUint16(b[32:34], uint16(channels*4))            // block align
	le.PutUint16(b[34:36], 32)                            // bits per sample
	copy(b[36:40], "data")
	le.PutUint32(b[40:44], dataBytes)

	_, err := w.Write(b[:])
	return err
}

// peakAccumulator bins samples into fixed buckets as they stream past.
type peakAccumulator struct {
	channels   int
	bucketSize int
	min, max   [][]float32
	curBucket  int
	curMin     []float32
	curMax     []float32
	curSamples int
}

func newPeakAccumulator(channels, frames int) *peakAccumulator {
	bs := frames / peakBuckets
	if bs < 1 {
		bs = 1
	}
	p := &peakAccumulator{
		channels:   channels,
		bucketSize: bs,
		min:        make([][]float32, channels),
		max:        make([][]float32, channels),
		curMin:     make([]float32, channels),
		curMax:     make([]float32, channels),
	}
	p.resetBucket()
	return p
}

func (p *peakAccumulator) resetBucket() {
	inf := float32(math.Inf(1))
	for c := 0; c < p.channels; c++ {
		p.curMin[c] = inf
		p.curMax[c] = -inf
	}
	p.curSamples = 0
}

func (p *peakAccumulator) add(ch, frame int, v float32) {
	if b := frame / p.bucketSize; b != p.curBucket {
		p.flush()
		p.curBucket = b
	}
	if v < p.curMin[ch] {
		p.curMin[ch] = v
	}
	if v > p.curMax[ch] {
		p.curMax[ch] = v
	}
	p.curSamples++
}

func (p *peakAccumulator) flush() {
	if p.curSamples == 0 {
		return
	}
	for c := 0; c < p.channels; c++ {
		mn, mx := p.curMin[c], p.curMax[c]
		if math.IsInf(float64(mn), 1) {
			mn = 0
		}
		if math.IsInf(float64(mx), -1) {
			mx = 0
		}
		p.min[c] = append(p.min[c], mn)
		p.max[c] = append(p.max[c], mx)
	}
	p.resetBucket()
}

func (p *peakAccumulator) finish(sampleRate, frames int) *PeakData {
	p.flush()
	data := make([][]float32, p.channels)
	for c := 0; c < p.channels; c++ {
		n := len(p.min[c])
		flat := make([]float32, 0, n*2)
		for i := 0; i < n; i++ {
			flat = append(flat, p.min[c][i], p.max[c][i])
		}
		data[c] = flat
	}
	buckets := 0
	if p.channels > 0 {
		buckets = len(p.min[0])
	}
	return &PeakData{
		Version:    1,
		Channels:   p.channels,
		SampleRate: sampleRate,
		Duration:   float64(frames) / float64(sampleRate),
		Buckets:    buckets,
		Data:       data,
	}
}

// WritePeaks serialises peak data next to its take.
func WritePeaks(path string, pd *PeakData) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriterSize(f, 1<<18)
	if err := json.NewEncoder(w).Encode(pd); err != nil {
		return err
	}
	return w.Flush()
}

// WAVInfo is the subset of a WAV header needed to describe a take.
type WAVInfo struct {
	Channels      int
	SampleRate    int
	BitsPerSample int
	DataBytes     int64
	// DataOffset is the byte offset of the first sample, i.e. just past the
	// data chunk header. 44 for every take this app writes.
	DataOffset int64
}

// Duration reports the take length in seconds.
func (w WAVInfo) Duration() float64 {
	bytesPerFrame := w.Channels * w.BitsPerSample / 8
	if bytesPerFrame <= 0 || w.SampleRate <= 0 {
		return 0
	}
	return float64(w.DataBytes) / float64(bytesPerFrame*w.SampleRate)
}

// Frames reports the number of sample frames in the data chunk.
func (w WAVInfo) Frames() int64 {
	bpf := int64(w.Channels * w.BitsPerSample / 8)
	if bpf <= 0 {
		return 0
	}
	return w.DataBytes / bpf
}

// ReadWAVInfo parses a WAV header by walking its chunks. Reading the real
// header rather than assuming the current capture config means takes recorded
// under a different channel layout still report an honest duration.
func ReadWAVInfo(path string) (WAVInfo, error) {
	var info WAVInfo

	f, err := os.Open(path)
	if err != nil {
		return info, err
	}
	defer f.Close()

	var riff [12]byte
	if _, err := io.ReadFull(f, riff[:]); err != nil {
		return info, err
	}
	if string(riff[0:4]) != "RIFF" || string(riff[8:12]) != "WAVE" {
		return info, fmt.Errorf("not a RIFF/WAVE file")
	}

	le := binary.LittleEndian
	var hdr [8]byte
	sawFmt := false

	for {
		if _, err := io.ReadFull(f, hdr[:]); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			return info, err
		}
		id := string(hdr[0:4])
		size := int64(le.Uint32(hdr[4:8]))

		switch id {
		case "fmt ":
			buf := make([]byte, min64(size, 40))
			if _, err := io.ReadFull(f, buf); err != nil {
				return info, err
			}
			if len(buf) < 16 {
				return info, fmt.Errorf("short fmt chunk")
			}
			info.Channels = int(le.Uint16(buf[2:4]))
			info.SampleRate = int(le.Uint32(buf[4:8]))
			info.BitsPerSample = int(le.Uint16(buf[14:16]))
			sawFmt = true
			if rest := size - int64(len(buf)); rest > 0 {
				if _, err := f.Seek(rest, io.SeekCurrent); err != nil {
					return info, err
				}
			}
		case "data":
			info.DataBytes = size
			if !sawFmt {
				return info, fmt.Errorf("data chunk before fmt chunk")
			}
			off, err := f.Seek(0, io.SeekCurrent)
			if err != nil {
				return info, err
			}
			info.DataOffset = off
			return info, nil
		default:
			if _, err := f.Seek(size+size%2, io.SeekCurrent); err != nil {
				return info, err
			}
		}
	}

	if !sawFmt {
		return info, fmt.Errorf("no fmt chunk")
	}
	return info, nil
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
