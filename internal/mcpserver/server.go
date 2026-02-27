package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sigumaa/yururi/internal/config"
	"github.com/sigumaa/yururi/internal/discordx"
	"github.com/sigumaa/yururi/internal/memory"
)

type Server struct {
	bind            string
	defaultTimezone string
	workspaceDir    string
	toolPolicy      toolPolicy
	discord         *discordx.Gateway
	memory          *memory.Store
	mcpServer       *mcp.Server
	httpServer      *http.Server
	docMu           sync.Mutex
}

var ErrToolDenied = errors.New("mcp tool denied by policy")

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
	MessageID string `json:"message_id"`
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

type MemoryUpsertUserNoteArgs struct {
	UserID string `json:"user_id" jsonschema:"対象ユーザーID"`
	Note   string `json:"note" jsonschema:"記録する内容"`
	Source string `json:"source,omitempty" jsonschema:"情報源(任意)"`
}

type MemoryUpsertChannelIntentArgs struct {
	ChannelID string `json:"channel_id" jsonschema:"対象チャンネルID"`
	Intent    string `json:"intent" jsonschema:"チャンネル趣旨"`
	Policy    string `json:"policy,omitempty" jsonschema:"応答ポリシー(任意)"`
}

type MemoryUpsertTaskArgs struct {
	TaskID       string `json:"task_id" jsonschema:"タスクID"`
	Title        string `json:"title" jsonschema:"タスク名"`
	Instructions string `json:"instructions" jsonschema:"タスク内容"`
	ChannelID    string `json:"channel_id,omitempty" jsonschema:"投稿先チャンネルID(任意)"`
	Schedule     string `json:"schedule,omitempty" jsonschema:"daily/hourly/every 6hなど"`
	NextRunAt    string `json:"next_run_at,omitempty" jsonschema:"次回実行時刻(RFC3339)"`
	Status       string `json:"status,omitempty" jsonschema:"active/doneなど"`
}

type MemoryQueryArgs struct {
	Keyword string `json:"keyword" jsonschema:"検索語"`
	Limit   int    `json:"limit,omitempty" jsonschema:"最大件数"`
}

type MemoryPathResult struct {
	Path string `json:"path"`
}

type MemoryTaskResult struct {
	TaskID    string `json:"task_id"`
	Status    string `json:"status"`
	NextRunAt string `json:"next_run_at,omitempty"`
}

type MemoryQueryResult struct {
	Matches []MemoryMatch `json:"matches"`
}

type MemoryMatch struct {
	Path    string `json:"path"`
	Excerpt string `json:"excerpt"`
}

type WorkspaceDocArgs struct {
	Name string `json:"name" jsonschema:"YURURI.md/SOUL.md/MEMORY.md/HEARTBEAT.md"`
}

type WorkspaceDocWriteArgs struct {
	Name    string `json:"name" jsonschema:"YURURI.md/SOUL.md/MEMORY.md/HEARTBEAT.md"`
	Content string `json:"content" jsonschema:"書き込む内容"`
}

type WorkspaceDocResult struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Content string `json:"content"`
}

func New(bind string, defaultTimezone string, workspaceDir string, discord *discordx.Gateway, store *memory.Store, policyOverrides ...config.MCPToolPolicyConfig) (*Server, error) {
	bind = strings.TrimSpace(bind)
	if bind == "" {
		return nil, errors.New("mcp bind is required")
	}
	workspaceDir = strings.TrimSpace(workspaceDir)
	if workspaceDir == "" {
		return nil, errors.New("workspace dir is required")
	}
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		return nil, fmt.Errorf("create workspace dir: %w", err)
	}
	if discord == nil {
		return nil, errors.New("discord gateway is required")
	}
	if store == nil {
		return nil, errors.New("memory store is required")
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
		workspaceDir:    filepath.Clean(workspaceDir),
		toolPolicy:      newToolPolicy(policyCfg),
		discord:         discord,
		memory:          store,
		mcpServer:       m,
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

	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "memory_upsert_user_note",
		Description: "ユーザーに関する記憶を更新する",
	}, s.handleMemoryUpsertUserNote)

	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "memory_upsert_channel_intent",
		Description: "チャンネルの趣旨メモを更新する",
	}, s.handleMemoryUpsertChannelIntent)

	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "memory_upsert_task",
		Description: "定期タスク/単発タスクを登録または更新する",
	}, s.handleMemoryUpsertTask)

	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "memory_query",
		Description: "永続メモをキーワード検索する",
	}, s.handleMemoryQuery)

	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "read_workspace_doc",
		Description: "ワークスペースの4軸ドキュメントを読み取る",
	}, s.handleReadWorkspaceDoc)

	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "append_workspace_doc",
		Description: "ワークスペースの4軸ドキュメントへ追記する",
	}, s.handleAppendWorkspaceDoc)

	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "replace_workspace_doc",
		Description: "ワークスペースの4軸ドキュメントを置換する",
	}, s.handleReplaceWorkspaceDoc)
}

