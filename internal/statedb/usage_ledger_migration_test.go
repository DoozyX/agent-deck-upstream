package statedb

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestUsageLedgerMigrationPreservesLegacyRowsAndAddsCanonicalColumns(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`INSERT INTO metadata (key, value) VALUES ('schema_version', '17')`,
		`CREATE TABLE cost_events (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			timestamp DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			model TEXT NOT NULL,
			input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			cache_read_tokens INTEGER NOT NULL DEFAULT 0,
			cache_write_tokens INTEGER NOT NULL DEFAULT 0,
			cost_microdollars INTEGER NOT NULL DEFAULT 0,
			budget_stop_triggered INTEGER NOT NULL DEFAULT 0
		)`,
		`INSERT INTO cost_events (
			id, session_id, timestamp, model, input_tokens, output_tokens,
			cache_read_tokens, cache_write_tokens, cost_microdollars
		) VALUES ('legacy-1', 'session-1', '2026-09-01T12:00:00Z', 'legacy-model', 7, 5, 3, 2, 99)`,
	}
	for _, statement := range statements {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatalf("prepare old schema: %v", err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var rowCount int
	if err := db.DB().QueryRow(`SELECT COUNT(*) FROM cost_events WHERE id = 'legacy-1'`).Scan(&rowCount); err != nil {
		t.Fatal(err)
	}
	if rowCount != 1 {
		t.Fatalf("legacy row count = %d, want 1", rowCount)
	}

	wantColumns := map[string]bool{
		"provider": false, "source_kind": false, "source_identity": false,
		"transcript_identity": false, "parent_session_id": false, "run_id": false,
		"cache_write_5m_tokens": false, "cache_write_1h_tokens": false,
		"reasoning_tokens": false, "provider_input_tokens": false, "pricing_status": false,
		"reconciliation_status": false,
	}
	rows, err := db.DB().Query(`PRAGMA table_info(cost_events)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if _, ok := wantColumns[name]; ok {
			wantColumns[name] = true
		}
	}
	for name, found := range wantColumns {
		if !found {
			t.Errorf("cost_events column %q missing", name)
		}
	}

	var id, sessionID, timestamp, model, provider, sourceKind, sourceIdentity, transcriptIdentity string
	var parentSessionID, runID, pricingStatus, reconciliationStatus string
	var input, output, cacheRead, cacheWrite, cacheWrite5m, cacheWrite1h, reasoning, cost int64
	var providerInput sql.NullInt64
	if err := db.DB().QueryRow(`
		SELECT id, session_id, parent_session_id, run_id, timestamp,
			provider, source_kind, source_identity, transcript_identity, model,
			input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
			cache_write_5m_tokens, cache_write_1h_tokens, reasoning_tokens,
			provider_input_tokens, cost_microdollars, pricing_status, reconciliation_status
		FROM cost_events WHERE id = 'legacy-1'`).Scan(
		&id, &sessionID, &parentSessionID, &runID, &timestamp,
		&provider, &sourceKind, &sourceIdentity, &transcriptIdentity, &model,
		&input, &output, &cacheRead, &cacheWrite, &cacheWrite5m, &cacheWrite1h,
		&reasoning, &providerInput, &cost, &pricingStatus, &reconciliationStatus); err != nil {
		t.Fatal(err)
	}
	if id != "legacy-1" || sessionID != "session-1" || timestamp != "2026-09-01T12:00:00Z" || model != "legacy-model" ||
		input != 7 || output != 5 || cacheRead != 3 || cacheWrite != 2 || cost != 99 {
		t.Fatalf("legacy values changed: id=%q session=%q timestamp=%q model=%q usage=%d/%d/%d/%d cost=%d",
			id, sessionID, timestamp, model, input, output, cacheRead, cacheWrite, cost)
	}
	if parentSessionID != "" || runID != "" || provider != "unknown" || sourceKind != "legacy" ||
		sourceIdentity != "" || transcriptIdentity != "" || cacheWrite5m != 0 || cacheWrite1h != 0 ||
		reasoning != 0 || providerInput.Valid || pricingStatus != "legacy_unresolved" || reconciliationStatus != "legacy_unreconciled" {
		t.Fatalf("legacy defaults changed: parent=%q run=%q provider=%q kind=%q source=%q transcript=%q write=%d/%d reasoning=%d provider_input=%v pricing=%q reconciliation=%q",
			parentSessionID, runID, provider, sourceKind, sourceIdentity, transcriptIdentity,
			cacheWrite5m, cacheWrite1h, reasoning, providerInput, pricingStatus, reconciliationStatus)
	}

	if err := db.DB().QueryRow(`SELECT COUNT(*) FROM usage_scan_checkpoints`).Scan(&rowCount); err != nil {
		t.Fatalf("usage_scan_checkpoints missing: %v", err)
	}
}

func TestCostEventRowCanonicalFieldsRoundTrip(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "roundtrip.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	providerInput := int64(12)
	want := &CostEventRow{
		ID: "event-1", SessionID: "session", ParentSessionID: "parent", RunID: "run",
		Timestamp: "2026-09-01T12:00:00Z", Provider: "codex", SourceKind: "rollout",
		SourceIdentity: "request-1", TranscriptIdentity: "transcript-1", Model: "gpt-test",
		InputTokens: 2, OutputTokens: 5, CacheReadTokens: 7, CacheWriteTokens: 3,
		CacheWrite5mTokens: 2, CacheWrite1hTokens: 1, ReasoningTokens: 4,
		ProviderInputTokens: &providerInput, CostMicrodollars: 123,
		PricingStatus: "known", ReconciliationStatus: "authoritative", BudgetStopTriggered: true,
	}
	if err := db.InsertCostEventRow(want); err != nil {
		t.Fatal(err)
	}
	got, err := db.LoadCostEventsForSession("session")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("row count = %d, want 1", len(got))
	}
	row := got[0]
	if row.ParentSessionID != want.ParentSessionID || row.RunID != want.RunID || row.Provider != want.Provider ||
		row.SourceKind != want.SourceKind || row.SourceIdentity != want.SourceIdentity || row.TranscriptIdentity != want.TranscriptIdentity ||
		row.CacheWrite5mTokens != want.CacheWrite5mTokens || row.CacheWrite1hTokens != want.CacheWrite1hTokens ||
		row.ReasoningTokens != want.ReasoningTokens || row.ProviderInputTokens == nil || *row.ProviderInputTokens != providerInput ||
		row.PricingStatus != want.PricingStatus || row.ReconciliationStatus != want.ReconciliationStatus {
		t.Fatalf("canonical row did not round trip: got=%+v want=%+v", row, want)
	}
}
