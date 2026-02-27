package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os/signal"
	"sort"
	"strings"
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
	"github.com/sigumaa/yururi/internal/memory"
	"github.com/sigumaa/yururi/internal/orchestrator"
	"github.com/sigumaa/yururi/internal/policy"
	"github.com/sigumaa/yururi/internal/prompt"
)

type messageTurnRunner interface {
	RunMessageTurn(ctx context.Context, channelKey string, input codex.TurnInput) (codex.TurnResult, error)
}

type fallbackTurnRunner interface {
	RunTurn(ctx context.Context, input codex.TurnInput) (codex.TurnResult, error)
}

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

	memoryStore, err := memory.NewStore(cfg.Memory.RootDir)
	if err != nil {
		log.Fatalf("init memory store: %v", err)
	}

	discord, err := discordgo.New("Bot " + cfg.Discord.Token)
	if err != nil {
		log.Fatalf("create discord session: %v", err)
	}
	discord.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsMessageContent

	gateway := discordx.NewGateway(discord, cfg.Discord)
	mcpSrv, err := mcpserver.New(cfg.MCP.Bind, cfg.Heartbeat.Timezone, cfg.Codex.WorkspaceDir, gateway, memoryStore)
	if err != nil {
		log.Fatalf("create mcp server: %v", err)
	}
	aiClient := codex.NewClient(cfg.Codex, cfg.MCP.URL)
	coordinator := orchestrator.New(aiClient)
	defer aiClient.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	var runSeq atomic.Uint64

	dispatcher := dispatch.New(ctx, 128, 1200*time.Millisecond, func(m *discordgo.MessageCreate, meta dispatch.CallbackMetadata) {
		if meta.MergedCount > 1 {
			log.Printf("event=channel_burst_coalesced guild=%s channel=%s merged=%d latest_message=%s queue_wait_ms=%d", m.GuildID, m.ChannelID, meta.MergedCount, m.ID, durationMS(meta.QueueWait))
		}
		runID := nextRunID(&runSeq, "msg")
		handleMessage(ctx, cfg, coordinator, gateway, discord, m, meta, runID)
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
			return runHeartbeatTurn(runCtx, cfg, coordinator, aiClient, memoryStore, nextRunID(&runSeq, "hb"))
		})
		if err != nil {
			log.Fatalf("init heartbeat runner: %v", err)
		}
		runner.Start(ctx)
	}

	log.Printf("yururi started: mcp_url=%s memory_root=%s model=%s reasoning=%s", cfg.MCP.URL, cfg.Memory.RootDir, cfg.Codex.Model, cfg.Codex.ReasoningEffort)

	select {
	case <-ctx.Done():
	case err := <-errCh:
		if err != nil {
			log.Printf("mcp server stopped with error: %v", err)
		}
	}
	log.Printf("yururi stopped")
}

func handleMessage(rootCtx context.Context, cfg config.Config, coordinator *orchestrator.Coordinator, gateway *discordx.Gateway, session *discordgo.Session, m *discordgo.MessageCreate, meta dispatch.CallbackMetadata, runID string) {
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
	if strings.TrimSpace(result.AssistantText) != "" {
		log.Printf("event=assistant_text run_id=%s message=%s thread=%s turn=%s text=%q", runID, m.ID, result.ThreadID, result.TurnID, result.AssistantText)
	}
	if strings.TrimSpace(result.ErrorMessage) != "" {
		log.Printf("event=codex_turn_error_detail run_id=%s message=%s err=%s", runID, m.ID, result.ErrorMessage)
	}
}

