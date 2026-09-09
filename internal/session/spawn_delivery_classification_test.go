package session

import (
	"errors"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/send"
)

// The spawn-time send verification loop used to `return nil` when its retry
// budget ran out, so `agent-deck launch --message-file brief.md` printed
// "(message sent)" and `"success": true` over a brief the agent never
// received. These pin the four exhaustion outcomes: every one of them is a
// failure, and each names a different remedy.
func TestExhaustedSpawnSendIsNeverReportedAsSuccess(t *testing.T) {
	cases := []struct {
		name                      string
		everCaptured              bool
		composerHoldingAtLastLook bool
		sawBodyInPane             bool
		wantDelivery              string
	}{
		{
			name:         "no pane capture ever succeeded is no evidence, not success",
			everCaptured: false,
			wantDelivery: send.DeliveryNoEvidence,
		},
		{
			name:                      "composer still holding the body is typed_not_submitted",
			everCaptured:              true,
			composerHoldingAtLastLook: true,
			sawBodyInPane:             true,
			wantDelivery:              send.DeliveryTypedNotSubmitted,
		},
		{
			name:          "body seen but gone by the last look is typed",
			everCaptured:  true,
			sawBodyInPane: true,
			wantDelivery:  send.DeliveryTyped,
		},
		{
			name:         "body never observed at all is no evidence",
			everCaptured: true,
			wantDelivery: send.DeliveryNoEvidence,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			delivery, err := classifyExhaustedSpawnSend(tc.everCaptured, tc.composerHoldingAtLastLook, tc.sawBodyInPane)
			if delivery != tc.wantDelivery {
				t.Errorf("delivery = %q, want %q", delivery, tc.wantDelivery)
			}
			if err == nil {
				t.Fatal("exhausting the verify budget returned a nil error: this is the exact overclaim that let a lost brief be reported as a successful launch")
			}
			if send.DeliveryMeansSubmitted(delivery) {
				t.Errorf("delivery %q reports as submitted after the budget was exhausted", delivery)
			}
			var de *send.DeliveryError
			if !errors.As(err, &de) {
				t.Fatalf("error does not carry a delivery classification: %v", err)
			}
			if de.Delivery != tc.wantDelivery {
				t.Errorf("DeliveryError.Delivery = %q, want %q", de.Delivery, tc.wantDelivery)
			}
			if send.DeliveryOf(err) != tc.wantDelivery {
				t.Errorf("DeliveryOf(err) = %q, want %q", send.DeliveryOf(err), tc.wantDelivery)
			}
		})
	}
}
