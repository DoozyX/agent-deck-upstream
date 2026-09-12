package web

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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
