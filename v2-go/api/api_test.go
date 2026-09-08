package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	a := New(&config.Config{OutputDir: dir}, nil, nil)
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
	r, _ := newTestAPI(t)
	for _, bad := range []string{"..%2Fescape.wav", "sub%2Fjam.wav", "jam_a.txt"} {
		w := patch(t, r, bad, `{"starred":true}`)
		if w.Code != http.StatusBadRequest {
			t.Errorf("file=%q: status = %d, want 400", bad, w.Code)
		}
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
