//go:build !windows

package config

import (
	"os"
	"path/filepath"
)

func replaceConfigFile(temp, target string) (string, error) {
	if err := os.Rename(temp, target); err != nil {
		return "", err
	}
	dir, err := os.Open(filepath.Dir(target))
	if err == nil {
		defer dir.Close()
		err = dir.Sync()
	}
	if err != nil {
		return "Saved and applied, but directory synchronization was unavailable; crash durability is not confirmed.", nil
	}
	return "", nil
}
