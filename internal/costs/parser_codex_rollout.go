package costs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

const (
	ProviderCodex                  = "codex"
	SourceKindCodexRollout         = "codex_rollout"
	SourceKindCodexRateLimit       = "codex_rate_limit"
	SourceKindCodexTerminalLimited = "terminal_limited"
)

const codexUnknownResetBackoff = 5 * time.Minute

type CodexRolloutParser struct{}

var _ TranscriptParser = (*CodexRolloutParser)(nil)

type codexCounters struct {
	Input      int64 `json:"input"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
	Output     int64 `json:"output"`
	Reasoning  int64 `json:"reasoning"`
}

type codexCheckpointState struct {
	Model             string         `json:"model,omitempty"`
	Previous          *codexCounters `json:"previous,omitempty"`
	Segment           int            `json:"segment,omitempty"`
	ForkBaseline      bool           `json:"fork_baseline,omitempty"`
	ForkBaselineTaken bool           `json:"fork_baseline_taken,omitempty"`
}

type codexRolloutRecord struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexTurnContext struct {
	Model string `json:"model"`
}

type codexSessionMeta struct {
	ForkedFromID string          `json:"forked_from_id"`
	Source       json.RawMessage `json:"source"`
}

type codexTokenCount struct {
	Type string `json:"type"`
	Info *struct {
		Total codexTokenUsage `json:"total_token_usage"`
	} `json:"info"`
	RateLimits struct {
		ReachedType string          `json:"rate_limit_reached_type"`
		Primary     *codexRateLimit `json:"primary"`
		Secondary   *codexRateLimit `json:"secondary"`
		Individual  *codexRateLimit `json:"individual_limit"`
	} `json:"rate_limits"`
}

type codexTokenUsage struct {
	Input      int64 `json:"input_tokens"`
	CacheRead  int64 `json:"cached_input_tokens"`
	CacheWrite int64 `json:"cache_write_input_tokens"`
	Output     int64 `json:"output_tokens"`
	Reasoning  int64 `json:"reasoning_output_tokens"`
}

type codexRateLimit struct {
	ResetsAt int64 `json:"resets_at"`
}

func (p *CodexRolloutParser) Parse(ctx context.Context, source TranscriptSource, checkpoint ScanCheckpoint) (ParseResult, error) {
	if source.Path == "" || source.Identity == "" {
		return ParseResult{}, fmt.Errorf("codex rollout source path and identity are required")
	}
	file, err := os.Open(source.Path)
	if err != nil {
		return ParseResult{}, fmt.Errorf("open codex rollout: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return ParseResult{}, fmt.Errorf("stat codex rollout: %w", err)
	}

	result := ParseResult{Checkpoint: checkpoint}
	result.Checkpoint.Provider = ProviderCodex
	result.Checkpoint.SourceKind = SourceKindCodexRollout
	result.Checkpoint.SourceIdentity = source.Identity
	result.Checkpoint.Complete = true
	result.Checkpoint.UpdatedAt = info.ModTime().UTC()

	state := codexCheckpointState{}
	if checkpoint.Fingerprint != "" {
		if err := json.Unmarshal([]byte(checkpoint.Fingerprint), &state); err != nil {
			return ParseResult{}, fmt.Errorf("decode codex checkpoint state: %w", err)
		}
	}
	start := checkpoint.Offset
	if start < 0 || start > info.Size() {
		result.Warnings = append(result.Warnings, fmt.Sprintf("checkpoint offset %d outside rollout size %d; rescanning", start, info.Size()))
		start = 0
		state = codexCheckpointState{}
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return ParseResult{}, fmt.Errorf("seek codex rollout: %w", err)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return ParseResult{}, fmt.Errorf("read codex rollout: %w", err)
	}

	consumed := int64(0)
	for len(data) > 0 {
		if err := ctx.Err(); err != nil {
			return ParseResult{}, err
		}
		newline := bytes.IndexByte(data, '\n')
		if newline < 0 {
			break
		}
		line := bytes.TrimSpace(data[:newline])
		recordOffset := start + consumed
		consumed += int64(newline + 1)
		data = data[newline+1:]
		result.Checkpoint.Offset = start + consumed
		if len(line) == 0 {
			continue
		}
		if warning := p.parseRecord(line, recordOffset, source, &state, &result); warning != "" {
			result.Warnings = append(result.Warnings, warning)
		}
	}
	fingerprint, err := json.Marshal(state)
	if err != nil {
		return ParseResult{}, fmt.Errorf("encode codex checkpoint state: %w", err)
	}
	result.Checkpoint.Fingerprint = string(fingerprint)
	return result, nil
}

func (p *CodexRolloutParser) parseRecord(line []byte, offset int64, source TranscriptSource, state *codexCheckpointState, result *ParseResult) string {
	var record codexRolloutRecord
	if err := json.Unmarshal(line, &record); err != nil {
		return fmt.Sprintf("malformed codex rollout record at byte %d", offset)
	}
	switch record.Type {
	case "turn_context":
		var turn codexTurnContext
		if err := json.Unmarshal(record.Payload, &turn); err != nil {
			return fmt.Sprintf("malformed codex turn context at byte %d", offset)
		}
		if turn.Model != "" {
			state.Model = turn.Model
		}
	case "session_meta":
		var metadata codexSessionMeta
		if err := json.Unmarshal(record.Payload, &metadata); err != nil {
			return fmt.Sprintf("malformed codex session metadata at byte %d", offset)
		}
		if metadata.ForkedFromID != "" || bytes.Contains(bytes.ToLower(metadata.Source), []byte(`"fork`)) {
			state.ForkBaseline = true
		}
	case "event_msg":
		return p.parseTokenCount(record, offset, source, state, result)
	}
	return ""
}

