package access

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
)

const minDistinctTokenChars = 10

// Scope limits what a named token may do. The access token has every scope.
type Scope int

const (
	// ScopeIngest only exports traces.
	ScopeIngest Scope = iota + 1
	// ScopeRead reads the dashboard, the query API and the MCP endpoint.
	ScopeRead
	// ScopeTriage also changes error group state.
	ScopeTriage
)

// Token protects the listeners. The access token grants full access; scoped
// tokens let applications export, and agents and people read or triage,
// without the access token.
// Copies share state, so Reload changes the credentials every copy checks.
type Token struct {
	state *tokenState
}

type tokenState struct {
	credentials atomic.Pointer[credentials]
	sessions    *sessions
	failures    *failureLog
	proxy       *ProxyAuth
}

type credentials struct {
	digest [sha256.Size]byte
	scoped []scopedToken
}

type scopedToken struct {
	name   string
	scope  Scope
	digest [sha256.Size]byte
}

type pair struct{ name, value string }

func newToken(c *credentials) Token {
	state := &tokenState{sessions: newSessions(), failures: newFailureLog()}
	state.credentials.Store(c)
	return Token{state: state}
}

func (t Token) current() *credentials {
	if t.state == nil {
		return nil
	}
	return t.state.credentials.Load()
}

func ParseToken(value string) (Token, error) {
	return parseAccess(value, "GEKO_ACCESS_TOKEN")
}

func parseAccess(value, label string) (Token, error) {
	if value == "" {
		return Token{}, nil
	}
	if err := validate(value, label); err != nil {
		return Token{}, err
	}
	return newToken(&credentials{digest: sha256.Sum256([]byte(value))}), nil
}

func validate(value, variable string) error {
	if len(value) < 32 || len(value) > 256 || strings.ContainsFunc(value, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_')
	}) {
		return fmt.Errorf("%s must contain 32 to 256 letters, digits, hyphens or underscores", variable)
	}
	// Reject repeated or patterned values. A random hex or base64url token of
	// 32 or more characters has far more than this many distinct characters.
	distinct := make(map[rune]struct{})
	for _, r := range value {
		distinct[r] = struct{}{}
	}
	if len(distinct) < minDistinctTokenChars {
		return fmt.Errorf("%s is too predictable; generate one with openssl rand -hex 32", variable)
	}
	return nil
}

// WithIngestTokens adds export-only tokens from comma-separated name=token pairs.
func (t Token) WithIngestTokens(spec string) (Token, error) {
	pairs, err := parseSpec(spec, "GEKO_INGEST_TOKENS")
	if err != nil {
		return Token{}, err
	}
	return t.withScoped(pairs, "GEKO_INGEST_TOKENS", ScopeIngest)
}

// WithReadTokens adds read-only tokens from comma-separated name=token pairs.
func (t Token) WithReadTokens(spec string) (Token, error) {
	pairs, err := parseSpec(spec, "GEKO_READ_TOKENS")
	if err != nil {
		return Token{}, err
	}
	return t.withScoped(pairs, "GEKO_READ_TOKENS", ScopeRead)
}

// WithTriageTokens adds tokens that read and resolve, mute or reopen error
// groups, from comma-separated name=token pairs.
func (t Token) WithTriageTokens(spec string) (Token, error) {
	pairs, err := parseSpec(spec, "GEKO_TRIAGE_TOKENS")
	if err != nil {
		return Token{}, err
	}
	return t.withScoped(pairs, "GEKO_TRIAGE_TOKENS", ScopeTriage)
}

func parseSpec(spec, variable string) ([]pair, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}
	var pairs []pair
	for entry := range strings.SplitSeq(spec, ",") {
		name, value, ok := strings.Cut(strings.TrimSpace(entry), "=")
		if !ok {
			return nil, fmt.Errorf("%s must be a comma-separated list of name=token pairs", variable)
		}
		pairs = append(pairs, pair{name, value})
	}
	return pairs, nil
}

