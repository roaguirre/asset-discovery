package main

import (
	"strconv"
	"testing"

	"asset-discovery/internal/collect"
)

func TestDNSVariantSweepFlagsDefaultToExhaustive(t *testing.T) {
	resetDNSVariantSweepFlags(t)

	if got := rootCmd.Flags().Lookup("dns-variant-sweep-mode").DefValue; got != string(collect.DNSVariantSweepModeExhaustive) {
		t.Fatalf("expected exhaustive default flag value, got %q", got)
	}

	got, err := dnsVariantSweepConfigFromFlags()
	if err != nil {
		t.Fatalf("expected default DNS variant sweep config to parse, got %v", err)
	}

	want := collect.DefaultDNSVariantSweepConfig()
	if got != want {
		t.Fatalf("expected default DNS variant sweep config %+v, got %+v", want, got)
	}
}

func TestDNSVariantSweepFlagsOverrideConfig(t *testing.T) {
	resetDNSVariantSweepFlags(t)

	mustSetFlag(t, "dns-variant-sweep-mode", string(collect.DNSVariantSweepModePrioritized))
	mustSetFlag(t, "dns-variant-batch-size", "64")
	mustSetFlag(t, "dns-variant-concurrency", "9")
	mustSetFlag(t, "dns-variant-prioritized-cap", "1024")

	got, err := dnsVariantSweepConfigFromFlags()
	if err != nil {
		t.Fatalf("expected overridden DNS variant sweep config to parse, got %v", err)
	}

	want := collect.DNSVariantSweepConfig{
		Mode:           collect.DNSVariantSweepModePrioritized,
		BatchSize:      64,
		Concurrency:    9,
		PrioritizedCap: 1024,
	}
	if got != want {
		t.Fatalf("expected DNS variant sweep config %+v, got %+v", want, got)
	}
}

func mustSetFlag(t *testing.T, name string, value string) {
	t.Helper()

	if err := rootCmd.Flags().Set(name, value); err != nil {
		t.Fatalf("failed to set flag %s=%s: %v", name, value, err)
	}
}

func resetDNSVariantSweepFlags(t *testing.T) {
	t.Helper()

	defaults := collect.DefaultDNSVariantSweepConfig()
	mustSetFlag(t, "dns-variant-sweep-mode", string(defaults.Mode))
	mustSetFlag(t, "dns-variant-batch-size", strconv.Itoa(defaults.BatchSize))
	mustSetFlag(t, "dns-variant-concurrency", strconv.Itoa(defaults.Concurrency))
	mustSetFlag(t, "dns-variant-prioritized-cap", strconv.Itoa(defaults.PrioritizedCap))
}
