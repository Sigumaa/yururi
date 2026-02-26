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
	HeartbeatSystemPrompt = "heartbeat.mdがワークスペース内に存在する場合はそれを確認し、内容に従って作業を行なってください。過去のチャットで言及された古いタスクを推測したり繰り返してはいけない。特に対応すべき事項がない場合は終了する"
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
	IsOwner     bool
	Current     RuntimeMessage
	Recent      []RuntimeMessage
}

type HeartbeatInput struct {
	DueTasks []HeartbeatTask
}

type HeartbeatTask struct {
	TaskID       string
	Title        string
	Instructions string
	ChannelID    string
	Schedule     string
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

func BuildHeartbeatBundle(instructions WorkspaceInstructions, input HeartbeatInput) Bundle {
	taskLines := []string{"(none)"}
	if len(input.DueTasks) > 0 {
		taskLines = make([]string, 0, len(input.DueTasks))
		for _, task := range input.DueTasks {
			line := fmt.Sprintf("- task_id=%s channel_id=%s title=%s schedule=%s instructions=%s",
				task.TaskID,
				valueOrFallback(task.ChannelID, "(unset)"),
				valueOrFallback(task.Title, "(untitled)"),
				valueOrFallback(task.Schedule, "(none)"),
				strings.TrimSpace(task.Instructions),
			)
			taskLines = append(taskLines, line)
		}
	}
	prompt := strings.Join([]string{
		HeartbeatSystemPrompt,
		"",
		"## due tasks",
		strings.Join(taskLines, "\n"),
	}, "\n")

	return Bundle{
		BaseInstructions:      buildBaseInstructions(instructions),
		DeveloperInstructions: buildDeveloperInstructions(),
		UserPrompt:            prompt,
	}
}

func buildBaseInstructions(instructions WorkspaceInstructions) string {
	sections := []string{
		"あなたはDiscordサーバー専用の自律エージェント『ゆるり』です。",
		"常に日本語で応答してください。",
		"返信・送信・リアクションは必要なときだけ行ってください。",
		"記憶の維持はmemory toolsとワークスペースのMarkdownを使って行ってください。",
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
		"メッセージ送信・返信・リアクションはMCPツールを使って行うこと。",
		"調査や複数ツール呼び出しを行う場合は必要に応じてstart_typingを使うこと。",
		"会話本文を永続保存しないこと。必要な知識だけmemoryツールへ保存すること。",
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
