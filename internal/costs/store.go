package costs

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Store persists and queries cost events in SQLite.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

type IngestResult struct {
	Inserted            int
	Updated             int
	Duplicates          int
	Superseded          int
	CheckpointsAdvanced int
}

// Ingest atomically persists canonical events, reconciliation, and complete
// checkpoints.
func (s *Store) Ingest(ctx context.Context, events []UsageEvent, checkpoints []ScanCheckpoint) (result IngestResult, err error) {
	for i := range events {
		if err := events[i].Usage.Validate(); err != nil {
			return result, fmt.Errorf("validate usage event %q: %w", events[i].ID, err)
		}
		if events[i].ID == "" {
			return result, fmt.Errorf("usage event id is required")
		}
		if events[i].SourceIdentity != "" && (events[i].Provider == "" || events[i].SourceKind == "") {
			return result, fmt.Errorf("usage event %q source identity requires provider and source kind", events[i].ID)
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, fmt.Errorf("begin usage ingest: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	completeSources := make(map[string]bool, len(checkpoints))
	for _, checkpoint := range checkpoints {
		if checkpoint.Complete {
			completeSources[checkpointKey(checkpoint.Provider, checkpoint.SourceIdentity)] = true
		}
	}

	for _, event := range events {
		inserted, updated, err := insertUsageEventTx(tx, event)
		if err != nil {
			return IngestResult{}, fmt.Errorf("insert usage event %q: %w", event.ID, err)
		}
		if updated {
			result.Updated++
		} else if !inserted {
			result.Duplicates++
		} else {
			result.Inserted++
		}

		canReconcile := completeSources[checkpointKey(event.Provider, event.TranscriptIdentity)]
		if !canReconcile || len(event.SupersedesEventIDs) == 0 {
			continue
		}
		for _, legacyID := range event.SupersedesEventIDs {
			update, err := tx.ExecContext(ctx, `
				UPDATE cost_events
				SET reconciliation_status = ?
				WHERE id = ? AND reconciliation_status = ?`,
				ReconciliationLegacySuperseded, legacyID, ReconciliationLegacyUnreconciled)
			if err != nil {
				return IngestResult{}, fmt.Errorf("supersede legacy event %q: %w", legacyID, err)
			}
			count, err := update.RowsAffected()
			if err != nil {
				return IngestResult{}, fmt.Errorf("count superseded legacy event %q: %w", legacyID, err)
			}
			result.Superseded += int(count)
		}
	}

	for _, checkpoint := range checkpoints {
		if checkpoint.Receipt != nil {
			if err := persistSyncReceiptTx(ctx, tx, *checkpoint.Receipt); err != nil {
				return IngestResult{}, err
			}
		}
		if err := persistCoverageAndReconcileTx(ctx, tx, checkpoint, &result); err != nil {
			return IngestResult{}, err
		}
		if !checkpoint.Complete {
			continue
		}
		if checkpoint.Provider == "" || checkpoint.SourceKind == "" || checkpoint.SourceIdentity == "" {
			return IngestResult{}, fmt.Errorf("complete checkpoint requires provider, source kind, and source identity")
		}
		updatedAt := checkpoint.UpdatedAt
		if updatedAt.IsZero() {
			return IngestResult{}, fmt.Errorf("complete checkpoint %q requires updated timestamp", checkpoint.SourceIdentity)
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO usage_scan_checkpoints (
				provider, source_kind, source_identity, offset, fingerprint, source_fingerprint, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(provider, source_kind, source_identity) DO UPDATE SET
				offset = excluded.offset,
				fingerprint = excluded.fingerprint,
				source_fingerprint = excluded.source_fingerprint,
				updated_at = excluded.updated_at`,
			checkpoint.Provider, checkpoint.SourceKind, checkpoint.SourceIdentity,
			checkpoint.Offset, checkpoint.Fingerprint, checkpoint.SourceFingerprint,
			updatedAt.UTC().Format(time.RFC3339Nano))
		if err != nil {
			return IngestResult{}, fmt.Errorf("advance checkpoint %q: %w", checkpoint.SourceIdentity, err)
		}
		result.CheckpointsAdvanced++
	}

	if err := tx.Commit(); err != nil {
		return IngestResult{}, fmt.Errorf("commit usage ingest: %w", err)
	}
	return result, nil
}

func persistSyncReceiptTx(ctx context.Context, tx *sql.Tx, receipt SyncReceipt) error {
	warnings, err := json.Marshal(receipt.Warnings)
	if err != nil {
		return fmt.Errorf("encode sync receipt warnings: %w", err)
	}
	blockedUntil := ""
	if !receipt.BlockedUntil.IsZero() {
		blockedUntil = receipt.BlockedUntil.UTC().Format(time.RFC3339Nano)
	}
	resetKnown := 0
	if receipt.ResetKnown {
		resetKnown = 1
	}
	coverageComplete := 0
	if receipt.CoverageComplete {
		coverageComplete = 1
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO usage_sync_receipts (
			provider, source_kind, source_identity, account, blocked_status,
			blocked_until, reset_known, backoff_seconds, coverage_complete,
			warnings_json, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(provider, source_kind, source_identity) DO UPDATE SET
			account = excluded.account,
			blocked_status = excluded.blocked_status,
			blocked_until = excluded.blocked_until,
			reset_known = excluded.reset_known,
			backoff_seconds = excluded.backoff_seconds,
			coverage_complete = excluded.coverage_complete,
			warnings_json = excluded.warnings_json,
			updated_at = excluded.updated_at`,
		receipt.Provider, receipt.SourceKind, receipt.SourceIdentity, receipt.Account,
		receipt.Status, blockedUntil, resetKnown, int64(receipt.Backoff/time.Second),
		coverageComplete, string(warnings), receipt.UpdatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("persist sync receipt %q: %w", receipt.SourceIdentity, err)
	}
	return nil
}

func persistCoverageAndReconcileTx(ctx context.Context, tx *sql.Tx, checkpoint ScanCheckpoint, result *IngestResult) error {
	if checkpoint.Provider == "" || checkpoint.SourceKind == "" || checkpoint.SourceIdentity == "" {
		return nil
	}
	hasCurrentRange := checkpoint.CoverageSessionID != "" &&
		checkpoint.CoverageSessionID != UnassignedSessionID &&
		!checkpoint.CoverageStart.IsZero() && !checkpoint.CoverageEnd.IsZero()
	if !checkpoint.Complete {
		if !hasCurrentRange {
			return nil
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO usage_pending_coverage (
				provider, source_kind, source_identity, session_id, start_at, end_at
			) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(provider, source_kind, source_identity) DO UPDATE SET
				session_id = excluded.session_id,
				start_at = MIN(usage_pending_coverage.start_at, excluded.start_at),
				end_at = MAX(usage_pending_coverage.end_at, excluded.end_at)`,
			checkpoint.Provider, checkpoint.SourceKind, checkpoint.SourceIdentity,
			checkpoint.CoverageSessionID, checkpoint.CoverageStart.UTC().Format(time.RFC3339Nano),
			checkpoint.CoverageEnd.UTC().Format(time.RFC3339Nano))
		if err != nil {
			return fmt.Errorf("persist pending coverage %q: %w", checkpoint.SourceIdentity, err)
		}
		return nil
	}

	sessionID := checkpoint.CoverageSessionID
	from, to := checkpoint.CoverageStart, checkpoint.CoverageEnd
	var pendingSession, pendingStart, pendingEnd string
	err := tx.QueryRowContext(ctx, `
		SELECT session_id, start_at, end_at FROM usage_pending_coverage
		WHERE provider = ? AND source_kind = ? AND source_identity = ?`,
		checkpoint.Provider, checkpoint.SourceKind, checkpoint.SourceIdentity).Scan(
		&pendingSession, &pendingStart, &pendingEnd)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("load pending coverage %q: %w", checkpoint.SourceIdentity, err)
	}
	if err == nil {
		pendingFrom, parseErr := time.Parse(time.RFC3339Nano, pendingStart)
		if parseErr != nil {
			return fmt.Errorf("parse pending coverage start %q: %w", checkpoint.SourceIdentity, parseErr)
		}
		pendingTo, parseErr := time.Parse(time.RFC3339Nano, pendingEnd)
		if parseErr != nil {
			return fmt.Errorf("parse pending coverage end %q: %w", checkpoint.SourceIdentity, parseErr)
		}
		if sessionID == "" {
			sessionID = pendingSession
		}
		if from.IsZero() || pendingFrom.Before(from) {
			from = pendingFrom
		}
		if to.IsZero() || pendingTo.After(to) {
			to = pendingTo
		}
	}
	if sessionID != "" && sessionID != UnassignedSessionID && !from.IsZero() && !to.IsZero() {
		update, err := tx.ExecContext(ctx, `
			UPDATE cost_events SET reconciliation_status = ?
			WHERE session_id = ? AND timestamp >= ? AND timestamp <= ?
				AND reconciliation_status = ?`,
			ReconciliationLegacySuperseded, sessionID,
			from.UTC().Format(time.RFC3339Nano), to.UTC().Format(time.RFC3339Nano),
			ReconciliationLegacyUnreconciled)
		if err != nil {
			return fmt.Errorf("reconcile coverage %q: %w", checkpoint.SourceIdentity, err)
		}
		count, err := update.RowsAffected()
		if err != nil {
			return fmt.Errorf("count reconciled coverage %q: %w", checkpoint.SourceIdentity, err)
		}
		result.Superseded += int(count)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM usage_pending_coverage WHERE provider = ? AND source_kind = ? AND source_identity = ?`, checkpoint.Provider, checkpoint.SourceKind, checkpoint.SourceIdentity); err != nil {
		return fmt.Errorf("clear pending coverage %q: %w", checkpoint.SourceIdentity, err)
	}
	return nil
}

func checkpointKey(provider, sourceIdentity string) string {
	return provider + "\x00" + sourceIdentity
}

func insertUsageEventTx(tx *sql.Tx, event UsageEvent) (bool, bool, error) {
	existingID, existingSource, found, err := findUsageEventByAliases(tx, event)
	if err != nil {
		return false, false, err
	}
	if found {
		updated, err := updateUsageEventCorrection(tx, existingID, event)
		if err != nil {
			return false, false, err
		}
		if err := registerUsageEventAliases(tx, event.Provider, existingSource, append(event.SourceAliases, event.SourceIdentity)); err != nil {
			return false, false, err
		}
		return false, updated, nil
	}
	providerInput := any(nil)
	if event.Usage.ProviderInputTokens != nil {
		providerInput = *event.Usage.ProviderInputTokens
	}
	result, err := tx.Exec(`
		INSERT OR IGNORE INTO cost_events (
			id, session_id, parent_session_id, run_id, timestamp,
			provider, source_kind, source_identity, transcript_identity, model,
			input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
			cache_write_5m_tokens, cache_write_1h_tokens, reasoning_tokens,
			provider_input_tokens, cost_microdollars, pricing_status,
			reconciliation_status
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.ID, event.SessionID, event.ParentSessionID, event.RunID,
		event.Timestamp.UTC().Format(time.RFC3339Nano), event.Provider, event.SourceKind,
		event.SourceIdentity, event.TranscriptIdentity, event.Model,
		event.Usage.InputTokens, event.Usage.OutputTokens, event.Usage.CacheReadTokens,
		event.Usage.CacheWriteTokens, event.Usage.CacheWrite5mTokens,
		event.Usage.CacheWrite1hTokens, event.Usage.ReasoningTokens, providerInput,
		event.CostMicrodollars, event.PricingStatus, event.ReconciliationStatus)
	if err != nil {
		return false, false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, false, err
	}
	if count != 1 {
		return false, false, nil
	}
	if err := registerUsageEventAliases(tx, event.Provider, event.SourceIdentity, append(event.SourceAliases, event.SourceIdentity)); err != nil {
		return false, false, err
	}
	return true, false, nil
}

func findUsageEventByAliases(tx *sql.Tx, event UsageEvent) (id, sourceIdentity string, found bool, err error) {
	aliases := uniqueNonEmpty(append(event.SourceAliases, event.SourceIdentity))
	for _, alias := range aliases {
		err = tx.QueryRow(`
			SELECT ce.id, ce.source_identity
			FROM usage_event_aliases a
			JOIN cost_events ce ON ce.provider = a.provider AND ce.source_identity = a.source_identity
			WHERE a.provider = ? AND a.alias = ?`, event.Provider, alias).Scan(&id, &sourceIdentity)
		if err == nil {
			return id, sourceIdentity, true, nil
		}
		if err != sql.ErrNoRows {
			return "", "", false, err
		}
		err = tx.QueryRow(`SELECT id, source_identity FROM cost_events WHERE provider = ? AND source_identity = ?`, event.Provider, alias).Scan(&id, &sourceIdentity)
		if err == nil {
			return id, sourceIdentity, true, nil
		}
		if err != sql.ErrNoRows {
			return "", "", false, err
		}
	}
	return "", "", false, nil
}

func updateUsageEventCorrection(tx *sql.Tx, existingID string, incoming UsageEvent) (bool, error) {
	var current TokenUsage
	var providerInput sql.NullInt64
	var reconciliation string
	err := tx.QueryRow(`
		SELECT input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
			cache_write_5m_tokens, cache_write_1h_tokens, reasoning_tokens,
			provider_input_tokens, reconciliation_status
		FROM cost_events WHERE id = ?`, existingID).Scan(
		&current.InputTokens, &current.OutputTokens, &current.CacheReadTokens,
		&current.CacheWriteTokens, &current.CacheWrite5mTokens,
		&current.CacheWrite1hTokens, &current.ReasoningTokens,
		&providerInput, &reconciliation)
	if err != nil {
		return false, err
	}
	if providerInput.Valid {
		value := providerInput.Int64
		current.ProviderInputTokens = &value
	}
	if ReconciliationStatus(reconciliation) != ReconciliationAuthoritative || incoming.ReconciliationStatus != ReconciliationAuthoritative {
		return false, nil
	}
	merged, changed := mergeMonotoneUsage(current, incoming.Usage)
	if !changed {
		return false, nil
	}
	providerInputValue := any(nil)
	if merged.ProviderInputTokens != nil {
		providerInputValue = *merged.ProviderInputTokens
	}
	_, err = tx.Exec(`
		UPDATE cost_events SET
			session_id = ?, parent_session_id = ?, run_id = ?, timestamp = ?,
			source_kind = ?, transcript_identity = ?, model = ?,
			input_tokens = ?, output_tokens = ?, cache_read_tokens = ?, cache_write_tokens = ?,
			cache_write_5m_tokens = ?, cache_write_1h_tokens = ?, reasoning_tokens = ?,
			provider_input_tokens = ?, cost_microdollars = ?, pricing_status = ?
		WHERE id = ?`,
		incoming.SessionID, incoming.ParentSessionID, incoming.RunID,
		incoming.Timestamp.UTC().Format(time.RFC3339Nano), incoming.SourceKind,
		incoming.TranscriptIdentity, incoming.Model,
		merged.InputTokens, merged.OutputTokens, merged.CacheReadTokens,
		merged.CacheWriteTokens, merged.CacheWrite5mTokens, merged.CacheWrite1hTokens,
		merged.ReasoningTokens, providerInputValue, incoming.CostMicrodollars,
		incoming.PricingStatus, existingID)
	return err == nil, err
}

func mergeMonotoneUsage(current, incoming TokenUsage) (TokenUsage, bool) {
	if incoming.InputTokens < current.InputTokens || incoming.OutputTokens < current.OutputTokens ||
		incoming.CacheReadTokens < current.CacheReadTokens || incoming.CacheWriteTokens < current.CacheWriteTokens {
		return current, false
	}
	merged := incoming
	if current.ReasoningTokens > merged.ReasoningTokens {
		merged.ReasoningTokens = current.ReasoningTokens
	}
	currentDetail := current.CacheWrite5mTokens + current.CacheWrite1hTokens
	incomingDetail := incoming.CacheWrite5mTokens + incoming.CacheWrite1hTokens
	if currentDetail > incomingDetail {
		merged.CacheWrite5mTokens = current.CacheWrite5mTokens
		merged.CacheWrite1hTokens = current.CacheWrite1hTokens
	}
	if current.ProviderInputTokens != nil && (merged.ProviderInputTokens == nil || *current.ProviderInputTokens > *merged.ProviderInputTokens) {
		value := *current.ProviderInputTokens
		merged.ProviderInputTokens = &value
	}
	if err := merged.Validate(); err != nil {
		return current, false
	}
	return merged, !tokenUsageEqual(current, merged)
}

func tokenUsageEqual(left, right TokenUsage) bool {
	if left.InputTokens != right.InputTokens || left.OutputTokens != right.OutputTokens ||
		left.CacheReadTokens != right.CacheReadTokens || left.CacheWriteTokens != right.CacheWriteTokens ||
		left.CacheWrite5mTokens != right.CacheWrite5mTokens || left.CacheWrite1hTokens != right.CacheWrite1hTokens ||
		left.ReasoningTokens != right.ReasoningTokens {
		return false
	}
	if left.ProviderInputTokens == nil || right.ProviderInputTokens == nil {
		return left.ProviderInputTokens == nil && right.ProviderInputTokens == nil
	}
	return *left.ProviderInputTokens == *right.ProviderInputTokens
}

func registerUsageEventAliases(tx *sql.Tx, provider, sourceIdentity string, aliases []string) error {
	for _, alias := range uniqueNonEmpty(aliases) {
		if _, err := tx.Exec(`
			INSERT INTO usage_event_aliases (provider, alias, source_identity)
			VALUES (?, ?, ?)
			ON CONFLICT(provider, alias) DO NOTHING`, provider, alias, sourceIdentity); err != nil {
			return err
		}
	}
	return nil
}

func uniqueNonEmpty(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

// NewStore creates a Store using an existing database connection.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db, now: time.Now}
}

// SetClock overrides the clock used for time-windowed queries. Tests use this
// to pin "now" to a deterministic instant (e.g. a Monday UTC boundary).
func (s *Store) SetClock(now func() time.Time) {
	s.now = now
}

// DB returns the underlying database for transactional operations.
func (s *Store) DB() *sql.DB {
	return s.db
}

// WriteCostEvent inserts a cost event.
func (s *Store) WriteCostEvent(ev CostEvent) error {
	event := UsageEventFromCostEvent(ev)
	if err := event.Usage.Validate(); err != nil {
		return fmt.Errorf("validate cost event %q: %w", event.ID, err)
	}
	_, err := insertCanonicalEvent(s.db, event)
	return err
}

// WriteCostEventTx inserts a cost event within a transaction.
func (s *Store) WriteCostEventTx(tx *sql.Tx, ev CostEvent) error {
	event := UsageEventFromCostEvent(ev)
	if err := event.Usage.Validate(); err != nil {
		return fmt.Errorf("validate cost event %q: %w", event.ID, err)
	}
	_, err := insertCanonicalEvent(tx, event)
	return err
}

type sqlExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func insertCanonicalEvent(exec sqlExecer, event UsageEvent) (sql.Result, error) {
	providerInput := any(nil)
	if event.Usage.ProviderInputTokens != nil {
		providerInput = *event.Usage.ProviderInputTokens
	}
	return exec.Exec(`
		INSERT INTO cost_events (
			id, session_id, parent_session_id, run_id, timestamp,
			provider, source_kind, source_identity, transcript_identity, model,
			input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
			cache_write_5m_tokens, cache_write_1h_tokens, reasoning_tokens,
			provider_input_tokens, cost_microdollars, pricing_status,
			reconciliation_status
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.ID, event.SessionID, event.ParentSessionID, event.RunID,
		event.Timestamp.UTC().Format(time.RFC3339Nano), event.Provider, event.SourceKind,
		event.SourceIdentity, event.TranscriptIdentity, event.Model,
		event.Usage.InputTokens, event.Usage.OutputTokens, event.Usage.CacheReadTokens,
		event.Usage.CacheWriteTokens, event.Usage.CacheWrite5mTokens,
		event.Usage.CacheWrite1hTokens, event.Usage.ReasoningTokens, providerInput,
		event.CostMicrodollars, event.PricingStatus, event.ReconciliationStatus)
}

// TotalBySession returns aggregated costs for a session.
func (s *Store) TotalBySession(sessionID string) (CostSummary, error) {
	return s.querySum(`WHERE session_id = ?`, sessionID)
}

func (s *Store) CoveredTotalBySession(sessionID string) (CoveredSummary, error) {
	return s.queryCovered(`WHERE session_id = ?`, sessionID)
}

func (s *Store) CoveredTotalToday() (CoveredSummary, error) {
	return s.queryCovered(`WHERE timestamp >= date('now', 'start of day')`)
}

func (s *Store) CoveredTotalThisWeek() (CoveredSummary, error) {
	return s.queryCovered(`WHERE timestamp >= date('now', 'weekday 1', '-7 days')`)
}

func (s *Store) CoveredTotalThisMonth() (CoveredSummary, error) {
	return s.queryCovered(`WHERE timestamp >= date('now', 'start of month')`)
}

// TotalToday returns today's total costs.
func (s *Store) TotalToday() (CostSummary, error) {
	return s.querySum(`WHERE timestamp >= date('now', 'start of day')`)
}

// TotalThisWeek returns this week's total costs (Monday start).
func (s *Store) TotalThisWeek() (CostSummary, error) {
	return s.querySum(`WHERE timestamp >= date('now', 'weekday 1', '-7 days')`)
}

// TotalThisMonth returns this month's total costs.
func (s *Store) TotalThisMonth() (CostSummary, error) {
	return s.querySum(`WHERE timestamp >= date('now', 'start of month')`)
}

// TotalYesterday returns the prior day's total costs (00:00:00 UTC of
// yesterday inclusive to 00:00:00 UTC of today exclusive).
func (s *Store) TotalYesterday() (CostSummary, error) {
	return s.querySum(`WHERE timestamp >= date('now', 'start of day', '-1 day')
		AND timestamp < date('now', 'start of day')`)
}

// TotalLastWeek returns the prior ISO-week's total costs (Monday start).
// Boundaries are computed in Go from the injected clock so the result is
// stable across the Monday UTC tick — SQLite's `date('now', 'weekday 1')`
// is a no-op on Monday and shifts the window by 7 days, producing the
// week-before-last instead of last week (#932).
func (s *Store) TotalLastWeek() (CostSummary, error) {
	now := s.now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	daysSinceMonday := (int(today.Weekday()) + 6) % 7 // Mon=0..Sun=6
	thisMonday := today.AddDate(0, 0, -daysSinceMonday)
	lastMonday := thisMonday.AddDate(0, 0, -7)
	return s.querySum(`WHERE timestamp >= ? AND timestamp < ?`,
		lastMonday.Format(time.RFC3339), thisMonday.Format(time.RFC3339))
}

// TotalLastMonth returns the prior calendar month's total costs.
func (s *Store) TotalLastMonth() (CostSummary, error) {
	return s.querySum(`WHERE timestamp >= date('now', 'start of month', '-1 month')
		AND timestamp < date('now', 'start of month')`)
}

// TopSessionsByCost returns the top N sessions by total cost.
// Joins with instances table to get session titles and groups.
func (s *Store) TopSessionsByCost(limit int) ([]SessionCost, error) {
	rows, err := s.db.Query(`
		SELECT ce.session_id, COALESCE(i.title, ce.session_id), COALESCE(i.group_path, ''),
			SUM(ce.cost_microdollars), COUNT(*)
		FROM cost_events ce
		LEFT JOIN instances i ON ce.session_id = i.id
		GROUP BY ce.session_id
		ORDER BY SUM(ce.cost_microdollars) DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []SessionCost
	for rows.Next() {
		var sc SessionCost
		if err := rows.Scan(&sc.SessionID, &sc.SessionTitle, &sc.Group, &sc.CostMicrodollars, &sc.EventCount); err != nil {
			return nil, err
		}
		result = append(result, sc)
	}
	return result, rows.Err()
}

// CostByModel returns total cost per model.
func (s *Store) CostByModel() (map[string]int64, error) {
	rows, err := s.db.Query(`
		SELECT model, SUM(cost_microdollars)
		FROM cost_events
		GROUP BY model`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]int64)
	for rows.Next() {
		var model string
		var cost int64
		if err := rows.Scan(&model, &cost); err != nil {
			return nil, err
		}
		result[model] = cost
	}
	return result, rows.Err()
}

// TotalByDateRange returns daily costs within a date range.
func (s *Store) TotalByDateRange(from, to time.Time) ([]DailyCost, error) {
	rows, err := s.db.Query(`
		SELECT date(timestamp), SUM(cost_microdollars)
		FROM cost_events
		WHERE timestamp >= ? AND timestamp < ?
		GROUP BY date(timestamp)
		ORDER BY date(timestamp)`,
		from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []DailyCost
	for rows.Next() {
		var dateStr string
		var dc DailyCost
		if err := rows.Scan(&dateStr, &dc.CostMicrodollars); err != nil {
			return nil, err
		}
		dc.Date, _ = time.Parse("2006-01-02", dateStr)
		result = append(result, dc)
	}
	return result, rows.Err()
}

// ProjectedMonthly estimates monthly spend based on rolling 7-day average.
func (s *Store) ProjectedMonthly() (int64, error) {
	var total sql.NullInt64
	err := s.db.QueryRow(`
		SELECT SUM(cost_microdollars)
		FROM cost_events
		WHERE timestamp >= datetime('now', '-7 days')`).Scan(&total)
	if err != nil {
		return 0, err
	}
	if !total.Valid {
		return 0, nil
	}
	dailyAvg := total.Int64 / 7
	return dailyAvg * 30, nil
}

// PurgeOlderThan deletes events older than the given number of days. Returns count deleted.
func (s *Store) PurgeOlderThan(days int) (int64, error) {
	result, err := s.db.Exec(`
		DELETE FROM cost_events
		WHERE timestamp < datetime('now', ? || ' days')`,
		fmt.Sprintf("-%d", days))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// RunningTotal returns the sum of costs for a session within a time window (for use in a transaction).
func (s *Store) RunningTotal(tx *sql.Tx, sessionID string, since time.Time) (int64, error) {
	var total sql.NullInt64
	err := tx.QueryRow(`
		SELECT SUM(cost_microdollars) FROM cost_events
		WHERE session_id = ? AND timestamp >= ?
			AND pricing_status IN (?, ?) AND reconciliation_status <> ?`,
		sessionID, since.UTC().Format(time.RFC3339), PricingKnown, PricingKnownZero, ReconciliationLegacySuperseded).Scan(&total)
	if err != nil {
		return 0, err
	}
	if !total.Valid {
		return 0, nil
	}
	return total.Int64, nil
}

// GlobalRunningTotal returns the sum of all costs within a time window (for use in a transaction).
func (s *Store) GlobalRunningTotal(tx *sql.Tx, since time.Time) (int64, error) {
	var total sql.NullInt64
	err := tx.QueryRow(`
		SELECT SUM(cost_microdollars) FROM cost_events
		WHERE timestamp >= ? AND pricing_status IN (?, ?) AND reconciliation_status <> ?`,
		since.UTC().Format(time.RFC3339), PricingKnown, PricingKnownZero, ReconciliationLegacySuperseded).Scan(&total)
	if err != nil {
		return 0, err
	}
	if !total.Valid {
		return 0, nil
	}
	return total.Int64, nil
}

// GroupRunningTotal returns the sum of costs for a set of sessions within a time window.
func (s *Store) GroupRunningTotal(tx *sql.Tx, sessionIDs []string, since time.Time) (int64, error) {
	if len(sessionIDs) == 0 {
		return 0, nil
	}
	placeholders := "?" + repeatArg(len(sessionIDs)-1)
	// #nosec G201 -- placeholders is "?, ?, ?" generated by repeatArg; all
	// values flow through args[], never interpolated into the SQL string.
	query := fmt.Sprintf(`SELECT COALESCE(SUM(cost_microdollars), 0) FROM cost_events WHERE session_id IN (%s) AND timestamp >= ? AND pricing_status IN (?, ?) AND reconciliation_status <> ?`, placeholders)
	args := make([]any, len(sessionIDs)+4)
	for i, id := range sessionIDs {
		args[i] = id
	}
	args[len(sessionIDs)] = since.UTC().Format(time.RFC3339)
	args[len(sessionIDs)+1] = PricingKnown
	args[len(sessionIDs)+2] = PricingKnownZero
	args[len(sessionIDs)+3] = ReconciliationLegacySuperseded
	var total int64
	err := tx.QueryRow(query, args...).Scan(&total)
	return total, err
}

func (s *Store) querySum(where string, args ...any) (CostSummary, error) {
	var cs CostSummary
	err := s.db.QueryRow(`
		SELECT COALESCE(SUM(cost_microdollars), 0),
			COALESCE(SUM(input_tokens + cache_read_tokens + cache_write_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0),
			COALESCE(SUM(cache_write_tokens), 0),
			COUNT(*)
		FROM cost_events `+where+` AND reconciliation_status <> ?`, append(args, ReconciliationLegacySuperseded)...).Scan(
		&cs.TotalCostMicrodollars,
		&cs.TotalInputTokens,
		&cs.TotalOutputTokens,
		&cs.TotalCacheReadTokens,
		&cs.TotalCacheWriteTokens,
		&cs.EventCount,
	)
	return cs, err
}

func (s *Store) queryCovered(where string, args ...any) (CoveredSummary, error) {
	var summary CoveredSummary
	tokens := `(input_tokens + cache_read_tokens + cache_write_tokens + output_tokens)`
	query := `
		SELECT
			COALESCE(SUM(CASE WHEN pricing_status IN (?, ?) THEN cost_microdollars ELSE 0 END), 0),
			COALESCE(SUM(input_tokens + cache_read_tokens + cache_write_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0),
			COALESCE(SUM(cache_write_tokens), 0),
			COUNT(*),
			COALESCE(SUM(` + tokens + `), 0),
			COALESCE(SUM(CASE WHEN pricing_status IN (?, ?) THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN pricing_status IN (?, ?) THEN ` + tokens + ` ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN pricing_status = ? THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN pricing_status = ? THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN pricing_status = ? OR reconciliation_status = ? THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN pricing_status = ? THEN ` + tokens + ` ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN pricing_status = ? OR reconciliation_status = ? THEN ` + tokens + ` ELSE 0 END), 0)
		FROM cost_events ` + where + ` AND reconciliation_status <> ?`
	queryArgs := []any{
		PricingKnown, PricingKnownZero,
		PricingKnown, PricingKnownZero,
		PricingKnown, PricingKnownZero,
		PricingKnownZero,
		PricingUnknown,
		PricingLegacyUnresolved, ReconciliationLegacyUnreconciled,
		PricingUnknown,
		PricingLegacyUnresolved, ReconciliationLegacyUnreconciled,
	}
	queryArgs = append(queryArgs, args...)
	queryArgs = append(queryArgs, ReconciliationLegacySuperseded)
	err := s.db.QueryRow(query, queryArgs...).Scan(
		&summary.TotalCostMicrodollars,
		&summary.TotalInputTokens,
		&summary.TotalOutputTokens,
		&summary.TotalCacheReadTokens,
		&summary.TotalCacheWriteTokens,
		&summary.EventCount,
		&summary.Coverage.TotalTokens,
		&summary.Coverage.KnownPriceEventCount,
		&summary.Coverage.KnownPriceTokens,
		&summary.Coverage.KnownZeroEventCount,
		&summary.Coverage.UnknownPriceEventCount,
		&summary.Coverage.UnreconciledEventCount,
		&summary.Coverage.UnknownPriceTokens,
		&summary.Coverage.UnreconciledTokens,
	)
	if err != nil {
		return CoveredSummary{}, err
	}
	summary.Coverage.EventCount = summary.EventCount
	summary.Coverage.CoverageKnown = true
	summary.Coverage.Complete = summary.Coverage.UnknownPriceEventCount == 0 && summary.Coverage.UnreconciledEventCount == 0
	return summary, nil
}

// DailyBySession returns daily costs for a specific session.
func (s *Store) DailyBySession(sessionID string, from, to time.Time) ([]DailyCost, error) {
	rows, err := s.db.Query(`
		SELECT date(timestamp), SUM(cost_microdollars)
		FROM cost_events
		WHERE session_id = ? AND timestamp >= ? AND timestamp < ?
		GROUP BY date(timestamp)
		ORDER BY date(timestamp)`,
		sessionID, from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []DailyCost
	for rows.Next() {
		var dateStr string
		var dc DailyCost
		if err := rows.Scan(&dateStr, &dc.CostMicrodollars); err != nil {
			return nil, err
		}
		dc.Date, _ = time.Parse("2006-01-02", dateStr)
		result = append(result, dc)
	}
	return result, rows.Err()
}

// CostByModelForSession returns cost per model for a specific session.
func (s *Store) CostByModelForSession(sessionID string) (map[string]int64, error) {
	rows, err := s.db.Query(`
		SELECT model, SUM(cost_microdollars)
		FROM cost_events
		WHERE session_id = ?
		GROUP BY model`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]int64)
	for rows.Next() {
		var model string
		var cost int64
		if err := rows.Scan(&model, &cost); err != nil {
			return nil, err
		}
		result[model] = cost
	}
	return result, rows.Err()
}

func repeatArg(n int) string {
	s := ""
	for i := 0; i < n; i++ {
		s += ", ?"
	}
	return s
}

// PageEventsAfter returns up to `limit` cost_events with rowid > afterRowID,
// ordered by rowid ascending, plus the rowid of the last returned row (or
// afterRowID itself if no rows were returned). Use 0 as the initial
// afterRowID. Cursor-based pagination is stable under concurrent inserts.
func (s *Store) PageEventsAfter(afterRowID int64, limit int) ([]CostEvent, int64, error) {
	rows, err := s.db.Query(`
		SELECT rowid, id, session_id, parent_session_id, run_id, timestamp,
			provider, source_kind, source_identity, transcript_identity, model,
			input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
			cache_write_5m_tokens, cache_write_1h_tokens, reasoning_tokens,
			provider_input_tokens, cost_microdollars, pricing_status,
			reconciliation_status
		FROM cost_events
		WHERE rowid > ?
		ORDER BY rowid ASC
		LIMIT ?`, afterRowID, limit)
	if err != nil {
		return nil, afterRowID, err
	}
	defer rows.Close()

	lastRowID := afterRowID
	var result []CostEvent
	for rows.Next() {
		var (
			rowid         int64
			ev            CostEvent
			ts            string
			providerInput sql.NullInt64
		)
		if err := rows.Scan(
			&rowid, &ev.ID, &ev.SessionID, &ev.ParentSessionID, &ev.RunID, &ts,
			&ev.Provider, &ev.SourceKind, &ev.SourceIdentity, &ev.TranscriptIdentity, &ev.Model,
			&ev.InputTokens, &ev.OutputTokens, &ev.CacheReadTokens, &ev.CacheWriteTokens,
			&ev.CacheWrite5mTokens, &ev.CacheWrite1hTokens, &ev.ReasoningTokens,
			&providerInput, &ev.CostMicrodollars, &ev.PricingStatus,
			&ev.ReconciliationStatus,
		); err != nil {
			return nil, afterRowID, err
		}
		if providerInput.Valid {
			value := providerInput.Int64
			ev.ProviderInputTokens = &value
		}
		ev.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
		lastRowID = rowid
		result = append(result, ev)
	}
	return result, lastRowID, rows.Err()
}

type PricingUpdate struct {
	CostMicrodollars int64
	Status           PricingStatus
}

// ApplyPricingUpdates atomically updates quote fields without changing event
// identity, tokens, attribution, or reconciliation state.
func (s *Store) ApplyPricingUpdates(ctx context.Context, updates map[string]PricingUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin pricing update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `UPDATE cost_events SET cost_microdollars = ?, pricing_status = ? WHERE id = ?`)
	if err != nil {
		return fmt.Errorf("prepare pricing update: %w", err)
	}
	defer stmt.Close()
	for id, update := range updates {
		if _, err := stmt.ExecContext(ctx, update.CostMicrodollars, update.Status, id); err != nil {
			return fmt.Errorf("update pricing %s: %w", id, err)
		}
	}
	return tx.Commit()
}

// ApplyCostUpdates writes a batch of cost_microdollars updates within a single
// transaction. The map key is cost_event id. Returns an error and rolls back
// on any failure; on success commits and returns nil.
func (s *Store) ApplyCostUpdates(ctx context.Context, updates map[string]int64) error {
	if len(updates) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `UPDATE cost_events SET cost_microdollars = ? WHERE id = ?`)
	if err != nil {
		return fmt.Errorf("prepare update: %w", err)
	}
	defer stmt.Close()

	for id, value := range updates {
		if _, err := stmt.ExecContext(ctx, value, id); err != nil {
			return fmt.Errorf("update %s: %w", id, err)
		}
	}
	return tx.Commit()
}
