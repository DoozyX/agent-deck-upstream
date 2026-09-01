// Package usage adapts the optional openusage executable to Agent Deck's
// small, provider-neutral usage contract.
package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Provider string

const (
	Claude Provider = "claude"
	Codex  Provider = "codex"
)

type Window struct {
	RemainingPercent int       `json:"remaining_percent"`
	ResetsAt         time.Time `json:"resets_at,omitempty"`
}

type Windows struct {
	Session5H *Window `json:"session_5h,omitempty"`
	Weekly    *Window `json:"weekly,omitempty"`
}

type Snapshot struct {
	Available bool      `json:"available"`
	Provider  Provider  `json:"provider"`
	Account   string    `json:"account,omitempty"`
	Plan      string    `json:"plan,omitempty"`
	FetchedAt time.Time `json:"fetched_at,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	Stale     bool      `json:"stale"`
	Windows   Windows   `json:"windows,omitempty"`
	Error     string    `json:"error,omitempty"`
}

type Account struct {
	Provider    Provider
	Home, Label string
}

type Runner struct {
	Path    string
	Timeout time.Duration
}

func (r Runner) Query(ctx context.Context, account Account) (Snapshot, error) {
	if account.Provider != Claude && account.Provider != Codex {
		return Snapshot{}, fmt.Errorf("unsupported usage provider %q", account.Provider)
	}
	path := r.Path
	if path == "" {
		var err error
		path, err = exec.LookPath("openusage")
		if err != nil {
			return Snapshot{}, fmt.Errorf("openusage unavailable: %w", err)
		}
	}
	timeout := r.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, string(account.Provider))
	cmd.Env = append([]string{}, os.Environ()...)
	key := "CLAUDE_CONFIG_DIR="
	if account.Provider == Codex {
		key = "CODEX_HOME="
	}
	filtered := cmd.Env[:0]
	for _, entry := range cmd.Env {
		if !strings.HasPrefix(entry, key) {
			filtered = append(filtered, entry)
		}
	}
	cmd.Env = append(filtered, key+account.Home)
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return Snapshot{}, fmt.Errorf("openusage timeout: %w", ctx.Err())
	}
	if err != nil {
		return Snapshot{}, fmt.Errorf("openusage %s failed: %w", account.Provider, err)
	}
	s, err := Parse(account.Provider, out)
	if err != nil {
		return Snapshot{}, err
	}
	s.Account = account.Label
	s.FetchedAt = time.Now().UTC()
	return s, nil
}

func Parse(provider Provider, raw []byte) (Snapshot, error) {
	var data map[string]json.RawMessage
	if err := json.Unmarshal(raw, &data); err != nil {
		return Snapshot{}, fmt.Errorf("malformed openusage response: %w", err)
	}
	s := Snapshot{Available: true, Provider: provider}
	_ = json.Unmarshal(data["plan"], &s.Plan)
	if stale, ok := boolField(data, "stale"); ok {
		s.Stale = stale
	}
	limits := data["limits"]
	if len(limits) == 0 {
		limits = data["windows"]
	}
	var windows map[string]json.RawMessage
	if err := json.Unmarshal(limits, &windows); err != nil {
		return Snapshot{}, fmt.Errorf("openusage response has no limits")
	}
	if provider == Claude {
		s.Windows.Session5H = parseWindow(windows["five_hour"])
		if s.Windows.Session5H == nil {
			s.Windows.Session5H = parseWindow(windows["session_5h"])
		}
	}
	s.Windows.Weekly = parseWindow(windows["weekly"])
	if s.Windows.Weekly == nil && s.Windows.Session5H == nil {
		return Snapshot{}, fmt.Errorf("openusage response has no supported windows")
	}
	return s, nil
}

func boolField(m map[string]json.RawMessage, key string) (bool, bool) {
	var v bool
	err := json.Unmarshal(m[key], &v)
	return v, err == nil
}
func parseWindow(raw json.RawMessage) *Window {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var value struct {
		RemainingPercent *int      `json:"remaining_percent"`
		Remaining        *int      `json:"remaining"`
		ResetsAt         time.Time `json:"resets_at"`
	}
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	p := value.RemainingPercent
	if p == nil {
		p = value.Remaining
	}
	if p == nil {
		return nil
	}
	return &Window{RemainingPercent: *p, ResetsAt: value.ResetsAt}
}

func CanonicalHome(home string) string {
	if home == "" {
		return ""
	}
	if abs, err := filepath.Abs(home); err == nil {
		home = abs
	}
	if real, err := filepath.EvalSymlinks(home); err == nil {
		home = real
	}
	return filepath.Clean(home)
}

// DefaultHome is the provider's conventional local configuration location.
func DefaultHome(provider Provider) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	if provider == Codex {
		return filepath.Join(home, ".codex")
	}
	return filepath.Join(home, ".claude")
}
