package access

import (
	"context"
	"fmt"
	"net/http"
)

// Role is what a signed-in caller may do. Each role includes the ones before it.
type Role int

const (
	// RoleViewer reads the dashboard, the query API and the MCP endpoint.
	RoleViewer Role = iota + 1
	// RoleTriager also resolves, mutes and reopens error groups.
	RoleTriager
	// RoleAdmin also restores traces, maps applications and tests notifications.
	RoleAdmin
)

func (r Role) String() string {
	switch r {
	case RoleViewer:
		return "viewer"
	case RoleTriager:
		return "triager"
	case RoleAdmin:
		return "admin"
	}
	return ""
}

// ParseRole reads viewer, triager or admin.
func ParseRole(value string) (Role, error) {
	for _, role := range []Role{RoleViewer, RoleTriager, RoleAdmin} {
		if value == role.String() {
			return role, nil
		}
	}
	return 0, fmt.Errorf("role %q must be viewer, triager or admin", value)
}

// Principal identifies who made a request, for display and the audit log. It
// never holds a secret.
type Principal struct {
	// Name is the token name, the proxy user, or "access" for the access token.
	Name string
	Role Role
	// Via is token, session or proxy.
	Via string
}

// Local is the caller when no token is configured: anyone who reaches the
// loopback listener.
var Local = Principal{Name: "local", Role: RoleAdmin, Via: "loopback"}

const accessName = "access"

type principalKey struct{}

// Caller returns the request's principal. There is none without a token, when
// the caller is Local, or on a public page such as sign-in.
func Caller(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalKey{}).(Principal)
	return principal, ok
}

// WithCaller returns a context carrying principal, for work done on a caller's behalf.
func WithCaller(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, principal)
}

func scopeRole(scope Scope) Role {
	switch scope {
	case ScopeRead:
		return RoleViewer
	case ScopeTriage:
		return RoleTriager
	}
	return 0
}

// triageRequest changes an error group's state or note, which triagers may do.
func triageRequest(r *http.Request) bool {
	return r.Method == http.MethodPost && (r.URL.Path == "/groups/state" || r.URL.Path == "/groups/note")
}

// permitted reports whether role may make the request. Ingest is checked separately.
func permitted(role Role, r *http.Request) bool {
	switch role {
	case RoleAdmin:
		return true
	case RoleTriager:
		return readRequest(r) || triageRequest(r)
	case RoleViewer:
		return readRequest(r)
	}
	return false
}
