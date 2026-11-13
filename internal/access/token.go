package access

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
)

const minDistinctTokenChars = 10

type Token struct {
	digest  [sha256.Size]byte
	enabled bool
}

func ParseToken(value string) (Token, error) {
	if value == "" {
		return Token{}, nil
	}
	if len(value) < 32 || len(value) > 256 || strings.ContainsFunc(value, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_')
	}) {
		return Token{}, errors.New("GEKO_ACCESS_TOKEN must contain 32 to 256 letters, digits, hyphens or underscores")
	}
	// Reject repeated or patterned values. A random hex or base64url token of
	// 32 or more characters has far more than this many distinct characters.
	distinct := make(map[rune]struct{})
	for _, r := range value {
		distinct[r] = struct{}{}
	}
	if len(distinct) < minDistinctTokenChars {
		return Token{}, errors.New("GEKO_ACCESS_TOKEN is too predictable; generate one with openssl rand -hex 32")
	}
	return Token{digest: sha256.Sum256([]byte(value)), enabled: true}, nil
}

func (t Token) Enabled() bool { return t.enabled }

func (t Token) matches(value string) bool {
	digest := sha256.Sum256([]byte(value))
	return t.enabled && subtle.ConstantTimeCompare(digest[:], t.digest[:]) == 1
}

func (t Token) Bearer(header string) bool {
	scheme, value, ok := strings.Cut(header, " ")
	return ok && strings.EqualFold(scheme, "Bearer") && t.matches(value)
}

type authenticatedKey struct{}

func Authenticated(ctx context.Context) bool {
	authenticated, _ := ctx.Value(authenticatedKey{}).(bool)
	return authenticated
}

func (t Token) Protect(next http.Handler) http.Handler {
	if !t.enabled {
		return next
	}
	protected := http.NewCrossOriginProtection().Handler(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers := r.Header.Values("Authorization")
		bearerOnly := r.URL.Path == "/v1/traces"
		if len(headers) == 1 {
			if t.Bearer(headers[0]) {
				protected.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), authenticatedKey{}, true)))
				return
			}
			if !bearerOnly {
				user, password, ok := r.BasicAuth()
				if ok && user == "geko" && t.matches(password) {
					protected.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), authenticatedKey{}, true)))
					return
				}
			}
		}
		if bearerOnly {
			w.Header().Set("WWW-Authenticate", "Bearer")
		} else {
			w.Header().Set("WWW-Authenticate", `Basic realm="Geko", charset="UTF-8"`)
		}
		http.Error(w, "Authentication required", http.StatusUnauthorized)
	})
}
