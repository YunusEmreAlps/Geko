// Package privacy filters telemetry before it reaches persistent storage.
package privacy

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ExceptionMessageHash fingerprints the exact message bytes used for strict search.
func ExceptionMessageHash(message string) string {
	digest := sha256.Sum256([]byte(message))
	return hex.EncodeToString(digest[:])
}

// Drop the whole field when it resembles a secret or payload. This also drops
// multiline header values and credentials with quoting or embedded spaces.
var sensitiveText = regexp.MustCompile(`(?i)\b(?:authorization|proxy-authorization|cookie|set-cookie|password|passwd|api[_-]?key|(?:access[_-]?|refresh[_-]?)?token|(?:client[_-]?)?secret|(?:request|response)[_. -]?body)\b["']?\s*[:=]|\bbearer\s+\S+|\bbasic\s+[a-z0-9+/]{4,}={0,2}(?:\s|$)|://[^/\s]+@|://[^\s]*[?#]|(?:^|[\s"'(])/[^\s]*[?#]|-----BEGIN [A-Z ]*PRIVATE KEY-----`)

// Text applies a best-effort filter to an untrusted, otherwise allowed value:
// it drops values with credential markers and masks recognizable personal data.
// An arbitrary secret may have no recognizable marker.
func Text(value string, maxBytes int) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.Map(func(r rune) rune {
		if r == '\r' {
			return '\n'
		}
		if unicode.IsControl(r) && r != '\n' && r != '\t' || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, value)
	if sensitiveText.MatchString(value) {
		return "[redacted]"
	}
	// Mask before truncating, so a cut can't leave part of a number behind.
	value = maskPII(value)
	if len(value) <= maxBytes {
		return value
	}
	for !utf8.RuneStart(value[maxBytes]) {
		maxBytes--
	}
	return value[:maxBytes]
}
