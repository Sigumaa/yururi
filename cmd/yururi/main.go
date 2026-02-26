package main

import (
	"context"
	"flag"
	"log"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/sigumaa/yururi/internal/codex"
	"github.com/sigumaa/yururi/internal/config"
	"github.com/sigumaa/yururi/internal/discordx"
	"github.com/sigumaa/yururi/internal/heartbeat"
	"github.com/sigumaa/yururi/internal/mcpserver"
	"github.com/sigumaa/yururi/internal/memory"
	"github.com/sigumaa/yururi/internal/policy"
	"github.com/sigumaa/yururi/internal/prompt"
)

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
	mcpSrv, err := mcpserver.New(cfg.MCP.Bind, cfg.Heartbeat.Timezone, gateway, memoryStore)
	if err != nil {
		log.Fatalf("create mcp server: %v", err)
	}
	aiClient := codex.NewClient(cfg.Codex, cfg.MCP.URL)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		if err := mcpSrv.Start(ctx); err != nil {
			errCh <- err
			stop()
		}
	}()

	discord.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		go handleMessage(ctx, cfg, aiClient, gateway, s, m)
	})

	if err := discord.Open(); err != nil {
		log.Fatalf("open discord session: %v", err)
	}
	defer discord.Close()

	if cfg.Heartbeat.Enabled {
		runner, err := heartbeat.NewRunner(cfg.Heartbeat.Cron, cfg.Heartbeat.Timezone, func(runCtx context.Context) error {
			return runHeartbeatTurn(runCtx, cfg, aiClient, memoryStore)
		})
		if err != nil {
			log.Fatalf("init heartbeat runner: %v", err)
		}
		runner.Start(ctx)
	}

	log.Printf("yururi started: mcp_url=%s memory_root=%s", cfg.MCP.URL, cfg.Memory.RootDir)

	select {
	case <-ctx.Done():
	case err := <-errCh:
		if err != nil {
			log.Printf("mcp server stopped with error: %v", err)
		}
	}
	log.Printf("yururi stopped")
}

func handleMessage(rootCtx context.Context, cfg config.Config, runtime *codex.Client, gateway *discordx.Gateway, session *discordgo.Session, m *discordgo.MessageCreate) {
	authorID := ""
	authorIsBot := false
	authorName := ""
	if m.Author != nil {
		authorID = m.Author.ID
		authorIsBot = m.Author.Bot
		authorName = displayAuthorName(m)
	}
	log.Printf("message received: message=%s guild=%s channel=%s author=%s", m.ID, m.GuildID, m.ChannelID, authorID)

	incoming := policy.Incoming{
		GuildID:     m.GuildID,
		ChannelID:   m.ChannelID,
		AuthorID:    authorID,
		AuthorIsBot: authorIsBot,
		WebhookID:   m.WebhookID,
	}
	allowed, reason := policy.Evaluate(cfg.Discord, incoming)
	if !allowed {
		log.Printf("message filtered: message=%s guild=%s channel=%s author=%s reason=%s", m.ID, m.GuildID, m.ChannelID, authorID, reason)
		return
	}

	ctx, cancel := context.WithTimeout(rootCtx, 3*time.Minute)
	defer cancel()

	history, err := gateway.ReadMessageHistory(ctx, m.ChannelID, m.ID, 30)
	if err != nil {
		log.Printf("read history failed: guild=%s channel=%s message=%s err=%v", m.GuildID, m.ChannelID, m.ID, err)
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

	log.Printf("codex turn started: message=%s guild=%s channel=%s author=%s", m.ID, m.GuildID, m.ChannelID, authorID)
	result, err := runtime.RunTurn(ctx, codex.TurnInput{
		BaseInstructions:      bundle.BaseInstructions,
		DeveloperInstructions: bundle.DeveloperInstructions,
		UserPrompt:            bundle.UserPrompt,
	})
	if err != nil {
		log.Printf("run codex turn failed: guild=%s channel=%s message=%s err=%v", m.GuildID, m.ChannelID, m.ID, err)
		return
	}
	log.Printf("codex turn completed: message=%s guild=%s channel=%s author=%s status=%s tool_calls=%d", m.ID, m.GuildID, m.ChannelID, authorID, result.Status, len(result.ToolCalls))
	if strings.TrimSpace(result.AssistantText) != "" {
		log.Printf("assistant text: message=%s thread=%s turn=%s text=%q", m.ID, result.ThreadID, result.TurnID, result.AssistantText)
	}
	if strings.TrimSpace(result.ErrorMessage) != "" {
		log.Printf("codex turn error detail: message=%s err=%s", m.ID, result.ErrorMessage)
	}
}

func runHeartbeatTurn(ctx context.Context, cfg config.Config, runtime *codex.Client, memoryStore *memory.Store) error {
	dueTasks, err := memoryStore.ClaimDueTasks(ctx, time.Now().UTC(), 20)
	if err != nil {
		return err
	}
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
	bundle := prompt.BuildHeartbeatBundle(instructions, prompt.HeartbeatInput{
		DueTasks: taskItems,
	})

	result, err := runtime.RunTurn(ctx, codex.TurnInput{
		BaseInstructions:      bundle.BaseInstructions,
		DeveloperInstructions: bundle.DeveloperInstructions,
		UserPrompt:            bundle.UserPrompt,
	})
	if err != nil {
		return err
	}
	log.Printf("heartbeat turn completed: status=%s tool_calls=%d due_tasks=%d", result.Status, len(result.ToolCalls), len(dueTasks))
	if strings.TrimSpace(result.ErrorMessage) != "" {
		log.Printf("heartbeat turn error detail: %s", result.ErrorMessage)
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
