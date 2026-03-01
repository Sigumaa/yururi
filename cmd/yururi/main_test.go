package main

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/sigumaa/yururi/internal/codex"
	"github.com/sigumaa/yururi/internal/config"
	"github.com/sigumaa/yururi/internal/discordx"
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

func TestBuildAutonomyPromptDoesNotIncludeHeartbeatPrompt(t *testing.T) {
	t.Parallel()

	got := buildAutonomyPrompt(nil)
	if !strings.Contains(got, prompt.AutonomySystemPrompt) {
		t.Fatalf("autonomy prompt missing autonomy system prompt: %q", got)
	}
	if !strings.Contains(got, "必要なら send_message") {
		t.Fatalf("autonomy prompt should mention generic tool usage: %q", got)
	}
	if strings.Contains(got, prompt.HeartbeatSystemPrompt) {
		t.Fatalf("autonomy prompt should not include heartbeat prompt: %q", got)
	}
	if strings.Contains(strings.ToLower(got), "heartbeat.md") {
		t.Fatalf("autonomy prompt should not mention heartbeat.md: %q", got)
	}
}

func TestBuildAutonomyPromptIncludesObservedChannels(t *testing.T) {
	t.Parallel()

	channels := []discordx.ChannelInfo{
		{ChannelID: "111", Name: "times-yururi"},
		{ChannelID: "222", Name: "times-web"},
	}
	got := buildAutonomyPrompt(channels)
	if !strings.Contains(got, "- times-yururi (111)") {
		t.Fatalf("autonomy prompt missing first channel: %q", got)
	}
	if !strings.Contains(got, "- times-web (222)") {
		t.Fatalf("autonomy prompt missing second channel: %q", got)
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
		{name: "multibyte safe trim", text: "これは長いテキストです", maxLen: 8, want: "これは長い..."},
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

func TestRunShutdownStepCompletes(t *testing.T) {
	t.Parallel()

	if timedOut := runShutdownStep("test_complete", 50*time.Millisecond, func() {}); timedOut {
		t.Fatal("runShutdownStep() timed out, want completed")
	}
}

func TestRunShutdownStepTimeout(t *testing.T) {
	t.Parallel()

	block := make(chan struct{})
	started := time.Now()
	timedOut := runShutdownStep("test_timeout", 20*time.Millisecond, func() {
		<-block
	})
	if !timedOut {
		close(block)
		t.Fatal("runShutdownStep() timedOut = false, want true")
	}
	if elapsed := time.Since(started); elapsed < 15*time.Millisecond {
		close(block)
		t.Fatalf("runShutdownStep() elapsed = %s, want >= 15ms", elapsed)
	}
	close(block)
}

func TestExpandObserveChannelIDsByCategory(t *testing.T) {
	t.Parallel()

	base := []string{"manual-1", " manual-2 ", "manual-1"}
	categories := []string{"cat-a", " cat-b "}
	channels := []*discordgo.Channel{
		{ID: "text-2", ParentID: "cat-b", Type: discordgo.ChannelTypeGuildText},
		{ID: "text-1", ParentID: "cat-a", Type: discordgo.ChannelTypeGuildText},
		{ID: "news-1", ParentID: "cat-a", Type: discordgo.ChannelTypeGuildNews},
		{ID: "thread-1", ParentID: "cat-a", Type: discordgo.ChannelTypeGuildPublicThread},
		{ID: "manual-1", ParentID: "cat-a", Type: discordgo.ChannelTypeGuildText},
		{ID: "text-3", ParentID: "cat-c", Type: discordgo.ChannelTypeGuildText},
	}

	got := expandObserveChannelIDsByCategory(base, categories, channels)
	want := []string{"manual-1", "manual-2", "text-1", "text-2"}
	if len(got) != len(want) {
		t.Fatalf("expandObserveChannelIDsByCategory() len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expandObserveChannelIDsByCategory()[%d] = %q, want %q (%v)", i, got[i], want[i], got)
		}
	}
}

func TestResolveObserveTextChannelsWithoutSession(t *testing.T) {
	t.Parallel()

	got, err := resolveObserveTextChannels(nil, config.DiscordConfig{
		GuildID:            "guild",
		ObserveChannelIDs:  []string{"observe-1", " observe-1 ", "observe-2"},
		ObserveCategoryIDs: []string{"cat-1"},
	})
	if err != nil {
		t.Fatalf("resolveObserveTextChannels() error = %v", err)
	}
	want := []string{"observe-1", "observe-2"}
	if len(got) != len(want) {
		t.Fatalf("resolveObserveTextChannels() len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("resolveObserveTextChannels()[%d] = %q, want %q (%v)", i, got[i], want[i], got)
		}
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
			name: "append memory doc does not reset",
			toolCalls: []codex.MCPToolCall{{
				Tool:      "append_workspace_doc",
				Arguments: map[string]any{"name": "  MEMORY.md  "},
			}},
			want: false,
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
			name: "json string arguments append does not reset",
			toolCalls: []codex.MCPToolCall{{
				Tool:      "append_workspace_doc",
				Arguments: `{"name":"memory.md"}`,
			}},
			want: false,
		},
		{
			name: "json string arguments replace resets",
			toolCalls: []codex.MCPToolCall{{
				Tool:      "replace_workspace_doc",
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
