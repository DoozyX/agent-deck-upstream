package costs

import (
	"fmt"
	"time"
)

// CostEvent represents a single token usage and cost record.
type CostEvent struct {
	ID                   string
	SessionID            string
	ParentSessionID      string
	RunID                string
	Timestamp            time.Time
	Provider             string
	SourceKind           string
	SourceIdentity       string
	TranscriptIdentity   string
	Model                string
	InputTokens          int64
	OutputTokens         int64
	CacheReadTokens      int64
	CacheWriteTokens     int64
	CacheWrite5mTokens   int64
	CacheWrite1hTokens   int64
	ReasoningTokens      int64
	ProviderInputTokens  *int64
	CostMicrodollars     int64 // 1 USD = 1,000,000 microdollars
	PricingStatus        PricingStatus
	ReconciliationStatus ReconciliationStatus
}

// CostSummary aggregates cost data.
type CostSummary struct {
	TotalCostMicrodollars int64
	TotalInputTokens      int64
	TotalOutputTokens     int64
	TotalCacheReadTokens  int64
	TotalCacheWriteTokens int64
	EventCount            int
}

// SessionCost represents per-session cost totals.
type SessionCost struct {
	SessionID        string
	SessionTitle     string
	Group            string
	CostMicrodollars int64
	EventCount       int
}

// DailyCost represents cost for a single day.
type DailyCost struct {
	Date             time.Time
	CostMicrodollars int64
	Group            string
}

// FormatUSD converts microdollars to a display string.
func FormatUSD(microdollars int64) string {
	return fmt.Sprintf("$%.2f", float64(microdollars)/1_000_000)
}

// RemoteCostSummary mirrors `agent-deck costs summary --json` output. #1101:
// when an SSH remote is configured, the TUI fetches one of these per remote
// and folds the totals into the local cost-line totals so the status bar
// reflects spend across every host.
type RemoteCostSummary struct {
	CostTodayMicrodollars     int64    `json:"cost_today_microdollars"`
	CostYesterdayMicrodollars int64    `json:"cost_yesterday_microdollars"`
	CostThisWeekMicrodollars  int64    `json:"cost_this_week_microdollars"`
	CostLastWeekMicrodollars  int64    `json:"cost_last_week_microdollars"`
	CostThisMonthMicrodollars int64    `json:"cost_this_month_microdollars"`
	CostLastMonthMicrodollars int64    `json:"cost_last_month_microdollars"`
	CostProjectedMicrodollars int64    `json:"cost_projected_microdollars"`
	EventsToday               int      `json:"events_today"`
	EventsThisWeek            int      `json:"events_this_week"`
	EventsThisMonth           int      `json:"events_this_month"`
	CoverageKnown             bool     `json:"coverage_known"`
	CoverageComplete          bool     `json:"coverage_complete"`
	ProjectionComplete        bool     `json:"projection_complete"`
	TodayCoverage             Coverage `json:"today_coverage"`
	YesterdayCoverage         Coverage `json:"yesterday_coverage"`
	ThisWeekCoverage          Coverage `json:"this_week_coverage"`
	LastWeekCoverage          Coverage `json:"last_week_coverage"`
	ThisMonthCoverage         Coverage `json:"this_month_coverage"`
	LastMonthCoverage         Coverage `json:"last_month_coverage"`
	ProjectionCoverage        Coverage `json:"projection_coverage"`
	DateBasis                 string   `json:"date_basis,omitempty"`
	Timezone                  string   `json:"timezone,omitempty"`
}

// MergeRemoteCostSummaries sums per-remote summaries into a single aggregate.
// Used by the TUI to display a combined "local + all remotes" cost line.
func MergeRemoteCostSummaries(summaries map[string]*RemoteCostSummary) RemoteCostSummary {
	var out RemoteCostSummary
	seen := false
	coverageKnown := true
	coverageComplete := true
	projectionComplete := true
	for _, s := range summaries {
		if s == nil {
			coverageKnown = false
			coverageComplete = false
			projectionComplete = false
			continue
		}
		first := !seen
		seen = true
		coverageKnown = coverageKnown && s.CoverageKnown
		coverageComplete = coverageComplete && s.CoverageComplete
		projectionComplete = projectionComplete && s.ProjectionComplete
		todayCoverage := legacyRemoteCoverage(s.TodayCoverage, s.EventsToday, s.CoverageKnown)
		weekCoverage := legacyRemoteCoverage(s.ThisWeekCoverage, s.EventsThisWeek, s.CoverageKnown)
		monthCoverage := legacyRemoteCoverage(s.ThisMonthCoverage, s.EventsThisMonth, s.CoverageKnown)
		out.CostTodayMicrodollars += s.CostTodayMicrodollars
		out.CostYesterdayMicrodollars += s.CostYesterdayMicrodollars
		out.CostThisWeekMicrodollars += s.CostThisWeekMicrodollars
		out.CostLastWeekMicrodollars += s.CostLastWeekMicrodollars
		out.CostThisMonthMicrodollars += s.CostThisMonthMicrodollars
		out.CostLastMonthMicrodollars += s.CostLastMonthMicrodollars
		out.CostProjectedMicrodollars += s.CostProjectedMicrodollars
		out.EventsToday += s.EventsToday
		out.EventsThisWeek += s.EventsThisWeek
		out.EventsThisMonth += s.EventsThisMonth
		if first {
			out.TodayCoverage = todayCoverage
			out.YesterdayCoverage = s.YesterdayCoverage
			out.ThisWeekCoverage = weekCoverage
			out.LastWeekCoverage = s.LastWeekCoverage
			out.ThisMonthCoverage = monthCoverage
			out.LastMonthCoverage = s.LastMonthCoverage
			out.ProjectionCoverage = s.ProjectionCoverage
		} else {
			out.TodayCoverage = MergeCoverage(out.TodayCoverage, todayCoverage)
			out.YesterdayCoverage = MergeCoverage(out.YesterdayCoverage, s.YesterdayCoverage)
			out.ThisWeekCoverage = MergeCoverage(out.ThisWeekCoverage, weekCoverage)
			out.LastWeekCoverage = MergeCoverage(out.LastWeekCoverage, s.LastWeekCoverage)
			out.ThisMonthCoverage = MergeCoverage(out.ThisMonthCoverage, monthCoverage)
			out.LastMonthCoverage = MergeCoverage(out.LastMonthCoverage, s.LastMonthCoverage)
			out.ProjectionCoverage = MergeCoverage(out.ProjectionCoverage, s.ProjectionCoverage)
		}
	}
	out.CoverageKnown = seen && coverageKnown
	out.CoverageComplete = seen && coverageKnown && coverageComplete
	out.ProjectionComplete = seen && coverageKnown && projectionComplete
	if len(summaries) > 0 {
		out.DateBasis = "UTC calendar dates"
		out.Timezone = "UTC"
	}
	return out
}

func legacyRemoteCoverage(coverage Coverage, events int, known bool) Coverage {
	if !known && coverage.EventCount == 0 {
		coverage.EventCount = events
		coverage.CoverageKnown = false
		coverage.Complete = false
	}
	return coverage
}