func (s *Server) handleReadMessageHistory(ctx context.Context, _ *mcp.CallToolRequest, args ReadHistoryArgs) (*mcp.CallToolResult, ReadHistoryResult, error) {
	if err := s.enforceToolPolicy("read_message_history"); err != nil {
		return nil, ReadHistoryResult{}, err
	}
	messages, err := s.discord.ReadMessageHistory(ctx, args.ChannelID, args.BeforeMessageID, args.Limit)
	if err != nil {
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
	return nil, ReadHistoryResult{Messages: out}, nil
}

func (s *Server) handleSendMessage(ctx context.Context, _ *mcp.CallToolRequest, args SendMessageArgs) (*mcp.CallToolResult, MessageResult, error) {
	if err := s.enforceToolPolicy("send_message"); err != nil {
		return nil, MessageResult{}, err
	}
	id, err := s.discord.SendMessage(ctx, args.ChannelID, args.Content)
	if err != nil {
		return nil, MessageResult{}, err
	}
	return nil, MessageResult{MessageID: id}, nil
}

func (s *Server) handleReplyMessage(ctx context.Context, _ *mcp.CallToolRequest, args ReplyMessageArgs) (*mcp.CallToolResult, MessageResult, error) {
	if err := s.enforceToolPolicy("reply_message"); err != nil {
		return nil, MessageResult{}, err
	}
	id, err := s.discord.ReplyMessage(ctx, args.ChannelID, args.ReplyToMessageID, args.Content)
	if err != nil {
		return nil, MessageResult{}, err
	}
	return nil, MessageResult{MessageID: id}, nil
}

func (s *Server) handleAddReaction(ctx context.Context, _ *mcp.CallToolRequest, args AddReactionArgs) (*mcp.CallToolResult, SimpleOK, error) {
	if err := s.enforceToolPolicy("add_reaction"); err != nil {
		return nil, SimpleOK{}, err
	}
	if err := s.discord.AddReaction(ctx, args.ChannelID, args.MessageID, args.Emoji); err != nil {
		return nil, SimpleOK{}, err
	}
	return nil, SimpleOK{OK: true}, nil
}

func (s *Server) handleStartTyping(ctx context.Context, _ *mcp.CallToolRequest, args StartTypingArgs) (*mcp.CallToolResult, SimpleOK, error) {
	if err := s.enforceToolPolicy("start_typing"); err != nil {
		return nil, SimpleOK{}, err
	}
	duration := 10 * time.Second
	if args.DurationSec > 0 {
		duration = time.Duration(args.DurationSec) * time.Second
	}
	s.discord.StartTyping(ctx, args.ChannelID, duration)
	return nil, SimpleOK{OK: true}, nil
}

func (s *Server) handleListChannels(ctx context.Context, _ *mcp.CallToolRequest, _ EmptyArgs) (*mcp.CallToolResult, ListChannelsResult, error) {
	if err := s.enforceToolPolicy("list_channels"); err != nil {
		return nil, ListChannelsResult{}, err
	}
	channels, err := s.discord.ListChannels(ctx)
	if err != nil {
		return nil, ListChannelsResult{}, err
	}
	out := make([]ChannelItem, 0, len(channels))
	for _, c := range channels {
		out = append(out, ChannelItem{ChannelID: c.ChannelID, Name: c.Name})
	}
	return nil, ListChannelsResult{Channels: out}, nil
}

func (s *Server) handleGetUserDetail(ctx context.Context, _ *mcp.CallToolRequest, args UserDetailArgs) (*mcp.CallToolResult, UserDetailResult, error) {
	if err := s.enforceToolPolicy("get_user_detail"); err != nil {
		return nil, UserDetailResult{}, err
	}
	user, err := s.discord.GetUserDetail(ctx, args.ChannelID, args.UserID)
	if err != nil {
		return nil, UserDetailResult{}, err
	}
	return nil, UserDetailResult{
		UserID:      user.UserID,
		Username:    user.Username,
		DisplayName: user.DisplayName,
		Nick:        user.Nick,
	}, nil
}

func (s *Server) handleGetCurrentTime(_ context.Context, _ *mcp.CallToolRequest, args CurrentTimeArgs) (*mcp.CallToolResult, CurrentTimeResult, error) {
	if err := s.enforceToolPolicy("get_current_time"); err != nil {
		return nil, CurrentTimeResult{}, err
	}
	tz := strings.TrimSpace(args.Timezone)
	if tz == "" {
		tz = s.defaultTimezone
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, CurrentTimeResult{}, fmt.Errorf("invalid timezone %q: %w", tz, err)
	}
	now := time.Now().In(loc)
	return nil, CurrentTimeResult{
		Timezone:       loc.String(),
		CurrentUnix:    now.Unix(),
		CurrentRFC3339: now.Format(time.RFC3339),
	}, nil
}

func (s *Server) handleMemoryUpsertUserNote(ctx context.Context, _ *mcp.CallToolRequest, args MemoryUpsertUserNoteArgs) (*mcp.CallToolResult, MemoryPathResult, error) {
	if err := s.enforceToolPolicy("memory_upsert_user_note"); err != nil {
		return nil, MemoryPathResult{}, err
	}
	path, err := s.memory.UpsertUserNote(ctx, memory.UserNoteInput{
		UserID: args.UserID,
		Note:   args.Note,
		Source: args.Source,
	})
	if err != nil {
		return nil, MemoryPathResult{}, err
	}
	return nil, MemoryPathResult{Path: path}, nil
}

func (s *Server) handleMemoryUpsertChannelIntent(ctx context.Context, _ *mcp.CallToolRequest, args MemoryUpsertChannelIntentArgs) (*mcp.CallToolResult, MemoryPathResult, error) {
	if err := s.enforceToolPolicy("memory_upsert_channel_intent"); err != nil {
		return nil, MemoryPathResult{}, err
	}
	path, err := s.memory.UpsertChannelIntent(ctx, memory.ChannelIntentInput{
		ChannelID: args.ChannelID,
		Intent:    args.Intent,
		Policy:    args.Policy,
	})
	if err != nil {
		return nil, MemoryPathResult{}, err
	}
	return nil, MemoryPathResult{Path: path}, nil
}

func (s *Server) handleMemoryUpsertTask(ctx context.Context, _ *mcp.CallToolRequest, args MemoryUpsertTaskArgs) (*mcp.CallToolResult, MemoryTaskResult, error) {
	if err := s.enforceToolPolicy("memory_upsert_task"); err != nil {
		return nil, MemoryTaskResult{}, err
	}
	var next time.Time
	if raw := strings.TrimSpace(args.NextRunAt); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return nil, MemoryTaskResult{}, fmt.Errorf("next_run_at must be RFC3339: %w", err)
		}
		next = parsed
	}
	task, err := s.memory.UpsertTask(ctx, memory.UpsertTaskInput{
		TaskID:       args.TaskID,
		Title:        args.Title,
		Instructions: args.Instructions,
		ChannelID:    args.ChannelID,
		Schedule:     args.Schedule,
		NextRunAt:    next,
		Status:       args.Status,
	})
	if err != nil {
		return nil, MemoryTaskResult{}, err
	}
	result := MemoryTaskResult{TaskID: task.TaskID, Status: task.Status}
	if !task.NextRunAt.IsZero() {
		result.NextRunAt = task.NextRunAt.UTC().Format(time.RFC3339)
	}
	return nil, result, nil
}

