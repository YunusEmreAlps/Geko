package privacy

import (
	"errors"
	"slices"
	"strings"
)

// AttributeRetention opts into additional exact keys. It never overrides strict
// redaction or protected attribute search. Lists apply at startup, not live.
type AttributeRetention struct {
	Resource []string `yaml:"resource" json:"resource,omitempty"`
	Scope    []string `yaml:"scope" json:"scope,omitempty"`
	Span     []string `yaml:"span" json:"span,omitempty"`
	Event    []string `yaml:"event" json:"event,omitempty"`
	Link     []string `yaml:"link" json:"link,omitempty"`
}

func (p AttributeRetention) Validate() error {
	for _, keys := range [][]string{p.Resource, p.Scope, p.Span, p.Event, p.Link} {
		if len(keys) > 32 {
			return errors.New("attribute_retention allows at most 32 exact keys per location")
		}
		for _, key := range keys {
			if !attributeName.MatchString(key) || strings.HasPrefix(strings.ToLower(key), "geko.") || SensitiveAttribute(key) {
				return errors.New("attribute_retention requires exact non-sensitive keys outside the geko namespace")
			}
		}
	}
	return nil
}

// SensitiveAttribute rejects known payload and credential keys even on opt-in.
func SensitiveAttribute(key string) bool {
	key = strings.ToLower(key)
	for _, part := range []string{"password", "passwd", "secret", "token", "authorization", "cookie", "api_key", "apikey", "api-key", "body", "db.statement", "db.query.text", "url.full", "url.query", "http.request.header", "http.response.header"} {
		if strings.Contains(key, part) {
			return true
		}
	}
	return false
}

func (p AttributeRetention) Clone() AttributeRetention {
	return AttributeRetention{Resource: slices.Clone(p.Resource), Scope: slices.Clone(p.Scope), Span: slices.Clone(p.Span), Event: slices.Clone(p.Event), Link: slices.Clone(p.Link)}
}
