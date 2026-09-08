// Package api exposes the dashcam over HTTP.
package api

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gabeduke/audio-dashcam/v2-go/audio"
	"github.com/gabeduke/audio-dashcam/v2-go/config"
	"github.com/gorilla/mux"
)

type API struct {
	cfg   *config.Config
	cap   *audio.Capture
	saver *audio.Saver
}

func New(cfg *config.Config, cap *audio.Capture, saver *audio.Saver) *API {
	return &API{cfg: cfg, cap: cap, saver: saver}
}

func (a *API) SetupRoutes(r *mux.Router) {
	r.HandleFunc("/api/status", a.handleStatus).Methods(http.MethodGet, http.MethodHead)
	r.HandleFunc("/api/jams", a.handleJams).Methods(http.MethodGet, http.MethodHead)
	r.HandleFunc("/api/trigger", a.handleTrigger).Methods(http.MethodPost)
	r.HandleFunc("/api/delete", a.handleDelete).Methods(http.MethodDelete)
	r.HandleFunc("/api/take", a.handleTakePatch).Methods(http.MethodPatch)
	r.HandleFunc("/api/download", a.handleDownload).Methods(http.MethodGet, http.MethodHead)
	r.HandleFunc("/api/peaks", a.handlePeaks).Methods(http.MethodGet, http.MethodHead)
	r.HandleFunc("/api/live", a.handleLive).Methods(http.MethodGet)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

type statusResponse struct {
	IsRecording     bool      `json:"is_recording"`
	CaptureHealthy  bool      `json:"capture_healthy"`
	LastError       string    `json:"last_error"`
	Device          string    `json:"device"`
	XRuns           uint64    `json:"xruns"`
	RingSeconds     int       `json:"ring_seconds"`
	BufferedSeconds float64   `json:"buffered_seconds"`
	SampleRate      int       `json:"sample_rate"`
	Channels        int       `json:"channels"`
	SaveChannels    []int     `json:"save_channels"`
	ChannelRMS      []float32 `json:"channel_rms"`
	FloorDB         float64   `json:"floor_db"`
	LastSaved       string    `json:"last_saved"`
	Saving          bool      `json:"saving"`
	DiskFreeGB      float64   `json:"disk_free_gb"`
	DiskPercent     float64   `json:"disk_percent"`
	MinFreeGB       float64   `json:"min_free_gb"`
}

func (a *API) handleStatus(w http.ResponseWriter, r *http.Request) {
	free, pct := a.saver.FreeGB()

	// Report channels 1-indexed, matching the hardware labelling and the env var.
	sc := make([]int, len(a.cfg.SaveChannels))
	for i, c := range a.cfg.SaveChannels {
		sc[i] = c + 1
	}

	writeJSON(w, http.StatusOK, statusResponse{
		IsRecording:     a.cap.Healthy(),
		CaptureHealthy:  a.cap.Healthy(),
		LastError:       a.cap.LastError(),
		Device:          a.cap.DeviceName(),
		XRuns:           a.cap.XRuns(),
		RingSeconds:     a.cfg.RingSeconds,
		BufferedSeconds: a.cap.BufferedSeconds(),
		SampleRate:      a.cfg.SampleRate,
		Channels:        a.cfg.Channels,
		SaveChannels:    sc,
		ChannelRMS:      a.cap.Levels().Snapshot(),
		FloorDB:         audio.FloorDB,
		LastSaved:       a.saver.LastSaved(),
		Saving:          a.saver.Saving(),
		DiskFreeGB:      free,
		DiskPercent:     pct,
		MinFreeGB:       a.cfg.MinFreeGB,
	})
}

func (a *API) handleJams(w http.ResponseWriter, r *http.Request) {
	takes, err := audio.ListTakes(a.cfg.OutputDir)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if takes == nil {
		takes = []audio.Take{}
	}

	body, err := json.Marshal(takes)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// An ETag lets the client skip re-rendering the list entirely when nothing
	// changed, which is what keeps a playing preview from being disturbed.
	sum := sha1.Sum(body)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}

func (a *API) handleTrigger(w http.ResponseWriter, r *http.Request) {
	seconds := 0.0 // 0 = whole ring
	if v := r.URL.Query().Get("seconds"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f < 0 {
			writeErr(w, http.StatusBadRequest, "seconds must be a non-negative number")
			return
		}
		seconds = f
	}

	name, err := a.saver.Save(seconds)
	switch {
	case errors.Is(err, audio.ErrLowDisk):
		writeErr(w, http.StatusInsufficientStorage, err.Error())
		return
	case errors.Is(err, audio.ErrNoAudio):
		writeErr(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "saved",
		"name":     name,
		"seconds":  seconds,
		"buffered": a.cap.BufferedSeconds(),
	})
}

func (a *API) handleDelete(w http.ResponseWriter, r *http.Request) {
	name, err := a.safeTakeName(r.URL.Query().Get("file"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	audio.RemoveTake(a.cfg.OutputDir, name)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "name": name})
}

func (a *API) handleDownload(w http.ResponseWriter, r *http.Request) {
	path, name, err := a.safeMediaPath(r.URL.Query().Get("file"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := os.Stat(path); err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	// Inline by default so <audio> can stream it; attachment only on request.
	if r.URL.Query().Get("dl") != "" {
		w.Header().Set("Content-Disposition", "attachment; filename=\""+name+"\"")
	}
	http.ServeFile(w, r, path)
}

func (a *API) handlePeaks(w http.ResponseWriter, r *http.Request) {
	name, err := a.safeTakeName(r.URL.Query().Get("file"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	path := filepath.Join(a.cfg.OutputDir, strings.TrimSuffix(name, ".wav")+".peaks.json")
	f, err := os.Open(path)
	if err != nil {
		writeErr(w, http.StatusNotFound, "peaks not generated")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, "peaks.json", statModTime(f), f)
}

// safeTakeName validates a .wav take name and rejects anything with a path in it.
func (a *API) safeTakeName(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("file is required")
	}
	name := filepath.Base(raw)
	if name != raw || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("invalid file name")
	}
	if filepath.Ext(name) != ".wav" {
		return "", fmt.Errorf("only .wav takes are addressable")
	}
	return name, nil
}

// safeMediaPath allows the take itself or its generated sidecars.
func (a *API) safeMediaPath(raw string) (string, string, error) {
	if raw == "" {
		return "", "", fmt.Errorf("file is required")
	}
	name := filepath.Base(raw)
	if name != raw || strings.ContainsAny(name, `/\`) {
		return "", "", fmt.Errorf("invalid file name")
	}
	switch filepath.Ext(name) {
	case ".wav", ".mp3":
	default:
		return "", "", fmt.Errorf("unsupported file type")
	}
	return filepath.Join(a.cfg.OutputDir, name), name, nil
}

func statModTime(f *os.File) (t time.Time) {
	if fi, err := f.Stat(); err == nil {
		return fi.ModTime()
	}
	return
}

// maxLabelLen caps a user-supplied take label. Long enough for a real name,
// short enough that the sidecar cannot be used as storage.
const maxLabelLen = 120

// handleTakePatch merges fields into a take's sidecar. It is a merge, not a
// replace: pointers (and a RawMessage for trim) distinguish "field absent"
// from "field set to its zero value", so starring a take cannot silently clear
// its label.
func (a *API) handleTakePatch(w http.ResponseWriter, r *http.Request) {
	name, err := a.safeTakeName(r.URL.Query().Get("file"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	wav := filepath.Join(a.cfg.OutputDir, name)
	if _, err := os.Stat(wav); err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}

	var body struct {
		Label   *string         `json:"label"`
		Starred *bool           `json:"starred"`
		Trim    json.RawMessage `json:"trim"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	m := audio.ReadMeta(wav)

	if body.Label != nil {
		label := strings.TrimSpace(*body.Label)
		if len(label) > maxLabelLen {
			label = label[:maxLabelLen]
		}
		m.Label = label
	}
	if body.Starred != nil {
		m.Starred = *body.Starred
	}
	if body.Trim != nil {
		if string(body.Trim) == "null" {
			m.Trim = nil
		} else {
			var tr audio.Trim
			if err := json.Unmarshal(body.Trim, &tr); err != nil {
				writeErr(w, http.StatusBadRequest, "invalid trim")
				return
			}
			if tr.StartFrame < 0 || tr.EndFrame <= tr.StartFrame {
				writeErr(w, http.StatusBadRequest, "trim end_frame must be greater than start_frame")
				return
			}
			m.Trim = &tr
		}
	}

	if err := audio.WriteMeta(wav, m); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, m)
}
