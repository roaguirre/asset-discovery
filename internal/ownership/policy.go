package ownership

const highConfidenceThreshold = 0.50

func IsHighConfidence(confidence float64) bool {
	return confidence >= highConfidenceThreshold
}
