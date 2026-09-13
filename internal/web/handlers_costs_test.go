package web

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

func writeCoverageWebFixtures(t *testing.T, store *costs.Store) {
	t.Helper()
	events := []costs.CostEvent{
		{ID: "known", SessionID: "session-a", ParentSessionID: "parent", RunID: "run-a", Timestamp: time.Now().UTC(), Provider: costs.ProviderClaude, SourceKind: costs.SourceKindClaudeDirect, SourceIdentity: "claude:msg:known", TranscriptIdentity: "claude:session", Model: "known-model", InputTokens: 10, CacheReadTokens: 20, CacheWriteTokens: 30, CacheWrite5mTokens: 11, CacheWrite1hTokens: 13, OutputTokens: 40, ReasoningTokens: 7, CostMicrodollars: 1_250_000, PricingStatus: costs.PricingKnown, ReconciliationStatus: costs.ReconciliationAuthoritative},
		{ID: "unknown", SessionID: "session-b", RunID: "run-b", Timestamp: time.Now().UTC(), Provider: costs.ProviderCodex, SourceKind: costs.SourceKindCodexRollout, SourceIdentity: "codex:event:unknown", TranscriptIdentity: "codex:session", Model: "future-model", InputTokens: 5, CacheReadTokens: 3, OutputTokens: 2, ReasoningTokens: 1, PricingStatus: costs.PricingUnknown, ReconciliationStatus: costs.ReconciliationAuthoritative},
		{ID: "legacy", SessionID: "session-b", Timestamp: time.Now().UTC(), Model: "legacy", InputTokens: 9, CostMicrodollars: 9_000_000},
	}
	for _, event := range events {
		if err := store.WriteCostEvent(event); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCostsSummaryCoveragePreservesNumericFields(t *testing.T) {
	store := newTestCostStore(t)
	writeCoverageWebFixtures(t, store)
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	srv.SetCostStore(store)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/costs/summary", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"today_usd", "week_usd", "month_usd", "projected_usd"} {
		if _, ok := got[key].(float64); !ok {
			t.Fatalf("legacy field %s changed type: %#v", key, got[key])
		}
	}
	today, ok := got["today_coverage"].(map[string]any)
	if !ok || today["unknown_price_tokens"] != float64(10) || today["unreconciled_tokens"] != float64(9) || today["complete"] != false {
		t.Fatalf("today coverage=%#v body=%s", today, rr.Body.String())
	}
	if got["projection_complete"] != false || got["date_basis"] != "UTC calendar dates" || got["timezone"] != "UTC" {
		t.Fatalf("projection/date metadata missing: %s", rr.Body.String())
	}
	for _, key := range []string{"days", "providers", "models", "sessions", "runs"} {
		if _, ok := got[key].([]any); !ok {
			t.Fatalf("covered breakdown %s missing: %s", key, rr.Body.String())
		}
	}
}

type failingBreakdownStore struct {
	*costs.Store
	dimension string
}

func (s failingBreakdownStore) failure(name string) error {
	if s.dimension == name {
		return errors.New("synthetic " + name + " failure")
	}
	return nil
}
func (s failingBreakdownStore) CoveredCostByDay() ([]costs.CostBreakdown, error) {
	if err := s.failure("days"); err != nil {
		return nil, err
	}
	return s.Store.CoveredCostByDay()
}
func (s failingBreakdownStore) CoveredCostByProvider() ([]costs.CostBreakdown, error) {
	if err := s.failure("providers"); err != nil {
		return nil, err
	}
	return s.Store.CoveredCostByProvider()
}
func (s failingBreakdownStore) CoveredCostByModel() ([]costs.CostBreakdown, error) {
	if err := s.failure("models"); err != nil {
		return nil, err
	}
	return s.Store.CoveredCostByModel()
}
func (s failingBreakdownStore) CoveredCostBySession() ([]costs.CostBreakdown, error) {
	if err := s.failure("sessions"); err != nil {
		return nil, err
	}
	return s.Store.CoveredCostBySession()
}
func (s failingBreakdownStore) CoveredCostByRun() ([]costs.CostBreakdown, error) {
	if err := s.failure("runs"); err != nil {
		return nil, err
	}
	return s.Store.CoveredCostByRun()
}

func TestCostsSummaryPropagatesEveryBreakdownFailure(t *testing.T) {
	for _, dimension := range []string{"days", "providers", "models", "sessions", "runs"} {
		t.Run(dimension, func(t *testing.T) {
			srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
			srv.costStore = failingBreakdownStore{Store: newTestCostStore(t), dimension: dimension}
			rr := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/costs/summary", nil))
			if rr.Code != http.StatusInternalServerError {
				t.Fatalf("%s failure status=%d body=%s, want 500", dimension, rr.Code, rr.Body.String())
			}
		})
	}
}

func TestCostsExportIncludesCanonicalAuditFields(t *testing.T) {
	store := newTestCostStore(t)
	writeCoverageWebFixtures(t, store)
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	srv.SetCostStore(store)

	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/costs/export?format=json&days=1", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("json status=%d body=%s", rr.Code, rr.Body.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows=%d body=%s", len(rows), rr.Body.String())
	}
	var known map[string]any
	for _, row := range rows {
		if row["source_identity"] == "claude:msg:known" {
			known = row
		}
		if _, exists := row["conversation_text"]; exists {
			t.Fatalf("export leaked conversation text: %#v", row)
		}
	}
	if known == nil || known["provider"] != "claude" || known["run_id"] != "run-a" || known["uncached_input_tokens"] != float64(10) || known["cache_write_input_tokens"] != float64(30) || known["cache_write_5m_input_tokens"] != float64(11) || known["cache_write_1h_input_tokens"] != float64(13) || known["cache_write_duration_unknown_input_tokens"] != float64(6) || known["reasoning_output_tokens"] != float64(7) || known["known_cost_usd"] != 1.25 || known["pricing_status"] != "known" || known["reconciliation_status"] != "authoritative" || known["timezone"] != "UTC" {
		t.Fatalf("known export row=%#v", known)
	}

	rr = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/costs/export?format=csv&days=1", nil))
	records, err := csv.NewReader(strings.NewReader(rr.Body.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	header := strings.Join(records[0], ",")
	for _, field := range []string{"provider", "source_kind", "source_identity", "parent_session_id", "run_id", "cache_write_input_tokens", "cache_write_5m_input_tokens", "cache_write_1h_input_tokens", "reasoning_output_tokens", "pricing_status", "reconciliation_status", "timezone"} {
		if !strings.Contains(header, field) {
			t.Fatalf("CSV header missing %s: %s", field, header)
		}
	}
	var csvKnown []string
	for _, record := range records[1:] {
		if record[6] == "claude:msg:known" {
			csvKnown = record
			break
		}
	}
	wantCSV := []string{
		time.Now().UTC().Format("2006-01-02"), known["timestamp"].(string), "UTC", "UTC calendar dates", "claude", "claude_direct",
		"claude:msg:known", "claude:session", "session-a", "parent", "run-a", "session", "known-model",
		"10", "20", "30", "11", "13", "6", "40", "7", "1.250000", "1.250000", "known", "authoritative",
	}
	if strings.Join(csvKnown, "\x00") != strings.Join(wantCSV, "\x00") {
		t.Fatalf("CSV row = %#v, want %#v", csvKnown, wantCSV)
	}
}

func TestCostsGroupsIncludesAllSessionsBeyondLegacyLimit(t *testing.T) {
	store := newTestCostStore(t)
	for i := 0; i < 1005; i++ {
		event := costs.CostEvent{ID: fmt.Sprintf("e-%04d", i), SessionID: fmt.Sprintf("s-%04d", i), Timestamp: time.Now().UTC(), CostMicrodollars: 1, PricingStatus: costs.PricingKnown, ReconciliationStatus: costs.ReconciliationAuthoritative}
		if err := store.WriteCostEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	srv.SetCostStore(store)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/costs/groups", nil))
	var groups []struct {
		CostUSD          float64 `json:"cost_usd"`
		Events, Sessions int
		Coverage         costs.Coverage
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &groups); err != nil {
		t.Fatal(err)
	}
	if rr.Code != http.StatusOK || len(groups) != 1 || groups[0].Events != 1005 || groups[0].Sessions != 1005 || groups[0].Coverage.EventCount != 1005 || groups[0].CostUSD != 0.001005 {
		t.Fatalf("groups=%+v status=%d body=%s", groups, rr.Code, rr.Body.String())
	}
}

type failingListStore struct {
	*costs.Store
	target string
}

func (s failingListStore) CoveredTopSessionsByCost(limit int) ([]costs.SessionCost, error) {
	if s.target == "sessions" {
		return nil, errors.New("synthetic sessions failure")
	}
	return s.Store.CoveredTopSessionsByCost(limit)
}
func (s failingListStore) CoveredCostByGroup() ([]costs.GroupCost, error) {
	if s.target == "groups" {
		return nil, errors.New("synthetic groups failure")
	}
	return s.Store.CoveredCostByGroup()
}

func TestCostsSessionAndGroupQueriesPropagateFailures(t *testing.T) {
	for _, tc := range []struct{ target, path string }{{"sessions", "/api/costs/sessions"}, {"groups", "/api/costs/groups"}} {
		t.Run(tc.target, func(t *testing.T) {
			srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
			srv.costStore = failingListStore{Store: newTestCostStore(t), target: tc.target}
			rr := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rr.Code != http.StatusInternalServerError {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestCostsSessionsUseGroupedCoverageAndDailyRangeIsBounded(t *testing.T) {
	store := newTestCostStore(t)
	now := time.Now().UTC()
	events := []costs.CostEvent{
		{ID: "recent-known", SessionID: "session", Timestamp: now, CostMicrodollars: 10, PricingStatus: costs.PricingKnown, ReconciliationStatus: costs.ReconciliationAuthoritative},
		{ID: "recent-unknown", SessionID: "session", Timestamp: now, InputTokens: 4, PricingStatus: costs.PricingUnknown, ReconciliationStatus: costs.ReconciliationAuthoritative},
		{ID: "old", SessionID: "old", Timestamp: now.AddDate(0, 0, -60), CostMicrodollars: 20, PricingStatus: costs.PricingKnown, ReconciliationStatus: costs.ReconciliationAuthoritative},
	}
	for _, event := range events {
		if err := store.WriteCostEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	srv.SetCostStore(store)

	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/costs/sessions", nil))
	var sessions []struct {
		SessionID string         `json:"session_id"`
		Events    int            `json:"events"`
		Coverage  costs.Coverage `json:"coverage"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &sessions); err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]struct {
		Events   int
		Coverage costs.Coverage
	}, len(sessions))
	for _, item := range sessions {
		byID[item.SessionID] = struct {
			Events   int
			Coverage costs.Coverage
		}{item.Events, item.Coverage}
	}
	if item, ok := byID["session"]; len(sessions) != 2 || !ok || item.Events != 2 || item.Coverage.UnknownPriceEventCount != 1 {
		t.Fatalf("sessions=%+v body=%s", sessions, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/costs/daily?days=30", nil))
	var days []struct {
		Date string `json:"date"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &days); err != nil {
		t.Fatal(err)
	}
	if len(days) != 1 || days[0].Date != now.Format("2006-01-02") {
		t.Fatalf("days=%+v body=%s", days, rr.Body.String())
	}
}

func TestCostsSummaryPublicWeekAndProviderBoundaries(t *testing.T) {
	store := newTestCostStore(t)
	monday := time.Date(2025, 11, 10, 0, 0, 1, 0, time.UTC)
	store.SetClock(func() time.Time { return monday })
	events := []costs.CostEvent{
		{ID: "current", SessionID: "s", Timestamp: monday, Provider: costs.ProviderClaude, Model: "same", InputTokens: 2, CostMicrodollars: 7, PricingStatus: costs.PricingKnown, ReconciliationStatus: costs.ReconciliationAuthoritative},
		{ID: "old", SessionID: "s", Timestamp: monday.Add(-24 * time.Hour), Provider: costs.ProviderCodex, Model: "same", OutputTokens: 3, CostMicrodollars: 9, PricingStatus: costs.PricingKnown, ReconciliationStatus: costs.ReconciliationAuthoritative},
	}
	for _, event := range events {
		if err := store.WriteCostEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	srv.SetCostStore(store)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/costs/summary", nil))
	var body struct {
		WeekUSD   float64               `json:"week_usd"`
		Providers []costs.CostBreakdown `json:"providers"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.WeekUSD != 0.000007 {
		t.Fatalf("week_usd=%v body=%s", body.WeekUSD, rr.Body.String())
	}
	if len(body.Providers) != 2 || body.Providers[0].Key != costs.ProviderClaude || body.Providers[0].UncachedInputTokens != 2 || body.Providers[1].Key != costs.ProviderCodex || body.Providers[1].OutputTokens != 3 {
		t.Fatalf("providers=%+v", body.Providers)
	}
}

type synchronizedRecorder struct {
	*httptest.ResponseRecorder
	mu sync.Mutex
}

func (r *synchronizedRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ResponseRecorder.Write(p)
}
func (r *synchronizedRecorder) Flush() {}
func (r *synchronizedRecorder) body() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Body.String()
}

func TestCostsStreamEmitsCoverageOnlyChange(t *testing.T) {
	store := newTestCostStore(t)
	if err := store.WriteCostEvent(costs.CostEvent{ID: "stream", SessionID: "s", Timestamp: time.Now().UTC(), PricingStatus: costs.PricingUnknown, ReconciliationStatus: costs.ReconciliationAuthoritative}); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	srv.SetCostStore(store)
	oldPoll, oldHeartbeat := costStreamPollInterval, costStreamHeartbeatInterval
	costStreamPollInterval, costStreamHeartbeatInterval = 5*time.Millisecond, time.Hour
	t.Cleanup(func() { costStreamPollInterval, costStreamHeartbeatInterval = oldPoll, oldHeartbeat })
	ctx, cancel := context.WithCancel(context.Background())
	recorder := &synchronizedRecorder{ResponseRecorder: httptest.NewRecorder()}
	done := make(chan struct{})
	go func() {
		srv.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/costs/stream", nil).WithContext(ctx))
		close(done)
	}()
	deadline := time.Now().Add(time.Second)
	for strings.Count(recorder.body(), "event: cost_summary") < 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if _, err := store.DB().Exec(`UPDATE cost_events SET pricing_status = ? WHERE id = 'stream'`, costs.PricingKnownZero); err != nil {
		t.Fatal(err)
	}
	for strings.Count(recorder.body(), "event: cost_summary") < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if count := strings.Count(recorder.body(), "event: cost_summary"); count < 2 {
		t.Fatalf("coverage-only change emitted %d summaries: %s", count, recorder.body())
	}
}

func TestCostsBatchAddsCoverageBesideLegacyCosts(t *testing.T) {
	store := newTestCostStore(t)
	writeCoverageWebFixtures(t, store)
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	srv.SetCostStore(store)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/costs/batch?ids=session-a,session-b", nil))
	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["costs"].(map[string]any)["session-a"].(float64); !ok {
		t.Fatalf("legacy batch cost changed type: %s", rr.Body.String())
	}
	coverage, ok := got["coverage"].(map[string]any)
	if !ok || coverage["session-b"].(map[string]any)["complete"] != false {
		t.Fatalf("batch coverage missing: %s", rr.Body.String())
	}
}

// newTestCostStore creates an in-memory cost store backed by a temp-dir SQLite database.
func newTestCostStore(t *testing.T) *costs.Store {
	t.Helper()
	dir := t.TempDir()
	sdb, err := statedb.Open(filepath.Join(dir, "costs_test.db"))
	if err != nil {
		t.Fatalf("statedb.Open: %v", err)
	}
	if err := sdb.Migrate(); err != nil {
		t.Fatalf("sdb.Migrate: %v", err)
	}
	t.Cleanup(func() { sdb.Close() })
	return costs.NewStore(sdb.DB())
}

func TestCostsBatch(t *testing.T) {
	store := newTestCostStore(t)

	// Record $0.05 for sess1 (50000 microdollars)
	if err := store.WriteCostEvent(costs.CostEvent{
		ID:               "evt-1",
		SessionID:        "sess1",
		Timestamp:        time.Now(),
		Model:            "claude-sonnet-4-6",
		CostMicrodollars: 50000,
		PricingStatus:    costs.PricingKnown, ReconciliationStatus: costs.ReconciliationAuthoritative,
	}); err != nil {
		t.Fatalf("WriteCostEvent sess1: %v", err)
	}

	// Record $1.20 for sess2 (1200000 microdollars)
	if err := store.WriteCostEvent(costs.CostEvent{
		ID:               "evt-2",
		SessionID:        "sess2",
		Timestamp:        time.Now(),
		Model:            "claude-sonnet-4-6",
		CostMicrodollars: 1200000,
		PricingStatus:    costs.PricingKnown, ReconciliationStatus: costs.ReconciliationAuthoritative,
	}); err != nil {
		t.Fatalf("WriteCostEvent sess2: %v", err)
	}

	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	srv.SetCostStore(store)

	req := httptest.NewRequest(http.MethodGet, "/api/costs/batch?ids=sess1,sess2", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Costs map[string]float64 `json:"costs"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Costs == nil {
		t.Fatal("expected non-nil costs map")
	}
	if got := resp.Costs["sess1"]; got < 0.049 || got > 0.051 {
		t.Errorf("sess1 cost = %f, want ~0.05", got)
	}
	if got := resp.Costs["sess2"]; got < 1.19 || got > 1.21 {
		t.Errorf("sess2 cost = %f, want ~1.20", got)
	}
}

func TestCostsBatchNoCostStore(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	// Intentionally do NOT call SetCostStore — costStore remains nil

	req := httptest.NewRequest(http.MethodGet, "/api/costs/batch?ids=sess1", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 with nil costStore, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Costs map[string]float64 `json:"costs"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Costs == nil {
		t.Fatal("expected non-nil empty costs map")
	}
	if len(resp.Costs) != 0 {
		t.Errorf("expected empty costs map, got %v", resp.Costs)
	}
}

func TestCostsBatchUnauthorized(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
		Token:      "secret-token",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/costs/batch?ids=sess1", nil)
	// No Authorization header
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "UNAUTHORIZED") {
		t.Fatalf("expected UNAUTHORIZED in body, got: %s", rr.Body.String())
	}
}

func TestCostsBatchEmptyIDs(t *testing.T) {
	store := newTestCostStore(t)
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	srv.SetCostStore(store)

	req := httptest.NewRequest(http.MethodGet, "/api/costs/batch?ids=", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Costs map[string]float64 `json:"costs"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Costs) != 0 {
		t.Errorf("expected empty costs for empty ids, got %v", resp.Costs)
	}
}

func TestCostsBatchMethodNotAllowed(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})

	// PERF-I: POST is now allowed (JSON body form). PUT / DELETE / PATCH
	// remain disallowed so the 405 path still has coverage.
	req := httptest.NewRequest(http.MethodPut, "/api/costs/batch", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for PUT, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestCostsBatchPOSTJSONBody exercises the PERF-I POST form with a JSON
// body — mirrors TestCostsBatch but uses the new transport. Guards the
// 414 URI Too Long regression that would come back if the frontend fell
// back to GET + query string for long session lists.
func TestCostsBatchPOSTJSONBody(t *testing.T) {
	store := newTestCostStore(t)
	if err := store.WriteCostEvent(costs.CostEvent{
		ID:               "evt-post-1",
		SessionID:        "sessA",
		Timestamp:        time.Now(),
		Model:            "claude-sonnet-4-6",
		CostMicrodollars: 75000,
		PricingStatus:    costs.PricingKnown, ReconciliationStatus: costs.ReconciliationAuthoritative,
	}); err != nil {
		t.Fatalf("WriteCostEvent sessA: %v", err)
	}

	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	srv.SetCostStore(store)

	body := strings.NewReader(`{"ids":["sessA","sessB"]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/costs/batch", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Costs map[string]float64 `json:"costs"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := resp.Costs["sessA"]; got < 0.074 || got > 0.076 {
		t.Errorf("sessA cost = %f, want ~0.075", got)
	}
}

func TestCostsBatchPOSTInvalidBody(t *testing.T) {
	store := newTestCostStore(t)
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	srv.SetCostStore(store)

	req := httptest.NewRequest(http.MethodPost, "/api/costs/batch", strings.NewReader("not-json"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid json, got %d: %s", rr.Code, rr.Body.String())
	}
}
