package models

import "strings"

// HasAISearchExecutedRoot reports whether the run already executed AI search
// for the given registrable root.
func (p *PipelineContext) HasAISearchExecutedRoot(root string) bool {
	root = normalizeAISearchExecutedRoot(root)
	if root == "" {
		return false
	}

	p.Lock()
	defer p.Unlock()

	for _, existing := range p.AISearchExecutedRoots {
		if existing == root {
			return true
		}
	}
	return false
}

// MarkAISearchExecutedRoot records that AI search already ran for the given
// registrable root during the current run.
func (p *PipelineContext) MarkAISearchExecutedRoot(root string) {
	root = normalizeAISearchExecutedRoot(root)
	if root == "" {
		return
	}

	p.Lock()
	defer p.Unlock()

	for _, existing := range p.AISearchExecutedRoots {
		if existing == root {
			return
		}
	}
	p.AISearchExecutedRoots = append(p.AISearchExecutedRoots, root)
}

func normalizeAISearchExecutedRoot(root string) string {
	return strings.ToLower(strings.TrimSpace(root))
}
