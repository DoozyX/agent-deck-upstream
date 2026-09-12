package web

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/logging"
)

func (s *Server) handleCostsSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	if s.costStore == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "cost tracking not enabled")
		return
	}

	today, err := s.costStore.CoveredTotalToday()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to query today costs")
		return
	}
	week, err := s.costStore.CoveredTotalThisWeek()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to query week costs")
		return
	}
	month, err := s.costStore.CoveredTotalThisMonth()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to query month costs")
		return
	}
	projected, projectionCoverage, err := s.costStore.CoveredProjectedMonthly()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to calculate projection")
		return
	}

	days, _ := s.costStore.CoveredCostByDay()
	providers, _ := s.costStore.CoveredCostByProvider()
	models, _ := s.costStore.CoveredCostByModel()
	sessions, _ := s.costStore.CoveredCostBySession()
	runs, _ := s.costStore.CoveredCostByRun()
	writeJSON(w, http.StatusOK, map[string]any{
		"today_usd":           microToUSD(today.TotalCostMicrodollars),
		"week_usd":            microToUSD(week.TotalCostMicrodollars),
		"month_usd":           microToUSD(month.TotalCostMicrodollars),
		"projected_usd":       microToUSD(projected),
		"today_events":        today.EventCount,
		"week_events":         week.EventCount,
		"month_events":        month.EventCount,
		"today_coverage":      today.Coverage,
		"week_coverage":       week.Coverage,
		"month_coverage":      month.Coverage,
		"projection_coverage": projectionCoverage,
		"projection_complete": projectionCoverage.CoverageKnown && projectionCoverage.Complete,
		"date_basis":          "UTC calendar dates",
		"timezone":            "UTC",
		"days":                days,
		"providers":           providers,
		"models":              models,
		"sessions":            sessions,
		"runs":                runs,
	})
}

func (s *Server) handleCostsDaily(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	if s.costStore == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "cost tracking not enabled")
		return
	}

	days := 30
	if d := r.URL.Query().Get("days"); d != "" {
		if v, err := strconv.Atoi(d); err == nil && v > 0 && v <= 365 {
			days = v
		}
	}

	now := time.Now().UTC()
	from := now.AddDate(0, 0, -days).Truncate(24 * time.Hour)
	to := now.AddDate(0, 0, 1).Truncate(24 * time.Hour)

	dailyCosts, err := s.costStore.CoveredCostByDay()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to query daily costs")
		return
	}

	type dailyEntry struct {
		Date                    string         `json:"date"`
		CostUSD                 float64        `json:"cost_usd"`
		Coverage                costs.Coverage `json:"coverage"`
		UncachedInputTokens     int64          `json:"uncached_input_tokens"`
		CacheReadInputTokens    int64          `json:"cache_read_input_tokens"`
		CacheWriteInputTokens   int64          `json:"cache_write_input_tokens"`
		CacheWrite5mInputTokens int64          `json:"cache_write_5m_input_tokens"`
		CacheWrite1hInputTokens int64          `json:"cache_write_1h_input_tokens"`
		OutputTokens            int64          `json:"output_tokens"`
		ReasoningOutputTokens   int64          `json:"reasoning_output_tokens"`
	}

	result := make([]dailyEntry, 0, len(dailyCosts))
	for _, dc := range dailyCosts {
		date, parseErr := time.Parse("2006-01-02", dc.Key)
		if parseErr != nil || date.Before(from) || !date.Before(to) {
			continue
		}
		result = append(result, dailyEntry{
			Date: dc.Key, CostUSD: microToUSD(dc.KnownCostMicrodollars), Coverage: dc.Coverage,
			UncachedInputTokens: dc.UncachedInputTokens, CacheReadInputTokens: dc.CacheReadInputTokens,
			CacheWriteInputTokens: dc.CacheWriteInputTokens, CacheWrite5mInputTokens: dc.CacheWrite5mInputTokens,
			CacheWrite1hInputTokens: dc.CacheWrite1hInputTokens, OutputTokens: dc.OutputTokens, ReasoningOutputTokens: dc.ReasoningOutputTokens,
		})
	}

	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleCostsSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	if s.costStore == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "cost tracking not enabled")
		return
	}

	sessions, err := s.costStore.CoveredTopSessionsByCost(100)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to query session costs")
		return
	}

	type sessionEntry struct {
		SessionID string         `json:"session_id"`
		Title     string         `json:"title"`
		Group     string         `json:"group"`
		CostUSD   float64        `json:"cost_usd"`
		Events    int            `json:"events"`
		Coverage  costs.Coverage `json:"coverage"`
	}

	result := make([]sessionEntry, 0, len(sessions))
	for _, sc := range sessions {
		covered, queryErr := s.costStore.CoveredTotalBySession(sc.SessionID)
		if queryErr != nil {
			continue
		}
		result = append(result, sessionEntry{
			SessionID: sc.SessionID,
			Title:     sc.SessionTitle,
			Group:     sc.Group,
			CostUSD:   microToUSD(covered.TotalCostMicrodollars),
			Events:    covered.EventCount,
			Coverage:  covered.Coverage,
		})
	}

	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleCostsModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	if s.costStore == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "cost tracking not enabled")
		return
	}

	models, err := s.costStore.CoveredCostByModel()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to query model costs")
		return
	}

	legacy := make(map[string]float64, len(models))
	for _, model := range models {
		legacy[model.Key] = microToUSD(model.KnownCostMicrodollars)
	}
	if r.URL.Query().Get("coverage") != "1" {
		writeJSON(w, http.StatusOK, legacy)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"costs": legacy, "breakdowns": models, "date_basis": "UTC calendar dates", "timezone": "UTC"})
}

