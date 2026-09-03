package send

import (
	"strings"
	"testing"
)

// The verification budget was a flat 50 retries — the same fifteen seconds for
// a one-line nudge and a 13k-character brief — so the largest payloads got the
// least slack at exactly the size where "large prompts silently fail to submit"
// was reported, through both --message-file and session send.
func TestVerifyBudgetGrowsWithPayloadAndStaysBounded(t *testing.T) {
	small := VerifyRetriesForPayload(50, "check the build")
	if small != 50 {
		t.Errorf("small payload budget = %d, want the base 50 unchanged", small)
	}

	// The 13,497-character brief from the retro.
	big := VerifyRetriesForPayload(50, strings.Repeat("x", 13497))
	if big <= 50 {
		t.Errorf("13k payload budget = %d, want more than the base 50", big)
	}

	huge := VerifyRetriesForPayload(50, strings.Repeat("x", 5_000_000))
	if huge > 50+maxExtraVerifyRetries {
		t.Errorf("5MB payload budget = %d, want it capped at %d — patience must stay bounded", huge, 50+maxExtraVerifyRetries)
	}
	if huge <= big {
		t.Errorf("budget did not grow between 13k (%d) and 5MB (%d)", big, huge)
	}
}

// A caller that disables verification keeps it disabled.
func TestZeroBudgetIsNotResurrectedByALargePayload(t *testing.T) {
	if got := VerifyRetriesForPayload(0, strings.Repeat("x", 20000)); got != 0 {
		t.Errorf("budget = %d, want 0 left alone", got)
	}
}

// 8000 is the observed-failure range, never a refusal threshold: the only
// measured hard limit is per-line and canonical-mode-only.
func TestLargePayloadIsAdvisoryNotARefusal(t *testing.T) {
	if IsLargePayload(strings.Repeat("x", LargePayloadBytes)) {
		t.Error("a payload exactly at the threshold is not yet large")
	}
	if !IsLargePayload(strings.Repeat("x", LargePayloadBytes+1)) {
		t.Error("a payload past the threshold should be flagged")
	}
	if hint := LargePayloadHint("short"); hint != "" {
		t.Errorf("small payload produced a hint: %q", hint)
	}
	hint := LargePayloadHint(strings.Repeat("x", 9000))
	if !strings.Contains(hint, "9000") {
		t.Errorf("hint %q does not name the size", hint)
	}
	if !strings.Contains(hint, "path") {
		t.Errorf("hint %q does not name the mitigation that worked (pass it by path)", hint)
	}
}

func TestShapeOfCountsBytesAndLines(t *testing.T) {
	if got := ShapeOf(""); got.Bytes != 0 || got.Lines != 0 {
		t.Errorf("ShapeOf(\"\") = %+v, want zero", got)
	}
	got := ShapeOf("a\nb\nc")
	if got.Bytes != 5 || got.Lines != 3 {
		t.Errorf("ShapeOf = %+v, want {5 3}", got)
	}
}
