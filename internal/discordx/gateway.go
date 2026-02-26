package discordx

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/sigumaa/yururi/internal/config"
)

const (
	defaultHistoryLimit = 20
	maxHistoryLimit     = 100
)

type Message struct {
	ID          string
	ChannelID   string
	GuildID     string
	AuthorID    string
	AuthorName  string
	AuthorIsBot bool
	Content     string
	CreatedAt   time.Time
}

type ChannelInfo struct {
	ChannelID string
	Name      string
}

type UserDetail struct {
	UserID      string
	Username    string
	DisplayName string
	Nick        string
}

type Gateway struct {
	session         *discordgo.Session
	guildID         string
	targetChannels  map[string]struct{}
	excludedChannel map[string]struct{}

	typingMu    sync.Mutex
	typingStops map[string]context.CancelFunc
}

func NewGateway(session *discordgo.Session, cfg config.DiscordConfig) *Gateway {
	target := make(map[string]struct{}, len(cfg.TargetChannelIDs))
	for _, id := range cfg.TargetChannelIDs {
		target[strings.TrimSpace(id)] = struct{}{}
	}
	excluded := make(map[string]struct{}, len(cfg.ExcludedChannelIDs))
	for _, id := range cfg.ExcludedChannelIDs {
		excluded[strings.TrimSpace(id)] = struct{}{}
	}

	return &Gateway{
		session:         session,
		guildID:         strings.TrimSpace(cfg.GuildID),
		targetChannels:  target,
		excludedChannel: excluded,
		typingStops:     map[string]context.CancelFunc{},
	}
}

func (g *Gateway) ReadMessageHistory(ctx context.Context, channelID string, beforeMessageID string, limit int) ([]Message, error) {
	if err := g.validateChannel(channelID); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = defaultHistoryLimit
	}
	if limit > maxHistoryLimit {
		limit = maxHistoryLimit
	}

	history, err := g.session.ChannelMessages(channelID, limit, beforeMessageID, "", "")
	if err != nil {
		return nil, fmt.Errorf("fetch channel history: %w", err)
	}

	out := make([]Message, 0, len(history))
	for _, msg := range history {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		if msg == nil || msg.Author == nil {
			continue
		}
		content := strings.TrimSpace(msg.Content)
		if len(msg.Attachments) > 0 {
			attachmentNames := make([]string, 0, len(msg.Attachments))
			for _, a := range msg.Attachments {
				if a == nil || strings.TrimSpace(a.URL) == "" {
					continue
				}
				name := strings.TrimSpace(a.Filename)
				if name == "" {
					name = "attachment"
				}
				attachmentNames = append(attachmentNames, fmt.Sprintf("%s(%s)", name, a.URL))
			}
			if len(attachmentNames) > 0 {
				if content != "" {
					content += "\n"
				}
				content += "attachments: " + strings.Join(attachmentNames, ", ")
			}
		}
		out = append(out, Message{
			ID:          msg.ID,
			ChannelID:   msg.ChannelID,
			GuildID:     msg.GuildID,
			AuthorID:    msg.Author.ID,
			AuthorName:  authorDisplayName(msg),
			AuthorIsBot: msg.Author.Bot,
			Content:     content,
			CreatedAt:   msg.Timestamp,
		})
	}

	return out, nil
}

func (g *Gateway) SendMessage(ctx context.Context, channelID string, content string) (string, error) {
	if err := g.validateChannel(channelID); err != nil {
		return "", err
	}
	text := strings.TrimSpace(content)
	if text == "" {
		return "", errors.New("content is required")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	msg, err := g.session.ChannelMessageSend(channelID, text)
	if err != nil {
		return "", fmt.Errorf("send message: %w", err)
	}
	return msg.ID, nil
}

func (g *Gateway) ReplyMessage(ctx context.Context, channelID string, replyToMessageID string, content string) (string, error) {
	if err := g.validateChannel(channelID); err != nil {
		return "", err
	}
	if strings.TrimSpace(replyToMessageID) == "" {
		return "", errors.New("reply_to_message_id is required")
	}
	text := strings.TrimSpace(content)
	if text == "" {
		return "", errors.New("content is required")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	msg, err := g.session.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Content: text,
		Reference: &discordgo.MessageReference{
			GuildID:   g.guildID,
			ChannelID: channelID,
			MessageID: replyToMessageID,
		},
	})
	if err != nil {
		return "", fmt.Errorf("send reply: %w", err)
	}
	return msg.ID, nil
}

