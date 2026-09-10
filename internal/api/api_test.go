package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gabeduke/hindsight/internal/audio"
	"github.com/gabeduke/hindsight/internal/config"
	"github.com/gorilla/mux"
)

// newTestAPI builds an API over a temp takes directory. Capture, Saver and the
// MIDI source are nil because the metadata handler never touches them; a test
// that needed audio would have to run on hardware.
func newTestAPI(t *testing.T) (*mux.Router, string) {
	t.Helper()
	dir := t.TempDir()
	a := New(&config.Config{OutputDir: dir}, nil, nil, nil, nil)
	r := mux.NewRouter()
	a.SetupRoutes(r)
	return r, dir
}

func writeTake(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("not a real wav"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func patch(t *testing.T, r *mux.Router, file, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/api/take?file="+file, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// newFlagAPI builds an API over a real Capture, so the flag endpoints have a
// ring to work with. The Source is nil and nothing is started; tests write into
// the ring directly.
func newFlagAPI(t *testing.T) (*mux.Router, *audio.Capture, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{
		OutputDir:    dir,
		Channels:     2,
		SampleRate:   48000,
		RingSeconds:  10,
		SaveChannels: []int{0, 1},
	}
	cap := audio.NewCapture(cfg, nil)
	a := New(cfg, cap, nil, cap.Envelope(), nil)
	r := mux.NewRouter()
	a.SetupRoutes(r)
	return r, cap, dir
}

// writeRealTake writes a genuine WAV, unlike writeTake, so cue points can be
// read back off it.
func writeRealTake(t *testing.T, dir, name string, frames int) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if _, err := audio.WriteWAV(p, make([]int32, frames*2), 2, []int{0, 1}, 48000); err != nil {
		t.Fatal(err)
	}
	return p
}

func do(t *testing.T, r *mux.Router, method, url string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(method, url, nil))
	return w
}

func TestPatchTakeSetsLabelAndStar(t *testing.T) {
	r, dir := newTestAPI(t)
	wav := writeTake(t, dir, "jam_a.wav")

	w := patch(t, r, "jam_a.wav", `{"label":"the good one","starred":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}

	m := audio.ReadMeta(wav)
	if m.Label != "the good one" || !m.Starred {
		t.Errorf("sidecar = %+v, want label set and starred", m)
	}
}

func TestPatchTakeMergesRatherThanReplaces(t *testing.T) {
	r, dir := newTestAPI(t)
	wav := writeTake(t, dir, "jam_a.wav")

	if w := patch(t, r, "jam_a.wav", `{"label":"keep me"}`); w.Code != http.StatusOK {
		t.Fatalf("first patch: %d", w.Code)
	}
	// Starring must not clear the label.
	if w := patch(t, r, "jam_a.wav", `{"starred":true}`); w.Code != http.StatusOK {
		t.Fatalf("second patch: %d", w.Code)
	}

	m := audio.ReadMeta(wav)
	if m.Label != "keep me" {
		t.Errorf("Label = %q, want it preserved across a starred-only patch", m.Label)
	}
	if !m.Starred {
		t.Error("Starred = false, want true")
	}
}

func TestPatchTakeExplicitNullClearsTrim(t *testing.T) {
	r, dir := newTestAPI(t)
	wav := writeTake(t, dir, "jam_a.wav")

	if w := patch(t, r, "jam_a.wav", `{"trim":{"start_frame":10,"end_frame":20}}`); w.Code != http.StatusOK {
		t.Fatalf("set trim: %d (%s)", w.Code, w.Body.String())
	}
	if m := audio.ReadMeta(wav); m.Trim == nil {
		t.Fatal("trim was not set")
	}

	if w := patch(t, r, "jam_a.wav", `{"trim":null}`); w.Code != http.StatusOK {
		t.Fatalf("clear trim: %d", w.Code)
	}
	if m := audio.ReadMeta(wav); m.Trim != nil {
		t.Errorf("Trim = %+v, want nil after an explicit null", m.Trim)
	}
}

func TestPatchTakeOmittedTrimIsLeftAlone(t *testing.T) {
	r, dir := newTestAPI(t)
	wav := writeTake(t, dir, "jam_a.wav")

	if w := patch(t, r, "jam_a.wav", `{"trim":{"start_frame":10,"end_frame":20}}`); w.Code != http.StatusOK {
		t.Fatalf("set trim: %d", w.Code)
	}
	if w := patch(t, r, "jam_a.wav", `{"starred":true}`); w.Code != http.StatusOK {
		t.Fatalf("star: %d", w.Code)
	}

	m := audio.ReadMeta(wav)
	if m.Trim == nil || m.Trim.StartFrame != 10 || m.Trim.EndFrame != 20 {
		t.Errorf("Trim = %+v, want it untouched when the field is absent", m.Trim)
	}
}

func TestPatchTakeRejectsBackwardsTrim(t *testing.T) {
	r, dir := newTestAPI(t)
	writeTake(t, dir, "jam_a.wav")

	w := patch(t, r, "jam_a.wav", `{"trim":{"start_frame":20,"end_frame":20}}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a zero-length trim", w.Code)
	}
}

func TestPatchTakeUnknownFileIs404(t *testing.T) {
	r, _ := newTestAPI(t)
	w := patch(t, r, "jam_missing.wav", `{"starred":true}`)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestPatchTakeRejectsTraversal(t *testing.T) {
	r, dir := newTestAPI(t)

	// Plant a real take one directory above OutputDir so a guard regression
	// that let ".." through would have a live target to write a sidecar into,
	// rather than the test passing by luck of there being nothing to escape to.
	parent := filepath.Dir(dir)
	escape := filepath.Join(parent, "escape.wav")
	if err := os.WriteFile(escape, []byte("not a real wav"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(escape) })

	for _, bad := range []string{"..%2Fescape.wav", "sub%2Fjam.wav", "jam_a.txt"} {
		w := patch(t, r, bad, `{"starred":true}`)
		if w.Code != http.StatusBadRequest {
			t.Errorf("file=%q: status = %d, want 400", bad, w.Code)
		}
	}

	if _, err := os.Stat(filepath.Join(parent, "escape.meta.json")); err == nil {
		t.Error("a sidecar was written outside OutputDir: traversal guard was bypassed")
	}
}

func TestPatchTakeTrimsAndCapsLabel(t *testing.T) {
	r, dir := newTestAPI(t)
	wav := writeTake(t, dir, "jam_a.wav")

	long := strings.Repeat("x", 500)
	if w := patch(t, r, "jam_a.wav", `{"label":"  spaced  "}`); w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if m := audio.ReadMeta(wav); m.Label != "spaced" {
		t.Errorf("Label = %q, want surrounding whitespace trimmed", m.Label)
	}

	body, _ := json.Marshal(map[string]string{"label": long})
	if w := patch(t, r, "jam_a.wav", string(body)); w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if m := audio.ReadMeta(wav); len(m.Label) != maxLabelLen {
		t.Errorf("len(Label) = %d, want it capped at %d", len(m.Label), maxLabelLen)
	}
}

func TestPatchTakeCapsMultiByteLabelByRune(t *testing.T) {
	r, dir := newTestAPI(t)
	wav := writeTake(t, dir, "jam_a.wav")

	// 60 ASCII runes (60 bytes) followed by 100 three-byte runes (300 bytes):
	// a byte-based cut at maxLabelLen (120 bytes) never even reaches the 61st
	// rune, landing only 20 "あ" runes in (60+20*3=120 bytes exactly) for 80
	// runes total. A correct rune-based cut keeps the first 120 runes
	// (60 ASCII + 60 "あ"). Deliberately not the report's single-rune
	// reproducer: that one's stray byte gets replaced with exactly one U+FFFD
	// by json.Marshal, which by coincidence still counts to maxLabelLen runes
	// even under the old buggy code, so it wouldn't actually catch a
	// regression back to byte slicing.
	label := strings.Repeat("x", 60) + strings.Repeat("あ", 100)
	body, _ := json.Marshal(map[string]string{"label": label})
	if w := patch(t, r, "jam_a.wav", string(body)); w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	m := audio.ReadMeta(wav)
	if !utf8.ValidString(m.Label) {
		t.Fatalf("Label is not valid UTF-8: %q", m.Label)
	}
	if n := utf8.RuneCountInString(m.Label); n != maxLabelLen {
		t.Errorf("rune count = %d, want %d", n, maxLabelLen)
	}
	want := strings.Repeat("x", 60) + strings.Repeat("あ", 60)
	if m.Label != want {
		t.Errorf("Label = %q, want %q", m.Label, want)
	}
}

func TestPatchTakeNewerSidecarIs409(t *testing.T) {
	r, dir := newTestAPI(t)
	wav := writeTake(t, dir, "jam_a.wav")

	// Written by a hypothetical future build that knows fields this one
	// doesn't; the sidecar naming convention (jam_x.meta.json) is fixed, so
	// this constructs the same path audio.metaPath would without importing it.
	sidecar := strings.TrimSuffix(wav, ".wav") + ".meta.json"
	if err := os.WriteFile(sidecar, []byte(`{"version":99,"label":"future"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	w := patch(t, r, "jam_a.wav", `{"starred":true}`)
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 for a sidecar from a newer version (body %s)", w.Code, w.Body.String())
	}
}

// newEnvelopeAPI builds an API over a real envelope holding `bins` loud bins.
// Capture and Saver stay nil: the envelope handler never touches them.
func newEnvelopeAPI(t *testing.T, capBins, bins int) *mux.Router {
	t.Helper()
	e := audio.NewEnvelope(capBins, []int{0}, 10)
	for i := 0; i < bins; i++ {
		e.PushBin(audio.Bin{Min: []float32{-0.5}, Max: []float32{0.5}, RMS: []float32{0}})
	}
	a := New(&config.Config{OutputDir: t.TempDir()}, nil, nil, e, nil)
	r := mux.NewRouter()
	a.SetupRoutes(r)
	return r
}

func getEnvelope(t *testing.T, r *mux.Router, query string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/envelope"+query, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad JSON: %v (%s)", err, w.Body.String())
	}
	return body
}

func TestEnvelopeReportsRingAndBuffered(t *testing.T) {
	r := newEnvelopeAPI(t, 90000, 3000) // 900s ring, 30s written
	body := getEnvelope(t, r, "?buckets=64")

	if got := body["ring_seconds"].(float64); got != 900 {
		t.Errorf("ring_seconds = %v, want 900", got)
	}
	if got := body["buffered_seconds"].(float64); got < 29.9 || got > 30.1 {
		t.Errorf("buffered_seconds = %v, want ~30", got)
	}
	// Assert against the constant, not a literal: this test is here to prove the
	// response carries the edge age the server bucketed on, so that the client
	// can re-derive the same axis. The value itself is a tuning decision and
	// moves — pinning it here would make tuning look like a regression.
	if got := body["edge_seconds"].(float64); got != audio.EdgeSeconds {
		t.Errorf("edge_seconds = %v, want %v (audio.EdgeSeconds)", got, audio.EdgeSeconds)
	}
}

func TestEnvelopeReportsTheEffectiveEdgeOnAShortRing(t *testing.T) {
	// A 1.0s ring sits exactly on the t == EdgeSeconds boundary where Buckets
	// falls back to RingSeconds()/2. The response must report that same 0.5,
	// not the bare EdgeSeconds constant, or the client places its markers on
	// an axis the server did not actually bucket against.
	r := newEnvelopeAPI(t, 100, 100) // 100 bins x 10ms = 1.0s ring, fully loud
	body := getEnvelope(t, r, "?buckets=8")

	if got := body["edge_seconds"].(float64); got < 0.499 || got > 0.501 {
		t.Errorf("edge_seconds = %v, want 0.5 (RingSeconds()/2 on a 1.0s ring)", got)
	}
}

func TestEnvelopeBucketsAreBase64OfTheRequestedLength(t *testing.T) {
	r := newEnvelopeAPI(t, 90000, 90000)
	body := getEnvelope(t, r, "?buckets=64")

	raw, err := base64.StdEncoding.DecodeString(body["buckets"].(string))
	if err != nil {
		t.Fatalf("buckets is not base64: %v", err)
	}
	if len(raw) != 64 {
		t.Fatalf("got %d buckets, want 64", len(raw))
	}
	if raw[len(raw)-1] == 0 {
		t.Fatal("newest bucket is silent, but the whole ring is loud")
	}
}

func TestEnvelopeSignalSecondsIsParallelToSpans(t *testing.T) {
	r := newEnvelopeAPI(t, 90000, 3000) // 30s of loud audio
	body := getEnvelope(t, r, "?buckets=16&spans=10,30")

	sig := body["signal_seconds"].([]any)
	if len(sig) != 2 {
		t.Fatalf("got %d signal_seconds for 2 spans", len(sig))
	}
	if v := sig[0].(float64); v < 9.9 || v > 10.1 {
		t.Errorf("10s span reports %v, want ~10", v)
	}
	if v := sig[1].(float64); v < 29.9 || v > 30.1 {
		t.Errorf("30s span reports %v, want ~30", v)
	}
}

func TestEnvelopeSpanZeroMeansTheWholeRing(t *testing.T) {
	// The UI's Full button sends 0, matching /api/trigger.
	r := newEnvelopeAPI(t, 1000, 1000) // 10s ring, fully loud
	body := getEnvelope(t, r, "?buckets=8&spans=0")

	sig := body["signal_seconds"].([]any)
	if v := sig[0].(float64); v < 9.9 || v > 10.1 {
		t.Fatalf("span 0 reports %v, want the whole 10s ring", v)
	}
}

func TestEnvelopeClampsBucketCount(t *testing.T) {
	r := newEnvelopeAPI(t, 90000, 90000)

	body := getEnvelope(t, r, "?buckets=99999")
	raw, _ := base64.StdEncoding.DecodeString(body["buckets"].(string))
	if len(raw) != 600 {
		t.Errorf("buckets=99999 returned %d, want the 600 cap", len(raw))
	}

	body = getEnvelope(t, r, "?buckets=0")
	raw, _ = base64.StdEncoding.DecodeString(body["buckets"].(string))
	if len(raw) != 1 {
		t.Errorf("buckets=0 returned %d, want 1", len(raw))
	}
}

func TestEnvelopeRejectsNonNumericParams(t *testing.T) {
	r := newEnvelopeAPI(t, 1000, 1000)
	for _, q := range []string{"?buckets=lots", "?spans=30,soon"} {
		req := httptest.NewRequest(http.MethodGet, "/api/envelope"+q, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", q, w.Code)
		}
	}
}

func TestEnvelopeWithoutAnEnvelopeIs503(t *testing.T) {
	a := New(&config.Config{OutputDir: t.TempDir()}, nil, nil, nil, nil)
	r := mux.NewRouter()
	a.SetupRoutes(r)

	req := httptest.NewRequest(http.MethodGet, "/api/envelope", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

func TestTakeRouteRejectsOtherMethods(t *testing.T) {
	r, dir := newTestAPI(t)
	writeTake(t, dir, "jam_a.wav")

	req := httptest.NewRequest(http.MethodPost, "/api/take?file=jam_a.wav", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405 for POST /api/take", w.Code)
	}
}

// fakeMIDI stands in for midi.Reader.
type fakeMIDI struct {
	connected bool
	bpm       float64
	ok        bool
}

func (f *fakeMIDI) Connected() bool { return f.connected }
func (f *fakeMIDI) BPM(start, end time.Time) (float64, bool) {
	return f.bpm, f.ok
}

func newStatusAPI(t *testing.T, m MIDISource) *mux.Router {
	t.Helper()
	a := New(&config.Config{OutputDir: t.TempDir()}, nil, nil, nil, m)
	r := mux.NewRouter()
	// Only the MIDI half of the status response is exercised here; the rest
	// needs a live Capture.
	r.HandleFunc("/api/midi", func(w http.ResponseWriter, req *http.Request) {
		conn, bpm := a.midiState()
		writeJSON(w, http.StatusOK, map[string]any{"midi_connected": conn, "midi_bpm": bpm})
	})
	return r
}

func midiState(t *testing.T, m MIDISource) (bool, *float64) {
	t.Helper()
	r := newStatusAPI(t, m)
	req := httptest.NewRequest(http.MethodGet, "/api/midi", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var got struct {
		Connected bool     `json:"midi_connected"`
		BPM       *float64 `json:"midi_bpm"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, w.Body.String())
	}
	return got.Connected, got.BPM
}

func TestStatusReportsLiveBPM(t *testing.T) {
	conn, bpm := midiState(t, &fakeMIDI{connected: true, bpm: 129.87, ok: true})
	if !conn {
		t.Error("midi_connected = false, want true")
	}
	if bpm == nil || *bpm != 129.87 {
		t.Errorf("midi_bpm = %v, want 129.87", bpm)
	}
}

// Connected but silent is the exact shape of the failure worth catching: the
// EP ships with clock-send off, and a run with it off is indistinguishable
// from firmware that cannot send clock. null, not 0.
func TestStatusReportsConnectedWithNoClockAsNull(t *testing.T) {
	conn, bpm := midiState(t, &fakeMIDI{connected: true, ok: false})
	if !conn {
		t.Error("midi_connected = false, want true")
	}
	if bpm != nil {
		t.Errorf("midi_bpm = %v, want null", *bpm)
	}
}

func TestStatusWithNoMIDISourceIsNotConnected(t *testing.T) {
	conn, bpm := midiState(t, nil)
	if conn {
		t.Error("midi_connected = true with no source, want false")
	}
	if bpm != nil {
		t.Errorf("midi_bpm = %v, want null", *bpm)
	}
}

func TestPatchTakeSetsBPM(t *testing.T) {
	r, dir := newTestAPI(t)
	writeTake(t, dir, "jam_a.wav")

	w := patch(t, r, "jam_a.wav", `{"bpm":129.874}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}

	got := audio.ReadMeta(filepath.Join(dir, "jam_a.wav"))
	if got.BPM == nil || *got.BPM != 129.87 {
		t.Errorf("BPM = %v, want 129.87 (rounded to two decimals)", got.BPM)
	}
}

// An empty submission clears the field rather than storing zero. The UI sends
// null when the input is emptied.
func TestPatchTakeClearsBPMWithNull(t *testing.T) {
	r, dir := newTestAPI(t)
	wav := writeTake(t, dir, "jam_a.wav")
	bpm := 120.0
	if err := audio.WriteMeta(wav, audio.Meta{BPM: &bpm}); err != nil {
		t.Fatal(err)
	}

	w := patch(t, r, "jam_a.wav", `{"bpm":null}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if got := audio.ReadMeta(wav); got.BPM != nil {
		t.Errorf("BPM = %v, want nil", *got.BPM)
	}
}

// The merge must stay a merge: setting a tempo cannot silently drop a label.
func TestPatchTakeBPMDoesNotClearTheLabel(t *testing.T) {
	r, dir := newTestAPI(t)
	wav := writeTake(t, dir, "jam_a.wav")
	if err := audio.WriteMeta(wav, audio.Meta{Label: "keep me", Starred: true}); err != nil {
		t.Fatal(err)
	}

	if w := patch(t, r, "jam_a.wav", `{"bpm":92}`); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	got := audio.ReadMeta(wav)
	if got.Label != "keep me" || !got.Starred {
		t.Errorf("patching bpm lost fields: %+v", got)
	}
}

// And the reverse: renaming must not drop a tempo.
func TestPatchTakeLabelDoesNotClearTheBPM(t *testing.T) {
	r, dir := newTestAPI(t)
	wav := writeTake(t, dir, "jam_a.wav")
	bpm := 92.0
	if err := audio.WriteMeta(wav, audio.Meta{BPM: &bpm}); err != nil {
		t.Fatal(err)
	}

	if w := patch(t, r, "jam_a.wav", `{"label":"renamed"}`); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	got := audio.ReadMeta(wav)
	if got.BPM == nil || *got.BPM != 92 {
		t.Errorf("renaming lost the BPM: %+v", got)
	}
}

// The range is deliberately wider than the EP will ever produce, because the
// whole point of the field is that the owner overrides it.
func TestPatchTakeAcceptsTheRangeBounds(t *testing.T) {
	for _, v := range []string{"20", "400", "20.0", "399.99"} {
		r, dir := newTestAPI(t)
		writeTake(t, dir, "jam_a.wav")
		if w := patch(t, r, "jam_a.wav", `{"bpm":`+v+`}`); w.Code != http.StatusOK {
			t.Errorf("bpm %s: status = %d, want 200 (body %s)", v, w.Code, w.Body.String())
		}
	}
}

func TestPatchTakeRejectsOutOfRangeBPM(t *testing.T) {
	for _, v := range []string{"0", "19.99", "400.01", "1000", "-120"} {
		r, dir := newTestAPI(t)
		wav := writeTake(t, dir, "jam_a.wav")
		w := patch(t, r, "jam_a.wav", `{"bpm":`+v+`}`)
		if w.Code != http.StatusBadRequest {
			t.Errorf("bpm %s: status = %d, want 400", v, w.Code)
		}
		if got := audio.ReadMeta(wav); got.BPM != nil {
			t.Errorf("bpm %s: a rejected value was written anyway (%v)", v, *got.BPM)
		}
	}
}

func TestPatchTakeRejectsNonNumericBPM(t *testing.T) {
	r, dir := newTestAPI(t)
	writeTake(t, dir, "jam_a.wav")

	if w := patch(t, r, "jam_a.wav", `{"bpm":"120"}`); w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// The response is explicit rather than the Meta struct, so a clear-to-empty
// patch reports what it actually did instead of omitting the field.
func TestPatchTakeResponseCarriesTheBPM(t *testing.T) {
	r, dir := newTestAPI(t)
	writeTake(t, dir, "jam_a.wav")

	w := patch(t, r, "jam_a.wav", `{"bpm":92.5}`)
	var got struct {
		BPM *float64 `json:"bpm"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.BPM == nil || *got.BPM != 92.5 {
		t.Errorf("response bpm = %v, want 92.5", got.BPM)
	}
}

func TestPostFlagOnAnEmptyRingIsRejected(t *testing.T) {
	r, _, _ := newFlagAPI(t)
	if w := do(t, r, http.MethodPost, "/api/flag"); w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", w.Code)
	}
}

// capture_healthy false does not mean there is no audio: an interface can be
// powered off with a full buffer still in memory. Nothing is ever started in
// this test, so the capture is as unhealthy as it gets.
func TestPostFlagSucceedsWhileCaptureIsUnhealthy(t *testing.T) {
	r, cap, _ := newFlagAPI(t)
	cap.Ring().WriteFrames(make([]int32, 480*2))

	w := do(t, r, http.MethodPost, "/api/flag")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	var body struct {
		Frame      uint64  `json:"frame"`
		AgeSeconds float64 `json:"age_seconds"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// 479, not 480: TotalFrames() is a count, so the newest existing frame is
	// one less. A mark at 480 would fall outside the half-open window of every
	// take that contains it.
	if body.Frame != 479 {
		t.Errorf("frame = %d, want 479", body.Frame)
	}
	if body.AgeSeconds != 0 {
		t.Errorf("age_seconds = %f, want 0 for a mark at the newest frame", body.AgeSeconds)
	}
}

func TestDeleteFlagRemovesOne(t *testing.T) {
	r, cap, _ := newFlagAPI(t)
	cap.Ring().WriteFrames(make([]int32, 480*2))
	cap.Flags().Mark(479)

	if w := do(t, r, http.MethodDelete, "/api/flag?frame=479"); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if cap.Flags().Len() != 0 {
		t.Errorf("Len = %d, want 0", cap.Flags().Len())
	}
}

func TestDeleteUnknownFlagIs404(t *testing.T) {
	r, _, _ := newFlagAPI(t)
	if w := do(t, r, http.MethodDelete, "/api/flag?frame=7"); w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestDeleteFlagAllClearsThem(t *testing.T) {
	r, cap, _ := newFlagAPI(t)
	cap.Flags().Mark(1)
	cap.Flags().Mark(2)

	if w := do(t, r, http.MethodDelete, "/api/flag?all=1"); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if cap.Flags().Len() != 0 {
		t.Errorf("Len = %d, want 0", cap.Flags().Len())
	}
}

func TestEnvelopeCarriesFlagAges(t *testing.T) {
	r, cap, _ := newFlagAPI(t)
	cap.Ring().WriteFrames(make([]int32, 2*48000*2)) // 2s of audio
	cap.Flags().Mark(48000)                          // 1s in, so 1s old

	body := getEnvelope(t, r, "?buckets=16")
	flags, ok := body["flags"].([]any)
	if !ok || len(flags) != 1 {
		t.Fatalf("flags = %v, want one age", body["flags"])
	}
	age, _ := flags[0].(float64)
	if age < 0.9 || age > 1.1 {
		t.Errorf("age = %f, want about 1.0", age)
	}
}

// The existing envelope harness passes a nil Capture. Flags must not break it.
func TestEnvelopeWithNoCaptureStillServesAnEmptyFlagArray(t *testing.T) {
	r := newEnvelopeAPI(t, 90000, 3000)
	body := getEnvelope(t, r, "?buckets=8")
	flags, ok := body["flags"].([]any)
	if !ok {
		t.Fatalf("flags = %v, want an array", body["flags"])
	}
	if len(flags) != 0 {
		t.Errorf("flags = %v, want empty", flags)
	}
}

func TestPatchTakeSetsFlagsAndCuePoints(t *testing.T) {
	r, dir := newTestAPI(t)
	writeRealTake(t, dir, "jam_flags.wav", 1000)

	w := patch(t, r, "jam_flags.wav", `{"flags":[{"frame":900},{"frame":100}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
	}

	wav := filepath.Join(dir, "jam_flags.wav")
	m := audio.ReadMeta(wav)
	if len(m.Flags) != 2 || m.Flags[0].Frame != 100 || m.Flags[1].Frame != 900 {
		t.Errorf("flags = %+v, want sorted 100 then 900", m.Flags)
	}
	cues, err := audio.ReadCues(wav)
	if err != nil {
		t.Fatalf("ReadCues: %v", err)
	}
	if len(cues) != 2 || cues[0] != 100 || cues[1] != 900 {
		t.Errorf("cues = %v, want [100 900]", cues)
	}
}

func TestPatchTakeClearsFlagsWithNull(t *testing.T) {
	r, dir := newTestAPI(t)
	writeRealTake(t, dir, "jam_flags.wav", 1000)
	patch(t, r, "jam_flags.wav", `{"flags":[{"frame":10}]}`)

	if w := patch(t, r, "jam_flags.wav", `{"flags":null}`); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if m := audio.ReadMeta(filepath.Join(dir, "jam_flags.wav")); m.Flags != nil {
		t.Errorf("flags = %+v, want nil", m.Flags)
	}
}

func TestPatchTakeOmittedFlagsAreLeftAlone(t *testing.T) {
	r, dir := newTestAPI(t)
	writeRealTake(t, dir, "jam_flags.wav", 1000)
	patch(t, r, "jam_flags.wav", `{"flags":[{"frame":10}]}`)

	patch(t, r, "jam_flags.wav", `{"label":"renamed"}`)

	m := audio.ReadMeta(filepath.Join(dir, "jam_flags.wav"))
	if len(m.Flags) != 1 || m.Flags[0].Frame != 10 {
		t.Errorf("flags = %+v, want the existing flag untouched", m.Flags)
	}
}

// A cue write on a file that is not a WAV must not lose the sidecar edit.
func TestPatchTakeKeepsFlagsWhenTheCueWriteFails(t *testing.T) {
	r, dir := newTestAPI(t)
	writeTake(t, dir, "jam_fake.wav") // deliberately not a real WAV

	if w := patch(t, r, "jam_fake.wav", `{"flags":[{"frame":5}]}`); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	m := audio.ReadMeta(filepath.Join(dir, "jam_fake.wav"))
	if len(m.Flags) != 1 {
		t.Errorf("flags = %+v, want the flag kept despite the cue failure", m.Flags)
	}
}

func TestPatchTakeRejectsTooManyFlags(t *testing.T) {
	r, dir := newTestAPI(t)
	writeRealTake(t, dir, "jam_flags.wav", 100000)

	var sb strings.Builder
	sb.WriteString(`{"flags":[`)
	for i := 0; i <= 512; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"frame":%d}`, i)
	}
	sb.WriteString(`]}`)

	if w := patch(t, r, "jam_flags.wav", sb.String()); w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestPatchTakeRejectsANegativeFlagFrame(t *testing.T) {
	r, dir := newTestAPI(t)
	writeRealTake(t, dir, "jam_flags.wav", 1000)
	if w := patch(t, r, "jam_flags.wav", `{"flags":[{"frame":-1}]}`); w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// Task 5's reviewer traced the worst case of PATCH having no mutex around
// WriteCues as lost cues with intact audio, never a corrupt file, because
// riffExtent clamps end to the real file size. This does not assert which
// flags win -- that's genuinely unspecified under a race -- only that the
// take stays a valid WAV with a readable cue chunk no matter which write
// physically lands last.
func TestConcurrentPatchesLeaveTheTakeParseable(t *testing.T) {
	r, dir := newTestAPI(t)
	wav := writeRealTake(t, dir, "jam_concurrent.wav", 100000)

	const n = 8
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := fmt.Sprintf(`{"flags":[{"frame":%d},{"frame":%d}]}`, i, i+50000)
			patch(t, r, "jam_concurrent.wav", body)
		}(i)
	}
	wg.Wait()

	if _, err := audio.ReadWAVInfo(wav); err != nil {
		t.Fatalf("ReadWAVInfo after concurrent PATCHes: %v", err)
	}
	if _, err := audio.ReadCues(wav); err != nil {
		t.Fatalf("ReadCues after concurrent PATCHes: %v", err)
	}
}
