package prompt

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	HeartbeatSystemPrompt = "heartbeat.mdがワークスペース内に存在する場合はそれを確認し、内容に従って作業を行なってください。過去のチャットで言及された古いタスクを推測したり繰り返してはいけない。特に対応すべき事項がない場合は終了する。times向けに共有文を書く場合は、報告書調を避け、やわらかい口語で短く書くこと。"
)

var (
	instructionOrder = []string{"YURURI.md", "SOUL.md", "MEMORY.md", "HEARTBEAT.md"}

	//go:embed templates/*.md
	templateFS embed.FS
)

type Bundle struct {
	BaseInstructions      string
	DeveloperInstructions string
	UserPrompt            string
}

type RuntimeMessage struct {
	ID         string
	AuthorID   string
	AuthorName string
	Content    string
	CreatedAt  time.Time
}

type MessageInput struct {
	GuildID     string
	ChannelID   string
	ChannelName string
	MergedCount int
	IsOwner     bool
	Current     RuntimeMessage
	Recent      []RuntimeMessage
}

type WorkspaceInstructions struct {
	Dir     string
	Content map[string]string
}

func EnsureWorkspaceInstructionFiles(workspaceDir string) error {
	if strings.TrimSpace(workspaceDir) == "" {
		return fmt.Errorf("workspace dir is required")
	}
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		return fmt.Errorf("create workspace dir: %w", err)
	}
	for _, name := range instructionOrder {
		path := filepath.Join(workspaceDir, name)
		_, statErr := os.Stat(path)
		if statErr == nil {
			continue
		}
		if !os.IsNotExist(statErr) {
			return fmt.Errorf("stat workspace instruction %s: %w", path, statErr)
		}

		body, readErr := readTemplate(name)
		if readErr != nil {
			return fmt.Errorf("read template %s: %w", name, readErr)
		}
		if writeErr := os.WriteFile(path, body, 0o644); writeErr != nil {
			return fmt.Errorf("write workspace instruction %s: %w", path, writeErr)
		}
	}
	return nil
}

func readTemplate(name string) ([]byte, error) {
	projectTemplatePath := filepath.Join("docs", "templates", name)
	if body, err := os.ReadFile(projectTemplatePath); err == nil {
		return body, nil
	}
	return templateFS.ReadFile(filepath.ToSlash(filepath.Join("templates", name)))
}

func LoadWorkspaceInstructions(workspaceDir string) (WorkspaceInstructions, error) {
	if strings.TrimSpace(workspaceDir) == "" {
		return WorkspaceInstructions{}, fmt.Errorf("workspace dir is required")
	}
	result := WorkspaceInstructions{
		Dir:     filepath.Clean(workspaceDir),
		Content: map[string]string{},
	}
	for _, name := range instructionOrder {
		path := filepath.Join(result.Dir, name)
		body, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return WorkspaceInstructions{}, fmt.Errorf("read %s: %w", path, err)
		}
		text := strings.TrimSpace(string(body))
		if text == "" {
			continue
		}
		result.Content[name] = text
	}
	return result, nil
}

func BuildMessageBundle(instructions WorkspaceInstructions, input MessageInput) Bundle {
	recent := make([]string, 0, len(input.Recent))
	for _, msg := range input.Recent {
		recent = append(recent, formatRuntimeMessage(msg))
	}
	recentSection := "(none)"
	if len(recent) > 0 {
		recentSection = strings.Join(recent, "\n\n")
	}

	ownerText := "false"
	if input.IsOwner {
		ownerText = "true"
	}

	prompt := strings.Join([]string{
		"以下は現在の入力情報です。",
		fmt.Sprintf("Guild ID: %s", input.GuildID),
		fmt.Sprintf("チャンネル: %s (ID: %s)", input.ChannelName, input.ChannelID),
		fmt.Sprintf("バースト統合件数: %d", mergedCountForPrompt(input.MergedCount)),
		fmt.Sprintf("owner_user_idか: %s", ownerText),
		"",
		"## 直近のメッセージ",
		"",
		recentSection,
		"",
		"## 今回のメッセージ",
		"",
		formatRuntimeMessage(input.Current),
	}, "\n")

	return Bundle{
		BaseInstructions:      buildBaseInstructions(instructions),
		DeveloperInstructions: buildDeveloperInstructions(),
		UserPrompt:            prompt,
	}
}

