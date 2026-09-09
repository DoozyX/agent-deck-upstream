package tmux

import (
	"regexp"
	"strings"
)

// Claude renders two kinds of modal selection on top of the composer: a tool
// permission dialog ("Do you want to proceed? / ❯ 1. Yes / 2. No") and an
// AskUserQuestion decision menu ("❯ 1. Reclaim now … / 2. Fix the defect …").
// Both occupy the composer region, and BOTH are UI, not typed text.
//
// That distinction is the whole point of this file. The composer-draft guard
// (internal/send) parses the composer region, and to it a rendered menu is
// indistinguishable from an operator's half-typed prompt: it saved the option
// list as a "draft", pressed Ctrl+C — which DISMISSES the question — sent its
// own message, then typed the menu's rendering back into the composer as
// literal text.
//
// Observed 2026-08-20 on an orchestrate conductor: its 15-minute heartbeat ate
// two AskUserQuestion decision prompts in 45 minutes. Each time the conductor
// re-asked, the next beat destroyed the question again, and the run sat for
// over an hour with three finished children behind a decision the human was
// never given the chance to answer. Nothing in the pane text distinguished
// that from a healthy idle prompt, so every status/substate observer called it
// idle-at-empty-prompt and no supervisor escalated.

// choiceFooterMarkers are footers Claude renders only while a modal selection
// is on screen. They are matched on the pane tail, so a phrase quoted earlier
// in the conversation cannot trip them.
var choiceFooterMarkers = []string{
	"Esc to cancel",
	"Use arrow keys to navigate",
	"Press Enter to select",
	"No, and tell Claude what to do differently",
	"Yes, allow once",
	"Yes, allow always",
	"Yes, and don't ask again",
	"Do you want to proceed?",
	"Do you trust the files in this folder?",
}

// selectedChoiceLine matches the SELECTED option of a menu: a selection marker
// and a numbered option on the SAME line ("❯ 1. Yes", "│ ❯ 1. Reclaim now").
// The marker is the discriminator that keeps ordinary prose out: an assistant
// message listing "1. The disk." above an empty "❯ " composer puts the marker
// on its own line and does not match.
var selectedChoiceLine = regexp.MustCompile(`^[❯›>]\s*([1-9][0-9]?)\.\s+\S`)

// otherChoiceLine matches any numbered option line, selected or not.
var otherChoiceLine = regexp.MustCompile(`^(?:[❯›>]\s*)?([1-9][0-9]?)\.\s+\S`)

// composerMarkers are the glyphs Claude draws its composer input line with. A
// menu's SELECTED option uses the same "❯", so otherChoiceLine discriminates.
var composerMarkers = []rune{'❯', '›', '>'}

// isComposerLine reports whether a trimmed pane line is Claude's composer input
// — a bare prompt or an operator draft — rather than a menu's selected option.
func isComposerLine(line string) bool {
	r := []rune(line)
	if len(r) == 0 {
		return false
	}
	found := false
	for _, m := range composerMarkers {
		if r[0] == m {
			found = true
			break
		}
	}
	if !found {
		return false
	}
	// "❯ 1. Yes" is a menu option wearing the same marker, not the composer.
	return !otherChoiceLine.MatchString(line)
}

// choiceTailLines is how far up the pane the scan reaches. A modal selection is
// always at the bottom; scanning further would start matching scrollback.
const choiceTailLines = 40

// PaneAwaitsChoice reports whether the pane is showing a modal selection that
// is waiting for a human to pick an option — a permission dialog or an
// AskUserQuestion menu.
//
// content must be ANSI-stripped pane text.
//
// The verdict is deliberately conservative in the direction that matters: a
// false positive only makes an automated sender refuse and escalate to a human
// (recoverable), while a false negative destroys the question (not).
func PaneAwaitsChoice(content string) bool {
	lines := strings.Split(paneTail(content, choiceTailLines), "\n")

	footer := false
	lastEvidence := -1  // last line that looks like menu UI
	lastComposer := -1  // last composer input line
	lastAssistant := -1 // last assistant turn glyph
	selected := ""
	numbers := map[string]bool{}

	for i, raw := range lines {
		for _, marker := range choiceFooterMarkers {
			if strings.Contains(raw, marker) {
				footer = true
				lastEvidence = i
				break
			}
		}
		if strings.HasPrefix(strings.TrimSpace(raw), claudeAssistantLinePrefix) {
			lastAssistant = i
		}
		line := trimChoiceLine(raw)
		if line == "" {
			continue
		}
		if isComposerLine(line) {
			lastComposer = i
			continue
		}
		if m := selectedChoiceLine.FindStringSubmatch(line); m != nil {
			selected = m[1]
			numbers[m[1]] = true
			lastEvidence = i
			continue
		}
		if m := otherChoiceLine.FindStringSubmatch(line); m != nil {
			numbers[m[1]] = true
			lastEvidence = i
		}
	}

	if lastEvidence < 0 {
		return false
	}

	// A live modal REPLACES the composer and is the last thing drawn above the
	// status bar — nothing from the conversation renders below it. So when BOTH
	// an assistant turn and a composer line appear below the last menu line, the
	// menu is scrollback: another agent quoted it as text (a watchdog escalating
	// someone else's decision prompt) and then carried on.
	//
	// Both signals are required, not either. A menu whose "Type something."
	// option is active draws a free-text field that also looks like a composer,
	// so a composer line alone must not retire a live menu — a false positive
	// only makes an automated sender escalate to a human (recoverable), while a
	// false negative destroys the question (not).
	if lastComposer > lastEvidence && lastAssistant > lastEvidence {
		return false
	}

	if footer {
		return true
	}

	// Structural fallback for menus whose footer this version does not render:
	// a selected numbered option plus at least one other option with a
	// DIFFERENT number. One number alone is a list item; two are a choice.
	return selected != "" && len(numbers) >= 2
}

// trimChoiceLine strips the leading whitespace and box-drawing gutter Claude
// draws around a dialog, so "│ ❯ 1. Yes" reduces to "❯ 1. Yes".
func trimChoiceLine(line string) string {
	line = strings.TrimSpace(line)
	for {
		r := []rune(line)
		if len(r) == 0 {
			return ""
		}
		switch r[0] {
		case '│', '┃', '║', '|':
			line = strings.TrimSpace(string(r[1:]))
			continue
		}
		return line
	}
}

// paneTail returns the last n lines of content.
func paneTail(content string, n int) string {
	lines := strings.Split(content, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
