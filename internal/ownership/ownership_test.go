package ownership

import (
	"slices"
	"testing"
)

func TestOwnersPreferTheServiceRule(t *testing.T) {
	owners, err := Parse("service:payments=payments-team, application:Storefront = web-team,,")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ application, service, want string }{
		{"Storefront", "payments", "payments-team"},
		{"Storefront", "checkout", "web-team"},
		{"", "payments", "payments-team"},
		{"Back office", "reporting", ""},
	} {
		if got := owners.For(tc.application, tc.service); got != tc.want {
			t.Errorf("For(%q, %q) = %q, want %q", tc.application, tc.service, got, tc.want)
		}
	}
	if rules := owners.Rules(); !slices.Equal(rules, []Rule{{"service", "payments", "payments-team"}, {"application", "Storefront", "web-team"}}) {
		t.Fatalf("rules = %v", rules)
	}
	if empty, err := Parse(""); err != nil || empty.For("a", "b") != "" || len(empty.Rules()) != 0 {
		t.Fatalf("empty owners = %v, %v", empty, err)
	}
}

func TestOwnersRejectMalformedRules(t *testing.T) {
	for _, spec := range []string{"payments=team", "team:payments=x", "service:payments", "service:=team", "service:payments=", "service:x=a\nb"} {
		if _, err := Parse(spec); err == nil {
			t.Errorf("Parse(%q) accepted a malformed rule", spec)
		}
	}
}
