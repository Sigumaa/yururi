package mcpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sigumaa/yururi/internal/config"
	"github.com/sigumaa/yururi/internal/discordx"
	"github.com/sigumaa/yururi/internal/xai"
)

func allowAllPolicy() config.MCPToolPolicyConfig {
	return config.MCPToolPolicyConfig{AllowPatterns: []string{"*"}}
}

func TestServerURL(t *testing.T) {
	t.Parallel()

	srv, err := New("127.0.0.1:39393", "Asia/Tokyo", &discordx.Gateway{}, nil, allowAllPolicy())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if got := srv.URL(); got != "http://127.0.0.1:39393/mcp" {
		t.Fatalf("URL() = %q, want %q", got, "http://127.0.0.1:39393/mcp")
	}
}

func TestHandleGetCurrentTime(t *testing.T) {
	t.Parallel()

	srv, err := New("127.0.0.1:39393", "Asia/Tokyo", &discordx.Gateway{}, nil, allowAllPolicy())
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

func TestToolPolicyEvaluateDenyPrecedence(t *testing.T) {
	t.Parallel()

	policy := newToolPolicy(config.MCPToolPolicyConfig{
		AllowPatterns: []string{"read_*"},
		DenyPatterns:  []string{"read_message_history"},
	})

	allowed, reason := policy.evaluate("read_message_history")
	if allowed {
		t.Fatal("policy.evaluate(read_message_history) = allowed, want denied")
	}
	if !strings.Contains(reason, `matched deny pattern "read_message_history"`) {
		t.Fatalf("deny reason = %q", reason)
	}

	allowed, reason = policy.evaluate("read_channel_history")
	if !allowed {
		t.Fatalf("policy.evaluate(read_channel_history) denied, reason=%q", reason)
	}
}

func TestToolPolicyEvaluateWildcardCaseInsensitive(t *testing.T) {
	t.Parallel()

	policy := newToolPolicy(config.MCPToolPolicyConfig{
		AllowPatterns: []string{"READ_*", "GET_CURRENT_*"},
	})

	allowed, reason := policy.evaluate("read_message_history")
	if !allowed {
		t.Fatalf("policy.evaluate(read_message_history) denied, reason=%q", reason)
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

func TestToolPolicyEvaluateDefaultAllow(t *testing.T) {
	t.Parallel()

	policy := newToolPolicy(config.MCPToolPolicyConfig{})
	allowed, reason := policy.evaluate("send_message")
	if !allowed {
		t.Fatalf("policy.evaluate(send_message) denied, reason=%q", reason)
	}
	if reason != "allowed by default" {
		t.Fatalf("policy.evaluate(send_message) reason = %q", reason)
	}
}

func TestHandleGetCurrentTimeDeniedByPolicy(t *testing.T) {
	t.Parallel()

	srv, err := New("127.0.0.1:39393", "Asia/Tokyo", &discordx.Gateway{}, nil, config.MCPToolPolicyConfig{
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

	xaiClient := xai.NewClient(xai.Config{
		BaseURL:    xaiServer.URL,
		APIKey:     "test-key",
		Model:      "grok-4-1-fast-non-reasoning",
		HTTPClient: xaiServer.Client(),
	})

	srv, err := New("127.0.0.1:39393", "Asia/Tokyo", &discordx.Gateway{}, xaiClient, allowAllPolicy())
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

	srv, err := New("127.0.0.1:39393", "Asia/Tokyo", &discordx.Gateway{}, nil, allowAllPolicy())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, _, err := srv.handleXSearch(context.Background(), nil, XSearchArgs{Query: "test"}); err == nil {
		t.Fatal("handleXSearch() error = nil, want disabled error")
	}
}

func TestHandleGetCurrentTimeUsageLimit(t *testing.T) {
	t.Parallel()

	srv, err := New("127.0.0.1:39393", "Asia/Tokyo", &discordx.Gateway{}, nil, config.MCPToolPolicyConfig{
		AllowPatterns: []string{"get_current_time"},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	for i := 0; i < defaultMaxToolCallsPerTurn; i++ {
		args := CurrentTimeArgs{Timezone: []string{"UTC", "Asia/Tokyo", "Europe/London"}[i]}
		if _, _, err := srv.handleGetCurrentTime(context.Background(), nil, args); err != nil {
			t.Fatalf("handleGetCurrentTime() warmup[%d] error = %v", i, err)
		}
	}

	if _, _, err := srv.handleGetCurrentTime(context.Background(), nil, CurrentTimeArgs{}); err == nil {
		t.Fatal("handleGetCurrentTime() error = nil, want usage-limit error")
	} else if !errors.Is(err, ErrToolUsageLimited) {
		t.Fatalf("handleGetCurrentTime() error = %v, want ErrToolUsageLimited", err)
	}
}

func TestHandleGetCurrentTimeSameArgumentRetryLimit(t *testing.T) {
	t.Parallel()

	srv, err := New("127.0.0.1:39393", "Asia/Tokyo", &discordx.Gateway{}, nil, config.MCPToolPolicyConfig{
		AllowPatterns: []string{"get_current_time"},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	args := CurrentTimeArgs{Timezone: "UTC"}
	for i := 0; i < defaultMaxSameArgsRetryCalls; i++ {
		if _, _, err := srv.handleGetCurrentTime(context.Background(), nil, args); err != nil {
			t.Fatalf("handleGetCurrentTime() warmup[%d] error = %v", i, err)
		}
	}

	if _, _, err := srv.handleGetCurrentTime(context.Background(), nil, args); err == nil {
		t.Fatal("handleGetCurrentTime() error = nil, want same-args usage-limit error")
	} else if !errors.Is(err, ErrToolUsageLimited) {
		t.Fatalf("handleGetCurrentTime() error = %v, want ErrToolUsageLimited", err)
	}
}
