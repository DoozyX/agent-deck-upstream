package costs_test

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/costs"
)

func TestTokenUsageValidate(t *testing.T) {
	inclusiveInput := int64(12)
	tests := []struct {
		name    string
		usage   costs.TokenUsage
		wantErr string
	}{
		{name: "valid exclusive usage", usage: costs.TokenUsage{InputTokens: 2, CacheReadTokens: 7, CacheWriteTokens: 3, OutputTokens: 5, ReasoningTokens: 4, ProviderInputTokens: &inclusiveInput}},
		{name: "negative uncached input", usage: costs.TokenUsage{InputTokens: -1}, wantErr: "input_tokens"},
		{name: "negative output", usage: costs.TokenUsage{OutputTokens: -1}, wantErr: "output_tokens"},
		{name: "negative cache read", usage: costs.TokenUsage{CacheReadTokens: -1}, wantErr: "cache_read_tokens"},
		{name: "negative cache write", usage: costs.TokenUsage{CacheWriteTokens: -1}, wantErr: "cache_write_tokens"},
		{name: "negative cache write 5m", usage: costs.TokenUsage{CacheWrite5mTokens: -1}, wantErr: "cache_write_5m_tokens"},
		{name: "negative cache write 1h", usage: costs.TokenUsage{CacheWrite1hTokens: -1}, wantErr: "cache_write_1h_tokens"},
		{name: "negative reasoning", usage: costs.TokenUsage{ReasoningTokens: -1}, wantErr: "reasoning_tokens"},
		{name: "reasoning exceeds output", usage: costs.TokenUsage{OutputTokens: 2, ReasoningTokens: 3}, wantErr: "reasoning_tokens"},
		{name: "cache duration subsets exceed aggregate", usage: costs.TokenUsage{CacheWriteTokens: 10, CacheWrite5mTokens: 6, CacheWrite1hTokens: 5}, wantErr: "cache write duration subsets"},
		{name: "provider inclusive input below cache subsets", usage: costs.TokenUsage{CacheReadTokens: 7, CacheWriteTokens: 3, ProviderInputTokens: int64Ptr(9)}, wantErr: "provider_input_tokens"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.usage.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate error = %v, want field %q", err, tt.wantErr)
			}
		})
	}
}

func int64Ptr(value int64) *int64 { return &value }
