package main

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/sigumaa/yururi/internal/config"
)

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
