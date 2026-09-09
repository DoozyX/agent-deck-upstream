package send

import (
	"strconv"
	"strings"
)

// LargePayloadBytes is the size above which a message is treated as a large
// payload: given a longer verification budget, and named in a delivery failure
// as a likely contributing cause.
//
// 8000 is not a measured cliff and is deliberately not used as a refusal
// threshold. It is the point at which large-prompt submission failures were
// actually observed in practice — a 13,497-character brief failed outright, a
// later ~9,000-character body silently failed to submit twice — and the point
// the orchestrate prompt renderer already warns at. The measured hard limit
// that DOES justify refusing is per-line and canonical-mode-only; see
// internal/tmux/canonical_line.go, which establishes that raw-mode panes (every
// TUI agent) took 8000 bytes intact. So the honest handling of a large body is
// more patience and a better failure message, not a made-up ceiling.
const LargePayloadBytes = 8000

// IsLargePayload reports whether a message is in the size range where
// submission failures have been observed.
func IsLargePayload(message string) bool {
	return len(message) > LargePayloadBytes
}

// PayloadShape describes a message for reporting. Both numbers go into the
// --json payload of every send: when a large prompt fails to submit, the first
// question is always how large it was, and that was previously unanswerable
// from the tool's own output.
type PayloadShape struct {
	Bytes int `json:"message_bytes"`
	Lines int `json:"message_lines"`
}

// ShapeOf measures a message.
func ShapeOf(message string) PayloadShape {
	if message == "" {
		return PayloadShape{}
	}
	return PayloadShape{Bytes: len(message), Lines: strings.Count(message, "\n") + 1}
}

// VerifyRetriesForPayload scales a verification retry budget with payload size.
//
// The budget was a flat 50 retries at 300ms — fifteen seconds — regardless of
// whether the message was a one-line nudge or a 13k-character brief. A bulk
// paste of that size takes Claude visibly longer to ingest, collapse behind its
// "[Pasted text …]" marker and begin a turn on, so the fixed budget gives a
// large body materially less slack than a small one at exactly the size where
// submission failures were reported. Time is the cheap resource here: waiting
// longer on a big paste costs seconds, while calling an undelivered brief
// delivered costs a run.
//
// Growth is linear above the large-payload threshold and capped, so the worst
// case stays bounded (base + maxExtraVerifyRetries, ~45s at the default
// cadence) rather than scaling with an arbitrarily large body.
func VerifyRetriesForPayload(base int, message string) int {
	if base <= 0 || !IsLargePayload(message) {
		return base
	}
	extra := (len(message) - LargePayloadBytes) / bytesPerExtraVerifyRetry
	if extra > maxExtraVerifyRetries {
		extra = maxExtraVerifyRetries
	}
	return base + extra
}

const (
	// bytesPerExtraVerifyRetry buys one more look per 500 bytes over the
	// threshold: a 13k brief gets ten extra looks, three more seconds.
	bytesPerExtraVerifyRetry = 500
	// maxExtraVerifyRetries caps the growth at 100 (30s at the 300ms
	// cadence), so a pathologically large body cannot make a send hang.
	maxExtraVerifyRetries = 100
)

// LargePayloadHint is the advice appended to a delivery failure for a large
// body. It names the mitigation that was found to work rather than restating
// the failure: pass the material by path and let the agent read it, so the
// prompt itself stays small.
func LargePayloadHint(message string) string {
	if !IsLargePayload(message) {
		return ""
	}
	return "the message is " + strconv.Itoa(len(message)) + " bytes; large bodies have been observed failing to submit — " +
		"write it to a file and send a short prompt pointing at that path instead of inlining it"
}
