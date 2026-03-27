package ownership

const (
	// DefaultHighConfidenceThreshold is the standard minimum confidence for
	// automatically promoting judge-approved candidates.
	DefaultHighConfidenceThreshold = 0.50
	// ManualReviewConfidenceThreshold lowers the promotion bar for manual runs
	// so analysts can review weaker frontier-expansion candidates.
	ManualReviewConfidenceThreshold = 0.20
)

// IsHighConfidence reports whether a confidence score meets the default
// promotion threshold.
func IsHighConfidence(confidence float64) bool {
	return IsConfidenceAtLeast(confidence, DefaultHighConfidenceThreshold)
}

// IsConfidenceAtLeast reports whether a confidence score satisfies the
// provided threshold.
func IsConfidenceAtLeast(confidence float64, threshold float64) bool {
	return confidence >= threshold
}
