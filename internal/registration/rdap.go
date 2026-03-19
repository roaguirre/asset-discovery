package registration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"asset-discovery/internal/discovery"
	"asset-discovery/internal/fetchutil"
	"asset-discovery/internal/models"

	"github.com/likexian/whois"
	whoisparser "github.com/likexian/whois-parser"
)

var ErrUnsupportedRegistrationData = errors.New("registration data unavailable")

const (
	whoisMaxAttempts    = 3
	whoisInitialBackoff = 250 * time.Millisecond
	whoisMaxBackoff     = 2 * time.Second
)

type rdapEntity struct {
	Roles     []string `json:"roles,omitempty"`
	PublicIds []struct {
		Identifier string `json:"identifier"`
	} `json:"publicIds,omitempty"`
	VCardArray []any `json:"vcardArray,omitempty"`
}

type rdapResponse struct {
	Entities []rdapEntity `json:"entities,omitempty"`
	Events   []struct {
		EventAction string `json:"eventAction,omitempty"`
		EventDate   string `json:"eventDate,omitempty"`
	} `json:"events,omitempty"`
	Status      []string `json:"status,omitempty"`
	NameServers []struct {
		LDHName string `json:"ldhName,omitempty"`
	} `json:"nameservers,omitempty"`
}

func LookupDomain(ctx context.Context, client *http.Client, domain string) (*models.RDAPData, error) {
	url := fmt.Sprintf("https://rdap.org/domain/%s", domain)

	resp, err := fetchutil.DoRequest(ctx, client, func(ctx context.Context) (*http.Request, error) {
		retryReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		retryReq.Header.Set("Accept", "application/rdap+json")
		retryReq.Header.Set("User-Agent", "Asset-Discovery-Bot/1.0")
		return retryReq, nil
	})
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return lookupDomainWHOIS(ctx, domain)
		}
		return nil, fmt.Errorf("unexpected status code %d from RDAP for %s", resp.StatusCode, domain)
	}

	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}

	var rdapResp rdapResponse
	if err := json.Unmarshal(body, &rdapResp); err != nil {
		return nil, err
	}

	data := &models.RDAPData{
		Statuses: discovery.UniqueLowerStrings(rdapResp.Status),
	}

	for _, ns := range rdapResp.NameServers {
		if normalized := discovery.NormalizeDomainIdentifier(ns.LDHName); normalized != "" {
			data.NameServers = append(data.NameServers, normalized)
		}
	}
	data.NameServers = discovery.UniqueLowerStrings(data.NameServers)

	for _, ev := range rdapResp.Events {
		parsedTime, err := time.Parse(time.RFC3339, ev.EventDate)
		if err != nil {
			continue
		}
		switch ev.EventAction {
		case "registration":
			data.CreationDate = parsedTime
		case "expiration":
			data.ExpirationDate = parsedTime
		case "last changed":
			data.UpdatedDate = parsedTime
		}
	}

	for _, entity := range rdapResp.Entities {
		vcard := parseEntityVCard(entity.VCardArray)
		switch {
		case entityHasRole(entity, "registrant"):
			if data.RegistrantName == "" {
				data.RegistrantName = vcard.name
			}
			if data.RegistrantOrg == "" {
				data.RegistrantOrg = vcard.organization
			}
			if data.RegistrantEmail == "" {
				data.RegistrantEmail = vcard.email
			}
		case entityHasRole(entity, "registrar"):
			if data.RegistrarName == "" {
				data.RegistrarName = discovery.FirstNonEmpty(vcard.organization, vcard.name)
			}
			if data.RegistrarIANAID == "" && len(entity.PublicIds) > 0 {
				data.RegistrarIANAID = strings.TrimSpace(entity.PublicIds[0].Identifier)
			}
		}
	}

	return data, nil
}

func lookupDomainWHOIS(ctx context.Context, domain string) (*models.RDAPData, error) {
	return lookupDomainWHOISWith(ctx, domain, func(domain string) (string, error) {
		return whois.Whois(domain)
	}, whoisparser.Parse)
}

