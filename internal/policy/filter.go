package policy

import "github.com/sigumaa/yururi/internal/config"

type Incoming struct {
	GuildID     string
	ChannelID   string
	AuthorID    string
	AuthorIsBot bool
	WebhookID   string
}

func ShouldProcess(discordCfg config.DiscordConfig, msg Incoming) bool {
	if msg.GuildID == "" || msg.ChannelID == "" {
		return false
	}
	if msg.GuildID != discordCfg.GuildID {
		return false
	}
	if !contains(discordCfg.TargetChannelIDs, msg.ChannelID) {
		return false
	}
	if contains(discordCfg.ExcludedChannelIDs, msg.ChannelID) {
		return false
	}
	if msg.AuthorID == "" {
		return false
	}
	if msg.AuthorIsBot || msg.WebhookID != "" {
		return contains(discordCfg.AllowedBotUserIDs, msg.AuthorID)
	}
	return true
}

func contains(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}
