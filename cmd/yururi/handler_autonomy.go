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

func runAutonomyTurn(ctx context.Context, cfg config.Config, runtime heartbeatRuntime, gateway *discordx.Gateway, whisperState *timesWhisperState, runID string) error {
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
	timesChannelID := strings.TrimSpace(cfg.Persona.TimesChannelID)
	timesRecent := []string(nil)
	if timesChannelID != "" && gateway != nil {
		history, historyErr := gateway.ReadMessageHistory(ctx, timesChannelID, "", autonomyTimesHistoryLimit)
		if historyErr != nil {
			log.Printf("event=autonomy_times_history_failed run_id=%s channel=%s err=%v", runID, timesChannelID, historyErr)
		} else {
			timesRecent = extractTimesPromptHistory(history, autonomyTimesHistoryMaxLen)
			log.Printf("event=autonomy_times_history_loaded run_id=%s channel=%s messages=%d", runID, timesChannelID, len(timesRecent))
		}
	}
	userPrompt := buildAutonomyPrompt(channels, timesChannelID, timesRecent)
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
	if err := postHeartbeatWhisper(ctx, cfg, gateway, whisperState, runID, result); err != nil {
		log.Printf("event=autonomy_times_post_failed run_id=%s err=%v", runID, err)
	}
	return nil
}

func buildAutonomyPrompt(channels []discordx.ChannelInfo, timesChannelID string, timesRecent []string) string {
	lines := []string{
		prompt.AutonomySystemPrompt,
		"指定チャンネルを観察し、返信するほどではないが共有価値のある内容は times チャンネルへ send_message で共有してよいです。",
		"返信・times投稿を含むすべての出力で SOUL.md のキャラクターを維持しつつ、文脈と相手に合わせてください。",
		"times投稿は形式を固定しません。独り言として、思ったことを SOUL.md のペルソナでそのままつぶやいてください。",
		"times投稿では人に説明する口調や、誰かに話しかける口調は避けてください。",
		"ownerの最近のX投稿は必要なときだけ twilog-mcp で確認してよいです。毎回参照や引用をする必要はありません。",
	}
	if strings.TrimSpace(timesChannelID) != "" {
		lines = append(lines, "times_channel_id="+strings.TrimSpace(timesChannelID))
	}
	if len(channels) > 0 {
		items := make([]string, 0, len(channels))
		for _, ch := range channels {
			items = append(items, fmt.Sprintf("- %s (%s)", fallbackForLog(ch.Name, "unknown"), ch.ChannelID))
		}
		lines = append(lines, "", "観察可能チャンネル:", strings.Join(items, "\n"))
	}
	if len(timesRecent) > 0 {
		lines = append(lines, "", "times直近投稿参照（重複回避のため。文体や内容を真似しないこと）:")
		for _, text := range timesRecent {
			lines = append(lines, "- "+text)
		}
	}
	return strings.Join(lines, "\n")
}

func extractTimesPromptHistory(messages []discordx.Message, maxLen int) []string {
	if len(messages) == 0 {
		return nil
	}
	out := make([]string, 0, len(messages))
	for i := len(messages) - 1; i >= 0; i-- {
		text := trimLogString(messages[i].Content, maxLen)
		if text == "" {
			continue
		}
		out = append(out, text)
	}
	return out
}
