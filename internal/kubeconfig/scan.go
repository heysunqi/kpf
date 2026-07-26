// Package kubeconfig discovers and loads kubeconfig files.
package kubeconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"k8s.io/client-go/tools/clientcmd"
)

const maxFileSize = 1 << 20 // 1 MiB

// Entry describes one discovered kubeconfig file.
type Entry struct {
	Path           string   `json:"path"`
	Basename       string   `json:"basename"`
	CurrentContext string   `json:"current_context"`
	Clusters       []string `json:"clusters"`
	Contexts       []string `json:"contexts"`
	Users          []string `json:"users"`
	Size           int64    `json:"size"`
}

// Scan returns kubeconfig-shaped files found in dir (non-recursive).
// Hidden files, *.lock, and files > maxFileSize are skipped.
// Files that fail to parse as kubeconfig are silently skipped.
func Scan(dir string) ([]Entry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read dir %s: %w", dir, err)
	}

	var out []Entry
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if !isKubeconfigName(name) {
			continue
		}
		if strings.HasSuffix(name, ".lock") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.Size() > maxFileSize {
			continue
		}
		path := filepath.Join(dir, name)
		entry, err := parseFile(path)
		if err != nil {
			continue
		}
		entry.Size = info.Size()
		entry.Basename = name
		out = append(out, entry)
	}

	sort.Slice(out, func(i, j int) bool {
		return filepath.Base(out[i].Path) < filepath.Base(out[j].Path)
	})
	return out, nil
}

// ScanDirs scans each directory and merges results, deduplicated by path.
func ScanDirs(dirs []string) ([]Entry, error) {
	var all []Entry
	seen := make(map[string]bool)
	for _, d := range dirs {
		es, err := Scan(d)
		if err != nil {
			return nil, err
		}
		for _, e := range es {
			if !seen[e.Path] {
				seen[e.Path] = true
				all = append(all, e)
			}
		}
	}
	return all, nil
}

func isKubeconfigName(name string) bool {
	if name == "config" || name == "kubeconfig" {
		return true
	}
	lower := strings.ToLower(name)
	for _, ext := range []string{".yaml", ".yml", ".config", ".kubeconfig"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

func parseFile(path string) (Entry, error) {
	apiConfig, err := clientcmd.LoadFromFile(path)
	if err != nil {
		return Entry{}, err
	}
	if len(apiConfig.Clusters) == 0 {
		return Entry{}, fmt.Errorf("no clusters")
	}
	e := Entry{
		Path:           path,
		CurrentContext: apiConfig.CurrentContext,
	}
	for name := range apiConfig.Clusters {
		e.Clusters = append(e.Clusters, name)
	}
	for name := range apiConfig.Contexts {
		e.Contexts = append(e.Contexts, name)
	}
	for name := range apiConfig.AuthInfos {
		e.Users = append(e.Users, name)
	}
	sort.Strings(e.Clusters)
	sort.Strings(e.Contexts)
	sort.Strings(e.Users)
	return e, nil
}

// DefaultDirs returns the directories scanned for kubeconfigs by default.
// Honors $KPF_KUBECONFIG_DIR (single override); otherwise ~/.kube + cwd.
func DefaultDirs() []string {
	if env := os.Getenv("KPF_KUBECONFIG_DIR"); env != "" {
		return []string{env}
	}
	var dirs []string
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".kube"))
	}
	dirs = append(dirs, ".")
	return dirs
}