func (s *Server) handleMemoryQuery(ctx context.Context, _ *mcp.CallToolRequest, args MemoryQueryArgs) (*mcp.CallToolResult, MemoryQueryResult, error) {
	if err := s.enforceToolPolicy("memory_query"); err != nil {
		return nil, MemoryQueryResult{}, err
	}
	results, err := s.memory.Query(ctx, memory.QueryInput{Keyword: args.Keyword, Limit: args.Limit})
	if err != nil {
		return nil, MemoryQueryResult{}, err
	}
	matches := make([]MemoryMatch, 0, len(results))
	for _, r := range results {
		matches = append(matches, MemoryMatch{Path: r.Path, Excerpt: r.Excerpt})
	}
	return nil, MemoryQueryResult{Matches: matches}, nil
}

func (s *Server) handleReadWorkspaceDoc(_ context.Context, _ *mcp.CallToolRequest, args WorkspaceDocArgs) (*mcp.CallToolResult, WorkspaceDocResult, error) {
	if err := s.enforceToolPolicy("read_workspace_doc"); err != nil {
		return nil, WorkspaceDocResult{}, err
	}
	path, err := s.workspaceDocPath(args.Name)
	if err != nil {
		return nil, WorkspaceDocResult{}, err
	}

	s.docMu.Lock()
	defer s.docMu.Unlock()

	body, err := os.ReadFile(path)
	if err != nil {
		return nil, WorkspaceDocResult{}, fmt.Errorf("read workspace doc: %w", err)
	}
	return nil, WorkspaceDocResult{Name: filepath.Base(path), Path: path, Content: string(body)}, nil
}

