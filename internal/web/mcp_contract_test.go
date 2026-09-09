package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPRouteConstant(t *testing.T) {
	if MCPRoute != "/mcp" {
		t.Fatalf("MCPRoute = %q, want /mcp", MCPRoute)
	}
}

func TestMCPToolCatalog_ExactSixWithReadOnlyAnnotations(t *testing.T) {
	catalog := MCPToolCatalog()
	if len(catalog) != 6 {
		t.Fatalf("catalog len = %d, want 6", len(catalog))
	}
	wantNames := []string{
		"fleet_status",
		"session_details",
		"send_to_session",
		"start_session",
		"stop_session",
		"restart_session",
	}
	for i, name := range wantNames {
		if catalog[i].Name != name {
			t.Fatalf("tool[%d].Name = %q, want %q", i, catalog[i].Name, name)
		}
		if catalog[i].Description == "" {
			t.Fatalf("tool %q missing description", name)
		}
		if catalog[i].Annotations == nil {
			t.Fatalf("tool %q missing annotations", name)
		}
		readOnly := catalog[i].Annotations.ReadOnlyHint
		wantRO := name == "fleet_status" || name == "session_details"
		if readOnly != wantRO {
			t.Fatalf("tool %q ReadOnlyHint = %v, want %v", name, readOnly, wantRO)
		}
	}
}

func TestMCPToolInputSchemas_RequiredFieldsNoCommandOrURL(t *testing.T) {
	cases := map[string][]string{
		"fleet_status":    nil,
		"session_details": {"sessionId"},
		"send_to_session": {"sessionId", "message"},
		"start_session":   {"sessionId"},
		"stop_session":    {"sessionId"},
		"restart_session": {"sessionId"},
	}
	for _, tool := range MCPToolCatalog() {
		required, ok := cases[tool.Name]
		if !ok {
			t.Fatalf("unexpected tool %q", tool.Name)
		}
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("%s: marshal schema: %v", tool.Name, err)
		}
		body := string(raw)
		for _, forbidden := range []string{"command", "url", "shell", "http"} {
			if strings.Contains(strings.ToLower(body), `"`+forbidden+`"`) {
				t.Fatalf("%s schema must not accept %q input; got %s", tool.Name, forbidden, body)
			}
		}
		var schema map[string]any
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("%s: decode schema: %v", tool.Name, err)
		}
		reqRaw, _ := schema["required"].([]any)
		got := make([]string, 0, len(reqRaw))
		for _, v := range reqRaw {
			s, _ := v.(string)
			got = append(got, s)
		}
		if len(got) != len(required) {
			t.Fatalf("%s required = %v, want %v", tool.Name, got, required)
		}
		for i := range required {
			if got[i] != required[i] {
				t.Fatalf("%s required[%d] = %q, want %q", tool.Name, i, got[i], required[i])
			}
		}
	}
}

func TestMCPStableJSONFieldNames(t *testing.T) {
	fleet := MCPFleetStatusResult{
		Profile:       "default",
		TotalGroups:   1,
		TotalSessions: 2,
		Sessions: []MCPSessionSummary{{
			ID: "s1", Title: "t", Tool: "claude", Status: "running",
			GroupPath: "g", ProjectPath: "/p", IsConductor: true,
		}},
		Groups: []MCPGroupSummary{{Name: "g", Path: "g", SessionCount: 2}},
	}
	raw, err := json.Marshal(fleet)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		`"profile"`, `"totalGroups"`, `"totalSessions"`, `"sessions"`, `"groups"`,
		`"id"`, `"title"`, `"tool"`, `"status"`, `"groupPath"`, `"projectPath"`, `"isConductor"`,
		`"name"`, `"path"`, `"sessionCount"`,
	} {
		if !bytes.Contains(raw, []byte(key)) {
			t.Fatalf("fleet status JSON missing %s: %s", key, raw)
		}
	}

	details := MCPSessionDetailsResult{
		ID: "s1", Title: "t", Tool: "claude", Status: "idle",
		GroupPath: "g", ProjectPath: "/p",
	}
	raw, err = json.Marshal(details)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"id"`, `"title"`, `"tool"`, `"status"`, `"groupPath"`, `"projectPath"`} {
		if !bytes.Contains(raw, []byte(key)) {
			t.Fatalf("session details JSON missing %s: %s", key, raw)
		}
	}

	mut := MCPMutationResult{SessionID: "s1", OK: true}
	raw, err = json.Marshal(mut)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"sessionId"`, `"ok"`} {
		if !bytes.Contains(raw, []byte(key)) {
			t.Fatalf("mutation JSON missing %s: %s", key, raw)
		}
	}
}

