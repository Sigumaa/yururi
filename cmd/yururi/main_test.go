package main

import (
	"context"
	"testing"

	"github.com/sigumaa/yururi/internal/codex"
	"github.com/sigumaa/yururi/internal/config"
	"github.com/sigumaa/yururi/internal/memory"
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

func TestRunHeartbeatTurnUsesCoordinatorForChannelTasks(t *testing.T) {
	t.Parallel()

	workspaceDir := t.TempDir()
	if err := prompt.EnsureWorkspaceInstructionFiles(workspaceDir); err != nil {
		t.Fatalf("EnsureWorkspaceInstructionFiles() error = %v", err)
	}
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	_, err = store.UpsertTask(context.Background(), memory.UpsertTaskInput{
		TaskID:       "task-1",
		Title:        "task 1",
		Instructions: "do something",
		ChannelID:    "c1",
	})
	if err != nil {
		t.Fatalf("UpsertTask(task-1) error = %v", err)
	}
	_, err = store.UpsertTask(context.Background(), memory.UpsertTaskInput{
		TaskID:       "task-2",
		Title:        "task 2",
		Instructions: "do something else",
		ChannelID:    "c2",
	})
	if err != nil {
		t.Fatalf("UpsertTask(task-2) error = %v", err)
	}

	cfg := config.Config{
		Discord: config.DiscordConfig{GuildID: "g1"},
		Codex:   config.CodexConfig{WorkspaceDir: workspaceDir},
	}
	coordinator := &heartbeatCoordinatorStub{}
	runtime := &heartbeatFallbackRuntimeStub{}

	if err := runHeartbeatTurn(context.Background(), cfg, coordinator, runtime, store, "hb-1"); err != nil {
		t.Fatalf("runHeartbeatTurn() error = %v", err)
	}

	if got := len(coordinator.calls); got != 2 {
		t.Fatalf("coordinator calls = %d, want 2", got)
	}
	if coordinator.calls[0].ChannelKey != "g1:c1" {
		t.Fatalf("first channel key = %q, want g1:c1", coordinator.calls[0].ChannelKey)
	}
	if coordinator.calls[1].ChannelKey != "g1:c2" {
		t.Fatalf("second channel key = %q, want g1:c2", coordinator.calls[1].ChannelKey)
	}
	if got := len(runtime.calls); got != 0 {
		t.Fatalf("fallback runtime calls = %d, want 0", got)
	}
}

func TestRunHeartbeatTurnFallbackWhenNoTasks(t *testing.T) {
	t.Parallel()

	workspaceDir := t.TempDir()
	if err := prompt.EnsureWorkspaceInstructionFiles(workspaceDir); err != nil {
		t.Fatalf("EnsureWorkspaceInstructionFiles() error = %v", err)
	}
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	cfg := config.Config{
		Discord: config.DiscordConfig{GuildID: "g1"},
		Codex:   config.CodexConfig{WorkspaceDir: workspaceDir},
	}
	coordinator := &heartbeatCoordinatorStub{}
	runtime := &heartbeatFallbackRuntimeStub{}

	if err := runHeartbeatTurn(context.Background(), cfg, coordinator, runtime, store, "hb-2"); err != nil {
		t.Fatalf("runHeartbeatTurn() error = %v", err)
	}

	if got := len(coordinator.calls); got != 0 {
		t.Fatalf("coordinator calls = %d, want 0", got)
	}
	if got := len(runtime.calls); got != 1 {
		t.Fatalf("fallback runtime calls = %d, want 1", got)
	}
}

func TestRunHeartbeatTurnFallbackForUnboundTask(t *testing.T) {
	t.Parallel()

	workspaceDir := t.TempDir()
	if err := prompt.EnsureWorkspaceInstructionFiles(workspaceDir); err != nil {
		t.Fatalf("EnsureWorkspaceInstructionFiles() error = %v", err)
	}
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	_, err = store.UpsertTask(context.Background(), memory.UpsertTaskInput{
		TaskID:       "task-free",
		Title:        "task free",
		Instructions: "do generic check",
		ChannelID:    "",
	})
	if err != nil {
		t.Fatalf("UpsertTask(task-free) error = %v", err)
	}

	cfg := config.Config{
		Discord: config.DiscordConfig{GuildID: "g1"},
		Codex:   config.CodexConfig{WorkspaceDir: workspaceDir},
	}
	coordinator := &heartbeatCoordinatorStub{}
	runtime := &heartbeatFallbackRuntimeStub{}

	if err := runHeartbeatTurn(context.Background(), cfg, coordinator, runtime, store, "hb-3"); err != nil {
		t.Fatalf("runHeartbeatTurn() error = %v", err)
	}

	if got := len(coordinator.calls); got != 0 {
		t.Fatalf("coordinator calls = %d, want 0", got)
	}
	if got := len(runtime.calls); got != 1 {
		t.Fatalf("fallback runtime calls = %d, want 1", got)
	}
}

type heartbeatCoordinatorCall struct {
	ChannelKey string
	Input      codex.TurnInput
}

type heartbeatCoordinatorStub struct {
	calls []heartbeatCoordinatorCall
}

func (s *heartbeatCoordinatorStub) RunMessageTurn(_ context.Context, channelKey string, input codex.TurnInput) (codex.TurnResult, error) {
	s.calls = append(s.calls, heartbeatCoordinatorCall{
		ChannelKey: channelKey,
		Input:      input,
	})
	return codex.TurnResult{
		ThreadID: "thread-test",
		TurnID:   "turn-test",
		Status:   "completed",
	}, nil
}

type heartbeatFallbackRuntimeStub struct {
	calls []codex.TurnInput
}

func (s *heartbeatFallbackRuntimeStub) RunTurn(_ context.Context, input codex.TurnInput) (codex.TurnResult, error) {
	s.calls = append(s.calls, input)
	return codex.TurnResult{
		ThreadID: "thread-fallback",
		TurnID:   "turn-fallback",
		Status:   "completed",
	}, nil
}