func (s *Server) handleAppendWorkspaceDoc(_ context.Context, _ *mcp.CallToolRequest, args WorkspaceDocWriteArgs) (*mcp.CallToolResult, WorkspaceDocResult, error) {
	if err := s.enforceToolPolicy("append_workspace_doc"); err != nil {
		return nil, WorkspaceDocResult{}, err
	}
	path, err := s.workspaceDocPath(args.Name)
	if err != nil {
		return nil, WorkspaceDocResult{}, err
	}
	content := strings.TrimSpace(args.Content)
	if content == "" {
		return nil, WorkspaceDocResult{}, errors.New("content is required")
	}

	s.docMu.Lock()
	defer s.docMu.Unlock()

	existing, readErr := os.ReadFile(path)
	if readErr != nil {
		return nil, WorkspaceDocResult{}, fmt.Errorf("read workspace doc: %w", readErr)
	}
	builder := strings.Builder{}
	builder.WriteString(string(existing))
	if !strings.HasSuffix(builder.String(), "\n") {
		builder.WriteString("\n")
	}
	builder.WriteString("\n")
	builder.WriteString(content)
	builder.WriteString("\n")

	final := builder.String()
	if err := os.WriteFile(path, []byte(final), 0o644); err != nil {
		return nil, WorkspaceDocResult{}, fmt.Errorf("append workspace doc: %w", err)
	}
	return nil, WorkspaceDocResult{Name: filepath.Base(path), Path: path, Content: final}, nil
}

func (s *Server) handleReplaceWorkspaceDoc(_ context.Context, _ *mcp.CallToolRequest, args WorkspaceDocWriteArgs) (*mcp.CallToolResult, WorkspaceDocResult, error) {
	if err := s.enforceToolPolicy("replace_workspace_doc"); err != nil {
		return nil, WorkspaceDocResult{}, err
	}
	path, err := s.workspaceDocPath(args.Name)
	if err != nil {
		return nil, WorkspaceDocResult{}, err
	}
	content := strings.TrimSpace(args.Content)
	if content == "" {
		return nil, WorkspaceDocResult{}, errors.New("content is required")
	}

	s.docMu.Lock()
	defer s.docMu.Unlock()

	final := content + "\n"
	if err := os.WriteFile(path, []byte(final), 0o644); err != nil {
		return nil, WorkspaceDocResult{}, fmt.Errorf("replace workspace doc: %w", err)
	}
	return nil, WorkspaceDocResult{Name: filepath.Base(path), Path: path, Content: final}, nil
}

func (s *Server) workspaceDocPath(name string) (string, error) {
	base := strings.TrimSpace(filepath.Base(name))
	switch base {
	case "YURURI.md", "SOUL.md", "MEMORY.md", "HEARTBEAT.md":
		return filepath.Join(s.workspaceDir, base), nil
	default:
		return "", fmt.Errorf("unsupported workspace doc: %s", name)
	}
}

func (s *Server) enforceToolPolicy(toolName string) error {
	allowed, reason := s.toolPolicy.evaluate(toolName)
	if allowed {
		return nil
	}
	log.Printf("mcp tool denied: tool=%s reason=%q", toolName, reason)
	return fmt.Errorf("%w: tool=%s reason=%s", ErrToolDenied, toolName, reason)
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
