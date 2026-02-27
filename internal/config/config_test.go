package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSetsDefaults(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	body := `discord:
  token: "token"
  guild_id: "guild"
  target_channel_ids: ["channel"]
persona:
  owner_user_id: "owner"
codex:
  command: "codex"
  args: ["--search", "app-server", "--listen", "stdio://"]
  workspace_dir: "` + filepath.Join(dir, "workspace") + `"
  home_dir: "` + filepath.Join(dir, ".codex-home") + `"
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.MCP.Bind == "" {
		t.Fatal("MCP.Bind is empty")
	}
	if cfg.MCP.URL == "" {
		t.Fatal("MCP.URL is empty")
	}
	if len(cfg.MCP.ToolPolicy.AllowPatterns) != 0 {
		t.Fatalf("MCP.ToolPolicy.AllowPatterns = %v, want empty", cfg.MCP.ToolPolicy.AllowPatterns)
	}
	if len(cfg.MCP.ToolPolicy.DenyPatterns) != 0 {
		t.Fatalf("MCP.ToolPolicy.DenyPatterns = %v, want empty", cfg.MCP.ToolPolicy.DenyPatterns)
	}
	if !cfg.Heartbeat.Enabled {
		t.Fatal("Heartbeat.Enabled = false, want true by default")
	}
	if cfg.XAI.Enabled {
		t.Fatal("XAI.Enabled = true, want false by default")
	}
	if cfg.XAI.BaseURL != "https://api.x.ai/v1" {
		t.Fatalf("XAI.BaseURL = %q", cfg.XAI.BaseURL)
	}
	if cfg.XAI.Model != "grok-4-1-fast-non-reasoning" {
		t.Fatalf("XAI.Model = %q", cfg.XAI.Model)
	}
	if cfg.XAI.TimeoutSec != 30 {
		t.Fatalf("XAI.TimeoutSec = %d", cfg.XAI.TimeoutSec)
	}
	if cfg.Persona.TimesChannelID != "" {
		t.Fatalf("Persona.TimesChannelID = %q, want empty", cfg.Persona.TimesChannelID)
	}
	if cfg.Persona.TimesMinIntervalS != 0 {
		t.Fatalf("Persona.TimesMinIntervalS = %d, want 0", cfg.Persona.TimesMinIntervalS)
	}
}

func TestLoadAppliesEnvOverrides(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	body := `discord:
  token: "token"
  guild_id: "guild"
  target_channel_ids: ["channel"]
persona:
  owner_user_id: "owner"
codex:
  command: "codex"
  args: ["--search", "app-server", "--listen", "stdio://"]
mcp:
  tool_policy:
    allow_patterns: ["read_*"]
    deny_patterns: ["read_workspace_doc"]
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	t.Setenv("MCP_BIND", "127.0.0.1:44444")
	t.Setenv("MCP_URL", "http://127.0.0.1:44444/mcp")
	t.Setenv("MCP_TOOL_POLICY_ALLOW_PATTERNS", "read_*, get_current_time")
	t.Setenv("MCP_TOOL_POLICY_DENY_PATTERNS", "replace_workspace_doc")
	t.Setenv("HEARTBEAT_ENABLED", "false")
	t.Setenv("XAI_ENABLED", "true")
	t.Setenv("XAI_API_KEY", "xai-key")
	t.Setenv("XAI_BASE_URL", "https://api.x.ai/v1/")
	t.Setenv("XAI_MODEL", "grok-4-1-fast-non-reasoning")
	t.Setenv("XAI_TIMEOUT_SEC", "45")
	t.Setenv("PERSONA_TIMES_CHANNEL_ID", "times-channel")
	t.Setenv("PERSONA_TIMES_MIN_INTERVAL_SEC", "120")

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.MCP.Bind != "127.0.0.1:44444" {
		t.Fatalf("MCP.Bind = %q", cfg.MCP.Bind)
	}
	if cfg.MCP.URL != "http://127.0.0.1:44444/mcp" {
		t.Fatalf("MCP.URL = %q", cfg.MCP.URL)
	}
	if got, want := cfg.MCP.ToolPolicy.AllowPatterns, []string{"read_*", "get_current_time"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("MCP.ToolPolicy.AllowPatterns = %v, want %v", got, want)
	}
	if got, want := cfg.MCP.ToolPolicy.DenyPatterns, []string{"replace_workspace_doc"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("MCP.ToolPolicy.DenyPatterns = %v, want %v", got, want)
	}
	if cfg.Heartbeat.Enabled {
		t.Fatalf("Heartbeat.Enabled = true, want false")
	}
	if !cfg.XAI.Enabled {
		t.Fatalf("XAI.Enabled = false, want true")
	}
	if cfg.XAI.APIKey != "xai-key" {
		t.Fatalf("XAI.APIKey = %q", cfg.XAI.APIKey)
	}
	if cfg.XAI.BaseURL != "https://api.x.ai/v1" {
		t.Fatalf("XAI.BaseURL = %q", cfg.XAI.BaseURL)
	}
	if cfg.XAI.Model != "grok-4-1-fast-non-reasoning" {
		t.Fatalf("XAI.Model = %q", cfg.XAI.Model)
	}
	if cfg.XAI.TimeoutSec != 45 {
		t.Fatalf("XAI.TimeoutSec = %d", cfg.XAI.TimeoutSec)
	}
	if cfg.Persona.TimesChannelID != "times-channel" {
		t.Fatalf("Persona.TimesChannelID = %q, want times-channel", cfg.Persona.TimesChannelID)
	}
	if cfg.Persona.TimesMinIntervalS != 120 {
		t.Fatalf("Persona.TimesMinIntervalS = %d, want 120", cfg.Persona.TimesMinIntervalS)
	}

	current := CurrentMCPToolPolicy()
	if got, want := current.AllowPatterns, []string{"read_*", "get_current_time"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("CurrentMCPToolPolicy().AllowPatterns = %v, want %v", got, want)
	}
	if got, want := current.DenyPatterns, []string{"replace_workspace_doc"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("CurrentMCPToolPolicy().DenyPatterns = %v, want %v", got, want)
	}
}

func TestLoadRequiresXAIKeyWhenEnabled(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	body := `discord:
  token: "token"
  guild_id: "guild"
  target_channel_ids: ["channel"]
persona:
  owner_user_id: "owner"
codex:
  command: "codex"
  args: ["--search", "app-server", "--listen", "stdio://"]
xai:
  enabled: true
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if _, err := Load(cfgPath); err == nil {
		t.Fatal("Load() error = nil, want xai api key validation error")
	}
}
