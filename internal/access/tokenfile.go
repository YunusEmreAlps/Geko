package access

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"slices"
	"time"

	"go.yaml.in/yaml/v3"
)

const maxTokenFile = 64 << 10

// LoadTokenFile reads tokens from a YAML file:
//
//	access: <token>
//	ingest: {storefront: <token>}
//	read: {agent: <token>}
func LoadTokenFile(path string) (Token, error) {
	data, err := readTokenFile(path)
	if err != nil {
		return Token{}, err
	}
	return parseTokenFile(data, path)
}

func readTokenFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open token file: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxTokenFile+1))
	if err != nil {
		return nil, fmt.Errorf("read token file %q: %w", path, err)
	}
	if len(data) > maxTokenFile {
		return nil, fmt.Errorf("token file %q exceeds 64 KiB", path)
	}
	return data, nil
}

func parseTokenFile(data []byte, path string) (Token, error) {
	var values struct {
		Access string            `yaml:"access"`
		Ingest map[string]string `yaml:"ingest"`
		Read   map[string]string `yaml:"read"`
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&values); err != nil {
		if errors.Is(err, io.EOF) {
			return Token{}, fmt.Errorf("token file %q is empty; set access", path)
		}
		// The decoder can quote the offending line, which may hold a token.
		return Token{}, fmt.Errorf("token file %q is not valid YAML with access, ingest and read keys", path)
	}
	if values.Access == "" {
		return Token{}, fmt.Errorf("token file %q must set access", path)
	}
	token, err := parseAccess(values.Access, "access in "+path)
	if err != nil {
		return Token{}, err
	}
	for _, scoped := range []struct {
		key    string
		values map[string]string
		scope  Scope
	}{{"ingest", values.Ingest, ScopeIngest}, {"read", values.Read, ScopeRead}} {
		names := make([]string, 0, len(scoped.values))
		for name := range scoped.values {
			names = append(names, name)
		}
		slices.Sort(names)
		pairs := make([]pair, 0, len(names))
		for _, name := range names {
			pairs = append(pairs, pair{name, scoped.values[name]})
		}
		if token, err = token.withScoped(pairs, scoped.key+" in "+path, scoped.scope); err != nil {
			return Token{}, err
		}
	}
	return token, nil
}

// WatchTokenFile reloads token whenever the file's content changes, until ctx
// ends. An invalid file is reported and the previous tokens stay in use.
func WatchTokenFile(ctx context.Context, path string, token Token, interval time.Duration) {
	// Start without a digest: the file may have changed since token was loaded.
	// Reloading identical tokens keeps sessions.
	var last [sha256.Size]byte
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		data, err := readTokenFile(path)
		if err != nil {
			log.Printf("tokens: %v; keeping the current tokens", err)
			continue
		}
		sum := sha256.Sum256(data)
		if sum == last {
			continue
		}
		last = sum
		next, err := parseTokenFile(data, path)
		changed := false
		if err == nil {
			changed, err = token.reload(next)
		}
		if err != nil {
			log.Printf("tokens: %v; keeping the current tokens", err)
			continue
		}
		if changed {
			log.Printf("tokens: reloaded %s", path)
		}
	}
}
