package usage

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// The threshold comparisons the recommender depends on are INCLUSIVE at both
// ends of the legal range and strict between the two thresholds:
//
//   - 0 and 100 are both legal values (validateThreshold rejects only < 0 and
//     > 100), so a policy may put every provider in one state on purpose.
//   - exhausted_below == constrained_below is legal (the merge rejects only
//     exhausted_below > constrained_below); it makes "constrained" unreachable
//     rather than being a config error.
//
// These boundaries are pinned here because loosening either comparison to its
// non-strict form is otherwise invisible: it leaves ./internal/usage green.
// See TestRecommend_StateThresholdBoundaries for the matching runtime
// comparisons in Recommend.
func TestPolicyFromConfig_AcceptsThresholdBoundaries(t *testing.T) {
	tests := []struct {
		name                           string
		exhausted, constrained         *int
		wantExhausted, wantConstrained int
		guards                         string
	}{
		{
			name: "both at zero", exhausted: policyIntPtr(0), constrained: policyIntPtr(0),
			wantExhausted: 0, wantConstrained: 0,
			guards: "validateThreshold value < 0 -> <= 0",
		},
		{
			name: "both at one hundred", exhausted: policyIntPtr(100), constrained: policyIntPtr(100),
			wantExhausted: 100, wantConstrained: 100,
			guards: "validateThreshold value > 100 -> >= 100",
		},
		{
			name: "constrained_below alone at one hundred", constrained: policyIntPtr(100),
			wantExhausted: 15, wantConstrained: 100,
			guards: "validateThreshold value > 100 -> >= 100",
		},
		{
			name:      "equal thresholds in the middle of the range",
			exhausted: policyIntPtr(35), constrained: policyIntPtr(35),
			wantExhausted: 35, wantConstrained: 35,
			guards: "merge ExhaustedBelow > ConstrainedBelow -> >=",
		},
		{
			// Only exhausted_below is set and it equals the DEFAULT
			// constrained_below: the merge's own comparison, not the loader's.
			name:          "exhausted_below equal to the default constrained_below",
			exhausted:     policyIntPtr(35),
			wantExhausted: 35, wantConstrained: 35,
			guards: "merge ExhaustedBelow > ConstrainedBelow -> >=",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &session.UserConfig{DefaultTool: "claude"}
			cfg.Usage.Policy = session.UsagePolicySettings{
				ExhaustedBelow:   tc.exhausted,
				ConstrainedBelow: tc.constrained,
			}
			got, err := PolicyFromConfig(cfg)
			if err != nil {
				t.Fatalf("PolicyFromConfig() error = %v, want nil (guards: %s)", err, tc.guards)
			}
			if got.ExhaustedBelow != tc.wantExhausted {
				t.Errorf("ExhaustedBelow = %d, want %d", got.ExhaustedBelow, tc.wantExhausted)
			}
			if got.ConstrainedBelow != tc.wantConstrained {
				t.Errorf("ConstrainedBelow = %d, want %d", got.ConstrainedBelow, tc.wantConstrained)
			}
		})
	}
}

// The other side of the same boundary: one past each end is still rejected, so
// tightening a comparison the other way is caught too.
func TestPolicyFromConfig_RejectsThresholdsJustOutsideTheRange(t *testing.T) {
	tests := []struct {
		name                   string
		exhausted, constrained *int
		wantErr                string
	}{
		{
			name: "exhausted_below at 101", exhausted: policyIntPtr(101), constrained: policyIntPtr(101),
			wantErr: "invalid [usage.policy].exhausted_below 101: must be between 0 and 100",
		},
		{
			name: "constrained_below at 101", constrained: policyIntPtr(101),
			wantErr: "invalid [usage.policy].constrained_below 101: must be between 0 and 100",
		},
		{
			name: "exhausted_below at -1", exhausted: policyIntPtr(-1), constrained: policyIntPtr(0),
			wantErr: "invalid [usage.policy].exhausted_below -1: must be between 0 and 100",
		},
		{
			name: "one past equal is inverted", exhausted: policyIntPtr(36), constrained: policyIntPtr(35),
			wantErr: "invalid [usage.policy].exhausted_below 36: must be <= constrained_below 35",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &session.UserConfig{DefaultTool: "claude"}
			cfg.Usage.Policy = session.UsagePolicySettings{
				ExhaustedBelow:   tc.exhausted,
				ConstrainedBelow: tc.constrained,
			}
			_, err := PolicyFromConfig(cfg)
			if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("PolicyFromConfig() error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}
