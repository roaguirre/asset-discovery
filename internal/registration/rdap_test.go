package registration

import (
	"context"
	"errors"
	"testing"

	whoisparser "github.com/likexian/whois-parser"
)

func TestLookupDomainWHOISWith_RetriesTransientFailure(t *testing.T) {
	attempts := 0
	data, err := lookupDomainWHOISWith(
		context.Background(),
		"example.com",
		func(domain string) (string, error) {
			attempts++
			if attempts == 1 {
				return "", errors.New("temporary whois failure")
			}
			return "raw whois payload", nil
		},
		func(raw string) (whoisparser.WhoisInfo, error) {
			return whoisparser.WhoisInfo{}, nil
		},
	)
	if err != nil {
		t.Fatalf("expected WHOIS retry to succeed, got %v", err)
	}
	if data == nil {
		t.Fatalf("expected WHOIS retry to return data")
	}
	if attempts != 2 {
		t.Fatalf("expected 2 WHOIS attempts, got %d", attempts)
	}
}

func TestLookupDomainWHOISWith_ReturnsUnsupportedWhenParserFails(t *testing.T) {
	_, err := lookupDomainWHOISWith(
		context.Background(),
		"example.com",
		func(domain string) (string, error) {
			return "raw whois payload", nil
		},
		func(raw string) (whoisparser.WhoisInfo, error) {
			return whoisparser.WhoisInfo{}, errors.New("parse failed")
		},
	)
	if !errors.Is(err, ErrUnsupportedRegistrationData) {
		t.Fatalf("expected unsupported registration data error, got %v", err)
	}
}
