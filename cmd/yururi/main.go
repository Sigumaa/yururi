package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/sigumaa/yururi/internal/codex"
	"github.com/sigumaa/yururi/internal/config"
	"github.com/sigumaa/yururi/internal/policy"
)

func main() {
	configPath := flag.String("config", "runtime/config.yaml", "path to config yaml")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	runtime := codex.NewClient(cfg.Codex)

	discord, err := discordgo.New("Bot " + cfg.Discord.Token)
	if err != nil {
		log.Fatalf("create discord session: %v", err)
	}
	discord.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsMessageContent

	discord.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		authorID := ""
		authorIsBot := false
		if m.Author != nil {
			authorID = m.Author.ID
			authorIsBot = m.Author.Bot
		}

		incoming := policy.Incoming{
			GuildID:     m.GuildID,
			ChannelID:   m.ChannelID,
			AuthorID:    authorID,
			AuthorIsBot: authorIsBot,
			WebhookID:   m.WebhookID,
		}
		if !policy.ShouldProcess(cfg.Discord, incoming) {
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		decision, err := runtime.RunTurn(ctx, codex.TurnInput{
			AuthorID: authorID,
			Content:  m.Content,
			IsOwner:  authorID != "" && authorID == cfg.Persona.OwnerUserID,
		})
		if err != nil {
			log.Printf("run codex turn failed: guild=%s channel=%s message=%s err=%v", m.GuildID, m.ChannelID, m.ID, err)
			return
		}

		if decision.Action != "reply" {
			return
		}

		_, err = s.ChannelMessageSendComplex(m.ChannelID, &discordgo.MessageSend{
			Content: decision.Content,
			Reference: &discordgo.MessageReference{
				GuildID:   m.GuildID,
				ChannelID: m.ChannelID,
				MessageID: m.ID,
			},
		})
		if err != nil {
			log.Printf("discord reply failed: guild=%s channel=%s message=%s err=%v", m.GuildID, m.ChannelID, m.ID, err)
		}
	})

	if err := discord.Open(); err != nil {
		log.Fatalf("open discord session: %v", err)
	}
	defer discord.Close()

	log.Printf("yururi started")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Printf("yururi stopped")
}
