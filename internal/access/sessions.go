package access

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	sessionCookie   = "geko_session"
	sessionLifetime = 7 * 24 * time.Hour
	maxSessions     = 1024
)

// sessions holds browser sign-ins in memory, so signing out revokes a session
// at once and a restart signs every browser out.
type sessions struct {
	mu    sync.Mutex
	now   func() time.Time
	items map[string]session
}

type session struct {
	full    bool
	expires time.Time
}

func newSessions() *sessions {
	return &sessions{now: time.Now, items: make(map[string]session)}
}

func (s *sessions) create(full bool) string {
	var raw [32]byte
	_, _ = rand.Read(raw[:])
	id := base64.RawURLEncoding.EncodeToString(raw[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if len(s.items) >= maxSessions {
		var oldest string
		for key, value := range s.items {
			if !value.expires.After(now) {
				delete(s.items, key)
			} else if oldest == "" || value.expires.Before(s.items[oldest].expires) {
				oldest = key
			}
		}
		if len(s.items) >= maxSessions {
			delete(s.items, oldest)
		}
	}
	s.items[id] = session{full: full, expires: now.Add(sessionLifetime)}
	return id
}

func (s *sessions) lookup(id string) (session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.items[id]
	if ok && !value.expires.After(s.now()) {
		delete(s.items, id)
		return session{}, false
	}
	return value, ok
}

func (s *sessions) clear() {
	s.mu.Lock()
	clear(s.items)
	s.mu.Unlock()
}

func (s *sessions) delete(id string) {
	s.mu.Lock()
	delete(s.items, id)
	s.mu.Unlock()
}

// SignIn starts a browser session for the access token or a read token and
// reports whether the secret was accepted.
func (t Token) SignIn(w http.ResponseWriter, r *http.Request, secret string) bool {
	if t.state == nil {
		return false
	}
	full := t.matches(secret)
	if !full && !t.allows(secret, ScopeRead) {
		t.state.failures.record(r)
		return false
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: t.state.sessions.create(full), Path: "/",
		MaxAge: int(sessionLifetime / time.Second), HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secureRequest(r),
	})
	return true
}

// SignOut ends the request's browser session, if any.
func (t Token) SignOut(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil && t.state != nil {
		t.state.sessions.delete(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secureRequest(r),
	})
}

// secureRequest marks cookies Secure behind a TLS proxy too. The header only
// adds a restriction, so a forged value can't weaken the cookie.
func secureRequest(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// SessionAuthenticated reports whether a browser session authenticated the request.
func SessionAuthenticated(r *http.Request) bool {
	value, _ := r.Context().Value(sessionKey{}).(bool)
	return value
}

type sessionKey struct{}
