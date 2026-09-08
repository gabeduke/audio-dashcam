package audio

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v3/disk"
)

// ErrLowDisk is returned when a save is refused for lack of space. The Python
// implementation had this guard; the Go rewrite dropped it while the README
// still claimed it existed.
var ErrLowDisk = errors.New("insufficient disk space")

// ErrNoAudio is returned when the ring has nothing in it yet.
var ErrNoAudio = errors.New("no audio buffered yet")

// Saver turns a slice of the ring into a take on disk, plus a preview and
// waveform peaks.
type Saver struct {
	cap *Capture

	mu        sync.Mutex
	lastSaved string
	saving    bool
}

func NewSaver(c *Capture) *Saver { return &Saver{cap: c} }

func (s *Saver) LastSaved() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastSaved
}

func (s *Saver) Saving() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saving
}

// FreeGB reports free space on the output volume.
func (s *Saver) FreeGB() (float64, float64) {
	u, err := disk.Usage(s.cap.cfg.OutputDir)
	if err != nil {
		return 0, 0
	}
	return float64(u.Free) / (1024 * 1024 * 1024), u.UsedPercent
}

// Save writes the most recent `seconds` of audio. seconds <= 0 means the whole
// ring. It returns the take's filename.
func (s *Saver) Save(seconds float64) (string, error) {
	cfg := s.cap.cfg

	freeGB, _ := s.FreeGB()
	if freeGB < cfg.MinFreeGB {
		return "", fmt.Errorf("%w: %.2f GB free, need %.2f GB", ErrLowDisk, freeGB, cfg.MinFreeGB)
	}

	frames := 0
	if seconds > 0 {
		frames = int(seconds * float64(cfg.SampleRate))
	}
	data, gotFrames := s.cap.Ring().Snapshot(frames)
	if gotFrames == 0 {
		return "", ErrNoAudio
	}

	s.mu.Lock()
	s.saving = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.saving = false
		s.mu.Unlock()
	}()

	ts := time.Now().Format("2006-01-02_150405")
	name := fmt.Sprintf("jam_%s.wav", ts)
	wavPath := filepath.Join(cfg.OutputDir, name)

	pick := cfg.OutChannels()
	start := time.Now()
	peaks, err := WriteWAV(wavPath, data, cfg.Channels, pick, cfg.SampleRate)
	if err != nil {
		os.Remove(wavPath)
		return "", fmt.Errorf("write wav: %w", err)
	}
	log.Printf("[*] saved %s — %.1fs, %d ch, %s in %s",
		name, float64(gotFrames)/float64(cfg.SampleRate), len(pick),
		sizeOf(wavPath), time.Since(start).Round(time.Millisecond))

	if err := WritePeaks(peaksPath(wavPath), peaks); err != nil {
		log.Printf("[!] peaks for %s: %v", name, err)
	}

	s.mu.Lock()
	s.lastSaved = name
	s.mu.Unlock()

	go s.makePreview(wavPath, len(pick))
	go s.prune()

	return name, nil
}

