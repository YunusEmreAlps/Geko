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

	"github.com/YunusEmreAlps/geko/internal/budget"
	"github.com/YunusEmreAlps/geko/internal/ownership"
	"go.yaml.in/yaml/v3"
)

type liveGroup uint8

const (
	liveTraceLinks liveGroup = iota
	liveOwners
	liveBudget
	liveOperations
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

// Runtime serializes updates and publishes immutable snapshots.
// Only successful file replacement changes the effective snapshot; any
// directory synchronization warning is retained in the published state.
type Runtime struct {
	mu      sync.Mutex
	state   atomic.Pointer[runtimeSnapshot]
	replace func(string, string) (string, error)
}

type runtimeSnapshot struct {
	RuntimeState
	owners                      ownership.Owners
	ownerSource                 SettingSource
	ownersEditable              bool
	ownersReason                string
	ownersAppliedAt             time.Time
	budgetPolicy                budget.Policy
	budgetSources               BudgetSources
	budgetEditable              bool
	budgetReason                string
	budgetAppliedAt             time.Time
	operations                  OperationsPolicy
	opsSources                  map[string]SettingSource
	opsEditable                 bool
	opsReason                   string
	opsAppliedAt                time.Time
	hasBudget, hasNotifications bool
}

func NewRuntime(cfg Config) *Runtime {
	r := &Runtime{replace: replaceConfigFile}
	file, data, reason := runtimeTarget(cfg)
	source := cfg.Source.Owners
	if source.Kind == "" {
		source = SettingSource{Kind: "default"}
	}
	s := runtimeSnapshot{RuntimeState: RuntimeState{Revision: cfg.Source.FileRevision, ConfigFile: file, URL: cfg.TraceLinkURL, Name: cfg.TraceLinkName, URLSource: cfg.Source.TraceLinkURL, NameSource: cfg.Source.TraceLinkName}, owners: cfg.Owners, ownerSource: source}
	s.Reason, s.ownersReason = reason, reason
	if reason == "" {
		if _, err := runtimeDocument(data); err != nil {
			s.Reason = "Live trace edits require explicit scalar trace-link fields without root YAML merges or shared anchors."
		} else if s.URLSource.Kind == "env" && s.NameSource.Kind == "env" {
			s.Reason = "Both trace-link fields are managed by environment variables."
		} else {
			s.Editable = true
		}
		if _, err := runtimeOwnerDocument(data); err != nil {
			s.ownersReason = "Live ownership edits require a mapping without YAML aliases, anchors or merges in owners."
		} else if source.Kind == "env" {
			s.ownersReason = "Owner mappings are managed by GEKO_OWNERS."
		} else if len(cfg.Owners.Rules()) > MaxLiveOwnerRules {
			s.ownersReason = "Too many owner rules for the live editor; edit YAML externally and restart."
		} else if _, err := validatedOwners(ownerMappings(cfg.Owners)); err != nil {
			s.ownersReason = "Ownership exceeds live editor limits; edit YAML externally and restart."
		} else {
			s.ownersEditable = true
		}
	}
	initRuntimeBudget(&s, cfg, data, reason)
	initRuntimeOperations(&s, cfg, data, reason)
	r.state.Store(&s)
	return r
}

func runtimeTarget(cfg Config) (string, []byte, string) {
	file := cfg.Source.FilePath
	if cfg.ReadOnly {
		return file, nil, "This instance is read-only."
	}
	if file == "" {
		return file, nil, "Load a YAML configuration file and restart Geko to enable persistent edits."
	}
	info, err := os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0222 == 0 {
		return file, nil, "The configuration must be a writable regular file, not a symlink."
	}
	if info.Size() > maxRuntimeConfigBytes {
		return file, nil, "Live editing supports configuration files up to 1 MiB; edit this file externally and restart."
	}
	resolved, err := filepath.EvalSymlinks(file)
	if err != nil {
		return file, nil, "The configuration path could not be resolved."
	}
	data, _, err := runtimeFile(resolved)
	if err != nil || revision(data) != cfg.Source.FileRevision {
		return resolved, nil, "The configuration changed during startup; restart Geko before editing."
	}
	return resolved, data, ""
}

func (r *Runtime) Snapshot() RuntimeState { return r.state.Load().RuntimeState }

// Update retains the existing trace-link contract. Both live editors share
// one revision, file lock and immutable publication boundary.
func (r *Runtime) Update(ctx context.Context, expected, link, name string) (RuntimeState, error) {
	s, err := r.updateDocument(ctx, expected, liveTraceLinks, func(s *runtimeSnapshot, doc *yaml.Node) error {
		if s.URLSource.Kind == "env" && link != s.URL || s.NameSource.Kind == "env" && name != s.Name {
			return ErrSettingsLocked
		}
		cleanLink, cleanName, err := traceLink(link, name)
		if err != nil {
			return ErrSettingsInvalid
		}
		if _, err := runtimeDocumentNode(doc); err != nil {
			return ErrSettingsConflict
		}
		if s.URLSource.Kind != "env" {
			setRuntimeScalar(doc.Content[0], "trace_link_url", cleanLink)
			s.URLSource = SettingSource{Kind: "yaml"}
		}
		if s.NameSource.Kind != "env" {
			setRuntimeScalar(doc.Content[0], "trace_link_name", cleanName)
			s.NameSource = SettingSource{Kind: "yaml"}
		}
		s.URL, s.Name = cleanLink, cleanName
		return nil
	})
	return s.RuntimeState, err
}

func (r *Runtime) updateDocument(ctx context.Context, expected string, group liveGroup, change func(*runtimeSnapshot, *yaml.Node) error) (runtimeSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	old := *r.state.Load()
	s := old
	editable := s.Editable
	switch group {
	case liveOwners:
		editable = s.ownersEditable
	case liveBudget:
		editable = s.budgetEditable
	case liveOperations:
		editable = s.opsEditable
	}
	if !editable {
		return old, ErrSettingsLocked
	}
	if expected != s.Revision {
		return old, ErrSettingsConflict
	}
	if err := ctx.Err(); err != nil {
		return old, err
	}
	lock, err := os.OpenFile(s.ConfigFile+".lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return old, ErrSettingsSave
	}
	defer func() { _ = lock.Close(); _ = os.Remove(s.ConfigFile + ".lock") }()
	data, mode, err := runtimeFile(s.ConfigFile)
	if err != nil {
		return old, ErrSettingsSave
	}
	if revision(data) != s.Revision {
		return old, ErrSettingsConflict
	}
	doc, err := runtimeRootDocument(data)
	if err != nil {
		return old, ErrSettingsConflict
	}
	if err := change(&s, doc); err != nil {
		return old, err
	}
	var encoded bytes.Buffer
	encoder := yaml.NewEncoder(&encoded)
	encoder.SetIndent(2)
	if err := encoder.Encode(doc); err != nil {
		return old, ErrSettingsSave
	}
	if err := encoder.Close(); err != nil {
		return old, ErrSettingsSave
	}
	if encoded.Len() > maxRuntimeConfigBytes {
		return old, ErrSettingsSave
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.ConfigFile), ".geko-config-*")
	if err != nil {
		return old, ErrSettingsSave
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
		return old, ErrSettingsSave
	}
	if err := ctx.Err(); err != nil {
		return old, err
	}
	// Check again immediately before replacement. The lock protects other
	// Geko processes; an external editor still needs operational coordination.
	latest, _, err := runtimeFile(s.ConfigFile)
	if err != nil {
		return old, ErrSettingsSave
	}
	if revision(latest) != s.Revision {
		return old, ErrSettingsConflict
	}
	warning, err := r.replace(tmp.Name(), s.ConfigFile)
	if err != nil {
		return old, ErrSettingsSave
	}
	s.Revision = revision(encoded.Bytes())
	s.AppliedAt, s.Warning = time.Now().UTC(), warning
	if group == liveOwners {
		s.ownersAppliedAt = s.AppliedAt
	}
	if group == liveBudget {
		s.budgetAppliedAt = s.AppliedAt
	}
	if group == liveOperations {
		s.opsAppliedAt = s.AppliedAt
	}
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

func runtimeRootDocument(data []byte) (*yaml.Node, error) {
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
		if root.Content[i].Value == "<<" {
			return nil, ErrSettingsInvalid
		}
	}
	return &doc, nil
}

func runtimeDocument(data []byte) (*yaml.Node, error) {
	doc, err := runtimeRootDocument(data)
	if err != nil {
		return nil, err
	}
	return runtimeDocumentNode(doc)
}

func runtimeDocumentNode(doc *yaml.Node) (*yaml.Node, error) {
	root := doc.Content[0]
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
	return doc, nil
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
