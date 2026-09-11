// internal/audio/render_test.go
package audio

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderArgsStereoTake(t *testing.T) {
	info := WAVInfo{Channels: 2, SampleRate: 48000, BitsPerSample: 32, DataBytes: 48000 * 2 * 4 * 10}
	got := RenderArgs("/takes/jam_a.wav", info, []int{0, 1}, 48000, 48000*4)
	want := []string{
		"-hide_banner", "-loglevel", "error", "-i", "/takes/jam_a.wav",
		"-af", "atrim=start_sample=48000:end_sample=192000,asetpts=PTS-STARTPTS,afade=t=in:st=0:d=0.003,afade=t=out:st=2.997:d=0.003",
		"-c:a", "libmp3lame", "-b:a", "128k", "-f", "mp3", "-",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("args =\n  %s\nwant\n  %s", strings.Join(got, " "), strings.Join(want, " "))
	}
}

func TestRenderArgsMultichannelTakeAppendsPan(t *testing.T) {
	info := WAVInfo{Channels: 8, SampleRate: 48000, BitsPerSample: 32}
	got := RenderArgs("/t.wav", info, []int{0, 1}, 0, 48000)
	af := got[6]
	if !strings.HasSuffix(af, ",pan=stereo|c0=c0|c1=c1") {
		t.Errorf("pan not appended after the fades: %q", af)
	}
	if !strings.Contains(af, "afade=t=out:st=0.997:d=0.003,pan=") {
		t.Errorf("fade-out start for a 1s region should be 0.997: %q", af)
	}
}

func TestRenderArgsMonoTakeUpmixes(t *testing.T) {
	info := WAVInfo{Channels: 1, SampleRate: 48000, BitsPerSample: 32}
	got := RenderArgs("/t.wav", info, []int{0}, 0, 48000)
	if !strings.HasSuffix(got[6], ",pan=stereo|c0=c0|c1=c0") {
		t.Errorf("mono should upmix like MakePreview: %q", got[6])
	}
}

func TestRenderFilename(t *testing.T) {
	cases := []struct {
		base     string
		from, to int64
		whole    bool
		want     string
	}{
		{"the good one", 48000 * 12, 48000*41 + 24000, false, "the good one 0.12-0.41.mp3"},
		{"jam_2026-09-10_103541", 0, 48000 * 75, false, "jam_2026-09-10_103541 0.00-1.15.mp3"},
		{"the good one", 0, 0, true, "the good one.mp3"},
		{`we're "live"/tonight?`, 0, 0, true, "were livetonight.mp3"},
		{"   ", 0, 0, true, "take.mp3"},
	}
	for _, c := range cases {
		if got := RenderFilename(c.base, c.from, c.to, 48000, c.whole); got != c.want {
			t.Errorf("RenderFilename(%q) = %q, want %q", c.base, got, c.want)
		}
	}
}

func TestRenderMP3Validates(t *testing.T) {
	p := filepath.Join(t.TempDir(), "jam_r.wav")
	if _, err := WriteWAV(p, make([]int32, 48000*2*2), 2, []int{0, 1}, 48000); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := RenderMP3(context.Background(), &buf, []int{0, 1}, p, 0, 48000*2+1); !errors.Is(err, ErrRange) {
		t.Errorf("past end: %v, want ErrRange", err)
	}
	if err := RenderMP3(context.Background(), &buf, []int{0, 1}, p, 10, 10); !errors.Is(err, ErrRange) {
		t.Errorf("empty: %v, want ErrRange", err)
	}
	// Cap: a 2s file cannot exceed 600s, so fake the check with a long info
	// through the exported constant instead -- covered in the API test with
	// a long take. Here only the bit-depth guard remains:
	p16 := filepath.Join(t.TempDir(), "jam_16.wav")
	write16BitWAV(t, p16, 1000, 2, 48000)
	if err := RenderMP3(context.Background(), &buf, []int{0, 1}, p16, 0, 100); !errors.Is(err, ErrBitDepth) {
		t.Errorf("16-bit: %v, want ErrBitDepth", err)
	}
	if buf.Len() != 0 {
		t.Error("a rejected render must write nothing")
	}
}

// TestRenderMP3ProducesAnMP3 needs ffmpeg. It is skipped where ffmpeg is
// absent (CI's ubuntu runner has it via apt in ci.yml; see Task 2).
func TestRenderMP3ProducesAnMP3(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	p := filepath.Join(t.TempDir(), "jam_r.wav")
	data := make([]int32, 48000*2*2)
	for i := range data {
		data[i] = int32((i%97)-48) << 20 // audible-ish noise so lame has work to do
	}
	if _, err := WriteWAV(p, data, 2, []int{0, 1}, 48000); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := RenderMP3(context.Background(), &buf, []int{0, 1}, p, 4800, 48000*2-4800); err != nil {
		t.Fatalf("RenderMP3: %v", err)
	}
	b := buf.Bytes()
	if len(b) < 4096 {
		t.Fatalf("only %d bytes", len(b))
	}
	if !(bytes.HasPrefix(b, []byte("ID3")) || (b[0] == 0xFF && b[1]&0xE0 == 0xE0)) {
		t.Errorf("body does not start with ID3 or an MP3 sync word: % x", b[:4])
	}
}
