package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sigumaa/yururi/internal/config"
	"github.com/sigumaa/yururi/internal/discordx"
	"github.com/sigumaa/yururi/internal/xai"
)

type Server struct {
	bind            string
	defaultTimezone string
	toolPolicy      toolPolicy
	discord         *discordx.Gateway
	xai             *xai.Client
	mcpServer       *mcp.Server
	httpServer      *http.Server

	toolUsageMu     sync.Mutex
	toolUsageBySess map[string]*toolUsageState
}

var ErrToolDenied = errors.New("mcp tool denied by policy")
var ErrToolUsageLimited = errors.New("mcp tool blocked by usage limit")

type EmptyArgs struct{}

type ReadHistoryArgs struct {
	ChannelID       string `json:"channel_id" jsonschema:"対象チャンネルID"`
	BeforeMessageID string `json:"before_message_id,omitempty" jsonschema:"このメッセージより前を取得(任意)"`
	Limit           int    `json:"limit,omitempty" jsonschema:"取得件数。最大100"`
}

type ReadHistoryResult struct {
	Messages []HistoryMessage `json:"messages"`
}

type HistoryMessage struct {
	MessageID   string `json:"message_id"`
	ChannelID   string `json:"channel_id"`
	GuildID     string `json:"guild_id"`
	AuthorID    string `json:"author_id"`
	AuthorName  string `json:"author_name"`
	AuthorIsBot bool   `json:"author_is_bot"`
	Content     string `json:"content"`
	CreatedAt   string `json:"created_at"`
}

type SendMessageArgs struct {
	ChannelID string `json:"channel_id" jsonschema:"送信先チャンネルID"`
	Content   string `json:"content" jsonschema:"送信本文"`
}

type ReplyMessageArgs struct {
	ChannelID        string `json:"channel_id" jsonschema:"送信先チャンネルID"`
	ReplyToMessageID string `json:"reply_to_message_id" jsonschema:"返信対象メッセージID"`
	Content          string `json:"content" jsonschema:"返信本文"`
}

type MessageResult struct {
	MessageID  string `json:"message_id,omitempty"`
	Suppressed bool   `json:"suppressed,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

type AddReactionArgs struct {
	ChannelID string `json:"channel_id" jsonschema:"対象チャンネルID"`
	MessageID string `json:"message_id" jsonschema:"対象メッセージID"`
	Emoji     string `json:"emoji" jsonschema:"絵文字(Unicodeまたはカスタム絵文字)"`
}

type SimpleOK struct {
	OK bool `json:"ok"`
}

type StartTypingArgs struct {
	ChannelID   string `json:"channel_id" jsonschema:"対象チャンネルID"`
	DurationSec int    `json:"duration_sec,omitempty" jsonschema:"typing表示秒数。省略時10秒"`
}

type ListChannelsResult struct {
	Channels []ChannelItem `json:"channels"`
}

type ChannelItem struct {
	ChannelID string `json:"channel_id"`
	Name      string `json:"name"`
}

type UserDetailArgs struct {
	ChannelID string `json:"channel_id" jsonschema:"対象チャンネルID"`
	UserID    string `json:"user_id" jsonschema:"対象ユーザーID"`
}

type UserDetailResult struct {
	UserID      string `json:"user_id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Nick        string `json:"nick"`
}

type CurrentTimeArgs struct {
	Timezone string `json:"timezone,omitempty" jsonschema:"IANA timezone。省略時はデフォルト"`
}

type CurrentTimeResult struct {
	Timezone       string `json:"timezone"`
	CurrentUnix    int64  `json:"current_unix"`
	CurrentRFC3339 string `json:"current_rfc3339"`
}

type XSearchArgs struct {
	Query                    string   `json:"query" jsonschema:"検索クエリ"`
	AllowedXHandles          []string `json:"allowed_x_handles,omitempty" jsonschema:"検索対象に含めるXハンドル(任意)"`
	ExcludedXHandles         []string `json:"excluded_x_handles,omitempty" jsonschema:"検索対象から除外するXハンドル(任意)"`
	FromDate                 string   `json:"from_date,omitempty" jsonschema:"検索開始日(YYYY-MM-DD, 任意)"`
	ToDate                   string   `json:"to_date,omitempty" jsonschema:"検索終了日(YYYY-MM-DD, 任意)"`
	EnableImageUnderstanding bool     `json:"enable_image_understanding,omitempty" jsonschema:"画像理解を有効化(任意)"`
	EnableVideoUnderstanding bool     `json:"enable_video_understanding,omitempty" jsonschema:"動画理解を有効化(任意)"`
}

