package web

import (
	"errors"
	"net/http"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPRoute is the dedicated Streamable HTTP MCP endpoint path.
// Existing /api/mcps routes remain separate and unchanged.
const MCPRoute = "/mcp"

// MCPDependencies are the seams NewMCPHandler receives from Server wiring.
// Task 02 implements tool bodies against these; Task 03 wires them from Server.
type MCPDependencies struct {
	Loader           MenuDataLoader
	Mutator          SessionMutator
	Authorize        func(*http.Request) bool
	MutationsAllowed func() bool
	AllowMutation    func() bool
}

// MCP tool / result contracts (stable JSON field names for Tasks 02–03).

type MCPSessionIDInput struct {
	SessionID string `json:"sessionId"`
}

type MCPSendToSessionInput struct {
	SessionID string `json:"sessionId"`
	Message   string `json:"message"`
}

type MCPFleetStatusResult struct {
	Profile       string              `json:"profile"`
	TotalGroups   int                 `json:"totalGroups"`
	TotalSessions int                 `json:"totalSessions"`
	Sessions      []MCPSessionSummary `json:"sessions"`
	Groups        []MCPGroupSummary   `json:"groups"`
}

type MCPSessionSummary struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Tool        string `json:"tool"`
	Status      string `json:"status"`
	GroupPath   string `json:"groupPath"`
	ProjectPath string `json:"projectPath"`
	IsConductor bool   `json:"isConductor"`
}

type MCPGroupSummary struct {
	Name         string `json:"name"`
	Path         string `json:"path"`
	SessionCount int    `json:"sessionCount"`
}

type MCPSessionDetailsResult struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Tool        string `json:"tool"`
	Status      string `json:"status"`
	Substate    string `json:"substate,omitempty"`
	GroupPath   string `json:"groupPath"`
	ProjectPath string `json:"projectPath"`
	IsConductor bool   `json:"isConductor,omitempty"`
}

type MCPMutationResult struct {
	SessionID string `json:"sessionId"`
	OK        bool   `json:"ok"`
}

// MCPErrorKind distinguishes protocol/API failure classes for Task 02 mapping.
type MCPErrorKind string

const (
	MCPErrorUnauthorized     MCPErrorKind = "unauthorized"
	MCPErrorMalformed        MCPErrorKind = "malformed"
	MCPErrorNotFound         MCPErrorKind = "not_found"
	MCPErrorMutationDisabled MCPErrorKind = "mutation_disabled"
	MCPErrorRateLimited      MCPErrorKind = "rate_limited"
	MCPErrorBackend          MCPErrorKind = "backend"
)

var (
	ErrMCPUnauthorized     = errors.New("mcp: unauthorized")
	ErrMCPMalformed        = errors.New("mcp: malformed input")
	ErrMCPNotFound         = errors.New("mcp: not found")
	ErrMCPMutationDisabled = errors.New("mcp: mutations disabled")
	ErrMCPRateLimited      = errors.New("mcp: rate limited")
	ErrMCPBackend          = errors.New("mcp: backend failure")
)

// ClassifyMCPError maps sentinel and common backend strings to MCP error kinds.
func ClassifyMCPError(err error) MCPErrorKind {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, ErrMCPUnauthorized):
		return MCPErrorUnauthorized
	case errors.Is(err, ErrMCPMalformed):
		return MCPErrorMalformed
	case errors.Is(err, ErrMCPNotFound):
		return MCPErrorNotFound
	case errors.Is(err, ErrMCPMutationDisabled):
		return MCPErrorMutationDisabled
	case errors.Is(err, ErrMCPRateLimited):
		return MCPErrorRateLimited
	case errors.Is(err, ErrMCPBackend):
		return MCPErrorBackend
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "not found"):
		return MCPErrorNotFound
	case strings.Contains(msg, "unauthorized"), strings.Contains(msg, "forbidden") && strings.Contains(msg, "token"):
		return MCPErrorUnauthorized
	case strings.Contains(msg, "disabled"):
		return MCPErrorMutationDisabled
	case strings.Contains(msg, "rate limit"), strings.Contains(msg, "too many"):
		return MCPErrorRateLimited
	case strings.Contains(msg, "malformed"),
		strings.Contains(msg, "required property"),
		strings.Contains(msg, "missing properties"), // jsonschema-go: "required: missing properties: [...]"
		strings.Contains(msg, "additional properties"),
		strings.Contains(msg, "additional property"),
		strings.Contains(msg, "unexpected additional"):
		return MCPErrorMalformed
	default:
		return MCPErrorBackend
	}
}

