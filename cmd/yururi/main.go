package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/sigumaa/yururi/internal/codex"
	"github.com/sigumaa/yururi/internal/config"
	"github.com/sigumaa/yururi/internal/discordx"
	"github.com/sigumaa/yururi/internal/dispatch"
	"github.com/sigumaa/yururi/internal/heartbeat"
	"github.com/sigumaa/yururi/internal/mcpserver"
	"github.com/sigumaa/yururi/internal/orchestrator"
	"github.com/sigumaa/yururi/internal/policy"
	"github.com/sigumaa/yururi/internal/prompt"
	"github.com/sigumaa/yururi/internal/xai"
)

type heartbeatRuntime interface {
	RunTurn(ctx context.Context, input codex.TurnInput) (codex.TurnResult, error)
}

type heartbeatWhisperSender interface {
	SendMessage(ctx context.Context, channelID string, content string) (string, error)
}

type timesWhisperState struct {
	mu     sync.Mutex
	lastAt time.Time
}

const maxHeartbeatLogValueLen = 280

func main() {
	configPath := flag.String("config", "runtime/config.yaml", "path to config yaml")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	if err := prompt.EnsureWorkspaceInstructionFiles(cfg.Codex.WorkspaceDir); err != nil {
		log.Fatalf("prepare workspace instruction files: %v", err)
	}

	discord, err := discordgo.New("Bot " + cfg.Discord.Token)
	if err != nil {
		log.Fatalf("create discord session: %v", err)
	}
	discord.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsMessageContent

	gateway := discordx.NewGateway(discord, cfg.Discord)
	var xSearchClient *xai.Client
	if cfg.XAI.Enabled {
		xSearchClient = xai.NewClient(xai.Config{
			BaseURL: cfg.XAI.BaseURL,
			APIKey:  cfg.XAI.APIKey,
			Model:   cfg.XAI.Model,
			HTTPClient: &http.Client{
				Timeout: time.Duration(cfg.XAI.TimeoutSec) * time.Second,
			},
		})
	}

	mcpSrv, err := mcpserver.New(cfg.MCP.Bind, cfg.Heartbeat.Timezone, cfg.Codex.WorkspaceDir, gateway, xSearchClient)
	if err != nil {
		log.Fatalf("create mcp server: %v", err)
	}
	aiClient := codex.NewClient(cfg.Codex, cfg.MCP.URL)
	coordinator := orchestrator.New(aiClient)
	defer aiClient.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	var runSeq atomic.Uint64
	whisperState := &timesWhisperState{}

	dispatcher := dispatch.New(ctx, 128, 1200*time.Millisecond, func(m *discordgo.MessageCreate, meta dispatch.CallbackMetadata) {
		if meta.MergedCount > 1 {
			log.Printf("event=channel_burst_coalesced guild=%s channel=%s merged=%d latest_message=%s queue_wait_ms=%d", m.GuildID, m.ChannelID, meta.MergedCount, m.ID, durationMS(meta.QueueWait))
		}
		runID := nextRunID(&runSeq, "msg")
		handleMessage(ctx, cfg, coordinator, gateway, discord, m, meta, whisperState, runID)
	})

	errCh := make(chan error, 1)
	go func() {
		if err := mcpSrv.Start(ctx); err != nil {
			errCh <- err
			stop()
		}
	}()

	discord.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		if dropped := dispatcher.Enqueue(m); dropped {
			log.Printf("dispatcher queue drop occurred: guild=%s channel=%s latest_message=%s", m.GuildID, m.ChannelID, m.ID)
		}
	})

	if err := discord.Open(); err != nil {
		log.Fatalf("open discord session: %v", err)
	}
	defer discord.Close()

	if cfg.Heartbeat.Enabled {
		runner, err := heartbeat.NewRunner(cfg.Heartbeat.Cron, cfg.Heartbeat.Timezone, func(runCtx context.Context) error {
			return runHeartbeatTurn(runCtx, cfg, aiClient, gateway, whisperState, nextRunID(&runSeq, "hb"))
		})
		if err != nil {
			log.Fatalf("init heartbeat runner: %v", err)
		}
		runner.Start(ctx)
	}
	if cfg.Autonomy.Enabled {
		runner, err := heartbeat.NewRunner(cfg.Autonomy.Cron, cfg.Autonomy.Timezone, func(runCtx context.Context) error {
			return runAutonomyTurn(runCtx, cfg, aiClient, gateway, whisperState, nextRunID(&runSeq, "auto"))
		})
		if err != nil {
			log.Fatalf("init autonomy runner: %v", err)
		}
		runner.Start(ctx)
	}

	log.Printf(
		"yururi started: mcp_url=%s model=%s reasoning=%s x_search_enabled=%t x_search_model=%s",
		cfg.MCP.URL,
		cfg.Codex.Model,
		cfg.Codex.ReasoningEffort,
		cfg.XAI.Enabled,
		cfg.XAI.Model,
	)

	select {
	case <-ctx.Done():
	case err := <-errCh:
		if err != nil {
			log.Printf("mcp server stopped with error: %v", err)
		}
	}
	log.Printf("yururi stopped")
}

