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
	"asset-discovery/internal/expand"
	"asset-discovery/internal/export"
	"asset-discovery/internal/filter"
	"asset-discovery/internal/models"
	"asset-discovery/internal/ownership"
	"asset-discovery/internal/reconsider"
	"asset-discovery/internal/search"
	"asset-discovery/internal/tracing/telemetry"
	"asset-discovery/internal/webhint"
)

const (
	legacyVisualizerOutputPrefix = "visualizer:"
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
	outputs, resolvedRunID := ResolveOutputTargets(
		cfg.Outputs,
		cfg.OutputsChanged,
		runID,
		now,
	)

	provider := cfg.Telemetry
	if provider == nil {
		provider = telemetry.Noop()
	}

	fastClient := &http.Client{Timeout: 20 * time.Second}
	standardClient := &http.Client{Timeout: 30 * time.Second}
	archiveClient := &http.Client{Timeout: 60 * time.Second}
	dnsRDAPClient := &http.Client{Timeout: 10 * time.Second}
	searchClient := &http.Client{Timeout: 45 * time.Second}

	ownershipJudge := ownership.NewDefaultJudge()
	webHintJudge := webhint.NewDefaultJudge()
	searchProvider, err := search.NewProviderFromEnv(
		search.WithOpenAIClient(searchClient),
	)
	if err != nil {
		log.Printf("AI search provider disabled: %v", err)
	}

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
		Expanders: []dag.Expander{
			expand.NewAISearchCollector(
				expand.WithAISearchProvider(searchProvider),
				expand.WithAISearchJudge(ownershipJudge),
			),
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

func NewPipelineWithEngine(engine *dag.Engine, runID string, outputs []string, provider telemetry.Provider) *Pipeline {
	if provider == nil {
		provider = telemetry.Noop()
	}
	if engine == nil {
		engine = &dag.Engine{}
	}
	engine.RunID = runID
	engine.Telemetry = provider

	return &Pipeline{
		engine:    engine,
		outputs:   append([]string(nil), outputs...),
		runID:     runID,
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

func (p *Pipeline) Resume(
	ctx context.Context,
	pCtx *models.PipelineContext,
	progress *dag.RunProgress,
	callbacks dag.ResumeCallbacks,
) (*models.PipelineContext, error) {
	ctx = telemetry.WithProvider(ctx, p.telemetry)

	if len(p.outputs) > 0 {
		telemetry.Infof(ctx, "Export run %s will write to: %s", p.runID, strings.Join(p.outputs, ", "))
	}

	return p.engine.Resume(ctx, pCtx, progress, callbacks)
}

func (p *Pipeline) Outputs() []string {
	return append([]string(nil), p.outputs...)
}

func (p *Pipeline) RunID() string {
	return p.runID
}

func ResolveOutputTargets(
	requested []string,
	outputsChanged bool,
	requestedRunID string,
	now time.Time,
) ([]string, string) {
	runID := strings.TrimSpace(requestedRunID)
	if runID == "" {
		runID = BuildRunID(now)
	}
	if outputsChanged {
		return append([]string(nil), requested...), runID
	}

	baseDir := filepath.Join("exports", "runs", runID)
	return []string{
		filepath.Join(baseDir, "results.json"),
		filepath.Join(baseDir, "results.csv"),
		filepath.Join(baseDir, "results.xlsx"),
	}, runID
}

func BuildRunID(now time.Time) string {
	return now.Format("20060102T150405.000000000-0700")
}

func BuildExporters(outputTargets []string, _ string) ([]dag.Exporter, error) {
	var exporters []dag.Exporter

	for _, out := range outputTargets {
		if err := validateOutputTarget(out); err != nil {
			return nil, err
		}

		switch strings.ToLower(filepath.Ext(out)) {
		case ".json":
			exporters = append(exporters, export.NewJSONExporter(out))
		case ".csv":
			exporters = append(exporters, export.NewCSVExporter(out))
		case ".xlsx":
			exporters = append(exporters, export.NewXLSXExporter(out))
		default:
			log.Printf("Warning: unsupported output format for %s, skipping.", out)
		}
	}
	return exporters, nil
}

func validateOutputTarget(target string) error {
	trimmed := strings.TrimSpace(target)
	if strings.HasPrefix(trimmed, legacyVisualizerOutputPrefix) {
		return fmt.Errorf(
			"visualizer output %q is no longer supported; specify JSON, CSV, or XLSX file paths only",
			target,
		)
	}

	if strings.EqualFold(filepath.Ext(trimmed), ".html") {
		return fmt.Errorf(
			"visualizer HTML output %q is no longer supported; specify JSON, CSV, or XLSX file paths only",
			target,
		)
	}

	return nil
}
