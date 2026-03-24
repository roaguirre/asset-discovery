package registration

import (
	"context"
	"errors"
	"testing"
	"time"

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

func TestLookupDomainWHOISViaSystemWith_UsesNICChileCustomParser(t *testing.T) {
	raw := `Registrant name: francisco javier aguirre aguirre
Registrant organisation:
Registrant email: contacto@example.cl
Registrar name: NIC Chile
Registrar URL: https://www.nic.cl
Creation date: 2018-01-25 21:37:38 CLST
Expiration date: 2028-01-25 21:37:38 CLST
Modified date: 2025-03-23 08:11:10 CLST
Status: ok
Name server: achiel.ns.cloudflare.com
Name server: aida.ns.cloudflare.com
`

	data, err := lookupDomainWHOISViaSystemWith(
		context.Background(),
		"gesprobira.cl",
		func(ctx context.Context, args ...string) (string, error) {
			if len(args) != 3 || args[0] != "-h" || args[1] != "whois.nic.cl" || args[2] != "gesprobira.cl" {
				t.Fatalf("unexpected system whois args: %+v", args)
			}
			return raw, nil
		},
		func(raw string) (whoisparser.WhoisInfo, error) {
			return whoisparser.WhoisInfo{}, errors.New("parse failed")
		},
	)
	if err != nil {
		t.Fatalf("expected NIC Chile custom parse to succeed, got %v", err)
	}
	if data == nil {
		t.Fatalf("expected parsed RDAP data")
	}
	if data.RegistrarName != "NIC Chile" {
		t.Fatalf("expected registrar to be parsed, got %+v", data)
	}
	if data.RegistrarURL != "https://www.nic.cl" {
		t.Fatalf("expected registrar URL to be parsed, got %+v", data)
	}
	if data.RegistrantName != "francisco javier aguirre aguirre" {
		t.Fatalf("expected registrant name to be parsed, got %+v", data)
	}
	if data.RegistrantEmail != "contacto@example.cl" {
		t.Fatalf("expected registrant email to be parsed, got %+v", data)
	}
	if got := len(data.NameServers); got != 2 {
		t.Fatalf("expected nameservers to be parsed, got %+v", data)
	}
	if data.CreationDate.IsZero() || data.ExpirationDate.IsZero() || data.UpdatedDate.IsZero() {
		t.Fatalf("expected NIC Chile dates to be parsed, got %+v", data)
	}
	if got := len(data.Statuses); got != 1 || data.Statuses[0] != "ok" {
		t.Fatalf("expected NIC Chile status to be parsed, got %+v", data)
	}
}

func TestLookupDomainWHOISViaSystemWith_RetriesSystemWHOISFailures(t *testing.T) {
	attempts := 0
	raw := `Registrar name: NIC Chile
Registrar URL: https://www.nic.cl
`

	data, err := lookupDomainWHOISViaSystemWith(
		context.Background(),
		"gesprobira.cl",
		func(ctx context.Context, args ...string) (string, error) {
			attempts++
			if attempts == 1 {
				return "", errors.New("exit status 71")
			}
			return raw, nil
		},
		func(raw string) (whoisparser.WhoisInfo, error) {
			return whoisparser.WhoisInfo{}, errors.New("parse failed")
		},
	)
	if err != nil {
		t.Fatalf("expected system WHOIS retry to succeed, got %v", err)
	}
	if attempts != 2 {
		t.Fatalf("expected 2 system WHOIS attempts, got %d", attempts)
	}
	if data == nil || data.RegistrarURL != "https://www.nic.cl" {
		t.Fatalf("expected retried system WHOIS data to be parsed, got %+v", data)
	}
}

func TestLookupDomainWHOISViaSystemWith_MergesNICChileCustomFieldsWhenGenericParseSucceeds(t *testing.T) {
	raw := `Registrar name: NIC Chile
Registrar URL: https://www.nic.cl
Modified date: 2025-03-23 08:11:10 CLST
`

	data, err := lookupDomainWHOISViaSystemWith(
		context.Background(),
		"gesprobira.cl",
		func(ctx context.Context, args ...string) (string, error) {
			return raw, nil
		},
		func(raw string) (whoisparser.WhoisInfo, error) {
			return whoisparser.WhoisInfo{}, nil
		},
	)
	if err != nil {
		t.Fatalf("expected merged WHOIS parse to succeed, got %v", err)
	}
	if data == nil {
		t.Fatalf("expected parsed RDAP data")
	}
	if data.RegistrarURL != "https://www.nic.cl" {
		t.Fatalf("expected custom NIC Chile fields to merge after generic parse success, got %+v", data)
	}
	if data.UpdatedDate.IsZero() {
		t.Fatalf("expected NIC Chile modified date to be preserved, got %+v", data)
	}
}

func TestParseLooseTime_ParsesNamedWHOISTimezone(t *testing.T) {
	got := parseLooseTime("2018-01-25 21:37:38 CLST")
	if got.IsZero() {
		t.Fatal("expected CLST timestamp to parse")
	}

	want := time.Date(2018, time.January, 25, 21, 37, 38, 0, got.Location())
	if !got.Equal(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}
