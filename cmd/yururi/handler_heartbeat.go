package main

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/sigumaa/yururi/internal/codex"
	"github.com/sigumaa/yururi/internal/config"
	"github.com/sigumaa/yururi/internal/prompt"
)

func runHeartbeatTurn(ctx context.Context, cfg config.Config, runtime heartbeatRuntime, runID string) error {
	started := time.Now()
	log.Printf("event=heartbeat_tick run_id=%s", runID)

	instructions, err := prompt.LoadWorkspaceInstructions(cfg.Codex.WorkspaceDir)
	if err != nil {
		return err
	}
	bundle := prompt.BuildHeartbeatBundle(instructions)
	result, err := runtime.RunTurn(ctx, codex.TurnInput{
		BaseInstructions:      bundle.BaseInstructions,
		DeveloperInstructions: bundle.DeveloperInstructions,
		UserPrompt:            bundle.UserPrompt,
	})
	if err != nil {
		log.Printf("event=heartbeat_turn_failed run_id=%s turn_latency_ms=%d err=%v", runID, durationMS(time.Since(started)), err)
		return err
	}
	log.Printf("event=heartbeat_turn_completed run_id=%s status=%s thread=%s turn=%s tool_calls=%d turn_latency_ms=%d", runID, result.Status, result.ThreadID, result.TurnID, len(result.ToolCalls), durationMS(time.Since(started)))
	if assistantText := strings.TrimSpace(result.AssistantText); assistantText != "" {
		log.Printf("event=heartbeat_assistant_text run_id=%s thread=%s turn=%s text=%q", runID, result.ThreadID, result.TurnID, assistantText)
		logDecisionSummary("heartbeat", runID, result.ThreadID, result.TurnID, assistantText)
	}
	for i, toolCall := range result.ToolCalls {
		logTurnToolCall("heartbeat", runID, result.ThreadID, result.TurnID, i, toolCall)
	}
	if strings.TrimSpace(result.ErrorMessage) != "" {
		log.Printf("event=heartbeat_turn_error_detail run_id=%s err=%s", runID, result.ErrorMessage)
	}
	return nil
}
