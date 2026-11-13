package config

import (
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"

	"github.com/YunusEmreAlps/geko/internal/access"
	"github.com/YunusEmreAlps/geko/internal/storage"

	"go.yaml.in/yaml/v3"
)

const DefaultListenAddr = "127.0.0.1:4318"
const defaultConfigPath = "geko.yaml"
const DefaultRetentionDays = 7
const DefaultMaxDBMiB = 256
const DefaultRedaction = "best_effort"

type Config struct {
	AccessToken   access.Token       `yaml:"-"`
	ListenAddr    string             `yaml:"listen_addr"`
	DashboardAddr string             `yaml:"dashboard_addr"`
	GRPCAddr      string             `yaml:"grpc_addr"`
	DBPath        string             `yaml:"db_path"`
	RetentionDays int                `yaml:"retention_days"`
	MaxDBMiB      int                `yaml:"max_db_mib"`
	OnFull        storage.FullPolicy `yaml:"on_full"`
	Redaction     string             `yaml:"redaction"`
	Source        Source             `yaml:"-"`
}

// Source records where each effective setting came from.
type Source struct {
	DashboardAddr, GRPCAddr                                        SettingSource
	FilePath                                                       string
	ListenAddr, DBPath, RetentionDays, MaxDBMiB, OnFull, Redaction SettingSource
}

type SettingSource struct {
	Kind     string
	Variable string // Environment variable name, when Kind is env.
}

func Load() (Config, error) {
	configPath := os.Getenv("GEKO_CONFIG")
	optional := configPath == ""
	if optional {
		configPath = defaultConfigPath
	}
	cfg, err := loadFile(configPath, optional)
	if err != nil {
		return Config{}, err
	}
	cfg.AccessToken, err = access.ParseToken(os.Getenv("GEKO_ACCESS_TOKEN"))
	if err != nil {
		return Config{}, err
	}

	addr := cfg.ListenAddr
	if override := envOverride("GEKO_LISTEN_ADDR", &cfg.Source.ListenAddr); override != "" {
		addr = override
	}
	if addr == "" {
		addr = DefaultListenAddr
	}
	endpoint, err := netip.ParseAddrPort(addr)
	if err != nil || endpoint.Addr().Zone() != "" || endpoint.Port() == 0 {
		return Config{}, fmt.Errorf("listen_addr (GEKO_LISTEN_ADDR) %q must be an IP address and a nonzero port (for example, %s)", addr, DefaultListenAddr)
	}
	if !endpoint.Addr().IsLoopback() && !cfg.AccessToken.Enabled() {
		return Config{}, errors.New("listen_addr must use a loopback IP unless GEKO_ACCESS_TOKEN is configured")
	}

	path := cfg.DBPath
	if override := envOverride("GEKO_DB_PATH", &cfg.Source.DBPath); override != "" {
		path = override
	} else if path != "" && !filepath.IsAbs(path) {
		path = filepath.Join(filepath.Dir(configPath), path)
	}
	if path == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return Config{}, fmt.Errorf("locate user configuration directory (or set db_path or GEKO_DB_PATH): %w", err)
		}
		path = filepath.Join(dir, "geko", "geko.db")
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return Config{}, fmt.Errorf("resolve db_path (GEKO_DB_PATH): %w", err)
	}

	cfg.ListenAddr, cfg.DBPath = endpoint.String(), path
	for _, setting := range []struct {
		key, env string
		value    *string
		source   *SettingSource
	}{
		{"dashboard_addr", "GEKO_DASHBOARD_ADDR", &cfg.DashboardAddr, &cfg.Source.DashboardAddr},
		{"grpc_addr", "GEKO_GRPC_ADDR", &cfg.GRPCAddr, &cfg.Source.GRPCAddr},
	} {
		if value, ok := os.LookupEnv(setting.env); ok {
			*setting.value = value
			*setting.source = SettingSource{Kind: "env", Variable: setting.env}
		}
		if *setting.value == "" {
			continue
		}
		address, err := netip.ParseAddrPort(*setting.value)
		if err != nil || address.Addr().Zone() != "" || address.Port() == 0 {
			return Config{}, fmt.Errorf("%s (%s) must be an IP address and a nonzero port", setting.key, setting.env)
		}
		if !address.Addr().IsLoopback() && !cfg.AccessToken.Enabled() {
			return Config{}, fmt.Errorf("%s must use a loopback IP unless GEKO_ACCESS_TOKEN is configured", setting.key)
		}
		*setting.value = address.String()
	}
	if cfg.DashboardAddr != "" && cfg.DashboardAddr == cfg.ListenAddr {
		return Config{}, errors.New("dashboard_addr must differ from listen_addr; omit it to share the HTTP listener")
	}
	if cfg.GRPCAddr != "" && (cfg.GRPCAddr == cfg.ListenAddr || cfg.GRPCAddr == cfg.DashboardAddr) {
		return Config{}, errors.New("grpc_addr must differ from the HTTP and dashboard listeners")
	}
	cfg.RetentionDays, err = intSetting("retention_days", "GEKO_RETENTION_DAYS", &cfg.Source.RetentionDays, cfg.RetentionDays, 1, 3650)
	if err != nil {
		return Config{}, err
	}
	cfg.MaxDBMiB, err = intSetting("max_db_mib", "GEKO_MAX_DB_MIB", &cfg.Source.MaxDBMiB, cfg.MaxDBMiB, 8, 65536)
	if err != nil {
		return Config{}, err
	}
	if override := envOverride("GEKO_ON_FULL", &cfg.Source.OnFull); override != "" {
		cfg.OnFull = storage.FullPolicy(override)
	}
	if cfg.OnFull != storage.EvictOldest && cfg.OnFull != storage.Reject {
		return Config{}, fmt.Errorf("on_full (GEKO_ON_FULL) must be %q or %q", storage.EvictOldest, storage.Reject)
	}
	if override := envOverride("GEKO_REDACTION", &cfg.Source.Redaction); override != "" {
		cfg.Redaction = override
	}
	if cfg.Redaction != DefaultRedaction && cfg.Redaction != "strict" {
		return Config{}, errors.New("redaction (GEKO_REDACTION) must be best_effort or strict")
	}
	return cfg, nil
}

