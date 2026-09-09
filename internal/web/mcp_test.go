package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/session"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type snapshotLoader struct {
	snap *MenuSnapshot
	err  error
}

func (s snapshotLoader) LoadMenuSnapshot() (*MenuSnapshot, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.snap == nil {
		return &MenuSnapshot{Profile: "default"}, nil
	}
	return s.snap, nil
}

func sampleFleetSnapshot() *MenuSnapshot {
	return &MenuSnapshot{
		Profile:       "default",
		TotalGroups:   1,
		TotalSessions: 2,
		Items: []MenuItem{
			{
				Type: "group",
				Group: &MenuGroup{
					Name: "work", Path: "work", SessionCount: 2,
				},
			},
			{
				Type: "session",
				Session: &MenuSession{
					ID: "sess-1", Title: "Alpha", Tool: "claude",
					Status: session.StatusRunning, Substate: "idle-at-empty-prompt",
					GroupPath: "work", ProjectPath: "/tmp/a", IsConductor: true,
				},
			},
			{
				Type: "session",
				Session: &MenuSession{
					ID: "sess-2", Title: "Beta", Tool: "codex",
					Status: session.StatusIdle, GroupPath: "work", ProjectPath: "/tmp/b",
				},
			},
		},
	}
}

func mcpTestDeps(t *testing.T, mutate *fakeMutator) MCPDependencies {
	t.Helper()
	if mutate == nil {
		mutate = &fakeMutator{}
	}
	return MCPDependencies{
		Loader:           snapshotLoader{snap: sampleFleetSnapshot()},
		Mutator:          mutate,
		Authorize:        func(*http.Request) bool { return true },
		MutationsAllowed: func() bool { return true },
		AllowMutation:    func() bool { return true },
	}
}

func connectMCP(t *testing.T, deps MCPDependencies) (*mcpsdk.ClientSession, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(NewMCPHandler(deps))
	t.Cleanup(srv.Close)
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "mcp-test", Version: "0"}, nil)
	session, err := client.Connect(context.Background(), &mcpsdk.StreamableClientTransport{Endpoint: srv.URL}, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, srv
}

func callToolJSON(t *testing.T, cs *mcpsdk.ClientSession, name string, args any) (json.RawMessage, bool, error) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return nil, false, err
	}
	if res.IsError {
		msg := ""
		if len(res.Content) > 0 {
			if tc, ok := res.Content[0].(*mcpsdk.TextContent); ok {
				msg = tc.Text
			}
		}
		return nil, true, errors.New(msg)
	}
	if res.StructuredContent != nil {
		raw, err := json.Marshal(res.StructuredContent)
		if err != nil {
			t.Fatalf("marshal structured: %v", err)
		}
		return raw, false, nil
	}
	if len(res.Content) > 0 {
		if tc, ok := res.Content[0].(*mcpsdk.TextContent); ok {
			return json.RawMessage(tc.Text), false, nil
		}
	}
	return nil, false, fmt.Errorf("empty tool result")
}

func TestMCP_ToolsListExactlyEightWithReadOnlyHints(t *testing.T) {
	cs, _ := connectMCP(t, mcpTestDeps(t, nil))
	listed, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(listed.Tools) != 8 {
		t.Fatalf("tools len = %d, want 8", len(listed.Tools))
	}
	want := map[string]bool{
		"fleet_status":    true,
		"session_details": true,
		"send_to_session": false,
		"start_session":   false,
		"stop_session":    false,
		"restart_session": false,
		"create_session":  false,
		"delete_session":  false,
	}
	got := make(map[string]bool, len(listed.Tools))
	for _, tool := range listed.Tools {
		roWant, ok := want[tool.Name]
		if !ok {
			t.Fatalf("unexpected tool %q", tool.Name)
		}
		if tool.Annotations == nil {
			t.Fatalf("tool %q missing annotations", tool.Name)
		}
		if tool.Annotations.ReadOnlyHint != roWant {
			t.Fatalf("tool %q ReadOnlyHint = %v, want %v", tool.Name, tool.Annotations.ReadOnlyHint, roWant)
		}
		got[tool.Name] = true
	}
	for name := range want {
		if !got[name] {
			t.Fatalf("missing tool %q", name)
		}
	}
}

