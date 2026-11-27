// Package ownership names the team that owns a failure, from rules that map
// services and applications to teams.
package ownership

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
)

// Owners maps services, then applications, to the teams that own them.
type Owners struct {
	services, applications map[string]string
	source                 func() Owners
}

// Dynamic resolves immutable ownership snapshots at lookup time. The supplier
// must return static owners and be safe for concurrent use.
func Dynamic(source func() Owners) Owners { return Owners{source: source} }

// Rule is one mapping, for display.
type Rule struct {
	Kind, Name, Team string
}

// Parse reads comma-separated service:<name>=<team> and
// application:<name>=<team> pairs, as GEKO_OWNERS holds them.
func Parse(spec string) (Owners, error) {
	services, applications := map[string]string{}, map[string]string{}
	for pair := range strings.SplitSeq(spec, ",") {
		if strings.TrimSpace(pair) == "" {
			continue
		}
		subject, team, ok := strings.Cut(pair, "=")
		kind, name, scoped := strings.Cut(subject, ":")
		if !ok || !scoped {
			return Owners{}, fmt.Errorf("owner %q must look like service:<name>=<team> or application:<name>=<team>", strings.TrimSpace(pair))
		}
		switch strings.TrimSpace(kind) {
		case "service":
			services[strings.TrimSpace(name)] = strings.TrimSpace(team)
		case "application":
			applications[strings.TrimSpace(name)] = strings.TrimSpace(team)
		default:
			return Owners{}, fmt.Errorf("owner %q must start with service or application", strings.TrimSpace(pair))
		}
	}
	return New(services, applications)
}

// New validates owners given as maps, as the YAML configuration holds them.
func New(services, applications map[string]string) (Owners, error) {
	owners := Owners{services: map[string]string{}, applications: map[string]string{}}
	for _, rules := range []struct {
		from map[string]string
		to   map[string]string
	}{{services, owners.services}, {applications, owners.applications}} {
		for name, team := range rules.from {
			if name == "" || len(name) > 256 {
				return Owners{}, errors.New("an owned service or application needs a name of up to 256 bytes")
			}
			if team == "" || len(team) > 128 || strings.ContainsFunc(team, unicode.IsControl) {
				return Owners{}, fmt.Errorf("the owner of %q must be a team name of up to 128 bytes", name)
			}
			rules.to[name] = team
		}
	}
	return owners, nil
}

// For returns the team owning failures recorded by service in application,
// preferring a rule for the service, or "" when no rule matches.
func (o Owners) For(application, service string) string {
	if o.source != nil {
		return o.source().For(application, service)
	}
	if team, ok := o.services[service]; ok {
		return team
	}
	return o.applications[application]
}

// Rules lists every mapping, services first, each sorted by name.
func (o Owners) Rules() []Rule {
	if o.source != nil {
		return o.source().Rules()
	}
	var rules []Rule
	for kind, mapping := range map[string]map[string]string{"service": o.services, "application": o.applications} {
		for name, team := range mapping {
			rules = append(rules, Rule{kind, name, team})
		}
	}
	slices.SortFunc(rules, func(a, b Rule) int {
		return cmp.Or(-cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Name, b.Name))
	})
	return rules
}
