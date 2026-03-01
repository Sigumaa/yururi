package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/sigumaa/yururi/internal/codex"
	"github.com/sigumaa/yururi/internal/config"
	"github.com/sigumaa/yururi/internal/discordx"
	"github.com/sigumaa/yururi/internal/prompt"
)

func runAutonomyTurn(ctx context.Context, cfg config.Config, runtime heartbeatRuntime, gateway *discordx.Gateway, runID string) error {
	started := time.Now()
	log.Printf("event=autonomy_tick run_id=%s", runID)

	instructions, err := prompt.LoadWorkspaceInstructions(cfg.Codex.WorkspaceDir)
	if err != nil {
		return err
	}
	channels, err := gateway.ListChannels(ctx)
	if err != nil {
		log.Printf("event=autonomy_list_channels_failed run_id=%s err=%v", runID, err)
	}
	userPrompt := buildAutonomyPrompt(channels)
	bundle := prompt.BuildHeartbeatBundle(instructions)
	result, err := runtime.RunTurn(ctx, codex.TurnInput{
		BaseInstructions:      bundle.BaseInstructions,
		DeveloperInstructions: bundle.DeveloperInstructions,
		UserPrompt:            userPrompt,
	})
	if err != nil {
		log.Printf("event=autonomy_turn_failed run_id=%s turn_latency_ms=%d err=%v", runID, durationMS(time.Since(started)), err)
		return err
	}
	log.Printf("event=autonomy_turn_completed run_id=%s status=%s thread=%s turn=%s tool_calls=%d turn_latency_ms=%d", runID, result.Status, result.ThreadID, result.TurnID, len(result.ToolCalls), durationMS(time.Since(started)))
	if assistantText := strings.TrimSpace(result.AssistantText); assistantText != "" {
		log.Printf("event=autonomy_assistant_text run_id=%s thread=%s turn=%s text=%q", runID, result.ThreadID, result.TurnID, assistantText)
		logDecisionSummary("autonomy", runID, result.ThreadID, result.TurnID, assistantText)
	}
	for i, toolCall := range result.ToolCalls {
		logTurnToolCall("autonomy", runID, result.ThreadID, result.TurnID, i, toolCall)
	}
	if strings.TrimSpace(result.ErrorMessage) != "" {
		log.Printf("event=autonomy_turn_error_detail run_id=%s err=%s", runID, result.ErrorMessage)
	}
	return nil
}

func buildAutonomyPrompt(channels []discordx.ChannelInfo) string {
	lines := []string{
		prompt.AutonomySystemPrompt,
		"指定チャンネルを観察し、必要なら send_message / reply_message / add_reaction を使ってよいです。",
		"すべての出力で SOUL.md のキャラクターを維持しつつ、文脈と相手に合わせてください。",
		"ownerの最近のX投稿は必要なときだけ twilog-mcp で確認してよいです。",
	}
	if len(channels) > 0 {
		items := make([]string, 0, len(channels))
		for _, ch := range channels {
			items = append(items, fmt.Sprintf("- %s (%s)", fallbackForLog(ch.Name, "unknown"), ch.ChannelID))
		}
		lines = append(lines, "", "観察可能チャンネル:", strings.Join(items, "\n"))
	}
	return strings.Join(lines, "\n")
}