func BuildHeartbeatBundle(instructions WorkspaceInstructions) Bundle {
	return Bundle{
		BaseInstructions:      buildBaseInstructions(instructions),
		DeveloperInstructions: buildDeveloperInstructions(),
		UserPrompt:            HeartbeatSystemPrompt,
	}
}

func buildBaseInstructions(instructions WorkspaceInstructions) string {
	sections := []string{
		"あなたはDiscordサーバー専用の自律エージェント『ゆるり』です。",
		"常に日本語で応答してください。",
		"返信・送信・リアクションは必要なときだけ行ってください。",
		"永続的な記憶は4軸Markdown（YURURI.md / SOUL.md / MEMORY.md / HEARTBEAT.md）だけで管理してください。",
	}

	loaded := make([]string, 0, len(instructions.Content))
	for _, name := range instructionOrder {
		text, ok := instructions.Content[name]
		if !ok {
			continue
		}
		loaded = append(loaded, "## "+name+"\n"+text)
	}
	if len(loaded) == 0 {
		keys := make([]string, 0, len(instructions.Content))
		for name := range instructions.Content {
			keys = append(keys, name)
		}
		sort.Strings(keys)
		for _, name := range keys {
			loaded = append(loaded, "## "+name+"\n"+instructions.Content[name])
		}
	}
	if len(loaded) > 0 {
		sections = append(sections, strings.Join(loaded, "\n\n"))
	}
	return strings.Join(sections, "\n\n")
}

func buildDeveloperInstructions() string {
	return strings.Join([]string{
		"返信・送信・リアクションは必ずDiscord MCPツールで実行すること。テキストだけを返して終了しないこと。",
		"返信が必要な内容を作成した場合は、同じターン中に必ず reply_message または send_message を1回以上実行して完了すること。",
		"調査や複数ツール呼び出しを行う場合は必要に応じてstart_typingを使うこと。",
		"X投稿やトレンドなど鮮度が必要な調査では、x_search が利用可能なら優先し、得られた引用URLを活用すること。",
		"twilog-mcp が利用可能な場合、ownerのX投稿確認には twilog-mcp を優先してよい。",
		"返信するほどではないが所感を残す価値がある場合は、times_channel_id が与えられていれば send_message で短く共有してよい。文体は硬すぎない口語を優先すること。",
		"会話本文の生ログを永続保存しないこと。ユーザー/チャンネルの好みや運用ルールは要約してMEMORY.mdへ記録すること。",
		"ユーザーから『覚えて』と言われた内容は、MEMORY.mdまたはHEARTBEAT.mdへ要約して反映すること。read_workspace_doc / append_workspace_doc / replace_workspace_docを優先すること。",
		"指定チャンネルの趣旨に合わせて口調と出力内容を調整すること。",
	}, "\n")
}

func formatRuntimeMessage(message RuntimeMessage) string {
	created := ""
	if !message.CreatedAt.IsZero() {
		created = message.CreatedAt.UTC().Format(time.RFC3339)
	}
	meta := fmt.Sprintf("[%s] %s (%s, Message ID: %s)", created, valueOrFallback(message.AuthorName, "unknown"), valueOrFallback(message.AuthorID, "unknown"), valueOrFallback(message.ID, "unknown"))
	content := strings.TrimSpace(message.Content)
	if content == "" {
		content = "(empty)"
	}
	return meta + "\n" + content
}

func valueOrFallback(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func mergedCountForPrompt(v int) int {
	if v <= 0 {
		return 1
	}
	return v
}
