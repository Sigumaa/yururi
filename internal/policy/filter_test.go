package policy

import (
	"testing"

	"github.com/sigumaa/yururi/internal/config"
)

func TestShouldProcess(t *testing.T) {
	t.Parallel()

	cfg := config.DiscordConfig{
		GuildID:            "guild-1",
		TargetChannelIDs:   []string{"chan-a", "chan-b"},
		ExcludedChannelIDs: []string{"chan-b"},
		AllowedBotUserIDs:  []string{"bot-allowed"},
	}

	tests := []struct {
		name string
		msg  Incoming
		want bool
	}{
		{
			name: "human message in target channel",
			msg: Incoming{
				GuildID:   "guild-1",
				ChannelID: "chan-a",
				AuthorID:  "user-1",
			},
			want: true,
		},
		{
			name: "wrong guild",
			msg: Incoming{
				GuildID:   "guild-2",
				ChannelID: "chan-a",
				AuthorID:  "user-1",
			},
			want: false,
		},
		{
			name: "channel not targeted",
			msg: Incoming{
				GuildID:   "guild-1",
				ChannelID: "chan-x",
				AuthorID:  "user-1",
			},
			want: false,
		},
		{
			name: "channel excluded",
			msg: Incoming{
				GuildID:   "guild-1",
				ChannelID: "chan-b",
				AuthorID:  "user-1",
			},
			want: false,
		},
		{
			name: "bot not allowed",
			msg: Incoming{
				GuildID:     "guild-1",
				ChannelID:   "chan-a",
				AuthorID:    "bot-denied",
				AuthorIsBot: true,
			},
			want: false,
		},
		{
			name: "bot allowed",
			msg: Incoming{
				GuildID:     "guild-1",
				ChannelID:   "chan-a",
				AuthorID:    "bot-allowed",
				AuthorIsBot: true,
			},
			want: true,
		},
		{
			name: "webhook allowed bot id",
			msg: Incoming{
				GuildID:   "guild-1",
				ChannelID: "chan-a",
				AuthorID:  "bot-allowed",
				WebhookID: "webhook-1",
			},
			want: true,
		},
		{
			name: "webhook denied bot id",
			msg: Incoming{
				GuildID:   "guild-1",
				ChannelID: "chan-a",
				AuthorID:  "bot-denied",
				WebhookID: "webhook-1",
			},
			want: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ShouldProcess(cfg, tc.msg); got != tc.want {
				t.Fatalf("ShouldProcess() = %v, want %v", got, tc.want)
			}
		})
	}
}