func TestClassifyMCPErrorKinds(t *testing.T) {
	cases := []struct {
		err  error
		kind MCPErrorKind
	}{
		{ErrMCPUnauthorized, MCPErrorUnauthorized},
		{ErrMCPMalformed, MCPErrorMalformed},
		{ErrMCPNotFound, MCPErrorNotFound},
		{ErrMCPMutationDisabled, MCPErrorMutationDisabled},
		{ErrMCPRateLimited, MCPErrorRateLimited},
		{ErrMCPBackend, MCPErrorBackend},
		{errors.New("session not found: abc"), MCPErrorNotFound},
		{errors.New("boom"), MCPErrorBackend},
	}
	for _, tc := range cases {
		if got := ClassifyMCPError(tc.err); got != tc.kind {
			t.Fatalf("ClassifyMCPError(%v) = %v, want %v", tc.err, got, tc.kind)
		}
	}
}

func TestNewMCPHandler_RejectsUnauthorized(t *testing.T) {
	h := NewMCPHandler(MCPDependencies{
		Loader:           stubMenuLoader{},
		Mutator:          &fakeMutator{},
		Authorize:        func(*http.Request) bool { return false },
		MutationsAllowed: func() bool { return true },
		AllowMutation:    func() bool { return true },
	})
	if h == nil {
		t.Fatal("NewMCPHandler returned nil")
	}
	req := httptest.NewRequest(http.MethodPost, MCPRoute, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rr.Code, rr.Body.String())
	}
}

func TestNewMCPHandler_AuthenticatedUsesStreamableHTTP(t *testing.T) {
	h := NewMCPHandler(MCPDependencies{
		Loader:           stubMenuLoader{},
		Mutator:          &fakeMutator{},
		Authorize:        func(*http.Request) bool { return true },
		MutationsAllowed: func() bool { return true },
		AllowMutation:    func() bool { return true },
	})
	req := httptest.NewRequest(http.MethodPost, MCPRoute, strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`,
	))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"protocolVersion"`) {
		t.Fatalf("initialize response missing protocolVersion: %s", rr.Body.String())
	}
}

func TestMCPDependenciesAndSendToSessionSignatures(t *testing.T) {
	var deps MCPDependencies
	_ = deps.Loader
	_ = deps.Mutator
	_ = deps.Authorize
	_ = deps.MutationsAllowed
	_ = deps.AllowMutation
	var _ func(MCPDependencies) http.Handler = NewMCPHandler

	var m SessionMutator = &fakeMutator{}
	if err := m.SendToSession("sess", "hello"); err == nil {
		// fakeMutator returns an error when sendToSessionFn is nil
		t.Fatal("expected fakeMutator.SendToSession to error when unconfigured")
	}
}

func TestPinnedMCPSDKProvidesStreamableHTTP(t *testing.T) {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "probe", Version: "0"}, nil)
	h := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, &mcpsdk.StreamableHTTPOptions{JSONResponse: true})
	if h == nil {
		t.Fatal("NewStreamableHTTPHandler returned nil")
	}
}

type stubMenuLoader struct{}

func (stubMenuLoader) LoadMenuSnapshot() (*MenuSnapshot, error) {
	return &MenuSnapshot{Profile: "default"}, nil
}
