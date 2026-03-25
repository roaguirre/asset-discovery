package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"asset-discovery/internal/app"
	"asset-discovery/internal/collect"
	"asset-discovery/internal/models"
	"asset-discovery/internal/tracing/telemetry"
)

var (
	seedsFile                string
	outputs                  []string
	dnsVariantSweepMode      string
	dnsVariantBatchSize      int
	dnsVariantConcurrency    int
	dnsVariantPrioritizedCap int
)

var rootCmd = &cobra.Command{
	Use:   "discover",
	Short: "Enterprise Asset Discovery tool",
	RunE: func(cmd *cobra.Command, args []string) error {
		dnsVariantSweepConfig, err := dnsVariantSweepConfigFromFlags()
		if err != nil {
			return err
		}

		// 1. Load seeds
		var seeds []models.Seed
		if seedsFile != "" {
			data, err := os.ReadFile(seedsFile)
			if err != nil {
				return fmt.Errorf("failed to read seeds file: %w", err)
			}
			if err := json.Unmarshal(data, &seeds); err != nil {
				return fmt.Errorf("failed to unmarshal seeds: %w", err)
			}
		}

		pipeline, err := app.NewPipeline(app.Config{
			Outputs:         outputs,
			OutputsChanged:  cmd.Flags().Changed("outputs"),
			Telemetry:       telemetry.NewStdlibProvider(log.Default()),
			DNSVariantSweep: dnsVariantSweepConfig,
		})
		if err != nil {
			return err
		}

		_, err = pipeline.Run(context.Background(), seeds)
		if err != nil {
			return nil
		}

		return nil
	},
}

func dnsVariantSweepConfigFromFlags() (collect.DNSVariantSweepConfig, error) {
	mode := collect.DNSVariantSweepMode(strings.ToLower(strings.TrimSpace(dnsVariantSweepMode)))
	switch mode {
	case collect.DNSVariantSweepModeExhaustive, collect.DNSVariantSweepModePrioritized:
	default:
		return collect.DNSVariantSweepConfig{}, fmt.Errorf("unsupported DNS variant sweep mode %q", dnsVariantSweepMode)
	}

	return collect.DNSVariantSweepConfig{
		Mode:           mode,
		BatchSize:      dnsVariantBatchSize,
		Concurrency:    dnsVariantConcurrency,
		PrioritizedCap: dnsVariantPrioritizedCap,
	}, nil
}

func init() {
	defaultVariantSweep := collect.DefaultDNSVariantSweepConfig()

	rootCmd.Flags().StringVarP(&seedsFile, "seeds", "s", "", "Path to seeds JSON file")
	rootCmd.Flags().StringSliceVarP(&outputs, "outputs", "o", nil, "Comma separated list of output paths. Use file paths for JSON, CSV, and XLSX outputs only. If omitted, timestamped exports are written under exports/runs/<run-id>/.")
	rootCmd.Flags().StringVar(&dnsVariantSweepMode, "dns-variant-sweep-mode", string(defaultVariantSweep.Mode), "DNS variant sweep mode: exhaustive or prioritized")
	rootCmd.Flags().IntVar(&dnsVariantBatchSize, "dns-variant-batch-size", defaultVariantSweep.BatchSize, "Maximum number of DNS variant roots to process per batch")
	rootCmd.Flags().IntVar(&dnsVariantConcurrency, "dns-variant-concurrency", defaultVariantSweep.Concurrency, "Maximum number of concurrent DNS variant probes")
	rootCmd.Flags().IntVar(&dnsVariantPrioritizedCap, "dns-variant-prioritized-cap", defaultVariantSweep.PrioritizedCap, "Maximum DNS variant roots to probe in prioritized mode")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