func TestMCP_CreateAndDeleteSession_DelegateAndReturnResults(t *testing.T) {
	var createArgs []string
	var deletedID string
	mut := &fakeMutator{
		createSessionFn: func(title, tool, projectPath, groupPath, modelID, reasoningEffort string) (string, error) {
			createArgs = []string{title, tool, projectPath, groupPath, modelID, reasoningEffort}
			return "new-session", nil
		},
		deleteSessionFn: func(id string) error { deletedID = id; return nil },
	}
	cs, _ := connectMCP(t, mcpTestDeps(t, mut))
	raw, isErr, err := callToolJSON(t, cs, "create_session", map[string]any{
		"title": "Mobile work", "tool": "codex", "projectPath": "/tmp/project",
		"groupPath": "mobile", "modelId": "gpt-5.6", "reasoningEffort": "high",
	})
	if err != nil || isErr {
		t.Fatalf("create_session: isErr=%v err=%v", isErr, err)
	}
	if got, want := strings.Join(createArgs, "|"), "Mobile work|codex|/tmp/project|mobile|gpt-5.6|high"; got != want {
		t.Fatalf("create args = %q, want %q", got, want)
	}
	var result MCPMutationResult
	if err := json.Unmarshal(raw, &result); err != nil || !result.OK || result.SessionID != "new-session" {
		t.Fatalf("create result=%s err=%v", raw, err)
	}

	raw, isErr, err = callToolJSON(t, cs, "delete_session", map[string]any{"sessionId": "new-session"})
	if err != nil || isErr {
		t.Fatalf("delete_session: isErr=%v err=%v", isErr, err)
	}
	if deletedID != "new-session" {
		t.Fatalf("deleted id = %q", deletedID)
	}
	if err := json.Unmarshal(raw, &result); err != nil || !result.OK || result.SessionID != "new-session" {
		t.Fatalf("delete result=%s err=%v", raw, err)
	}
}

func TestMCP_CreateAndDeleteSession_ValidationAndGuards(t *testing.T) {
	var delegated atomic.Int64
	mut := &fakeMutator{
		createSessionFn: func(string, string, string, string, string, string) (string, error) {
			delegated.Add(1)
			return "x", nil
		},
		deleteSessionFn: func(string) error { delegated.Add(1); return nil },
	}
	cs, _ := connectMCP(t, mcpTestDeps(t, mut))
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"create_session", map[string]any{"title": "", "projectPath": "/tmp/p"}},
		{"create_session", map[string]any{"title": "x", "projectPath": " "}},
		{"delete_session", map[string]any{"sessionId": " "}},
	} {
		_, isErr, err := callToolJSON(t, cs, tc.name, tc.args)
		if !isErr || ClassifyMCPError(err) != MCPErrorMalformed {
			t.Fatalf("%s validation: isErr=%v kind=%s err=%v", tc.name, isErr, ClassifyMCPError(err), err)
		}
	}
	deps := mcpTestDeps(t, mut)
	deps.MutationsAllowed = func() bool { return false }
	cs2, _ := connectMCP(t, deps)
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"create_session", map[string]any{"title": "x", "projectPath": "/tmp/p"}},
		{"delete_session", map[string]any{"sessionId": "x"}},
	} {
		_, isErr, err := callToolJSON(t, cs2, tc.name, tc.args)
		if !isErr || ClassifyMCPError(err) != MCPErrorMutationDisabled {
			t.Fatalf("%s guard: isErr=%v kind=%s err=%v", tc.name, isErr, ClassifyMCPError(err), err)
		}
	}
	if delegated.Load() != 0 {
		t.Fatalf("invalid/guarded calls delegated=%d", delegated.Load())
	}
}