func handleMessage(rootCtx context.Context, cfg config.Config, coordinator *orchestrator.Coordinator, gateway *discordx.Gateway, session *discordgo.Session, m *discordgo.MessageCreate, meta dispatch.CallbackMetadata, whisperState *timesWhisperState, runID string) {
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
	}
	if strings.TrimSpace(result.ErrorMessage) != "" {
		log.Printf("event=codex_turn_error_detail run_id=%s message=%s err=%s", runID, m.ID, result.ErrorMessage)
	}
	if err := postMessageWhisper(ctx, cfg, gateway, whisperState, runID, m, channelName, result); err != nil {
		log.Printf("event=message_times_post_failed run_id=%s message=%s err=%v", runID, m.ID, err)
	}
}

func runHeartbeatTurn(ctx context.Context, cfg config.Config, runtime heartbeatRuntime, sender heartbeatWhisperSender, whisperState *timesWhisperState, runID string) error {
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
		if maybeDecisionOutput(assistantText) {
			decision, err := codex.ParseDecisionOutput(assistantText)
			if err != nil {
				log.Printf("event=heartbeat_decision_parse_failed run_id=%s thread=%s turn=%s err=%v", runID, result.ThreadID, result.TurnID, err)
			} else {
				log.Printf(
					"event=heartbeat_decision_summary run_id=%s thread=%s turn=%s action=%s content=%q",
					runID,
					result.ThreadID,
					result.TurnID,
					decision.Action,
					trimLogString(decision.Content, maxHeartbeatLogValueLen),
				)
			}
		}
	}
	for i, toolCall := range result.ToolCalls {
		log.Printf(
			"event=heartbeat_tool_call run_id=%s thread=%s turn=%s index=%d server=%s tool=%s status=%s arguments=%q result=%q",
			runID,
			result.ThreadID,
			result.TurnID,
			i,
			toolCall.Server,
			toolCall.Tool,
			toolCall.Status,
			trimLogAny(toolCall.Arguments, maxHeartbeatLogValueLen),
			trimLogAny(toolCall.Result, maxHeartbeatLogValueLen),
		)
	}
	if strings.TrimSpace(result.ErrorMessage) != "" {
		log.Printf("event=heartbeat_turn_error_detail run_id=%s err=%s", runID, result.ErrorMessage)
	}
	if err := postHeartbeatWhisper(ctx, cfg, sender, whisperState, runID, result); err != nil {
		log.Printf("event=heartbeat_times_post_failed run_id=%s err=%v", runID, err)
	}
	return nil
}

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
	userPrompt := buildAutonomyPrompt(channels, cfg.Persona.TimesChannelID)
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
	if strings.TrimSpace(result.AssistantText) != "" {
		log.Printf("event=autonomy_assistant_text run_id=%s thread=%s turn=%s text=%q", runID, result.ThreadID, result.TurnID, result.AssistantText)
	}
	if strings.TrimSpace(result.ErrorMessage) != "" {
		log.Printf("event=autonomy_turn_error_detail run_id=%s err=%s", runID, result.ErrorMessage)
	}
	if err := postHeartbeatWhisper(ctx, cfg, gateway, whisperState, runID, result); err != nil {
		log.Printf("event=autonomy_times_post_failed run_id=%s err=%v", runID, err)
	}
	return nil
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
	if maxLen <= 0 || len(trimmed) <= maxLen {
		return trimmed
	}
	if maxLen <= 3 {
		return trimmed[:maxLen]
	}
	return trimmed[:maxLen-3] + "..."
}

func maybeDecisionOutput(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	return strings.Contains(trimmed, "{") && strings.Contains(trimmed, "\"action\"")
}

func shouldResetSessionAfterMemoryUpdate(toolCalls []codex.MCPToolCall) bool {
	for _, toolCall := range toolCalls {
		if isMemoryUpdateToolCall(toolCall) {
			return true
		}
	}
	return false
}

