package daemon

import (
	"reflect"
	"testing"
)

func TestComputeParity_BothEmpty(t *testing.T) {
	orphan, missing := computeParity(map[int]bool{}, map[int]bool{})
	if len(orphan) != 0 || len(missing) != 0 {
		t.Errorf("expected empty, got orphan=%v missing=%v", orphan, missing)
	}
}

func TestComputeParity_Match(t *testing.T) {
	listens := map[int]bool{8080: true, 9999: true}
	state := map[int]bool{8080: true, 9999: true}
	orphan, missing := computeParity(listens, state)
	if len(orphan) != 0 || len(missing) != 0 {
		t.Errorf("expected parity ok, got orphan=%v missing=%v", orphan, missing)
	}
}

func TestComputeParity_Orphan(t *testing.T) {
	// Listener held by kpf but no matching forward state — typical leak.
	listens := map[int]bool{8080: true, 9999: true, 27017: true}
	state := map[int]bool{8080: true}
	orphan, missing := computeParity(listens, state)
	wantOrphan := []int{9999, 27017} // sorted
	if !reflect.DeepEqual(orphan, wantOrphan) {
		t.Errorf("orphan: got %v, want %v", orphan, wantOrphan)
	}
	if len(missing) != 0 {
		t.Errorf("missing: expected empty, got %v", missing)
	}
}

func TestComputeParity_Missing(t *testing.T) {
	// Forward registered but no listener — e.g. restore dial failed.
	listens := map[int]bool{8080: true}
	state := map[int]bool{8080: true, 9090: true, 27017: true}
	orphan, missing := computeParity(listens, state)
	wantMissing := []int{9090, 27017} // sorted
	if !reflect.DeepEqual(missing, wantMissing) {
		t.Errorf("missing: got %v, want %v", missing, wantMissing)
	}
	if len(orphan) != 0 {
		t.Errorf("orphan: expected empty, got %v", orphan)
	}
}

func TestComputeParity_BothSides(t *testing.T) {
	listens := map[int]bool{1000: true, 2000: true}
	state := map[int]bool{2000: true, 3000: true}
	orphan, missing := computeParity(listens, state)
	if !reflect.DeepEqual(orphan, []int{1000}) {
		t.Errorf("orphan: got %v, want [1000]", orphan)
	}
	if !reflect.DeepEqual(missing, []int{3000}) {
		t.Errorf("missing: got %v, want [3000]", missing)
	}
}

func TestComputeParity_SortedOutput(t *testing.T) {
	// Insertion order must not affect output ordering.
	listens := map[int]bool{9999: true, 8080: true, 1: true}
	state := map[int]bool{}
	orphan, _ := computeParity(listens, state)
	want := []int{1, 8080, 9999}
	if !reflect.DeepEqual(orphan, want) {
		t.Errorf("orphan: got %v, want %v", orphan, want)
	}
}

func TestComputeParity_NilMaps(t *testing.T) {
	// Defensive: a nil map should behave like an empty map (no panic).
	orphan, missing := computeParity(nil, nil)
	if len(orphan) != 0 || len(missing) != 0 {
		t.Errorf("expected empty, got orphan=%v missing=%v", orphan, missing)
	}
}
