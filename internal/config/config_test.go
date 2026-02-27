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
	if cfg.Memory.RootDir == "" {
		t.Fatal("Memory.RootDir is empty")
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
	t.Setenv("MCP_TOOL_POLICY_ALLOW_PATTERNS", "memory_*, get_current_time")
	t.Setenv("MCP_TOOL_POLICY_DENY_PATTERNS", "memory_upsert_*")
	t.Setenv("HEARTBEAT_ENABLED", "false")
	t.Setenv("MEMORY_ROOT_DIR", filepath.Join(dir, "memory"))

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
	if got, want := cfg.MCP.ToolPolicy.AllowPatterns, []string{"memory_*", "get_current_time"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("MCP.ToolPolicy.AllowPatterns = %v, want %v", got, want)
	}
	if got, want := cfg.MCP.ToolPolicy.DenyPatterns, []string{"memory_upsert_*"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("MCP.ToolPolicy.DenyPatterns = %v, want %v", got, want)
	}
	if cfg.Heartbeat.Enabled {
		t.Fatalf("Heartbeat.Enabled = true, want false")
	}
	if cfg.Memory.RootDir != filepath.Join(dir, "memory") {
		t.Fatalf("Memory.RootDir = %q", cfg.Memory.RootDir)
	}

	current := CurrentMCPToolPolicy()
	if got, want := current.AllowPatterns, []string{"memory_*", "get_current_time"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("CurrentMCPToolPolicy().AllowPatterns = %v, want %v", got, want)
	}
	if got, want := current.DenyPatterns, []string{"memory_upsert_*"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("CurrentMCPToolPolicy().DenyPatterns = %v, want %v", got, want)
	}
}