func (t Token) withScoped(pairs []pair, label string, scope Scope) (Token, error) {
	if len(pairs) == 0 {
		return t, nil
	}
	c := t.current()
	if c == nil {
		return Token{}, fmt.Errorf("%s requires an access token", label)
	}
	seen := map[[sha256.Size]byte]bool{c.digest: true}
	// Read and triage token names identify callers, so they share one namespace.
	sameNames := func(other Scope) bool {
		return other == scope || scopeRole(other) != 0 && scopeRole(scope) != 0
	}
	names := make(map[string]bool)
	for _, token := range c.scoped {
		seen[token.digest] = true
		if sameNames(token.scope) {
			names[token.name] = true
		}
	}
	next := &credentials{digest: c.digest, scoped: append([]scopedToken(nil), c.scoped...)}
	for _, entry := range pairs {
		if !validName(entry.name) {
			return Token{}, fmt.Errorf("%s: token names use 1 to 64 letters, digits, dots, hyphens or underscores", label)
		}
		if names[entry.name] {
			return Token{}, fmt.Errorf("%s names %q more than once, or a read or triage token has that name", label, entry.name)
		}
		if err := validate(entry.value, label+" token "+entry.name); err != nil {
			return Token{}, err
		}
		digest := sha256.Sum256([]byte(entry.value))
		if seen[digest] {
			return Token{}, fmt.Errorf("%s token %s must differ from every other configured token", label, entry.name)
		}
		seen[digest], names[entry.name] = true, true
		next.scoped = append(next.scoped, scopedToken{name: entry.name, scope: scope, digest: digest})
	}
	token := newToken(next)
	token.state.proxy = t.state.proxy
	return token, nil
}

func validName(name string) bool {
	return len(name) > 0 && len(name) <= 64 && !strings.ContainsFunc(name, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.')
	})
}

func (t Token) Enabled() bool { return t.state != nil }

// Names lists the token names with a scope, never their values.
func (t Token) Names(scope Scope) []string {
	var names []string
	if c := t.current(); c != nil {
		for _, token := range c.scoped {
			if token.scope == scope {
				names = append(names, token.name)
			}
		}
	}
	return names
}

// Reload replaces the credentials of t and every copy of it. Protection can't
// be turned on or off at runtime. Changed credentials sign out every browser,
// so a rotated token also ends sessions started with the old one.
func (t Token) Reload(next Token) error {
	_, err := t.reload(next)
	return err
}

func (t Token) reload(next Token) (changed bool, err error) {
	if t.state == nil || next.state == nil {
		return false, errors.New("an access token is required before and after a reload")
	}
	replacement := next.current()
	previous := t.state.credentials.Swap(replacement)
	if !slices.Equal(previous.scoped, replacement.scoped) || previous.digest != replacement.digest {
		t.state.sessions.clear()
		return true, nil
	}
	return false, nil
}

func (t Token) matches(value string) bool {
	c := t.current()
	if c == nil {
		return false
	}
	digest := sha256.Sum256([]byte(value))
	return subtle.ConstantTimeCompare(digest[:], c.digest[:]) == 1
}

// identify returns the principal of the access token or a read or triage
// token. Every configured digest is compared to keep timing independent of the match.
func (t Token) identify(value string) (Principal, bool) {
	c := t.current()
	if c == nil {
		return Principal{}, false
	}
	digest := sha256.Sum256([]byte(value))
	var principal Principal
	if subtle.ConstantTimeCompare(digest[:], c.digest[:]) == 1 {
		principal = Principal{Name: accessName, Role: RoleAdmin, Via: "token"}
	}
	for _, token := range c.scoped {
		if subtle.ConstantTimeCompare(digest[:], token.digest[:]) == 1 && scopeRole(token.scope) != 0 {
			principal = Principal{Name: token.name, Role: scopeRole(token.scope), Via: "token"}
		}
	}
	return principal, principal.Role != 0
}

// allows reports whether value is the access token or a token with the scope.
// Every configured digest is compared to keep timing independent of the match.
func (t Token) allows(value string, scope Scope) bool {
	c := t.current()
	if c == nil {
		return false
	}
	digest := sha256.Sum256([]byte(value))
	match := subtle.ConstantTimeCompare(digest[:], c.digest[:])
	for _, token := range c.scoped {
		if subtle.ConstantTimeCompare(digest[:], token.digest[:]) == 1 && token.scope == scope {
			match = 1
		}
	}
	return match == 1
}

func bearerValue(header string) (string, bool) {
	scheme, value, ok := strings.Cut(header, " ")
	return value, ok && strings.EqualFold(scheme, "Bearer")
}

// Bearer reports whether the header carries the access token.
func (t Token) Bearer(header string) bool {
	value, ok := bearerValue(header)
	return ok && t.matches(value)
}

// IngestBearer reports whether the header may export traces.
func (t Token) IngestBearer(header string) bool {
	value, ok := bearerValue(header)
	return ok && t.allows(value, ScopeIngest)
}

type authenticatedKey struct{}

type publicKey struct{}

