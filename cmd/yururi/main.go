package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sort"
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

type heartbeatWhisperHistoryReader interface {
	ReadMessageHistory(ctx context.Context, channelID string, beforeMessageID string, limit int) ([]discordx.Message, error)
}

type timesWhisperState struct {
	mu     sync.Mutex
	lastAt time.Time
}

const (
	maxHeartbeatLogValueLen        = 280
	timesWhisperDedupeHistoryLimit = 20
)

func main() {
	configPath := flag.String("config", "runtime/config.yaml", "path to config yaml")
	flag.Parse()
	configureLogOutput(os.Stdout)

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

	resolvedObserve, err := resolveObserveTextChannels(discord, cfg.Discord)
	if err != nil {
		log.Printf("event=observe_categories_resolve_failed guild=%s categories=%d err=%v", cfg.Discord.GuildID, len(cfg.Discord.ObserveCategoryIDs), err)
	} else {
		added := len(resolvedObserve) - len(cfg.Discord.ObserveChannelIDs)
		cfg.Discord.ObserveChannelIDs = resolvedObserve
		if len(cfg.Discord.ObserveCategoryIDs) > 0 {
			log.Printf("event=observe_categories_resolved guild=%s categories=%d observe_channels=%d added=%d", cfg.Discord.GuildID, len(cfg.Discord.ObserveCategoryIDs), len(cfg.Discord.ObserveChannelIDs), added)
		}
	}

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
	stop()
	log.Printf("event=shutdown_started")
	runShutdownStep("discord_close", 2*time.Second, func() {
		_ = discord.Close()
	})
	runShutdownStep("codex_close", 2*time.Second, func() {
		aiClient.Close()
	})
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
		logDecisionSummary("message", runID, result.ThreadID, result.TurnID, result.AssistantText)
	}
	for i, toolCall := range result.ToolCalls {
		logTurnToolCall("message", runID, result.ThreadID, result.TurnID, i, toolCall)
	}
	if strings.TrimSpace(result.ErrorMessage) != "" {
		log.Printf("event=codex_turn_error_detail run_id=%s message=%s err=%s", runID, m.ID, result.ErrorMessage)
	}
	if err := postMessageWhisper(ctx, cfg, gateway, whisperState, runID, m, result); err != nil {
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
		logDecisionSummary("heartbeat", runID, result.ThreadID, result.TurnID, assistantText)
	}
	for i, toolCall := range result.ToolCalls {
		logTurnToolCall("heartbeat", runID, result.ThreadID, result.TurnID, i, toolCall)
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

func buildAutonomyPrompt(channels []discordx.ChannelInfo, timesChannelID string) string {
	lines := []string{
		prompt.AutonomySystemPrompt,
		"指定チャンネルを観察し、返信するほどではないが共有価値のある内容は times チャンネルへ send_message で共有してよいです。",
		"返信・times投稿を含むすべての出力で SOUL.md のキャラクター・語り口を維持してください。",
		"times投稿は形式を固定しません。独り言として、思ったことを SOUL.md のペルソナでそのままつぶやいてください。",
		"times投稿では人に説明する口調や、誰かに話しかける口調は避けてください。",
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

func runShutdownStep(name string, timeout time.Duration, fn func()) bool {
	if fn == nil {
		return false
	}
	started := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()

	if timeout <= 0 {
		<-done
		log.Printf("event=shutdown_step_completed step=%s latency_ms=%d", name, durationMS(time.Since(started)))
		return false
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		log.Printf("event=shutdown_step_completed step=%s latency_ms=%d", name, durationMS(time.Since(started)))
		return false
	case <-timer.C:
		log.Printf("event=shutdown_step_timeout step=%s timeout_ms=%d", name, durationMS(timeout))
		return true
	}
}

func resolveObserveTextChannels(session *discordgo.Session, discordCfg config.DiscordConfig) ([]string, error) {
	base := uniqueTrimmedValues(discordCfg.ObserveChannelIDs)
	categoryIDs := uniqueTrimmedValues(discordCfg.ObserveCategoryIDs)
	if len(categoryIDs) == 0 || session == nil {
		return base, nil
	}
	guildID := strings.TrimSpace(discordCfg.GuildID)
	if guildID == "" {
		return base, nil
	}
	channels, err := session.GuildChannels(guildID)
	if err != nil {
		return nil, fmt.Errorf("list guild channels: %w", err)
	}
	return expandObserveChannelIDsByCategory(base, categoryIDs, channels), nil
}

func expandObserveChannelIDsByCategory(base []string, categoryIDs []string, channels []*discordgo.Channel) []string {
	out := uniqueTrimmedValues(base)
	if len(categoryIDs) == 0 || len(channels) == 0 {
		return out
	}

	categorySet := make(map[string]struct{}, len(categoryIDs))
	for _, id := range categoryIDs {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			continue
		}
		categorySet[trimmed] = struct{}{}
	}
	if len(categorySet) == 0 {
		return out
	}

	exists := make(map[string]struct{}, len(out))
	for _, id := range out {
		exists[id] = struct{}{}
	}
	discovered := make([]string, 0)
	for _, ch := range channels {
		if ch == nil {
			continue
		}
		if _, ok := categorySet[strings.TrimSpace(ch.ParentID)]; !ok {
			continue
		}
		if ch.Type != discordgo.ChannelTypeGuildText {
			continue
		}
		channelID := strings.TrimSpace(ch.ID)
		if channelID == "" {
			continue
		}
		if _, ok := exists[channelID]; ok {
			continue
		}
		exists[channelID] = struct{}{}
		discovered = append(discovered, channelID)
	}

	sort.Strings(discovered)
	return append(out, discovered...)
}

func uniqueTrimmedValues(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
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
