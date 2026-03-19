package nodes

const highConfidenceOwnershipThreshold = 0.90

func hasHighConfidenceOwnership(confidence float64) bool {
	return confidence >= highConfidenceOwnershipThreshold
}
