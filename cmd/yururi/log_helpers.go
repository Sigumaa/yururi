package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sigumaa/yururi/internal/codex"
)

func nextRunID(seq *atomic.Uint64, prefix string) string {
	number := seq.Add(1)
	p := strings.TrimSpace(prefix)
	if p == "" {
		p = "run"
	}
	return fmt.Sprintf("%s-%d", p, number)
}

func durationMS(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return d.Milliseconds()
}

func normalizeMergedCount(v int) int {
	if v <= 0 {
		return 1
	}
	return v
}

func trimLogAny(value any, maxLen int) string {
	if value == nil {
		return ""
	}
	if str, ok := value.(string); ok {
		return trimLogString(str, maxLen)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return trimLogString(fmt.Sprint(value), maxLen)
	}
	return trimLogString(string(encoded), maxLen)
}

func trimLogString(text string, maxLen int) string {
	trimmed := strings.TrimSpace(text)
	runes := []rune(trimmed)
	if maxLen <= 0 || len(runes) <= maxLen {
		return trimmed
	}
	if maxLen <= 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-3]) + "..."
}

func maybeDecisionOutput(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	return strings.Contains(trimmed, "{") && strings.Contains(trimmed, "\"action\"")
}

func logDecisionSummary(eventPrefix string, runID string, threadID string, turnID string, assistantText string) {
	text := strings.TrimSpace(assistantText)
	if !maybeDecisionOutput(text) {
		return
	}
	decision, err := codex.ParseDecisionOutput(text)
	if err != nil {
		log.Printf("event=%s_decision_parse_failed run_id=%s thread=%s turn=%s err=%v", eventPrefix, runID, threadID, turnID, err)
		return
	}
	log.Printf(
		"event=%s_decision_summary run_id=%s thread=%s turn=%s action=%s content=%q",
		eventPrefix,
		runID,
		threadID,
		turnID,
		decision.Action,
		trimLogString(decision.Content, maxHeartbeatLogValueLen),
	)
}

func logTurnToolCall(eventPrefix string, runID string, threadID string, turnID string, index int, toolCall codex.MCPToolCall) {
	log.Printf(
		"event=%s_tool_call run_id=%s thread=%s turn=%s index=%d server=%s tool=%s status=%s arguments=%q result=%q",
		eventPrefix,
		runID,
		threadID,
		turnID,
		index,
		toolCall.Server,
		toolCall.Tool,
		toolCall.Status,
		trimLogAny(toolCall.Arguments, maxHeartbeatLogValueLen),
		trimLogAny(toolCall.Result, maxHeartbeatLogValueLen),
	)
}