func (s *Server) handleCostsExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	if s.costStore == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "cost tracking not enabled")
		return
	}

	format := r.URL.Query().Get("format")
	if format == "" {
		format = "json"
	}
	if format != "csv" && format != "json" {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "format must be csv or json")
		return
	}

	now := time.Now().UTC()
	var from, to time.Time

	fromStr := r.URL.Query().Get("from")
	toStr := r.URL.Query().Get("to")

	if fromStr != "" && toStr != "" {
		var err error
		from, err = time.Parse("2006-01-02", fromStr)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid from date format, use YYYY-MM-DD")
			return
		}
		to, err = time.Parse("2006-01-02", toStr)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid to date format, use YYYY-MM-DD")
			return
		}
		to = to.AddDate(0, 0, 1) // include the end date
	} else {
		days := 30
		if d := r.URL.Query().Get("days"); d != "" {
			if v, err := strconv.Atoi(d); err == nil && v > 0 && v <= 365 {
				days = v
			}
		}
		from = now.AddDate(0, 0, -days).Truncate(24 * time.Hour)
		to = now.AddDate(0, 0, 1).Truncate(24 * time.Hour)
	}

	events, err := s.costStore.EventsByDateRange(from, to)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to query daily costs")
		return
	}

	entries := make([]costExportRow, 0, len(events))
	for _, event := range events {
		entries = append(entries, newCostExportRow(event))
	}

	if format == "csv" {
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", "attachment; filename=costs.csv")
		w.WriteHeader(http.StatusOK)

		cw := csv.NewWriter(w)
		_ = cw.Write(costExportCSVHeader)
		for _, e := range entries {
			_ = cw.Write(e.csvRecord())
		}
		cw.Flush()
		return
	}

	w.Header().Set("Content-Disposition", "attachment; filename=costs.json")
	writeJSON(w, http.StatusOK, entries)
}

