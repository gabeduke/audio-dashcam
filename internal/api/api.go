// Package api exposes the dashcam over HTTP.
package api

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gabeduke/hindsight/internal/audio"
	"github.com/gabeduke/hindsight/internal/config"
	"github.com/gorilla/mux"
)

// MIDISource is the live clock, as the API needs it: is it there, and what is
// it saying right now. Satisfied by *midi.Reader; nil is a valid value and
// means this build has no clock.
type MIDISource interface {
	Connected() bool
	BPM(start, end time.Time) (float64, bool)
}

type API struct {
	cfg   *config.Config
	cap   *audio.Capture
	saver *audio.Saver
	env   *audio.Envelope
	midi  MIDISource
}

func New(cfg *config.Config, cap *audio.Capture, saver *audio.Saver, env *audio.Envelope, m MIDISource) *API {
	return &API{cfg: cfg, cap: cap, saver: saver, env: env, midi: m}
}

// liveTempoWindow is how far back the status poll asks about. Eight seconds is
// two quarter notes' worth down to about 15 BPM, so the reading survives any
// tempo the field accepts, while staying short enough that the number moves
// with the room rather than lagging it.
const liveTempoWindow = 8 * time.Second

// midiState reports the clock's presence and its current tempo. A nil BPM means
// no defensible reading -- which is the shape that catches the EP's clock-send
// being switched off, since that looks exactly like connected and silent.
func (a *API) midiState() (bool, *float64) {
	if a.midi == nil {
		return false, nil
	}
	connected := a.midi.Connected()
	now := time.Now()
	if bpm, ok := a.midi.BPM(now.Add(-liveTempoWindow), now); ok {
		return connected, &bpm
	}
	return connected, nil
}

func (a *API) SetupRoutes(r *mux.Router) {
	r.HandleFunc("/api/status", a.handleStatus).Methods(http.MethodGet, http.MethodHead)
	r.HandleFunc("/api/jams", a.handleJams).Methods(http.MethodGet, http.MethodHead)
	r.HandleFunc("/api/trigger", a.handleTrigger).Methods(http.MethodPost)
	r.HandleFunc("/api/delete", a.handleDelete).Methods(http.MethodDelete)
	r.HandleFunc("/api/take", a.handleTakePatch).Methods(http.MethodPatch)
	r.HandleFunc("/api/download", a.handleDownload).Methods(http.MethodGet, http.MethodHead)
	r.HandleFunc("/api/peaks", a.handlePeaks).Methods(http.MethodGet, http.MethodHead)
	r.HandleFunc("/api/envelope", a.handleEnvelope).Methods(http.MethodGet, http.MethodHead)
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
	MIDIConnected   bool      `json:"midi_connected"`
	MIDIBPM         *float64  `json:"midi_bpm"`
}

func (a *API) handleStatus(w http.ResponseWriter, r *http.Request) {
	free, pct := a.saver.FreeGB()
	midiConnected, midiBPM := a.midiState()

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
		MIDIConnected:   midiConnected,
		MIDIBPM:         midiBPM,
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

// maxBuckets caps what a client can ask for. The ribbon wants about one bucket
// per CSS pixel and the widest target is ~1246px, so this is generous.
//
// It is also the only upper bound on the work one request makes the envelope
// do — Buckets clamps the low end but not the high end — so this is what stops
// an unbounded ?buckets= from turning into an unbounded aggregation.
const maxBuckets = 600

type envelopeResponse struct {
	RingSeconds     float64 `json:"ring_seconds"`
	BufferedSeconds float64 `json:"buffered_seconds"`
	EdgeSeconds     float64 `json:"edge_seconds"`
	// Buckets is base64 rather than a JSON array: 400 buckets is 536 chars
	// against ~1600, it is the encoding scripts/take-envelope.py already
	// writes, and Go marshals []byte this way with no conversion.
	Buckets       []byte    `json:"buckets"`
	SignalSeconds []float64 `json:"signal_seconds"`
}

// handleEnvelope serves the buffer ribbon: log-spaced buckets over the whole
// ring, plus seconds-of-signal for each capture tier the client names.
//
// The tiers come from the client so buildDurations() stays the only place that
// decides what they are.
func (a *API) handleEnvelope(w http.ResponseWriter, r *http.Request) {
	if a.env == nil {
		writeErr(w, http.StatusServiceUnavailable, "envelope not available")
		return
	}

	buckets := 400
	if v := r.URL.Query().Get("buckets"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "buckets must be an integer")
			return
		}
		buckets = n
	}
	if buckets < 1 {
		buckets = 1
	}
	if buckets > maxBuckets {
		buckets = maxBuckets
	}

	ring := a.env.RingSeconds()
	var spans []float64
	if v := r.URL.Query().Get("spans"); v != "" {
		for _, part := range strings.Split(v, ",") {
			f, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
			if err != nil || f < 0 {
				writeErr(w, http.StatusBadRequest, "spans must be non-negative numbers")
				return
			}
			spans = append(spans, f)
		}
	}

	// Non-nil so an empty spans list marshals as [] rather than null.
	sig := make([]float64, len(spans))
	for i, s := range spans {
		if s == 0 { // 0 means the whole ring, matching /api/trigger
			s = ring
		}
		sig[i] = a.env.SignalSeconds(s)
	}

	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, envelopeResponse{
		RingSeconds:     ring,
		BufferedSeconds: a.env.BufferedSeconds(),
		EdgeSeconds:     a.env.EdgeSecondsEffective(),
		Buckets:         a.env.Buckets(buckets),
		SignalSeconds:   sig,
	})
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

