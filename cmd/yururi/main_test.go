package main

import (
	"context"
	"errors"
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
			Status:        "completed",
			AssistantText: "THOUGHT_NOTE_123",
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
	if !strings.Contains(sender.messages[0].content, "THOUGHT_NOTE_123") {
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
			Status:        "completed",
			AssistantText: "定期チェック完了。今回は様子見します。",
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

func TestPostHeartbeatWhisperSuppressesDuplicateRecent(t *testing.T) {
	t.Parallel()

	cfg := config.Config{
		Persona: config.PersonaConfig{
			TimesChannelID: "times",
		},
	}
	sender := &heartbeatWhisperSenderStub{
		history: []discordx.Message{
			{Content: "同じ   内容"},
		},
	}
	result := codex.TurnResult{
		AssistantText: "同じ 内容",
	}

	if err := postHeartbeatWhisper(context.Background(), cfg, sender, &timesWhisperState{}, "hb-dup", result); err != nil {
		t.Fatalf("postHeartbeatWhisper() error = %v", err)
	}
	if len(sender.messages) != 0 {
		t.Fatalf("times whisper count = %d, want 0", len(sender.messages))
	}
	if sender.readHistoryCalls != 1 {
		t.Fatalf("read history calls = %d, want 1", sender.readHistoryCalls)
	}
}

func TestPostMessageWhisperSuppressesDuplicateRecent(t *testing.T) {
	t.Parallel()

	cfg := config.Config{
		Persona: config.PersonaConfig{
			TimesChannelID: "times",
		},
	}
	sender := &heartbeatWhisperSenderStub{
		history: []discordx.Message{
			{Content: "dupe message"},
		},
	}
	result := codex.TurnResult{
		AssistantText: "dupe message",
	}

	if err := postMessageWhisper(context.Background(), cfg, sender, &timesWhisperState{}, "msg-dup", nil, result); err != nil {
		t.Fatalf("postMessageWhisper() error = %v", err)
	}
	if len(sender.messages) != 0 {
		t.Fatalf("times whisper count = %d, want 0", len(sender.messages))
	}
}

func TestPostHeartbeatWhisperStillPostsWhenHistoryReadFails(t *testing.T) {
	t.Parallel()

	cfg := config.Config{
		Persona: config.PersonaConfig{
			TimesChannelID: "times",
		},
	}
	sender := &heartbeatWhisperSenderStub{
		historyErr: errors.New("history failed"),
	}
	result := codex.TurnResult{
		AssistantText: "history failure fallback",
	}

	if err := postHeartbeatWhisper(context.Background(), cfg, sender, &timesWhisperState{}, "hb-history-err", result); err != nil {
		t.Fatalf("postHeartbeatWhisper() error = %v", err)
	}
	if len(sender.messages) != 1 {
		t.Fatalf("times whisper count = %d, want 1", len(sender.messages))
	}
}

func TestBuildAutonomyPromptDoesNotIncludeHeartbeatPrompt(t *testing.T) {
	t.Parallel()

	got := buildAutonomyPrompt(nil, "", nil)
	if !strings.Contains(got, prompt.AutonomySystemPrompt) {
		t.Fatalf("autonomy prompt missing autonomy system prompt: %q", got)
	}
	if !strings.Contains(got, "独り言") {
		t.Fatalf("autonomy prompt should mention monologue style for times: %q", got)
	}
	if strings.Contains(got, prompt.HeartbeatSystemPrompt) {
		t.Fatalf("autonomy prompt should not include heartbeat prompt: %q", got)
	}
	if strings.Contains(strings.ToLower(got), "heartbeat.md") {
		t.Fatalf("autonomy prompt should not mention heartbeat.md: %q", got)
	}
}

func TestBuildAutonomyPromptIncludesObservedChannelsAndTimes(t *testing.T) {
	t.Parallel()

	channels := []discordx.ChannelInfo{
		{ChannelID: "111", Name: "times-yururi"},
		{ChannelID: "222", Name: "times-web"},
	}
	got := buildAutonomyPrompt(channels, "999", nil)
	if !strings.Contains(got, "times_channel_id=999") {
		t.Fatalf("autonomy prompt missing times channel id: %q", got)
	}
	if !strings.Contains(got, "- times-yururi (111)") {
		t.Fatalf("autonomy prompt missing first channel: %q", got)
	}
	if !strings.Contains(got, "- times-web (222)") {
		t.Fatalf("autonomy prompt missing second channel: %q", got)
	}
}

func TestBuildAutonomyPromptIncludesTimesRecentReference(t *testing.T) {
	t.Parallel()

	got := buildAutonomyPrompt(nil, "times", []string{"first", "second"})
	if !strings.Contains(got, "times直近投稿参照") {
		t.Fatalf("autonomy prompt missing times history section: %q", got)
	}
	if !strings.Contains(got, "毎回参照や引用をする必要はありません") {
		t.Fatalf("autonomy prompt missing optional X reference guidance: %q", got)
	}
	if !strings.Contains(got, "- first") || !strings.Contains(got, "- second") {
		t.Fatalf("autonomy prompt missing times history entries: %q", got)
	}
	if !strings.Contains(got, "真似しない") {
		t.Fatalf("autonomy prompt missing anti-anchor note: %q", got)
	}
}

func TestExtractTimesPromptHistory(t *testing.T) {
	t.Parallel()

	messages := []discordx.Message{
		{Content: " newest message "},
		{Content: "   "},
		{Content: "oldest message"},
	}
	got := extractTimesPromptHistory(messages, 8)
	want := []string{"oldes...", "newes..."}
	if len(got) != len(want) {
		t.Fatalf("extractTimesPromptHistory len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("extractTimesPromptHistory[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestBuildMessageWhisperMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		result  codex.TurnResult
		wantOK  bool
		wantHas string
	}{
		{
			name: "skip when delivery exists",
			result: codex.TurnResult{
				ToolCalls:     []codex.MCPToolCall{{Tool: "reply_message"}},
				AssistantText: "anything",
			},
			wantOK: false,
		},
		{
			name: "post plain assistant text",
			result: codex.TurnResult{
				AssistantText: "この話題は返信不要だけど自分は賛成です",
			},
			wantOK:  true,
			wantHas: "この話題は返信不要だけど自分は賛成です",
		},
		{
			name: "keep assistant text even when tools exist",
			result: codex.TurnResult{
				AssistantText: "👀でリアクションしておいたよ。\nあわせて運用メモを更新した。",
				ToolCalls: []codex.MCPToolCall{
					{Tool: "add_reaction", Status: "completed"},
					{Tool: "append_workspace_doc", Status: "completed"},
				},
			},
			wantOK:  true,
			wantHas: "👀でリアクションしておいたよ",
		},
		{
			name: "skip noop decision",
			result: codex.TurnResult{
				AssistantText: `{"action":"noop"}`,
			},
			wantOK: false,
		},
		{
			name: "post error",
			result: codex.TurnResult{
				ErrorMessage: "network error",
			},
			wantOK: false,
		},
		{
			name: "post operational report as is",
			result: codex.TurnResult{
				AssistantText: "completed. executed.",
			},
			wantOK:  true,
			wantHas: "completed. executed.",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			content, ok := buildMessageWhisperMessage(tc.result)
			if ok != tc.wantOK {
				t.Fatalf("buildMessageWhisperMessage() ok = %v, want %v", ok, tc.wantOK)
			}
			if tc.wantHas != "" && !strings.Contains(content, tc.wantHas) {
				t.Fatalf("content = %q, want contains %q", content, tc.wantHas)
			}
		})
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

func TestSelectPersonaWhisperText(t *testing.T) {
	t.Parallel()

	if got, ok := selectPersonaWhisperText("  "); ok || got != "" {
		t.Fatalf("selectPersonaWhisperText(empty) = (%q, %v), want empty/false", got, ok)
	}
	if got, ok := selectPersonaWhisperText("completed. executed."); !ok || got != "completed. executed." {
		t.Fatalf("selectPersonaWhisperText(operational) = (%q, %v), want pass-through", got, ok)
	}
	if got, ok := selectPersonaWhisperText("line1\nline2"); !ok || got != "line1\nline2" {
		t.Fatalf("selectPersonaWhisperText(multiline) = (%q, %v), want pass-through", got, ok)
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
	messages         []whisperMessage
	history          []discordx.Message
	historyErr       error
	readHistoryCalls int
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

func (s *heartbeatWhisperSenderStub) ReadMessageHistory(_ context.Context, _ string, _ string, _ int) ([]discordx.Message, error) {
	s.readHistoryCalls++
	if s.historyErr != nil {
		return nil, s.historyErr
	}
	out := make([]discordx.Message, len(s.history))
	copy(out, s.history)
	return out, nil
}
