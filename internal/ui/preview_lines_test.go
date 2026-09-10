package ui

import (
	"reflect"
	"testing"
)

func TestPreviewLineWindowKeepsOnlyVisibleLines(t *testing.T) {
	tests := []struct {
		name          string
		content       string
		maxLines      int
		offset        int
		totalLines    int
		wantLines     []string
		wantTruncated bool
		wantAbove     int
		wantBelow     int
		wantOffset    int
	}{
		{
			name:          "tail",
			content:       "a\nb\nc\nd\n",
			maxLines:      3,
			totalLines:    4,
			wantLines:     []string{"b", "c", "d"},
			wantTruncated: true,
			wantAbove:     1,
		},
		{
			name:          "scrolled up",
			content:       "a\nb\nc\nd\n",
			maxLines:      3,
			offset:        1,
			totalLines:    4,
			wantLines:     []string{"a", "b", "c"},
			wantTruncated: true,
			wantAbove:     0,
			wantBelow:     1,
			wantOffset:    1,
		},
		{
			name:      "trailing whitespace is discarded",
			content:   "a\n \n\n",
			maxLines:  2,
			wantLines: []string{"a"},
		},
		{
			name:      "fits",
			content:   "a\nb\n",
			maxLines:  3,
			wantLines: []string{"a", "b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := previewLineWindow(tt.content, tt.maxLines, tt.offset, tt.totalLines)
			if !reflect.DeepEqual(got.lines, tt.wantLines) {
				t.Fatalf("lines = %#v, want %#v", got.lines, tt.wantLines)
			}
			if got.truncatedFromTop != tt.wantTruncated || got.truncatedCount != tt.wantAbove || got.scrolledBelow != tt.wantBelow || got.offset != tt.wantOffset {
				t.Fatalf("window metadata = (truncated=%t above=%d below=%d offset=%d), want (%t, %d, %d, %d)", got.truncatedFromTop, got.truncatedCount, got.scrolledBelow, got.offset, tt.wantTruncated, tt.wantAbove, tt.wantBelow, tt.wantOffset)
			}
		})
	}
}
