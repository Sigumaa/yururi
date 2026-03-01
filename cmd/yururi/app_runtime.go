package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os/signal"
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
	"github.com/sigumaa/yururi/internal/prompt"
	"github.com/sigumaa/yururi/internal/xai"
)

func runApplication(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if err := prompt.EnsureWorkspaceInstructionFiles(cfg.Codex.WorkspaceDir); err != nil {
		return fmt.Errorf("prepare workspace instruction files: %w", err)
	}

	discord, err := discordgo.New("Bot " + cfg.Discord.Token)
	if err != nil {
		return fmt.Errorf("create discord session: %w", err)
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
		return fmt.Errorf("create mcp server: %w", err)
	}
	aiClient := codex.NewClient(cfg.Codex, cfg.MCP.URL)
	coordinator := orchestrator.New(aiClient)

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

	discord.AddHandler(func(_ *discordgo.Session, m *discordgo.MessageCreate) {
		if dropped := dispatcher.Enqueue(m); dropped {
			log.Printf("dispatcher queue drop occurred: guild=%s channel=%s latest_message=%s", m.GuildID, m.ChannelID, m.ID)
		}
	})

	if err := discord.Open(); err != nil {
		return fmt.Errorf("open discord session: %w", err)
	}

	if cfg.Heartbeat.Enabled {
		runner, err := heartbeat.NewRunner(cfg.Heartbeat.Cron, cfg.Heartbeat.Timezone, func(runCtx context.Context) error {
			return runHeartbeatTurn(runCtx, cfg, aiClient, nextRunID(&runSeq, "hb"))
		})
		if err != nil {
			return fmt.Errorf("init heartbeat runner: %w", err)
		}
		runner.Start(ctx)
	}
	if cfg.Autonomy.Enabled {
		runner, err := heartbeat.NewRunner(cfg.Autonomy.Cron, cfg.Autonomy.Timezone, func(runCtx context.Context) error {
			return runAutonomyTurn(runCtx, cfg, aiClient, gateway, nextRunID(&runSeq, "auto"))
		})
		if err != nil {
			return fmt.Errorf("init autonomy runner: %w", err)
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
	return nil
}