// Public reports whether the request reached a page that needs no sign-in,
// such as the sign-in page and its assets, past token protection.
func Public(ctx context.Context) bool {
	public, _ := ctx.Value(publicKey{}).(bool)
	return public
}

func Authenticated(ctx context.Context) bool {
	authenticated, _ := ctx.Value(authenticatedKey{}).(bool)
	return authenticated
}

// FullAccess reports whether the request has the admin role: the access token,
// or a proxy user with that role.
func FullAccess(ctx context.Context) bool {
	principal, _ := Caller(ctx)
	return principal.Role == RoleAdmin
}

// readRequest reports whether a read-only token may make the request. The MCP
// endpoint only exposes read-only tools.
func readRequest(r *http.Request) bool {
	return r.Method == http.MethodGet || r.Method == http.MethodHead || r.URL.Path == "/mcp"
}

// publicRequest needs no credentials: probes, signing in and out, and the
// static assets the sign-in page uses.
func publicRequest(r *http.Request) bool {
	get := r.Method == http.MethodGet || r.Method == http.MethodHead
	switch {
	case get && (r.URL.Path == "/healthz" || r.URL.Path == "/readyz"):
		return true
	case r.URL.Path == "/login" && (get || r.Method == http.MethodPost):
		return true
	case r.URL.Path == "/logout" && r.Method == http.MethodPost:
		return true
	}
	return get && strings.HasPrefix(r.URL.Path, "/assets/")
}

func (t Token) Protect(next http.Handler) http.Handler {
	if t.state == nil {
		return next
	}
	protected := http.NewCrossOriginProtection().Handler(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if publicRequest(r) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), publicKey{}, true)))
			return
		}
		headers := r.Header.Values("Authorization")
		bearerOnly := r.URL.Path == "/v1/traces"
		admit := func(principal Principal) {
			ctx := context.WithValue(r.Context(), authenticatedKey{}, true)
			protected.ServeHTTP(w, r.WithContext(WithCaller(ctx, principal)))
		}
		serve := func(value string) bool {
			if bearerOnly {
				if principal, ok := t.identify(value); ok && principal.Role == RoleAdmin {
					admit(principal)
					return true
				}
				if t.allows(value, ScopeIngest) {
					ctx := context.WithValue(r.Context(), authenticatedKey{}, true)
					protected.ServeHTTP(w, r.WithContext(ctx))
					return true
				}
				return false
			}
			principal, ok := t.identify(value)
			if !ok || !permitted(principal.Role, r) {
				return false
			}
			admit(principal)
			return true
		}
		if proxy := t.state.proxy; proxy != nil && !bearerOnly {
			principal, present, err := proxy.identify(r)
			if err != nil {
				t.state.failures.record(r)
				http.Error(w, "The sign-in proxy's user has no access to Geko", http.StatusForbidden)
				return
			}
			if present {
				if !permitted(principal.Role, r) {
					http.Error(w, "Your Geko role doesn't allow this action", http.StatusForbidden)
					return
				}
				admit(principal)
				return
			}
		}
		if cookie, err := r.Cookie(sessionCookie); err == nil && len(headers) == 0 && !bearerOnly {
			if current, ok := t.state.sessions.lookup(cookie.Value); ok {
				if !permitted(current.principal.Role, r) {
					http.Error(w, "Your Geko role doesn't allow this action", http.StatusForbidden)
					return
				}
				principal := current.principal
				principal.Via = "session"
				ctx := context.WithValue(r.Context(), sessionKey{}, true)
				r = r.WithContext(ctx)
				admit(principal)
				return
			}
		}
		if len(headers) == 1 {
			if value, ok := bearerValue(headers[0]); ok && serve(value) {
				return
			}
			if !bearerOnly {
				if user, password, ok := r.BasicAuth(); ok && user == "geko" && serve(password) {
					return
				}
			}
		}
		// A request without credentials is a normal browser sign-in challenge.
		if len(headers) > 0 {
			t.state.failures.record(r)
		}
		if len(headers) == 0 && !bearerOnly && r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html") {
			http.Redirect(w, r, "/login?"+url.Values{"next": {r.URL.RequestURI()}}.Encode(), http.StatusSeeOther)
			return
		}
		// No Basic challenge: browsers would show their own sign-in dialog,
		// which can't sign out. Scripts can still send Basic credentials.
		w.Header().Set("WWW-Authenticate", `Bearer realm="Geko"`)
		http.Error(w, "Authentication required", http.StatusUnauthorized)
	})
}