// MCPToolSpec is the JSON-visible tool contract (name, description, schema, hints).
type MCPToolSpec struct {
	Name        string
	Description string
	Annotations *mcpsdk.ToolAnnotations
	InputSchema map[string]any
}

// MCPToolCatalog returns the six fixed remote-MCP tools and their input schemas.
// Task 02 registers these on the Streamable HTTP server; this catalog is the
// authoritative contract for names, required fields, and read-only hints.
func MCPToolCatalog() []MCPToolSpec {
	sessionIDProps := map[string]any{
		"sessionId": map[string]any{
			"type":        "string",
			"description": "Existing Agent Deck session ID",
			"minLength":   1,
		},
	}
	return []MCPToolSpec{
		{
			Name:        "fleet_status",
			Description: "Return current sessions, groups, conductors, and statuses. Read-only.",
			Annotations: &mcpsdk.ToolAnnotations{ReadOnlyHint: true},
			InputSchema: map[string]any{
				"type":                 "object",
				"properties":           map[string]any{},
				"additionalProperties": false,
			},
		},
		{
			Name:        "session_details",
			Description: "Return details for one existing session. Read-only.",
			Annotations: &mcpsdk.ToolAnnotations{ReadOnlyHint: true},
			InputSchema: map[string]any{
				"type":                 "object",
				"properties":           sessionIDProps,
				"required":             []string{"sessionId"},
				"additionalProperties": false,
			},
		},
		{
			Name:        "send_to_session",
			Description: "Send a user-supplied message to one existing session. Does not run shell commands or call arbitrary URLs.",
			Annotations: &mcpsdk.ToolAnnotations{ReadOnlyHint: false},
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"sessionId": map[string]any{
						"type":        "string",
						"description": "Existing Agent Deck session ID",
						"minLength":   1,
					},
					"message": map[string]any{
						"type":        "string",
						"description": "User message to deliver to the session",
						"minLength":   1,
					},
				},
				"required":             []string{"sessionId", "message"},
				"additionalProperties": false,
			},
		},
		{
			Name:        "start_session",
			Description: "Start one existing session.",
			Annotations: &mcpsdk.ToolAnnotations{ReadOnlyHint: false},
			InputSchema: map[string]any{
				"type":                 "object",
				"properties":           sessionIDProps,
				"required":             []string{"sessionId"},
				"additionalProperties": false,
			},
		},
		{
			Name:        "stop_session",
			Description: "Stop one existing session.",
			Annotations: &mcpsdk.ToolAnnotations{ReadOnlyHint: false},
			InputSchema: map[string]any{
				"type":                 "object",
				"properties":           sessionIDProps,
				"required":             []string{"sessionId"},
				"additionalProperties": false,
			},
		},
		{
			Name:        "restart_session",
			Description: "Restart one existing session.",
			Annotations: &mcpsdk.ToolAnnotations{ReadOnlyHint: false},
			InputSchema: map[string]any{
				"type":                 "object",
				"properties":           sessionIDProps,
				"required":             []string{"sessionId"},
				"additionalProperties": false,
			},
		},
	}
}

// NewMCPHandler returns the dedicated Streamable HTTP MCP handler.
//
// Task 01 settles the transport, auth gate, and dependency seams only.
// Tool registration and business logic belong to Task 02; this handler
// exposes a bare MCP server so initialize succeeds over Streamable HTTP.
func NewMCPHandler(deps MCPDependencies) http.Handler {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{
		Name:    "agent-deck",
		Version: "0.0.0-contract",
	}, nil)
	inner := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server {
		return server
	}, &mcpsdk.StreamableHTTPOptions{JSONResponse: true})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Fail closed: a missing Authorize seam is treated as unauthorized.
		if deps.Authorize == nil || !deps.Authorize(r) {
			http.Error(w, ErrMCPUnauthorized.Error(), http.StatusUnauthorized)
			return
		}
		// Keep deps referenced so the constructor signature stays the Task 01
		// contract even before Task 02 consumes Loader/Mutator/mutation seams.
		_ = deps.Loader
		_ = deps.Mutator
		_ = deps.MutationsAllowed
		_ = deps.AllowMutation
		inner.ServeHTTP(w, r)
	})
}
