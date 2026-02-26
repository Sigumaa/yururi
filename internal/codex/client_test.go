package codex

import (
	"strings"
	"testing"
)

func TestNormalizeMethod(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  string
		expect string
	}{
		{
			name:   "snake_case",
			input:  "agent_message_delta",
			expect: "agent_message_delta",
		},
		{
			name:   "slash and camelCase",
			input:  "item/agentMessage/delta",
			expect: "item_agent_message_delta",
		},
		{
			name:   "slash",
			input:  "turn/completed",
			expect: "turn_completed",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := normalizeMethod(tc.input)
			if got != tc.expect {
				t.Fatalf("normalizeMethod(%q) = %q, want %q", tc.input, got, tc.expect)
			}
		})
	}
}

func TestIsAgentMessageDeltaMethod(t *testing.T) {
	t.Parallel()

	if !isAgentMessageDeltaMethod(normalizeMethod("agent_message_delta")) {
		t.Fatal("isAgentMessageDeltaMethod(agent_message_delta) = false, want true")
	}
	if !isAgentMessageDeltaMethod(normalizeMethod("item/agentMessage/delta")) {
		t.Fatal("isAgentMessageDeltaMethod(item/agentMessage/delta) = false, want true")
	}
	if isAgentMessageDeltaMethod(normalizeMethod("turn/completed")) {
		t.Fatal("isAgentMessageDeltaMethod(turn/completed) = true, want false")
	}
}

func TestThreadStartParams(t *testing.T) {
	t.Parallel()

	params := threadStartParams()
	if got := params["approvalPolicy"]; got != "never" {
		t.Fatalf("threadStartParams().approvalPolicy = %v, want never", got)
	}
	if got := params["sandbox"]; got != "workspace-write" {
		t.Fatalf("threadStartParams().sandbox = %v, want workspace-write", got)
	}
}

func TestWithCodexHomeEnv(t *testing.T) {
	t.Parallel()

	base := []string{
		"HOME=/tmp/original-home",
		"CODEX_HOME=/tmp/old-codex-home",
		"PATH=/usr/bin",
	}
	got := withCodexHomeEnv(base, "/tmp/new-codex-home")

	if countEntries(got, "CODEX_HOME=") != 1 {
		t.Fatalf("withCodexHomeEnv() CODEX_HOME entries = %d, want 1", countEntries(got, "CODEX_HOME="))
	}
	if !containsEnv(got, "CODEX_HOME=/tmp/new-codex-home") {
		t.Fatalf("withCodexHomeEnv() missing CODEX_HOME=/tmp/new-codex-home in %v", got)
	}
	if !containsEnv(got, "HOME=/tmp/original-home") {
		t.Fatalf("withCodexHomeEnv() changed HOME unexpectedly: %v", got)
	}
}

func containsEnv(env []string, entry string) bool {
	for _, v := range env {
		if v == entry {
			return true
		}
	}
	return false
}

func countEntries(env []string, prefix string) int {
	count := 0
	for _, v := range env {
		if strings.HasPrefix(v, prefix) {
			count++
		}
	}
	return count
}
