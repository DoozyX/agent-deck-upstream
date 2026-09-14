package costs_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/costs"
)

func claudeSource(path, identity, kind string) costs.TranscriptSource {
	return costs.TranscriptSource{
		Provider: costs.ProviderClaude, Kind: kind, Identity: identity, Path: path,
		SessionID: "deck-session", ParentSessionID: "parent", RunID: "run-1",
	}
}

func eventByModel(events []costs.UsageEvent, model string) []costs.UsageEvent {
	var result []costs.UsageEvent
	for _, event := range events {
		if event.Model == model {
			result = append(result, event)
		}
	}
	return result
}

func TestClaudeTranscriptDirectProgressStreamingCacheAndFallback(t *testing.T) {
	path := filepath.Join("testdata", "claude", "direct_progress_streaming.jsonl")
	parser := &costs.ClaudeTranscriptParser{}
	result, err := parser.Parse(context.Background(), claudeSource(path, "claude-parent", costs.SourceKindClaudeDirect), costs.ScanCheckpoint{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 4 {
		t.Fatalf("events=%d, want 4; warnings=%v", len(result.Events), result.Warnings)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "timestamp") {
		t.Fatalf("warnings=%v, want one timestamp warning", result.Warnings)
	}

	mainEvents := eventByModel(result.Events, "claude-test")
	if len(mainEvents) != 3 {
		t.Fatalf("claude-test events=%d", len(mainEvents))
	}
	stream := mainEvents[0]
	if stream.SourceIdentity != "claude:msg:msg-1" || stream.Usage.InputTokens != 2 || stream.Usage.CacheReadTokens != 8 ||
		stream.Usage.CacheWriteTokens != 10 || stream.Usage.CacheWrite5mTokens != 4 || stream.Usage.CacheWrite1hTokens != 6 ||
		stream.Usage.OutputTokens != 9 || stream.Usage.ReasoningTokens != 3 || stream.Usage.TotalInputTokens() != 20 || stream.Usage.CacheWriteUnknownTokens() != 0 {
		t.Fatalf("deduplicated stream event=%+v", stream)
	}
	cacheOnly := mainEvents[1]
	if cacheOnly.SourceIdentity != "claude:msg:msg-cache" || cacheOnly.Usage.TotalInputTokens() != 20 || cacheOnly.Usage.OutputTokens != 0 {
		t.Fatalf("cache-only event=%+v", cacheOnly)
	}
	if mainEvents[2].SourceIdentity == "" || !strings.HasPrefix(mainEvents[2].SourceIdentity, "claude:content:") {
		t.Fatalf("fallback identity=%q", mainEvents[2].SourceIdentity)
	}
	reparsed, err := parser.Parse(context.Background(), claudeSource(path, "claude-parent", costs.SourceKindClaudeDirect), costs.ScanCheckpoint{})
	if err != nil {
		t.Fatal(err)
	}
	if eventByModel(reparsed.Events, "claude-test")[2].SourceIdentity != mainEvents[2].SourceIdentity {
		t.Fatalf("fallback identity changed across replay")
	}

	child := eventByModel(result.Events, "claude-child")
	if len(child) != 1 || child[0].SourceKind != costs.SourceKindClaudeProgress || child[0].SourceIdentity != "claude:msg:msg-child" {
		t.Fatalf("progress child=%+v", child)
	}

	replay, err := parser.Parse(context.Background(), claudeSource(path, "claude-parent", costs.SourceKindClaudeDirect), result.Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.Events) != 0 || replay.Checkpoint.Offset != result.Checkpoint.Offset {
		t.Fatalf("replay events=%d checkpoint=%+v", len(replay.Events), replay.Checkpoint)
	}
}

func TestClaudeTranscriptAppendEmitsOneGenuineRequest(t *testing.T) {
	original, err := os.ReadFile(filepath.Join("testdata", "claude", "native_child.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "native.jsonl")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	parser := &costs.ClaudeTranscriptParser{}
	first, err := parser.Parse(context.Background(), claudeSource(path, "claude-append", costs.SourceKindClaudeNativeChild), costs.ScanCheckpoint{})
	if err != nil {
		t.Fatal(err)
	}
	appended := []byte(`{"type":"assistant","uuid":"native-new","requestId":"req-new","timestamp":"2026-09-01T16:00:05Z","message":{"id":"msg-new","model":"claude-child","usage":{"input_tokens":2,"cache_read_input_tokens":3,"cache_creation_input_tokens":4,"output_tokens":5}}}` + "\n")
	if err := os.WriteFile(path, append(original, appended...), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := parser.Parse(context.Background(), claudeSource(path, "claude-append", costs.SourceKindClaudeNativeChild), first.Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Events) != 1 || second.Events[0].Usage.TotalInputTokens() != 9 || second.Events[0].Usage.OutputTokens != 5 {
		t.Fatalf("appended events=%+v warnings=%v", second.Events, second.Warnings)
	}
}

func TestClaudeTranscriptNativeChildDeduplicatesWithParentProgress(t *testing.T) {
	parser := &costs.ClaudeTranscriptParser{}
	parent, err := parser.Parse(context.Background(), claudeSource(filepath.Join("testdata", "claude", "direct_progress_streaming.jsonl"), "claude-parent", costs.SourceKindClaudeDirect), costs.ScanCheckpoint{})
	if err != nil {
		t.Fatal(err)
	}
	native, err := parser.Parse(context.Background(), claudeSource(filepath.Join("testdata", "claude", "native_child.jsonl"), "claude-child-file", costs.SourceKindClaudeNativeChild), costs.ScanCheckpoint{})
	if err != nil {
		t.Fatal(err)
	}
	var parentChild costs.UsageEvent
	for _, event := range parent.Events {
		if event.SourceIdentity == "claude:msg:msg-child" {
			parentChild = event
		}
	}
	if len(native.Events) != 1 || native.Events[0].SourceKind != costs.SourceKindClaudeNativeChild || native.Events[0].SourceIdentity != parentChild.SourceIdentity {
		t.Fatalf("parent child=%+v native=%+v", parentChild, native.Events)
	}
	store := testStore(t)
	result, err := store.Ingest(context.Background(), append([]costs.UsageEvent{parentChild}, native.Events...), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Inserted != 1 || result.Duplicates != 1 {
		t.Fatalf("cross-source ingest=%+v", result)
	}
}

func TestClaudeTranscriptMalformedAndPartialDoNotAdvanceCheckpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude.jsonl")
	complete := []byte(`{"type":"assistant","uuid":"ok","timestamp":"2026-09-01T17:00:00Z","message":{"id":"msg-ok","model":"claude-test","usage":{"input_tokens":1,"output_tokens":1}}}` + "\n")
	malformed := []byte("{not-json}\n")
	partial := []byte(`{"type":"assistant"`)
	if err := os.WriteFile(path, append(append(complete, malformed...), partial...), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := (&costs.ClaudeTranscriptParser{}).Parse(context.Background(), claudeSource(path, "claude-malformed", costs.SourceKindClaudeDirect), costs.ScanCheckpoint{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 1 || result.Checkpoint.Offset != int64(len(complete)) || len(result.Warnings) != 2 {
		t.Fatalf("events=%d checkpoint=%d warnings=%v", len(result.Events), result.Checkpoint.Offset, result.Warnings)
	}
}

func TestClaudeTranscriptEqualTotalRetainsMostCompleteUsageDetails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude.jsonl")
	lines := []byte(
		`{"type":"assistant","uuid":"detail-a","requestId":"req-detail","timestamp":"2026-09-01T17:00:00Z","message":{"id":"msg-detail","model":"claude-test","usage":{"input_tokens":2,"cache_creation_input_tokens":10,"output_tokens":9,"cache_creation":{"ephemeral_5m_input_tokens":4,"ephemeral_1h_input_tokens":6},"output_tokens_details":{"thinking_tokens":3}}}}` + "\n" +
			`{"type":"assistant","uuid":"detail-b","requestId":"req-detail","timestamp":"2026-09-01T17:00:01Z","message":{"id":"msg-detail","model":"claude-test","usage":{"input_tokens":2,"cache_creation_input_tokens":10,"output_tokens":9}}}` + "\n")
	if err := os.WriteFile(path, lines, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := (&costs.ClaudeTranscriptParser{}).Parse(context.Background(), claudeSource(path, "claude:detail", costs.SourceKindClaudeDirect), costs.ScanCheckpoint{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("events=%d warnings=%v", len(result.Events), result.Warnings)
	}
	usage := result.Events[0].Usage
	if usage.CacheWrite5mTokens != 4 || usage.CacheWrite1hTokens != 6 || usage.ReasoningTokens != 3 {
		t.Fatalf("detail regressed: %+v", usage)
	}
}
