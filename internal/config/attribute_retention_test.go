package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAttributeRetentionYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "geko.yaml")
	if err := os.WriteFile(path, []byte("attribute_retention:\n  span: [order.id]\n  event: [retry.reason]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadFile(path, false)
	if err != nil || len(cfg.AttributeRetention.Span) != 1 || cfg.AttributeRetention.Span[0] != "order.id" {
		t.Fatalf("policy = %+v, %v", cfg.AttributeRetention, err)
	}
	if err := os.WriteFile(path, []byte("attribute_retention:\n  resource: [auth.token]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadFile(path, false); err == nil {
		t.Fatal("sensitive allowlist key accepted")
	}
}
