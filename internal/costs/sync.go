package costs

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"
)

// SyncResult holds the result of a historical sync operation.
type SyncResult struct {
	SessionsScanned  int
	SourcesScanned   int
	SourcesChanged   int
	EventsImported   int
	EventsSkipped    int
	EventsReconciled int
	Warnings         []CoverageWarning
	Receipts         []SyncReceipt
	Errors           []string
}

// Sync incrementally imports authoritative provider transcripts.
func Sync(ctx context.Context, store *Store, pricer *Pricer, sources []TranscriptSource) SyncResult {
	var result SyncResult
	if store == nil {
		result.Errors = append(result.Errors, "sync: store is nil")
		return result
	}
	if pricer == nil {
		result.Errors = append(result.Errors, "sync: pricer is nil")
		return result
	}
	parsers := map[string]TranscriptParser{
		ProviderClaude: &ClaudeTranscriptParser{},
		ProviderCodex:  &CodexRolloutParser{},
	}
	for _, source := range sources {
		result.SourcesScanned++
		parser := parsers[source.Provider]
		if parser == nil {
			result.Errors = append(result.Errors, fmt.Sprintf("sync %s: unsupported provider %q", source.Identity, source.Provider))
			continue
		}
		fileInfo, err := os.Stat(source.Path)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("sync %s: read source: %v", source.Identity, err))
			continue
		}
		checkpoint, found, err := loadScanCheckpoint(ctx, store, source)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("sync %s: load checkpoint: %v", source.Identity, err))
			continue
		}
		if found {
			fingerprint, fingerprintErr := sourceFingerprint(source.Path, checkpoint.Offset)
			if fingerprintErr != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("sync %s: fingerprint source: %v", source.Identity, fingerprintErr))
				continue
			}
			if checkpoint.SourceFingerprint == "" || fingerprint != checkpoint.SourceFingerprint {
				result.Warnings = append(result.Warnings, CoverageWarning{Provider: source.Provider, Source: source.Identity, Kind: "source_replaced", Message: "transcript identity changed; rescanning from the beginning"})
				checkpoint = ScanCheckpoint{}
				found = false
			}
		}
		if found && checkpoint.Offset == fileInfo.Size() && !fileInfo.ModTime().After(checkpoint.UpdatedAt) {
			if receipt, ok, receiptErr := loadSyncReceipt(ctx, store, source); receiptErr != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("sync %s: load receipt: %v", source.Identity, receiptErr))
			} else if ok {
				result.Receipts = append(result.Receipts, receipt)
				appendReceiptWarning(&result, receipt)
			}
			continue
		}
		if found && checkpoint.Offset == fileInfo.Size() && fileInfo.ModTime().After(checkpoint.UpdatedAt) {
			checkpoint = ScanCheckpoint{}
		}
		parsed, err := parser.Parse(ctx, source, checkpoint)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("sync %s: parse: %v", source.Identity, err))
			continue
		}
		result.SourcesChanged++
		for _, warning := range parsed.Warnings {
			result.Warnings = append(result.Warnings, CoverageWarning{
				Provider: source.Provider, Source: source.Identity, Kind: "parse", Message: warning,
			})
		}
		for i := range parsed.Events {
			applyEventPrice(&parsed.Events[i], pricer)
		}
		parsed.Checkpoint.Complete = parsed.Complete
		parsed.Checkpoint.CoverageSessionID = source.SessionID
		if len(parsed.Events) > 0 {
			parsed.Checkpoint.CoverageStart, parsed.Checkpoint.CoverageEnd = eventRange(parsed.Events)
		}
		parsed.Checkpoint.SourceFingerprint, err = sourceFingerprint(source.Path, parsed.Checkpoint.Offset)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("sync %s: fingerprint checkpoint: %v", source.Identity, err))
			continue
		}
		receipt := SyncReceipt{
			Provider: source.Provider, SourceKind: source.Kind, SourceIdentity: source.Identity,
			Account: source.Account, CoverageComplete: parsed.Complete,
			Warnings: append([]string(nil), parsed.Warnings...), UpdatedAt: time.Now().UTC(),
		}
		if parsed.BlockedStatus != "" {
			receipt.Status = parsed.BlockedStatus
			receipt.BlockedUntil = parsed.BlockedUntil
			receipt.ResetKnown = parsed.BlockedResetKnown
			receipt.Backoff = parsed.BlockedBackoff
		}
		parsed.Checkpoint.Receipt = &receipt
		ingested, err := store.IngestPriced(ctx, parsed.Events, []ScanCheckpoint{parsed.Checkpoint}, pricer)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("sync %s: ingest: %v", source.Identity, err))
			continue
		}
		result.EventsImported += ingested.Inserted
		result.EventsImported += ingested.Updated
		result.EventsSkipped += ingested.Duplicates
		result.EventsReconciled += ingested.Superseded
		result.Receipts = append(result.Receipts, receipt)
		appendReceiptWarning(&result, receipt)
	}
	return result
}

