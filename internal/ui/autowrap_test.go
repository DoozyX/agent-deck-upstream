package ui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// The #607 drift class: the TUI clamps every row to exactly h.width cells using
// ansi.StringWidth, but that is a prediction of what the terminal will draw.
// The session list is built almost entirely out of UAX#11 Ambiguous glyphs
// (● ◐ ○ ▶ ▾ ├ └ ─), which a terminal configured for East Asian ambiguous-wide
// — the common default in mobile terminal apps — renders at two cells. A row we
// counted as exactly h.width then overflows, the terminal wraps it onto the next
// physical row, and Bubble Tea's incremental renderer (which believes it wrote
// one line) is off by one for every row below. On screen that reads as stale
// duplicated rows and a second highlighted row left over from the previous
// frame.
//
// The fix pins DECAWM off so the terminal clips the overflow instead of wrapping
// it. These tests pin both halves: the sequence is really emitted on every
// frame, and emitting it cannot itself change the frame's geometry.

func autowrapTestItems() []session.Item {
	return []session.Item{
		{Type: session.ItemTypeSession, Session: &session.Instance{ID: "s1", Title: "install shell"}, Level: 0},
		{Type: session.ItemTypeSession, Session: &session.Instance{ID: "s2", Title: "pine-willow-4f654310"}, Level: 1},
		{Type: session.ItemTypeSession, Session: &session.Instance{ID: "s3", Title: "eager-maple"}, Level: 1},
	}
}

// TestAutowrapPin_FrameCarriesDECAWMOff is the primary gate: without this
// sequence in the byte stream the terminal is free to wrap a mis-measured row
// and the drift is back.
func TestAutowrapPin_FrameCarriesDECAWMOff(t *testing.T) {
	t.Setenv("AGENTDECK_AUTOWRAP", "")

	h := newTestHomeWithItems(60, 24, autowrapTestItems())

	view := h.View()
	if !strings.HasPrefix(view, ansiDisableAutowrap) {
		t.Fatalf("frame does not start with the DECAWM-off sequence %q; a terminal that "+
			"measures an ambiguous-width glyph wider than we did will wrap the row and "+
			"drift every row below it (#607)\nframe head=%q", ansiDisableAutowrap, head(view))
	}
	if strings.Count(view, ansiEnableAutowrap) != 0 {
		t.Fatalf("frame re-enables autowrap mid-frame, defeating the pin")
	}
}

// TestAutowrapPin_DoesNotChangeFrameGeometry is the correctness half: the pin is
// only safe because the sequence occupies zero terminal cells. If it ever cost a
// cell it would push every row one column right and become the bug it fixes.
func TestAutowrapPin_DoesNotChangeFrameGeometry(t *testing.T) {
	t.Setenv("AGENTDECK_AUTOWRAP", "")

	h := newTestHomeWithItems(60, 24, autowrapTestItems())

	raw := h.renderFrame()
	pinned := pinAutowrapOff(raw)

	if got := ansi.StringWidth(ansiDisableAutowrap); got != 0 {
		t.Fatalf("DECAWM-off sequence measures %d cells, want 0 — pinning it would shift the frame", got)
	}

	rawRows := strings.Split(raw, "\n")
	pinnedRows := strings.Split(pinned, "\n")
	if len(rawRows) != len(pinnedRows) {
		t.Fatalf("pin changed row count: %d -> %d", len(rawRows), len(pinnedRows))
	}
	for i := range rawRows {
		if got, want := lipgloss.Width(pinnedRows[i]), lipgloss.Width(rawRows[i]); got != want {
			t.Fatalf("pin changed row %d width: %d -> %d", i, want, got)
		}
		if got, want := ansi.Strip(pinnedRows[i]), ansi.Strip(rawRows[i]); got != want {
			t.Fatalf("pin changed row %d visible text:\n got: %q\nwant: %q", i, got, want)
		}
	}
}

// TestAutowrapPin_EmptyFrameStaysEmpty keeps "no frame" distinguishable from "a
// frame of pure escape bytes" — Bubble Tea and several call sites treat an empty
// View as a signal.
func TestAutowrapPin_EmptyFrameStaysEmpty(t *testing.T) {
	t.Setenv("AGENTDECK_AUTOWRAP", "")

	if got := pinAutowrapOff(""); got != "" {
		t.Fatalf("empty frame became %q", got)
	}
}

// TestAutowrapPin_IsIdempotent matters because View re-pins the cached
// lastRenderedFrame on every attach frame; without the guard the prefix would
// accumulate for as long as an attach lasts.
func TestAutowrapPin_IsIdempotent(t *testing.T) {
	t.Setenv("AGENTDECK_AUTOWRAP", "")

	once := pinAutowrapOff("row")
	twice := pinAutowrapOff(once)
	if once != twice {
		t.Fatalf("pin is not idempotent:\n once: %q\ntwice: %q", once, twice)
	}
	if n := strings.Count(twice, ansiDisableAutowrap); n != 1 {
		t.Fatalf("pin appears %d times after double application, want 1", n)
	}
}

// TestAutowrapPin_OptOut covers the escape hatch: this changes a global terminal
// mode, so a user on a terminal where it misbehaves must be able to turn it off
// without giving up the TUI.
func TestAutowrapPin_OptOut(t *testing.T) {
	t.Setenv("AGENTDECK_AUTOWRAP", "keep")

	if got := pinAutowrapOff("row"); got != "row" {
		t.Fatalf("AGENTDECK_AUTOWRAP=keep still pinned the frame: %q", got)
	}

	var buf bytes.Buffer
	RestoreAutowrap(&buf)
	if buf.Len() != 0 {
		t.Fatalf("AGENTDECK_AUTOWRAP=keep still wrote a restore sequence: %q", buf.String())
	}
}

// TestRestoreAutowrap_ReenablesWrapping guards the exit contract. DECAWM is a
// global terminal mode and leaving the alternate screen does not restore it, so
// forgetting this would hand the user back a shell that never wraps.
func TestRestoreAutowrap_ReenablesWrapping(t *testing.T) {
	t.Setenv("AGENTDECK_AUTOWRAP", "")

	var buf bytes.Buffer
	RestoreAutowrap(&buf)
	if got := buf.String(); got != ansiEnableAutowrap {
		t.Fatalf("RestoreAutowrap wrote %q, want %q", got, ansiEnableAutowrap)
	}

	RestoreAutowrap(nil) // must not panic
}

func head(s string) string {
	if len(s) > 60 {
		return s[:60]
	}
	return s
}