func runHeartbeatTurn(ctx context.Context, cfg config.Config, coordinator messageTurnRunner, runtime fallbackTurnRunner, memoryStore *memory.Store, runID string) error {
	started := time.Now()
	dueTasks, err := memoryStore.ClaimDueTasks(ctx, time.Now().UTC(), 20)
	if err != nil {
		return err
	}
	log.Printf("event=heartbeat_tick run_id=%s due_tasks=%d", runID, len(dueTasks))

	taskItems := make([]prompt.HeartbeatTask, 0, len(dueTasks))
	for _, task := range dueTasks {
		taskItems = append(taskItems, prompt.HeartbeatTask{
			TaskID:       task.TaskID,
			Title:        task.Title,
			Instructions: task.Instructions,
			ChannelID:    task.ChannelID,
			Schedule:     task.Schedule,
		})
	}

	instructions, err := prompt.LoadWorkspaceInstructions(cfg.Codex.WorkspaceDir)
	if err != nil {
		return err
	}

	byChannel := map[string][]prompt.HeartbeatTask{}
	fallbackTasks := make([]prompt.HeartbeatTask, 0, len(taskItems))
	for _, task := range taskItems {
		channelID := strings.TrimSpace(task.ChannelID)
		if channelID == "" {
			fallbackTasks = append(fallbackTasks, task)
			continue
		}
		byChannel[channelID] = append(byChannel[channelID], task)
	}

	var runErrs []error
	channelIDs := make([]string, 0, len(byChannel))
	for channelID := range byChannel {
		channelIDs = append(channelIDs, channelID)
	}
	sort.Strings(channelIDs)

	for index, channelID := range channelIDs {
		channelRunID := fmt.Sprintf("%s-c%d", runID, index+1)
		taskSet := byChannel[channelID]
		bundle := prompt.BuildHeartbeatBundle(instructions, prompt.HeartbeatInput{
			DueTasks: taskSet,
		})
		channelKey := orchestrator.ChannelKey(cfg.Discord.GuildID, channelID)
		result, err := coordinator.RunMessageTurn(ctx, channelKey, codex.TurnInput{
			BaseInstructions:      bundle.BaseInstructions,
			DeveloperInstructions: bundle.DeveloperInstructions,
			UserPrompt:            bundle.UserPrompt,
		})
		if err != nil {
			runErrs = append(runErrs, fmt.Errorf("channel %s heartbeat turn: %w", channelID, err))
			log.Printf("event=heartbeat_turn_failed run_id=%s channel=%s err=%v", channelRunID, channelID, err)
			continue
		}
		log.Printf("event=heartbeat_turn_completed run_id=%s channel=%s status=%s thread=%s turn=%s tool_calls=%d", channelRunID, channelID, result.Status, result.ThreadID, result.TurnID, len(result.ToolCalls))
		if strings.TrimSpace(result.ErrorMessage) != "" {
			log.Printf("event=heartbeat_turn_error_detail run_id=%s channel=%s err=%s", channelRunID, channelID, result.ErrorMessage)
		}
	}

	needsFallbackTurn := len(fallbackTasks) > 0 || len(taskItems) == 0
	if needsFallbackTurn {
		bundle := prompt.BuildHeartbeatBundle(instructions, prompt.HeartbeatInput{
			DueTasks: fallbackTasks,
		})
		result, err := runtime.RunTurn(ctx, codex.TurnInput{
			BaseInstructions:      bundle.BaseInstructions,
			DeveloperInstructions: bundle.DeveloperInstructions,
			UserPrompt:            bundle.UserPrompt,
		})
		if err != nil {
			runErrs = append(runErrs, fmt.Errorf("fallback heartbeat turn: %w", err))
			log.Printf("event=heartbeat_fallback_turn_failed run_id=%s err=%v", runID, err)
		} else {
			log.Printf("event=heartbeat_fallback_turn_completed run_id=%s status=%s tool_calls=%d", runID, result.Status, len(result.ToolCalls))
			if strings.TrimSpace(result.ErrorMessage) != "" {
				log.Printf("event=heartbeat_fallback_turn_error_detail run_id=%s err=%s", runID, result.ErrorMessage)
			}
		}
	}

	log.Printf("event=heartbeat_done run_id=%s due_tasks=%d turn_latency_ms=%d errors=%d", runID, len(taskItems), durationMS(time.Since(started)), len(runErrs))
	return errors.Join(runErrs...)
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
