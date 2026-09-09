package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gabeduke/hindsight/internal/api"
	"github.com/gabeduke/hindsight/internal/audio"
	"github.com/gabeduke/hindsight/internal/config"
	"github.com/gabeduke/hindsight/internal/midi"
	"github.com/gorilla/mux"
)

func main() {
	log.SetFlags(log.Ltime)

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if err := os.MkdirAll(cfg.OutputDir, 0o755); err != nil {
		log.Fatalf("output dir: %v", err)
	}
	log.Printf("[*] audio-dashcam v2 — %s", cfg)

	cap := audio.NewCapture(cfg, audio.NewDeviceSource(cfg))
	if err := cap.Start(); err != nil {
		log.Fatalf("capture: %v", err)
	}
	defer cap.Stop()

	saver := audio.NewSaver(cap)

	// The clock ring covers the same window as the audio ring, so a full-ring
	// save can still ask about its oldest end. Sized in pulses at the fastest
	// tempo the BPM field accepts.
	clock := midi.NewClock(midi.CapacityFor(cfg.RingSeconds))
	reader := midi.NewReader(cfg.DeviceMatch, clock)
	reader.Start()
	defer reader.Stop()
	saver.SetTempoSource(reader)

	r := mux.NewRouter()
	api.New(cfg, cap, saver, cap.Envelope(), reader).SetupRoutes(r)
	r.PathPrefix("/").Handler(noCacheShell(http.FileServer(http.Dir(staticDir()))))

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: it would cut off long websocket streams.
	}

	go func() {
		log.Printf("[*] listening on :%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	log.Printf("[*] shutting down")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

// staticDir resolves the UI directory. It is a thin wrapper so that the
// decision itself stays pure and testable.
func staticDir() string {
	exe, _ := os.Executable()
	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	return resolveStaticDir(os.Getenv("STATIC_DIR"), exe, cwd, home, dirExists)
}

// resolveStaticDir picks the UI directory from the layouts this ships in.
//
// Executable-relative candidates come first so an installed binary is never
// confused by whatever directory systemd happened to start it in. The
// working directory is consulted only afterwards, which is what makes
// `go run ./cmd/hindsight` work from a checkout -- there the binary lives in
// a temporary build directory with no UI anywhere near it.
func resolveStaticDir(envDir, exePath, cwd, home string, exists func(string) bool) string {
	if envDir != "" {
		return envDir
	}
	dir := filepath.Dir(exePath)
	candidates := []string{
		filepath.Join(dir, "..", "web", "static"), // release / deploy.sh
		filepath.Join(dir, "static"),              // flat
		filepath.Join(dir, "..", "static"),        // flat, one level down
		filepath.Join(cwd, "web", "static"),       // go run from a checkout
	}
	for _, c := range candidates {
		if exists(c) {
			return filepath.Clean(c)
		}
	}
	return filepath.Join(home, "hindsight", "web", "static")
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// noCacheShell keeps the app shell fresh so a redeploy is picked up on reload,
// while letting fingerprinted vendor assets cache normally.
func noCacheShell(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Ext(r.URL.Path) {
		case ".html", ".js", ".css", ".json", "":
			w.Header().Set("Cache-Control", "no-cache")
		}
		next.ServeHTTP(w, r)
	})
}
