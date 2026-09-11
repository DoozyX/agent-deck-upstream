package main

import (
	"reflect"
	"testing"
)

// A help invocation for a command that does not exist must never be mistaken
// for "boot the TUI". `agent-deck attach --help` did exactly that: `attach` is
// not in main()'s dispatch switch, so it fell through to the bubbletea boot
// path and ran headless for 52 days at ~90% CPU, re-probing the configured
// remote over SSH forever, because nothing downstream ever rejects an
// unrecognized first token.
func TestResidualTUIArgs(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantExtra []string
		wantHelp  bool
	}{
		{name: "bare launch", args: nil, wantExtra: nil},
		{name: "group long", args: []string{"--group", "fjordbyte"}, wantExtra: nil},
		{name: "group short", args: []string{"-g", "fjordbyte"}, wantExtra: nil},
		{name: "group equals", args: []string{"--group=fjordbyte"}, wantExtra: nil},
		{name: "select", args: []string{"--select", "coral-wren"}, wantExtra: nil},
		{name: "select equals", args: []string{"--select=coral-wren"}, wantExtra: nil},
		{name: "combined", args: []string{"-g", "x", "--select=y"}, wantExtra: nil},

		// The 52-day process.
		{name: "unknown subcommand with help", args: []string{"attach", "--help"}, wantExtra: []string{"attach"}, wantHelp: true},
		{name: "unknown subcommand", args: []string{"attach"}, wantExtra: []string{"attach"}},
		{name: "typo subcommand", args: []string{"lst"}, wantExtra: []string{"lst"}},
		{name: "unknown flag", args: []string{"--halp"}, wantExtra: []string{"--halp"}},
		{name: "stray path", args: []string{"/Users/me/proj"}, wantExtra: []string{"/Users/me/proj"}},

		// Help alongside only valid TUI flags is help, not an unknown command.
		{name: "help with group", args: []string{"-g", "x", "--help"}, wantExtra: nil, wantHelp: true},
		{name: "bare help", args: []string{"-h"}, wantExtra: nil, wantHelp: true},

		// A group/select value must not be re-reported as an unknown command,
		// even when it looks exactly like one.
		{name: "group value shadows command", args: []string{"-g", "list"}, wantExtra: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extra, help := residualTUIArgs(tt.args)
			if !reflect.DeepEqual(extra, tt.wantExtra) {
				t.Errorf("residualTUIArgs(%q) extra = %q, want %q", tt.args, extra, tt.wantExtra)
			}
			if help != tt.wantHelp {
				t.Errorf("residualTUIArgs(%q) help = %v, want %v", tt.args, help, tt.wantHelp)
			}
		})
	}
}

// The TUI must refuse to boot when stdout is a pipe. This is the guard that
// would have stopped the 52-day process regardless of the dispatch bug: it was
// launched as `agent-deck attach --help 2>&1 | head -8`, so stdout was never a
// terminal and no human could ever have seen or quit it.
func TestHeadlessTUIRefused(t *testing.T) {
	tests := []struct {
		name        string
		isTTY       bool
		webHeadless bool
		allowEnv    bool
		wantRefused bool
	}{
		{name: "interactive terminal boots", isTTY: true, wantRefused: false},
		{name: "piped stdout refused", isTTY: false, wantRefused: true},
		{name: "web --no-tui never boots a TUI", isTTY: false, webHeadless: true, wantRefused: false},
		{name: "escape hatch honored", isTTY: false, allowEnv: true, wantRefused: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := headlessTUIRefused(tt.isTTY, tt.webHeadless, tt.allowEnv); got != tt.wantRefused {
				t.Errorf("headlessTUIRefused(tty=%v, headless=%v, allow=%v) = %v, want %v",
					tt.isTTY, tt.webHeadless, tt.allowEnv, got, tt.wantRefused)
			}
		})
	}
}