func TestMCP_CreateSessionFailureLogsSafeDiagnosticFields(t *testing.T) {
	logDir := t.TempDir()
	logging.Init(logging.Config{LogDir: logDir})
	t.Cleanup(func() { logging.Init(logging.Config{}) })

	mut := &fakeMutator{createSessionFn: func(string, string, string, string, string, string) (string, error) {
		return "", errors.New("start session: chdir /Users/doozyx/missing: no such file or directory")
	}}
	cs, _ := connectMCP(t, mcpTestDeps(t, mut))
	_, isErr, err := callToolJSON(t, cs, "create_session", map[string]any{
		"title": "secret title", "tool": "cursor", "projectPath": "/Users/doozyx/missing",
	})
	if !isErr || err == nil {
		t.Fatalf("create_session failure: isErr=%v err=%v", isErr, err)
	}
	raw, readErr := os.ReadFile(logDir + "/debug.log")
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatalf("read log: %v", readErr)
	}
	got := string(raw)
	for _, want := range []string{`"msg":"mcp_tool_failed"`, `"tool":"create_session"`, `"errorKind":"backend"`, `"sessionTool":"cursor"`, `"projectPath":"/Users/doozyx/missing"`, `no such file or directory`} {
		if !strings.Contains(got, want) {
			t.Fatalf("log missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "secret title") {
		t.Fatalf("log exposed session title: %s", got)
	}
}

func TestMCP_FleetStatus_ReadOnlySnapshot(t *testing.T) {
	mut := &fakeMutator{
		startSessionFn: func(string) error {
			t.Fatal("fleet_status must not mutate")
			return nil
		},
	}
	cs, _ := connectMCP(t, mcpTestDeps(t, mut))
	raw, isErr, err := callToolJSON(t, cs, "fleet_status", map[string]any{})
	if err != nil || isErr {
		t.Fatalf("fleet_status error: isErr=%v err=%v", isErr, err)
	}
	var got MCPFleetStatusResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, raw)
	}
	if got.Profile != "default" || got.TotalGroups != 1 || got.TotalSessions != 2 {
		t.Fatalf("fleet totals = %+v", got)
	}
	if len(got.Sessions) != 2 || len(got.Groups) != 1 {
		t.Fatalf("sessions=%d groups=%d", len(got.Sessions), len(got.Groups))
	}
	if got.Sessions[0].ID != "sess-1" || !got.Sessions[0].IsConductor {
		t.Fatalf("session[0]=%+v", got.Sessions[0])
	}
	if got.Groups[0].Path != "work" || got.Groups[0].SessionCount != 2 {
		t.Fatalf("group=%+v", got.Groups[0])
	}
}

func TestMCP_SessionDetails_FoundAndMissing(t *testing.T) {
	cs, _ := connectMCP(t, mcpTestDeps(t, nil))

	raw, isErr, err := callToolJSON(t, cs, "session_details", map[string]any{"sessionId": "sess-1"})
	if err != nil || isErr {
		t.Fatalf("session_details: isErr=%v err=%v", isErr, err)
	}
	var details MCPSessionDetailsResult
	if err := json.Unmarshal(raw, &details); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if details.ID != "sess-1" || details.Title != "Alpha" || details.Substate == "" {
		t.Fatalf("details=%+v", details)
	}

	_, isErr, err = callToolJSON(t, cs, "session_details", map[string]any{"sessionId": "missing"})
	if !isErr {
		t.Fatalf("expected tool error for missing session, err=%v", err)
	}
	if ClassifyMCPError(err) != MCPErrorNotFound {
		t.Fatalf("kind=%s err=%v", ClassifyMCPError(err), err)
	}

	_, isErr, err = callToolJSON(t, cs, "session_details", map[string]any{})
	if !isErr {
		t.Fatalf("expected malformed/missing sessionId error, err=%v", err)
	}
	kind := ClassifyMCPError(err)
	if kind != MCPErrorMalformed && !strings.Contains(strings.ToLower(err.Error()), "required") && !strings.Contains(strings.ToLower(err.Error()), "sessionid") {
		t.Fatalf("expected malformed/validation error, kind=%s err=%v", kind, err)
	}
}

func TestMCP_SendToSession_DelegatesOnly(t *testing.T) {
	var gotID, gotMsg string
	mut := &fakeMutator{
		sendToSessionFn: func(id, message string) error {
			gotID, gotMsg = id, message
			return nil
		},
	}
	cs, _ := connectMCP(t, mcpTestDeps(t, mut))
	raw, isErr, err := callToolJSON(t, cs, "send_to_session", map[string]any{
		"sessionId": "sess-1",
		"message":   "hello fleet",
	})
	if err != nil || isErr {
		t.Fatalf("send_to_session: isErr=%v err=%v", isErr, err)
	}
	if gotID != "sess-1" || gotMsg != "hello fleet" {
		t.Fatalf("mutator got id=%q msg=%q", gotID, gotMsg)
	}
	var mutRes MCPMutationResult
	if err := json.Unmarshal(raw, &mutRes); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !mutRes.OK || mutRes.SessionID != "sess-1" {
		t.Fatalf("result=%+v", mutRes)
	}

	// Extra unsafe fields must not be accepted as alternate channels.
	_, isErr, err = callToolJSON(t, cs, "send_to_session", map[string]any{
		"sessionId": "sess-1",
		"message":   "ok",
		"command":   "rm -rf /",
		"url":       "https://evil.example",
	})
	if !isErr {
		t.Fatalf("expected rejection of extra command/url fields, err=%v", err)
	}
}