type costExportRow struct {
	Date                      string                     `json:"date"`
	Timestamp                 string                     `json:"timestamp"`
	Timezone                  string                     `json:"timezone"`
	DateBasis                 string                     `json:"date_basis"`
	Provider                  string                     `json:"provider"`
	SourceKind                string                     `json:"source_kind"`
	SourceIdentity            string                     `json:"source_identity"`
	TranscriptIdentity        string                     `json:"transcript_identity"`
	SessionID                 string                     `json:"session_id"`
	ParentSessionID           string                     `json:"parent_session_id"`
	RunID                     string                     `json:"run_id"`
	Attribution               string                     `json:"attribution"`
	Model                     string                     `json:"model"`
	UncachedInputTokens       int64                      `json:"uncached_input_tokens"`
	CacheReadInputTokens      int64                      `json:"cache_read_input_tokens"`
	CacheWriteInputTokens     int64                      `json:"cache_write_input_tokens"`
	CacheWrite5mInputTokens   int64                      `json:"cache_write_5m_input_tokens"`
	CacheWrite1hInputTokens   int64                      `json:"cache_write_1h_input_tokens"`
	CacheWriteDurationUnknown int64                      `json:"cache_write_duration_unknown_input_tokens"`
	OutputTokens              int64                      `json:"output_tokens"`
	ReasoningOutputTokens     int64                      `json:"reasoning_output_tokens"`
	CostUSD                   float64                    `json:"cost_usd"`
	KnownCostUSD              float64                    `json:"known_cost_usd"`
	PricingStatus             costs.PricingStatus        `json:"pricing_status"`
	ReconciliationStatus      costs.ReconciliationStatus `json:"reconciliation_status"`
}

func newCostExportRow(event costs.CostEvent) costExportRow {
	knownMicro := int64(0)
	if event.PricingStatus == costs.PricingKnown || event.PricingStatus == costs.PricingKnownZero {
		knownMicro = event.CostMicrodollars
	}
	residual := event.CacheWriteTokens - event.CacheWrite5mTokens - event.CacheWrite1hTokens
	if residual < 0 {
		residual = 0
	}
	attribution := "unassigned"
	if event.SessionID != "" && event.SessionID != costs.UnassignedSessionID {
		attribution = "session"
	}
	return costExportRow{
		Date: event.Timestamp.UTC().Format("2006-01-02"), Timestamp: event.Timestamp.UTC().Format(time.RFC3339Nano),
		Timezone: "UTC", DateBasis: "UTC calendar dates", Provider: event.Provider, SourceKind: event.SourceKind,
		SourceIdentity: event.SourceIdentity, TranscriptIdentity: event.TranscriptIdentity, SessionID: event.SessionID,
		ParentSessionID: event.ParentSessionID, RunID: event.RunID, Attribution: attribution, Model: event.Model,
		UncachedInputTokens: event.InputTokens, CacheReadInputTokens: event.CacheReadTokens, CacheWriteInputTokens: event.CacheWriteTokens,
		CacheWrite5mInputTokens: event.CacheWrite5mTokens, CacheWrite1hInputTokens: event.CacheWrite1hTokens,
		CacheWriteDurationUnknown: residual, OutputTokens: event.OutputTokens, ReasoningOutputTokens: event.ReasoningTokens,
		CostUSD: microToUSD(knownMicro), KnownCostUSD: microToUSD(knownMicro), PricingStatus: event.PricingStatus,
		ReconciliationStatus: event.ReconciliationStatus,
	}
}

var costExportCSVHeader = []string{
	"date", "timestamp", "timezone", "date_basis", "provider", "source_kind", "source_identity", "transcript_identity",
	"session_id", "parent_session_id", "run_id", "attribution", "model", "uncached_input_tokens", "cache_read_input_tokens",
	"cache_write_input_tokens", "cache_write_5m_input_tokens", "cache_write_1h_input_tokens", "cache_write_duration_unknown_input_tokens",
	"output_tokens", "reasoning_output_tokens", "cost_usd", "known_cost_usd", "pricing_status", "reconciliation_status",
}

