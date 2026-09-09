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
	OutputReader     SessionOutputReader
	Authorize        func(*http.Request) bool
	MutationsAllowed func() bool
	AllowMutation    func() bool
}

type SessionOutputReader interface {
	SessionOutput(sessionID string) (string, error)
}

// MCP tool / result contracts (stable JSON field names for Tasks 02–03).

type MCPSessionIDInput struct {
	SessionID string `json:"sessionId"`
}

type MCPSendToSessionInput struct {
	SessionID string `json:"sessionId"`
	Message   string `json:"message"`
}

type MCPCreateSessionInput struct {
	Title           string `json:"title"`
	Tool            string `json:"tool,omitempty"`
	ProjectPath     string `json:"projectPath"`
	GroupPath       string `json:"groupPath,omitempty"`
	ModelID         string `json:"modelId,omitempty"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
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

type MCPSessionOutputResult struct {
	SessionID string `json:"sessionId"`
	Content   string `json:"content"`
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
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case strings.Contains(msg, "not found"):
		return MCPErrorNotFound
	case strings.Contains(msg, "unauthorized"), strings.Contains(msg, "forbidden") && strings.Contains(msg, "token"):
		return MCPErrorUnauthorized
	case strings.Contains(msg, "disabled"):
		return MCPErrorMutationDisabled
	case strings.Contains(msg, "rate limit"), strings.Contains(msg, "too many"):
		return MCPErrorRateLimited
	case isMCPMalformedValidationMessage(msg):
		return MCPErrorMalformed
	default:
		return MCPErrorBackend
	}
}

// MCPAdditionalPropertyPrefix is the exact singular MCP validator wrapper form
// ClassifyMCPError accepts for undeclared tool inputs (Task 02 may emit this).
const MCPAdditionalPropertyPrefix = "mcp: additional property:"

// isMCPMalformedValidationMessage matches only pinned jsonschema-go forms and
// the explicit MCP singular wrapper prefix — not bare substrings like
// "missing properties" or "additional property" that can appear in backend text.
// jsonschema-go may wrap as "validating <path>: <form>"; peel only those
// "validating …: " wrappers until a recognized form is a prefix of the remainder.
// Do not peel arbitrary "prefix: " segments (e.g. "storage: required: …").
func isMCPMalformedValidationMessage(msg string) bool {
	for {
		msg = strings.TrimSpace(msg)
		switch {
		case strings.HasPrefix(msg, "mcp: malformed"),
			strings.HasPrefix(msg, MCPAdditionalPropertyPrefix),
			strings.HasPrefix(msg, "required: missing properties:"),
			strings.HasPrefix(msg, "minlength:"),
			strings.HasPrefix(msg, "unexpected additional properties"):
			return true
		}
		// Only peel jsonschema-go "validating <path>: …" wrappers.
		if !strings.HasPrefix(msg, "validating ") {
			return false
		}
		_, rest, ok := strings.Cut(msg, ": ")
		if !ok || rest == "" || rest == msg {
			return false
		}
		msg = rest
	}
}

// MCPToolSpec is the JSON-visible tool contract (name, description, schema, hints).
type MCPToolSpec struct {
	Name        string
	Description string
	Annotations *mcpsdk.ToolAnnotations
	InputSchema map[string]any
}

// MCPToolCatalog returns the nine fixed remote-MCP tools and their input schemas.
// Task 02 registers these on the Streamable HTTP server; this catalog is the
// authoritative contract for names, required fields, and read-only hints.
func MCPToolCatalog() []MCPToolSpec {
	notDestructive := false
	destructive := true
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
		{
			Name:        "create_session",
			Description: "Create, start, and persist a new Agent Deck session.",
			Annotations: &mcpsdk.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &notDestructive},
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"title":           map[string]any{"type": "string", "minLength": 1},
					"tool":            map[string]any{"type": "string"},
					"projectPath":     map[string]any{"type": "string", "minLength": 1},
					"groupPath":       map[string]any{"type": "string"},
					"modelId":         map[string]any{"type": "string"},
					"reasoningEffort": map[string]any{"type": "string"},
				},
				"required":             []string{"title", "projectPath"},
				"additionalProperties": false,
			},
		},
		{
			Name:        "delete_session",
			Description: "Delete one existing session through Agent Deck's recoverable deletion path.",
			Annotations: &mcpsdk.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive},
			InputSchema: map[string]any{
				"type":                 "object",
				"properties":           sessionIDProps,
				"required":             []string{"sessionId"},
				"additionalProperties": false,
			},
		},
		{
			Name:        "session_output",
			Description: "Return the latest assistant response from one local Agent Deck session. Read-only.",
			Annotations: &mcpsdk.ToolAnnotations{ReadOnlyHint: true},
			InputSchema: map[string]any{
				"type":                 "object",
				"properties":           sessionIDProps,
				"required":             []string{"sessionId"},
				"additionalProperties": false,
			},
		},
	}
}

// NewMCPHandler is implemented in mcp.go (Task 02): Streamable HTTP transport,
// auth gate, and the nine tool registrations against MCPDependencies.
