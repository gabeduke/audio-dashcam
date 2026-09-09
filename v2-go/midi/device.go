package midi

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// DefaultCardsPath and DefaultSndDir are the real ALSA locations. They are
// parameters rather than constants everywhere below so the tests can run
// against a fixture directory, on a Mac, with no hardware.
const (
	DefaultCardsPath = "/proc/asound/cards"
	DefaultSndDir    = "/dev/snd"
)

// ErrNoDevice reports that no card matched, or that the matching card exposes
// no rawmidi node. This is a normal state, not a failure: the interface is
// frequently unplugged, and a Mac has no /proc/asound at all. Callers must not
// treat it as an error worth shouting about.
var ErrNoDevice = errors.New("no matching MIDI device")

// cardHeader matches the first line of a card entry in /proc/asound/cards.
// The real line is `<space>2 [Sidekick       ]: USB-Audio - EP-136 K.O.
// Sidekick` -- the card number is space-padded, which is why the pattern
// leads with \s* rather than anchoring on the digit.
//
// Continuation lines are indented and carry the long name, which is where the
// full manufacturer and model text lives.
var cardHeader = regexp.MustCompile(`^\s*(\d+)\s+\[`)

// Find returns the path of the rawmidi character device belonging to the first
// card whose /proc/asound/cards entry contains match, case-insensitively.
//
// The whole entry is searched -- both lines -- rather than the bracketed id
// alone. ALSA truncates that id to 15 characters and sanitises it, so the
// EP-136 appears there as "Sidekick" with no model number in it; the string
// DEVICE_MATCH is set to only appears in the short and long names.
func Find(cardsPath, sndDir, match string) (string, error) {
	b, err := os.ReadFile(cardsPath)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNoDevice, err)
	}

	needle := strings.ToLower(match)
	card := -1
	var entry strings.Builder
	found := -1

	flush := func() {
		if card >= 0 && found < 0 && strings.Contains(strings.ToLower(entry.String()), needle) {
			found = card
		}
	}

	for _, line := range strings.Split(string(b), "\n") {
		if m := cardHeader.FindStringSubmatch(line); m != nil {
			flush()
			card, _ = strconv.Atoi(m[1])
			entry.Reset()
		}
		entry.WriteString(line)
		entry.WriteString("\n")
	}
	flush()

	if found < 0 {
		return "", fmt.Errorf("%w: no card matching %q", ErrNoDevice, match)
	}
	return rawMIDINode(sndDir, found)
}

// rawMIDINode picks the lowest-numbered rawmidi sub-device on a card. The
// EP-136 is hw:2,0,0, so D0 is what this resolves to in practice; taking the
// lowest rather than hardcoding D0 costs nothing and survives a device that
// numbers differently.
func rawMIDINode(sndDir string, card int) (string, error) {
	// The trailing D in the pattern is what stops card 1 matching card 12:
	// "midiC1D*" cannot match "midiC12D0".
	glob := filepath.Join(sndDir, fmt.Sprintf("midiC%dD*", card))
	nodes, err := filepath.Glob(glob)
	if err != nil || len(nodes) == 0 {
		return "", fmt.Errorf("%w: card %d exposes no rawmidi node", ErrNoDevice, card)
	}
	sort.Strings(nodes)
	return nodes[0], nil
}