func TestMCP_SendToSession_PreservesMeaningfulWhitespace(t *testing.T) {
	const wantMsg = "  indented code\n\tline\n"
	var gotMsg string
	mut := &fakeMutator{
		sendToSessionFn: func(_, message string) error {
			gotMsg = message
			return nil
		},
	}
	cs, _ := connectMCP(t, mcpTestDeps(t, mut))
	_, isErr, err := callToolJSON(t, cs, "send_to_session", map[string]any{
		"sessionId": "sess-1",
		"message":   wantMsg,
	})
	if err != nil || isErr {
		t.Fatalf("send_to_session: isErr=%v err=%v", isErr, err)
	}
	if gotMsg != wantMsg {
		t.Fatalf("mutator message = %q, want byte-for-byte %q", gotMsg, wantMsg)
	}

	_, isErr, err = callToolJSON(t, cs, "send_to_session", map[string]any{
		"sessionId": "sess-1",
		"message":   "   \n\t  ",
	})
	if !isErr || ClassifyMCPError(err) != MCPErrorMalformed {
		t.Fatalf("all-whitespace message: isErr=%v kind=%s err=%v", isErr, ClassifyMCPError(err), err)
	}
}

func TestMCP_NilMutationSeamsFailClosed(t *testing.T) {
	var delegated atomic.Int64
	mut := &fakeMutator{
		startSessionFn: func(string) error {
			delegated.Add(1)
			return nil
		},
		sendToSessionFn: func(string, string) error {
			delegated.Add(1)
			return nil
		},
	}

	deps := mcpTestDeps(t, mut)
	deps.MutationsAllowed = nil
	cs, _ := connectMCP(t, deps)
	for _, tool := range []string{"send_to_session", "start_session", "stop_session", "restart_session"} {
		args := map[string]any{"sessionId": "sess-1"}
		if tool == "send_to_session" {
			args["message"] = "x"
		}
		_, isErr, err := callToolJSON(t, cs, tool, args)
		if !isErr || ClassifyMCPError(err) != MCPErrorMutationDisabled {
			t.Fatalf("%s nil MutationsAllowed: isErr=%v kind=%s err=%v", tool, isErr, ClassifyMCPError(err), err)
		}
	}

	deps = mcpTestDeps(t, mut)
	deps.AllowMutation = nil
	cs2, _ := connectMCP(t, deps)
	_, isErr, err := callToolJSON(t, cs2, "start_session", map[string]any{"sessionId": "sess-1"})
	if !isErr || ClassifyMCPError(err) != MCPErrorRateLimited {
		t.Fatalf("nil AllowMutation: isErr=%v kind=%s err=%v", isErr, ClassifyMCPError(err), err)
	}
	if delegated.Load() != 0 {
		t.Fatalf("nil safety seams must not delegate; delegated=%d", delegated.Load())
	}
}

func TestMCP_FleetStatus_NegativeTotalsNoPanic(t *testing.T) {
	deps := mcpTestDeps(t, nil)
	deps.Loader = snapshotLoader{snap: &MenuSnapshot{
		Profile:       "default",
		TotalGroups:   -3,
		TotalSessions: -1,
		Items: []MenuItem{
			{Type: "session", Session: &MenuSession{ID: "sess-neg", Title: "Neg", Tool: "claude", Status: session.StatusIdle}},
		},
	}}
	cs, _ := connectMCP(t, deps)
	raw, isErr, err := callToolJSON(t, cs, "fleet_status", map[string]any{})
	if err != nil || isErr {
		t.Fatalf("fleet_status with negative totals: isErr=%v err=%v", isErr, err)
	}
	var got MCPFleetStatusResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, raw)
	}
	if got.TotalSessions != -1 || got.TotalGroups != -3 {
		t.Fatalf("totals = %+v", got)
	}
	if len(got.Sessions) != 1 || got.Sessions[0].ID != "sess-neg" {
		t.Fatalf("sessions=%+v", got.Sessions)
	}
}

