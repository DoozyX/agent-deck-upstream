package web

import (
	"errors"
	"net/http"
	"strings"
)

// registerMCPRoute mounts the dedicated Streamable HTTP MCP endpoint.
// Production MCP is available when Config.Token is non-empty. MCPNoAuth is an
// explicit temporary escape hatch for the dedicated endpoint only; it does
// not change authorization on the rest of the web server.
func (s *Server) registerMCPRoute(mux *http.ServeMux) {
	if strings.TrimSpace(s.cfg.Token) == "" && !s.cfg.MCPNoAuth {
		mux.HandleFunc(MCPRoute, func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, ErrMCPUnauthorized.Error(), http.StatusUnauthorized)
		})
		return
	}
	authorize := s.authorizeRequest
	if s.cfg.MCPNoAuth {
		authorize = func(*http.Request) bool { return true }
	}

	mux.Handle(MCPRoute, NewMCPHandler(MCPDependencies{
		Loader:    s.menuData,
		Mutator:   &mcpLiveMutator{s: s},
		Authorize: authorize,
		MutationsAllowed: func() bool {
			// ReadOnly and WebMutations are independent Config inputs; both must
			// allow writes (web_cmd clears WebMutations when --read-only is set,
			// but direct Config{ReadOnly:true, WebMutations:true} must still deny).
			return !s.cfg.ReadOnly && s.cfg.WebMutations
		},
		AllowMutation: func() bool {
			return s.mutationLimiter.Allow()
		},
	}))
}

// mcpLiveMutator forwards SessionMutator calls to the Server-injected mutator
// so SetMutator after NewServer still reaches MCP tools.
type mcpLiveMutator struct {
	s *Server
}

func (m *mcpLiveMutator) mutator() SessionMutator {
	if m == nil || m.s == nil {
		return nil
	}
	return m.s.mutator
}

func (m *mcpLiveMutator) CreateSession(title, tool, projectPath, groupPath, modelID, reasoningEffort string) (string, error) {
	if mut := m.mutator(); mut != nil {
		return mut.CreateSession(title, tool, projectPath, groupPath, modelID, reasoningEffort)
	}
	return "", errMCPMutatorUnavailable
}

func (m *mcpLiveMutator) StartSession(sessionID string) error {
	if mut := m.mutator(); mut != nil {
		return mut.StartSession(sessionID)
	}
	return errMCPMutatorUnavailable
}

func (m *mcpLiveMutator) StopSession(sessionID string) error {
	if mut := m.mutator(); mut != nil {
		return mut.StopSession(sessionID)
	}
	return errMCPMutatorUnavailable
}

func (m *mcpLiveMutator) RestartSession(sessionID string) error {
	if mut := m.mutator(); mut != nil {
		return mut.RestartSession(sessionID)
	}
	return errMCPMutatorUnavailable
}

func (m *mcpLiveMutator) SendToSession(sessionID, message string) error {
	if mut := m.mutator(); mut != nil {
		return mut.SendToSession(sessionID, message)
	}
	return errMCPMutatorUnavailable
}

func (m *mcpLiveMutator) DeleteSession(sessionID string) error {
	if mut := m.mutator(); mut != nil {
		return mut.DeleteSession(sessionID)
	}
	return errMCPMutatorUnavailable
}

func (m *mcpLiveMutator) CloseSession(sessionID string) error {
	if mut := m.mutator(); mut != nil {
		return mut.CloseSession(sessionID)
	}
	return errMCPMutatorUnavailable
}

func (m *mcpLiveMutator) ArchiveSession(sessionID string) error {
	if mut := m.mutator(); mut != nil {
		return mut.ArchiveSession(sessionID)
	}
	return errMCPMutatorUnavailable
}

func (m *mcpLiveMutator) UnarchiveSession(sessionID string) error {
	if mut := m.mutator(); mut != nil {
		return mut.UnarchiveSession(sessionID)
	}
	return errMCPMutatorUnavailable
}

func (m *mcpLiveMutator) ForkSession(sessionID string) (string, error) {
	if mut := m.mutator(); mut != nil {
		return mut.ForkSession(sessionID)
	}
	return "", errMCPMutatorUnavailable
}

func (m *mcpLiveMutator) UndoDelete() (string, error) {
	if mut := m.mutator(); mut != nil {
		return mut.UndoDelete()
	}
	return "", errMCPMutatorUnavailable
}

func (m *mcpLiveMutator) UpdateSession(sessionID string, updates map[string]string) ([]string, bool, error) {
	if mut := m.mutator(); mut != nil {
		return mut.UpdateSession(sessionID, updates)
	}
	return nil, false, errMCPMutatorUnavailable
}

func (m *mcpLiveMutator) CreateGroup(name, parentPath string) (string, error) {
	if mut := m.mutator(); mut != nil {
		return mut.CreateGroup(name, parentPath)
	}
	return "", errMCPMutatorUnavailable
}

func (m *mcpLiveMutator) RenameGroup(groupPath, newName string) error {
	if mut := m.mutator(); mut != nil {
		return mut.RenameGroup(groupPath, newName)
	}
	return errMCPMutatorUnavailable
}

func (m *mcpLiveMutator) DeleteGroup(groupPath string) error {
	if mut := m.mutator(); mut != nil {
		return mut.DeleteGroup(groupPath)
	}
	return errMCPMutatorUnavailable
}

func (m *mcpLiveMutator) FinishWorktree(sessionID string, opts WorktreeFinishOptions) (WorktreeFinishResult, error) {
	if mut := m.mutator(); mut != nil {
		return mut.FinishWorktree(sessionID, opts)
	}
	return WorktreeFinishResult{}, errMCPMutatorUnavailable
}

// errMCPMutatorUnavailable is returned when SetMutator has not wired a backend.
var errMCPMutatorUnavailable = errors.New("session mutator unavailable")
