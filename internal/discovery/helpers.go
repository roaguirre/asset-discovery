package discovery

import (
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/net/publicsuffix"

	"asset-discovery/internal/models"
)

var domainPattern = regexp.MustCompile(`(?i)^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)

var companyStopWords = map[string]struct{}{
	"and": {}, "cl": {}, "co": {}, "company": {}, "corp": {}, "corporation": {}, "de": {}, "el": {}, "for": {},
	"foundation": {}, "group": {}, "holding": {}, "holdings": {}, "inc": {}, "international": {}, "la": {},
	"limitada": {}, "limited": {}, "llc": {}, "ltda": {}, "net": {}, "sa": {}, "services": {}, "solutions": {},
	"spa": {}, "systems": {}, "tech": {}, "the": {},
}

func NormalizeDomainIdentifier(identifier string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(identifier), "."))
}

func RegistrableDomain(identifier string) string {
	normalized := NormalizeDomainIdentifier(identifier)
	if normalized == "" {
		return ""
	}

	root, err := publicsuffix.EffectiveTLDPlusOne(normalized)
	if err != nil {
		return normalized
	}

	return root
}

func BuildDiscoveredSeed(parent models.Seed, domain string, tag string) models.Seed {
	root := RegistrableDomain(domain)
	if root == "" {
		root = NormalizeDomainIdentifier(domain)
	}

	tags := append([]string{}, parent.Tags...)
	if tag != "" {
		tags = append(tags, tag)
	}

	return models.Seed{
		ID:          parent.ID + ":" + root,
		CompanyName: FirstNonEmpty(parent.CompanyName, parent.ID),
		Domains:     []string{root},
		Address:     parent.Address,
		Industry:    parent.Industry,
		Tags:        UniqueLowerStrings(tags),
	}
}

func RootsOverlap(left, right []string) bool {
	if len(left) == 0 || len(right) == 0 {
		return false
	}

	seen := make(map[string]struct{}, len(left))
	for _, candidate := range left {
		root := RegistrableDomain(candidate)
		if root == "" {
			continue
		}
		seen[root] = struct{}{}
	}

	for _, candidate := range right {
		root := RegistrableDomain(candidate)
		if root == "" {
			continue
		}
		if _, exists := seen[root]; exists {
			return true
		}
	}

	return false
}

func UniqueLowerStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	sort.Strings(out)
	return out
}

func ExtractDomainCandidates(raw string) []string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "*.")
	if raw == "" {
		return nil
	}

	candidates := make([]string, 0, 2)
	if domain := emailDomain(raw); domain != "" {
		candidates = append(candidates, domain)
	}
	if host := hostFromURLLike(raw); host != "" {
		candidates = append(candidates, host)
	}
	if normalized := NormalizeDomainIdentifier(raw); normalized != "" {
		candidates = append(candidates, normalized)
	}

	unique := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if !isLikelyDomain(candidate) {
			continue
		}
		if _, exists := seen[candidate]; exists {
			continue
		}
		seen[candidate] = struct{}{}
		unique = append(unique, candidate)
	}

	return unique
}

func NormalizeOrganization(raw string) string {
	tokens := make([]string, 0)
	raw = strings.ToLower(raw)
	raw = strings.NewReplacer("&", " ", "/", " ", "-", " ", "_", " ", ".", " ", ",", " ").Replace(raw)
	for _, token := range strings.Fields(raw) {
		if len(token) < 3 {
			continue
		}
		if _, skip := companyStopWords[token]; skip {
			continue
		}
		tokens = append(tokens, token)
	}
	return strings.Join(tokens, " ")
}

func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func emailDomain(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	raw = strings.TrimPrefix(raw, "mailto:")
	parts := strings.Split(raw, "@")
	if len(parts) != 2 {
		return ""
	}
	return NormalizeDomainIdentifier(parts[1])
}

func hostFromURLLike(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	if !strings.Contains(raw, "://") {
		raw = "https://" + strings.TrimPrefix(raw, "//")
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}

	return NormalizeDomainIdentifier(parsed.Hostname())
}

func isLikelyDomain(candidate string) bool {
	candidate = NormalizeDomainIdentifier(candidate)
	if candidate == "" || net.ParseIP(candidate) != nil {
		return false
	}
	return domainPattern.MatchString(candidate)
}
