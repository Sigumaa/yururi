package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

const (
	defaultCodexCommand         = "codex"
	defaultCodexModel           = "gpt-5.3-codex"
	defaultCodexReasoningEffort = "medium"
	defaultMCPBind              = "127.0.0.1:39393"
	defaultHeartbeatCron        = "0 */30 * * * *"
	defaultHeartbeatTimezone    = "Asia/Tokyo"
	defaultXAIBaseURL           = "https://api.x.ai/v1"
	defaultXAIModel             = "grok-4-1-fast-non-reasoning"
	defaultXAITimeoutSec        = 30
)

var defaultCodexArgs = []string{"--search", "app-server", "--listen", "stdio://"}

type Config struct {
	Discord   DiscordConfig   `yaml:"discord"`
	Persona   PersonaConfig   `yaml:"persona"`
	Codex     CodexConfig     `yaml:"codex"`
	MCP       MCPConfig       `yaml:"mcp"`
	Heartbeat HeartbeatConfig `yaml:"heartbeat"`
	Autonomy  AutonomyConfig  `yaml:"autonomy"`
	XAI       XAIConfig       `yaml:"xai"`
}

type DiscordConfig struct {
	Token              string   `yaml:"token"`
	GuildID            string   `yaml:"guild_id"`
	TargetChannelIDs   []string `yaml:"target_channel_ids"`
	ObserveChannelIDs  []string `yaml:"observe_channel_ids"`
	ExcludedChannelIDs []string `yaml:"excluded_channel_ids"`
	AllowedBotUserIDs  []string `yaml:"allowed_bot_user_ids"`
}

type PersonaConfig struct {
	OwnerUserID       string `yaml:"owner_user_id"`
	TimesChannelID    string `yaml:"times_channel_id"`
	TimesMinIntervalS int    `yaml:"times_min_interval_sec"`
}

type CodexConfig struct {
	Command         string                          `yaml:"command"`
	Args            []string                        `yaml:"args"`
	Model           string                          `yaml:"model"`
	ReasoningEffort string                          `yaml:"reasoning_effort"`
	WorkspaceDir    string                          `yaml:"workspace_dir"`
	CWD             string                          `yaml:"cwd"`
	HomeDir         string                          `yaml:"home_dir"`
	Home            string                          `yaml:"home"`
	MCPServers      map[string]CodexMCPServerConfig `yaml:"mcp_servers"`
}

type CodexMCPServerConfig struct {
	URL     string            `yaml:"url"`
	Command string            `yaml:"command"`
	Args    []string          `yaml:"args"`
	Headers map[string]string `yaml:"headers"`
}

type MCPConfig struct {
	Bind       string              `yaml:"bind"`
	URL        string              `yaml:"url"`
	ToolPolicy MCPToolPolicyConfig `yaml:"tool_policy"`
}

type MCPToolPolicyConfig struct {
	AllowPatterns []string `yaml:"allow_patterns"`
	DenyPatterns  []string `yaml:"deny_patterns"`
}

type HeartbeatConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Cron     string `yaml:"cron"`
	Timezone string `yaml:"timezone"`
}

type XAIConfig struct {
	Enabled    bool   `yaml:"enabled"`
	APIKey     string `yaml:"api_key"`
	BaseURL    string `yaml:"base_url"`
	Model      string `yaml:"model"`
	TimeoutSec int    `yaml:"timeout_sec"`
}

type AutonomyConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Cron     string `yaml:"cron"`
	Timezone string `yaml:"timezone"`
}

var (
	currentMCPToolPolicyMu sync.RWMutex
	currentMCPToolPolicy   MCPToolPolicyConfig
)

func Load(path string) (Config, error) {
	cfg := Config{
		Codex: CodexConfig{
			Command:         defaultCodexCommand,
			Args:            append([]string(nil), defaultCodexArgs...),
			Model:           defaultCodexModel,
			ReasoningEffort: defaultCodexReasoningEffort,
		},
		MCP: MCPConfig{
			Bind: defaultMCPBind,
		},
		Heartbeat: HeartbeatConfig{
			Enabled:  true,
			Cron:     defaultHeartbeatCron,
			Timezone: defaultHeartbeatTimezone,
		},
		Autonomy: AutonomyConfig{
			Enabled: false,
			Cron:    defaultHeartbeatCron,
		},
		XAI: XAIConfig{
			Enabled:    false,
			BaseURL:    defaultXAIBaseURL,
			Model:      defaultXAIModel,
			TimeoutSec: defaultXAITimeoutSec,
		},
	}

	body, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	if err := yaml.Unmarshal(body, &cfg); err != nil {
		return Config{}, fmt.Errorf("unmarshal config: %w", err)
	}

	applyEnvOverrides(&cfg)
	cfg.normalize()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	setCurrentMCPToolPolicy(cfg.MCP.ToolPolicy)
	return cfg, nil
}

