// Package config resolves filesystem paths used by kpf.
package config

import (
	"os"
	"path/filepath"
)

// Paths groups all on-disk locations the daemon and TUI read/write.
type Paths struct {
	Home       string
	Socket     string
	StateFile  string
	DaemonFile string
	LogFile    string
}

// DefaultPaths returns the standard XDG-style paths, creating the home
// directory if it does not yet exist. Honors $KPF_HOME for overrides.
func DefaultPaths() (Paths, error) {
	home := os.Getenv("KPF_HOME")
	if home == "" {
		if uhome, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(uhome, ".local", "share", "kpf")
		} else {
			home = filepath.Join(os.TempDir(), "kpf")
		}
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return Paths{}, err
	}
	return Paths{
		Home:       home,
		Socket:     envOr("KPF_SOCKET", filepath.Join(home, "kpf.sock")),
		StateFile:  filepath.Join(home, "state.json"),
		DaemonFile: filepath.Join(home, "daemon.json"),
		LogFile:    filepath.Join(home, "daemon.log"),
	}, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// KubeconfigDirOverride returns the value of $KPF_KUBECONFIG_DIR if set.
// Empty string means "use defaults".
func KubeconfigDirOverride() string {
	return os.Getenv("KPF_KUBECONFIG_DIR")
}
