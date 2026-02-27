package mcpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/sigumaa/yururi/internal/config"
	"github.com/sigumaa/yururi/internal/discordx"
	"github.com/sigumaa/yururi/internal/xai"
)

func TestServerURL(t *testing.T) {
	t.Parallel()

	workspaceDir := t.TempDir()

	srv, err := New("127.0.0.1:39393", "Asia/Tokyo", workspaceDir, &discordx.Gateway{}, nil, config.MCPToolPolicyConfig{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if got := srv.URL(); got != "http://127.0.0.1:39393/mcp" {
		t.Fatalf("URL() = %q, want %q", got, "http://127.0.0.1:39393/mcp")
	}
}

func TestHandleGetCurrentTime(t *testing.T) {
	t.Parallel()

	workspaceDir := t.TempDir()

	srv, err := New("127.0.0.1:39393", "Asia/Tokyo", workspaceDir, &discordx.Gateway{}, nil, config.MCPToolPolicyConfig{})
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

	workspaceDir := t.TempDir()
	seedPath := workspaceDir + "/MEMORY.md"
	if err := os.WriteFile(seedPath, []byte("# MEMORY.md\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	srv, err := New("127.0.0.1:39393", "Asia/Tokyo", workspaceDir, &discordx.Gateway{}, nil, config.MCPToolPolicyConfig{})
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
		AllowPatterns: []string{"read_*"},
		DenyPatterns:  []string{"read_workspace_doc"},
	})

	allowed, reason := policy.evaluate("read_workspace_doc")
	if allowed {
		t.Fatal("policy.evaluate(read_workspace_doc) = allowed, want denied")
	}
	if !strings.Contains(reason, `matched deny pattern "read_workspace_doc"`) {
		t.Fatalf("deny reason = %q", reason)
	}

	allowed, reason = policy.evaluate("read_message_history")
	if !allowed {
		t.Fatalf("policy.evaluate(read_message_history) denied, reason=%q", reason)
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

	allowed, reason = policy.evaluate("send_message")
	if allowed {
		t.Fatalf("policy.evaluate(send_message) = allowed, want denied")
	}
	if reason != "not matched by allow patterns" {
		t.Fatalf("policy.evaluate(send_message) reason = %q", reason)
	}
}

func TestHandleGetCurrentTimeDeniedByPolicy(t *testing.T) {
	t.Parallel()

	workspaceDir := t.TempDir()

	srv, err := New("127.0.0.1:39393", "Asia/Tokyo", workspaceDir, &discordx.Gateway{}, nil, config.MCPToolPolicyConfig{
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

func TestHandleXSearchSuccess(t *testing.T) {
	t.Parallel()

	xaiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp-x-1",
			"model":"grok-4-1-fast-non-reasoning",
			"output_text":"x search result",
			"citations":[{"type":"url_citation","url":"https://x.com/openai/status/1","title":"OpenAI post"}]
		}`))
	}))
	defer xaiServer.Close()

	workspaceDir := t.TempDir()
	xaiClient := xai.NewClient(xai.Config{
		BaseURL:    xaiServer.URL,
		APIKey:     "test-key",
		Model:      "grok-4-1-fast-non-reasoning",
		HTTPClient: xaiServer.Client(),
	})

	srv, err := New("127.0.0.1:39393", "Asia/Tokyo", workspaceDir, &discordx.Gateway{}, xaiClient, config.MCPToolPolicyConfig{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, got, err := srv.handleXSearch(context.Background(), nil, XSearchArgs{
		Query: "today trend",
	})
	if err != nil {
		t.Fatalf("handleXSearch() error = %v", err)
	}
	if got.Text != "x search result" {
		t.Fatalf("Text = %q, want %q", got.Text, "x search result")
	}
	if got.ResponseID != "resp-x-1" {
		t.Fatalf("ResponseID = %q, want %q", got.ResponseID, "resp-x-1")
	}
	if len(got.Citations) != 1 {
		t.Fatalf("Citations len = %d, want 1", len(got.Citations))
	}
}

func TestHandleXSearchDisabled(t *testing.T) {
	t.Parallel()

	workspaceDir := t.TempDir()
	srv, err := New("127.0.0.1:39393", "Asia/Tokyo", workspaceDir, &discordx.Gateway{}, nil, config.MCPToolPolicyConfig{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, _, err := srv.handleXSearch(context.Background(), nil, XSearchArgs{Query: "test"}); err == nil {
		t.Fatal("handleXSearch() error = nil, want disabled error")
	}
}
