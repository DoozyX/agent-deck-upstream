package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

const costsUsage = "Usage: agent-deck costs <sync|summary|recompute>"

func handleCosts(profile string, args []string) {
	if len(args) > 0 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		fmt.Println(costsUsage)
		return
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, costsUsage)
		os.Exit(1)
	}

	switch args[0] {
	case "sync":
		if helpRequested(args[1:]) || (len(args) == 2 && args[1] == "help") {
			fmt.Println("Usage: agent-deck costs sync")
			return
		}
		if len(args) != 1 {
			fmt.Fprintln(os.Stderr, "Usage: agent-deck costs sync")
			os.Exit(1)
		}
		handleCostsSync(profile)
	case "summary":
		handleCostsSummary(profile, args[1:])
	case "recompute":
		handleCostsRecompute(profile, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown costs subcommand: %s\n", args[0])
		fmt.Fprintln(os.Stderr, costsUsage)
		os.Exit(1)
	}
}

// openCostStore creates a cost store from the profile's database.
func openCostStore(profile string) (*costs.Store, *session.Storage) {
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to open storage: %v\n", err)
		os.Exit(1)
	}
	db := storage.GetDB()
	if db == nil {
		fmt.Fprintln(os.Stderr, "Error: database not available")
		os.Exit(1)
	}
	return costs.NewStore(db.DB()), storage
}

// newPricerFromConfig creates a Pricer using the user's config overrides.
func newPricerFromConfig() *costs.Pricer {
	cfg, _ := session.LoadUserConfig()
	return newPricerFromUserConfig(cfg)
}

func newPricerFromUserConfig(cfg *session.UserConfig) *costs.Pricer {
	pricerCfg := costs.PricerConfig{}
	if cfg != nil && len(cfg.Costs.Pricing.Overrides) > 0 {
		pricerCfg.Overrides = make(map[string]costs.PriceOverride)
		for model, ov := range cfg.Costs.Pricing.Overrides {
			pricerCfg.Overrides[model] = costs.PriceOverride{
				InputPerMtok:      ov.InputPerMtok,
				OutputPerMtok:     ov.OutputPerMtok,
				CacheReadPerMtok:  ov.CacheReadPerMtok,
				CacheWritePerMtok: ov.CacheWritePerMtok,
			}
		}
	}
	return costs.NewPricer(pricerCfg)
}

func handleCostsSync(profile string) {
	userConfig, err := session.LoadUserConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error: failed to load user config")
		os.Exit(1)
	}
	costStore, storage := openCostStore(profile)
	defer storage.Close()
	pricer := newPricerFromUserConfig(userConfig)

	instances, err := storage.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to load sessions: %v\n", err)
		os.Exit(1)
	}

	discoveryConfig := buildCostDiscoveryConfig(storage.Profile(), userConfig, instances)
	sources, warnings, err := costs.DiscoverTranscriptSources(discoveryConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to discover usage transcripts: %v\n", err)
		os.Exit(1)
	}
	if len(sources) == 0 {
		fmt.Println("No Claude or Codex transcripts found to sync.")
		printCostSyncWarnings(warnings)
		return
	}

	fmt.Printf("Syncing cost data from %d Claude/Codex transcript source(s)...\n", len(sources))
	result := costs.Sync(context.Background(), costStore, pricer, sources)
	result.Warnings = append(warnings, result.Warnings...)

	fmt.Printf("\nResults:\n")
	fmt.Printf("  Sources scanned:  %d\n", result.SourcesScanned)
	fmt.Printf("  Sources changed:  %d\n", result.SourcesChanged)
	fmt.Printf("  Events imported:  %d\n", result.EventsImported)
	fmt.Printf("  Events skipped:   %d (already tracked)\n", result.EventsSkipped)
	fmt.Printf("  Events reconciled:%d\n", result.EventsReconciled)
	printCostSyncWarnings(result.Warnings)
	if len(result.Errors) > 0 {
		fmt.Printf("  Errors:           %d\n", len(result.Errors))
		for _, e := range result.Errors {
			fmt.Printf("    - %s\n", e)
		}
		os.Exit(1)
	}
}