func envOverride(name string, source *SettingSource) string {
	value := os.Getenv(name)
	if value != "" {
		*source = SettingSource{Kind: "env", Variable: name}
	}
	return value
}

func intSetting(key, env string, source *SettingSource, value, minValue, maxValue int) (int, error) {
	var err error
	if override := envOverride(env, source); override != "" {
		value, err = strconv.Atoi(override)
	}
	if err != nil || value < minValue || value > maxValue {
		return 0, fmt.Errorf("%s (%s) must be an integer from %d to %d", key, env, minValue, maxValue)
	}
	return value, nil
}

func loadFile(path string, optional bool) (Config, error) {
	defaultSource := SettingSource{Kind: "default"}
	cfg := Config{
		RetentionDays: DefaultRetentionDays, MaxDBMiB: DefaultMaxDBMiB,
		OnFull: storage.EvictOldest, Redaction: DefaultRedaction,
		Source: Source{
			DashboardAddr: defaultSource, GRPCAddr: defaultSource,
			ListenAddr: defaultSource, DBPath: defaultSource, RetentionDays: defaultSource,
			MaxDBMiB: defaultSource, OnFull: defaultSource, Redaction: defaultSource,
		},
	}
	file, err := os.Open(path)
	if optional && errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("open config %q: %w", path, err)
	}
	defer file.Close()

	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return cfg, fmt.Errorf("resolve config path %q: %w", path, err)
	}
	cfg.Source.FilePath = absolutePath

	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	var values struct {
		DashboardAddr *string             `yaml:"dashboard_addr"`
		GRPCAddr      *string             `yaml:"grpc_addr"`
		ListenAddr    *string             `yaml:"listen_addr"`
		DBPath        *string             `yaml:"db_path"`
		RetentionDays *int                `yaml:"retention_days"`
		MaxDBMiB      *int                `yaml:"max_db_mib"`
		OnFull        *storage.FullPolicy `yaml:"on_full"`
		Redaction     *string             `yaml:"redaction"`
	}
	err = decoder.Decode(&values)
	if errors.Is(err, io.EOF) {
		return cfg, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	var extra yaml.Node
	err = decoder.Decode(&extra)
	if err == nil {
		return Config{}, fmt.Errorf("config %q must contain a single YAML document", path)
	}
	if !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	// Empty listener and path values retain their historical default behavior.
	if values.ListenAddr != nil && *values.ListenAddr != "" {
		yamlSetting(values.ListenAddr, &cfg.ListenAddr, &cfg.Source.ListenAddr)
	}
	if values.DBPath != nil && *values.DBPath != "" {
		yamlSetting(values.DBPath, &cfg.DBPath, &cfg.Source.DBPath)
	}
	yamlSetting(values.RetentionDays, &cfg.RetentionDays, &cfg.Source.RetentionDays)
	yamlSetting(values.DashboardAddr, &cfg.DashboardAddr, &cfg.Source.DashboardAddr)
	yamlSetting(values.GRPCAddr, &cfg.GRPCAddr, &cfg.Source.GRPCAddr)
	yamlSetting(values.MaxDBMiB, &cfg.MaxDBMiB, &cfg.Source.MaxDBMiB)
	yamlSetting(values.OnFull, &cfg.OnFull, &cfg.Source.OnFull)
	yamlSetting(values.Redaction, &cfg.Redaction, &cfg.Source.Redaction)
	return cfg, nil
}

func yamlSetting[T any](value *T, target *T, source *SettingSource) {
	if value != nil {
		*target = *value
		*source = SettingSource{Kind: "yaml"}
	}
}