func TestMCP_LifecycleTools_DelegateAndGuards(t *testing.T) {
	var started, stopped, restarted []string
	mut := &fakeMutator{
		startSessionFn:   func(id string) error { started = append(started, id); return nil },
		stopSessionFn:    func(id string) error { stopped = append(stopped, id); return nil },
		restartSessionFn: func(id string) error { restarted = append(restarted, id); return nil },
	}
	cs, _ := connectMCP(t, mcpTestDeps(t, mut))
	for _, tool := range []string{"start_session", "stop_session", "restart_session"} {
		raw, isErr, err := callToolJSON(t, cs, tool, map[string]any{"sessionId": "sess-2"})
		if err != nil || isErr {
			t.Fatalf("%s: isErr=%v err=%v", tool, isErr, err)
		}
		var mutRes MCPMutationResult
		if err := json.Unmarshal(raw, &mutRes); err != nil || !mutRes.OK || mutRes.SessionID != "sess-2" {
			t.Fatalf("%s result=%s err=%v", tool, raw, err)
		}
	}
	if len(started) != 1 || started[0] != "sess-2" {
		t.Fatalf("started=%v", started)
	}
	if len(stopped) != 1 || stopped[0] != "sess-2" {
		t.Fatalf("stopped=%v", stopped)
	}
	if len(restarted) != 1 || restarted[0] != "sess-2" {
		t.Fatalf("restarted=%v", restarted)
	}

	deps := mcpTestDeps(t, mut)
	deps.MutationsAllowed = func() bool { return false }
	cs2, _ := connectMCP(t, deps)
	for _, tool := range []string{"send_to_session", "start_session", "stop_session", "restart_session"} {
		args := map[string]any{"sessionId": "sess-1"}
		if tool == "send_to_session" {
			args["message"] = "x"
		}
		_, isErr, err := callToolJSON(t, cs2, tool, args)
		if !isErr || ClassifyMCPError(err) != MCPErrorMutationDisabled {
			t.Fatalf("%s mutations disabled: isErr=%v kind=%s err=%v", tool, isErr, ClassifyMCPError(err), err)
		}
	}

	deps = mcpTestDeps(t, mut)
	deps.AllowMutation = func() bool { return false }
	cs3, _ := connectMCP(t, deps)
	_, isErr, err := callToolJSON(t, cs3, "start_session", map[string]any{"sessionId": "sess-1"})
	if !isErr || ClassifyMCPError(err) != MCPErrorRateLimited {
		t.Fatalf("rate limit: isErr=%v kind=%s err=%v", isErr, ClassifyMCPError(err), err)
	}
}

func TestMCP_BackendAndNilDependencyErrors(t *testing.T) {
	mut := &fakeMutator{
		startSessionFn: func(string) error { return errors.New("tmux boom") },
	}
	deps := mcpTestDeps(t, mut)
	cs, _ := connectMCP(t, deps)
	_, isErr, err := callToolJSON(t, cs, "start_session", map[string]any{"sessionId": "sess-1"})
	if !isErr || ClassifyMCPError(err) != MCPErrorBackend {
		t.Fatalf("backend: isErr=%v kind=%s err=%v", isErr, ClassifyMCPError(err), err)
	}

	deps.Loader = nil
	csNil, _ := connectMCP(t, deps)
	_, isErr, err = callToolJSON(t, csNil, "fleet_status", map[string]any{})
	if !isErr || ClassifyMCPError(err) != MCPErrorBackend {
		t.Fatalf("nil loader: isErr=%v kind=%s err=%v", isErr, ClassifyMCPError(err), err)
	}

	deps = mcpTestDeps(t, nil)
	deps.Mutator = nil
	csNilMut, _ := connectMCP(t, deps)
	_, isErr, err = callToolJSON(t, csNilMut, "stop_session", map[string]any{"sessionId": "sess-1"})
	if !isErr || ClassifyMCPError(err) != MCPErrorBackend {
		t.Fatalf("nil mutator: isErr=%v kind=%s err=%v", isErr, ClassifyMCPError(err), err)
	}

	deps = mcpTestDeps(t, mut)
	deps.Loader = snapshotLoader{err: errors.New("disk gone")}
	csLoadErr, _ := connectMCP(t, deps)
	_, isErr, err = callToolJSON(t, csLoadErr, "session_details", map[string]any{"sessionId": "sess-1"})
	if !isErr || ClassifyMCPError(err) != MCPErrorBackend {
		t.Fatalf("loader err: isErr=%v kind=%s err=%v", isErr, ClassifyMCPError(err), err)
	}
}