func isMemoryUpdateToolCall(toolCall codex.MCPToolCall) bool {
	tool := strings.ToLower(strings.TrimSpace(toolCall.Tool))
	if tool != "append_workspace_doc" && tool != "replace_workspace_doc" {
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

func postHeartbeatWhisper(ctx context.Context, cfg config.Config, sender heartbeatWhisperSender, whisperState *timesWhisperState, runID string, result codex.TurnResult) error {
	channelID := strings.TrimSpace(cfg.Persona.TimesChannelID)
	if channelID == "" || sender == nil {
		return nil
	}
	content, ok := buildHeartbeatWhisperMessage(runID, result)
	if !ok {
		return nil
	}
	minInterval := time.Duration(cfg.Persona.TimesMinIntervalS) * time.Second
	if whisperState != nil && !whisperState.shouldSend(time.Now().UTC(), minInterval) {
		log.Printf("event=heartbeat_times_suppressed run_id=%s channel=%s reason=min_interval", runID, channelID)
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

func buildAutonomyPrompt(channels []discordx.ChannelInfo, timesChannelID string) string {
	lines := []string{
		prompt.HeartbeatSystemPrompt,
		"",
		"これは自律観察モードです。",
		"指定チャンネルを観察し、返信するほどではないが共有価値のある所感は times チャンネルへ send_message で短く投稿してください。",
		"ownerの最近のX投稿確認には twilog-mcp が利用可能なら優先してください。",
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
	return strings.Join(lines, "\n")
}

func postMessageWhisper(ctx context.Context, cfg config.Config, sender heartbeatWhisperSender, whisperState *timesWhisperState, runID string, message *discordgo.MessageCreate, channelName string, result codex.TurnResult) error {
	channelID := strings.TrimSpace(cfg.Persona.TimesChannelID)
	if channelID == "" || sender == nil {
		return nil
	}
	if message != nil && strings.TrimSpace(message.ChannelID) == channelID {
		return nil
	}
	content, ok := buildMessageWhisperMessage(runID, channelName, result)
	if !ok {
		return nil
	}
	minInterval := time.Duration(cfg.Persona.TimesMinIntervalS) * time.Second
	if whisperState != nil && !whisperState.shouldSend(time.Now().UTC(), minInterval) {
		log.Printf("event=message_times_suppressed run_id=%s channel=%s reason=min_interval", runID, channelID)
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

func buildHeartbeatWhisperMessage(runID string, result codex.TurnResult) (string, bool) {
	action, summary, hasDecision := heartbeatDecisionSummary(result.AssistantText)
	errMsg := strings.TrimSpace(result.ErrorMessage)
	shouldPost := errMsg != "" || len(result.ToolCalls) > 0 || (hasDecision && action != "noop")
	if !shouldPost {
		return "", false
	}

	lines := []string{
		"ゆるりのつぶやき: 定期チェック",
		fmt.Sprintf("run=%s status=%s", runID, fallbackForLog(result.Status, "unknown")),
	}
	if hasDecision {
		lines = append(lines, fmt.Sprintf("判断=%s", action))
		if action != "noop" && strings.TrimSpace(summary) != "" {
			lines = append(lines, "要点="+trimLogString(summary, 140))
		}
	}
	if len(result.ToolCalls) > 0 {
		lines = append(lines, "実行="+summarizeToolCalls(result.ToolCalls, 4))
	}
	if errMsg != "" {
		lines = append(lines, "エラー="+trimLogString(errMsg, 140))
	}
	return strings.Join(lines, "\n"), true
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

func buildMessageWhisperMessage(runID string, channelName string, result codex.TurnResult) (string, bool) {
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
	text = trimLogString(text, 140)
	if text == "" && errMsg == "" {
		return "", false
	}

	lines := []string{
		"ゆるりのつぶやき: 観察メモ",
		fmt.Sprintf("run=%s channel=%s", runID, fallbackForLog(channelName, "unknown")),
	}
	if text != "" {
		lines = append(lines, "所感="+text)
	}
	if errMsg != "" {
		lines = append(lines, "エラー="+trimLogString(errMsg, 140))
	}
	return strings.Join(lines, "\n"), true
}

func summarizeToolCalls(toolCalls []codex.MCPToolCall, limit int) string {
	if len(toolCalls) == 0 {
		return "none"
	}
	if limit <= 0 {
		limit = len(toolCalls)
	}
	parts := make([]string, 0, limit)
	for i, call := range toolCalls {
		if i >= limit {
			parts = append(parts, fmt.Sprintf("...(+%d)", len(toolCalls)-limit))
			break
		}
		name := strings.TrimSpace(call.Tool)
		if name == "" {
			name = "unknown_tool"
		}
		status := strings.TrimSpace(call.Status)
		if status == "" {
			status = "unknown"
		}
		parts = append(parts, name+"("+status+")")
	}
	return strings.Join(parts, ", ")
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

func fallbackForLog(value string, fallback string) string {
	v := strings.TrimSpace(value)
	if v == "" {
		return fallback
	}
	return v
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
