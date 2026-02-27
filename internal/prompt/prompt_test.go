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
	if !strings.Contains(bundle.UserPrompt, "バースト統合件数: 4") {
		t.Fatalf("UserPrompt missing merged count: %q", bundle.UserPrompt)
	}
	if !strings.Contains(bundle.DeveloperInstructions, "Discord MCPツール") {
		t.Fatalf("DeveloperInstructions missing MCP guidance: %q", bundle.DeveloperInstructions)
	}
	if !strings.Contains(bundle.DeveloperInstructions, "reply_message または send_message") {
		t.Fatalf("DeveloperInstructions missing explicit delivery guidance: %q", bundle.DeveloperInstructions)
	}
	if !strings.Contains(bundle.DeveloperInstructions, "read_workspace_doc / append_workspace_doc / replace_workspace_doc") {
		t.Fatalf("DeveloperInstructions missing workspace doc tool priority guidance: %q", bundle.DeveloperInstructions)
	}
	if !strings.Contains(bundle.DeveloperInstructions, "要約してMEMORY.md") {
		t.Fatalf("DeveloperInstructions missing MEMORY summarization guidance: %q", bundle.DeveloperInstructions)
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
