package config

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.yaml.in/yaml/v3"
)

const maxRuntimeConfigBytes = 1 << 20

var (
	ErrSettingsConflict = errors.New("configuration changed; reload Settings, or restart Geko if YAML was edited externally")
	ErrSettingsLocked   = errors.New("these settings are not editable in this instance")
	ErrSettingsInvalid  = errors.New("use an HTTP(S) trace URL containing {trace_id} without credentials and a name of at most 40 bytes; clear both fields to disable")
	ErrSettingsSave     = errors.New("could not save configuration; check file permissions, mount mode and available disk space")
)

// RuntimeState exposes only the settings supported for live updates, never the
// complete configuration (which can contain credentials).
type RuntimeState struct {
	Revision   string        `json:"revision"`
	ConfigFile string        `json:"config_file"`
	URL        string        `json:"trace_link_url"`
	Name       string        `json:"trace_link_name"`
	URLSource  SettingSource `json:"url_source"`
	NameSource SettingSource `json:"name_source"`
	Editable   bool          `json:"editable"`
	Reason     string        `json:"reason,omitempty"`
	AppliedAt  time.Time     `json:"applied_at"`
	Warning    string        `json:"warning,omitempty"`
}

// Runtime serializes updates and publishes value-only immutable snapshots.
// Only successful file replacement changes the effective snapshot; any
// directory synchronization warning is retained in the published state.
type Runtime struct {
	mu      sync.Mutex
	state   atomic.Pointer[RuntimeState]
	replace func(string, string) (string, error)
}

func NewRuntime(cfg Config) *Runtime {
	r := &Runtime{replace: replaceConfigFile}
	s := RuntimeState{Revision: cfg.Source.FileRevision, ConfigFile: cfg.Source.FilePath, URL: cfg.TraceLinkURL, Name: cfg.TraceLinkName, URLSource: cfg.Source.TraceLinkURL, NameSource: cfg.Source.TraceLinkName}
	switch {
	case cfg.ReadOnly:
		s.Reason = "This instance is read-only."
	case s.ConfigFile == "":
		s.Reason = "Load a YAML configuration file and restart Geko to enable persistent edits."
	case s.URLSource.Kind == "env" && s.NameSource.Kind == "env":
		s.Reason = "Both fields are managed by environment variables."
	default:
		// Resolve directory symlinks once. Replacing a symlinked config file
		// itself would break its target/mount semantics, so reject that case.
		info, err := os.Lstat(s.ConfigFile)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0222 == 0 {
			s.Reason = "The configuration must be a writable regular file, not a symlink."
		} else if info.Size() > maxRuntimeConfigBytes {
			s.Reason = "Live editing supports configuration files up to 1 MiB; edit this file externally and restart."
		} else if resolved, err := filepath.EvalSymlinks(s.ConfigFile); err != nil {
			s.Reason = "The configuration path could not be resolved."
		} else {
			s.ConfigFile = resolved
			data, _, err := runtimeFile(s.ConfigFile)
			if err != nil || revision(data) != s.Revision {
				s.Reason = "The configuration changed during startup; restart Geko before editing."
			} else if _, err := runtimeDocument(data); err != nil {
				s.Reason = "Live edits require explicit scalar trace-link fields without root YAML merges or shared anchors."
			} else {
				s.Editable = true
			}
		}
	}
	r.state.Store(&s)
	return r
}

func (r *Runtime) Snapshot() RuntimeState { return *r.state.Load() }