type XSearchResult struct {
	Text       string         `json:"text"`
	Citations  []xai.Citation `json:"citations,omitempty"`
	ResponseID string         `json:"response_id,omitempty"`
	Model      string         `json:"model,omitempty"`
}

const maxMCPToolLogValueLen = 280

const (
	defaultMaxToolCallsPerTurn   = 3
	defaultMaxSameArgsRetryCalls = 2
	toolUsageTurnIdleReset       = 8 * time.Second
)

type toolUsageState struct {
	turnToken   string
	lastCallAt  time.Time
	callCount   int
	argumentHit map[string]int
}

func New(bind string, defaultTimezone string, discord *discordx.Gateway, xaiClient *xai.Client, policyOverrides ...config.MCPToolPolicyConfig) (*Server, error) {
	bind = strings.TrimSpace(bind)
	if bind == "" {
		return nil, errors.New("mcp bind is required")
	}
	if discord == nil {
		return nil, errors.New("discord gateway is required")
	}
	if strings.TrimSpace(defaultTimezone) == "" {
		defaultTimezone = "Asia/Tokyo"
	}
	policyCfg := config.CurrentMCPToolPolicy()
	if len(policyOverrides) > 0 {
		policyCfg = policyOverrides[0]
	}

	m := mcp.NewServer(&mcp.Implementation{
		Name:    "yururi-discord",
		Version: "v0.1.0",
	}, nil)

	s := &Server{
		bind:            bind,
		defaultTimezone: defaultTimezone,
		toolPolicy:      newToolPolicy(policyCfg),
		discord:         discord,
		xai:             xaiClient,
		mcpServer:       m,
		toolUsageBySess: map[string]*toolUsageState{},
	}
	s.registerTools()

	handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		return s.mcpServer
	}, nil)
	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	s.httpServer = &http.Server{
		Addr:              bind,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s, nil
}

func (s *Server) URL() string {
	return "http://" + s.bind + "/mcp"
}

func (s *Server) Start(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.httpServer.Shutdown(shutdownCtx)
	}()

	go func() {
		if err := s.httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}

func logMCPToolStart(toolName string, args any) time.Time {
	started := time.Now()
	log.Printf("event=mcp_tool_started tool=%s args=%q", toolName, trimLogAny(args, maxMCPToolLogValueLen))
	return started
}

func logMCPToolFailed(toolName string, started time.Time, err error) {
	log.Printf("event=mcp_tool_failed tool=%s latency_ms=%d err=%v", toolName, durationMS(time.Since(started)), err)
}

func logMCPToolCompleted(toolName string, started time.Time, result any) {
	log.Printf("event=mcp_tool_completed tool=%s latency_ms=%d result=%q", toolName, durationMS(time.Since(started)), trimLogAny(result, maxMCPToolLogValueLen))
}

func (s *Server) registerTools() {
	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "read_message_history",
		Description: "Discordのメッセージ履歴を取得する",
	}, s.handleReadMessageHistory)

	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "send_message",
		Description: "Discordチャンネルにメッセージを送信する",
	}, s.handleSendMessage)

	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "reply_message",
		Description: "Discordメッセージに返信する",
	}, s.handleReplyMessage)

	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "add_reaction",
		Description: "Discordメッセージにリアクションする",
	}, s.handleAddReaction)

	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "start_typing",
		Description: "Discordのtyping表示を開始する",
	}, s.handleStartTyping)

	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "list_channels",
		Description: "対象チャンネル一覧を取得する",
	}, s.handleListChannels)

	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "get_user_detail",
		Description: "Discordユーザー詳細を取得する",
	}, s.handleGetUserDetail)

	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "get_current_time",
		Description: "現在時刻を取得する",
	}, s.handleGetCurrentTime)

	if s.xai != nil {
		mcp.AddTool(s.mcpServer, &mcp.Tool{
			Name:        "x_search",
			Description: "xAIのX SearchでX投稿を検索する",
		}, s.handleXSearch)
	}
}

