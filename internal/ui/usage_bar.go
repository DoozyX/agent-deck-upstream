package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/usage"
	"github.com/charmbracelet/lipgloss"
)

const usageRefreshInterval = 30 * time.Second

func usageRefreshDue(last time.Time, inFlight bool, now time.Time) bool {
	return !inFlight && (last.IsZero() || !now.Before(last.Add(usageRefreshInterval)))
}

// renderUsageBar is deliberately presentation-only: collection happens away
// from the render loop so cursor movement never waits on openusage.
func renderUsageBar(snapshots []usage.Snapshot, width int) string {
	parts := make([]string, 0, len(snapshots))
	for _, s := range snapshots {
		if !s.Available || s.Error != "" {
			continue
		}
		label := s.Account
		if label == "" {
			label = string(s.Provider)
		}
		provider := strings.Title(string(s.Provider))
		prefix := ""
		if s.Stale {
			prefix = "~"
		}
		part := fmt.Sprintf("%s%s %s", prefix, provider, label)
		if s.Provider == usage.Claude && s.Windows.Session5H != nil {
			part += fmt.Sprintf(" 5h %d%%", s.Windows.Session5H.RemainingPercent)
		}
		if s.Windows.Weekly != nil {
			part += fmt.Sprintf(" W%d%%", s.Windows.Weekly.RemainingPercent)
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 || width <= 0 {
		return ""
	}
	lines := []string{}
	line := ""
	for _, part := range parts {
		candidate := part
		if line != "" {
			candidate = line + " | " + part
		}
		if line != "" && lipgloss.Width(candidate) > width {
			lines = append(lines, line)
			line = part
		} else {
			line = candidate
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lipgloss.NewStyle().Foreground(ColorTextDim).Render(strings.Join(lines, "\n"))
}
