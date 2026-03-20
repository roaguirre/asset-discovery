package app

import (
	"context"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"asset-discovery/internal/collect"
	"asset-discovery/internal/dag"
	"asset-discovery/internal/enrich"
	"asset-discovery/internal/export"
	"asset-discovery/internal/filter"
	"asset-discovery/internal/models"
	"asset-discovery/internal/ownership"
	"asset-discovery/internal/tracing/telemetry"
	"asset-discovery/internal/webhint"
)

const DefaultVisualizerOutput = "exports/visualizer.html"

type Config struct {
	Outputs        []string
	OutputsChanged bool
	RunID          string
	Now            func() time.Time
	Telemetry      telemetry.Provider
}

type Pipeline struct {
	engine    *dag.Engine
	outputs   []string
	runID     string
	telemetry telemetry.Provider
}

func NewPipeline(cfg Config) *Pipeline {
	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = time.Now
	}

	runID := cfg.RunID
	now := nowFn()
	if runID == "" {
		runID = BuildRunID(now)
	}

	outputs, resolvedRunID := ResolveOutputTargets(cfg.Outputs, cfg.OutputsChanged, now)
	if cfg.RunID != "" {
		resolvedRunID = cfg.RunID
	}

	provider := cfg.Telemetry
	if provider == nil {
		provider = telemetry.Noop()
	}

	fastClient := &http.Client{Timeout: 20 * time.Second}
	standardClient := &http.Client{Timeout: 30 * time.Second}
	archiveClient := &http.Client{Timeout: 60 * time.Second}
	dnsRDAPClient := &http.Client{Timeout: 10 * time.Second}

	ownershipJudge := ownership.NewDefaultJudge()
	webHintJudge := webhint.NewDefaultJudge()

	engine := &dag.Engine{
		Collectors: []dag.Collector{
			collect.NewDNSCollector(
				collect.WithDNSCollectorJudge(ownershipJudge),
				collect.WithDNSCollectorRDAPClient(dnsRDAPClient),
			),
			collect.NewCrtShCollector(collect.WithCrtShClient(archiveClient)),
			collect.NewRDAPCollector(collect.WithRDAPClient(standardClient)),
			collect.NewReverseRegistrationCollector(
				collect.WithReverseRegistrationClient(archiveClient),
				collect.WithReverseRegistrationJudge(ownershipJudge),
			),
			collect.NewHackerTargetCollector(collect.WithHackerTargetClient(standardClient)),
			collect.NewAlienVaultCollector(collect.WithAlienVaultClient(standardClient)),
			collect.NewWaybackCollector(collect.WithWaybackClient(archiveClient)),
			collect.NewASNCIDRCollector(
				collect.WithASNCIDRClient(standardClient),
				collect.WithASNCIDRJudge(ownershipJudge),
			),
			collect.NewCrawlerCollector(
				collect.WithCrawlerClient(fastClient),
				collect.WithCrawlerJudge(ownershipJudge),
			),
			collect.NewWebHintCollector(
				collect.WithWebHintClient(fastClient),
				collect.WithWebHintJudge(webHintJudge),
			),
		},
		Enrichers: []dag.Enricher{
			enrich.NewDNSResolverEnricher(),
			enrich.NewIPEnricher(enrich.WithIPEnricherJudge(ownershipJudge)),
		},
		Filters: []dag.Filter{
			filter.NewMergeFilter(),
		},
		Exporters: BuildExporters(outputs, resolvedRunID),
		RunID:     resolvedRunID,
		Telemetry: provider,
	}

	return &Pipeline{
		engine:    engine,
		outputs:   outputs,
		runID:     resolvedRunID,
		telemetry: provider,
	}
}

func (p *Pipeline) Run(ctx context.Context, seeds []models.Seed) (*models.PipelineContext, error) {
	ctx = telemetry.WithProvider(ctx, p.telemetry)

	if len(p.outputs) > 0 {
		telemetry.Infof(ctx, "Export run %s will write to: %s", p.runID, strings.Join(p.outputs, ", "))
	}
	telemetry.Info(ctx, "Starting pipeline...")

	pCtx := &models.PipelineContext{
		Seeds: append([]models.Seed(nil), seeds...),
	}

	result, err := p.engine.Run(ctx, pCtx)
	if err != nil {
		telemetry.Infof(ctx, "Pipeline completed with error: %v", err)
		return result, err
	}

	telemetry.Info(ctx, "Pipeline completed successfully.")
	return result, nil
}

func (p *Pipeline) Outputs() []string {
	return append([]string(nil), p.outputs...)
}

func (p *Pipeline) RunID() string {
	return p.runID
}

func ResolveOutputTargets(requested []string, outputsChanged bool, now time.Time) ([]string, string) {
	runID := BuildRunID(now)
	if outputsChanged {
		return append([]string(nil), requested...), runID
	}

	baseDir := filepath.Join("exports", "runs", runID)
	return []string{
		filepath.Join(baseDir, "results.json"),
		filepath.Join(baseDir, "results.csv"),
		filepath.Join(baseDir, "results.xlsx"),
		DefaultVisualizerOutput,
	}, runID
}

func BuildRunID(now time.Time) string {
	return now.Format("20060102T150405.000000000-0700")
}

func BuildExporters(outputTargets []string, runID string) []dag.Exporter {
	var exporters []dag.Exporter
	var visualizerTargets []string

	rawDownloads := export.Downloads{}

	for _, out := range outputTargets {
		switch strings.ToLower(filepath.Ext(out)) {
		case ".json":
			exporters = append(exporters, export.NewJSONExporter(out))
			rawDownloads.JSON = out
		case ".csv":
			exporters = append(exporters, export.NewCSVExporter(out))
			rawDownloads.CSV = out
		case ".xlsx":
			exporters = append(exporters, export.NewXLSXExporter(out))
			rawDownloads.XLSX = out
		case ".html":
			visualizerTargets = append(visualizerTargets, out)
		default:
			log.Printf("Warning: unsupported output format for %s, skipping.", out)
		}
	}

	for _, out := range visualizerTargets {
		exporters = append(exporters, export.NewVisualizerExporter(out, runID, RelativeDownloads(out, rawDownloads)))
	}

	return exporters
}

func RelativeDownloads(htmlPath string, downloads export.Downloads) export.Downloads {
	return export.Downloads{
		JSON: RelativeOutputPath(htmlPath, downloads.JSON),
		CSV:  RelativeOutputPath(htmlPath, downloads.CSV),
		XLSX: RelativeOutputPath(htmlPath, downloads.XLSX),
	}
}

func RelativeOutputPath(fromFile, toFile string) string {
	if toFile == "" {
		return ""
	}

	rel, err := filepath.Rel(filepath.Dir(fromFile), toFile)
	if err != nil {
		return filepath.ToSlash(toFile)
	}

	return filepath.ToSlash(rel)
}
