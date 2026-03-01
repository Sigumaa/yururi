package policy

import "github.com/sigumaa/yururi/internal/config"

type Incoming struct {
	GuildID     string
	ChannelID   string
	AuthorID    string
	AuthorIsBot bool
	WebhookID   string
}

func Evaluate(discordCfg config.DiscordConfig, msg Incoming) (bool, string) {
	if msg.GuildID == "" || msg.ChannelID == "" {
		return false, "missing_guild_or_channel"
	}
	if msg.GuildID != discordCfg.GuildID {
		return false, "guild_not_allowed"
	}
	if !contains(discordCfg.ReadChannelIDs, msg.ChannelID) {
		return false, "channel_not_readable"
	}
	if contains(discordCfg.ExcludedChannelIDs, msg.ChannelID) {
		return false, "channel_excluded"
	}
	if msg.AuthorID == "" {
		return false, "missing_author"
	}
	if msg.AuthorIsBot || msg.WebhookID != "" {
		if !contains(discordCfg.AllowedBotUserIDs, msg.AuthorID) {
			return false, "bot_or_webhook_not_allowed"
		}
	}
	return true, "allowed"
}

func ShouldProcess(discordCfg config.DiscordConfig, msg Incoming) bool {
	allowed, _ := Evaluate(discordCfg, msg)
	return allowed
}

func contains(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}
