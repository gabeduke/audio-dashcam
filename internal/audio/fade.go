package audio

// FadeFrames is the declick fade length: 3ms at the given rate. 144 frames
// at 48kHz. Long enough to remove the click a hard cut makes, short enough
// that it is inaudible as a fade.
func FadeFrames(sampleRate int) int64 { return int64(sampleRate) * 3 / 1000 }

// applyFades scales a block of interleaved samples in place with a linear
// fade-in over the region's first `fade` frames and a fade-out over its last
// `fade` frames. `first` is the block's first frame relative to the region
// start, `total` the region length, so blocks can be faded independently as
// they stream past. Frame i is scaled by i/fade on the way in and by
// (total-i)/fade on the way out; frame 0 is silent, the last frame is one
// step above silence. Integer math on int64 keeps it exact.
func applyFades(block []int32, first, total int64, channels int, fade int64) {
	if fade <= 0 || channels <= 0 {
		return
	}
	n := int64(len(block) / channels)
	for i := int64(0); i < n; i++ {
		pos := first + i
		var num, den int64 = 1, 1
		if pos < fade {
			num, den = pos, fade
		} else if rem := total - pos; rem <= fade {
			num, den = rem, fade
		} else {
			continue
		}
		for c := 0; c < channels; c++ {
			k := int(i)*channels + c
			block[k] = int32(int64(block[k]) * num / den)
		}
	}
}
