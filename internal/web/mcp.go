package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewMCPHandler returns the dedicated Streamable HTTP MCP handler with the six
// approved tools registered against Loader/Mutator seams.
func NewMCPHandler(deps MCPDependencies) http.Handler {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{
		Name:    "agent-deck",
		Version: "0.0.0-mcp",
	}, nil)
	registerMCPTools(server, deps)

	inner := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server {
		return server
	}, &mcpsdk.StreamableHTTPOptions{JSONResponse: true})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Fail closed: a missing Authorize seam must not bypass auth (Task 01 fix).
		if deps.Authorize == nil || !deps.Authorize(r) {
			http.Error(w, ErrMCPUnauthorized.Error(), http.StatusUnauthorized)
			return
		}
		inner.ServeHTTP(w, r)
	})
}

func registerMCPTools(server *mcpsdk.Server, deps MCPDependencies) {
	for _, spec := range MCPToolCatalog() {
		tool := &mcpsdk.Tool{
			Name:        spec.Name,
			Description: spec.Description,
			Annotations: spec.Annotations,
			InputSchema: spec.InputSchema,
		}
		switch spec.Name {
		case "fleet_status":
			mcpsdk.AddTool(server, tool, func(ctx context.Context, req *mcpsdk.CallToolRequest, _ struct{}) (*mcpsdk.CallToolResult, MCPFleetStatusResult, error) {
				out, err := mcpFleetStatus(deps)
				return nil, out, err
			})
		case "session_details":
			mcpsdk.AddTool(server, tool, func(ctx context.Context, req *mcpsdk.CallToolRequest, in MCPSessionIDInput) (*mcpsdk.CallToolResult, MCPSessionDetailsResult, error) {
				out, err := mcpSessionDetails(deps, in.SessionID)
				return nil, out, err
			})
		case "send_to_session":
			mcpsdk.AddTool(server, tool, func(ctx context.Context, req *mcpsdk.CallToolRequest, in MCPSendToSessionInput) (*mcpsdk.CallToolResult, MCPMutationResult, error) {
				out, err := mcpSendToSession(deps, in)
				return nil, out, err
			})
		case "start_session":
			mcpsdk.AddTool(server, tool, func(ctx context.Context, req *mcpsdk.CallToolRequest, in MCPSessionIDInput) (*mcpsdk.CallToolResult, MCPMutationResult, error) {
				out, err := mcpMutateSession(deps, in.SessionID, func(m SessionMutator, id string) error {
					return m.StartSession(id)
				})
				return nil, out, err
			})
		case "stop_session":
			mcpsdk.AddTool(server, tool, func(ctx context.Context, req *mcpsdk.CallToolRequest, in MCPSessionIDInput) (*mcpsdk.CallToolResult, MCPMutationResult, error) {
				out, err := mcpMutateSession(deps, in.SessionID, func(m SessionMutator, id string) error {
					return m.StopSession(id)
				})
				return nil, out, err
			})
		case "restart_session":
			mcpsdk.AddTool(server, tool, func(ctx context.Context, req *mcpsdk.CallToolRequest, in MCPSessionIDInput) (*mcpsdk.CallToolResult, MCPMutationResult, error) {
				out, err := mcpMutateSession(deps, in.SessionID, func(m SessionMutator, id string) error {
					return m.RestartSession(id)
				})
				return nil, out, err
			})
		default:
			panic(fmt.Sprintf("unexpected MCP tool %q", spec.Name))
		}
	}
}

func mcpGuardMutation(deps MCPDependencies) error {
	if deps.MutationsAllowed != nil && !deps.MutationsAllowed() {
		return ErrMCPMutationDisabled
	}
	if deps.AllowMutation != nil && !deps.AllowMutation() {
		return ErrMCPRateLimited
	}
	return nil
}

func mcpRequireSessionID(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", fmt.Errorf("%w: sessionId is required", ErrMCPMalformed)
	}
	return id, nil
}

