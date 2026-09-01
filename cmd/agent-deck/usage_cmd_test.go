package main

import (
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/usage"
	"testing"
)

func TestUsageAccountForSessionUsesEffectiveProviderHome(t *testing.T) {
	inst := &session.Instance{Title: "Work", Tool: "claude"}
	account, err := usageAccountForSession(inst)
	if err != nil {
		t.Fatal(err)
	}
	if account.Provider != usage.Claude || account.Home == "" || account.Label != "Work" {
		t.Fatalf("account=%#v", account)
	}
}

func TestUsageAccountForSessionRejectsUnsupportedTools(t *testing.T) {
	if _, err := usageAccountForSession(&session.Instance{Tool: "gemini"}); err == nil {
		t.Fatal("expected unsupported-tool error")
	}
}
