package discovery

import "testing"

func TestICANNPublicSuffixesFiltersPrivateWildcardAndExceptionRules(t *testing.T) {
	suffixes := ICANNPublicSuffixes()

	if !containsStringValue(suffixes, "com.au") {
		t.Fatalf("expected explicit ICANN suffix com.au to be present")
	}
	if containsStringValue(suffixes, "appspot.com") {
		t.Fatalf("expected private suffix appspot.com to be excluded")
	}
	if containsStringValue(suffixes, "*.ck") {
		t.Fatalf("expected wildcard suffix *.ck to be excluded")
	}
	if containsStringValue(suffixes, "!city.kawasaki.jp") {
		t.Fatalf("expected exception rule !city.kawasaki.jp to be excluded")
	}
}

func TestRegistrableLabel(t *testing.T) {
	if got := RegistrableLabel("example.com.au"); got != "example" {
		t.Fatalf("expected example label, got %q", got)
	}
	if got := RegistrableLabel("example.com"); got != "example" {
		t.Fatalf("expected example label, got %q", got)
	}
}

func TestExtractStructuredTXTDomainCandidates(t *testing.T) {
	values := []string{
		`v=spf1 include:_spf.example.net redirect=spf.example.org exists:mail.example.io`,
		`v=DMARC1; rua=mailto:dmarc@example.com,mailto:security@example-security.com; ruf=mailto:alerts@example-help.com`,
	}

	candidates := ExtractStructuredTXTDomainCandidates(values...)

	expected := []string{
		"_spf.example.net",
		"spf.example.org",
		"mail.example.io",
		"example.com",
		"example-security.com",
		"example-help.com",
	}
	for _, want := range expected {
		if !containsStringValue(candidates, want) {
			t.Fatalf("expected %q in TXT candidates, got %+v", want, candidates)
		}
	}
}

func containsStringValue(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
