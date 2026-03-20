package discovery

import (
	"net"
	"regexp"
	"strings"

	"golang.org/x/net/publicsuffix"
)

var dnsDomainPattern = regexp.MustCompile(`(?i)^[_a-z0-9](?:[_a-z0-9-]{0,61}[_a-z0-9])?(?:\.[_a-z0-9](?:[_a-z0-9-]{0,61}[_a-z0-9])?)+$`)

// RegistrableLabel returns the registrable label left of the public suffix.
// For example, example.com.au -> example.
func RegistrableLabel(domain string) string {
	root := RegistrableDomain(domain)
	if root == "" {
		return ""
	}

	suffix, _ := publicsuffix.PublicSuffix(root)
	suffix = NormalizeDomainIdentifier(suffix)
	if suffix == "" || suffix == root {
		return ""
	}

	label := strings.TrimSuffix(root, "."+suffix)
	if label == "" || strings.Contains(label, ".") {
		return ""
	}

	return label
}

func LastLabel(domain string) string {
	domain = NormalizeDomainIdentifier(domain)
	if domain == "" {
		return ""
	}

	parts := strings.Split(domain, ".")
	return parts[len(parts)-1]
}

// ExtractDNSDomainCandidates is like ExtractDomainCandidates but accepts
// underscore-prefixed DNS host labels such as _spf.example.com.
func ExtractDNSDomainCandidates(raw string) []string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "*.")
	if raw == "" {
		return nil
	}

	candidates := make([]string, 0, 3)
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
		if !isLikelyDNSDomain(candidate) {
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

// ExtractStructuredTXTDomainCandidates extracts domain-like values from DNS TXT
// records, including SPF and DMARC directives that encode domains.
func ExtractStructuredTXTDomainCandidates(values ...string) []string {
	candidates := make([]string, 0)

	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}

		candidates = append(candidates, ExtractDNSDomainCandidates(trimmed)...)

		tokens := strings.FieldsFunc(trimmed, func(r rune) bool {
			switch r {
			case ' ', '\t', '\n', '\r', ';':
				return true
			default:
				return false
			}
		})

		for _, token := range tokens {
			token = strings.Trim(token, "\"'(),")
			if token == "" {
				continue
			}

			lower := strings.ToLower(token)
			switch {
			case strings.HasPrefix(lower, "include:"):
				candidates = append(candidates, ExtractDNSDomainCandidates(token[len("include:"):])...)
			case strings.HasPrefix(lower, "exists:"):
				candidates = append(candidates, ExtractDNSDomainCandidates(token[len("exists:"):])...)
			case strings.HasPrefix(lower, "redirect="):
				candidates = append(candidates, ExtractDNSDomainCandidates(token[len("redirect="):])...)
			case strings.HasPrefix(lower, "exp="):
				candidates = append(candidates, ExtractDNSDomainCandidates(token[len("exp="):])...)
			case strings.HasPrefix(lower, "rua="), strings.HasPrefix(lower, "ruf="):
				raw := token[strings.IndexByte(token, '=')+1:]
				for _, item := range strings.Split(raw, ",") {
					candidates = append(candidates, ExtractDNSDomainCandidates(strings.TrimSpace(item))...)
				}
			}
		}
	}

	return UniqueLowerStrings(candidates)
}

func isLikelyDNSDomain(candidate string) bool {
	candidate = NormalizeDomainIdentifier(candidate)
	if candidate == "" || net.ParseIP(candidate) != nil {
		return false
	}
	return dnsDomainPattern.MatchString(candidate)
}
