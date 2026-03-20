package discovery

import (
	"sync"
)

//go:generate go run ../tools/generate_icann_suffixes -out icann_suffixes_gen.go

var (
	icannSuffixesOnce    sync.Once
	icannSuffixPositions map[string]int
)

func ICANNPublicSuffixes() []string {
	initICANNSuffixes()
	return append([]string(nil), icannExplicitSuffixes...)
}

func ICANNSuffixPosition(suffix string) int {
	initICANNSuffixes()

	suffix = NormalizeDomainIdentifier(suffix)
	if idx, exists := icannSuffixPositions[suffix]; exists {
		return idx
	}

	return len(icannExplicitSuffixes) + 1
}

func initICANNSuffixes() {
	icannSuffixesOnce.Do(func() {
		icannSuffixPositions = make(map[string]int)
		for idx, suffix := range icannExplicitSuffixes {
			icannSuffixPositions[suffix] = idx
		}
	})
}
