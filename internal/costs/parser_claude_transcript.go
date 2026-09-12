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
	ProviderClaude              = "claude"
	SourceKindClaudeDirect      = "claude_direct"
	SourceKindClaudeProgress    = "claude_progress"
	SourceKindClaudeNativeChild = "claude_native_child"
)

type ClaudeTranscriptParser struct{}

var _ TranscriptParser = (*ClaudeTranscriptParser)(nil)

type claudeTranscriptRecord struct {
	Type      string          `json:"type"`
	UUID      string          `json:"uuid"`
	RequestID string          `json:"requestId"`
	Timestamp string          `json:"timestamp"`
	Message   claudeMessage   `json:"message"`
	Data      *claudeProgress `json:"data"`
}

type claudeMessage struct {
	ID      string          `json:"id"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"`
	Usage   claudeUsage     `json:"usage"`
}

type claudeProgress struct {
	Message struct {
		Timestamp string        `json:"timestamp"`
		Message   claudeMessage `json:"message"`
	} `json:"message"`
}

type claudeUsage struct {
	Input         int64 `json:"input_tokens"`
	Output        int64 `json:"output_tokens"`
	CacheRead     int64 `json:"cache_read_input_tokens"`
	CacheWrite    int64 `json:"cache_creation_input_tokens"`
	CacheCreation struct {
		FiveMinute int64 `json:"ephemeral_5m_input_tokens"`
		OneHour    int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
	OutputDetails struct {
		Thinking int64 `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
}

type claudeCandidate struct {
	event UsageEvent
	score int64
}

func (p *ClaudeTranscriptParser) Parse(ctx context.Context, source TranscriptSource, checkpoint ScanCheckpoint) (ParseResult, error) {
	if source.Path == "" || source.Identity == "" {
		return ParseResult{}, fmt.Errorf("claude transcript source path and identity are required")
	}
	file, err := os.Open(source.Path)
	if err != nil {
		return ParseResult{}, fmt.Errorf("open claude transcript: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return ParseResult{}, fmt.Errorf("stat claude transcript: %w", err)
	}

	result := ParseResult{Checkpoint: checkpoint, Complete: true}
	result.Checkpoint.Provider = ProviderClaude
	result.Checkpoint.SourceKind = source.Kind
	result.Checkpoint.SourceIdentity = source.Identity
	result.Checkpoint.Complete = true
	result.Checkpoint.UpdatedAt = info.ModTime().UTC()
	start := checkpoint.Offset
	if start < 0 || start > info.Size() {
		result.Warnings = append(result.Warnings, fmt.Sprintf("checkpoint offset %d outside transcript size %d; rescanning", start, info.Size()))
		start = 0
		result.Checkpoint.Offset = 0
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return ParseResult{}, fmt.Errorf("seek claude transcript: %w", err)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return ParseResult{}, fmt.Errorf("read claude transcript: %w", err)
	}

	candidates := make(map[string]claudeCandidate)
	var order []string
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
		lineLength := int64(newline + 1)
		data = data[newline+1:]
		if len(line) == 0 {
			consumed += lineLength
			result.Checkpoint.Offset = start + consumed
			continue
		}
		candidate, warning, malformed := parseClaudeRecord(line, recordOffset, source)
		if warning != "" {
			result.Warnings = append(result.Warnings, warning)
		}
		if malformed {
			result.Complete = false
			break
		}
		consumed += lineLength
		result.Checkpoint.Offset = start + consumed
		if candidate == nil {
			continue
		}
		identity := candidate.event.SourceIdentity
		existing, found := candidates[identity]
		if !found {
			order = append(order, identity)
		}
		if !found || candidate.score >= existing.score {
			candidates[identity] = *candidate
		}
	}
	if len(bytes.TrimSpace(data)) > 0 {
		result.Complete = false
		result.Warnings = append(result.Warnings, fmt.Sprintf("partial claude transcript record at byte %d", start+consumed))
	}
	for _, identity := range order {
		result.Events = append(result.Events, candidates[identity].event)
	}
	return result, nil
}

func parseClaudeRecord(line []byte, offset int64, source TranscriptSource) (*claudeCandidate, string, bool) {
	var record claudeTranscriptRecord
	if err := json.Unmarshal(line, &record); err != nil {
		return nil, fmt.Sprintf("malformed claude transcript record at byte %d", offset), true
	}
	message := record.Message
	timestampText := record.Timestamp
	sourceKind := SourceKindClaudeDirect
	switch record.Type {
	case "assistant":
		if source.Kind == SourceKindClaudeNativeChild {
			sourceKind = SourceKindClaudeNativeChild
		}
	case "progress":
		if record.Data == nil {
			return nil, "", false
		}
		message = record.Data.Message.Message
		if record.Data.Message.Timestamp != "" {
			timestampText = record.Data.Message.Timestamp
		}
		sourceKind = SourceKindClaudeProgress
	default:
		return nil, "", false
	}

	usage := TokenUsage{
		InputTokens: message.Usage.Input, OutputTokens: message.Usage.Output,
		CacheReadTokens: message.Usage.CacheRead, CacheWriteTokens: message.Usage.CacheWrite,
		CacheWrite5mTokens: message.Usage.CacheCreation.FiveMinute,
		CacheWrite1hTokens: message.Usage.CacheCreation.OneHour,
		ReasoningTokens:    message.Usage.OutputDetails.Thinking,
	}
	if usage.TotalTokens() == 0 {
		return nil, "", false
	}
	if err := usage.Validate(); err != nil {
		return nil, fmt.Sprintf("invalid claude usage at byte %d: %v", offset, err), false
	}
	timestamp, err := time.Parse(time.RFC3339Nano, timestampText)
	if err != nil {
		return nil, fmt.Sprintf("claude usage at byte %d has invalid or missing timestamp", offset), false
	}
	identity := claudeMessageIdentity(record, message, source.Identity)
	event := UsageEvent{
		ID: identity, Provider: ProviderClaude, SourceKind: sourceKind,
		SourceIdentity: identity, TranscriptIdentity: source.Identity,
		SessionID: source.SessionID, ParentSessionID: source.ParentSessionID, RunID: source.RunID,
		Timestamp: timestamp.UTC(), Model: message.Model, Usage: usage,
		PricingStatus: PricingUnknown, ReconciliationStatus: ReconciliationAuthoritative,
	}
	return &claudeCandidate{event: event, score: usage.TotalTokens()}, "", false
}

func claudeMessageIdentity(record claudeTranscriptRecord, message claudeMessage, transcriptIdentity string) string {
	if message.ID != "" {
		return "claude:msg:" + message.ID
	}
	if record.RequestID != "" {
		return "claude:req:" + record.RequestID
	}
	if record.UUID != "" {
		return "claude:uuid:" + record.UUID
	}
	value := transcriptIdentity + "\x00" + message.Model + "\x00" + string(message.Content)
	sum := sha256.Sum256([]byte(value))
	return "claude:content:" + hex.EncodeToString(sum[:16])
}
