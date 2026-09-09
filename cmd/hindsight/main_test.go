package main

import (
	"path/filepath"
	"testing"
)

// existsIn returns a predicate reporting true for exactly the given paths,
// so a layout can be described without touching the filesystem.
func existsIn(paths ...string) func(string) bool {
	set := make(map[string]bool, len(paths))
	for _, p := range paths {
		set[filepath.Clean(p)] = true
	}
	return func(p string) bool { return set[filepath.Clean(p)] }
}

func TestResolveStaticDir(t *testing.T) {
	const home = "/home/pi"

	tests := []struct {
		name   string
		env    string
		exe    string
		cwd    string
		exists func(string) bool
		want   string
	}{
		{
			name: "STATIC_DIR wins over every layout",
			env:  "/srv/ui",
			exe:  "/home/pi/hindsight/bin/hindsight",
			cwd:  "/home/pi/hindsight",
			// Deliberately a real layout too: the override must still win.
			exists: existsIn("/home/pi/hindsight/web/static"),
			want:   "/srv/ui",
		},
		{
			name:   "unpacked release: web/static beside bin/",
			exe:    "/home/pi/hindsight/bin/hindsight",
			cwd:    "/home/pi",
			exists: existsIn("/home/pi/hindsight/web/static"),
			want:   "/home/pi/hindsight/web/static",
		},
		{
			name:   "flat layout: static/ beside the binary",
			exe:    "/opt/hindsight/hindsight",
			cwd:    "/",
			exists: existsIn("/opt/hindsight/static"),
			want:   "/opt/hindsight/static",
		},
		{
			name:   "go run from a checkout: only the cwd locates the UI",
			exe:    "/var/folders/T/go-build123/b001/exe/hindsight",
			cwd:    "/Users/dev/hindsight",
			exists: existsIn("/Users/dev/hindsight/web/static"),
			want:   "/Users/dev/hindsight/web/static",
		},
		{
			name:   "nothing found: the install root is the last word",
			exe:    "/usr/bin/hindsight",
			cwd:    "/tmp",
			exists: existsIn(),
			want:   "/home/pi/hindsight/web/static",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveStaticDir(tt.env, tt.exe, tt.cwd, home, tt.exists)
			if got != tt.want {
				t.Errorf("resolveStaticDir() = %q, want %q", got, tt.want)
			}
		})
	}
}