func lookupDomainWHOISWith(ctx context.Context, domain string, lookup func(string) (string, error), parse func(string) (whoisparser.WhoisInfo, error)) (*models.RDAPData, error) {
	whoisRaw, err := retryWHOISLookup(ctx, domain, lookup)
	if err != nil {
		return nil, fmt.Errorf("WHOIS lookup failed for %s: %w", domain, err)
	}

	parsed, err := parse(whoisRaw)
	if err != nil {
		return nil, ErrUnsupportedRegistrationData
	}

	data := &models.RDAPData{}

	if parsed.Domain != nil {
		data.Statuses = discovery.UniqueLowerStrings(parsed.Domain.Status)
		data.NameServers = discovery.UniqueLowerStrings(parsed.Domain.NameServers)

		if parsed.Domain.CreatedDateInTime != nil {
			data.CreationDate = *parsed.Domain.CreatedDateInTime
		} else if parsed.Domain.CreatedDate != "" {
			data.CreationDate = parseLooseTime(parsed.Domain.CreatedDate)
		}
		if parsed.Domain.ExpirationDateInTime != nil {
			data.ExpirationDate = *parsed.Domain.ExpirationDateInTime
		} else if parsed.Domain.ExpirationDate != "" {
			data.ExpirationDate = parseLooseTime(parsed.Domain.ExpirationDate)
		}
		if parsed.Domain.UpdatedDateInTime != nil {
			data.UpdatedDate = *parsed.Domain.UpdatedDateInTime
		} else if parsed.Domain.UpdatedDate != "" {
			data.UpdatedDate = parseLooseTime(parsed.Domain.UpdatedDate)
		}
	}

	if parsed.Registrar != nil {
		data.RegistrarName = strings.TrimSpace(parsed.Registrar.Name)
		data.RegistrarIANAID = strings.TrimSpace(parsed.Registrar.ID)
	}

	if parsed.Registrant != nil {
		data.RegistrantName = strings.TrimSpace(parsed.Registrant.Name)
		data.RegistrantEmail = strings.TrimSpace(parsed.Registrant.Email)
		data.RegistrantOrg = strings.TrimSpace(parsed.Registrant.Organization)
	}

	return data, nil
}

func retryWHOISLookup(ctx context.Context, domain string, lookup func(string) (string, error)) (string, error) {
	backoff := whoisInitialBackoff
	var lastErr error

	for attempt := 1; attempt <= whoisMaxAttempts; attempt++ {
		if ctx != nil && ctx.Err() != nil {
			if lastErr != nil {
				return "", lastErr
			}
			return "", ctx.Err()
		}

		raw, err := lookup(domain)
		if err == nil {
			return raw, nil
		}

		lastErr = err
		if attempt == whoisMaxAttempts || !shouldRetryWHOISError(ctx, err) {
			break
		}

		wait := backoff
		if wait <= 0 {
			wait = 100 * time.Millisecond
		}
		if wait > whoisMaxBackoff {
			wait = whoisMaxBackoff
		}

		timer := time.NewTimer(wait)
		if ctx == nil {
			<-timer.C
		} else {
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return "", ctx.Err()
			}
		}

		if backoff <= 0 {
			backoff = 100 * time.Millisecond
		} else {
			backoff *= 2
			if backoff > whoisMaxBackoff {
				backoff = whoisMaxBackoff
			}
		}
	}

	return "", lastErr
}

func shouldRetryWHOISError(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		return false
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	return true
}

type rdapVCard struct {
	name         string
	organization string
	email        string
}

func parseEntityVCard(raw []any) rdapVCard {
	if len(raw) < 2 {
		return rdapVCard{}
	}

	properties, ok := raw[1].([]any)
	if !ok {
		return rdapVCard{}
	}

	out := rdapVCard{}
	for _, property := range properties {
		entry, ok := property.([]any)
		if !ok || len(entry) < 4 {
			continue
		}

		name, ok := entry[0].(string)
		if !ok {
			continue
		}

		value := strings.TrimSpace(vcardValueString(entry[3]))
		if value == "" {
			continue
		}

		switch strings.ToLower(name) {
		case "fn":
			if out.name == "" {
				out.name = value
			}
		case "org":
			if out.organization == "" {
				out.organization = value
			}
		case "email":
			if out.email == "" {
				out.email = strings.TrimPrefix(strings.ToLower(value), "mailto:")
			}
		}
	}

	return out
}

func vcardValueString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			part := strings.TrimSpace(fmt.Sprint(item))
			if part != "" {
				parts = append(parts, part)
			}
		}
		return strings.Join(parts, " ")
	default:
		return fmt.Sprint(value)
	}
}

func entityHasRole(entity rdapEntity, role string) bool {
	role = strings.ToLower(strings.TrimSpace(role))
	for _, candidate := range entity.Roles {
		if strings.ToLower(strings.TrimSpace(candidate)) == role {
			return true
		}
	}
	return false
}

func parseLooseTime(value string) time.Time {
	layouts := []string{
		time.RFC3339,
		"2006-01-02T15:04:05Z",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed
		}
	}
	return time.Time{}
}