func CurrentMCPToolPolicy() MCPToolPolicyConfig {
	currentMCPToolPolicyMu.RLock()
	defer currentMCPToolPolicyMu.RUnlock()
	return MCPToolPolicyConfig{
		AllowPatterns: append([]string(nil), currentMCPToolPolicy.AllowPatterns...),
		DenyPatterns:  append([]string(nil), currentMCPToolPolicy.DenyPatterns...),
	}
}

func (c Config) Validate() error {
	if c.Discord.Token == "" {
		return errors.New("discord.token is required")
	}
	if c.Discord.GuildID == "" {
		return errors.New("discord.guild_id is required")
	}
	if len(c.Discord.TargetChannelIDs) == 0 {
		return errors.New("discord.target_channel_ids is required")
	}
	if c.Codex.Command == "" {
		return errors.New("codex.command is required")
	}
	if len(c.Codex.Args) == 0 {
		return errors.New("codex.args is required")
	}
	if c.MCP.Bind == "" {
		return errors.New("mcp.bind is required")
	}
	if c.MCP.URL == "" {
		return errors.New("mcp.url is required")
	}
	if c.Heartbeat.Enabled {
		if c.Heartbeat.Cron == "" {
			return errors.New("heartbeat.cron is required when heartbeat.enabled=true")
		}
		if c.Heartbeat.Timezone == "" {
			return errors.New("heartbeat.timezone is required when heartbeat.enabled=true")
		}
	}
	if c.Autonomy.Enabled {
		if c.Autonomy.Cron == "" {
			return errors.New("autonomy.cron is required when autonomy.enabled=true")
		}
	}
	if c.XAI.Enabled {
		if c.XAI.APIKey == "" {
			return errors.New("xai.api_key is required when xai.enabled=true")
		}
	}
	return nil
}

func (c *Config) normalize() {
	if c.Codex.WorkspaceDir == "" {
		c.Codex.WorkspaceDir = c.Codex.CWD
	}
	if c.Codex.HomeDir == "" {
		c.Codex.HomeDir = c.Codex.Home
	}

	if c.Codex.WorkspaceDir != "" {
		c.Codex.WorkspaceDir = filepath.Clean(c.Codex.WorkspaceDir)
	}
	if c.Codex.HomeDir != "" {
		c.Codex.HomeDir = filepath.Clean(c.Codex.HomeDir)
	}
	for name, server := range c.Codex.MCPServers {
		normalized := CodexMCPServerConfig{
			URL:     strings.TrimSpace(server.URL),
			Command: strings.TrimSpace(server.Command),
			Args:    cleanList(server.Args),
		}
		if len(server.Headers) > 0 {
			normalized.Headers = make(map[string]string, len(server.Headers))
			for k, v := range server.Headers {
				key := strings.TrimSpace(k)
				val := strings.TrimSpace(v)
				if key == "" || val == "" {
					continue
				}
				normalized.Headers[key] = val
			}
		}
		c.Codex.MCPServers[name] = normalized
	}

	if strings.TrimSpace(c.MCP.URL) == "" {
		c.MCP.URL = "http://" + c.MCP.Bind + "/mcp"
	}
	if strings.TrimSpace(c.Autonomy.Timezone) == "" {
		c.Autonomy.Timezone = c.Heartbeat.Timezone
	}
	if strings.TrimSpace(c.XAI.BaseURL) == "" {
		c.XAI.BaseURL = defaultXAIBaseURL
	}
	c.XAI.BaseURL = strings.TrimRight(strings.TrimSpace(c.XAI.BaseURL), "/")
	if c.XAI.BaseURL == "" {
		c.XAI.BaseURL = defaultXAIBaseURL
	}
	if strings.TrimSpace(c.XAI.Model) == "" {
		c.XAI.Model = defaultXAIModel
	}
	if c.XAI.TimeoutSec <= 0 {
		c.XAI.TimeoutSec = defaultXAITimeoutSec
	}
	c.Discord.TargetChannelIDs = cleanList(c.Discord.TargetChannelIDs)
	c.Discord.ObserveChannelIDs = cleanList(c.Discord.ObserveChannelIDs)
	c.Discord.ExcludedChannelIDs = cleanList(c.Discord.ExcludedChannelIDs)
	c.Discord.AllowedBotUserIDs = cleanList(c.Discord.AllowedBotUserIDs)
	c.MCP.ToolPolicy.AllowPatterns = cleanList(c.MCP.ToolPolicy.AllowPatterns)
	c.MCP.ToolPolicy.DenyPatterns = cleanList(c.MCP.ToolPolicy.DenyPatterns)
	if c.Persona.TimesMinIntervalS < 0 {
		c.Persona.TimesMinIntervalS = 0
	}
}

