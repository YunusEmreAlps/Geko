package privacy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"slices"
	"strings"
)

const AttributeDigestPrefix = "geko.hmac."

var attributeName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_.-]{0,127}$`)

// AttributeSearch hashes selected span attributes with a server-held key.
type AttributeSearch struct {
	keys   []string
	secret []byte
}

func NewAttributeSearch(names, secret string) (AttributeSearch, error) {
	if names == "" && secret == "" {
		return AttributeSearch{}, nil
	}
	key, err := hex.DecodeString(secret)
	if err != nil || len(key) != sha256.Size {
		return AttributeSearch{}, errors.New("GEKO_ATTRIBUTE_SEARCH_KEY must contain 32 hex-encoded bytes")
	}
	keys := strings.Split(names, ",")
	if len(keys) > 16 {
		return AttributeSearch{}, errors.New("GEKO_SEARCH_ATTRIBUTES accepts at most 16 keys")
	}
	for i, name := range keys {
		name = strings.TrimSpace(name)
		if !attributeName.MatchString(name) || strings.HasPrefix(name, "geko.") {
			return AttributeSearch{}, errors.New("GEKO_SEARCH_ATTRIBUTES must contain comma-separated attribute names outside the geko.* namespace")
		}
		keys[i] = name
	}
	slices.Sort(keys)
	return AttributeSearch{keys: slices.Compact(keys), secret: key}, nil
}

func (p AttributeSearch) Keys() []string { return slices.Clone(p.keys) }
func (p AttributeSearch) Enabled() bool  { return len(p.keys) > 0 }

func (p AttributeSearch) Digest(name, value string) (string, bool) {
	if _, ok := slices.BinarySearch(p.keys, name); !ok || len(value) == 0 || len(value) > 4096 {
		return "", false
	}
	mac := hmac.New(sha256.New, p.secret)
	mac.Write([]byte(name))
	mac.Write([]byte{0})
	mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil)), true
}

func (p AttributeSearch) Protects(name string) bool {
	_, ok := slices.BinarySearch(p.keys, name)
	return ok
}
