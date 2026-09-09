package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
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

// version is stamped at build time with -ldflags "-X main.version=v2026.09.09.1".
var version = "dev"

func main() {
	log.SetFlags(log.Ltime)

	demo := flag.Bool("demo", false, "run with a synthetic audio source and no hardware")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	cfg.Version = version
	if err := os.MkdirAll(cfg.OutputDir, 0o755); err != nil {
		log.Fatalf("output dir: %v", err)
	}
	log.Printf("[*] hindsight %s — %s", version, cfg)

	// FixedClock and Reader each satisfy both consumers, so both are held
	// through their interfaces rather than asserted back out of one.
	var (
		src   audio.Source
		clock api.MIDISource
		tempo audio.TempoSource
	)
	if *demo {
		fc := midi.NewFixedClock(audio.DemoBPM)
		src, clock, tempo = audio.NewDemoSource(cfg), fc, fc
		log.Printf("[*] demo mode — synthetic audio, no hardware")
	} else {
		src = audio.NewDeviceSource(cfg)
	}

	cap := audio.NewCapture(cfg, src)
	if err := cap.Start(); err != nil {
		log.Fatalf("capture: %v", err)
	}
	defer cap.Stop()

	saver := audio.NewSaver(cap)

	if !*demo {
		// The clock ring covers the same window as the audio ring, so a
		// full-ring save can still ask about its oldest end. Sized in pulses
		// at the fastest tempo the BPM field accepts.
		mc := midi.NewClock(midi.CapacityFor(cfg.RingSeconds))
		reader := midi.NewReader(cfg.DeviceMatch, mc)
		reader.Start()
		defer reader.Stop()
		clock, tempo = reader, reader
	}
	saver.SetTempoSource(tempo)

	r := mux.NewRouter()
	api.New(cfg, cap, saver, cap.Envelope(), clock).SetupRoutes(r)
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

// noCacheShell sends Cache-Control: no-cache for the app shell -- HTML, JS,
// CSS, JSON and directory indexes -- so the browser revalidates them and a
// redeploy is picked up on reload rather than on a cache expiry.
//
// Nothing under web/static is fingerprinted, so this covers the vendored
// WaveSurfer copy too: /vendor/wavesurfer.esm.js is a .js like any other.
// Everything else -- the icons, and anything else without one of those
// extensions -- falls through to http.FileServer's ETag and Last-Modified
// handling untouched.
func noCacheShell(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Ext(r.URL.Path) {
		case ".html", ".js", ".css", ".json", "":
			w.Header().Set("Cache-Control", "no-cache")
		}
		next.ServeHTTP(w, r)
	})
}
