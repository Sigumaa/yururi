package discordx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	duplicateWindow     = 10 * time.Minute
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
	session          *discordgo.Session
	guildID          string
	writableChannels map[string]struct{}
	readableChannels map[string]struct{}
	excludedChannel  map[string]struct{}

	typingMu    sync.Mutex
	typingStops map[string]context.CancelFunc

	dedupMu          sync.Mutex
	recentContentMap map[string]map[string]time.Time
}

type DuplicateSuppressedError struct {
	ChannelID string
}

func (e *DuplicateSuppressedError) Error() string {
	return fmt.Sprintf("duplicate message suppressed in channel %s", strings.TrimSpace(e.ChannelID))
}

func IsDuplicateSuppressed(err error) bool {
	var target *DuplicateSuppressedError
	return errors.As(err, &target)
}

func NewGateway(session *discordgo.Session, cfg config.DiscordConfig) *Gateway {
	writable := make(map[string]struct{}, len(cfg.WriteChannelIDs))
	readable := make(map[string]struct{}, len(cfg.ReadChannelIDs)+len(cfg.ObserveChannelIDs))
	for _, id := range cfg.ReadChannelIDs {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			continue
		}
		readable[trimmed] = struct{}{}
	}
	for _, id := range cfg.WriteChannelIDs {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			continue
		}
		writable[trimmed] = struct{}{}
	}
	for _, id := range cfg.ObserveChannelIDs {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			continue
		}
		readable[trimmed] = struct{}{}
	}
	excluded := make(map[string]struct{}, len(cfg.ExcludedChannelIDs))
	for _, id := range cfg.ExcludedChannelIDs {
		excluded[strings.TrimSpace(id)] = struct{}{}
	}

	return &Gateway{
		session:          session,
		guildID:          strings.TrimSpace(cfg.GuildID),
		writableChannels: writable,
		readableChannels: readable,
		excludedChannel:  excluded,
		typingStops:      map[string]context.CancelFunc{},
		recentContentMap: map[string]map[string]time.Time{},
	}
}

func (g *Gateway) ReadMessageHistory(ctx context.Context, channelID string, beforeMessageID string, limit int) ([]Message, error) {
	if err := g.validateReadableChannel(channelID); err != nil {
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
	if err := g.validateWritableChannel(channelID); err != nil {
		return "", err
	}
	text := strings.TrimSpace(content)
	if text == "" {
		return "", errors.New("content is required")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if g.isDuplicateContent(channelID, text) {
		return "", &DuplicateSuppressedError{ChannelID: channelID}
	}
	msg, err := g.session.ChannelMessageSendComplex(channelID, buildMessageSend(text))
	if err != nil {
		return "", fmt.Errorf("send message: %w", err)
	}
	g.rememberContent(channelID, text)
	return msg.ID, nil
}

func (g *Gateway) ReplyMessage(ctx context.Context, channelID string, replyToMessageID string, content string) (string, error) {
	if err := g.validateWritableChannel(channelID); err != nil {
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
	if g.isDuplicateContent(channelID, text) {
		return "", &DuplicateSuppressedError{ChannelID: channelID}
	}
	msg, err := g.session.ChannelMessageSendComplex(channelID, buildReplyMessageSend(g.guildID, channelID, replyToMessageID, text))
	if err != nil {
		return "", fmt.Errorf("send reply: %w", err)
	}
	g.rememberContent(channelID, text)
	return msg.ID, nil
}

func buildMessageSend(content string) *discordgo.MessageSend {
	return &discordgo.MessageSend{
		Content: content,
		Flags:   discordgo.MessageFlagsSuppressEmbeds,
	}
}

func buildReplyMessageSend(guildID string, channelID string, replyToMessageID string, content string) *discordgo.MessageSend {
	msg := buildMessageSend(content)
	msg.Reference = &discordgo.MessageReference{
		GuildID:   guildID,
		ChannelID: channelID,
		MessageID: replyToMessageID,
	}
	return msg
}

func (g *Gateway) AddReaction(ctx context.Context, channelID string, messageID string, emoji string) error {
	if err := g.validateWritableChannel(channelID); err != nil {
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
	ids := make([]string, 0, len(g.readableChannels))
	for channelID := range g.readableChannels {
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
	if err := g.validateReadableChannel(channelID); err != nil {
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

func (g *Gateway) validateWritableChannel(channelID string) error {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return errors.New("channel_id is required")
	}
	if _, excluded := g.excludedChannel[channelID]; excluded {
		return fmt.Errorf("channel %s is excluded", channelID)
	}
	if _, ok := g.writableChannels[channelID]; !ok {
		return fmt.Errorf("channel %s is not in write_channel_ids", channelID)
	}
	return nil
}

func (g *Gateway) validateReadableChannel(channelID string) error {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return errors.New("channel_id is required")
	}
	if _, excluded := g.excludedChannel[channelID]; excluded {
		return fmt.Errorf("channel %s is excluded", channelID)
	}
	if _, ok := g.readableChannels[channelID]; !ok {
		return fmt.Errorf("channel %s is not in read_channel_ids or observe_channel_ids", channelID)
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

func (g *Gateway) isDuplicateContent(channelID string, content string) bool {
	channelID = strings.TrimSpace(channelID)
	signature := dedupSignature(content)
	if channelID == "" || signature == "" {
		return false
	}
	now := time.Now().UTC()
	threshold := now.Add(-duplicateWindow)

	g.dedupMu.Lock()
	defer g.dedupMu.Unlock()

	channelMap, ok := g.recentContentMap[channelID]
	if !ok {
		return false
	}
	for hash, ts := range channelMap {
		if ts.Before(threshold) {
			delete(channelMap, hash)
		}
	}
	if len(channelMap) == 0 {
		delete(g.recentContentMap, channelID)
		return false
	}
	last, ok := channelMap[signature]
	if !ok {
		return false
	}
	return !last.Before(threshold)
}

func (g *Gateway) rememberContent(channelID string, content string) {
	channelID = strings.TrimSpace(channelID)
	signature := dedupSignature(content)
	if channelID == "" || signature == "" {
		return
	}
	now := time.Now().UTC()

	g.dedupMu.Lock()
	defer g.dedupMu.Unlock()

	channelMap, ok := g.recentContentMap[channelID]
	if !ok {
		channelMap = map[string]time.Time{}
		g.recentContentMap[channelID] = channelMap
	}
	channelMap[signature] = now
}

func dedupSignature(content string) string {
	normalized := strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(content)), " "))
	if normalized == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}
