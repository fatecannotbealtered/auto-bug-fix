package config

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type Config struct {
	Agent     AgentConfig     `json:"agent"`
	Poll      PollConfig      `json:"poll"`
	Workspace WorkspaceConfig `json:"workspace"`
	Knowledge KnowledgeConfig `json:"knowledge"`
}

type AgentConfig struct {
	Command   string `json:"command"`
	AgentType string `json:"agentType"` // "kiro" | "cursor" | "" (custom)
}

type PollConfig struct {
	IntervalSeconds int `json:"intervalSeconds"`
	MaxConcurrent   int `json:"maxConcurrent"`
	// StateExpiryDays: done/failed issues older than this are re-eligible for triggering.
	// 0 = never re-trigger (default).
	StateExpiryDays int          `json:"stateExpiryDays"`
	Filter          FilterConfig `json:"filter"`
}

type WorkspaceConfig struct {
	Root    string `json:"root"`
	Cleanup string `json:"cleanup"`
}

type KnowledgeConfig struct {
	Dir        string `json:"dir"`
	Read       bool   `json:"read"`
	Update     bool   `json:"update"`
	Handoff    bool   `json:"handoff"`
	HandoffDir string `json:"handoffDir"`

	readSet    bool
	updateSet  bool
	handoffSet bool
}

// FilterConfig defines which Bugs to process. All fields are optional.
// The poller translates these into jira-cli flags — no JQL knowledge required.
type FilterConfig struct {
	// TitleContains filters issues whose summary contains this string.
	TitleContains string `json:"titleContains"`
	// AssignedToMe limits results to issues assigned to the current user.
	AssignedToMe bool `json:"assignedToMe"`
	// ExcludeStatuses lists status names to skip (e.g. ["Done", "已关闭"]).
	ExcludeStatuses []string `json:"excludeStatuses"`

	assignedToMeSet bool
}

var envVarRe = regexp.MustCompile(`\$([A-Z_][A-Z0-9_]*)`)

const DefaultPollIntervalSeconds = 300
const DefaultPollMaxConcurrent = 3
const DefaultWorkspaceCleanup = "keep"
const DefaultKnowledgeDir = ".tcl"
const DefaultKnowledgeHandoffDir = "handoff"

func DefaultWorkspaceRoot() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".auto-bug-fix", "workspaces")
	}
	return filepath.Join(home, ".auto-bug-fix", "workspaces")
}

