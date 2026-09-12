package costs

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
)

// TranscriptEntry represents one line of a Claude transcript JSONL file.
// Handles both "assistant" entries (direct usage) and "progress" entries (subagent usage).
type TranscriptEntry struct {
	Type    string `json:"type"`
	UUID    string `json:"uuid"`
	Message struct {
		Model string `json:"model"`
		Usage struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
	// Progress entries nest usage inside data.message.message
	Data      *progressData `json:"data,omitempty"`
	Timestamp string        `json:"timestamp"` // ISO 8601
}

type progressData struct {
	Message struct {
		Timestamp string `json:"timestamp"`
		Message   struct {
			Model string `json:"model"`
			Usage struct {
				InputTokens              int64 `json:"input_tokens"`
				OutputTokens             int64 `json:"output_tokens"`
				CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
				CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			} `json:"usage"`
		} `json:"message"`
	} `json:"message"`
}

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
		ingested, err := store.Ingest(ctx, parsed.Events, []ScanCheckpoint{parsed.Checkpoint})
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
	const window = int64(4096)
	firstLength := boundary
	if firstLength > window {
		firstLength = window
	}
	if firstLength > 0 {
		if _, err := io.CopyN(hash, file, firstLength); err != nil {
			return "", err
		}
	}
	if boundary > window {
		if _, err := file.Seek(boundary-window, io.SeekStart); err != nil {
			return "", err
		}
		if _, err := io.CopyN(hash, file, window); err != nil {
			return "", err
		}
	}
	var device, inode uint64
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		device, inode = uint64(stat.Dev), uint64(stat.Ino)
	}
	return fmt.Sprintf("v1:%d:%d:%d:%s", device, inode, boundary, hex.EncodeToString(hash.Sum(nil))), nil
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

// SyncSession holds the info needed to locate a session's transcript.
type SyncSession struct {
	InstanceID      string
	ClaudeSessionID string
	ProjectPath     string
	Tool            string
}

// SyncFromTranscripts reads historical usage from Claude transcript files
// and backfills cost_events for managed sessions.
func SyncFromTranscripts(store *Store, pricer *Pricer, sessions []SyncSession) SyncResult {
	var result SyncResult

	home, err := os.UserHomeDir()
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("get home dir: %v", err))
		return result
	}

	// Collect existing event IDs to avoid duplicates
	existing := make(map[string]bool)

	for _, sess := range sessions {
		if sess.Tool != "claude" || sess.ClaudeSessionID == "" {
			continue
		}

		result.SessionsScanned++

		// Derive transcript path: ~/.claude/projects/<slugified-path>/<session-id>.jsonl
		sluggedPath := slugifyProjectPath(sess.ProjectPath)
		transcriptPath := filepath.Join(home, ".claude", "projects", sluggedPath, sess.ClaudeSessionID+".jsonl")

		if _, err := os.Stat(transcriptPath); os.IsNotExist(err) {
			continue
		}

		events, errs := parseTranscriptFile(transcriptPath, sess.InstanceID, pricer)
		result.Errors = append(result.Errors, errs...)

		for _, ev := range events {
			// Check if we already have this event (by a deterministic ID)
			dedupKey := fmt.Sprintf("%s_%s", sess.InstanceID, ev.dedupKey)
			if existing[dedupKey] {
				result.EventsSkipped++
				continue
			}

			// Check if already in database
			var count int
			if err := store.db.QueryRow("SELECT COUNT(*) FROM cost_events WHERE id = ?", dedupKey).Scan(&count); err != nil {
				continue
			}
			if count > 0 {
				result.EventsSkipped++
				existing[dedupKey] = true
				continue
			}

			costEvent := CostEvent{
				ID:               dedupKey,
				SessionID:        sess.InstanceID,
				Timestamp:        ev.timestamp,
				Model:            ev.model,
				InputTokens:      ev.inputTokens,
				OutputTokens:     ev.outputTokens,
				CacheReadTokens:  ev.cacheReadTokens,
				CacheWriteTokens: ev.cacheWriteTokens,
				CostMicrodollars: pricer.ComputeCost(ev.model, ev.inputTokens, ev.outputTokens, ev.cacheReadTokens, ev.cacheWriteTokens),
			}

			if err := store.WriteCostEvent(costEvent); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("write event: %v", err))
				continue
			}
			existing[dedupKey] = true
			result.EventsImported++
		}
	}

	return result
}

type parsedUsage struct {
	dedupKey         string
	timestamp        time.Time
	model            string
	inputTokens      int64
	outputTokens     int64
	cacheReadTokens  int64
	cacheWriteTokens int64
}

func parseTranscriptFile(path, instanceID string, pricer *Pricer) ([]parsedUsage, []string) {
	f, err := os.Open(path)
	if err != nil {
		return nil, []string{fmt.Sprintf("open %s: %v", path, err)}
	}
	defer f.Close()

	var results []parsedUsage
	var errors []string

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024) // 10MB max line

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var entry TranscriptEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue // skip unparseable lines
		}

		var model string
		var inputTok, outputTok, cacheRead, cacheWrite int64
		var tsStr string

		switch entry.Type {
		case "assistant":
			usage := entry.Message.Usage
			model = entry.Message.Model
			inputTok = usage.InputTokens
			outputTok = usage.OutputTokens
			cacheRead = usage.CacheReadInputTokens
			cacheWrite = usage.CacheCreationInputTokens
			tsStr = entry.Timestamp

		case "progress":
			if entry.Data == nil {
				continue
			}
			usage := entry.Data.Message.Message.Usage
			model = entry.Data.Message.Message.Model
			inputTok = usage.InputTokens
			outputTok = usage.OutputTokens
			cacheRead = usage.CacheReadInputTokens
			cacheWrite = usage.CacheCreationInputTokens
			tsStr = entry.Data.Message.Timestamp
			if tsStr == "" {
				tsStr = entry.Timestamp
			}

		default:
			continue
		}

		if inputTok == 0 && outputTok == 0 {
			continue
		}

		ts, err := time.Parse(time.RFC3339Nano, tsStr)
		if err != nil {
			ts = time.Now()
		}

		dedupKey := entry.UUID
		if dedupKey == "" {
			dedupKey = uuid.NewString()
		}

		results = append(results, parsedUsage{
			dedupKey:         dedupKey,
			timestamp:        ts,
			model:            model,
			inputTokens:      inputTok,
			outputTokens:     outputTok,
			cacheReadTokens:  cacheRead,
			cacheWriteTokens: cacheWrite,
		})
	}

	if err := scanner.Err(); err != nil {
		errors = append(errors, fmt.Sprintf("scan %s: %v", path, err))
	}

	return results, errors
}

// slugifyProjectPath converts a project path to Claude's directory slug format.
// /home/user/Documents/Projects/foo -> -home-user-Documents-Projects-foo
// Claude replaces / with - and also . with -, and trims trailing slashes.
func slugifyProjectPath(projectPath string) string {
	projectPath = strings.TrimRight(projectPath, "/")
	slug := strings.ReplaceAll(projectPath, "/", "-")
	slug = strings.ReplaceAll(slug, ".", "-")
	return slug
}
