package tmux

import "testing"

func TestParseLaunchAckMarker(t *testing.T) {
	tests := []struct {
		marker   string
		wantExit *int
		wantPID  int
		wantOK   bool
	}{
		{marker: "exit:0", wantExit: intPtr(0), wantOK: true},
		{marker: "exit:17", wantExit: intPtr(17), wantOK: true},
		{marker: "pid:42", wantPID: 42, wantOK: true},
		{marker: "exit:nope", wantOK: false},
		{marker: "pid:0", wantOK: false},
		{marker: "started", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.marker, func(t *testing.T) {
			gotExit, gotPID, gotOK := parseLaunchAckMarker(tt.marker)
			if gotOK != tt.wantOK || gotPID != tt.wantPID {
				t.Fatalf("parseLaunchAckMarker(%q) = (%v, %d, %v), want (%v, %d, %v)", tt.marker, gotExit, gotPID, gotOK, tt.wantExit, tt.wantPID, tt.wantOK)
			}
			if (gotExit == nil) != (tt.wantExit == nil) || gotExit != nil && *gotExit != *tt.wantExit {
				t.Fatalf("parseLaunchAckMarker(%q) exit = %v, want %v", tt.marker, gotExit, tt.wantExit)
			}
		})
	}
}

func intPtr(value int) *int { return &value }
