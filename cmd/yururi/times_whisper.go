package main

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/sigumaa/yururi/internal/codex"
	"github.com/sigumaa/yururi/internal/config"
)

func postHeartbeatWhisper(ctx context.Context, cfg config.Config, sender heartbeatWhisperSender, whisperState *timesWhisperState, runID string, result codex.TurnResult) error {
	channelID := strings.TrimSpace(cfg.Persona.TimesChannelID)
	if channelID == "" || sender == nil {
		return nil
	}
	content, ok := buildHeartbeatWhisperMessage(result)
	if !ok {
		return nil
	}
	minInterval := time.Duration(cfg.Persona.TimesMinIntervalS) * time.Second
	if whisperState != nil && !whisperState.shouldSend(time.Now().UTC(), minInterval) {
		log.Printf("event=heartbeat_times_suppressed run_id=%s channel=%s reason=min_interval", runID, channelID)
		return nil
	}
	if duplicated, err := shouldSuppressDuplicateWhisper(ctx, sender, channelID, content); err != nil {
		log.Printf("event=heartbeat_times_dedupe_history_failed run_id=%s channel=%s err=%v", runID, channelID, err)
	} else if duplicated {
		log.Printf("event=heartbeat_times_suppressed run_id=%s channel=%s reason=duplicate_recent", runID, channelID)
		return nil
	}
	if _, err := sender.SendMessage(ctx, channelID, content); err != nil {
		return err
	}
	if whisperState != nil {
		whisperState.markSent(time.Now().UTC())
	}
	log.Printf("event=heartbeat_times_posted run_id=%s channel=%s", runID, channelID)
	return nil
}

func postMessageWhisper(ctx context.Context, cfg config.Config, sender heartbeatWhisperSender, whisperState *timesWhisperState, runID string, message *discordgo.MessageCreate, result codex.TurnResult) error {
	channelID := strings.TrimSpace(cfg.Persona.TimesChannelID)
	if channelID == "" || sender == nil {
		return nil
	}
	if message != nil && strings.TrimSpace(message.ChannelID) == channelID {
		return nil
	}
	content, ok := buildMessageWhisperMessage(result)
	if !ok {
		return nil
	}
	minInterval := time.Duration(cfg.Persona.TimesMinIntervalS) * time.Second
	if whisperState != nil && !whisperState.shouldSend(time.Now().UTC(), minInterval) {
		log.Printf("event=message_times_suppressed run_id=%s channel=%s reason=min_interval", runID, channelID)
		return nil
	}
	if duplicated, err := shouldSuppressDuplicateWhisper(ctx, sender, channelID, content); err != nil {
		log.Printf("event=message_times_dedupe_history_failed run_id=%s channel=%s err=%v", runID, channelID, err)
	} else if duplicated {
		log.Printf("event=message_times_suppressed run_id=%s channel=%s reason=duplicate_recent", runID, channelID)
		return nil
	}
	if _, err := sender.SendMessage(ctx, channelID, content); err != nil {
		return err
	}
	if whisperState != nil {
		whisperState.markSent(time.Now().UTC())
	}
	if message != nil {
		log.Printf("event=message_times_posted run_id=%s message=%s channel=%s", runID, message.ID, channelID)
	} else {
		log.Printf("event=message_times_posted run_id=%s channel=%s", runID, channelID)
	}
	return nil
}

func buildHeartbeatWhisperMessage(result codex.TurnResult) (string, bool) {
	if hasDeliveryToolCall(result.ToolCalls) {
		return "", false
	}
	action, summary, hasDecision := heartbeatDecisionSummary(result.AssistantText)
	if hasDecision {
		if action == "noop" && strings.TrimSpace(summary) == "" && strings.TrimSpace(result.ErrorMessage) == "" && len(result.ToolCalls) == 0 {
			return "", false
		}
		if strings.TrimSpace(summary) != "" {
			return selectPersonaWhisperText(summary)
		}
	}
	text := strings.TrimSpace(result.AssistantText)
	if text != "" && !hasDecision {
		return selectPersonaWhisperText(text)
	}
	return "", false
}

func heartbeatDecisionSummary(assistantText string) (action string, summary string, ok bool) {
	text := strings.TrimSpace(assistantText)
	if text == "" {
		return "", "", false
	}
	decision, err := codex.ParseDecisionOutput(text)
	if err != nil {
		return "", "", false
	}
	return strings.TrimSpace(decision.Action), strings.TrimSpace(decision.Content), true
}

func messageDecisionSummary(assistantText string) (action string, summary string, ok bool) {
	text := strings.TrimSpace(assistantText)
	if text == "" {
		return "", "", false
	}
	decision, err := codex.ParseDecisionOutput(text)
	if err != nil {
		return "", "", false
	}
	return strings.TrimSpace(decision.Action), strings.TrimSpace(decision.Content), true
}

func buildMessageWhisperMessage(result codex.TurnResult) (string, bool) {
	if hasDeliveryToolCall(result.ToolCalls) {
		return "", false
	}
	errMsg := strings.TrimSpace(result.ErrorMessage)
	action, summary, hasDecision := messageDecisionSummary(result.AssistantText)
	if hasDecision && action == "noop" && summary == "" && errMsg == "" {
		return "", false
	}

	text := ""
	if hasDecision {
		text = strings.TrimSpace(summary)
	} else {
		text = strings.TrimSpace(result.AssistantText)
	}
	if text == "" && errMsg == "" {
		return "", false
	}
	if text != "" {
		return selectPersonaWhisperText(text)
	}
	return "", false
}

func hasDeliveryToolCall(toolCalls []codex.MCPToolCall) bool {
	for _, call := range toolCalls {
		tool := strings.ToLower(strings.TrimSpace(call.Tool))
		if tool == "send_message" || tool == "reply_message" {
			return true
		}
	}
	return false
}

func selectPersonaWhisperText(text string) (string, bool) {
	line := trimLogString(text, 280)
	if line == "" {
		return "", false
	}
	return line, true
}

func shouldSuppressDuplicateWhisper(ctx context.Context, sender heartbeatWhisperSender, channelID string, content string) (bool, error) {
	if sender == nil {
		return false, nil
	}
	reader, ok := sender.(heartbeatWhisperHistoryReader)
	if !ok {
		return false, nil
	}
	normalized := normalizeWhisperContent(content)
	if normalized == "" {
		return false, nil
	}
	history, err := reader.ReadMessageHistory(ctx, channelID, "", timesWhisperDedupeHistoryLimit)
	if err != nil {
		return false, err
	}
	for _, msg := range history {
		if normalizeWhisperContent(msg.Content) == normalized {
			return true, nil
		}
	}
	return false, nil
}

func normalizeWhisperContent(text string) string {
	normalized := strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	return strings.ToLower(normalized)
}

func (s *timesWhisperState) shouldSend(now time.Time, minInterval time.Duration) bool {
	if s == nil || minInterval <= 0 {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastAt.IsZero() {
		return true
	}
	return now.Sub(s.lastAt) >= minInterval
}

func (s *timesWhisperState) markSent(now time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.lastAt = now
	s.mu.Unlock()
}
