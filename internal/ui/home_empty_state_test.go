package ui

import (
	"strconv"
	"testing"
)

func TestRenderEmptyStateResponsiveDoesNotPanicAtNarrowWidths(t *testing.T) {
	config := EmptyStateConfig{
		Icon:  "○",
		Title: "No remote sessions",
		Hints: []string{"Press r to refresh"},
	}

	for _, width := range []int{9, 10} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			got := renderEmptyStateResponsive(config, width, 8)
			if got == "" {
				t.Fatal("renderEmptyStateResponsive returned empty output")
			}
		})
	}
}
