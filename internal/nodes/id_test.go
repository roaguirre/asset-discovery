package nodes

import "testing"

func TestNewNodeID_IsUnique(t *testing.T) {
	const total = 5000

	seen := make(map[string]struct{}, total)
	for i := 0; i < total; i++ {
		id := newNodeID("dom-crtsh")
		if _, exists := seen[id]; exists {
			t.Fatalf("expected generated ID to be unique, got duplicate %q", id)
		}
		seen[id] = struct{}{}
	}
}
