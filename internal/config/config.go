package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	defaultCodexCommand         = "codex"
	defaultCodexModel           = "gpt-5.3-codex"
	defaultCodexReasoningEffort = "medium"
)

var defaultCodexArgs = []string{"--search", "app-server", "--listen", "stdio://"}

type Config struct {
	Discord DiscordConfig `yaml:"discord"`
	Persona PersonaConfig `yaml:"persona"`
	Codex   CodexConfig   `yaml:"codex"`
}

type DiscordConfig struct {
	Token              string   `yaml:"token"`
	GuildID            string   `yaml:"guild_id"`
	TargetChannelIDs   []string `yaml:"target_channel_ids"`
	ExcludedChannelIDs []string `yaml:"excluded_channel_ids"`
	AllowedBotUserIDs  []string `yaml:"allowed_bot_user_ids"`
}

type PersonaConfig struct {
	OwnerUserID string `yaml:"owner_user_id"`
}

type CodexConfig struct {
	Command         string   `yaml:"command"`
	Args            []string `yaml:"args"`
	Model           string   `yaml:"model"`
	ReasoningEffort string   `yaml:"reasoning_effort"`
	WorkspaceDir    string   `yaml:"workspace_dir"`
	CWD             string   `yaml:"cwd"`
	HomeDir         string   `yaml:"home_dir"`
	Home            string   `yaml:"home"`
}

func Load(path string) (Config, error) {
	cfg := Config{
		Codex: CodexConfig{
			Command:         defaultCodexCommand,
			Args:            append([]string(nil), defaultCodexArgs...),
			Model:           defaultCodexModel,
			ReasoningEffort: defaultCodexReasoningEffort,
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
	cfg.Codex.normalize()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
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
	return nil
}

func (c *CodexConfig) normalize() {
	if c.WorkspaceDir == "" {
		c.WorkspaceDir = c.CWD
	}
	if c.HomeDir == "" {
		c.HomeDir = c.Home
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
	applyList("DISCORD_EXCLUDED_CHANNEL_IDS", &cfg.Discord.ExcludedChannelIDs)
	applyList("DISCORD_ALLOWED_BOT_USER_IDS", &cfg.Discord.AllowedBotUserIDs)
	applyString("PERSONA_OWNER_USER_ID", &cfg.Persona.OwnerUserID)
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
