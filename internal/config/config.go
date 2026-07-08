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
	Verify    VerifyConfig    `json:"verify"`
	Notify    NotifyConfig    `json:"notify"`
	Interact  InteractConfig  `json:"interact"`
}

// VerifyConfig configures the two-phase evidence gate. When Enabled, an auto-fix is
// split into investigate (local commit, no writes) -> read-only verifier -> execute,
// and a refuted or integrity-failed proposal is downgraded to auto-diagnose BEFORE
// any MR is opened. Command is the read-only verifier launch command; when empty and
// agentType is known it is derived at runtime (installer.VerifierCommand), mirroring
// how agent.command is derived. The default is disabled — behavior unchanged.
type VerifyConfig struct {
	Enabled bool   `json:"enabled"`
	Command string `json:"command"`
}

type AgentConfig struct {
	Command   string `json:"command"`
	AgentType string `json:"agentType"` // "kiro" | "cursor" | "claude-code" | "codex" | "" (custom)
	// Model is the model the spawned agent must use. Required for a known
	// agentType. For flag-capable agents (cursor/claude-code/codex) it is
	// injected as --model in the derived command; for kiro (no --model flag) it
	// is written into the agent JSON by `setup --agent kiro`.
	Model string `json:"model"`
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

// NotifyConfig controls the optional completion-notification channel. v1 sends a
// one-way Lark (Feishu) interactive card via lark-cli after a fix run, so the
// Jira follow-up owner sees the outcome, root cause, and change without polling.
// Disabled by default (opt-in). No secrets here: lark-cli owns Lark auth; Target
// is a non-secret routing value (a Lark chat_id / open_id) used as the fallback
// recipient when the Jira assignee cannot be resolved.
type NotifyConfig struct {
	Enabled bool   `json:"enabled"`
	Channel string `json:"channel"`
	Target  string `json:"target"`
}

// InteractConfig controls the optional bidirectional Feishu interaction subsystem.
// When Enabled, the poller daemon runs a long-lived inbound listener that consumes
// Lark card callbacks (via lark-cli) so a human can answer an agent's needs-info
// questions from an interactive card, and the answer re-triggers the fix. Disabled
// by default (opt-in). No secrets here: lark-cli owns Lark auth.
//
// AuthorizedOpenIDs is the allowlist of Lark open_ids permitted to act on cards.
// Authorization is checked ONLY against the callback's server-delivered operator_id,
// never against card-carried values (which are untrusted); so at least one open_id
// is required when Enabled. TimeoutHours bounds how long an unanswered interaction
// stays pending before it is given up (0 = a built-in default).
type InteractConfig struct {
	Enabled           bool     `json:"enabled"`
	AuthorizedOpenIDs []string `json:"authorizedOpenIds"`
	// Approval adds a human MR-approval gate AFTER the AI verifier upholds a fix:
	// the proposal is held (StatusAwaitingApproval) and an approve/reject card is
	// sent; only an authorized approve opens the MR. Requires verify.enabled (it
	// gates the verified two-phase execute) and interact.enabled (the listener
	// receives the callback).
	Approval     bool `json:"approval"`
	TimeoutHours int  `json:"timeoutHours"`
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
const DefaultKnowledgeDir = ".repo-knowledge"
const DefaultKnowledgeHandoffDir = "handoff"
const DefaultInteractTimeoutHours = 24

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
	cfg.Agent.Model = substituteEnv(cfg.Agent.Model, missing)
	cfg.Poll.Filter.TitleContains = substituteEnv(cfg.Poll.Filter.TitleContains, missing)
	cfg.Workspace.Root = substituteEnv(cfg.Workspace.Root, missing)
	cfg.Workspace.Cleanup = substituteEnv(cfg.Workspace.Cleanup, missing)
	cfg.Knowledge.Dir = substituteEnv(cfg.Knowledge.Dir, missing)
	cfg.Knowledge.HandoffDir = substituteEnv(cfg.Knowledge.HandoffDir, missing)
	cfg.Verify.Command = substituteEnv(cfg.Verify.Command, missing)
	cfg.Notify.Target = substituteEnv(cfg.Notify.Target, missing)
	for i := range cfg.Interact.AuthorizedOpenIDs {
		cfg.Interact.AuthorizedOpenIDs[i] = substituteEnv(cfg.Interact.AuthorizedOpenIDs[i], missing)
	}

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
	if cfg.Interact.TimeoutHours == 0 {
		cfg.Interact.TimeoutHours = DefaultInteractTimeoutHours
	}
	if cfg.Interact.AuthorizedOpenIDs == nil {
		cfg.Interact.AuthorizedOpenIDs = []string{}
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

// KnownAgentType reports whether agentType is one whose launch command can be
// derived automatically (so agent.command may be omitted).
func KnownAgentType(agentType string) bool {
	switch agentType {
	case "kiro", "cursor", "claude-code", "codex":
		return true
	default:
		return false
	}
}

// Validate checks required fields and constraints. It deliberately does NOT
// require jira/gitlab/kibana hosts or tokens: authentication and connectivity
// are each sibling CLI's own responsibility (jira-cli/gitlab-cli/kibana-cli),
// verified at runtime by `auto-bug-fix doctor`, not stored in this config.
func Validate(cfg Config) error {
	// agentType is primary: a known type derives its command at runtime, so an
	// explicit agent.command is only required for a custom/unknown agent.
	if strings.TrimSpace(cfg.Agent.Command) == "" && !KnownAgentType(cfg.Agent.AgentType) {
		return fmt.Errorf("agent.agentType must be one of kiro/cursor/claude-code/codex, or set a custom agent.command")
	}
	if cfg.Agent.AgentType != "" && !KnownAgentType(cfg.Agent.AgentType) {
		return fmt.Errorf("agent.agentType must be \"kiro\", \"cursor\", \"claude-code\", \"codex\", or empty")
	}
	// A known agentType must pin its model: the spawned agent should not silently
	// fall back to each CLI's default model on an unattended fix.
	if KnownAgentType(cfg.Agent.AgentType) && strings.TrimSpace(cfg.Agent.Model) == "" {
		return fmt.Errorf("agent.model is required for agentType %q — specify the model the agent must use", cfg.Agent.AgentType)
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
	// A custom agent cannot have its verifier command derived, so require it
	// explicitly when the gate is on. A known agentType derives it at runtime.
	if cfg.Verify.Enabled && strings.TrimSpace(cfg.Verify.Command) == "" && !KnownAgentType(cfg.Agent.AgentType) {
		return fmt.Errorf("verify.command is required when verify.enabled is true and agentType is custom")
	}
	// The completion notification is the human hand-off at this stage, so when it
	// is enabled a guaranteed fallback recipient is required: the agent normally
	// resolves the Jira assignee at runtime, but notify.target must exist so a run
	// always has somewhere to deliver to.
	if cfg.Notify.Enabled && strings.TrimSpace(cfg.Notify.Target) == "" {
		return fmt.Errorf("notify.target is required when notify.enabled is true (fallback recipient); set notify.target or set notify.enabled=false")
	}
	// The interaction listener authorizes actions solely on the callback's
	// operator_id, so an empty allowlist would let no one act (and, worse, invites
	// trusting card-carried values). Require at least one authorized open_id.
	if cfg.Interact.Enabled && len(cfg.Interact.AuthorizedOpenIDs) == 0 {
		return fmt.Errorf("interact.authorizedOpenIds must list at least one open_id when interact.enabled is true (only these users may act on cards)")
	}
	if cfg.Interact.TimeoutHours < 0 {
		return fmt.Errorf("interact.timeoutHours must be >= 0")
	}
	if cfg.Interact.Approval && !cfg.Interact.Enabled {
		return fmt.Errorf("interact.approval requires interact.enabled=true (the listener receives the approve/reject callback)")
	}
	if cfg.Interact.Approval && !cfg.Verify.Enabled {
		return fmt.Errorf("interact.approval requires verify.enabled=true (approval gates the verified two-phase execute)")
	}
	if cfg.Interact.Approval && !cfg.Notify.Enabled {
		return fmt.Errorf("interact.approval requires notify.enabled=true (the approve/reject card is delivered via the notification channel; without it the fix would hang at awaiting-approval)")
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
