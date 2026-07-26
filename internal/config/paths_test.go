package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultPaths(t *testing.T) {
	t.Setenv("KPF_HOME", "")
	t.Setenv("HOME", t.TempDir())

	p, err := DefaultPaths()
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}
	want := filepath.Join(p.Home, "kpf.sock")
	if p.Socket != want {
		t.Errorf("Socket = %q, want %q", p.Socket, want)
	}
	if _, err := os.Stat(p.Home); err != nil {
		t.Errorf("home not created: %v", err)
	}
}

func TestDefaultPaths_Override(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("KPF_HOME", tmp)

	p, err := DefaultPaths()
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}
	if p.Home != tmp {
		t.Errorf("Home = %q, want %q", p.Home, tmp)
	}
	if p.Socket != filepath.Join(tmp, "kpf.sock") {
		t.Errorf("Socket = %q", p.Socket)
	}
}

func TestKubeconfigDirOverride(t *testing.T) {
	t.Setenv("KPF_KUBECONFIG_DIR", "/tmp/custom")
	if got := KubeconfigDirOverride(); got != "/tmp/custom" {
		t.Errorf("got %q", got)
	}
	t.Setenv("KPF_KUBECONFIG_DIR", "")
	if got := KubeconfigDirOverride(); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