func (e costExportRow) csvRecord() []string {
	return []string{
		e.Date, e.Timestamp, e.Timezone, e.DateBasis, e.Provider, e.SourceKind, e.SourceIdentity, e.TranscriptIdentity,
		e.SessionID, e.ParentSessionID, e.RunID, e.Attribution, e.Model, strconv.FormatInt(e.UncachedInputTokens, 10),
		strconv.FormatInt(e.CacheReadInputTokens, 10), strconv.FormatInt(e.CacheWriteInputTokens, 10),
		strconv.FormatInt(e.CacheWrite5mInputTokens, 10), strconv.FormatInt(e.CacheWrite1hInputTokens, 10),
		strconv.FormatInt(e.CacheWriteDurationUnknown, 10), strconv.FormatInt(e.OutputTokens, 10), strconv.FormatInt(e.ReasoningOutputTokens, 10),
		fmt.Sprintf("%.6f", e.CostUSD), fmt.Sprintf("%.6f", e.KnownCostUSD), string(e.PricingStatus), string(e.ReconciliationStatus),
	}
}

var (
	costStreamPollInterval      = 5 * time.Second
	costStreamHeartbeatInterval = 30 * time.Second
)

func (s *Server) handleCostsStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	if s.costStore == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "cost tracking not enabled")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "stream unavailable")
		return
	}

	summary, err := s.buildCostSummary()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load cost data")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	lastToday := summary.TodayMicro
	lastWeek := summary.WeekMicro
	lastMonth := summary.MonthMicro
	lastCoverage := [3]costs.Coverage{summary.TodayCoverage, summary.WeekCoverage, summary.MonthCoverage}

	if err := writeSSEEvent(w, flusher, "cost_summary", summary); err != nil {
		return
	}

	pollTicker := time.NewTicker(costStreamPollInterval)
	defer pollTicker.Stop()

	heartbeatTicker := time.NewTicker(costStreamHeartbeatInterval)
	defer heartbeatTicker.Stop()

	ctx := r.Context()
	emitIfChanged := func() error {
		next, err := s.buildCostSummary()
		if err != nil {
			logging.ForComponent(logging.CompWeb).Error("cost_stream_refresh_failed",
				slog.String("error", err.Error()))
			return nil
		}
		nextCoverage := [3]costs.Coverage{next.TodayCoverage, next.WeekCoverage, next.MonthCoverage}
		if next.TodayMicro == lastToday && next.WeekMicro == lastWeek && next.MonthMicro == lastMonth && nextCoverage == lastCoverage {
			return nil
		}
		if err := writeSSEEvent(w, flusher, "cost_summary", next); err != nil {
			return err
		}
		lastToday = next.TodayMicro
		lastWeek = next.WeekMicro
		lastMonth = next.MonthMicro
		lastCoverage = nextCoverage
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeatTicker.C:
			if err := writeSSEComment(w, flusher, "keepalive"); err != nil {
				return
			}
		case <-pollTicker.C:
			if err := emitIfChanged(); err != nil {
				return
			}
		}
	}
}

type costSummarySSE struct {
	TodayUSD      float64        `json:"today_usd"`
	WeekUSD       float64        `json:"week_usd"`
	MonthUSD      float64        `json:"month_usd"`
	TodayMicro    int64          `json:"-"`
	WeekMicro     int64          `json:"-"`
	MonthMicro    int64          `json:"-"`
	TodayCoverage costs.Coverage `json:"today_coverage"`
	WeekCoverage  costs.Coverage `json:"week_coverage"`
	MonthCoverage costs.Coverage `json:"month_coverage"`
}

