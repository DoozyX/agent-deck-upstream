package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

func TestStopHookCostEventCarriesCanonicalClaudeIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	transcriptDir := filepath.Join(home, ".claude", "projects", "project")
	if err := os.MkdirAll(transcriptDir, 0o700); err != nil {
		t.Fatal(err)
	}
	transcriptPath := filepath.Join(transcriptDir, "native-hook.jsonl")
	line := []byte(`{"type":"assistant","uuid":"uuid-hook","requestId":"req-hook","timestamp":"2026-09-01T16:00:02Z","message":{"id":"msg-hook","model":"claude-sonnet-5","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":2,"cache_read_input_tokens":3,"cache_creation_input_tokens":4,"output_tokens":5,"cache_creation":{"ephemeral_5m_input_tokens":1,"ephemeral_1h_input_tokens":3},"output_tokens_details":{"thinking_tokens":2}}}}`)
	if err := os.WriteFile(transcriptPath, append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	stopPayload, err := json.Marshal(map[string]string{"hook_event_name": "Stop", "transcript_path": transcriptPath})
	if err != nil {
		t.Fatal(err)
	}
	writeCostEvent("instance-hook", stopPayload)

	entries, err := os.ReadDir(getCostEventsDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("cost event files=%d, want 1", len(entries))
	}
	data, err := os.ReadFile(filepath.Join(getCostEventsDir(), entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatal(err)
	}
	wantTimestamp := time.Date(2026, 9, 1, 16, 0, 2, 0, time.UTC).UnixNano()
	if event["provider"] != "claude" || event["source_kind"] != "hook" ||
		event["source_identity"] != "claude:msg:msg-hook" || event["transcript_identity"] != "claude:native-hook" ||
		event["ts"] != float64(wantTimestamp) || event["cache_write_5m_tokens"] != float64(1) ||
		event["cache_write_1h_tokens"] != float64(3) || event["reasoning_tokens"] != float64(2) {
		t.Fatalf("hook identity/usage payload=%s", data)
	}
	aliases, ok := event["source_aliases"].([]any)
	if !ok || len(aliases) != 3 || aliases[0] != "claude:msg:msg-hook" || aliases[1] != "claude:req:req-hook" || aliases[2] != "claude:uuid:uuid-hook" {
		t.Fatalf("hook aliases=%v payload=%s", event["source_aliases"], data)
	}
}

func TestCostsSyncRejectsMalformedConfigWithoutLeakingContents(t *testing.T) {
	home := t.TempDir()
	writeCostsConfig(t, home, "[profiles.work.codex\nconfig_dir = \"super-secret-transcript-home\"\n")
	out, err := runCostsIngestionCLI(t, home, nil, "-p", "work", "costs", "sync")
	if err == nil {
		t.Fatalf("malformed config exited zero:\n%s", out)
	}
	if !strings.Contains(out, "Error: failed to load user config") || strings.Contains(out, "super-secret-transcript-home") {
		t.Fatalf("config diagnostic was absent or unsanitized:\n%s", out)
	}
}

func TestCostsSyncRejectsExplicitMissingProviderHome(t *testing.T) {
	home := t.TempDir()
	missing := filepath.Join(home, "private-account-home")
	writeCostsConfig(t, home, fmt.Sprintf("[profiles.work.codex]\nconfig_dir = %q\n", missing))
	out, err := runCostsIngestionCLI(t, home, nil, "-p", "work", "costs", "sync")
	if err == nil {
		t.Fatalf("explicit missing home exited zero:\n%s", out)
	}
	if !strings.Contains(out, "configured codex home for account \"work\" does not exist") || strings.Contains(out, missing) {
		t.Fatalf("missing-home diagnostic was absent or unsanitized:\n%s", out)
	}
}

func TestCostsSyncUsesSelectedProfileAndPerInstanceCodexHomeOnly(t *testing.T) {
	home := t.TempDir()
	selectedHome := filepath.Join(home, "codex-selected")
	accountHome := filepath.Join(home, "codex-account")
	unrelatedHome := filepath.Join(home, "codex-unrelated")
	for _, dir := range []string{selectedHome, accountHome, unrelatedHome, filepath.Join(home, ".claude", "projects")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	accountSession := "11111111-1111-1111-1111-111111111111"
	unrelatedSession := "22222222-2222-2222-2222-222222222222"
	writeCodexUsageRollout(t, accountHome, accountSession, 10, 2, 3)
	writeCodexUsageRollout(t, unrelatedHome, unrelatedSession, 20, 4, 6)
	writeCostsConfig(t, home, fmt.Sprintf(
		"[profiles.work.codex]\nconfig_dir = %q\n[profiles.account-a.codex]\nconfig_dir = %q\n[profiles.unrelated.codex]\nconfig_dir = %q\n",
		selectedHome, accountHome, unrelatedHome))
	instance := &session.Instance{
		ID: "managed-codex", Title: "managed-codex", ProjectPath: home,
		Command: "codex", Tool: "codex", Status: session.StatusIdle,
		Account: "account-a", CodexSessionID: accountSession, CreatedAt: time.Now(),
	}
	out, err := runCostsIngestionCLI(t, home, instance, "-p", "work", "costs", "sync")
	if err != nil {
		t.Fatalf("costs sync failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "from 1 Claude/Codex transcript source(s)") || !strings.Contains(out, "Events imported:  1") {
		t.Fatalf("profile scope imported wrong sources:\n%s", out)
	}
	db := openCostsCLIState(t, home, "work")
	var count int
	var transcriptIdentity, sessionID string
	if err := db.DB().QueryRow(`SELECT COUNT(*), MIN(transcript_identity), MIN(session_id) FROM cost_events`).Scan(&count, &transcriptIdentity, &sessionID); err != nil {
		t.Fatal(err)
	}
	if count != 1 || transcriptIdentity != "codex:"+accountSession || sessionID != instance.ID {
		t.Fatalf("ledger count=%d transcript=%q session=%q", count, transcriptIdentity, sessionID)
	}
	var receiptAccount string
	if err := db.DB().QueryRow(`SELECT account FROM usage_sync_receipts WHERE source_identity = ?`, "codex:"+accountSession).Scan(&receiptAccount); err != nil {
		t.Fatal(err)
	}
	if receiptAccount != "account-a" {
		t.Fatalf("receipt account=%q", receiptAccount)
	}
}

func TestCostsSyncPrintsAndPersistsBlockedResetReceipt(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, "codex-work")
	if err := os.MkdirAll(filepath.Join(home, ".claude", "projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	sessionID := "33333333-3333-3333-3333-333333333333"
	writeCodexBlockedRollout(t, codexHome, sessionID)
	writeCostsConfig(t, home, fmt.Sprintf("[profiles.work.codex]\nconfig_dir = %q\n", codexHome))
	out, err := runCostsIngestionCLI(t, home, nil, "-p", "work", "costs", "sync")
	if err != nil {
		t.Fatalf("blocked receipt sync failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "provider usage is blocked (primary); resets at 2026-09-02T00:00:00Z") {
		t.Fatalf("blocked reset missing from CLI:\n%s", out)
	}
	db := openCostsCLIState(t, home, "work")
	var status, blockedUntil string
	var resetKnown int
	if err := db.DB().QueryRow(`SELECT blocked_status, blocked_until, reset_known FROM usage_sync_receipts WHERE source_identity = ?`, "codex:"+sessionID).Scan(&status, &blockedUntil, &resetKnown); err != nil {
		t.Fatal(err)
	}
	if status != "primary" || blockedUntil != "2026-09-02T00:00:00Z" || resetKnown != 1 {
		t.Fatalf("receipt status=%q until=%q known=%d", status, blockedUntil, resetKnown)
	}
}

func writeCostsConfig(t *testing.T, home, body string) {
	t.Helper()
	path := filepath.Join(home, ".config", "agent-deck", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeCodexUsageRollout(t *testing.T, home, sessionID string, input, cached, output int64) {
	t.Helper()
	path := filepath.Join(home, "sessions", "2026", "09", "01", "rollout-2026-09-01T12-00-00-"+sessionID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(
		`{"timestamp":"2026-09-01T12:00:00Z","type":"turn_context","payload":{"model":"gpt-5.6-sol"}}`+"\n"+
			`{"timestamp":"2026-09-01T12:00:01Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,"output_tokens":%d}}}}`+"\n",
		input, cached, output)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeCodexBlockedRollout(t *testing.T, home, sessionID string) {
	t.Helper()
	path := filepath.Join(home, "sessions", "2026", "09", "01", "rollout-2026-09-01T12-00-00-"+sessionID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"timestamp":"2026-09-01T12:00:00Z","type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{"rate_limit_reached_type":"primary","primary":{"resets_at":1788307200}}}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runCostsIngestionCLI(t *testing.T, home string, instance *session.Instance, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmdArgs := append([]string{"-test.run=^TestCostsIngestionCLIHelperProcess$", "--"}, args...)
	cmd := exec.CommandContext(ctx, os.Args[0], cmdArgs...)
	env := []string{
		"AGENT_DECK_COSTS_INGESTION_HELPER=1",
		"AGENT_DECK_TASK6_HELPER_PROCESS=1",
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"XDG_STATE_HOME=" + filepath.Join(home, ".local", "state"),
		"CLAUDE_CONFIG_DIR=",
		"CODEX_HOME=",
	}
	if instance != nil {
		encoded, err := json.Marshal(instance)
		if err != nil {
			t.Fatal(err)
		}
		env = append(env, "AGENT_DECK_COSTS_TEST_INSTANCE="+string(encoded))
	}
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("costs CLI timed out:\n%s", out)
	}
	return string(out), err
}

func TestCostsIngestionCLIHelperProcess(t *testing.T) {
	if os.Getenv("AGENT_DECK_COSTS_INGESTION_HELPER") != "1" {
		return
	}
	if encoded := os.Getenv("AGENT_DECK_COSTS_TEST_INSTANCE"); encoded != "" {
		var instance session.Instance
		if err := json.Unmarshal([]byte(encoded), &instance); err != nil {
			t.Fatal(err)
		}
		session.ClearUserConfigCache()
		storage, err := session.NewStorageWithProfile("work")
		if err != nil {
			t.Fatal(err)
		}
		if err := storage.Save([]*session.Instance{&instance}); err != nil {
			storage.Close()
			t.Fatal(err)
		}
		if err := storage.Close(); err != nil {
			t.Fatal(err)
		}
		session.ClearUserConfigCache()
	}
	separator := 0
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	os.Args = append([]string{"agent-deck"}, os.Args[separator+1:]...)
	main()
}

func openCostsCLIState(t *testing.T, home, profile string) *statedb.StateDB {
	t.Helper()
	path := filepath.Join(home, ".local", "share", "agent-deck", "profiles", profile, "state.db")
	db, err := statedb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestHookWriterDeduplicatesBeforeAndAfterAuthoritativeSync(t *testing.T) {
	for _, order := range []string{"hook-before-sync", "hook-after-sync"} {
		t.Run(order, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
			transcriptDir := filepath.Join(home, ".claude", "projects", "project")
			if err := os.MkdirAll(transcriptDir, 0o700); err != nil {
				t.Fatal(err)
			}
			transcriptPath := filepath.Join(transcriptDir, "native-overlap.jsonl")
			line := []byte(`{"type":"assistant","uuid":"uuid-overlap","requestId":"req-overlap","timestamp":"2026-09-01T16:00:02Z","message":{"id":"msg-overlap","model":"claude-sonnet-5","usage":{"input_tokens":2,"cache_read_input_tokens":3,"cache_creation_input_tokens":4,"output_tokens":5}}}` + "\n")
			if err := os.WriteFile(transcriptPath, line, 0o600); err != nil {
				t.Fatal(err)
			}
			stopPayload, err := json.Marshal(map[string]string{"hook_event_name": "Stop", "transcript_path": transcriptPath})
			if err != nil {
				t.Fatal(err)
			}
			writeCostEvent("instance-overlap", stopPayload)
			entries, err := os.ReadDir(getCostEventsDir())
			if err != nil || len(entries) != 1 {
				t.Fatalf("read hook event: entries=%d err=%v", len(entries), err)
			}
			data, err := os.ReadFile(filepath.Join(getCostEventsDir(), entries[0].Name()))
			if err != nil {
				t.Fatal(err)
			}
			var raw costs.RawCostEvent
			if err := json.Unmarshal(data, &raw); err != nil {
				t.Fatal(err)
			}

			db, err := statedb.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			if err := db.Migrate(); err != nil {
				t.Fatal(err)
			}
			store := costs.NewStore(db.DB())
			pricer := costs.NewPricer(costs.PricerConfig{})
			source := costs.TranscriptSource{
				Provider: costs.ProviderClaude, Kind: costs.SourceKindClaudeDirect,
				Identity: costs.ClaudeTranscriptIdentity(transcriptPath), Path: transcriptPath,
				SessionID: "instance-overlap",
			}
			writeHook := func() {
				t.Helper()
				if err := store.WriteRawCostEvent(raw, pricer); err != nil {
					t.Fatal(err)
				}
			}
			syncTranscript := func() {
				t.Helper()
				result := costs.Sync(context.Background(), store, pricer, []costs.TranscriptSource{source})
				if len(result.Errors) != 0 {
					t.Fatalf("sync=%+v", result)
				}
			}
			if order == "hook-before-sync" {
				writeHook()
				var provider, sourceIdentity string
				if err := store.DB().QueryRow(`SELECT provider, source_identity FROM cost_events`).Scan(&provider, &sourceIdentity); err != nil {
					t.Fatal(err)
				}
				if provider != costs.ProviderClaude || sourceIdentity != "claude:msg:msg-overlap" {
					t.Fatalf("hook metadata provider=%q source_identity=%q", provider, sourceIdentity)
				}
				syncTranscript()
			} else {
				syncTranscript()
				writeHook()
			}
			var visibleRows int
			if err := store.DB().QueryRow(`SELECT COUNT(*) FROM cost_events WHERE reconciliation_status <> ?`, costs.ReconciliationLegacySuperseded).Scan(&visibleRows); err != nil {
				t.Fatal(err)
			}
			if visibleRows != 1 {
				t.Fatalf("visible overlapping rows=%d, want 1", visibleRows)
			}
		})
	}
}
