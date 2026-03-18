package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"asset-discovery/internal/dag"
	"asset-discovery/internal/models"
	"asset-discovery/internal/nodes"
)

var (
	seedsFile string
	outputs   []string
)

const defaultVisualizerOutput = "exports/visualizer.html"

var rootCmd = &cobra.Command{
	Use:   "discover",
	Short: "Enterprise Asset Discovery tool",
	RunE: func(cmd *cobra.Command, args []string) error {
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

		ctx := context.Background()
		pCtx := &models.PipelineContext{
			Seeds: seeds,
		}

		runStartedAt := time.Now()
		resolvedOutputs, runID := resolveOutputTargets(outputs, cmd.Flags().Changed("outputs"), runStartedAt)
		exporters := buildExporters(resolvedOutputs, runID)

		if len(resolvedOutputs) > 0 {
			log.Printf("Export run %s will write to: %s", runID, strings.Join(resolvedOutputs, ", "))
		}

		// 2. Initialize Engine & Nodes
		engine := &dag.Engine{
			Collectors: []dag.Collector{
				nodes.NewDNSCollector(),
				nodes.NewCrtShCollector(),
				nodes.NewRDAPCollector(),
				nodes.NewHackerTargetCollector(),
				nodes.NewAlienVaultCollector(),
				nodes.NewWaybackCollector(),
			},
			Enrichers: []dag.Enricher{
				nodes.NewDNSResolverEnricher(),
				nodes.NewIPEnricher(),
			},
			Filters:   []dag.Filter{nodes.NewMergeFilter()},
			Exporters: exporters,
		}

		log.Println("Starting pipeline...")

		// 3. Run Engine
		_, err := engine.Run(ctx, pCtx)
		if err != nil {
			log.Printf("Pipeline completed with error: %v", err)
		} else {
			log.Println("Pipeline completed successfully.")
		}

		return nil
	},
}

func init() {
	rootCmd.Flags().StringVarP(&seedsFile, "seeds", "s", "", "Path to seeds JSON file")
	rootCmd.Flags().StringSliceVarP(&outputs, "outputs", "o", nil, "Comma separated list of output paths. If omitted, timestamped JSON/CSV/XLSX exports are written under exports/runs/<run-id>/ and exports/visualizer.html is refreshed.")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func resolveOutputTargets(requested []string, outputsChanged bool, now time.Time) ([]string, string) {
	runID := buildRunID(now)
	if outputsChanged {
		return append([]string(nil), requested...), runID
	}

	baseDir := filepath.Join("exports", "runs", runID)
	return []string{
		filepath.Join(baseDir, "results.json"),
		filepath.Join(baseDir, "results.csv"),
		filepath.Join(baseDir, "results.xlsx"),
		defaultVisualizerOutput,
	}, runID
}

func buildRunID(now time.Time) string {
	return now.Format("20060102T150405.000000000-0700")
}

func buildExporters(outputTargets []string, runID string) []dag.Exporter {
	var exporters []dag.Exporter
	var visualizerTargets []string

	rawDownloads := models.VisualizerDownloads{}

	for _, out := range outputTargets {
		switch strings.ToLower(filepath.Ext(out)) {
		case ".json":
			exporters = append(exporters, nodes.NewJSONExporter(out))
			rawDownloads.JSON = out
		case ".csv":
			exporters = append(exporters, nodes.NewCSVExporter(out))
			rawDownloads.CSV = out
		case ".xlsx":
			exporters = append(exporters, nodes.NewXLSXExporter(out))
			rawDownloads.XLSX = out
		case ".html":
			visualizerTargets = append(visualizerTargets, out)
		default:
			log.Printf("Warning: unsupported output format for %s, skipping.", out)
		}
	}

	for _, out := range visualizerTargets {
		exporters = append(exporters, nodes.NewVisualizerExporter(out, runID, relativeDownloads(out, rawDownloads)))
	}

	return exporters
}

func relativeDownloads(htmlPath string, downloads models.VisualizerDownloads) models.VisualizerDownloads {
	return models.VisualizerDownloads{
		JSON: relativeOutputPath(htmlPath, downloads.JSON),
		CSV:  relativeOutputPath(htmlPath, downloads.CSV),
		XLSX: relativeOutputPath(htmlPath, downloads.XLSX),
	}
}

func relativeOutputPath(fromFile, toFile string) string {
	if toFile == "" {
		return ""
	}

	rel, err := filepath.Rel(filepath.Dir(fromFile), toFile)
	if err != nil {
		return filepath.ToSlash(toFile)
	}

	return filepath.ToSlash(rel)
}
