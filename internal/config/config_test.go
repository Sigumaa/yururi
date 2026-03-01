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
	if len(cfg.Discord.ObserveCategoryIDs) != 0 {
		t.Fatalf("Discord.ObserveCategoryIDs = %v, want empty", cfg.Discord.ObserveCategoryIDs)
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
	t.Setenv("DISCORD_OBSERVE_CHANNEL_IDS", "observe-1,observe-2")
	t.Setenv("DISCORD_OBSERVE_CATEGORY_IDS", "cat-1,cat-2")
	t.Setenv("HEARTBEAT_ENABLED", "false")
	t.Setenv("XAI_ENABLED", "true")
	t.Setenv("XAI_API_KEY", "xai-key")
	t.Setenv("XAI_BASE_URL", "https://api.x.ai/v1/")
	t.Setenv("XAI_MODEL", "grok-4-1-fast-non-reasoning")
	t.Setenv("XAI_TIMEOUT_SEC", "45")

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
	if got, want := cfg.Discord.ObserveChannelIDs, []string{"observe-1", "observe-2"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Discord.ObserveChannelIDs = %v, want %v", got, want)
	}
	if got, want := cfg.Discord.ObserveCategoryIDs, []string{"cat-1", "cat-2"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Discord.ObserveCategoryIDs = %v, want %v", got, want)
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

func TestLoadAppliesMCPBearerTokenFromConfig(t *testing.T) {
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
  mcp_servers:
    twilog-mcp:
      command: "npx"
      args: ["mcp-remote", "https://twilog-mcp.togetter.dev/mcp"]
      bearer_token: "config-token"
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	server, ok := cfg.Codex.MCPServers["twilog-mcp"]
	if !ok {
		t.Fatal("twilog-mcp config missing")
	}
	if server.Headers["Authorization"] != "Bearer config-token" {
		t.Fatalf("Authorization header = %q, want %q", server.Headers["Authorization"], "Bearer config-token")
	}
	if len(server.Args) != 4 {
		t.Fatalf("twilog args len = %d, want 4 (%v)", len(server.Args), server.Args)
	}
	if server.Args[2] != "--header" || server.Args[3] != "Authorization: Bearer config-token" {
		t.Fatalf("twilog auth args = %v, want --header Authorization: Bearer config-token", server.Args)
	}
}

func TestLoadEnvTwilogTokenOverridesConfigBearerToken(t *testing.T) {
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
  mcp_servers:
    twilog-mcp:
      command: "npx"
      args: ["mcp-remote", "https://twilog-mcp.togetter.dev/mcp"]
      bearer_token: "config-token"
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Setenv("CODEX_MCP_TWILOG_BEARER_TOKEN", "env-token")

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	server, ok := cfg.Codex.MCPServers["twilog-mcp"]
	if !ok {
		t.Fatal("twilog-mcp config missing")
	}
	if server.Headers["Authorization"] != "Bearer env-token" {
		t.Fatalf("Authorization header = %q, want %q", server.Headers["Authorization"], "Bearer env-token")
	}
	if len(server.Args) != 4 {
		t.Fatalf("twilog args len = %d, want 4 (%v)", len(server.Args), server.Args)
	}
	if server.Args[2] != "--header" || server.Args[3] != "Authorization: Bearer env-token" {
		t.Fatalf("twilog auth args = %v, want --header Authorization: Bearer env-token", server.Args)
	}
}

func TestLoadRewritesExistingMCPRemoteAuthorizationHeader(t *testing.T) {
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
  mcp_servers:
    twilog-mcp:
      command: "npx"
      args: ["mcp-remote", "https://twilog-mcp.togetter.dev/mcp", "--header", "Authorization: Bearer old-token"]
      bearer_token: "new-token"
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	server, ok := cfg.Codex.MCPServers["twilog-mcp"]
	if !ok {
		t.Fatal("twilog-mcp config missing")
	}
	if server.Headers["Authorization"] != "Bearer new-token" {
		t.Fatalf("Authorization header = %q, want %q", server.Headers["Authorization"], "Bearer new-token")
	}
	if len(server.Args) != 4 {
		t.Fatalf("twilog args len = %d, want 4 (%v)", len(server.Args), server.Args)
	}
	if server.Args[2] != "--header" || server.Args[3] != "Authorization: Bearer new-token" {
		t.Fatalf("twilog auth args = %v, want rewritten auth header", server.Args)
	}
}
