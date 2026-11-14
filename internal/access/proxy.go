package access

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ProxyAuth trusts a user name that an authenticating reverse proxy, such as
// oauth2-proxy, sets in a header. Only requests from the trusted addresses can
// use it; the proxy must also replace any copy of the header a client sends.
type ProxyAuth struct {
	Header  string
	Trusted []netip.Prefix
	// Roles assigns roles to users by name, compared without case.
	Roles map[string]Role
	// Default is the role of unlisted users; zero refuses them.
	Default Role
}

// ParseProxyRoles reads comma-separated user=role pairs.
func ParseProxyRoles(spec string) (map[string]Role, error) {
	roles := make(map[string]Role)
	for entry := range strings.SplitSeq(spec, ",") {
		if entry = strings.TrimSpace(entry); entry == "" {
			continue
		}
		user, value, ok := strings.Cut(entry, "=")
		user = strings.ToLower(strings.TrimSpace(user))
		if !ok || !validUser(user) {
			return nil, errors.New("GEKO_PROXY_ROLES must be a comma-separated list of user=role pairs")
		}
		role, err := ParseRole(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("GEKO_PROXY_ROLES: %w", err)
		}
		if _, exists := roles[user]; exists {
			return nil, fmt.Errorf("GEKO_PROXY_ROLES names %q more than once", user)
		}
		roles[user] = role
	}
	return roles, nil
}

// ParsePrefixes reads comma-separated IP addresses and CIDR prefixes.
func ParsePrefixes(spec string) ([]netip.Prefix, error) {
	var prefixes []netip.Prefix
	for entry := range strings.SplitSeq(spec, ",") {
		if entry = strings.TrimSpace(entry); entry == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(entry)
		if err != nil {
			address, addrErr := netip.ParseAddr(entry)
			if addrErr != nil || address.Zone() != "" {
				return nil, fmt.Errorf("%q is not an IP address or CIDR prefix", entry)
			}
			prefix = netip.PrefixFrom(address, address.BitLen())
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return prefixes, nil
}

// WithProxy accepts users from an authenticating proxy in addition to tokens.
// Call it before serving; reloading tokens keeps the setting.
func (t Token) WithProxy(proxy ProxyAuth) (Token, error) {
	if t.state == nil {
		return Token{}, errors.New("GEKO_PROXY_USER_HEADER requires an access token")
	}
	if proxy.Header == "" || strings.ContainsFunc(proxy.Header, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_')
	}) {
		return Token{}, errors.New("GEKO_PROXY_USER_HEADER must be a header name such as X-Auth-Request-Email")
	}
	if strings.EqualFold(proxy.Header, "Authorization") || strings.EqualFold(proxy.Header, "Cookie") {
		return Token{}, errors.New("GEKO_PROXY_USER_HEADER can't be Authorization or Cookie")
	}
	if len(proxy.Trusted) == 0 {
		return Token{}, errors.New("GEKO_PROXY_USER_HEADER requires GEKO_PROXY_TRUSTED_IPS, the addresses of the proxy")
	}
	proxy.Header = http.CanonicalHeaderKey(proxy.Header)
	t.state.proxy = &proxy
	return t, nil
}

// Proxy returns the proxy setting, if any.
func (t Token) Proxy() *ProxyAuth {
	if t.state == nil {
		return nil
	}
	return t.state.proxy
}

// identify returns the user the proxy named. present is false when the request
// carries no header from a trusted address, so tokens apply instead.
func (p *ProxyAuth) identify(r *http.Request) (principal Principal, present bool, err error) {
	values := r.Header.Values(p.Header)
	if len(values) == 0 || !p.trusts(r.RemoteAddr) {
		return Principal{}, false, nil
	}
	user := strings.TrimSpace(values[0])
	if len(values) != 1 || !validUser(user) {
		return Principal{}, true, errors.New("invalid proxy user header")
	}
	role, listed := p.Roles[strings.ToLower(user)]
	if !listed {
		role = p.Default
	}
	if role == 0 {
		return Principal{}, true, errors.New("user has no Geko role")
	}
	return Principal{Name: user, Role: role, Via: "proxy"}, true, nil
}

func (p *ProxyAuth) trusts(remote string) bool {
	address, err := netip.ParseAddrPort(remote)
	if err != nil {
		return false
	}
	for _, prefix := range p.Trusted {
		if prefix.Contains(address.Addr().Unmap()) {
			return true
		}
	}
	return false
}

func validUser(user string) bool {
	return user != "" && len(user) <= 256 && utf8.ValidString(user) && !strings.ContainsFunc(user, func(r rune) bool {
		return unicode.IsControl(r) || r == ',' || r == '='
	})
}
