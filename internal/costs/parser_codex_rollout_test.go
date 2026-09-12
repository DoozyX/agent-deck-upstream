package costs_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
)

func codexSource(path, identity string) costs.TranscriptSource {
	return costs.TranscriptSource{
		Provider: costs.ProviderCodex, Kind: costs.SourceKindCodexRollout,
		Identity: identity, Path: path, SessionID: "deck-session", RunID: "run-1",
	}
}

func TestCodexRolloutCumulativeModelSwitchResetAndReplay(t *testing.T) {
	path := filepath.Join("testdata", "codex", "cumulative_model_reset.jsonl")
	result, err := (&costs.CodexRolloutParser{}).Parse(context.Background(), codexSource(path, "rollout-main"), costs.ScanCheckpoint{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 3 {
		t.Fatalf("events = %d, want 3; warnings=%v", len(result.Events), result.Warnings)
	}
	want := []struct {
		model                                 string
		input, read, write, output, reasoning int64
	}{
		{"gpt-a", 30, 60, 10, 20, 5},
		{"gpt-b", 20, 40, 0, 15, 3},
		{"gpt-b", 15, 5, 0, 4, 1},
	}
	for i, expected := range want {
		event := result.Events[i]
		if event.Model != expected.model || event.Usage.InputTokens != expected.input || event.Usage.CacheReadTokens != expected.read ||
			event.Usage.CacheWriteTokens != expected.write || event.Usage.OutputTokens != expected.output || event.Usage.ReasoningTokens != expected.reasoning {
			t.Fatalf("event[%d] = model=%q usage=%+v, want %+v", i, event.Model, event.Usage, expected)
		}
		if event.SourceIdentity == "" || event.TranscriptIdentity != "rollout-main" {
			t.Fatalf("event[%d] identity = %q transcript=%q", i, event.SourceIdentity, event.TranscriptIdentity)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if result.Checkpoint.Offset != info.Size() || !result.Checkpoint.Complete {
		t.Fatalf("checkpoint = %+v, want offset %d complete", result.Checkpoint, info.Size())
	}

	replay, err := (&costs.CodexRolloutParser{}).Parse(context.Background(), codexSource(path, "rollout-main"), result.Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.Events) != 0 || replay.Checkpoint.Offset != result.Checkpoint.Offset {
		t.Fatalf("unchanged replay = events=%d checkpoint=%+v", len(replay.Events), replay.Checkpoint)
	}
}

func TestCodexRolloutForkSkipsInheritedBaseline(t *testing.T) {
	path := filepath.Join("testdata", "codex", "fork.jsonl")
	result, err := (&costs.CodexRolloutParser{}).Parse(context.Background(), codexSource(path, "rollout-fork"), costs.ScanCheckpoint{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("events = %d, want 1; warnings=%v", len(result.Events), result.Warnings)
	}
	usage := result.Events[0].Usage
	if usage.InputTokens != 10 || usage.CacheReadTokens != 20 || usage.OutputTokens != 5 || usage.ReasoningTokens != 1 {
		t.Fatalf("fork delta = %+v", usage)
	}
}

func TestCodexRolloutInvalidCountersAndBlockedStatus(t *testing.T) {
	path := filepath.Join("testdata", "codex", "invalid_and_blocked.jsonl")
	result, err := (&costs.CodexRolloutParser{}).Parse(context.Background(), codexSource(path, "rollout-invalid"), costs.ScanCheckpoint{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 0 || len(result.Warnings) != 1 {
		t.Fatalf("events=%d warnings=%v", len(result.Events), result.Warnings)
	}
	wantReset := time.Unix(1788278400, 0).UTC()
	if result.BlockedStatus != "primary" || !result.BlockedResetKnown || !result.BlockedUntil.Equal(wantReset) {
		t.Fatalf("blocked result = status=%q known=%v until=%s", result.BlockedStatus, result.BlockedResetKnown, result.BlockedUntil)
	}
}

func TestCodexRolloutPartialTrailingLineDoesNotAdvance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	complete := []byte("{\"timestamp\":\"2026-09-01T15:00:00Z\",\"type\":\"turn_context\",\"payload\":{\"model\":\"gpt-partial\"}}\n")
	partial := []byte("{\"timestamp\":\"2026-09-01T15:00:01Z\",\"type\":\"event_msg\"")
	if err := os.WriteFile(path, append(complete, partial...), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := (&costs.CodexRolloutParser{}).Parse(context.Background(), codexSource(path, "rollout-partial"), costs.ScanCheckpoint{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Checkpoint.Offset != int64(len(complete)) || len(result.Warnings) != 0 {
		t.Fatalf("partial checkpoint=%d warnings=%v, want %d and no warning", result.Checkpoint.Offset, result.Warnings, len(complete))
	}
}

func TestCodexRolloutAppendEmitsExactlyOneDelta(t *testing.T) {
	original, err := os.ReadFile(filepath.Join("testdata", "codex", "cumulative_model_reset.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	parser := &costs.CodexRolloutParser{}
	first, err := parser.Parse(context.Background(), codexSource(path, "rollout-append"), costs.ScanCheckpoint{})
	if err != nil {
		t.Fatal(err)
	}
	appended := []byte(`{"timestamp":"2026-09-01T12:00:06Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":50,"cached_input_tokens":25,"cache_write_input_tokens":0,"output_tokens":10,"reasoning_output_tokens":3,"total_tokens":60}}}}` + "\n")
	if err := os.WriteFile(path, append(original, appended...), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := parser.Parse(context.Background(), codexSource(path, "rollout-append"), first.Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Events) != 1 {
		t.Fatalf("appended events = %d, want 1; warnings=%v", len(second.Events), second.Warnings)
	}
	usage := second.Events[0].Usage
	if usage.InputTokens != 10 || usage.CacheReadTokens != 20 || usage.OutputTokens != 6 || usage.ReasoningTokens != 2 {
		t.Fatalf("appended delta = %+v", usage)
	}
}

func TestCodexRolloutUnknownResetUsesBlockedBackoff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blocked.jsonl")
	line := []byte(`{"timestamp":"2026-09-01T15:00:00Z","type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{"rate_limit_reached_type":"primary","primary":{}}}}` + "\n")
	if err := os.WriteFile(path, line, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := (&costs.CodexRolloutParser{}).Parse(context.Background(), codexSource(path, "rollout-blocked"), costs.ScanCheckpoint{})
	if err != nil {
		t.Fatal(err)
	}
	if result.BlockedStatus != "primary" || result.BlockedResetKnown || result.BlockedBackoff <= 0 {
		t.Fatalf("unknown reset result = status=%q known=%v backoff=%s", result.BlockedStatus, result.BlockedResetKnown, result.BlockedBackoff)
	}
}

func TestCodexRolloutTerminalFallbackIsCoverageLimited(t *testing.T) {
	collector := costs.NewCollector(costs.NewPricer(costs.PricerConfig{}))
	events, err := collector.Collect("codex", "session", "Tokens used: 100 prompt + 5 completion (gpt-4.1)")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("fallback events = %d", len(events))
	}
	if events[0].Provider != costs.ProviderCodex || events[0].SourceKind != costs.SourceKindCodexTerminalLimited {
		t.Fatalf("fallback metadata = provider=%q source_kind=%q", events[0].Provider, events[0].SourceKind)
	}
}

func TestCodexRolloutSingleCounterRegressionDoesNotRebillUnaffectedCounters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	data := []byte(
		`{"timestamp":"2026-09-01T18:00:00Z","type":"turn_context","payload":{"model":"gpt-5.6-sol"}}` + "\n" +
			`{"timestamp":"2026-09-01T18:00:01Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":10,"reasoning_output_tokens":3}}}}` + "\n" +
			`{"timestamp":"2026-09-01T18:00:02Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":120,"cached_input_tokens":20,"output_tokens":12,"reasoning_output_tokens":0}}}}` + "\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := (&costs.CodexRolloutParser{}).Parse(context.Background(), codexSource(path, "codex:single-regression"), costs.ScanCheckpoint{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 2 {
		t.Fatalf("events=%d warnings=%v", len(result.Events), result.Warnings)
	}
	usage := result.Events[1].Usage
	if usage.InputTokens != 20 || usage.CacheReadTokens != 0 || usage.OutputTokens != 2 || usage.ReasoningTokens != 0 {
		t.Fatalf("regression delta=%+v warnings=%v", usage, result.Warnings)
	}
	if result.Complete || len(result.Warnings) == 0 {
		t.Fatalf("ambiguous regression must leave a visible coverage gap: complete=%v warnings=%v", result.Complete, result.Warnings)
	}
}

func TestCodexRolloutInputOutputOnlyResetStartsNewSegment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	body := `{"timestamp":"2026-09-01T12:00:00Z","type":"turn_context","payload":{"model":"gpt-5.6-sol"}}` + "\n" +
		`{"timestamp":"2026-09-01T12:00:01Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"output_tokens":20}}}}` + "\n" +
		`{"timestamp":"2026-09-01T12:00:02Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":10,"output_tokens":2}}}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := (&costs.CodexRolloutParser{}).Parse(context.Background(), codexSource(path, "codex:sparse-reset"), costs.ScanCheckpoint{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 2 || result.Events[1].Usage.InputTokens != 10 || result.Events[1].Usage.OutputTokens != 2 {
		t.Fatalf("events=%+v warnings=%v complete=%v; want reset event input=10 output=2", result.Events, result.Warnings, result.Complete)
	}
	if !result.Complete || len(result.Warnings) != 0 {
		t.Fatalf("coordinated sparse reset must remain complete: complete=%v warnings=%v", result.Complete, result.Warnings)
	}
}