func buildCostDiscoveryConfig(profile string, cfg *session.UserConfig, instances []*session.Instance) costs.DiscoveryConfig {
	var config costs.DiscoveryConfig
	addHome := func(provider, path, account string, required bool) {
		if path == "" {
			return
		}
		config.Homes = append(config.Homes, costs.ProviderHome{Provider: provider, Path: path, Account: account, Required: required})
	}
	selectedCodexRequired := strings.TrimSpace(os.Getenv("CODEX_HOME")) != ""
	if cfg != nil {
		selectedCodexRequired = selectedCodexRequired || cfg.GetProfileCodexConfigDir(profile) != "" || strings.TrimSpace(cfg.Codex.ConfigDir) != ""
	}
	selectedClaudeHome := session.GetClaudeConfigDir()
	selectedCodexHome := session.GetCodexConfigDir()
	addHome(costs.ProviderClaude, selectedClaudeHome, profile, session.IsClaudeConfigDirExplicit())
	addHome(costs.ProviderCodex, selectedCodexHome, profile, selectedCodexRequired)
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		switch inst.Tool {
		case "claude":
			home := session.GetClaudeConfigDirForInstance(inst)
			addHome(costs.ProviderClaude, home, inst.Account, session.IsClaudeConfigDirExplicitForInstance(inst))
			if inst.ClaudeSessionID != "" {
				config.Attributions = append(config.Attributions, costs.TranscriptAttribution{
					Provider: costs.ProviderClaude, Home: home, NativeSessionID: inst.ClaudeSessionID,
					SessionID: inst.ID, ParentSessionID: inst.ParentSessionID, Archived: !inst.ArchivedAt.IsZero(),
				})
			}
		case "codex":
			home := session.GetCodexConfigDirForInstance(inst)
			addHome(costs.ProviderCodex, home, inst.Account, selectedCodexRequired || home != selectedCodexHome)
			if inst.CodexSessionID != "" {
				config.Attributions = append(config.Attributions, costs.TranscriptAttribution{
					Provider: costs.ProviderCodex, Home: home, NativeSessionID: inst.CodexSessionID,
					SessionID: inst.ID, ParentSessionID: inst.ParentSessionID, Archived: !inst.ArchivedAt.IsZero(),
				})
			}
		}
	}
	return config
}

func printCostSyncWarnings(warnings []costs.CoverageWarning) {
	if len(warnings) == 0 {
		return
	}
	fmt.Printf("  Warnings:         %d\n", len(warnings))
	for _, warning := range warnings {
		fmt.Printf("    - %s %s: %s\n", warning.Provider, warning.Source, warning.Message)
	}
}

