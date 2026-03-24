package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/spf13/cobra"

	"asset-discovery/internal/app"
	"asset-discovery/internal/models"
	"asset-discovery/internal/tracing/telemetry"
)

var (
	seedsFile      string
	outputs        []string
	visualizerPath string
)

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

		pipeline := app.NewPipeline(app.Config{
			Outputs:        outputs,
			OutputsChanged: cmd.Flags().Changed("outputs"),
			Telemetry:      telemetry.NewStdlibProvider(log.Default()),
		})

		_, err := pipeline.Run(context.Background(), seeds)
		if err != nil {
			return nil
		}

		return nil
	},
}

func init() {
	rootCmd.Flags().StringVarP(&seedsFile, "seeds", "s", "", "Path to seeds JSON file")
	rootCmd.Flags().StringSliceVarP(&outputs, "outputs", "o", nil, "Comma separated list of output paths. If omitted, timestamped JSON/CSV/XLSX exports are written under exports/runs/<run-id>/ and exports/visualizer.html is refreshed.")

	refreshVisualizerCmd := &cobra.Command{
		Use:   "refresh-visualizer",
		Short: "Rebuild visualizer.html from archived visualizer snapshots",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.RefreshVisualizerHTML(visualizerPath); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Refreshed %s\n", visualizerPath)
			return nil
		},
	}
	refreshVisualizerCmd.Flags().StringVar(&visualizerPath, "path", app.DefaultVisualizerOutput, "Path to the visualizer HTML file to rebuild from archived snapshots")
	rootCmd.AddCommand(refreshVisualizerCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