func TestMCP_UnknownToolAndMalformedProtocol(t *testing.T) {
	cs, srv := connectMCP(t, mcpTestDeps(t, nil))
	_, isErr, err := callToolJSON(t, cs, "not_a_tool", map[string]any{})
	if err == nil && !isErr {
		t.Fatal("expected unknown tool failure")
	}

	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("malformed POST: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusOK && !strings.Contains(string(body), "error") {
		t.Fatalf("expected protocol error for malformed body; status=%d body=%s", resp.StatusCode, body)
	}
}

func TestMCP_AuthenticatedInitializeAndFleetStatus_HTTPEvidence(t *testing.T) {
	deps := mcpTestDeps(t, nil)
	srv := httptest.NewServer(NewMCPHandler(deps))
	t.Cleanup(srv.Close)

	initReq, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"evidence","version":"0"}}}`,
	))
	if err != nil {
		t.Fatalf("new initialize request: %v", err)
	}
	initReq.Header.Set("Content-Type", "application/json")
	initReq.Header.Set("Accept", "application/json, text/event-stream")
	initResp, err := http.DefaultClient.Do(initReq)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	defer initResp.Body.Close()
	initBody, _ := io.ReadAll(initResp.Body)
	t.Logf("authenticated initialize status=%d body=%s", initResp.StatusCode, initBody)
	if initResp.StatusCode != http.StatusOK || !strings.Contains(string(initBody), `"protocolVersion"`) {
		t.Fatalf("initialize failed: status=%d body=%s", initResp.StatusCode, initBody)
	}
	sessionID := initResp.Header.Get("Mcp-Session-Id")

	notify, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
	))
	if err != nil {
		t.Fatalf("new notify request: %v", err)
	}
	notify.Header.Set("Content-Type", "application/json")
	notify.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		notify.Header.Set("Mcp-Session-Id", sessionID)
	}
	notifyResp, err := http.DefaultClient.Do(notify)
	if err != nil {
		t.Fatalf("initialized notify: %v", err)
	}
	_, _ = io.Copy(io.Discard, notifyResp.Body)
	notifyResp.Body.Close()

	callReq, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"fleet_status","arguments":{}}}`,
	))
	if err != nil {
		t.Fatalf("new tools/call request: %v", err)
	}
	callReq.Header.Set("Content-Type", "application/json")
	callReq.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		callReq.Header.Set("Mcp-Session-Id", sessionID)
	}
	callResp, err := http.DefaultClient.Do(callReq)
	if err != nil {
		t.Fatalf("fleet_status call: %v", err)
	}
	defer callResp.Body.Close()
	callBody, _ := io.ReadAll(callResp.Body)
	t.Logf("authenticated fleet_status status=%d body=%s", callResp.StatusCode, callBody)
	if callResp.StatusCode != http.StatusOK {
		t.Fatalf("fleet_status HTTP status=%d", callResp.StatusCode)
	}
	if !strings.Contains(string(callBody), `"fleet_status"`) && !strings.Contains(string(callBody), `"totalSessions"`) && !strings.Contains(string(callBody), `"sess-1"`) {
		t.Fatalf("fleet_status response missing expected data: %s", callBody)
	}
}

func TestMCP_ConcurrentToolCalls(t *testing.T) {
	var calls atomic.Int64
	mut := &fakeMutator{
		sendToSessionFn: func(id, message string) error {
			calls.Add(1)
			return nil
		},
	}
	cs, _ := connectMCP(t, mcpTestDeps(t, mut))
	var wg sync.WaitGroup
	errCh := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, isErr, err := callToolJSON(t, cs, "send_to_session", map[string]any{
				"sessionId": "sess-1",
				"message":   fmt.Sprintf("m%d", i),
			})
			if err != nil || isErr {
				errCh <- fmt.Errorf("call %d: isErr=%v err=%v", i, isErr, err)
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	if calls.Load() != 20 {
		t.Fatalf("calls=%d want 20", calls.Load())
	}
}
