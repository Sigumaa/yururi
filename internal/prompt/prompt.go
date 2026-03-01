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
	HeartbeatSystemPrompt = "HEARTBEAT.md を確認し、必要な作業のみ実行してください。対応事項がなければ終了してください。"
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
	memoryFocus := buildMemoryFocusSection(instructions, input)

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
		memoryFocus,
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
		"返信または投稿が必要な場合は、同じターン中に reply_message または send_message を実行して完了すること。",
		"リアクションが適切な場合は add_reaction を使って完了すること。",
		"調査や複数ツール呼び出しを行う場合は必要に応じてstart_typingを使うこと。",
	}, "\n")
}

func formatRuntimeMessage(message RuntimeMessage) string {
	meta := fmt.Sprintf("%s (%s, Message ID: %s)", valueOrFallback(message.AuthorName, "unknown"), valueOrFallback(message.AuthorID, "unknown"), valueOrFallback(message.ID, "unknown"))
	content := strings.TrimSpace(message.Content)
	if content == "" {
		content = "(empty)"
	}
	return meta + "\n" + content
}

func buildMemoryFocusSection(instructions WorkspaceInstructions, input MessageInput) string {
	memoryText := strings.TrimSpace(instructions.Content["MEMORY.md"])
	if memoryText == "" {
		return ""
	}
	lines := extractMemoryFocusLines(memoryText, input.Current.AuthorID, input.Current.AuthorName, 12)
	if len(lines) == 0 {
		return ""
	}
	return "\n## MEMORY参照（今回の話者関連）\n\n" + strings.Join(lines, "\n")
}

func extractMemoryFocusLines(memoryText string, authorID string, authorName string, maxLines int) []string {
	text := strings.TrimSpace(memoryText)
	if text == "" || maxLines <= 0 {
		return nil
	}
	keys := uniqueMemoryFocusKeys(authorID, authorName)
	if len(keys) == 0 {
		return nil
	}
	source := strings.Split(text, "\n")
	out := make([]string, 0, maxLines)
	seen := map[string]struct{}{}
	for i := 0; i < len(source) && len(out) < maxLines; i++ {
		line := strings.TrimSpace(source[i])
		if line == "" || !lineContainsAnyFold(line, keys) {
			continue
		}
		if header, ok := nearestSectionHeader(source, i); ok {
			key := "header:" + header
			if _, exists := seen[key]; !exists && len(out) < maxLines {
				seen[key] = struct{}{}
				out = append(out, header)
			}
		}
		key := "line:" + line
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, line)
	}
	return out
}

func uniqueMemoryFocusKeys(authorID string, authorName string) []string {
	keys := make([]string, 0, 2)
	seen := map[string]struct{}{}
	for _, raw := range []string{authorID, authorName} {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		lowered := strings.ToLower(trimmed)
		if _, ok := seen[lowered]; ok {
			continue
		}
		seen[lowered] = struct{}{}
		keys = append(keys, lowered)
	}
	return keys
}

func lineContainsAnyFold(line string, keys []string) bool {
	lowered := strings.ToLower(strings.TrimSpace(line))
	if lowered == "" {
		return false
	}
	for _, key := range keys {
		if key != "" && strings.Contains(lowered, key) {
			return true
		}
	}
	return false
}

func nearestSectionHeader(lines []string, index int) (string, bool) {
	if len(lines) == 0 || index <= 0 {
		return "", false
	}
	for i := index - 1; i >= 0; i-- {
		header := strings.TrimSpace(lines[i])
		if strings.HasPrefix(header, "#") {
			return header, true
		}
		if header != "" && !strings.HasPrefix(header, "-") && !strings.HasPrefix(header, "*") {
			break
		}
	}
	return "", false
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
