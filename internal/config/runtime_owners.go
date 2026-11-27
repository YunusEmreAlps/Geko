package config

import (
	"bytes"
	"context"
	"errors"
	"io"
	"time"

	"github.com/YunusEmreAlps/geko/internal/ownership"
	"go.yaml.in/yaml/v3"
)

const MaxLiveOwnerRules = 256
const MaxLiveOwnerYAML = 128 << 10

var ErrOwnersInvalid = errors.New("use services and applications mappings with at most 256 rules; names must be 1–256 bytes and teams 1–128 bytes without control characters; YAML aliases, anchors and merges are not supported")

type OwnerMappings struct {
	Services     map[string]string `json:"services" yaml:"services,omitempty"`
	Applications map[string]string `json:"applications" yaml:"applications,omitempty"`
}

type OwnersState struct {
	Revision  string        `json:"revision"`
	Mappings  OwnerMappings `json:"owners"`
	Source    SettingSource `json:"source"`
	Editable  bool          `json:"editable"`
	Reason    string        `json:"reason,omitempty"`
	AppliedAt time.Time     `json:"applied_at"`
	Warning   string        `json:"warning,omitempty"`
	YAML      string        `json:"-"`
}

// Owners returns an immutable value: its maps are private to ownership.Owners.
func (r *Runtime) Owners() ownership.Owners { return r.state.Load().owners }

func (r *Runtime) OwnersSource() SettingSource { return r.state.Load().ownerSource }

func (r *Runtime) OwnersState() OwnersState { return ownerState(*r.state.Load()) }

func ownerState(s runtimeSnapshot) OwnersState {
	mapping := ownerMappings(s.owners)
	data, _ := yaml.Marshal(mapping)
	return OwnersState{Revision: s.Revision, Mappings: mapping, Source: s.ownerSource, Editable: s.ownersEditable, Reason: s.ownersReason, AppliedAt: s.ownersAppliedAt, Warning: s.Warning, YAML: string(data)}
}

func ownerMappings(owners ownership.Owners) OwnerMappings {
	mapping := OwnerMappings{Services: map[string]string{}, Applications: map[string]string{}}
	for _, rule := range owners.Rules() {
		if rule.Kind == "service" {
			mapping.Services[rule.Name] = rule.Team
		} else {
			mapping.Applications[rule.Name] = rule.Team
		}
	}
	return mapping
}

func ParseOwnerMappings(data []byte) (OwnerMappings, error) {
	var mapping OwnerMappings
	if len(data) > MaxLiveOwnerYAML {
		return mapping, ErrOwnersInvalid
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return mapping, ErrOwnersInvalid
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode || !plainOwnerNode(doc.Content[0]) {
		return mapping, ErrOwnersInvalid
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&mapping); err != nil {
		return mapping, ErrOwnersInvalid
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return mapping, ErrOwnersInvalid
	}
	if _, err := validatedOwners(mapping); err != nil {
		return mapping, err
	}
	return mapping, nil
}

func validatedOwners(mapping OwnerMappings) (ownership.Owners, error) {
	if len(mapping.Services)+len(mapping.Applications) > MaxLiveOwnerRules {
		return ownership.Owners{}, ErrOwnersInvalid
	}
	encoded, err := yaml.Marshal(mapping)
	if err != nil || len(encoded) > MaxLiveOwnerYAML {
		return ownership.Owners{}, ErrOwnersInvalid
	}
	owners, err := ownership.New(mapping.Services, mapping.Applications)
	if err != nil {
		return ownership.Owners{}, ErrOwnersInvalid
	}
	return owners, nil
}

func (r *Runtime) UpdateOwners(ctx context.Context, expected string, mapping OwnerMappings) (OwnersState, error) {
	s, err := r.updateDocument(ctx, expected, liveOwners, func(s *runtimeSnapshot, doc *yaml.Node) error {
		owners, err := validatedOwners(mapping)
		if err != nil {
			return err
		}
		if node := ownerNode(doc); node != nil && !plainOwnerNode(node) {
			return ErrSettingsConflict
		}
		var next yaml.Node
		if err := next.Encode(mapping); err != nil {
			return ErrOwnersInvalid
		}
		mergeOwnerNode(doc.Content[0], "owners", &next)
		s.owners = owners
		s.ownerSource = SettingSource{Kind: "yaml"}
		return nil
	})
	return ownerState(s), err
}

func ownerNode(doc *yaml.Node) *yaml.Node {
	root := doc.Content[0]
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value == "owners" {
			return root.Content[i+1]
		}
	}
	return nil
}

func runtimeOwnerDocument(data []byte) (*yaml.Node, error) {
	doc, err := runtimeRootDocument(data)
	if err != nil {
		return nil, err
	}
	if node := ownerNode(doc); node != nil && !plainOwnerNode(node) {
		return nil, ErrOwnersInvalid
	}
	return doc, nil
}

func plainOwnerNode(node *yaml.Node) bool {
	if node.Kind == yaml.AliasNode || node.Anchor != "" {
		return false
	}
	if node.Kind == yaml.MappingNode {
		for i := 0; i < len(node.Content); i += 2 {
			if node.Content[i].Value == "<<" {
				return false
			}
		}
	}
	for _, child := range node.Content {
		if !plainOwnerNode(child) {
			return false
		}
	}
	return true
}

// Reuse matching mapping/scalar nodes to retain their comments. Removed rules
// are removed; unrelated root keys and comments are never decoded/rebuilt.
func mergeOwnerNode(root *yaml.Node, key string, next *yaml.Node) {
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value != key {
			continue
		}
		old := root.Content[i+1]
		if old.Kind == yaml.MappingNode && next.Kind == yaml.MappingNode {
			var content []*yaml.Node
			for j := 0; j < len(next.Content); j += 2 {
				name, value := next.Content[j], next.Content[j+1]
				mergeOwnerNode(old, name.Value, value)
				for k := 0; k < len(old.Content); k += 2 {
					if old.Content[k].Value == name.Value {
						content = append(content, old.Content[k], old.Content[k+1])
						break
					}
				}
			}
			old.Content = content
		} else {
			next.HeadComment, next.LineComment, next.FootComment = old.HeadComment, old.LineComment, old.FootComment
			root.Content[i+1] = next
		}
		return
	}
	root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, next)
}