func (s *Server) buildCostSummary() (*costSummarySSE, error) {
	today, err := s.costStore.CoveredTotalToday()
	if err != nil {
		return nil, err
	}
	week, err := s.costStore.CoveredTotalThisWeek()
	if err != nil {
		return nil, err
	}
	month, err := s.costStore.CoveredTotalThisMonth()
	if err != nil {
		return nil, err
	}
	return &costSummarySSE{
		TodayUSD:      microToUSD(today.TotalCostMicrodollars),
		WeekUSD:       microToUSD(week.TotalCostMicrodollars),
		MonthUSD:      microToUSD(month.TotalCostMicrodollars),
		TodayMicro:    today.TotalCostMicrodollars,
		WeekMicro:     week.TotalCostMicrodollars,
		MonthMicro:    month.TotalCostMicrodollars,
		TodayCoverage: today.Coverage, WeekCoverage: week.Coverage, MonthCoverage: month.Coverage,
	}, nil
}

// MarshalJSON implements custom JSON serialization for costSummarySSE,
// excluding the internal micro fields.
func (c costSummarySSE) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		TodayUSD      float64        `json:"today_usd"`
		WeekUSD       float64        `json:"week_usd"`
		MonthUSD      float64        `json:"month_usd"`
		TodayCoverage costs.Coverage `json:"today_coverage"`
		WeekCoverage  costs.Coverage `json:"week_coverage"`
		MonthCoverage costs.Coverage `json:"month_coverage"`
	}{
		TodayUSD:      c.TodayUSD,
		WeekUSD:       c.WeekUSD,
		MonthUSD:      c.MonthUSD,
		TodayCoverage: c.TodayCoverage, WeekCoverage: c.WeekCoverage, MonthCoverage: c.MonthCoverage,
	})
}

func (s *Server) handleCostsGroups(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	if s.costStore == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "cost tracking not enabled")
		return
	}

	sessions, err := s.costStore.CoveredTopSessionsByCost(1000)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to query costs")
		return
	}

	type groupEntry struct {
		Group    string         `json:"group"`
		CostUSD  float64        `json:"cost_usd"`
		Events   int            `json:"events"`
		Sessions int            `json:"sessions"`
		Coverage costs.Coverage `json:"coverage"`
	}

	groups := make(map[string]*groupEntry)
	for _, sc := range sessions {
		covered, queryErr := s.costStore.CoveredTotalBySession(sc.SessionID)
		if queryErr != nil {
			continue
		}
		g := sc.Group
		if g == "" {
			g = "(ungrouped)"
		}
		entry, ok := groups[g]
		if !ok {
			entry = &groupEntry{Group: g, Coverage: covered.Coverage}
			groups[g] = entry
		} else {
			entry.Coverage = costs.MergeCoverage(entry.Coverage, covered.Coverage)
		}
		entry.CostUSD += microToUSD(covered.TotalCostMicrodollars)
		entry.Events += covered.EventCount
		entry.Sessions++
	}

	result := make([]groupEntry, 0, len(groups))
	for _, entry := range groups {
		result = append(result, *entry)
	}

	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleCostsSessionDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	if s.costStore == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "cost tracking not enabled")
		return
	}

	sessionID := r.URL.Query().Get("id")
	if sessionID == "" {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "missing id parameter")
		return
	}

	summary, err := s.costStore.CoveredTotalBySession(sessionID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to query session")
		return
	}

	// Get daily breakdown for this session
	now := time.Now().UTC()
	from := now.AddDate(0, 0, -30).Truncate(24 * time.Hour)
	to := now.AddDate(0, 0, 1).Truncate(24 * time.Hour)
	daily, err := s.costStore.CoveredCostByDayForSession(sessionID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to query session daily costs")
		return
	}

	// Get model breakdown for this session
	models, err := s.costStore.CoveredCostByModelForSession(sessionID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to query session model costs")
		return
	}

	type dailyEntry struct {
		Date     string         `json:"date"`
		CostUSD  float64        `json:"cost_usd"`
		Coverage costs.Coverage `json:"coverage"`
	}
	dailyResult := make([]dailyEntry, 0, len(daily))
	for _, dc := range daily {
		date, parseErr := time.Parse("2006-01-02", dc.Key)
		if parseErr != nil || date.Before(from) || !date.Before(to) {
			continue
		}
		dailyResult = append(dailyResult, dailyEntry{
			Date: dc.Key, CostUSD: microToUSD(dc.KnownCostMicrodollars), Coverage: dc.Coverage,
		})
	}

	modelResult := make(map[string]float64, len(models))
	for _, model := range models {
		modelResult[model.Key] = microToUSD(model.KnownCostMicrodollars)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"session_id":       sessionID,
		"cost_usd":         microToUSD(summary.TotalCostMicrodollars),
		"input_tokens":     summary.TotalInputTokens,
		"output_tokens":    summary.TotalOutputTokens,
		"cache_read":       summary.TotalCacheReadTokens,
		"cache_write":      summary.TotalCacheWriteTokens,
		"events":           summary.EventCount,
		"coverage":         summary.Coverage,
		"daily":            dailyResult,
		"models":           modelResult,
		"model_breakdowns": models,
		"date_basis":       "UTC calendar dates",
		"timezone":         "UTC",
	})
}

