package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("GEKO_LISTEN_ADDR", "")
	t.Setenv("GEKO_DB_PATH", "")

	dir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := Config{ListenAddr: DefaultListenAddr, DBPath: filepath.Join(dir, "geko", "geko.db")}
	if got != want {
		t.Fatalf("Load() = %+v, want %+v", got, want)
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("GEKO_LISTEN_ADDR", "[::1]:4320")
	t.Setenv("GEKO_DB_PATH", "test/geko.db")

	path, err := filepath.Abs("test/geko.db")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if want := (Config{ListenAddr: "[::1]:4320", DBPath: path}); got != want {
		t.Fatalf("Load() = %+v, want %+v", got, want)
	}
}

func TestRejectUnsafeListener(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:4318", "[::]:4318", ":4318", "localhost:4318", "127.0.0.1:0", "127.0.0.1:65536"} {
		t.Run(addr, func(t *testing.T) {
			t.Setenv("GEKO_LISTEN_ADDR", addr)
			if _, err := Load(); err == nil {
				t.Fatalf("Load() accepted %q", addr)
			}
		})
	}
}
