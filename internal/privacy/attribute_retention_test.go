package privacy

import "testing"

func TestAttributeRetentionRejectsSensitiveAndWildcardKeys(t *testing.T) {
	for _, key := range []string{"http.*", "authorization", "request.body", "db.query.text", "geko.run.id", "user.password", "auth.token"} {
		if err := (AttributeRetention{Span: []string{key}}).Validate(); err == nil {
			t.Errorf("accepted unsafe key %q", key)
		}
	}
	p := AttributeRetention{Span: []string{"order.id"}, Event: []string{"retry.reason"}}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	clone := p.Clone()
	clone.Span[0] = "changed"
	if p.Span[0] != "order.id" {
		t.Fatal("policy alias mutated")
	}
}