func mcpFleetStatus(deps MCPDependencies) (MCPFleetStatusResult, error) {
	if deps.Loader == nil {
		return MCPFleetStatusResult{}, fmt.Errorf("%w: menu loader unavailable", ErrMCPBackend)
	}
	snap, err := deps.Loader.LoadMenuSnapshot()
	if err != nil {
		return MCPFleetStatusResult{}, fmt.Errorf("%w: %v", ErrMCPBackend, err)
	}
	if snap == nil {
		return MCPFleetStatusResult{}, fmt.Errorf("%w: empty menu snapshot", ErrMCPBackend)
	}
	out := MCPFleetStatusResult{
		Profile:       snap.Profile,
		TotalGroups:   snap.TotalGroups,
		TotalSessions: snap.TotalSessions,
		Sessions:      make([]MCPSessionSummary, 0, snap.TotalSessions),
		Groups:        make([]MCPGroupSummary, 0, snap.TotalGroups),
	}
	for _, item := range snap.Items {
		if item.Group != nil {
			out.Groups = append(out.Groups, MCPGroupSummary{
				Name:         item.Group.Name,
				Path:         item.Group.Path,
				SessionCount: item.Group.SessionCount,
			})
		}
		if item.Session != nil {
			out.Sessions = append(out.Sessions, MCPSessionSummary{
				ID:          item.Session.ID,
				Title:       item.Session.Title,
				Tool:        item.Session.Tool,
				Status:      string(item.Session.Status),
				GroupPath:   item.Session.GroupPath,
				ProjectPath: item.Session.ProjectPath,
				IsConductor: item.Session.IsConductor,
			})
		}
	}
	return out, nil
}

func mcpSessionDetails(deps MCPDependencies, sessionID string) (MCPSessionDetailsResult, error) {
	id, err := mcpRequireSessionID(sessionID)
	if err != nil {
		return MCPSessionDetailsResult{}, err
	}
	if deps.Loader == nil {
		return MCPSessionDetailsResult{}, fmt.Errorf("%w: menu loader unavailable", ErrMCPBackend)
	}
	snap, err := deps.Loader.LoadMenuSnapshot()
	if err != nil {
		return MCPSessionDetailsResult{}, fmt.Errorf("%w: %v", ErrMCPBackend, err)
	}
	if snap == nil {
		return MCPSessionDetailsResult{}, fmt.Errorf("%w: empty menu snapshot", ErrMCPBackend)
	}
	for _, item := range snap.Items {
		if item.Session == nil || item.Session.ID != id {
			continue
		}
		s := item.Session
		return MCPSessionDetailsResult{
			ID:          s.ID,
			Title:       s.Title,
			Tool:        s.Tool,
			Status:      string(s.Status),
			Substate:    s.Substate,
			GroupPath:   s.GroupPath,
			ProjectPath: s.ProjectPath,
			IsConductor: s.IsConductor,
		}, nil
	}
	return MCPSessionDetailsResult{}, fmt.Errorf("%w: session %q", ErrMCPNotFound, id)
}

func mcpSendToSession(deps MCPDependencies, in MCPSendToSessionInput) (MCPMutationResult, error) {
	if err := mcpGuardMutation(deps); err != nil {
		return MCPMutationResult{}, err
	}
	id, err := mcpRequireSessionID(in.SessionID)
	if err != nil {
		return MCPMutationResult{}, err
	}
	msg := strings.TrimSpace(in.Message)
	if msg == "" {
		return MCPMutationResult{}, fmt.Errorf("%w: message is required", ErrMCPMalformed)
	}
	if deps.Mutator == nil {
		return MCPMutationResult{}, fmt.Errorf("%w: session mutator unavailable", ErrMCPBackend)
	}
	if err := deps.Mutator.SendToSession(id, msg); err != nil {
		return MCPMutationResult{}, mapMutatorError(err)
	}
	return MCPMutationResult{SessionID: id, OK: true}, nil
}

func mcpMutateSession(deps MCPDependencies, sessionID string, fn func(SessionMutator, string) error) (MCPMutationResult, error) {
	if err := mcpGuardMutation(deps); err != nil {
		return MCPMutationResult{}, err
	}
	id, err := mcpRequireSessionID(sessionID)
	if err != nil {
		return MCPMutationResult{}, err
	}
	if deps.Mutator == nil {
		return MCPMutationResult{}, fmt.Errorf("%w: session mutator unavailable", ErrMCPBackend)
	}
	if err := fn(deps.Mutator, id); err != nil {
		return MCPMutationResult{}, mapMutatorError(err)
	}
	return MCPMutationResult{SessionID: id, OK: true}, nil
}

func mapMutatorError(err error) error {
	if err == nil {
		return nil
	}
	switch ClassifyMCPError(err) {
	case MCPErrorNotFound:
		return fmt.Errorf("%w: %v", ErrMCPNotFound, err)
	case MCPErrorUnauthorized:
		return fmt.Errorf("%w: %v", ErrMCPUnauthorized, err)
	case MCPErrorMutationDisabled:
		return fmt.Errorf("%w: %v", ErrMCPMutationDisabled, err)
	case MCPErrorRateLimited:
		return fmt.Errorf("%w: %v", ErrMCPRateLimited, err)
	case MCPErrorMalformed:
		return fmt.Errorf("%w: %v", ErrMCPMalformed, err)
	default:
		return fmt.Errorf("%w: %v", ErrMCPBackend, err)
	}
}