// makePreview renders the mp3 proxy. The channel mapping is explicit: a bare
// `-ac 2` on an 8-channel file makes ffmpeg assume a 7.1 layout, which folds
// channel 3 into a mono centre and discards channel 4 as LFE entirely — which
// is exactly what made previews sound wrong.
func (s *Saver) makePreview(wavPath string, outCh int) {
	cfg := s.cap.cfg
	mp3Path := previewPath(wavPath)
	tmp := mp3Path + ".tmp"

	args := []string{"-y", "-hide_banner", "-loglevel", "error", "-i", wavPath}
	if outCh > 2 {
		// Take the configured pair out of a multichannel take by index.
		l, r := cfg.SaveChannels[0], cfg.SaveChannels[0]
		if len(cfg.SaveChannels) > 1 {
			r = cfg.SaveChannels[1]
		}
		args = append(args, "-filter_complex", fmt.Sprintf("pan=stereo|c0=c%d|c1=c%d", l, r))
	} else if outCh == 1 {
		args = append(args, "-af", "pan=stereo|c0=c0|c1=c0")
	}
	// -f mp3 is required because the temp file is written with a .tmp
	// extension, which ffmpeg cannot infer a muxer from.
	args = append(args, "-c:a", "libmp3lame", "-b:a", "128k", "-f", "mp3", tmp)

	// nice so a long encode never competes with the audio thread.
	cmd := exec.Command("nice", append([]string{"-n", "10", "ffmpeg"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("[!] preview for %s failed: %v %s", filepath.Base(wavPath), err, strings.TrimSpace(string(out)))
		os.Remove(tmp)
		return
	}
	if err := os.Rename(tmp, mp3Path); err != nil {
		log.Printf("[!] preview rename: %v", err)
		return
	}
	log.Printf("[*] preview ready: %s", filepath.Base(mp3Path))
}

// prune enforces MAX_SAVES by deleting the oldest takes and their sidecars.
func (s *Saver) prune() {
	max := s.cap.cfg.MaxSaves
	if max <= 0 {
		return
	}
	takes, err := ListTakes(s.cap.cfg.OutputDir)
	if err != nil || len(takes) <= max {
		return
	}
	for _, t := range takes[max:] {
		log.Printf("[*] pruning %s (over MAX_SAVES=%d)", t.Name, max)
		RemoveTake(s.cap.cfg.OutputDir, t.Name)
	}
}

// Take describes one saved recording.
type Take struct {
	Name       string    `json:"name"`
	SizeMB     float64   `json:"size_mb"`
	Duration   float64   `json:"duration_seconds"`
	Created    time.Time `json:"created"`
	Channels   int       `json:"channels"`
	SampleRate int       `json:"sample_rate"`
	HasPreview bool      `json:"has_preview"`
	HasPeaks   bool      `json:"has_peaks"`
	Preview    string    `json:"preview_name"`

	// From the sidecar. Name above is the filename; Label is what the user
	// called it.
	Label   string `json:"label"`
	Starred bool   `json:"starred"`
	Trim    *Trim  `json:"trim,omitempty"`
}

// ListTakes returns takes newest first. Duration and layout come from each
// file's own header, so takes recorded under an older channel configuration
// still report correctly.
func ListTakes(dir string) ([]Take, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Take
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".wav" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		full := filepath.Join(dir, e.Name())
		prev := filepath.Base(previewPath(full))

		t := Take{
			Name:       e.Name(),
			SizeMB:     float64(info.Size()) / (1024 * 1024),
			Created:    info.ModTime(),
			HasPreview: exists(previewPath(full)),
			HasPeaks:   exists(peaksPath(full)),
			Preview:    prev,
		}
		if wi, err := ReadWAVInfo(full); err == nil {
			t.Duration = wi.Duration()
			t.Channels = wi.Channels
			t.SampleRate = wi.SampleRate
		}

		m := ReadMeta(full)
		t.Label = m.Label
		t.Starred = m.Starred
		t.Trim = m.Trim

		out = append(out, t)
	}
	// Starred first, then newest. Starring is how a take is kept in reach once
	// newer ones have pushed it down the list.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Starred != out[j].Starred {
			return out[i].Starred
		}
		return out[i].Created.After(out[j].Created)
	})
	return out, nil
}

// RemoveTake deletes a take and its sidecar files.
func RemoveTake(dir, name string) {
	base := filepath.Join(dir, filepath.Base(name))
	os.Remove(base)
	os.Remove(previewPath(base))
	os.Remove(peaksPath(base))
}

func previewPath(wav string) string { return strings.TrimSuffix(wav, ".wav") + "_preview.mp3" }
func peaksPath(wav string) string   { return strings.TrimSuffix(wav, ".wav") + ".peaks.json" }

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func sizeOf(p string) string {
	fi, err := os.Stat(p)
	if err != nil {
		return "?"
	}
	mb := float64(fi.Size()) / (1024 * 1024)
	return fmt.Sprintf("%.1f MB", mb)
}
