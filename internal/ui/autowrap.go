package ui

import (
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// Terminal autowrap (DECAWM) control sequences.
//
// #607 class — "row offset drift", reported again from a phone terminal over
// SSH ("multiple rows look selected, list content duplicated on scroll").
//
// Every frame this TUI emits is clamped to exactly h.width cells per row by
// clampViewToViewport, measured with ansi.StringWidth. That measurement is a
// *prediction* of what the terminal will do. Terminals disagree with it:
// East Asian ambiguous-width glyphs (the list is built out of ● ◐ ○ ▶ ▾ ├ └ ─,
// all UAX#11 Ambiguous), emoji presentation, and grapheme-cluster width modes
// (Ghostty's grapheme-width-method=unicode, most mobile terminal apps) can each
// render a row wider than we counted.
//
// With autowrap ON, one such row costs two physical rows: the terminal wraps
// the overflow, Bubble Tea's incremental renderer still believes it wrote one
// line, and every row below is off by one from then on. The visible result is
// exactly the report — stale rows left on screen, a second highlighted row from
// the previous frame, content that looks duplicated after a scroll.
//
// With autowrap OFF the terminal clips the overflow at the right edge instead.
// A row can lose a trailing cell (usually one of clampViewToViewport's pad
// spaces), but a mis-measured glyph can no longer shift the rest of the screen.
// That trade is strictly better: drift is unrecoverable without a full repaint,
// clipping is invisible.
//
// This is a global terminal mode, so it must be restored on exit — see
// RestoreAutowrap, called from the TUI entry point after the Bubble Tea program
// returns. Set AGENTDECK_AUTOWRAP=keep to opt out entirely.
const (
	ansiDisableAutowrap = "\x1b[?7l"
	ansiEnableAutowrap  = "\x1b[?7h"
)

// autowrapPinDisabled reports whether the user opted out of the DECAWM pin via
// AGENTDECK_AUTOWRAP=keep. Any other value (including unset) keeps the pin on.
func autowrapPinDisabled() bool {
	return strings.EqualFold(os.Getenv("AGENTDECK_AUTOWRAP"), "keep")
}

// pinAutowrapOff prefixes a rendered frame with the DECAWM-off sequence so the
// terminal clips over-wide rows rather than wrapping them.
//
// The sequence occupies zero terminal cells, so the frame's row count and every
// row's rendered width are unchanged — it is safe to apply to any frame,
// including the modal panels that return before clampViewToViewport.
//
// Applied once per frame at the head: DECAWM is sticky, and every path that
// could reset it (resize, tea.ClearScreen under full_repaint, the initial
// altscreen enter) forces Bubble Tea to repaint row 0, which re-asserts it.
// An empty frame is returned unchanged so "no frame" stays distinguishable
// from "a frame of pure escape bytes".
func pinAutowrapOff(frame string) string {
	if frame == "" || autowrapPinDisabled() {
		return frame
	}
	if strings.HasPrefix(frame, ansiDisableAutowrap) {
		return frame
	}
	return ansiDisableAutowrap + frame
}

// RestoreAutowrap re-enables terminal autowrap (DECAWM). Call it once the
// Bubble Tea program has returned, and again before handing the terminal to an
// exec'd program, so nothing downstream inherits a non-wrapping terminal. It is
// a no-op when the pin was never applied.
//
// Writing to a non-terminal os.File is skipped so this never leaks escape bytes
// into a redirected stdout (test output, `agent-deck | tee`); any other writer
// is honoured so callers can assert on it.
func RestoreAutowrap(w io.Writer) {
	if w == nil || autowrapPinDisabled() {
		return
	}
	if f, ok := w.(*os.File); ok && !term.IsTerminal(int(f.Fd())) {
		return
	}
	_, _ = io.WriteString(w, ansiEnableAutowrap)
}
