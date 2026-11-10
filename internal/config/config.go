package config

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
)

const DefaultListenAddr = "127.0.0.1:4318"

type Config struct {
	ListenAddr string
	DBPath     string
}

func Load() (Config, error) {
	addr := os.Getenv("GEKO_LISTEN_ADDR")
	if addr == "" {
		addr = DefaultListenAddr
	}
	endpoint, err := netip.ParseAddrPort(addr)
	if err != nil || !endpoint.Addr().IsLoopback() || endpoint.Addr().Zone() != "" || endpoint.Port() == 0 {
		return Config{}, fmt.Errorf("GEKO_LISTEN_ADDR %q must be a loopback IP and a nonzero port (for example, %s)", addr, DefaultListenAddr)
	}

	path := os.Getenv("GEKO_DB_PATH")
	if path == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return Config{}, fmt.Errorf("locate user configuration directory (or set GEKO_DB_PATH): %w", err)
		}
		path = filepath.Join(dir, "geko", "geko.db")
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return Config{}, fmt.Errorf("resolve GEKO_DB_PATH: %w", err)
	}

	return Config{ListenAddr: endpoint.String(), DBPath: path}, nil
}