func (p *CodexRolloutParser) parseTokenCount(record codexRolloutRecord, offset int64, source TranscriptSource, state *codexCheckpointState, result *ParseResult) string {
	var message codexTokenCount
	if err := json.Unmarshal(record.Payload, &message); err != nil {
		return fmt.Sprintf("malformed codex token count at byte %d", offset)
	}
	if message.Type != "token_count" {
		return ""
	}
	if message.Info == nil {
		setCodexBlockedStatus(message, result)
		return ""
	}
	timestamp, err := time.Parse(time.RFC3339Nano, record.Timestamp)
	if err != nil {
		return fmt.Sprintf("codex token count at byte %d has invalid timestamp", offset)
	}
	current := codexCounters{
		Input: message.Info.Total.Input, CacheRead: message.Info.Total.CacheRead,
		CacheWrite: message.Info.Total.CacheWrite, Output: message.Info.Total.Output,
		Reasoning: message.Info.Total.Reasoning,
	}
	if state.ForkBaseline && !state.ForkBaselineTaken && state.Previous == nil {
		state.Previous = &current
		state.ForkBaselineTaken = true
		return ""
	}
	if state.Previous != nil && current == *state.Previous {
		return ""
	}
	delta := current
	if state.Previous != nil {
		if countersReset(current, *state.Previous) {
			state.Segment++
		} else {
			delta = subtractCounters(current, *state.Previous)
		}
	}
	state.Previous = &current

	providerInput := delta.Input
	usage := TokenUsage{
		InputTokens:  delta.Input - delta.CacheRead - delta.CacheWrite,
		OutputTokens: delta.Output, CacheReadTokens: delta.CacheRead,
		CacheWriteTokens: delta.CacheWrite, ReasoningTokens: delta.Reasoning,
		ProviderInputTokens: &providerInput,
	}
	if err := usage.Validate(); err != nil {
		return fmt.Sprintf("invalid codex token counters at byte %d: %v", offset, err)
	}
	identity := codexEventIdentity(source.Identity, state.Segment, current, record.Timestamp)
	result.Events = append(result.Events, UsageEvent{
		ID: identity, Provider: ProviderCodex, SourceKind: SourceKindCodexRollout,
		SourceIdentity: identity, TranscriptIdentity: source.Identity,
		SessionID: source.SessionID, ParentSessionID: source.ParentSessionID, RunID: source.RunID,
		Timestamp: timestamp.UTC(), Model: state.Model, Usage: usage,
		PricingStatus: PricingUnknown, ReconciliationStatus: ReconciliationAuthoritative,
	})
	return ""
}

func setCodexBlockedStatus(message codexTokenCount, result *ParseResult) {
	result.BlockedStatus = message.RateLimits.ReachedType
	if result.BlockedStatus == "" {
		result.BlockedStatus = "blocked"
	}
	var limit *codexRateLimit
	switch message.RateLimits.ReachedType {
	case "secondary":
		limit = message.RateLimits.Secondary
	case "individual":
		limit = message.RateLimits.Individual
	default:
		limit = message.RateLimits.Primary
	}
	if limit != nil && limit.ResetsAt > 0 {
		result.BlockedResetKnown = true
		result.BlockedUntil = time.Unix(limit.ResetsAt, 0).UTC()
		return
	}
	result.BlockedBackoff = codexUnknownResetBackoff
}

func countersReset(current, previous codexCounters) bool {
	return current.Input < previous.Input || current.CacheRead < previous.CacheRead ||
		current.CacheWrite < previous.CacheWrite || current.Output < previous.Output ||
		current.Reasoning < previous.Reasoning
}

func subtractCounters(current, previous codexCounters) codexCounters {
	return codexCounters{
		Input: current.Input - previous.Input, CacheRead: current.CacheRead - previous.CacheRead,
		CacheWrite: current.CacheWrite - previous.CacheWrite, Output: current.Output - previous.Output,
		Reasoning: current.Reasoning - previous.Reasoning,
	}
}

func codexEventIdentity(transcriptIdentity string, segment int, counters codexCounters, timestamp string) string {
	value := fmt.Sprintf("%s\x00%d\x00%d\x00%d\x00%d\x00%d\x00%d\x00%s",
		transcriptIdentity, segment, counters.Input, counters.CacheRead, counters.CacheWrite,
		counters.Output, counters.Reasoning, timestamp)
	sum := sha256.Sum256([]byte(value))
	return "codex:" + hex.EncodeToString(sum[:16])
}
