package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/gabeduke/audio-dashcam/v2-go/audio"
	"github.com/gabeduke/audio-dashcam/v2-go/config"
	"github.com/gorilla/mux"
)

// newTestAPI builds an API over a temp takes directory. Capture and Saver are
// nil because the metadata handler never touches them; a test that needed
// audio would have to run on hardware.
func newTestAPI(t *testing.T) (*mux.Router, string) {
	t.Helper()
	dir := t.TempDir()
	a := New(&config.Config{OutputDir: dir}, nil, nil, nil)
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
	a := New(&config.Config{OutputDir: t.TempDir()}, nil, nil, e)
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
	if got := body["edge_seconds"].(float64); got != 1 {
		t.Errorf("edge_seconds = %v, want 1", got)
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
	a := New(&config.Config{OutputDir: t.TempDir()}, nil, nil, nil)
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