func applyEnvOverrides(cfg *Config) {
	applyString := func(key string, dst *string) {
		if v, ok := os.LookupEnv(key); ok {
			*dst = strings.TrimSpace(v)
		}
	}
	applyList := func(key string, dst *[]string) {
		if v, ok := os.LookupEnv(key); ok {
			*dst = parseCSV(v)
		}
	}

	applyString("DISCORD_TOKEN", &cfg.Discord.Token)
	applyString("DISCORD_GUILD_ID", &cfg.Discord.GuildID)
	applyList("DISCORD_TARGET_CHANNEL_IDS", &cfg.Discord.TargetChannelIDs)
	applyList("DISCORD_OBSERVE_CHANNEL_IDS", &cfg.Discord.ObserveChannelIDs)
	applyList("DISCORD_EXCLUDED_CHANNEL_IDS", &cfg.Discord.ExcludedChannelIDs)
	applyList("DISCORD_ALLOWED_BOT_USER_IDS", &cfg.Discord.AllowedBotUserIDs)
	applyString("PERSONA_OWNER_USER_ID", &cfg.Persona.OwnerUserID)
	applyString("PERSONA_TIMES_CHANNEL_ID", &cfg.Persona.TimesChannelID)
	applyString("CODEX_COMMAND", &cfg.Codex.Command)
	if v, ok := os.LookupEnv("CODEX_ARGS"); ok {
		cfg.Codex.Args = parseArgs(v)
	}
	applyString("CODEX_MODEL", &cfg.Codex.Model)
	applyString("CODEX_REASONING_EFFORT", &cfg.Codex.ReasoningEffort)
	applyString("CODEX_CWD", &cfg.Codex.WorkspaceDir)
	applyString("CODEX_WORKSPACE_DIR", &cfg.Codex.WorkspaceDir)
	applyString("CODEX_HOME", &cfg.Codex.HomeDir)
	applyString("CODEX_HOME_DIR", &cfg.Codex.HomeDir)
	applyString("MCP_BIND", &cfg.MCP.Bind)
	applyString("MCP_URL", &cfg.MCP.URL)
	applyList("MCP_TOOL_POLICY_ALLOW_PATTERNS", &cfg.MCP.ToolPolicy.AllowPatterns)
	applyList("MCP_TOOL_POLICY_DENY_PATTERNS", &cfg.MCP.ToolPolicy.DenyPatterns)
	if v, ok := os.LookupEnv("HEARTBEAT_ENABLED"); ok {
		cfg.Heartbeat.Enabled = parseBool(v, cfg.Heartbeat.Enabled)
	}
	applyString("HEARTBEAT_CRON", &cfg.Heartbeat.Cron)
	applyString("HEARTBEAT_TIMEZONE", &cfg.Heartbeat.Timezone)
	if v, ok := os.LookupEnv("AUTONOMY_ENABLED"); ok {
		cfg.Autonomy.Enabled = parseBool(v, cfg.Autonomy.Enabled)
	}
	applyString("AUTONOMY_CRON", &cfg.Autonomy.Cron)
	applyString("AUTONOMY_TIMEZONE", &cfg.Autonomy.Timezone)
	if v, ok := os.LookupEnv("XAI_ENABLED"); ok {
		cfg.XAI.Enabled = parseBool(v, cfg.XAI.Enabled)
	}
	applyString("XAI_API_KEY", &cfg.XAI.APIKey)
	applyString("XAI_BASE_URL", &cfg.XAI.BaseURL)
	applyString("XAI_MODEL", &cfg.XAI.Model)
	if v, ok := os.LookupEnv("XAI_TIMEOUT_SEC"); ok {
		cfg.XAI.TimeoutSec = parseInt(v, cfg.XAI.TimeoutSec)
	}
	if v, ok := os.LookupEnv("PERSONA_TIMES_MIN_INTERVAL_SEC"); ok {
		cfg.Persona.TimesMinIntervalS = parseInt(v, cfg.Persona.TimesMinIntervalS)
	}
	if v, ok := os.LookupEnv("CODEX_MCP_TWILOG_BEARER_TOKEN"); ok {
		name := "twilog-mcp"
		server := cfg.Codex.MCPServers[name]
		token := strings.TrimSpace(v)
		if token != "" {
			if server.Headers == nil {
				server.Headers = map[string]string{}
			}
			server.Headers["Authorization"] = "Bearer " + token
			cfg.Codex.MCPServers[name] = server
		}
	}
}

func parseArgs(v string) []string {
	raw := strings.TrimSpace(v)
	if raw == "" {
		return nil
	}
	if strings.HasPrefix(raw, "[") {
		var args []string
		if err := json.Unmarshal([]byte(raw), &args); err == nil {
			return cleanList(args)
		}
	}
	return parseCSV(raw)
}

func parseCSV(v string) []string {
	parts := strings.Split(v, ",")
	return cleanList(parts)
}

func cleanList(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

func parseBool(raw string, fallback bool) bool {
	v := strings.TrimSpace(strings.ToLower(raw))
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func parseInt(raw string, fallback int) int {
	v := strings.TrimSpace(raw)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func setCurrentMCPToolPolicy(policy MCPToolPolicyConfig) {
	currentMCPToolPolicyMu.Lock()
	defer currentMCPToolPolicyMu.Unlock()
	currentMCPToolPolicy = MCPToolPolicyConfig{
		AllowPatterns: append([]string(nil), policy.AllowPatterns...),
		DenyPatterns:  append([]string(nil), policy.DenyPatterns...),
	}
}