func appendReceiptWarning(result *SyncResult, receipt SyncReceipt) {
	if receipt.Status == "" {
		return
	}
	message := fmt.Sprintf("provider usage is blocked (%s)", receipt.Status)
	if receipt.ResetKnown && !receipt.BlockedUntil.IsZero() {
		message += "; resets at " + receipt.BlockedUntil.UTC().Format(time.RFC3339)
	} else if receipt.Backoff > 0 {
		message += "; retry after " + receipt.Backoff.String()
	}
	result.Warnings = append(result.Warnings, CoverageWarning{
		Provider: receipt.Provider, Source: receipt.SourceIdentity, Kind: "blocked", Message: message,
	})
}

func loadSyncReceipt(ctx context.Context, store *Store, source TranscriptSource) (SyncReceipt, bool, error) {
	var receipt SyncReceipt
	var blockedUntil, warningsJSON, updatedAt string
	var resetKnown, coverageComplete int
	var backoffSeconds int64
	err := store.db.QueryRowContext(ctx, `
		SELECT provider, source_kind, source_identity, account, blocked_status,
			blocked_until, reset_known, backoff_seconds, coverage_complete,
			warnings_json, updated_at
		FROM usage_sync_receipts
		WHERE provider = ? AND source_kind = ? AND source_identity = ?`,
		source.Provider, source.Kind, source.Identity).Scan(
		&receipt.Provider, &receipt.SourceKind, &receipt.SourceIdentity,
		&receipt.Account, &receipt.Status, &blockedUntil, &resetKnown,
		&backoffSeconds, &coverageComplete, &warningsJSON, &updatedAt)
	if err == sql.ErrNoRows {
		return SyncReceipt{}, false, nil
	}
	if err != nil {
		return SyncReceipt{}, false, err
	}
	receipt.ResetKnown = resetKnown != 0
	receipt.CoverageComplete = coverageComplete != 0
	receipt.Backoff = time.Duration(backoffSeconds) * time.Second
	if blockedUntil != "" {
		receipt.BlockedUntil, err = time.Parse(time.RFC3339Nano, blockedUntil)
		if err != nil {
			return SyncReceipt{}, false, err
		}
	}
	receipt.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return SyncReceipt{}, false, err
	}
	if err := json.Unmarshal([]byte(warningsJSON), &receipt.Warnings); err != nil {
		return SyncReceipt{}, false, err
	}
	return receipt, true, nil
}

func loadScanCheckpoint(ctx context.Context, store *Store, source TranscriptSource) (ScanCheckpoint, bool, error) {
	var checkpoint ScanCheckpoint
	var updatedAt string
	err := store.db.QueryRowContext(ctx, `
		SELECT provider, source_kind, source_identity, offset, fingerprint, source_fingerprint, updated_at
		FROM usage_scan_checkpoints
		WHERE provider = ? AND source_kind = ? AND source_identity = ?`,
		source.Provider, source.Kind, source.Identity).Scan(
		&checkpoint.Provider, &checkpoint.SourceKind, &checkpoint.SourceIdentity,
		&checkpoint.Offset, &checkpoint.Fingerprint, &checkpoint.SourceFingerprint, &updatedAt)
	if err == sql.ErrNoRows {
		return ScanCheckpoint{}, false, nil
	}
	if err != nil {
		return ScanCheckpoint{}, false, err
	}
	checkpoint.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return ScanCheckpoint{}, false, fmt.Errorf("invalid checkpoint timestamp: %w", err)
	}
	checkpoint.Complete = true
	return checkpoint, true, nil
}

func eventRange(events []UsageEvent) (time.Time, time.Time) {
	from, to := events[0].Timestamp, events[0].Timestamp
	for _, event := range events[1:] {
		if event.Timestamp.Before(from) {
			from = event.Timestamp
		}
		if event.Timestamp.After(to) {
			to = event.Timestamp
		}
	}
	return from, to
}

func sourceFingerprint(path string, boundary int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if boundary < 0 || boundary > info.Size() {
		return "", fmt.Errorf("checkpoint boundary %d outside source size %d", boundary, info.Size())
	}
	hash := sha256.New()
	if boundary > 0 {
		if _, err := io.CopyN(hash, file, boundary); err != nil {
			return "", err
		}
	}
	var device, inode uint64
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		device, inode = uint64(stat.Dev), uint64(stat.Ino)
	}
	return fmt.Sprintf("v2:%d:%d:%d:%s", device, inode, boundary, hex.EncodeToString(hash.Sum(nil))), nil
}

func applyEventPrice(event *UsageEvent, pricer *Pricer) {
	quote := pricer.Quote(event.Model, event.Usage)
	event.PricingStatus = quote.Status
	event.CostMicrodollars = quote.CostMicrodollars
}

func overlappingLegacyEventIDs(ctx context.Context, store *Store, sessionID string, events []UsageEvent) ([]string, error) {
	from := events[0].Timestamp
	to := events[0].Timestamp
	for _, event := range events[1:] {
		if event.Timestamp.Before(from) {
			from = event.Timestamp
		}
		if event.Timestamp.After(to) {
			to = event.Timestamp
		}
	}
	rows, err := store.db.QueryContext(ctx, `
		SELECT id FROM cost_events
		WHERE session_id = ? AND timestamp >= ? AND timestamp <= ?
			AND reconciliation_status = ?`,
		sessionID, from.UTC().Format(time.RFC3339Nano), to.UTC().Format(time.RFC3339Nano),
		ReconciliationLegacyUnreconciled)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
