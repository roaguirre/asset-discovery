package app

import (
	"context"
	"fmt"
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
	"asset-discovery/internal/reconsider"
	"asset-discovery/internal/tracing/telemetry"
	"asset-discovery/internal/webhint"
)

const (
	VisualizerOutputPrefix     = "visualizer:"
	DefaultVisualizerOutput    = VisualizerOutputPrefix + DefaultVisualizerOutputDir
	DefaultVisualizerOutputDir = "exports/visualizer"
)

type Config struct {
	Outputs         []string
	OutputsChanged  bool
	RunID           string
	Now             func() time.Time
	Telemetry       telemetry.Provider
	DNSVariantSweep collect.DNSVariantSweepConfig
}

type Pipeline struct {
	engine    *dag.Engine
	outputs   []string
	runID     string
	telemetry telemetry.Provider
}

func NewPipeline(cfg Config) (*Pipeline, error) {
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

	exporters, err := BuildExporters(outputs, resolvedRunID)
	if err != nil {
		return nil, err
	}

	engine := &dag.Engine{
		Collectors: []dag.Collector{
			collect.NewDNSCollector(
				collect.WithDNSCollectorJudge(ownershipJudge),
				collect.WithDNSCollectorRDAPClient(dnsRDAPClient),
				collect.WithDNSCollectorVariantSweepConfig(cfg.DNSVariantSweep),
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
			collect.NewSitemapCollector(
				collect.WithSitemapClient(fastClient),
				collect.WithSitemapJudge(ownershipJudge),
			),
			collect.NewWebHintCollector(
				collect.WithWebHintClient(fastClient),
				collect.WithWebHintJudge(webHintJudge),
			),
		},
		Enrichers: []dag.Enricher{
			enrich.NewDomainEnricher(
				enrich.WithDomainEnricherRDAPClient(dnsRDAPClient),
			),
			enrich.NewIPEnricher(enrich.WithIPEnricherJudge(ownershipJudge)),
		},
		Reconsiderers: []dag.Reconsiderer{
			reconsider.NewDiscardedCandidateReconsiderer(
				reconsider.WithDiscardedCandidateReconsidererJudge(ownershipJudge),
			),
		},
		Filters: []dag.Filter{
			filter.NewMergeFilter(),
		},
		Exporters: exporters,
		RunID:     resolvedRunID,
		Telemetry: provider,
	}

	return &Pipeline{
		engine:    engine,
		outputs:   outputs,
		runID:     resolvedRunID,
		telemetry: provider,
	}, nil
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

func BuildExporters(outputTargets []string, runID string) ([]dag.Exporter, error) {
	var exporters []dag.Exporter
	var visualizerTargets []string

	rawDownloads := export.Downloads{}

	for _, out := range outputTargets {
		if dir, isVisualizer, err := ParseVisualizerOutputTarget(out); err != nil {
			return nil, err
		} else if isVisualizer {
			visualizerTargets = append(visualizerTargets, dir)
			continue
		}

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
		default:
			log.Printf("Warning: unsupported output format for %s, skipping.", out)
		}
	}

	for _, out := range visualizerTargets {
		exporters = append(exporters, export.NewVisualizerExporter(out, runID, RelativeDownloads(out, rawDownloads)))
	}

	return exporters, nil
}

func ParseVisualizerOutputTarget(target string) (string, bool, error) {
	trimmed := strings.TrimSpace(target)
	if strings.HasPrefix(trimmed, VisualizerOutputPrefix) {
		dir := strings.TrimSpace(strings.TrimPrefix(trimmed, VisualizerOutputPrefix))
		if dir == "" {
			return "", true, fmt.Errorf("visualizer output %q must include a directory path after %q", target, VisualizerOutputPrefix)
		}
		return dir, true, nil
	}

	if strings.EqualFold(filepath.Ext(trimmed), ".html") {
		base := strings.TrimSuffix(trimmed, filepath.Ext(trimmed))
		if strings.TrimSpace(base) == "" {
			base = DefaultVisualizerOutputDir
		}
		return "", false, fmt.Errorf("visualizer HTML output %q is no longer supported; use %q instead", target, VisualizerOutputPrefix+base)
	}

	return "", false, nil
}

func RelativeDownloads(visualizerDir string, downloads export.Downloads) export.Downloads {
	return export.Downloads{
		JSON: RelativeOutputPath(visualizerDir, downloads.JSON),
		CSV:  RelativeOutputPath(visualizerDir, downloads.CSV),
		XLSX: RelativeOutputPath(visualizerDir, downloads.XLSX),
	}
}

func RelativeOutputPath(fromDir, toFile string) string {
	if toFile == "" {
		return ""
	}

	rel, err := filepath.Rel(fromDir, toFile)
	if err != nil {
		return filepath.ToSlash(toFile)
	}

	return filepath.ToSlash(rel)
}
