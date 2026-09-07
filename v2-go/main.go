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

	"github.com/gabeduke/audio-dashcam/v2-go/api"
	"github.com/gabeduke/audio-dashcam/v2-go/audio"
	"github.com/gabeduke/audio-dashcam/v2-go/config"
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

	cap := audio.NewCapture(cfg)
	if err := cap.Start(); err != nil {
		log.Fatalf("capture: %v", err)
	}
	defer cap.Stop()

	saver := audio.NewSaver(cap)

	r := mux.NewRouter()
	api.New(cfg, cap, saver).SetupRoutes(r)
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

// staticDir resolves the UI directory next to the binary so the service works
// regardless of the working directory systemd hands it.
func staticDir() string {
	if v := os.Getenv("STATIC_DIR"); v != "" {
		return v
	}
	if exe, err := os.Executable(); err == nil {
		if d := filepath.Join(filepath.Dir(exe), "static"); dirExists(d) {
			return d
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "audio-dashcam", "v2-go", "static")
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
