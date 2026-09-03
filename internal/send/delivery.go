package send

import "errors"

// Delivery statuses shared by every path that types a message into an agent
// pane. They were introduced for `session send` (issue #1413) and lived as
// unexported constants in cmd/agent-deck; they are defined here so the
// spawn-time delivery path in internal/session can classify its own outcome
// in the same vocabulary instead of inventing a second one that drifts.
//
// The retro that forced this move: `agent-deck launch --message-file brief.md`
// printed "(message sent)" and `"success": true` while the child sat at
// ctx=0 with an empty composer — the brief was never delivered. `session send`
// had reported that case honestly since #1413; launch never adopted the
// contract, so the same failure was re-discovered across four separate runs.
// One vocabulary, one source of truth, both paths.
const (
	// DeliverySubmitted: positive evidence the agent accepted the message
	// (an "active" transition, or the composer cleared after holding it).
	// This is the ONLY status that means work started.
	DeliverySubmitted = "submitted"
	// DeliveryUnverified: the message was sent but no signal could reach a
	// verdict either way, so submission is genuinely unknown.
	DeliveryUnverified = "unverified"
	// DeliveryTyped: the body was observed reaching the pane, but nothing
	// proved the agent accepted it as a turn.
	DeliveryTyped = "typed"
	// DeliveryLineTooLong: refused before typing anything because a payload
	// line exceeds the pane's canonical-mode line buffer (issue #1793).
	DeliveryLineTooLong = "line_too_long"
	// DeliveryTypedNotSubmitted: the body is still sitting unsent in the
	// composer after the bounded Enter-retry budget (issue #1413).
	DeliveryTypedNotSubmitted = "typed_not_submitted"
	// DeliveryNoEvidence: no positive delivery signal was ever observed
	// (issue #876 silent-drop classification).
	DeliveryNoEvidence = "no_evidence"
	// DeliverySendFailed: the initial tmux send-keys itself failed.
	DeliverySendFailed = "send_failed"
)

// DeliveryMeansSubmitted reports whether a delivery status is positive
// evidence that the agent took the message up as a turn. Callers must never
// re-derive this: "typed" in particular means the bytes arrived and nothing
// confirmed the agent accepted them, which is exactly the case that has been
// mistaken for success.
func DeliveryMeansSubmitted(delivery string) bool {
	return delivery == DeliverySubmitted
}

// DeliveryError carries a delivery classification alongside the failure, so a
// caller that only sees an `error` can still report WHICH way the send failed
// instead of collapsing every outcome into "failed to send".
type DeliveryError struct {
	// Delivery is one of the Delivery* constants above.
	Delivery string
	// Err is the underlying cause, if any.
	Err error
	// Detail is a human-readable explanation used when Err is nil (the
	// verification loop exhausting its budget is a failure with no
	// underlying error to wrap).
	Detail string
}

func (e *DeliveryError) Error() string {
	switch {
	case e.Err != nil && e.Detail != "":
		return e.Detail + ": " + e.Err.Error()
	case e.Err != nil:
		return e.Err.Error()
	case e.Detail != "":
		return e.Detail
	default:
		return "message delivery failed (" + e.Delivery + ")"
	}
}

func (e *DeliveryError) Unwrap() error { return e.Err }

// DeliveryOf extracts the delivery classification from err. An error that
// carries no classification is reported as DeliverySendFailed, and a nil
// error as DeliverySubmitted — the two ends of the contract.
func DeliveryOf(err error) string {
	if err == nil {
		return DeliverySubmitted
	}
	var de *DeliveryError
	if errors.As(err, &de) {
		return de.Delivery
	}
	return DeliverySendFailed
}
