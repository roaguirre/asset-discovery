package models

import "strings"

func (p *PipelineContext) HasDNSVariantSweepLabel(label string) bool {
	label = normalizeDNSVariantSweepLabel(label)
	if label == "" {
		return false
	}

	p.Lock()
	defer p.Unlock()

	for _, existing := range p.DNSVariantSweepLabels {
		if existing == label {
			return true
		}
	}
	return false
}

func (p *PipelineContext) MarkDNSVariantSweepLabel(label string) {
	label = normalizeDNSVariantSweepLabel(label)
	if label == "" {
		return
	}

	p.Lock()
	defer p.Unlock()

	for _, existing := range p.DNSVariantSweepLabels {
		if existing == label {
			return
		}
	}
	p.DNSVariantSweepLabels = append(p.DNSVariantSweepLabels, label)
}

func normalizeDNSVariantSweepLabel(label string) string {
	return strings.ToLower(strings.TrimSpace(label))
}