func handleCostsSummary(profile string, args []string) {
	// #1101: --json output so a remote agent-deck can be queried over SSH and
	// its cost totals merged into the local TUI's status-line cost segment.
	fs := flag.NewFlagSet("costs summary", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	costStore, storage := openCostStore(profile)
	defer storage.Close()

	today, _ := costStore.CoveredTotalToday()
	yesterday, _ := costStore.CoveredTotalYesterday()
	week, _ := costStore.CoveredTotalThisWeek()
	lastWeek, _ := costStore.CoveredTotalLastWeek()
	month, _ := costStore.CoveredTotalThisMonth()
	lastMonth, _ := costStore.CoveredTotalLastMonth()
	projected, projectionCoverage, _ := costStore.CoveredProjectedMonthly()
	days, _ := costStore.CoveredCostByDay()
	providers, _ := costStore.CoveredCostByProvider()
	models, _ := costStore.CoveredCostByModel()
	sessions, _ := costStore.CoveredCostBySession()
	runs, _ := costStore.CoveredCostByRun()
	allCoverage := costs.MergeCoverage(today.Coverage, yesterday.Coverage, week.Coverage, lastWeek.Coverage, month.Coverage, lastMonth.Coverage)

	if *jsonOutput {
		// Wire shape mirrors costs.RemoteCostSummary so SSHRunner can json.Unmarshal directly.
		remote := costs.RemoteCostSummary{
			CostTodayMicrodollars: today.TotalCostMicrodollars, CostYesterdayMicrodollars: yesterday.TotalCostMicrodollars,
			CostThisWeekMicrodollars: week.TotalCostMicrodollars, CostLastWeekMicrodollars: lastWeek.TotalCostMicrodollars,
			CostThisMonthMicrodollars: month.TotalCostMicrodollars, CostLastMonthMicrodollars: lastMonth.TotalCostMicrodollars,
			CostProjectedMicrodollars: projected, EventsToday: today.EventCount, EventsThisWeek: week.EventCount, EventsThisMonth: month.EventCount,
			CoverageKnown: allCoverage.CoverageKnown, CoverageComplete: allCoverage.Complete,
			ProjectionComplete: projectionCoverage.CoverageKnown && projectionCoverage.Complete,
			TodayCoverage:      today.Coverage, YesterdayCoverage: yesterday.Coverage, ThisWeekCoverage: week.Coverage,
			LastWeekCoverage: lastWeek.Coverage, ThisMonthCoverage: month.Coverage, LastMonthCoverage: lastMonth.Coverage,
			ProjectionCoverage: projectionCoverage, DateBasis: "UTC calendar dates", Timezone: "UTC",
		}
		payload := struct {
			costs.RemoteCostSummary
			Days      []costs.CostBreakdown `json:"days"`
			Providers []costs.CostBreakdown `json:"providers"`
			Models    []costs.CostBreakdown `json:"models"`
			Sessions  []costs.CostBreakdown `json:"sessions"`
			Runs      []costs.CostBreakdown `json:"runs"`
		}{remote, days, providers, models, sessions, runs}
		enc := json.NewEncoder(os.Stdout)
		_ = enc.Encode(payload)
		return
	}

	fmt.Printf("Cost Summary (UTC calendar dates):\n")
	printCoveredSummary("Today", today)
	printCoveredSummary("This week", week)
	printCoveredSummary("This month", month)
	projectionSummary := costs.CoveredSummary{CostSummary: costs.CostSummary{TotalCostMicrodollars: projected}, Coverage: projectionCoverage}
	projectionStatus := costs.CostCoverageStatus(projectionSummary)
	if projectionStatus == "price unknown" || projectionStatus == "coverage unknown" {
		fmt.Printf("  Projected:  %s (incomplete)\n", projectionStatus)
	} else if projectionCoverage.Complete {
		fmt.Printf("  Projected:  %s/mo (%s)\n", costs.FormatUSD(projected), projectionStatus)
	} else {
		fmt.Printf("  Projected:  %s known subtotal/mo (incomplete)\n", costs.FormatUSD(projected))
	}

	printCostBreakdowns("By day", days)
	printCostBreakdowns("By provider", providers)
	printCostBreakdowns("By model", models)
	printCostBreakdowns("By session", sessions)
	printCostBreakdowns("By run", runs)
}

func printCoveredSummary(label string, summary costs.CoveredSummary) {
	status := costs.CostCoverageStatus(summary)
	value := costs.FormatUSD(summary.TotalCostMicrodollars)
	if status == "price unknown" || status == "coverage unknown" {
		value = status
	} else if status != "complete" {
		value += " (" + status + ")"
	}
	fmt.Printf("  %-10s %s (%d events)\n", label+":", value, summary.EventCount)
	if detail := costs.CoverageDetail(summary.Coverage); detail != "" {
		fmt.Printf("               %s\n", detail)
	}
}

func printCostBreakdowns(title string, items []costs.CostBreakdown) {
	if len(items) == 0 {
		return
	}
	fmt.Printf("\n%s:\n", title)
	for _, item := range items {
		summary := costs.CoveredSummary{CostSummary: costs.CostSummary{TotalCostMicrodollars: item.KnownCostMicrodollars, EventCount: item.Coverage.EventCount}, Coverage: item.Coverage}
		status := costs.CostCoverageStatus(summary)
		amount := costs.FormatUSD(item.KnownCostMicrodollars)
		if status == "price unknown" || status == "coverage unknown" {
			amount = status
		} else if status != "complete" {
			amount += " " + status
		}
		fmt.Printf("  %-24s %s | input %d cache-read %d cache-write %d (5m %d, 1h %d) output %d (reasoning subset %d)",
			item.Key, amount, item.UncachedInputTokens, item.CacheReadInputTokens, item.CacheWriteInputTokens,
			item.CacheWrite5mInputTokens, item.CacheWrite1hInputTokens, item.OutputTokens, item.ReasoningOutputTokens)
		if detail := costs.CoverageDetail(item.Coverage); detail != "" {
			fmt.Printf(" | %s", detail)
		}
		fmt.Println()
	}
}

func handleCostsRecompute(profile string, args []string) {
	dryRun := false
	for _, a := range args {
		switch a {
		case "--dry-run", "-n":
			dryRun = true
		case "-h", "--help":
			fmt.Println("Usage: agent-deck costs recompute [--dry-run]")
			fmt.Println("\nRecalculate cost_microdollars for every cost_events row using current")
			fmt.Println("pricing data (defaults + user overrides). For unknown models, the prior amount is preserved")
			fmt.Println("while any stale known status is demoted to unknown. Idempotent.")
			return
		default:
			fmt.Fprintf(os.Stderr, "Unknown flag: %s\n", a)
			fmt.Fprintln(os.Stderr, "Usage: agent-deck costs recompute [--dry-run]")
			os.Exit(1)
		}
	}

	userConfig, err := session.LoadUserConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error: failed to load user config")
		os.Exit(1)
	}
	costStore, storage := openCostStore(profile)
	defer storage.Close()
	pricer := newPricerFromUserConfig(userConfig)

	if dryRun {
		fmt.Println("Recomputing cost_events (dry-run, no rows will be modified)...")
	} else {
		fmt.Println("Recomputing cost_events...")
	}

	updated, skipped, err := costs.Recompute(context.Background(), costStore, pricer, dryRun)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\nResults:\n")
	if dryRun {
		fmt.Printf("  Would update: %d\n", updated)
	} else {
		fmt.Printf("  Updated:      %d\n", updated)
	}
	fmt.Printf("  Skipped:      %d (already correct or unknown model)\n", skipped)
	if dryRun && updated > 0 {
		fmt.Println("\nRe-run without --dry-run to apply changes.")
	}
}
