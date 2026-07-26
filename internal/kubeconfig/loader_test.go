package kubeconfig

import (
	"testing"
)

func TestLoad_GoodConfig(t *testing.T) {
	cfg, err := Load("testdata/good.config", "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Host != "https://prod.example.com:6443" {
		t.Errorf("Host = %q", cfg.Host)
	}
	if cfg.BearerToken != "fake-token-1" {
		t.Errorf("BearerToken = %q", cfg.BearerToken)
	}
}

func TestLoad_NamedContext(t *testing.T) {
	// good.config has only one context ("prod"); passing a non-existent one
	// should error rather than silently fall back.
	_, err := Load("testdata/good.config", "staging")
	if err == nil {
		t.Error("expected error for unknown context")
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := Load("/no/such/path/config", "")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestDefaultContext(t *testing.T) {
	ctx, err := DefaultContext("testdata/good.config")
	if err != nil {
		t.Fatalf("DefaultContext: %v", err)
	}
	if ctx != "prod" {
		t.Errorf("DefaultContext = %q, want prod", ctx)
	}
}
