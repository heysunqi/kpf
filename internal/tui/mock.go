package tui

// mockResourceType is reused by the resource-kind picker (step ③).
// Counts are sourced from the static defaults in defaultResourceTypes();
// computing per-kind counts from real resources was abandoned because
// step ④ fetches the actual list anyway.
type mockResourceType struct {
	Kind  string
	Count int
}