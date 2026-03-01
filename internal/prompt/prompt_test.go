package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnsureWorkspaceInstructionFiles(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	if err := EnsureWorkspaceInstructionFiles(workspace); err != nil {
		t.Fatalf("EnsureWorkspaceInstructionFiles() error = %v", err)
	}
	for _, name := range instructionOrder {
		if _, err := os.Stat(filepath.Join(workspace, name)); err != nil {
			t.Fatalf("instruction file %s missing: %v", name, err)
		}
	}
}

func TestBuildMessageBundle(t *testing.T) {
	t.Parallel()

	ins := WorkspaceInstructions{
		Dir: "/tmp",
		Content: map[string]string{
			"YURURI.md": "# YURURI",
			"SOUL.md":   "# SOUL",
			"MEMORY.md": strings.Join([]string{
				"# MEMORY.md",
				"## Users",
				"- user:u1 は長文より短文を好む",
				"- user:u2 は丁寧語を好む",
			}, "\n"),
		},
	}
	bundle := BuildMessageBundle(ins, MessageInput{
		GuildID:     "g1",
		ChannelID:   "c1",
		ChannelName: "chat",
		MergedCount: 4,
		IsOwner:     true,
		Current: RuntimeMessage{
			ID:         "m2",
			AuthorID:   "u1",
			AuthorName: "shiyui",
			Content:    "ゆるり、これ見えてる？",
			CreatedAt:  time.Date(2026, 2, 26, 12, 0, 0, 0, time.UTC),
		},
		Recent: []RuntimeMessage{{
			ID:         "m1",
			AuthorID:   "u2",
			AuthorName: "friend",
			Content:    "hello",
			CreatedAt:  time.Date(2026, 2, 26, 11, 59, 0, 0, time.UTC),
		}},
	})

	if !strings.Contains(bundle.BaseInstructions, "YURURI.md") {
		t.Fatalf("BaseInstructions missing YURURI.md: %q", bundle.BaseInstructions)
	}
	if !strings.Contains(bundle.BaseInstructions, "4軸Markdown") {
		t.Fatalf("BaseInstructions missing 4-axis markdown guidance: %q", bundle.BaseInstructions)
	}
	if !strings.Contains(bundle.UserPrompt, "ゆるり、これ見えてる？") {
		t.Fatalf("UserPrompt missing current message: %q", bundle.UserPrompt)
	}
	if strings.Contains(bundle.UserPrompt, "2026-02-26T12:00:00Z") {
		t.Fatalf("UserPrompt should not include timestamp: %q", bundle.UserPrompt)
	}
	if !strings.Contains(bundle.UserPrompt, "バースト統合件数: 4") {
		t.Fatalf("UserPrompt missing merged count: %q", bundle.UserPrompt)
	}
	if !strings.Contains(bundle.DeveloperInstructions, "Discord MCPツール") {
		t.Fatalf("DeveloperInstructions missing MCP guidance: %q", bundle.DeveloperInstructions)
	}
	if !strings.Contains(bundle.DeveloperInstructions, "reply_message または send_message") {
		t.Fatalf("DeveloperInstructions missing explicit delivery guidance: %q", bundle.DeveloperInstructions)
	}
	if !strings.Contains(bundle.DeveloperInstructions, "add_reaction") {
		t.Fatalf("DeveloperInstructions missing reaction guidance: %q", bundle.DeveloperInstructions)
	}
	if !strings.Contains(bundle.UserPrompt, "## MEMORY参照（今回の話者関連）") {
		t.Fatalf("UserPrompt missing memory focus section: %q", bundle.UserPrompt)
	}
	if !strings.Contains(bundle.UserPrompt, "user:u1 は長文より短文を好む") {
		t.Fatalf("UserPrompt missing focused memory line: %q", bundle.UserPrompt)
	}
}

func TestBuildHeartbeatBundle(t *testing.T) {
	t.Parallel()

	bundle := BuildHeartbeatBundle(WorkspaceInstructions{})
	if !strings.Contains(bundle.UserPrompt, HeartbeatSystemPrompt) {
		t.Fatalf("heartbeat prompt missing heartbeat system prompt: %q", bundle.UserPrompt)
	}
	if strings.Contains(strings.ToLower(bundle.UserPrompt), "due tasks") {
		t.Fatalf("heartbeat prompt should not include due tasks section: %q", bundle.UserPrompt)
	}
}

func TestExtractMemoryFocusLines(t *testing.T) {
	t.Parallel()

	memory := strings.Join([]string{
		"# MEMORY.md",
		"## Users",
		"- user:u1 は短文を好む",
		"- user:u2 は詳細説明を好む",
		"## Channel",
		"- channel:times は独り言運用",
		"- user:u1 は times で絵文字少なめ",
	}, "\n")
	got := extractMemoryFocusLines(memory, "u1", "shiyui", 5)
	if len(got) == 0 {
		t.Fatal("extractMemoryFocusLines() returned empty, want focused lines")
	}
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "user:u1 は短文を好む") {
		t.Fatalf("extractMemoryFocusLines() missing u1 line: %v", got)
	}
	if strings.Contains(joined, "user:u2 は詳細説明を好む") {
		t.Fatalf("extractMemoryFocusLines() should not include unrelated user line: %v", got)
	}
}
