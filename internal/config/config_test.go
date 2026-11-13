package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/YunusEmreAlps/geko/internal/storage"
)

func TestLoadDefaults(t *testing.T) {
	t.Chdir(t.TempDir())
	clearConfigEnv(t)

	dir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := Config{RetentionDays: DefaultRetentionDays, MaxDBMiB: DefaultMaxDBMiB, ListenAddr: DefaultListenAddr, DBPath: filepath.Join(dir, "geko", "geko.db")}
	want.OnFull, want.Redaction = storage.EvictOldest, DefaultRedaction
	want.Source = defaultTestSource()
	if got != want {
		t.Fatalf("Load() = %+v, want %+v", got, want)
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Chdir(t.TempDir())
	clearConfigEnv(t)
	t.Setenv("GEKO_LISTEN_ADDR", "[::1]:4320")
	t.Setenv("GEKO_DB_PATH", "test/geko.db")
	t.Setenv("GEKO_RETENTION_DAYS", "14")
	t.Setenv("GEKO_MAX_DB_MIB", "512")
	t.Setenv("GEKO_ON_FULL", "reject")
	t.Setenv("GEKO_REDACTION", "strict")

	path, err := filepath.Abs("test/geko.db")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		RetentionDays: 14, MaxDBMiB: 512, ListenAddr: "[::1]:4320", DBPath: path,
		OnFull: storage.Reject, Redaction: "strict",
		Source: Source{
			DashboardAddr: SettingSource{Kind: "default"}, GRPCAddr: SettingSource{Kind: "default"},
			ListenAddr: envSource("GEKO_LISTEN_ADDR"), DBPath: envSource("GEKO_DB_PATH"),
			RetentionDays: envSource("GEKO_RETENTION_DAYS"), MaxDBMiB: envSource("GEKO_MAX_DB_MIB"),
			OnFull: envSource("GEKO_ON_FULL"), Redaction: envSource("GEKO_REDACTION"),
		},
	}
	if got != want {
		t.Fatalf("Load() = %+v, want %+v", got, want)
	}
}

func TestRejectUnsafeListener(t *testing.T) {
	t.Chdir(t.TempDir())
	clearConfigEnv(t)
	for _, addr := range []string{"0.0.0.0:4318", "[::]:4318", ":4318", "localhost:4318", "127.0.0.1:0", "127.0.0.1:65536"} {
		t.Run(addr, func(t *testing.T) {
			t.Setenv("GEKO_LISTEN_ADDR", addr)
			if _, err := Load(); err == nil {
				t.Fatalf("Load() accepted %q", addr)
			}
		})
	}
}

func TestLoadYAMLAndEnvironmentPrecedence(t *testing.T) {
	workingDir := t.TempDir()
	t.Chdir(workingDir)
	clearConfigEnv(t)
	writeConfig(t, defaultConfigPath, "listen_addr: '127.0.0.1:4320'\ndb_path: data/default.db\non_full: reject\nredaction: strict\n")

	got, err := Load()
	origin := defaultTestSource()
	origin.FilePath = filepath.Join(workingDir, defaultConfigPath)
	yamlSource := SettingSource{Kind: "yaml"}
	origin.ListenAddr, origin.DBPath, origin.OnFull, origin.Redaction = yamlSource, yamlSource, yamlSource, yamlSource
	if want := (Config{
		RetentionDays: DefaultRetentionDays, MaxDBMiB: DefaultMaxDBMiB, ListenAddr: "127.0.0.1:4320",
		DBPath: filepath.Join(workingDir, "data", "default.db"), OnFull: storage.Reject, Redaction: "strict",
		Source: origin,
	}); err != nil || got != want {
		t.Fatalf("Load() = %+v, %v, want %+v", got, err, want)
	}

	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "custom.yaml")
	writeConfig(t, configPath, "listen_addr: '[::1]:4321'\ndb_path: data/custom.db\n")
	t.Setenv("GEKO_CONFIG", configPath)
	got, err = Load()
	origin = defaultTestSource()
	origin.FilePath, origin.ListenAddr, origin.DBPath = configPath, yamlSource, yamlSource
	if want := (Config{
		RetentionDays: DefaultRetentionDays, MaxDBMiB: DefaultMaxDBMiB, ListenAddr: "[::1]:4321",
		DBPath: filepath.Join(configDir, "data", "custom.db"), Source: origin,
		OnFull: storage.EvictOldest, Redaction: DefaultRedaction,
	}); err != nil || got != want {
		t.Fatalf("Load() = %+v, %v, want %+v", got, err, want)
	}

	t.Setenv("GEKO_LISTEN_ADDR", "127.0.0.1:4322")
	t.Setenv("GEKO_DB_PATH", "override.db")
	got, err = Load()
	origin.ListenAddr, origin.DBPath = envSource("GEKO_LISTEN_ADDR"), envSource("GEKO_DB_PATH")
	if want := (Config{
		RetentionDays: DefaultRetentionDays, MaxDBMiB: DefaultMaxDBMiB, ListenAddr: "127.0.0.1:4322",
		DBPath: filepath.Join(workingDir, "override.db"), Source: origin,
		OnFull: storage.EvictOldest, Redaction: DefaultRedaction,
	}); err != nil || got != want {
		t.Fatalf("Load() = %+v, %v, want %+v", got, err, want)
	}
}

