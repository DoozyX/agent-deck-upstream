package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPRouteConstant(t *testing.T) {
	if MCPRoute != "/mcp" {
		t.Fatalf("MCPRoute = %q, want /mcp", MCPRoute)
	}
}

func TestMCPToolCatalog_ExactNineWithReadOnlyAnnotations(t *testing.T) {
	catalog := MCPToolCatalog()
	if len(catalog) != 9 {
		t.Fatalf("catalog len = %d, want 9", len(catalog))
	}
	wantNames := []string{
		"fleet_status",
		"session_details",
		"send_to_session",
		"start_session",
		"stop_session",
		"restart_session",
		"create_session",
		"delete_session",
		"session_output",
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
		wantRO := name == "fleet_status" || name == "session_details" || name == "session_output"
		if readOnly != wantRO {
			t.Fatalf("tool %q ReadOnlyHint = %v, want %v", name, readOnly, wantRO)
		}
		if name == "delete_session" && (catalog[i].Annotations.DestructiveHint == nil || !*catalog[i].Annotations.DestructiveHint) {
			t.Fatalf("delete_session must advertise destructiveHint=true")
		}
		if name == "create_session" && (catalog[i].Annotations.DestructiveHint == nil || *catalog[i].Annotations.DestructiveHint) {
			t.Fatalf("create_session must advertise destructiveHint=false")
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
		"create_session":  {"title", "projectPath"},
		"delete_session":  {"sessionId"},
		"session_output":  {"sessionId"},
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
		if schema["additionalProperties"] != false {
			t.Fatalf("%s must set additionalProperties=false; got %#v", tool.Name, schema["additionalProperties"])
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

func TestFleetStatusSchema_RejectsAdditionalProperties(t *testing.T) {
	var fleet MCPToolSpec
	for _, tool := range MCPToolCatalog() {
		if tool.Name == "fleet_status" {
			fleet = tool
			break
		}
	}
	if fleet.Name == "" {
		t.Fatal("fleet_status missing from catalog")
	}
	if fleet.InputSchema["additionalProperties"] != false {
		t.Fatalf("fleet_status additionalProperties = %#v, want false", fleet.InputSchema["additionalProperties"])
	}
	raw, err := json.Marshal(fleet.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("unmarshal fleet_status schema: %v", err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatalf("resolve fleet_status schema: %v", err)
	}
	if err := resolved.Validate(map[string]any{"command": "rm -rf /"}); err == nil {
		t.Fatal("expected fleet_status schema to reject undeclared property command")
	}
	if err := resolved.Validate(map[string]any{}); err != nil {
		t.Fatalf("empty object must be valid for fleet_status: %v", err)
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
		{errors.New("mcp: malformed input"), MCPErrorMalformed},
		{errors.New("required: missing properties: [\"sessionId\"]"), MCPErrorMalformed},
		{errors.New(`unexpected additional properties ["command"]`), MCPErrorMalformed},
		// Singular MCP wrapper must use the exact prefixed form.
		{errors.New(MCPAdditionalPropertyPrefix + ` "command"`), MCPErrorMalformed},
		{errors.New("boom"), MCPErrorBackend},
		// Backend failures that merely contain "invalid" must stay backend.
		{errors.New("invalid session state from backend"), MCPErrorBackend},
		{errors.New("tmux returned invalid pane id"), MCPErrorBackend},
		// Negative: backend text that contains validator substrings but is not
		// a pinned jsonschema-go / MCP wrapper message must stay backend.
		{errors.New("storage lost missing properties while syncing"), MCPErrorBackend},
		{errors.New("conductor reported an unexpected additional session"), MCPErrorBackend},
		{errors.New(`tmux echoed additional property "pane" in status`), MCPErrorBackend},
		{errors.New("required property sessionId missing from remote host"), MCPErrorBackend},
		{errors.New("additional properties are not allowed by the remote"), MCPErrorBackend},
		// Backend domain prefixes must not be peeled into validator forms.
		{errors.New("storage: required: missing properties: [sessionId]"), MCPErrorBackend},
		// Positive: jsonschema-go "validating <path>: " wrappers still classify.
		{errors.New(`validating root: required: missing properties: ["sessionId"]`), MCPErrorMalformed},
		{errors.New(`validating root: validating /properties/outer: required: missing properties: ["sessionId"]`), MCPErrorMalformed},
		{errors.New(`validating root: unexpected additional properties ["command"]`), MCPErrorMalformed},
	}
	for _, tc := range cases {
		if got := ClassifyMCPError(tc.err); got != tc.kind {
			t.Fatalf("ClassifyMCPError(%v) = %v, want %v", tc.err, got, tc.kind)
		}
	}
}

func TestClassifyMCPError_JSONSchemaResolvedForms(t *testing.T) {
	resolve := func(t *testing.T, name string) *jsonschema.Resolved {
		t.Helper()
		var spec MCPToolSpec
		for _, tool := range MCPToolCatalog() {
			if tool.Name == name {
				spec = tool
				break
			}
		}
		if spec.Name == "" {
			t.Fatalf("%s missing from catalog", name)
		}
		raw, err := json.Marshal(spec.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema jsonschema.Schema
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			t.Fatal(err)
		}
		return resolved
	}

	t.Run("required_missing_properties", func(t *testing.T) {
		err := resolve(t, "session_details").Validate(map[string]any{})
		if err == nil {
			t.Fatal("expected missing sessionId validation error")
		}
		if !strings.Contains(err.Error(), "required: missing properties:") {
			t.Fatalf("unexpected validator wording: %v", err)
		}
		if got := ClassifyMCPError(err); got != MCPErrorMalformed {
			t.Fatalf("ClassifyMCPError(%v) = %v, want malformed", err, got)
		}
	})

	t.Run("unexpected_additional_properties_resolved", func(t *testing.T) {
		err := resolve(t, "fleet_status").Validate(map[string]any{"command": "x"})
		if err == nil {
			t.Fatal("expected additional-properties validation error")
		}
		if !strings.Contains(err.Error(), "unexpected additional properties") {
			t.Fatalf("unexpected validator wording: %v", err)
		}
		if got := ClassifyMCPError(err); got != MCPErrorMalformed {
			t.Fatalf("ClassifyMCPError(resolved %v) = %v, want malformed", err, got)
		}
	})

	t.Run("singular_additional_property_prefixed", func(t *testing.T) {
		singular := errors.New(MCPAdditionalPropertyPrefix + ` "command"`)
		if got := ClassifyMCPError(singular); got != MCPErrorMalformed {
			t.Fatalf("ClassifyMCPError(%v) = %v, want malformed", singular, got)
		}
		// Non-prefixed singular phrasing must not reclassify backend noise.
		loose := errors.New(`additional property "command" is not allowed`)
		if got := ClassifyMCPError(loose); got != MCPErrorBackend {
			t.Fatalf("ClassifyMCPError(%v) = %v, want backend", loose, got)
		}
	})
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

func TestNewMCPHandler_NilAuthorizeFailsClosed(t *testing.T) {
	h := NewMCPHandler(MCPDependencies{
		Loader:  stubMenuLoader{},
		Mutator: &fakeMutator{},
		// Authorize deliberately nil — must fail closed.
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
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("nil Authorize status = %d, want 401; body=%s", rr.Code, rr.Body.String())
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