func (s *Server) handleReadMessageHistory(ctx context.Context, req *mcp.CallToolRequest, args ReadHistoryArgs) (*mcp.CallToolResult, ReadHistoryResult, error) {
	started := logMCPToolStart("read_message_history", args)
	if err := s.enforceToolPolicy("read_message_history"); err != nil {
		logMCPToolFailed("read_message_history", started, err)
		return nil, ReadHistoryResult{}, err
	}
	if err := s.enforceToolUsage(req, "read_message_history", args); err != nil {
		logMCPToolFailed("read_message_history", started, err)
		return nil, ReadHistoryResult{}, err
	}
	messages, err := s.discord.ReadMessageHistory(ctx, args.ChannelID, args.BeforeMessageID, args.Limit)
	if err != nil {
		logMCPToolFailed("read_message_history", started, err)
		return nil, ReadHistoryResult{}, err
	}
	out := make([]HistoryMessage, 0, len(messages))
	for _, msg := range messages {
		out = append(out, HistoryMessage{
			MessageID:   msg.ID,
			ChannelID:   msg.ChannelID,
			GuildID:     msg.GuildID,
			AuthorID:    msg.AuthorID,
			AuthorName:  msg.AuthorName,
			AuthorIsBot: msg.AuthorIsBot,
			Content:     msg.Content,
			CreatedAt:   msg.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	result := ReadHistoryResult{Messages: out}
	logMCPToolCompleted("read_message_history", started, result)
	return nil, result, nil
}

func (s *Server) handleSendMessage(ctx context.Context, req *mcp.CallToolRequest, args SendMessageArgs) (*mcp.CallToolResult, MessageResult, error) {
	started := logMCPToolStart("send_message", args)
	if err := s.enforceToolPolicy("send_message"); err != nil {
		logMCPToolFailed("send_message", started, err)
		return nil, MessageResult{}, err
	}
	if err := s.enforceToolUsage(req, "send_message", args); err != nil {
		logMCPToolFailed("send_message", started, err)
		return nil, MessageResult{}, err
	}
	id, err := s.discord.SendMessage(ctx, args.ChannelID, args.Content)
	if err != nil {
		if discordx.IsDuplicateSuppressed(err) {
			result := MessageResult{
				Suppressed: true,
				Reason:     "duplicate_content",
			}
			log.Printf("event=message_duplicate_suppressed tool=send_message channel=%s", strings.TrimSpace(args.ChannelID))
			logMCPToolCompleted("send_message", started, result)
			return nil, result, nil
		}
		logMCPToolFailed("send_message", started, err)
		return nil, MessageResult{}, err
	}
	result := MessageResult{MessageID: id}
	logMCPToolCompleted("send_message", started, result)
	return nil, result, nil
}

func (s *Server) handleReplyMessage(ctx context.Context, req *mcp.CallToolRequest, args ReplyMessageArgs) (*mcp.CallToolResult, MessageResult, error) {
	started := logMCPToolStart("reply_message", args)
	if err := s.enforceToolPolicy("reply_message"); err != nil {
		logMCPToolFailed("reply_message", started, err)
		return nil, MessageResult{}, err
	}
	if err := s.enforceToolUsage(req, "reply_message", args); err != nil {
		logMCPToolFailed("reply_message", started, err)
		return nil, MessageResult{}, err
	}
	id, err := s.discord.ReplyMessage(ctx, args.ChannelID, args.ReplyToMessageID, args.Content)
	if err != nil {
		if discordx.IsDuplicateSuppressed(err) {
			result := MessageResult{
				Suppressed: true,
				Reason:     "duplicate_content",
			}
			log.Printf("event=message_duplicate_suppressed tool=reply_message channel=%s", strings.TrimSpace(args.ChannelID))
			logMCPToolCompleted("reply_message", started, result)
			return nil, result, nil
		}
		logMCPToolFailed("reply_message", started, err)
		return nil, MessageResult{}, err
	}
	result := MessageResult{MessageID: id}
	logMCPToolCompleted("reply_message", started, result)
	return nil, result, nil
}

func (s *Server) handleAddReaction(ctx context.Context, req *mcp.CallToolRequest, args AddReactionArgs) (*mcp.CallToolResult, SimpleOK, error) {
	started := logMCPToolStart("add_reaction", args)
	if err := s.enforceToolPolicy("add_reaction"); err != nil {
		logMCPToolFailed("add_reaction", started, err)
		return nil, SimpleOK{}, err
	}
	if err := s.enforceToolUsage(req, "add_reaction", args); err != nil {
		logMCPToolFailed("add_reaction", started, err)
		return nil, SimpleOK{}, err
	}
	if err := s.discord.AddReaction(ctx, args.ChannelID, args.MessageID, args.Emoji); err != nil {
		logMCPToolFailed("add_reaction", started, err)
		return nil, SimpleOK{}, err
	}
	result := SimpleOK{OK: true}
	logMCPToolCompleted("add_reaction", started, result)
	return nil, result, nil
}

func (s *Server) handleStartTyping(ctx context.Context, req *mcp.CallToolRequest, args StartTypingArgs) (*mcp.CallToolResult, SimpleOK, error) {
	started := logMCPToolStart("start_typing", args)
	if err := s.enforceToolPolicy("start_typing"); err != nil {
		logMCPToolFailed("start_typing", started, err)
		return nil, SimpleOK{}, err
	}
	if err := s.enforceToolUsage(req, "start_typing", args); err != nil {
		logMCPToolFailed("start_typing", started, err)
		return nil, SimpleOK{}, err
	}
	duration := 10 * time.Second
	if args.DurationSec > 0 {
		duration = time.Duration(args.DurationSec) * time.Second
	}
	s.discord.StartTyping(ctx, args.ChannelID, duration)
	result := SimpleOK{OK: true}
	logMCPToolCompleted("start_typing", started, result)
	return nil, result, nil
}

func (s *Server) handleListChannels(ctx context.Context, req *mcp.CallToolRequest, _ EmptyArgs) (*mcp.CallToolResult, ListChannelsResult, error) {
	started := logMCPToolStart("list_channels", EmptyArgs{})
	if err := s.enforceToolPolicy("list_channels"); err != nil {
		logMCPToolFailed("list_channels", started, err)
		return nil, ListChannelsResult{}, err
	}
	if err := s.enforceToolUsage(req, "list_channels", EmptyArgs{}); err != nil {
		logMCPToolFailed("list_channels", started, err)
		return nil, ListChannelsResult{}, err
	}
	channels, err := s.discord.ListChannels(ctx)
	if err != nil {
		logMCPToolFailed("list_channels", started, err)
		return nil, ListChannelsResult{}, err
	}
	out := make([]ChannelItem, 0, len(channels))
	for _, c := range channels {
		out = append(out, ChannelItem{ChannelID: c.ChannelID, Name: c.Name})
	}
	result := ListChannelsResult{Channels: out}
	logMCPToolCompleted("list_channels", started, result)
	return nil, result, nil
}

func (s *Server) handleGetUserDetail(ctx context.Context, req *mcp.CallToolRequest, args UserDetailArgs) (*mcp.CallToolResult, UserDetailResult, error) {
	started := logMCPToolStart("get_user_detail", args)
	if err := s.enforceToolPolicy("get_user_detail"); err != nil {
		logMCPToolFailed("get_user_detail", started, err)
		return nil, UserDetailResult{}, err
	}
	if err := s.enforceToolUsage(req, "get_user_detail", args); err != nil {
		logMCPToolFailed("get_user_detail", started, err)
		return nil, UserDetailResult{}, err
	}
	user, err := s.discord.GetUserDetail(ctx, args.ChannelID, args.UserID)
	if err != nil {
		logMCPToolFailed("get_user_detail", started, err)
		return nil, UserDetailResult{}, err
	}
	result := UserDetailResult{
		UserID:      user.UserID,
		Username:    user.Username,
		DisplayName: user.DisplayName,
		Nick:        user.Nick,
	}
	logMCPToolCompleted("get_user_detail", started, result)
	return nil, result, nil
}

func (s *Server) handleGetCurrentTime(_ context.Context, req *mcp.CallToolRequest, args CurrentTimeArgs) (*mcp.CallToolResult, CurrentTimeResult, error) {
	started := logMCPToolStart("get_current_time", args)
	if err := s.enforceToolPolicy("get_current_time"); err != nil {
		logMCPToolFailed("get_current_time", started, err)
		return nil, CurrentTimeResult{}, err
	}
	if err := s.enforceToolUsage(req, "get_current_time", args); err != nil {
		logMCPToolFailed("get_current_time", started, err)
		return nil, CurrentTimeResult{}, err
	}
	tz := strings.TrimSpace(args.Timezone)
	if tz == "" {
		tz = s.defaultTimezone
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		logMCPToolFailed("get_current_time", started, err)
		return nil, CurrentTimeResult{}, fmt.Errorf("invalid timezone %q: %w", tz, err)
	}
	now := time.Now().In(loc)
	result := CurrentTimeResult{
		Timezone:       loc.String(),
		CurrentUnix:    now.Unix(),
		CurrentRFC3339: now.Format(time.RFC3339),
	}
	logMCPToolCompleted("get_current_time", started, result)
	return nil, result, nil
}

func (s *Server) handleXSearch(ctx context.Context, req *mcp.CallToolRequest, args XSearchArgs) (*mcp.CallToolResult, XSearchResult, error) {
	started := logMCPToolStart("x_search", args)
	if err := s.enforceToolPolicy("x_search"); err != nil {
		logMCPToolFailed("x_search", started, err)
		return nil, XSearchResult{}, err
	}
	if err := s.enforceToolUsage(req, "x_search", args); err != nil {
		logMCPToolFailed("x_search", started, err)
		return nil, XSearchResult{}, err
	}
	if s.xai == nil {
		err := errors.New("x_search is disabled")
		logMCPToolFailed("x_search", started, err)
		return nil, XSearchResult{}, err
	}
	result, err := s.xai.Query(ctx, args.Query, xai.SearchOptions{
		AllowedXHandles:          args.AllowedXHandles,
		ExcludedXHandles:         args.ExcludedXHandles,
		FromDate:                 args.FromDate,
		ToDate:                   args.ToDate,
		EnableImageUnderstanding: args.EnableImageUnderstanding,
		EnableVideoUnderstanding: args.EnableVideoUnderstanding,
	})
	if err != nil {
		logMCPToolFailed("x_search", started, err)
		return nil, XSearchResult{}, err
	}
	out := XSearchResult{
		Text:       result.Text,
		Citations:  result.Citations,
		ResponseID: result.ResponseID,
		Model:      result.Model,
	}
	logMCPToolCompleted("x_search", started, out)
	return nil, out, nil
}

func (s *Server) enforceToolPolicy(toolName string) error {
	allowed, reason := s.toolPolicy.evaluate(toolName)
	if allowed {
		return nil
	}
	log.Printf("mcp tool denied: tool=%s reason=%q", toolName, reason)
	return fmt.Errorf("%w: tool=%s reason=%s", ErrToolDenied, toolName, reason)
}

func (s *Server) enforceToolUsage(req *mcp.CallToolRequest, toolName string, args any) error {
	now := time.Now().UTC()
	sessionKey := toolUsageSessionKey(req)
	turnToken := toolUsageTurnToken(req)
	argSignature := toolUsageArgumentSignature(toolName, req, args)

	s.toolUsageMu.Lock()
	defer s.toolUsageMu.Unlock()

	state, ok := s.toolUsageBySess[sessionKey]
	if !ok {
		state = &toolUsageState{}
		s.toolUsageBySess[sessionKey] = state
	}
	if shouldResetToolUsageState(state, turnToken, now) {
		state.callCount = 0
		state.argumentHit = map[string]int{}
		state.turnToken = turnToken
	}
	if state.argumentHit == nil {
		state.argumentHit = map[string]int{}
	}

	nextCallCount := state.callCount + 1
	if nextCallCount > defaultMaxToolCallsPerTurn {
		return fmt.Errorf(
			"%w: tool=%s reason=max_tool_calls_per_turn_exceeded limit=%d session=%s",
			ErrToolUsageLimited,
			toolName,
			defaultMaxToolCallsPerTurn,
			sessionKey,
		)
	}
	nextArgHit := state.argumentHit[argSignature] + 1
	if nextArgHit > defaultMaxSameArgsRetryCalls {
		return fmt.Errorf(
			"%w: tool=%s reason=same_arguments_retry_exceeded limit=%d session=%s",
			ErrToolUsageLimited,
			toolName,
			defaultMaxSameArgsRetryCalls,
			sessionKey,
		)
	}

	state.callCount = nextCallCount
	state.argumentHit[argSignature] = nextArgHit
	state.lastCallAt = now
	if turnToken != "" {
		state.turnToken = turnToken
	}
	return nil
}

func durationMS(d time.Duration) int64 {
	return d.Milliseconds()
}

func trimLogAny(value any, maxLen int) string {
	if value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return trimLogString(v, maxLen)
	}
	body, err := json.Marshal(value)
	if err == nil {
		return trimLogString(string(body), maxLen)
	}
	return trimLogString(fmt.Sprintf("%v", value), maxLen)
}

func trimLogString(text string, maxLen int) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || maxLen <= 0 {
		return trimmed
	}
	runes := []rune(trimmed)
	if len(runes) <= maxLen {
		return trimmed
	}
	if maxLen <= 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-3]) + "..."
}

