package config

import (
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

const DefaultListenAddr = "127.0.0.1:4318"
const defaultConfigPath = "geko.yaml"

type Config struct {
	ListenAddr string `yaml:"listen_addr"`
	DBPath     string `yaml:"db_path"`
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

	addr := cfg.ListenAddr
	if override := os.Getenv("GEKO_LISTEN_ADDR"); override != "" {
		addr = override
	}
	if addr == "" {
		addr = DefaultListenAddr
	}
	endpoint, err := netip.ParseAddrPort(addr)
	if err != nil || !endpoint.Addr().IsLoopback() || endpoint.Addr().Zone() != "" || endpoint.Port() == 0 {
		return Config{}, fmt.Errorf("listen_addr (GEKO_LISTEN_ADDR) %q must be a loopback IP and a nonzero port (for example, %s)", addr, DefaultListenAddr)
	}

	path := cfg.DBPath
	if override := os.Getenv("GEKO_DB_PATH"); override != "" {
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

	return Config{ListenAddr: endpoint.String(), DBPath: path}, nil
}

func loadFile(path string, optional bool) (Config, error) {
	var cfg Config
	file, err := os.Open(path)
	if optional && errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("open config %q: %w", path, err)
	}
	defer file.Close()

	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	err = decoder.Decode(&cfg)
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
	return cfg, nil
}
