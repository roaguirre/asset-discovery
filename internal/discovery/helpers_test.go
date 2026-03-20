package discovery

import "testing"

func TestNormalizeOrganization_PreservesDistinctLegalNames(t *testing.T) {
	group := NormalizeOrganization("Example Group")
	holdings := NormalizeOrganization("Example Holdings")

	if group != "example group" {
		t.Fatalf("expected canonical group name, got %q", group)
	}
	if holdings != "example holdings" {
		t.Fatalf("expected canonical holdings name, got %q", holdings)
	}
	if group == holdings {
		t.Fatalf("expected distinct legal names to stay distinct after normalization")
	}
}