type toolPolicy struct {
	allowPatterns []string
	denyPatterns  []string
}

func newToolPolicy(cfg config.MCPToolPolicyConfig) toolPolicy {
	return toolPolicy{
		allowPatterns: normalizeToolPatterns(cfg.AllowPatterns),
		denyPatterns:  normalizeToolPatterns(cfg.DenyPatterns),
	}
}

func (p toolPolicy) evaluate(toolName string) (bool, string) {
	name := strings.ToLower(strings.TrimSpace(toolName))
	if name == "" {
		return false, "tool name is empty"
	}
	for _, pattern := range p.denyPatterns {
		if matchToolPattern(pattern, name) {
			return false, fmt.Sprintf("matched deny pattern %q", pattern)
		}
	}
	if len(p.allowPatterns) == 0 {
		return true, "allowed by default"
	}
	for _, pattern := range p.allowPatterns {
		if matchToolPattern(pattern, name) {
			return true, fmt.Sprintf("matched allow pattern %q", pattern)
		}
	}
	return false, "not matched by allow patterns"
}

func normalizeToolPatterns(patterns []string) []string {
	out := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		trimmed := strings.ToLower(strings.TrimSpace(pattern))
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

func matchToolPattern(pattern string, value string) bool {
	p := strings.ToLower(strings.TrimSpace(pattern))
	v := strings.ToLower(strings.TrimSpace(value))
	if p == "" {
		return false
	}

	patternIndex := 0
	valueIndex := 0
	starPatternIndex := -1
	starValueIndex := 0

	for valueIndex < len(v) {
		if patternIndex < len(p) && p[patternIndex] == '*' {
			starPatternIndex = patternIndex
			starValueIndex = valueIndex
			patternIndex++
			continue
		}
		if patternIndex < len(p) && p[patternIndex] == v[valueIndex] {
			patternIndex++
			valueIndex++
			continue
		}
		if starPatternIndex == -1 {
			return false
		}
		patternIndex = starPatternIndex + 1
		starValueIndex++
		valueIndex = starValueIndex
	}
	for patternIndex < len(p) && p[patternIndex] == '*' {
		patternIndex++
	}
	return patternIndex == len(p)
}

func shouldResetToolUsageState(state *toolUsageState, turnToken string, now time.Time) bool {
	if state == nil || state.callCount == 0 {
		return true
	}
	if turnToken != "" {
		return !strings.EqualFold(strings.TrimSpace(state.turnToken), strings.TrimSpace(turnToken))
	}
	if state.lastCallAt.IsZero() {
		return true
	}
	return now.Sub(state.lastCallAt) > toolUsageTurnIdleReset
}

func toolUsageSessionKey(req *mcp.CallToolRequest) string {
	if req == nil || req.Session == nil {
		return "session:unknown"
	}
	id := strings.TrimSpace(req.Session.ID())
	if id == "" {
		return "session:unknown"
	}
	return "session:" + id
}

func toolUsageTurnToken(req *mcp.CallToolRequest) string {
	if req == nil || req.Params == nil {
		return ""
	}
	meta := map[string]any(req.Params.Meta)
	if len(meta) == 0 {
		return ""
	}
	for _, key := range []string{
		"turn_id",
		"turnId",
		"expected_turn_id",
		"expectedTurnId",
		"run_id",
		"runId",
		"request_id",
		"requestId",
		"progress_token",
		"progressToken",
	} {
		raw, ok := meta[key]
		if !ok {
			continue
		}
		if token := strings.TrimSpace(fmt.Sprint(raw)); token != "" {
			return token
		}
	}
	return ""
}

func toolUsageArgumentSignature(toolName string, req *mcp.CallToolRequest, args any) string {
	payload := "{}"
	if req != nil && req.Params != nil && len(req.Params.Arguments) > 0 {
		normalized := normalizeJSONRaw(req.Params.Arguments)
		if normalized != "" {
			payload = normalized
		}
	} else if args != nil {
		body, err := json.Marshal(args)
		if err == nil {
			payload = normalizeJSONRaw(body)
		}
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(strings.ToLower(toolName)) + "|" + payload))
	return hex.EncodeToString(sum[:])
}

func normalizeJSONRaw(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return strings.TrimSpace(string(raw))
	}
	body, err := json.Marshal(decoded)
	if err != nil {
		return strings.TrimSpace(string(raw))
	}
	return string(body)
}
