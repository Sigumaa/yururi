package discordx

import (
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/sigumaa/yururi/internal/config"
)

func TestAuthorDisplayName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		msg  *discordgo.Message
		want string
	}{
		{
			name: "nil message",
			msg:  nil,
			want: "unknown",
		},
		{
			name: "nil author",
			msg:  &discordgo.Message{},
			want: "unknown",
		},
		{
			name: "member nick preferred",
			msg: &discordgo.Message{
				Author: &discordgo.User{ID: "u1", Username: "user", GlobalName: "global"},
				Member: &discordgo.Member{Nick: "nick"},
			},
			want: "nick",
		},
		{
			name: "member nil uses global name",
			msg: &discordgo.Message{
				Author: &discordgo.User{ID: "u1", Username: "user", GlobalName: "global"},
			},
			want: "global",
		},
		{
			name: "fallback username",
			msg: &discordgo.Message{
				Author: &discordgo.User{ID: "u1", Username: "user"},
			},
			want: "user",
		},
		{
			name: "fallback id",
			msg: &discordgo.Message{
				Author: &discordgo.User{ID: "u1"},
			},
			want: "u1",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := authorDisplayName(tc.msg)
			if got != tc.want {
				t.Fatalf("authorDisplayName() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGatewayChannelValidation(t *testing.T) {
	t.Parallel()

	gateway := NewGateway(nil, config.DiscordConfig{
		GuildID:           "g1",
		TargetChannelIDs:  []string{"c-target"},
		ObserveChannelIDs: []string{"c-observe"},
	})

	if err := gateway.validateReadableChannel("c-target"); err != nil {
		t.Fatalf("validateReadableChannel(target) error = %v", err)
	}
	if err := gateway.validateReadableChannel("c-observe"); err != nil {
		t.Fatalf("validateReadableChannel(observe) error = %v", err)
	}
	if err := gateway.validateWritableChannel("c-target"); err != nil {
		t.Fatalf("validateWritableChannel(target) error = %v", err)
	}
	if err := gateway.validateWritableChannel("c-observe"); err == nil {
		t.Fatal("validateWritableChannel(observe) error = nil, want error")
	}
}
