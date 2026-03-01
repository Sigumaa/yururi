package main

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/sigumaa/yururi/internal/codex"
	"github.com/sigumaa/yururi/internal/config"
	"github.com/sigumaa/yururi/internal/discordx"
	"github.com/sigumaa/yururi/internal/dispatch"
	"github.com/sigumaa/yururi/internal/orchestrator"
	"github.com/sigumaa/yururi/internal/policy"
	"github.com/sigumaa/yururi/internal/prompt"
)

func handleMessage(rootCtx context.Context, cfg config.Config, coordinator *orchestrator.Coordinator, gateway *discordx.Gateway, session *discordgo.Session, m *discordgo.MessageCreate, meta dispatch.CallbackMetadata, whisperState *timesWhisperState, runID string) {
	_ = whisperState
	authorID := ""
	authorIsBot := false
	authorName := ""
	if m.Author != nil {
		authorID = m.Author.ID
		authorIsBot = m.Author.Bot
		authorName = displayAuthorName(m)
	}
	log.Printf("event=message_received run_id=%s message=%s guild=%s channel=%s author=%s merged=%d queue_wait_ms=%d enqueued_at=%s", runID, m.ID, m.GuildID, m.ChannelID, authorID, normalizeMergedCount(meta.MergedCount), durationMS(meta.QueueWait), meta.EnqueuedAt.UTC().Format(time.RFC3339Nano))

	incoming := policy.Incoming{
		GuildID:     m.GuildID,
		ChannelID:   m.ChannelID,
		AuthorID:    authorID,
		AuthorIsBot: authorIsBot,
		WebhookID:   m.WebhookID,
	}
	allowed, reason := policy.Evaluate(cfg.Discord, incoming)
	if !allowed {
		log.Printf("event=message_filtered run_id=%s message=%s guild=%s channel=%s author=%s reason=%s", runID, m.ID, m.GuildID, m.ChannelID, authorID, reason)
		return
	}

	ctx, cancel := context.WithTimeout(rootCtx, 3*time.Minute)
	defer cancel()

	historyLimit := calculateHistoryLimit(meta.MergedCount)
	history, err := gateway.ReadMessageHistory(ctx, m.ChannelID, m.ID, historyLimit)
	if err != nil {
		log.Printf("event=history_read_failed run_id=%s guild=%s channel=%s message=%s err=%v", runID, m.GuildID, m.ChannelID, m.ID, err)
	}
	recent := toPromptMessages(history)

	instructions, err := prompt.LoadWorkspaceInstructions(cfg.Codex.WorkspaceDir)
	if err != nil {
		log.Printf("load workspace instructions failed: err=%v", err)
		return
	}
	channelName := m.ChannelID
	if session != nil {
		if ch, err := session.Channel(m.ChannelID); err == nil && ch != nil && strings.TrimSpace(ch.Name) != "" {
			channelName = ch.Name
		}
	}
	bundle := prompt.BuildMessageBundle(instructions, prompt.MessageInput{
		GuildID:     m.GuildID,
		ChannelID:   m.ChannelID,
		ChannelName: channelName,
		MergedCount: meta.MergedCount,
		IsOwner:     authorID != "" && authorID == cfg.Persona.OwnerUserID,
		Current: prompt.RuntimeMessage{
			ID:         m.ID,
			AuthorID:   authorID,
			AuthorName: authorName,
			Content:    mergeMessageContent(m),
			CreatedAt:  m.Timestamp,
		},
		Recent: recent,
	})

	turnStarted := time.Now()
	log.Printf("event=codex_turn_started run_id=%s message=%s guild=%s channel=%s author=%s", runID, m.ID, m.GuildID, m.ChannelID, authorID)
	channelKey := orchestrator.ChannelKey(m.GuildID, m.ChannelID)
	result, err := coordinator.RunMessageTurn(ctx, channelKey, codex.TurnInput{
		BaseInstructions:      bundle.BaseInstructions,
		DeveloperInstructions: bundle.DeveloperInstructions,
		UserPrompt:            bundle.UserPrompt,
	})
	if err != nil {
		log.Printf("event=codex_turn_failed run_id=%s guild=%s channel=%s message=%s turn_latency_ms=%d err=%v", runID, m.GuildID, m.ChannelID, m.ID, durationMS(time.Since(turnStarted)), err)
		return
	}
	log.Printf("event=codex_turn_completed run_id=%s message=%s guild=%s channel=%s author=%s status=%s thread=%s turn=%s tool_calls=%d turn_latency_ms=%d", runID, m.ID, m.GuildID, m.ChannelID, authorID, result.Status, result.ThreadID, result.TurnID, len(result.ToolCalls), durationMS(time.Since(turnStarted)))
	if shouldResetSessionAfterMemoryUpdate(result.ToolCalls) {
		if coordinator.ResetSession(channelKey) {
			log.Printf("event=message_session_reset run_id=%s message=%s guild=%s channel=%s reason=memory_doc_updated", runID, m.ID, m.GuildID, m.ChannelID)
		}
	}
	if strings.TrimSpace(result.AssistantText) != "" {
		log.Printf("event=assistant_text run_id=%s message=%s thread=%s turn=%s text=%q", runID, m.ID, result.ThreadID, result.TurnID, result.AssistantText)
		logDecisionSummary("message", runID, result.ThreadID, result.TurnID, result.AssistantText)
	}
	for i, toolCall := range result.ToolCalls {
		logTurnToolCall("message", runID, result.ThreadID, result.TurnID, i, toolCall)
	}
	if strings.TrimSpace(result.ErrorMessage) != "" {
		log.Printf("event=codex_turn_error_detail run_id=%s message=%s err=%s", runID, m.ID, result.ErrorMessage)
	}
	log.Printf("event=message_times_skipped run_id=%s message=%s reason=disabled_auto_whisper", runID, m.ID)
}

