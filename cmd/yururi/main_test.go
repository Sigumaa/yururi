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

	if err := runHeartbeatTurn(context.Background(), cfg, runtime, nil, nil, "hb-test"); err != nil {
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

func TestRunHeartbeatTurnPostsTimesWhisperWhenWorkExists(t *testing.T) {
	t.Parallel()

	workspaceDir := t.TempDir()
	if err := prompt.EnsureWorkspaceInstructionFiles(workspaceDir); err != nil {
		t.Fatalf("EnsureWorkspaceInstructionFiles() error = %v", err)
	}
	cfg := config.Config{
		Codex: config.CodexConfig{WorkspaceDir: workspaceDir},
		Persona: config.PersonaConfig{
			TimesChannelID: "times",
		},
	}
	runtime := &heartbeatRuntimeStub{
		result: codex.TurnResult{
			Status: "completed",
			ToolCalls: []codex.MCPToolCall{
				{Tool: "read_workspace_doc", Status: "completed"},
			},
		},
	}
	sender := &heartbeatWhisperSenderStub{}

	if err := runHeartbeatTurn(context.Background(), cfg, runtime, sender, &timesWhisperState{}, "hb-whisper"); err != nil {
		t.Fatalf("runHeartbeatTurn() error = %v", err)
	}
	if len(sender.messages) != 1 {
		t.Fatalf("times whisper count = %d, want 1", len(sender.messages))
	}
	if sender.messages[0].channelID != "times" {
		t.Fatalf("times whisper channel = %q, want times", sender.messages[0].channelID)
	}
	if !strings.Contains(sender.messages[0].content, "定期チェック") {
		t.Fatalf("times whisper content missing summary: %q", sender.messages[0].content)
	}
}

func TestRunHeartbeatTurnSuppressesTimesWhisperForNoop(t *testing.T) {
	t.Parallel()

	workspaceDir := t.TempDir()
	if err := prompt.EnsureWorkspaceInstructionFiles(workspaceDir); err != nil {
		t.Fatalf("EnsureWorkspaceInstructionFiles() error = %v", err)
	}
	cfg := config.Config{
		Codex: config.CodexConfig{WorkspaceDir: workspaceDir},
		Persona: config.PersonaConfig{
			TimesChannelID: "times",
		},
	}
	runtime := &heartbeatRuntimeStub{
		result: codex.TurnResult{
			Status:        "completed",
			AssistantText: `{"action":"noop"}`,
		},
	}
	sender := &heartbeatWhisperSenderStub{}

	if err := runHeartbeatTurn(context.Background(), cfg, runtime, sender, &timesWhisperState{}, "hb-noop"); err != nil {
		t.Fatalf("runHeartbeatTurn() error = %v", err)
	}
	if len(sender.messages) != 0 {
		t.Fatalf("times whisper count = %d, want 0", len(sender.messages))
	}
}

func TestRunHeartbeatTurnTimesWhisperRespectsMinInterval(t *testing.T) {
	t.Parallel()

	workspaceDir := t.TempDir()
	if err := prompt.EnsureWorkspaceInstructionFiles(workspaceDir); err != nil {
		t.Fatalf("EnsureWorkspaceInstructionFiles() error = %v", err)
	}
	cfg := config.Config{
		Codex: config.CodexConfig{WorkspaceDir: workspaceDir},
		Persona: config.PersonaConfig{
			TimesChannelID:    "times",
			TimesMinIntervalS: 300,
		},
	}
	runtime := &heartbeatRuntimeStub{
		result: codex.TurnResult{
			Status: "completed",
			ToolCalls: []codex.MCPToolCall{
				{Tool: "read_workspace_doc", Status: "completed"},
			},
		},
	}
	sender := &heartbeatWhisperSenderStub{}
	state := &timesWhisperState{}

	if err := runHeartbeatTurn(context.Background(), cfg, runtime, sender, state, "hb-1"); err != nil {
		t.Fatalf("runHeartbeatTurn() first error = %v", err)
	}
	if err := runHeartbeatTurn(context.Background(), cfg, runtime, sender, state, "hb-2"); err != nil {
		t.Fatalf("runHeartbeatTurn() second error = %v", err)
	}
	if len(sender.messages) != 1 {
		t.Fatalf("times whisper count = %d, want 1", len(sender.messages))
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

func TestShouldResetSessionAfterMemoryUpdate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		toolCalls []codex.MCPToolCall
		want      bool
	}{
		{
			name: "append memory doc",
			toolCalls: []codex.MCPToolCall{{
				Tool:      "append_workspace_doc",
				Arguments: map[string]any{"name": "  MEMORY.md  "},
			}},
			want: true,
		},
		{
			name: "replace memory doc with case difference",
			toolCalls: []codex.MCPToolCall{{
				Tool:      "replace_workspace_doc",
				Arguments: map[string]any{"name": " memory.MD "},
			}},
			want: true,
		},
		{
			name: "name mismatch",
			toolCalls: []codex.MCPToolCall{{
				Tool:      "append_workspace_doc",
				Arguments: map[string]any{"name": "SOUL.md"},
			}},
			want: false,
		},
		{
			name: "tool mismatch",
			toolCalls: []codex.MCPToolCall{{
				Tool:      "read_workspace_doc",
				Arguments: map[string]any{"name": "MEMORY.md"},
			}},
			want: false,
		},
		{
			name: "json string arguments",
			toolCalls: []codex.MCPToolCall{{
				Tool:      "append_workspace_doc",
				Arguments: `{"name":"memory.md"}`,
			}},
			want: true,
		},
		{
			name: "missing arguments name",
			toolCalls: []codex.MCPToolCall{{
				Tool:      "append_workspace_doc",
				Arguments: map[string]any{"content": "updated"},
			}},
			want: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := shouldResetSessionAfterMemoryUpdate(tc.toolCalls); got != tc.want {
				t.Fatalf("shouldResetSessionAfterMemoryUpdate() = %v, want %v", got, tc.want)
			}
		})
	}
}

type heartbeatRuntimeStub struct {
	calls  []codex.TurnInput
	result codex.TurnResult
	err    error
}

func (s *heartbeatRuntimeStub) RunTurn(_ context.Context, input codex.TurnInput) (codex.TurnResult, error) {
	s.calls = append(s.calls, input)
	if s.err != nil {
		return codex.TurnResult{}, s.err
	}
	if strings.TrimSpace(s.result.Status) == "" && strings.TrimSpace(s.result.ThreadID) == "" && strings.TrimSpace(s.result.TurnID) == "" && len(s.result.ToolCalls) == 0 && strings.TrimSpace(s.result.AssistantText) == "" && strings.TrimSpace(s.result.ErrorMessage) == "" {
		return codex.TurnResult{
			ThreadID: "thread-test",
			TurnID:   "turn-test",
			Status:   "completed",
		}, nil
	}
	return s.result, nil
}

type heartbeatWhisperSenderStub struct {
	messages []whisperMessage
}

type whisperMessage struct {
	channelID string
	content   string
}

func (s *heartbeatWhisperSenderStub) SendMessage(_ context.Context, channelID string, content string) (string, error) {
	s.messages = append(s.messages, whisperMessage{
		channelID: channelID,
		content:   content,
	})
	return "m1", nil
}
