package main

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/sigumaa/yururi/internal/codex"
	"github.com/sigumaa/yururi/internal/config"
	"github.com/sigumaa/yururi/internal/prompt"
)

func TestCalculateHistoryLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		mergedCount int
		want        int
	}{
		{name: "default", mergedCount: 0, want: 30},
		{name: "single", mergedCount: 1, want: 30},
		{name: "small burst", mergedCount: 5, want: 30},
		{name: "middle burst", mergedCount: 40, want: 52},
		{name: "large burst cap", mergedCount: 300, want: 100},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := calculateHistoryLimit(tc.mergedCount)
			if got != tc.want {
				t.Fatalf("calculateHistoryLimit(%d) = %d, want %d", tc.mergedCount, got, tc.want)
			}
		})
	}
}

func TestRunHeartbeatTurnCallsRuntime(t *testing.T) {
	t.Parallel()

	workspaceDir := t.TempDir()
	if err := prompt.EnsureWorkspaceInstructionFiles(workspaceDir); err != nil {
		t.Fatalf("EnsureWorkspaceInstructionFiles() error = %v", err)
	}
	cfg := config.Config{
		Codex: config.CodexConfig{WorkspaceDir: workspaceDir},
	}
	runtime := &heartbeatRuntimeStub{}

	if err := runHeartbeatTurn(context.Background(), cfg, runtime, "hb-test"); err != nil {
		t.Fatalf("runHeartbeatTurn() error = %v", err)
	}
	if got := len(runtime.calls); got != 1 {
		t.Fatalf("runtime RunTurn calls = %d, want 1", got)
	}
	if !strings.Contains(runtime.calls[0].UserPrompt, prompt.HeartbeatSystemPrompt) {
		t.Fatalf("heartbeat prompt missing system prompt: %q", runtime.calls[0].UserPrompt)
	}
	if strings.Contains(strings.ToLower(runtime.calls[0].UserPrompt), "due tasks") {
		t.Fatalf("heartbeat prompt should not include due tasks section: %q", runtime.calls[0].UserPrompt)
	}
}

func TestTrimLogString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		text   string
		maxLen int
		want   string
	}{
		{name: "trim spaces", text: "  hello  ", maxLen: 10, want: "hello"},
		{name: "within limit", text: "hello", maxLen: 5, want: "hello"},
		{name: "over limit", text: "abcdef", maxLen: 5, want: "ab..."},
		{name: "tiny max", text: "abcdef", maxLen: 2, want: "ab"},
		{name: "non positive max", text: "abcdef", maxLen: 0, want: "abcdef"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := trimLogString(tc.text, tc.maxLen); got != tc.want {
				t.Fatalf("trimLogString(%q, %d) = %q, want %q", tc.text, tc.maxLen, got, tc.want)
			}
		})
	}
}

func TestTrimLogAny(t *testing.T) {
	t.Parallel()

	if got := trimLogAny(nil, 10); got != "" {
		t.Fatalf("trimLogAny(nil, 10) = %q, want empty string", got)
	}
	if got := trimLogAny("  hello  ", 10); got != "hello" {
		t.Fatalf("trimLogAny(string, 10) = %q, want %q", got, "hello")
	}
	if got := trimLogAny(map[string]string{"a": "b"}, 20); got != `{"a":"b"}` {
		t.Fatalf("trimLogAny(map, 20) = %q, want %q", got, `{"a":"b"}`)
	}
	if got := trimLogAny(math.NaN(), 20); got != "NaN" {
		t.Fatalf("trimLogAny(NaN, 20) = %q, want %q", got, "NaN")
	}
}

type heartbeatRuntimeStub struct {
	calls []codex.TurnInput
}

func (s *heartbeatRuntimeStub) RunTurn(_ context.Context, input codex.TurnInput) (codex.TurnResult, error) {
	s.calls = append(s.calls, input)
	return codex.TurnResult{
		ThreadID: "thread-test",
		TurnID:   "turn-test",
		Status:   "completed",
	}, nil
}