func toPromptMessages(messages []discordx.Message) []prompt.RuntimeMessage {
	if len(messages) == 0 {
		return nil
	}
	reversed := make([]prompt.RuntimeMessage, 0, len(messages))
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		reversed = append(reversed, prompt.RuntimeMessage{
			ID:         msg.ID,
			AuthorID:   msg.AuthorID,
			AuthorName: msg.AuthorName,
			Content:    msg.Content,
			CreatedAt:  msg.CreatedAt,
		})
	}
	return reversed
}

func mergeMessageContent(m *discordgo.MessageCreate) string {
	if m == nil {
		return ""
	}
	content := strings.TrimSpace(m.Content)
	if len(m.Attachments) == 0 {
		return content
	}
	parts := make([]string, 0, len(m.Attachments))
	for _, a := range m.Attachments {
		if a == nil {
			continue
		}
		if strings.TrimSpace(a.URL) == "" {
			continue
		}
		name := strings.TrimSpace(a.Filename)
		if name == "" {
			name = "attachment"
		}
		parts = append(parts, name+"("+a.URL+")")
	}
	if len(parts) == 0 {
		return content
	}
	if content == "" {
		return "attachments: " + strings.Join(parts, ", ")
	}
	return content + "\nattachments: " + strings.Join(parts, ", ")
}

func displayAuthorName(m *discordgo.MessageCreate) string {
	if m == nil || m.Author == nil {
		return "unknown"
	}
	if m.Member != nil && strings.TrimSpace(m.Member.Nick) != "" {
		return m.Member.Nick
	}
	if strings.TrimSpace(m.Author.GlobalName) != "" {
		return m.Author.GlobalName
	}
	if strings.TrimSpace(m.Author.Username) != "" {
		return m.Author.Username
	}
	return m.Author.ID
}

func calculateHistoryLimit(mergedCount int) int {
	const (
		minLimit = 30
		maxLimit = 100
		margin   = 12
	)
	if mergedCount <= 1 {
		return minLimit
	}
	limit := mergedCount + margin
	if limit < minLimit {
		limit = minLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	return limit
}

func shouldResetSessionAfterMemoryUpdate(toolCalls []codex.MCPToolCall) bool {
	for _, toolCall := range toolCalls {
		if isMemoryReplaceToolCall(toolCall) {
			return true
		}
	}
	return false
}

func isMemoryReplaceToolCall(toolCall codex.MCPToolCall) bool {
	tool := strings.ToLower(strings.TrimSpace(toolCall.Tool))
	if tool != "replace_workspace_doc" {
		return false
	}

	name, ok := workspaceDocNameFromArguments(toolCall.Arguments)
	if !ok {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(name), "MEMORY.md")
}

func workspaceDocNameFromArguments(arguments any) (string, bool) {
	switch value := arguments.(type) {
	case map[string]any:
		name, ok := value["name"].(string)
		return name, ok
	case string:
		var payload map[string]any
		if err := json.Unmarshal([]byte(value), &payload); err != nil {
			return "", false
		}
		name, ok := payload["name"].(string)
		return name, ok
	default:
		return "", false
	}
}