func TestRejectInvalidYAMLConfig(t *testing.T) {
	clearConfigEnv(t)
	cases := []struct {
		name, content, want string
	}{
		{"unknown key", "listen_adrr: '127.0.0.1:4318'", "listen_adrr"},
		{"malformed", "listen_addr: [", "decode config"},
		{"multiple documents", "db_path: a.db\n---\ndb_path: b.db", "single YAML document"},
		{"public listener", "listen_addr: '0.0.0.0:4318'", "loopback"},
		{"zero retention", "retention_days: 0", "retention_days"},
		{"too small database", "max_db_mib: 1", "max_db_mib"},
		{"unknown capacity policy", "on_full: other", "on_full"},
		{"unknown redaction mode", "redaction: other", "redaction"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "invalid.yaml")
			writeConfig(t, path, tc.content)
			t.Setenv("GEKO_CONFIG", path)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load() error = %v, want %q", err, tc.want)
			}
		})
	}

	path := filepath.Join(t.TempDir(), "missing.yaml")
	t.Setenv("GEKO_CONFIG", path)
	if _, err := Load(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load() error = %v, want missing config", err)
	}
}

func clearConfigEnv(t *testing.T) {
	t.Setenv("GEKO_ACCESS_TOKEN", "")
	t.Helper()
	for _, key := range []string{"GEKO_DASHBOARD_ADDR", "GEKO_GRPC_ADDR"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GEKO_CONFIG", "")
	t.Setenv("GEKO_LISTEN_ADDR", "")
	t.Setenv("GEKO_DB_PATH", "")
	t.Setenv("GEKO_RETENTION_DAYS", "")
	t.Setenv("GEKO_MAX_DB_MIB", "")
	t.Setenv("GEKO_ON_FULL", "")
	t.Setenv("GEKO_REDACTION", "")
}

func TestTokenRequiredForNonLoopback(t *testing.T) {
	t.Chdir(t.TempDir())
	clearConfigEnv(t)
	t.Setenv("GEKO_LISTEN_ADDR", "0.0.0.0:4318")
	t.Setenv("GEKO_DASHBOARD_ADDR", "0.0.0.0:8080")
	t.Setenv("GEKO_GRPC_ADDR", "[::]:4317")
	for _, value := range []string{"", "too-short", strings.Repeat("a", 32), strings.Repeat("ab", 32)} {
		t.Setenv("GEKO_ACCESS_TOKEN", value)
		if _, err := Load(); err == nil {
			t.Fatal("allowed non-loopback without a valid token")
		}
	}
	t.Setenv("GEKO_ACCESS_TOKEN", "3f9c1e7a0b5d48e2c6a1f0d9b7e4c2a8")
	cfg, err := Load()
	if err != nil || !cfg.AccessToken.Enabled() || cfg.ListenAddr != "0.0.0.0:4318" {
		t.Fatalf("token mode failed to load: %v", err)
	}
}

func defaultTestSource() Source {
	d := SettingSource{Kind: "default"}
	return Source{DashboardAddr: d, GRPCAddr: d, ListenAddr: d, DBPath: d, RetentionDays: d, MaxDBMiB: d, OnFull: d, Redaction: d}
}

func TestOptionalListeners(t *testing.T) {
	t.Chdir(t.TempDir())
	clearConfigEnv(t)
	writeConfig(t, defaultConfigPath, "dashboard_addr: '127.0.0.1:8081'\ngrpc_addr: '[::1]:4317'\n")
	cfg, err := Load()
	if err != nil || cfg.DashboardAddr != "127.0.0.1:8081" || cfg.GRPCAddr != "[::1]:4317" || cfg.Source.GRPCAddr.Kind != "yaml" {
		t.Fatalf("config=%+v, %v", cfg, err)
	}
	t.Setenv("GEKO_GRPC_ADDR", "")
	cfg, err = Load()
	if err != nil || cfg.GRPCAddr != "" || cfg.Source.GRPCAddr.Kind != "env" {
		t.Fatalf("disable gRPC=%+v, %v", cfg, err)
	}
	for _, key := range []string{"GEKO_GRPC_ADDR", "GEKO_DASHBOARD_ADDR"} {
		t.Run(key, func(t *testing.T) {
			for _, addr := range []string{"0.0.0.0:4317", "localhost:4317", "127.0.0.1:0", "[::1%lo]:4317", "127.0.0.1:4318", "invalid"} {
				t.Setenv(key, addr)
				if _, err := Load(); err == nil {
					t.Fatalf("accepted %s=%s", key, addr)
				}
			}
		})
	}
}

func envSource(variable string) SettingSource {
	return SettingSource{Kind: "env", Variable: variable}
}

func writeConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