func (g *Gateway) AddReaction(ctx context.Context, channelID string, messageID string, emoji string) error {
	if err := g.validateChannel(channelID); err != nil {
		return err
	}
	if strings.TrimSpace(messageID) == "" {
		return errors.New("message_id is required")
	}
	if strings.TrimSpace(emoji) == "" {
		return errors.New("emoji is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := g.session.MessageReactionAdd(channelID, messageID, emoji); err != nil {
		return fmt.Errorf("add reaction: %w", err)
	}
	return nil
}

func (g *Gateway) StartTyping(_ context.Context, channelID string, duration time.Duration) {
	if duration <= 0 {
		duration = 10 * time.Second
	}
	if duration > 2*time.Minute {
		duration = 2 * time.Minute
	}

	g.typingMu.Lock()
	if stop, ok := g.typingStops[channelID]; ok {
		stop()
	}
	typingCtx, cancel := context.WithCancel(context.Background())
	g.typingStops[channelID] = cancel
	g.typingMu.Unlock()

	go func() {
		defer func() {
			g.typingMu.Lock()
			delete(g.typingStops, channelID)
			g.typingMu.Unlock()
		}()

		ticker := time.NewTicker(8 * time.Second)
		defer ticker.Stop()

		deadline := time.NewTimer(duration)
		defer deadline.Stop()

		_ = g.session.ChannelTyping(channelID)
		for {
			select {
			case <-typingCtx.Done():
				return
			case <-deadline.C:
				return
			case <-ticker.C:
				_ = g.session.ChannelTyping(channelID)
			}
		}
	}()
}

func (g *Gateway) ListChannels(ctx context.Context) ([]ChannelInfo, error) {
	ids := make([]string, 0, len(g.targetChannels))
	for channelID := range g.targetChannels {
		if _, excluded := g.excludedChannel[channelID]; excluded {
			continue
		}
		ids = append(ids, channelID)
	}
	sortStrings(ids)
	out := make([]ChannelInfo, 0, len(ids))
	for _, channelID := range ids {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		ch, err := g.session.Channel(channelID)
		if err != nil {
			out = append(out, ChannelInfo{ChannelID: channelID, Name: "unknown"})
			continue
		}
		out = append(out, ChannelInfo{ChannelID: channelID, Name: ch.Name})
	}
	return out, nil
}

func (g *Gateway) GetUserDetail(ctx context.Context, channelID string, userID string) (UserDetail, error) {
	if err := g.validateChannel(channelID); err != nil {
		return UserDetail{}, err
	}
	if strings.TrimSpace(userID) == "" {
		return UserDetail{}, errors.New("user_id is required")
	}
	if err := ctx.Err(); err != nil {
		return UserDetail{}, err
	}

	member, err := g.session.GuildMember(g.guildID, userID)
	if err != nil {
		return UserDetail{}, fmt.Errorf("fetch member: %w", err)
	}
	if member.User == nil {
		return UserDetail{}, errors.New("member user is nil")
	}

	return UserDetail{
		UserID:      member.User.ID,
		Username:    member.User.Username,
		DisplayName: member.User.GlobalName,
		Nick:        member.Nick,
	}, nil
}

func (g *Gateway) validateChannel(channelID string) error {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return errors.New("channel_id is required")
	}
	if _, excluded := g.excludedChannel[channelID]; excluded {
		return fmt.Errorf("channel %s is excluded", channelID)
	}
	if _, ok := g.targetChannels[channelID]; !ok {
		return fmt.Errorf("channel %s is not in target_channel_ids", channelID)
	}
	return nil
}

func authorDisplayName(msg *discordgo.Message) string {
	if msg == nil || msg.Author == nil {
		return "unknown"
	}
	if msg.Member != nil && strings.TrimSpace(msg.Member.Nick) != "" {
		return msg.Member.Nick
	}
	if strings.TrimSpace(msg.Author.GlobalName) != "" {
		return msg.Author.GlobalName
	}
	if strings.TrimSpace(msg.Author.Username) != "" {
		return msg.Author.Username
	}
	return msg.Author.ID
}

func sortStrings(values []string) {
	if len(values) < 2 {
		return
	}
	for i := 0; i < len(values)-1; i++ {
		for j := i + 1; j < len(values); j++ {
			if values[j] < values[i] {
				values[i], values[j] = values[j], values[i]
			}
		}
	}
}
