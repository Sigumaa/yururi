package mcpserver

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/sigumaa/yururi/internal/config"
	"github.com/sigumaa/yururi/internal/discordx"
	"github.com/sigumaa/yururi/internal/memory"
)

func TestServerURL(t *testing.T) {
	t.Parallel()

	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	workspaceDir := t.TempDir()

	srv, err := New("127.0.0.1:39393", "Asia/Tokyo", workspaceDir, &discordx.Gateway{}, store, config.MCPToolPolicyConfig{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if got := srv.URL(); got != "http://127.0.0.1:39393/mcp" {
		t.Fatalf("URL() = %q, want %q", got, "http://127.0.0.1:39393/mcp")
	}
}

func TestHandleGetCurrentTime(t *testing.T) {
	t.Parallel()

	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	workspaceDir := t.TempDir()

	srv, err := New("127.0.0.1:39393", "Asia/Tokyo", workspaceDir, &discordx.Gateway{}, store, config.MCPToolPolicyConfig{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, got, err := srv.handleGetCurrentTime(context.Background(), nil, CurrentTimeArgs{Timezone: "Asia/Tokyo"})
	if err != nil {
		t.Fatalf("handleGetCurrentTime() error = %v", err)
	}
	if got.Timezone != "Asia/Tokyo" {
		t.Fatalf("Timezone = %q, want %q", got.Timezone, "Asia/Tokyo")
	}
	if got.CurrentRFC3339 == "" {
		t.Fatal("CurrentRFC3339 is empty")
	}

	if _, _, err := srv.handleGetCurrentTime(context.Background(), nil, CurrentTimeArgs{Timezone: "Invalid/Timezone"}); err == nil {
		t.Fatal("handleGetCurrentTime() error = nil, want error")
	}
}

func TestWorkspaceDocReadWrite(t *testing.T) {
	t.Parallel()

	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	workspaceDir := t.TempDir()
	seedPath := workspaceDir + "/MEMORY.md"
	if err := os.WriteFile(seedPath, []byte("# MEMORY.md\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	srv, err := New("127.0.0.1:39393", "Asia/Tokyo", workspaceDir, &discordx.Gateway{}, store, config.MCPToolPolicyConfig{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if _, _, err := srv.handleAppendWorkspaceDoc(context.Background(), nil, WorkspaceDocWriteArgs{
		Name:    "MEMORY.md",
		Content: "- user prefers concise answers",
	}); err != nil {
		t.Fatalf("handleAppendWorkspaceDoc() error = %v", err)
	}

	_, got, err := srv.handleReadWorkspaceDoc(context.Background(), nil, WorkspaceDocArgs{Name: "MEMORY.md"})
	if err != nil {
		t.Fatalf("handleReadWorkspaceDoc() error = %v", err)
	}
	if !strings.Contains(got.Content, "user prefers concise answers") {
		t.Fatalf("workspace doc content missing appended text: %q", got.Content)
	}
}

func TestToolPolicyEvaluateDenyPrecedence(t *testing.T) {
	t.Parallel()

	policy := newToolPolicy(config.MCPToolPolicyConfig{
		AllowPatterns: []string{"memory_*"},
		DenyPatterns:  []string{"memory_upsert_*"},
	})

	allowed, reason := policy.evaluate("memory_upsert_task")
	if allowed {
		t.Fatal("policy.evaluate(memory_upsert_task) = allowed, want denied")
	}
	if !strings.Contains(reason, `matched deny pattern "memory_upsert_*"`) {
		t.Fatalf("deny reason = %q", reason)
	}

	allowed, reason = policy.evaluate("memory_query")
	if !allowed {
		t.Fatalf("policy.evaluate(memory_query) denied, reason=%q", reason)
	}
}

func TestToolPolicyEvaluateWildcardCaseInsensitive(t *testing.T) {
	t.Parallel()

	policy := newToolPolicy(config.MCPToolPolicyConfig{
		AllowPatterns: []string{"READ_*", "GET_CURRENT_*"},
	})

	allowed, reason := policy.evaluate("read_workspace_doc")
	if !allowed {
		t.Fatalf("policy.evaluate(read_workspace_doc) denied, reason=%q", reason)
	}

	allowed, reason = policy.evaluate("Get_Current_Time")
	if !allowed {
		t.Fatalf("policy.evaluate(Get_Current_Time) denied, reason=%q", reason)
	}

	allowed, reason = policy.evaluate("memory_query")
	if allowed {
		t.Fatalf("policy.evaluate(memory_query) = allowed, want denied")
	}
	if reason != "not matched by allow patterns" {
		t.Fatalf("policy.evaluate(memory_query) reason = %q", reason)
	}
}

func TestHandleGetCurrentTimeDeniedByPolicy(t *testing.T) {
	t.Parallel()

	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	workspaceDir := t.TempDir()

	srv, err := New("127.0.0.1:39393", "Asia/Tokyo", workspaceDir, &discordx.Gateway{}, store, config.MCPToolPolicyConfig{
		DenyPatterns: []string{"get_current_*"},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if _, _, err := srv.handleGetCurrentTime(context.Background(), nil, CurrentTimeArgs{}); err == nil {
		t.Fatal("handleGetCurrentTime() error = nil, want deny error")
	} else {
		if !errors.Is(err, ErrToolDenied) {
			t.Fatalf("handleGetCurrentTime() error = %v, want ErrToolDenied", err)
		}
		if !strings.Contains(err.Error(), "tool=get_current_time") {
			t.Fatalf("handleGetCurrentTime() error missing tool name: %v", err)
		}
	}
}