func (f *FilterConfig) UnmarshalJSON(data []byte) error {
	var raw struct {
		TitleContains   string   `json:"titleContains"`
		AssignedToMe    *bool    `json:"assignedToMe"`
		ExcludeStatuses []string `json:"excludeStatuses"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	f.TitleContains = raw.TitleContains
	f.ExcludeStatuses = raw.ExcludeStatuses
	if raw.AssignedToMe != nil {
		f.AssignedToMe = *raw.AssignedToMe
		f.assignedToMeSet = true
	} else {
		f.AssignedToMe = true
	}
	return nil
}

func (k *KnowledgeConfig) UnmarshalJSON(data []byte) error {
	var raw struct {
		Dir        string `json:"dir"`
		Read       *bool  `json:"read"`
		Update     *bool  `json:"update"`
		Handoff    *bool  `json:"handoff"`
		HandoffDir string `json:"handoffDir"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	k.Dir = raw.Dir
	k.HandoffDir = raw.HandoffDir
	if raw.Read != nil {
		k.Read = *raw.Read
		k.readSet = true
	}
	if raw.Update != nil {
		k.Update = *raw.Update
		k.updateSet = true
	}
	if raw.Handoff != nil {
		k.Handoff = *raw.Handoff
		k.handoffSet = true
	}
	return nil
}

// substituteEnv replaces every $VAR with its environment value. A placeholder
// whose variable is unset resolves to "" and its name is recorded in missing,
// so the caller can warn instead of silently producing empty config fields.
func substituteEnv(s string, missing map[string]struct{}) string {
	return envVarRe.ReplaceAllStringFunc(s, func(m string) string {
		name := m[1:]
		if v, ok := os.LookupEnv(name); ok {
			return v
		}
		missing[name] = struct{}{}
		return ""
	})
}

// substituteEnvInConfig substitutes $VAR placeholders across all config fields
// and returns the sorted, deduplicated names of any placeholders that were not
// set in the environment.
func substituteEnvInConfig(cfg *Config) []string {
	missing := map[string]struct{}{}
	cfg.Agent.Command = substituteEnv(cfg.Agent.Command, missing)
	cfg.Poll.Filter.TitleContains = substituteEnv(cfg.Poll.Filter.TitleContains, missing)
	cfg.Workspace.Root = substituteEnv(cfg.Workspace.Root, missing)
	cfg.Workspace.Cleanup = substituteEnv(cfg.Workspace.Cleanup, missing)
	cfg.Knowledge.Dir = substituteEnv(cfg.Knowledge.Dir, missing)
	cfg.Knowledge.HandoffDir = substituteEnv(cfg.Knowledge.HandoffDir, missing)

	if len(missing) == 0 {
		return nil
	}
	names := make([]string, 0, len(missing))
	for name := range missing {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func ApplyDefaults(cfg *Config) {
	if cfg.Poll.IntervalSeconds == 0 {
		cfg.Poll.IntervalSeconds = DefaultPollIntervalSeconds
	}
	if cfg.Poll.MaxConcurrent == 0 {
		cfg.Poll.MaxConcurrent = DefaultPollMaxConcurrent
	}
	if !cfg.Poll.Filter.assignedToMeSet {
		cfg.Poll.Filter.AssignedToMe = true
	}
	if cfg.Poll.Filter.ExcludeStatuses == nil {
		cfg.Poll.Filter.ExcludeStatuses = []string{}
	}
	if strings.TrimSpace(cfg.Workspace.Root) == "" {
		cfg.Workspace.Root = DefaultWorkspaceRoot()
	}
	if strings.TrimSpace(cfg.Workspace.Cleanup) == "" {
		cfg.Workspace.Cleanup = DefaultWorkspaceCleanup
	}
	if strings.TrimSpace(cfg.Knowledge.Dir) == "" {
		cfg.Knowledge.Dir = DefaultKnowledgeDir
	}
	if strings.TrimSpace(cfg.Knowledge.HandoffDir) == "" {
		cfg.Knowledge.HandoffDir = DefaultKnowledgeHandoffDir
	}
	if !cfg.Knowledge.readSet {
		cfg.Knowledge.Read = true
	}
	if !cfg.Knowledge.updateSet {
		cfg.Knowledge.Update = true
	}
	if !cfg.Knowledge.handoffSet {
		cfg.Knowledge.Handoff = true
	}
}

// Load reads and parses the config file, substituting $ENV_VAR placeholders.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("config file not found: %s", path)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("failed to parse config %s: %w", path, err)
	}
	if missing := substituteEnvInConfig(&cfg); len(missing) > 0 {
		// Unset placeholders become empty strings; surface them so a forgotten
		// `export $JIRA_TOKEN` shows up here rather than as a misleading
		// "jira.token is required" later in Validate.
		log.Printf("[auto-bug-fix] config %s: unresolved environment placeholder(s) treated as empty: $%s", path, strings.Join(missing, ", $"))
	}
	ApplyDefaults(&cfg)
	return cfg, nil
}

// Validate checks required fields and constraints. It deliberately does NOT
// require jira/gitlab/kibana hosts or tokens: authentication and connectivity
// are each sibling CLI's own responsibility (jira-cli/gitlab-cli/kibana-cli),
// verified at runtime by `auto-bug-fix doctor`, not stored in this config.
func Validate(cfg Config) error {
	if strings.TrimSpace(cfg.Agent.Command) == "" {
		return fmt.Errorf("agent.command is required")
	}
	if cfg.Agent.AgentType != "" && cfg.Agent.AgentType != "kiro" && cfg.Agent.AgentType != "cursor" && cfg.Agent.AgentType != "claude-code" && cfg.Agent.AgentType != "codex" {
		return fmt.Errorf("agent.agentType must be \"kiro\", \"cursor\", \"claude-code\", \"codex\", or empty")
	}
	if cfg.Poll.IntervalSeconds < 0 {
		return fmt.Errorf("poll.intervalSeconds must be >= 0")
	}
	if cfg.Poll.MaxConcurrent < 0 {
		return fmt.Errorf("poll.maxConcurrent must be >= 0")
	}
	switch cfg.Workspace.Cleanup {
	case "keep", "on-success", "always":
	default:
		return fmt.Errorf("workspace.cleanup must be \"keep\", \"on-success\", or \"always\"")
	}
	if !isRepoRelativePath(cfg.Knowledge.Dir) {
		return fmt.Errorf("knowledge.dir must be a repo-relative path")
	}
	if !isRepoRelativePath(cfg.Knowledge.HandoffDir) {
		return fmt.Errorf("knowledge.handoffDir must be a repo-relative path")
	}
	return nil
}

func isRepoRelativePath(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" || filepath.IsAbs(path) || strings.HasPrefix(path, "/") || strings.HasPrefix(path, `\`) {
		return false
	}
	if len(path) >= 2 && path[1] == ':' {
		return false
	}
	clean := filepath.Clean(path)
	return clean != "." && clean != ".." &&
		!strings.HasPrefix(clean, ".."+string(filepath.Separator)) &&
		!strings.HasPrefix(clean, "../") &&
		!strings.HasPrefix(clean, `..\`)
}
