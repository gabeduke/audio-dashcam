// internal/audio/render.go
package audio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"unicode"
)

// MaxRenderSeconds caps a share render. Ten minutes at 128 kbps is ~9.4MB,
// past what any messaging app accepts anyway.
const MaxRenderSeconds = 600

// ErrRenderTooLong is a range over MaxRenderSeconds.
var ErrRenderTooLong = errors.New("region longer than 10 minutes")

// renderFadeSeconds is the declick fade, the same 3ms FadeFrames gives a cut.
const renderFadeSeconds = 0.003

// RenderArgs builds ffmpeg's argument list (without the program name) for an
// MP3 of frames [from, to). The trim is a sample-exact filter rather than
// -ss/-t, so a render and a cut of the same region contain the same frames,
// and the two afades reproduce the cut's linear 3ms declick. When the take
// has more than two channels the configured pair is panned out, exactly as
// MakePreview does; a mono take is upmixed. Output goes to stdout.
func RenderArgs(wavPath string, info WAVInfo, saveChannels []int, from, to int64) []string {
	dur := float64(to-from) / float64(info.SampleRate)
	chain := fmt.Sprintf(
		"atrim=start_sample=%d:end_sample=%d,asetpts=PTS-STARTPTS,afade=t=in:st=0:d=%g,afade=t=out:st=%s:d=%g",
		from, to, renderFadeSeconds, trimFloat(dur-renderFadeSeconds), renderFadeSeconds,
	)
	if info.Channels > 2 {
		l, r := 0, 0
		if len(saveChannels) > 0 {
			l, r = saveChannels[0], saveChannels[0]
		}
		if len(saveChannels) > 1 {
			r = saveChannels[1]
		}
		chain += fmt.Sprintf(",pan=stereo|c0=c%d|c1=c%d", l, r)
	} else if info.Channels == 1 {
		chain += ",pan=stereo|c0=c0|c1=c0"
	}
	return []string{
		"-hide_banner", "-loglevel", "error", "-i", wavPath,
		"-af", chain,
		"-c:a", "libmp3lame", "-b:a", "128k", "-f", "mp3", "-",
	}
}

// trimFloat formats a seconds value without trailing zeros, so the golden
// argument list reads "2.997" rather than "2.997000".
func trimFloat(f float64) string {
	s := fmt.Sprintf("%.3f", f)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "" || s == "-" {
		return "0"
	}
	return s
}

// RenderFilename names a shared MP3: the take's label or stem, reduced to
// filename-safe characters, plus the region's span in m.ss, or nothing for
// the whole take. Messages shows the name, so it should read like a title.
func RenderFilename(base string, from, to int64, sampleRate int, whole bool) string {
	safe := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' || r == '-' || r == '_' || r == '.' {
			return r
		}
		return -1
	}, base)
	safe = strings.TrimSpace(safe)
	if safe == "" {
		safe = "take"
	}
	if whole {
		return safe + ".mp3"
	}
	return fmt.Sprintf("%s %s-%s.mp3", safe, mmss(from, sampleRate), mmss(to, sampleRate))
}

func mmss(frame int64, sampleRate int) string {
	s := frame / int64(sampleRate)
	return fmt.Sprintf("%d.%02d", s/60, s%60)
}

// RenderMP3 streams an MP3 of frames [from, to) to w straight from ffmpeg's
// stdout. Nothing touches disk. Validation mirrors WriteSlice16 so the
// handler can map errors the same way. ffmpeg is killed when ctx ends, which
// is how a client that closes the tab stops a ten-minute encode.
func RenderMP3(ctx context.Context, w io.Writer, saveChannels []int, wavPath string, from, to int64) error {
	info, err := ReadWAVInfo(wavPath)
	if err != nil {
		return err
	}
	if info.BitsPerSample != 32 {
		return ErrBitDepth
	}
	if from < 0 || to <= from || to > info.Frames() {
		return fmt.Errorf("%w: [%d, %d) of %d frames", ErrRange, from, to, info.Frames())
	}
	if to-from > int64(MaxRenderSeconds*info.SampleRate) {
		return ErrRenderTooLong
	}
	args := append([]string{"-n", "10", "ffmpeg"}, RenderArgs(wavPath, info, saveChannels, from, to)...)
	cmd := exec.CommandContext(ctx, "nice", args...)
	cmd.Stdout = w
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
