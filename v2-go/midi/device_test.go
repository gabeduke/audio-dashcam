package midi

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// realCards is the verbatim `cat /proc/asound/cards` from the Pi on
// 2026-09-09 with the EP-136 connected.
//
// The bracketed id is "EP136" -- no hyphen. DEVICE_MATCH is "EP-136", which
// appears only in the short name and the long name, so a discovery that
// matched the bracketed id would find nothing. That is not a hypothetical:
// this is the real device this project is built around, and this is its real
// entry. An earlier version of this fixture guessed "Sidekick" here; the
// hardware turned out to differ in the details and agree on the principle.
const realCards = ` 0 [vc4hdmi0       ]: vc4-hdmi - vc4-hdmi-0
                      vc4-hdmi-0
 1 [vc4hdmi1       ]: vc4-hdmi - vc4-hdmi-1
                      vc4-hdmi-1
 2 [EP136          ]: USB-Audio - EP-136
                      teenage engineering EP-136 at usb-xhci-hcd.0-1, high speed
`

const cardsNoEP = ` 0 [vc4hdmi0       ]: vc4-hdmi - vc4-hdmi-0
                      vc4-hdmi-0
 1 [vc4hdmi1       ]: vc4-hdmi - vc4-hdmi-1
                      vc4-hdmi-1
`

// fixture writes a cards file and a snd directory containing the named nodes.
func fixture(t *testing.T, cards string, nodes ...string) (cardsPath, sndDir string) {
	t.Helper()
	root := t.TempDir()
	cardsPath = filepath.Join(root, "cards")
	if err := os.WriteFile(cardsPath, []byte(cards), 0o644); err != nil {
		t.Fatal(err)
	}
	sndDir = filepath.Join(root, "snd")
	if err := os.MkdirAll(sndDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		if err := os.WriteFile(filepath.Join(sndDir, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return cardsPath, sndDir
}

// The bracketed id reads "Sidekick" and contains no "EP-136" at all, so
// matching only the id would miss the device this project is built around.
func TestFindMatchesTheLongNameNotTheBracketedID(t *testing.T) {
	cards, snd := fixture(t, realCards, "midiC2D0", "controlC2")

	got, err := Find(cards, snd, "EP-136")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	want := filepath.Join(snd, "midiC2D0")
	if got != want {
		t.Errorf("Find = %q, want %q", got, want)
	}
}

func TestFindMatchesTheBracketedIDToo(t *testing.T) {
	cards, snd := fixture(t, realCards, "midiC2D0")

	got, err := Find(cards, snd, "EP136")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got != filepath.Join(snd, "midiC2D0") {
		t.Errorf("Find = %q, want the card 2 node", got)
	}
}

func TestFindIsCaseInsensitive(t *testing.T) {
	cards, snd := fixture(t, realCards, "midiC2D0")

	if _, err := Find(cards, snd, "ep-136"); err != nil {
		t.Errorf("Find with lowercase match: %v", err)
	}
}

// The EP is frequently unplugged. That is a normal state, and it must be
// distinguishable from a real failure so the reader can stay quiet about it.
func TestFindReportsNoDeviceWhenTheCardIsAbsent(t *testing.T) {
	cards, snd := fixture(t, cardsNoEP)

	_, err := Find(cards, snd, "EP-136")
	if !errors.Is(err, ErrNoDevice) {
		t.Errorf("err = %v, want ErrNoDevice", err)
	}
}

// A card can exist and expose no MIDI at all -- an HDMI output, for one.
func TestFindReportsNoDeviceWhenTheCardHasNoMIDINode(t *testing.T) {
	cards, snd := fixture(t, realCards, "controlC2", "pcmC2D0c")

	_, err := Find(cards, snd, "EP-136")
	if !errors.Is(err, ErrNoDevice) {
		t.Errorf("err = %v, want ErrNoDevice", err)
	}
}

// The EP is hw:2,0,0, so D0 is the right sub-device. Take the lowest rather
// than hardcoding it, so a device that numbers differently still works.
func TestFindTakesTheLowestSubDevice(t *testing.T) {
	cards, snd := fixture(t, realCards, "midiC2D1", "midiC2D0", "midiC2D2")

	got, err := Find(cards, snd, "EP-136")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got != filepath.Join(snd, "midiC2D0") {
		t.Errorf("Find = %q, want midiC2D0", got)
	}
}

// Another card's node must never be mistaken for this one's. midiC1D0 is a
// prefix collision hazard for a naive match on "midiC1".
func TestFindDoesNotPickAnotherCardsNode(t *testing.T) {
	cards, snd := fixture(t, realCards, "midiC1D0", "midiC12D0")

	_, err := Find(cards, snd, "EP-136")
	if !errors.Is(err, ErrNoDevice) {
		t.Errorf("err = %v, want ErrNoDevice (card 2 has no node)", err)
	}
}

// On a Mac there is no /proc/asound at all. That is the same normal state.
func TestFindReportsNoDeviceWhenThereIsNoALSA(t *testing.T) {
	_, err := Find("/nonexistent/proc/asound/cards", "/nonexistent/dev/snd", "EP-136")
	if !errors.Is(err, ErrNoDevice) {
		t.Errorf("err = %v, want ErrNoDevice", err)
	}
}
