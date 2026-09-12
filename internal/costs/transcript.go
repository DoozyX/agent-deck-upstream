package costs

import (
	"context"
	"time"
)

// TranscriptSource identifies one canonical provider transcript without tying
// its identity to an Agent Deck title or instance lifecycle.
type TranscriptSource struct {
	Provider        string
	Kind            string
	Identity        string
	Path            string
	Account         string
	SessionID       string
	ParentSessionID string
	RunID           string
}

// ScanCheckpoint is committed only for a complete scan. Offset points just
// after the last complete provider record, never into a partial trailing line.
type ScanCheckpoint struct {
	Provider       string
	SourceKind     string
	SourceIdentity string
	Offset         int64
	Fingerprint    string
	UpdatedAt      time.Time
	Complete       bool
}

type ParseResult struct {
	Events            []UsageEvent
	Checkpoint        ScanCheckpoint
	Complete          bool
	Warnings          []string
	BlockedStatus     string
	BlockedUntil      time.Time
	BlockedResetKnown bool
	BlockedBackoff    time.Duration
}

type TranscriptParser interface {
	Parse(context.Context, TranscriptSource, ScanCheckpoint) (ParseResult, error)
}