// Update persists before publishing. Concurrent Geko writers cooperate through
// an exclusive lock file; stale browser revisions and external edits conflict.
// Uncooperative external editors must not write YAML concurrently with Geko.
func (r *Runtime) Update(ctx context.Context, expected, link, name string) (RuntimeState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.Snapshot()
	if !s.Editable {
		return s, ErrSettingsLocked
	}
	if expected != s.Revision {
		return s, ErrSettingsConflict
	}
	if s.URLSource.Kind == "env" && link != s.URL || s.NameSource.Kind == "env" && name != s.Name {
		return s, ErrSettingsLocked
	}
	link, name, err := traceLink(link, name)
	if err != nil {
		return s, ErrSettingsInvalid
	}
	if err := ctx.Err(); err != nil {
		return s, err
	}
	lock, err := os.OpenFile(s.ConfigFile+".lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return s, ErrSettingsSave
	}
	defer func() { _ = lock.Close(); _ = os.Remove(s.ConfigFile + ".lock") }()
	data, mode, err := runtimeFile(s.ConfigFile)
	if err != nil {
		return s, ErrSettingsSave
	}
	if revision(data) != s.Revision {
		return s, ErrSettingsConflict
	}
	doc, err := runtimeDocument(data)
	if err != nil {
		return s, ErrSettingsConflict
	}
	if s.URLSource.Kind != "env" {
		setRuntimeScalar(doc.Content[0], "trace_link_url", link)
	}
	if s.NameSource.Kind != "env" {
		setRuntimeScalar(doc.Content[0], "trace_link_name", name)
	}
	var encoded bytes.Buffer
	encoder := yaml.NewEncoder(&encoded)
	encoder.SetIndent(2)
	if err := encoder.Encode(doc); err != nil {
		return s, ErrSettingsSave
	}
	if err := encoder.Close(); err != nil {
		return s, ErrSettingsSave
	}
	if encoded.Len() > maxRuntimeConfigBytes {
		return s, ErrSettingsSave
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.ConfigFile), ".geko-config-*")
	if err != nil {
		return s, ErrSettingsSave
	}
	defer os.Remove(tmp.Name())
	_, writeErr := tmp.Write(encoded.Bytes())
	if writeErr == nil {
		writeErr = tmp.Chmod(mode.Perm())
	}
	if writeErr == nil {
		writeErr = tmp.Sync()
	}
	writeErr = errors.Join(writeErr, tmp.Close())
	if writeErr != nil {
		return s, ErrSettingsSave
	}
	if err := ctx.Err(); err != nil {
		return s, err
	}
	// Check again immediately before replacement. The lock protects other
	// Geko processes; an external editor still needs operational coordination.
	latest, _, err := runtimeFile(s.ConfigFile)
	if err != nil {
		return s, ErrSettingsSave
	}
	if revision(latest) != s.Revision {
		return s, ErrSettingsConflict
	}
	warning, err := r.replace(tmp.Name(), s.ConfigFile)
	if err != nil {
		return s, ErrSettingsSave
	}
	s.URL, s.Name, s.Revision = link, name, revision(encoded.Bytes())
	if s.URLSource.Kind != "env" {
		s.URLSource = SettingSource{Kind: "yaml"}
	}
	if s.NameSource.Kind != "env" {
		s.NameSource = SettingSource{Kind: "yaml"}
	}
	s.AppliedAt, s.Warning = time.Now().UTC(), warning
	r.state.Store(&s)
	return s, nil
}

func revision(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

func runtimeFile(path string) ([]byte, os.FileMode, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, 0, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0222 == 0 || info.Size() > maxRuntimeConfigBytes {
		return nil, 0, ErrSettingsLocked
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxRuntimeConfigBytes+1))
	if err == nil && len(data) > maxRuntimeConfigBytes {
		err = ErrSettingsSave
	}
	return data, info.Mode(), err
}

func runtimeDocument(data []byte) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map", HeadComment: strings.TrimSpace(string(data))}}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, ErrSettingsInvalid
	}
	for i := 0; i < len(root.Content); i += 2 {
		key, value := root.Content[i], root.Content[i+1]
		if key.Value == "<<" {
			return nil, ErrSettingsInvalid
		}
		if key.Value == "trace_link_url" || key.Value == "trace_link_name" {
			if value.Kind != yaml.ScalarNode || value.Anchor != "" {
				return nil, ErrSettingsInvalid
			}
		}
	}
	return &doc, nil
}

func setRuntimeScalar(root *yaml.Node, key, value string) {
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			root.Content[i+1].Value, root.Content[i+1].Tag = value, "!!str"
			return
		}
	}
	root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
}
