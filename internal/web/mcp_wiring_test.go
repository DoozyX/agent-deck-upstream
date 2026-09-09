package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/time/rate"
)

const wiringToken = "wiring-secret"

func wiringServer(t *testing.T, cfg Config) *Server {
	t.Helper()
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = "127.0.0.1:0"
	}
	if cfg.MenuData == nil {
		cfg.MenuData = snapshotLoader{snap: sampleFleetSnapshot()}
	}
	srv := NewServer(cfg)
	srv.SetMutator(&fakeMutator{
		startSessionFn:   func(string) error { return nil },
		stopSessionFn:    func(string) error { return nil },
		restartSessionFn: func(string) error { return nil },
		sendToSessionFn:  func(string, string) error { return nil },
	})
	return srv
}

func postMCP(t *testing.T, h http.Handler, path, authHeader, body string, queryToken string) *httptest.ResponseRecorder {
	t.Helper()
	url := path
	if queryToken != "" {
		url = path + "?token=" + queryToken
	}
	req := httptest.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	// No Origin/Referer: MCP clients are non-browser and must clear CSRF.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func mcpInitializeBody() string {
	return `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"wiring","version":"0"}}}`
}

func TestServer_MCPRoute_CredentialMatrix(t *testing.T) {
	srv := wiringServer(t, Config{Token: wiringToken, WebMutations: true})
	h := srv.Handler()

	t.Run("missing bearer", func(t *testing.T) {
		rec := postMCP(t, h, MCPRoute, "", mcpInitializeBody(), "")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d body=%s, want 401", rec.Code, rec.Body.String())
		}
	})

	t.Run("wrong bearer", func(t *testing.T) {
		rec := postMCP(t, h, MCPRoute, "Bearer wrong-"+wiringToken, mcpInitializeBody(), "")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d body=%s, want 401", rec.Code, rec.Body.String())
		}
	})

	t.Run("query-string token rejected", func(t *testing.T) {
		rec := postMCP(t, h, MCPRoute, "", mcpInitializeBody(), wiringToken)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("query token status=%d body=%s, want 401", rec.Code, rec.Body.String())
		}
	})

	t.Run("correct bearer initializes", func(t *testing.T) {
		rec := postMCP(t, h, MCPRoute, "Bearer "+wiringToken, mcpInitializeBody(), "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s, want 200", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"protocolVersion"`) {
			t.Fatalf("initialize body missing protocolVersion: %s", rec.Body.String())
		}
	})

	t.Run("lowercase bearer initializes", func(t *testing.T) {
		rec := postMCP(t, h, MCPRoute, "bearer "+wiringToken, mcpInitializeBody(), "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s, want 200", rec.Code, rec.Body.String())
		}
	})
}

func TestServer_MCPRoute_UnavailableWithoutToken(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
	}{
		{"loopback tokenless", Config{ListenAddr: "127.0.0.1:0", WebMutations: true}},
		{"insecure-bind tokenless", Config{ListenAddr: "0.0.0.0:0", InsecureBind: true, WebMutations: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := wiringServer(t, tc.cfg)
			rec := postMCP(t, srv.Handler(), MCPRoute, "", mcpInitializeBody(), "")
			if rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), `"protocolVersion"`) {
				t.Fatalf("tokenless /mcp must not initialize MCP; status=%d body=%s", rec.Code, rec.Body.String())
			}
			if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusNotFound && rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("tokenless /mcp status=%d body=%s, want 401/404/503", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestServer_MCPRoute_AuthenticatedInitializeAndFleetStatus(t *testing.T) {
	srv := wiringServer(t, Config{Token: wiringToken, WebMutations: true})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "wiring-test", Version: "0"}, nil)
	transport := &mcpsdk.StreamableClientTransport{
		Endpoint:   ts.URL + MCPRoute,
		HTTPClient: bearerHTTPClient(wiringToken),
	}
	cs, err := client.Connect(context.Background(), transport, nil)
	if err != nil {
		t.Fatalf("Connect/initialize: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	raw, isErr, err := callToolJSON(t, cs, "fleet_status", map[string]any{})
	if err != nil || isErr {
		t.Fatalf("fleet_status: isErr=%v err=%v", isErr, err)
	}
	var got MCPFleetStatusResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, raw)
	}
	if got.TotalSessions != 2 || got.Profile != "default" {
		t.Fatalf("fleet_status=%+v", got)
	}
}

func TestServer_MCPRoute_AllowsReverseProxyHostOnLoopback(t *testing.T) {
	srv := wiringServer(t, Config{Token: wiringToken, WebMutations: true})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	req, err := http.NewRequest(http.MethodPost, ts.URL+MCPRoute, strings.NewReader(mcpInitializeBody()))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "mcp-agent-deck.example.com"
	req.Header.Set("Authorization", "Bearer "+wiringToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200 for an authenticated reverse-proxy Host", resp.StatusCode)
	}
}

func TestServer_MCPRoute_ExpiresIdleSessions(t *testing.T) {
	previousTimeout := mcpSessionTimeout
	mcpSessionTimeout = 50 * time.Millisecond
	t.Cleanup(func() { mcpSessionTimeout = previousTimeout })

	srv := wiringServer(t, Config{Token: wiringToken, WebMutations: true})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	newClientSession := func() *mcpsdk.ClientSession {
		t.Helper()
		client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "timeout-test", Version: "0"}, nil)
		session, err := client.Connect(context.Background(), &mcpsdk.StreamableClientTransport{
			Endpoint:   ts.URL + MCPRoute,
			HTTPClient: bearerHTTPClient(wiringToken),
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = session.Close() })
		return session
	}

	idle := newClientSession()
	active := newClientSession()
	keepAliveDone := make(chan struct{})
	keepAliveErr := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if _, err := active.ListTools(context.Background(), nil); err != nil {
					keepAliveErr <- err
					return
				}
			case <-keepAliveDone:
				keepAliveErr <- nil
				return
			}
		}
	}()

	// Ten timeout periods gives the SDK cleanup timer ample scheduling room;
	// the active session is refreshed independently throughout that window.
	time.Sleep(10 * mcpSessionTimeout)
	if _, err := idle.ListTools(context.Background(), nil); err == nil {
		t.Fatal("idle session should expire")
	}
	close(keepAliveDone)
	if err := <-keepAliveErr; err != nil {
		t.Fatalf("active session keepalive failed: %v", err)
	}
	if _, err := active.ListTools(context.Background(), nil); err != nil {
		t.Fatalf("active session should remain usable after idle session expires: %v", err)
	}
}

func TestServer_MCPRoute_MalformedAndInputValidation(t *testing.T) {
	srv := wiringServer(t, Config{Token: wiringToken, WebMutations: true})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	rec := postMCP(t, srv.Handler(), MCPRoute, "Bearer "+wiringToken, `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{`, "")
	if rec.Code == http.StatusOK && !strings.Contains(rec.Body.String(), "error") {
		t.Fatalf("expected malformed protocol failure; status=%d body=%s", rec.Code, rec.Body.String())
	}

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "wiring-val", Version: "0"}, nil)
	cs, err := client.Connect(context.Background(), &mcpsdk.StreamableClientTransport{
		Endpoint:   ts.URL + MCPRoute,
		HTTPClient: bearerHTTPClient(wiringToken),
	}, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	_, isErr, err := callToolJSON(t, cs, "session_details", map[string]any{})
	if !isErr {
		t.Fatalf("expected validation error for missing sessionId, err=%v", err)
	}
	kind := ClassifyMCPError(err)
	if kind != MCPErrorMalformed && !strings.Contains(strings.ToLower(err.Error()), "required") && !strings.Contains(strings.ToLower(err.Error()), "sessionid") {
		t.Fatalf("kind=%s err=%v", kind, err)
	}

	_, isErr, err = callToolJSON(t, cs, "send_to_session", map[string]any{
		"sessionId": "sess-1",
		"message":   "x",
		"command":   "rm -rf /",
		"url":       "https://evil.example",
	})
	if !isErr {
		t.Fatalf("expected rejection of command/url extras, err=%v", err)
	}
}

func TestServer_MCPRoute_MutationPolicyAndRateLimit(t *testing.T) {
	t.Run("mutations disabled", func(t *testing.T) {
		srv := wiringServer(t, Config{Token: wiringToken, WebMutations: false})
		ts := httptest.NewServer(srv.Handler())
		t.Cleanup(ts.Close)
		cs := connectWiredMCP(t, ts.URL, wiringToken)
		_, isErr, err := callToolJSON(t, cs, "start_session", map[string]any{"sessionId": "sess-1"})
		if !isErr || ClassifyMCPError(err) != MCPErrorMutationDisabled {
			t.Fatalf("want mutation_disabled; isErr=%v kind=%s err=%v", isErr, ClassifyMCPError(err), err)
		}
	})

	t.Run("rate limited", func(t *testing.T) {
		srv := wiringServer(t, Config{Token: wiringToken, WebMutations: true})
		srv.mutationLimiter = rate.NewLimiter(0, 0) // deny all
		ts := httptest.NewServer(srv.Handler())
		t.Cleanup(ts.Close)
		cs := connectWiredMCP(t, ts.URL, wiringToken)
		_, isErr, err := callToolJSON(t, cs, "stop_session", map[string]any{"sessionId": "sess-1"})
		if !isErr || ClassifyMCPError(err) != MCPErrorRateLimited {
			t.Fatalf("want rate_limited; isErr=%v kind=%s err=%v", isErr, ClassifyMCPError(err), err)
		}
	})

	t.Run("nil mutator backend error", func(t *testing.T) {
		srv := NewServer(Config{
			ListenAddr:   "127.0.0.1:0",
			Token:        wiringToken,
			WebMutations: true,
			MenuData:     snapshotLoader{snap: sampleFleetSnapshot()},
		})
		// Intentionally do not SetMutator.
		ts := httptest.NewServer(srv.Handler())
		t.Cleanup(ts.Close)
		cs := connectWiredMCP(t, ts.URL, wiringToken)
		_, isErr, err := callToolJSON(t, cs, "restart_session", map[string]any{"sessionId": "sess-1"})
		if !isErr || ClassifyMCPError(err) != MCPErrorBackend {
			t.Fatalf("want backend for nil mutator; isErr=%v kind=%s err=%v", isErr, ClassifyMCPError(err), err)
		}
	})
}

// Direct Config{ReadOnly:true} must fail closed even when WebMutations remains true
// (production web_cmd clears both, but NewServer accepts the flags independently).
func TestServer_MCPRoute_ReadOnlyBlocksMutations(t *testing.T) {
	srv := wiringServer(t, Config{Token: wiringToken, ReadOnly: true, WebMutations: true})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	cs := connectWiredMCP(t, ts.URL, wiringToken)

	raw, isErr, err := callToolJSON(t, cs, "fleet_status", map[string]any{})
	if err != nil || isErr {
		t.Fatalf("authenticated read fleet_status must still work: isErr=%v err=%v", isErr, err)
	}
	var fleet MCPFleetStatusResult
	if err := json.Unmarshal(raw, &fleet); err != nil {
		t.Fatalf("decode fleet_status: %v", err)
	}
	if fleet.TotalSessions != 2 {
		t.Fatalf("fleet_status=%+v", fleet)
	}

	_, isErr, err = callToolJSON(t, cs, "session_details", map[string]any{"sessionId": "sess-1"})
	if err != nil || isErr {
		t.Fatalf("authenticated read session_details must still work: isErr=%v err=%v", isErr, err)
	}

	for _, tool := range []string{"send_to_session", "start_session", "stop_session", "restart_session"} {
		args := map[string]any{"sessionId": "sess-1"}
		if tool == "send_to_session" {
			args["message"] = "should not mutate"
		}
		_, isErr, err := callToolJSON(t, cs, tool, args)
		if !isErr || ClassifyMCPError(err) != MCPErrorMutationDisabled {
			t.Fatalf("%s under ReadOnly: want mutation_disabled; isErr=%v kind=%s err=%v",
				tool, isErr, ClassifyMCPError(err), err)
		}
	}
}

func TestServer_MCPRoute_SendToSessionDelegatesToLiveMutator(t *testing.T) {
	var gotID, gotMsg string
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		Token:        wiringToken,
		WebMutations: true,
		MenuData:     snapshotLoader{snap: sampleFleetSnapshot()},
	})
	srv.SetMutator(&fakeMutator{
		sendToSessionFn: func(id, message string) error {
			gotID, gotMsg = id, message
			return nil
		},
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	cs := connectWiredMCP(t, ts.URL, wiringToken)

	raw, isErr, err := callToolJSON(t, cs, "send_to_session", map[string]any{
		"sessionId": "sess-wired-42",
		"message":   "hello live mutator",
	})
	if err != nil || isErr {
		t.Fatalf("send_to_session: isErr=%v err=%v", isErr, err)
	}
	if gotID != "sess-wired-42" || gotMsg != "hello live mutator" {
		t.Fatalf("live mutator got id=%q msg=%q, want sess-wired-42 / hello live mutator", gotID, gotMsg)
	}
	var mutRes MCPMutationResult
	if err := json.Unmarshal(raw, &mutRes); err != nil {
		t.Fatalf("decode: %v body=%s", err, raw)
	}
	if !mutRes.OK || mutRes.SessionID != "sess-wired-42" {
		t.Fatalf("result=%+v", mutRes)
	}
}

func TestServer_MCPRoute_SeparateFromAPIMCPs(t *testing.T) {
	srv := wiringServer(t, Config{Token: wiringToken, WebMutations: true})
	mgr := newFakeMCPManager()
	mgr.catalog = []MCPCatalogEntry{{Name: "context7", Description: "docs"}}
	srv.SetMCPManager(mgr)

	// /api/mcps catalog remains the config catalog, not the Streamable MCP endpoint.
	req := httptest.NewRequest(http.MethodGet, "/api/mcps", nil)
	req.Header.Set("Authorization", "Bearer "+wiringToken)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/mcps status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"mcps"`) || !strings.Contains(rec.Body.String(), `"context7"`) {
		t.Fatalf("/api/mcps catalog shape unexpected: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"protocolVersion"`) || strings.Contains(rec.Body.String(), `"fleet_status"`) {
		t.Fatalf("/api/mcps must not speak MCP protocol: %s", rec.Body.String())
	}

	// /mcp is the protocol endpoint and does not serve the catalog JSON shape.
	mcpRec := postMCP(t, srv.Handler(), MCPRoute, "Bearer "+wiringToken, mcpInitializeBody(), "")
	if mcpRec.Code != http.StatusOK {
		t.Fatalf("/mcp initialize status=%d body=%s", mcpRec.Code, mcpRec.Body.String())
	}
	if !strings.Contains(mcpRec.Body.String(), `"protocolVersion"`) {
		t.Fatalf("/mcp initialize missing protocolVersion: %s", mcpRec.Body.String())
	}
	if strings.Contains(mcpRec.Body.String(), `"context7"`) {
		t.Fatalf("/mcp must not serve catalog entries: %s", mcpRec.Body.String())
	}
}

func TestServer_MCPRoute_BindSafetyUnchanged(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "0.0.0.0:8420", Token: ""})
	if err := srv.checkBindSecurity(); err == nil {
		t.Fatal("non-loopback tokenless bind must still be refused")
	}
	srvOK := NewServer(Config{ListenAddr: "0.0.0.0:8420", Token: wiringToken})
	if err := srvOK.checkBindSecurity(); err != nil {
		t.Fatalf("non-loopback with token must remain allowed: %v", err)
	}
}

type bearerRoundTripper struct {
	token string
	base  http.RoundTripper
}

func (b bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	base := b.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(r)
}

func bearerHTTPClient(token string) *http.Client {
	return &http.Client{Transport: bearerRoundTripper{token: token}}
}

func connectWiredMCP(t *testing.T, baseURL, token string) *mcpsdk.ClientSession {
	t.Helper()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "wiring", Version: "0"}, nil)
	cs, err := client.Connect(context.Background(), &mcpsdk.StreamableClientTransport{
		Endpoint:   baseURL + MCPRoute,
		HTTPClient: bearerHTTPClient(token),
	}, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}
