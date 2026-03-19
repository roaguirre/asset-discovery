package nodes

const highConfidenceOwnershipThreshold = 0.50

func hasHighConfidenceOwnership(confidence float64) bool {
	return confidence >= highConfidenceOwnershipThreshold
}