// maxLabelLen caps a user-supplied take label, in characters (runes), not
// bytes — a multi-byte label must not get a shorter effective cap than an
// ASCII one. Long enough for a real name, short enough that the sidecar
// cannot be used as storage.
const maxLabelLen = 120

// minBPM and maxBPM bound an edited tempo. The range is deliberately far wider
// than the EP will ever produce: the free-running clock does not reliably match
// the loaded project tempo, so the point of the field is that the owner
// overrides it -- including for takes whose clock reading was confidently wrong.
const (
	minBPM = 20.0
	maxBPM = 400.0
)

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
	fi, err := os.Stat(wav)
	if err != nil || !fi.Mode().IsRegular() {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}

	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	var body struct {
		Label   *string         `json:"label"`
		Starred *bool           `json:"starred"`
		Trim    json.RawMessage `json:"trim"`
		BPM     json.RawMessage `json:"bpm"`
	}
	if err := dec.Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	// A second JSON value in the body (e.g. two concatenated objects) would
	// otherwise be silently ignored, applying only the first.
	if dec.More() {
		writeErr(w, http.StatusBadRequest, "unexpected trailing content in body")
		return
	}

	m := audio.ReadMeta(wav)

	if body.Label != nil {
		m.Label = sanitizeLabel(*body.Label)
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

	// RawMessage, like trim: absent, null and a value are three states. An
	// empty submission from the UI arrives as null and clears the field rather
	// than storing a tempo of zero.
	if body.BPM != nil {
		if string(body.BPM) == "null" {
			m.BPM = nil
		} else {
			var v float64
			if err := json.Unmarshal(body.BPM, &v); err != nil {
				writeErr(w, http.StatusBadRequest, "bpm must be a number")
				return
			}
			if math.IsNaN(v) || math.IsInf(v, 0) || v < minBPM || v > maxBPM {
				writeErr(w, http.StatusBadRequest,
					fmt.Sprintf("bpm must be between %g and %g", minBPM, maxBPM))
				return
			}
			v = math.Round(v*100) / 100
			m.BPM = &v
		}
	}

	if err := audio.WriteMeta(wav, m); err != nil {
		switch {
		case errors.Is(err, audio.ErrNewerSidecar):
			writeErr(w, http.StatusConflict, "this take was edited by a newer version")
		case errors.Is(err, syscall.ENOSPC):
			writeErr(w, http.StatusInsufficientStorage, "disk full")
		default:
			// The real error names absolute paths and the temp-file scheme, so log
			// it and keep it off the wire.
			log.Printf("take patch %s: %v", name, err)
			writeErr(w, http.StatusInternalServerError, "could not save")
		}
		return
	}

	// Explicit rather than returning audio.Meta: its omitempty tags would drop
	// the very fields a clear-to-empty patch just changed, and version is
	// internal.
	writeJSON(w, http.StatusOK, struct {
		Label   string      `json:"label"`
		Starred bool        `json:"starred"`
		Trim    *audio.Trim `json:"trim"`
		BPM     *float64    `json:"bpm"`
	}{Label: m.Label, Starred: m.Starred, Trim: m.Trim, BPM: m.BPM})
}

// sanitizeLabel prepares a user-supplied label for storage. It strips control
// characters and Unicode format characters (category Cf — a right-to-left
// override, a zero-width space) that are invisible or misleading when
// rendered; this is about display integrity, not injection, since the label
// is never interpreted as markup or code. It then trims surrounding
// whitespace and caps the result by rune count, not byte count, so a
// multi-byte label isn't truncated mid-rune.
func sanitizeLabel(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > maxLabelLen {
		s = string([]rune(s)[:maxLabelLen])
	}
	return s
}