// handleCostsBatch dispatches GET /api/costs/batch and POST /api/costs/batch.
// PERF-I: the POST form takes a JSON body {"ids":["sess1","sess2",...]}
// so large batches do not hit the 414 URI Too Long limit that the GET
// query-string form triggers once the sidebar grows past ~50 sessions.
// The GET form stays in place for one release as a backward-compat safety
// net; both forms reuse the same body-computation helper.
func (s *Server) handleCostsBatch(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleCostsBatchGET(w, r)
	case http.MethodPost:
		s.handleCostsBatchPOST(w, r)
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
	}
}

func (s *Server) handleCostsBatchGET(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	if s.costStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{"costs": map[string]float64{}, "coverage": map[string]costs.Coverage{}})
		return
	}

	rawIDs := r.URL.Query().Get("ids")
	if rawIDs == "" {
		writeJSON(w, http.StatusOK, map[string]any{"costs": map[string]float64{}, "coverage": map[string]costs.Coverage{}})
		return
	}

	ids := strings.Split(rawIDs, ",")
	amounts, coverage := s.computeBatchCosts(ids)
	writeJSON(w, http.StatusOK, map[string]any{"costs": amounts, "coverage": coverage})
}

// handleCostsBatchPOST is the PERF-I successor to the GET handler. Accepts
// { "ids": ["sess1", "sess2", ...] } in the JSON body to avoid 414 URI
// Too Long when many sessions are queried. The response shape is
// identical to the GET variant.
func (s *Server) handleCostsBatchPOST(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	if s.costStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{"costs": map[string]float64{}, "coverage": map[string]costs.Coverage{}})
		return
	}

	var req struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid json body")
		return
	}
	if len(req.IDs) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"costs": map[string]float64{}, "coverage": map[string]costs.Coverage{}})
		return
	}
	amounts, coverage := s.computeBatchCosts(req.IDs)
	writeJSON(w, http.StatusOK, map[string]any{"costs": amounts, "coverage": coverage})
}

func (s *Server) computeBatchCosts(ids []string) (map[string]float64, map[string]costs.Coverage) {
	const maxBatch = 200
	if len(ids) > maxBatch {
		ids = ids[:maxBatch]
	}
	sessionCosts := make(map[string]float64, len(ids))
	coverage := make(map[string]costs.Coverage, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		summary, err := s.costStore.CoveredTotalBySession(id)
		if err != nil {
			continue
		}
		sessionCosts[id] = microToUSD(summary.TotalCostMicrodollars)
		coverage[id] = summary.Coverage
	}
	return sessionCosts, coverage
}

func microToUSD(microdollars int64) float64 {
	return float64(microdollars) / 1_000_000
}
