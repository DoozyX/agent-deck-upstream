package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/web"
)

func TestBuildWebServer_RemoteMCPRouteAuth(t *testing.T) {
	withTempHomeAndConfig(t, "")
	const token = "remote-mcp-secret"
	srv, err := buildWebServer("test-profile",
		[]string{"--listen", "127.0.0.1:0", "--token", token},
		emptyMenuData{}, noopMutator{})
	if err != nil {
		t.Fatalf("buildWebServer: %v", err)
	}

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"cmd","version":"0"}}}`

	post := func(auth string) int {
		req := httptest.NewRequest(http.MethodPost, web.MCPRoute, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec.Code
	}

	if code := post(""); code != http.StatusUnauthorized {
		t.Fatalf("missing token: status=%d, want 401", code)
	}
	if code := post("Bearer wrong"); code != http.StatusUnauthorized {
		t.Fatalf("wrong token: status=%d, want 401", code)
	}
	if code := post("Bearer " + token); code != http.StatusOK {
		t.Fatalf("correct token: status=%d, want 200", code)
	}
}

func TestBuildWebServer_RemoteMCPUnavailableWithoutToken(t *testing.T) {
	withTempHomeAndConfig(t, "")
	srv, err := buildWebServer("test-profile",
		[]string{"--listen", "127.0.0.1:0"}, emptyMenuData{}, noopMutator{})
	if err != nil {
		t.Fatalf("buildWebServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, web.MCPRoute, strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"cmd","version":"0"}}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("tokenless /mcp status=%d body=%s, want 401", rec.Code, rec.Body.String())
	}
}
