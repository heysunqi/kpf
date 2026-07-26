package kubeconfig

import (
	"path/filepath"
	"sort"
	"testing"
)

func TestScan_FiltersAndSorts(t *testing.T) {
	dir := "testdata"
	got, err := Scan(dir)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	wantNames := []string{"good.config", "good.yaml"}
	if len(got) != len(wantNames) {
		var names []string
		for _, e := range got {
			names = append(names, e.Basename)
		}
		sort.Strings(names)
		t.Fatalf("got %d entries (%v), want %d (%v)", len(got), names, len(wantNames), wantNames)
	}
	// Verify .hidden, .lock, .txt, empty, bad are filtered.
	for _, e := range got {
		switch e.Basename {
		case ".hidden.config", "foo.config.lock", "notes.txt", "empty.config", "bad.config":
			t.Errorf("did not filter %q", e.Basename)
		}
	}
	// Sorted by basename.
	if got[0].Basename != "good.config" || got[1].Basename != "good.yaml" {
		t.Errorf("not sorted: %s, %s", got[0].Basename, got[1].Basename)
	}
}

func TestScan_ParsesFields(t *testing.T) {
	dir := "testdata"
	got, err := Scan(dir)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("no entries")
	}
	var good *Entry
	for i := range got {
		if got[i].Basename == "good.config" {
			good = &got[i]
			break
		}
	}
	if good == nil {
		t.Fatal("good.config not found")
	}
	if good.CurrentContext != "prod" {
		t.Errorf("CurrentContext = %q, want prod", good.CurrentContext)
	}
	if len(good.Clusters) != 1 || good.Clusters[0] != "prod-cluster" {
		t.Errorf("Clusters = %v", good.Clusters)
	}
	if len(good.Users) != 2 {
		t.Errorf("Users = %v", good.Users)
	}
}

func TestScan_MissingDir(t *testing.T) {
	got, err := Scan(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if got != nil {
		t.Errorf("expected nil slice, got %v", got)
	}
}

func TestScanDirs_Dedup(t *testing.T) {
	dir := "testdata"
	got, err := ScanDirs([]string{dir, dir})
	if err != nil {
		t.Fatalf("ScanDirs: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 unique entries, got %d", len(got))
	}
}
