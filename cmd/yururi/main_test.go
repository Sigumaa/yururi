package main

import (
	"context"
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
	if !strings.Contains(runtime.calls[0].UserPrompt, "## due tasks") {
		t.Fatalf("heartbeat prompt missing due tasks section: %q", runtime.calls[0].UserPrompt)
	}
}

func TestShouldRecoverDiscordDelivery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  codex.TurnResult
		expect bool
	}{
		{
			name:   "no assistant text",
			input:  codex.TurnResult{AssistantText: "", ToolCalls: nil},
			expect: false,
		},
		{
			name:   "assistant text without action tool",
			input:  codex.TurnResult{AssistantText: "こんにちは", ToolCalls: nil},
			expect: true,
		},
		{
			name: "assistant text with reply tool",
			input: codex.TurnResult{
				AssistantText: "こんにちは",
				ToolCalls: []codex.MCPToolCall{
					{Tool: "reply_message", Status: "completed"},
				},
			},
			expect: false,
		},
		{
			name:   "assistant text with error detail",
			input:  codex.TurnResult{AssistantText: "こんにちは", ErrorMessage: "failed"},
			expect: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := shouldRecoverDiscordDelivery(tc.input)
			if got != tc.expect {
				t.Fatalf("shouldRecoverDiscordDelivery() = %v, want %v", got, tc.expect)
			}
		})
	}
}

func TestBuildDeliveryRecoveryPrompt(t *testing.T) {
	t.Parallel()

	got := buildDeliveryRecoveryPrompt("c1", "m1", "返信案")
	if !strings.Contains(got, "対象チャンネルID: c1") {
		t.Fatalf("recovery prompt missing channel id: %q", got)
	}
	if !strings.Contains(got, "対象メッセージID: m1") {
		t.Fatalf("recovery prompt missing message id: %q", got)
	}
	if !strings.Contains(got, "reply_message") || !strings.Contains(got, "send_message") {
		t.Fatalf("recovery prompt missing delivery tool instruction: %q", got)
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
