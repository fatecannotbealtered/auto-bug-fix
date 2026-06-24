// Package doctor runs preflight checks. It verifies config validity, that the
// agent CLI and git are on PATH, and that the capability CLIs (jira-cli,
// gitlab-cli, kibana-cli) are actually usable by delegating to each one's own
// `doctor --json` (authentication/connectivity is each CLI's responsibility, not
// stored in this tool's config).
package doctor

import (
	"encoding/json"
	"strings"

	"github.com/fatecannotbealtered/auto-bug-fix/internal/agent"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/config"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/installer"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/notify"
)

type Level int

const (
	OK Level = iota
	Info
	Warn
	Fail
)

func (l Level) String() string {
	switch l {
	case OK:
		return "OK"
	case Info:
		return "INFO"
	case Warn:
		return "WARN"
	default:
		return "FAIL"
	}
}

// Check is a single preflight result.
type Check struct {
	Name   string
	Level  Level
	Detail string
}

// LookPath matches exec.LookPath; injectable for tests.
type LookPath func(string) (string, error)

// Probe runs a CLI and returns its stdout; injectable for tests. Implementations
// should return stdout even on a non-zero exit so JSON output can be parsed.
type Probe func(bin string, args ...string) ([]byte, error)

// TemplateProbe reports which subagent template files are missing for agentType,
// and whether agentType is one we can verify (false for empty/custom commands).
type TemplateProbe func(agentType string) (missing []string, verifiable bool)

// SkillProbe reports the agent's own skill directory plus which required and
// optional CLI skills (jira-cli/gitlab-cli/kibana-cli) are missing from it, and
// whether agentType is verifiable (false for empty/custom commands).
type SkillProbe func(agentType string) (dir string, missingRequired, missingOptional []string, verifiable bool)

// cliHealth is the JSON shape shared by the sibling CLIs' `doctor --json`.
type cliHealth struct {
	AuthValid bool   `json:"authValid"`
	Host      string `json:"host"`
}

type cliDoctorCheck struct {
	Check  string `json:"check"`
	Status string `json:"status"`
}

type cliHealthData struct {
	AuthValid bool             `json:"authValid"`
	Host      string           `json:"host"`
	Checks    []cliDoctorCheck `json:"checks"`
}

type cliHealthEnvelope struct {
	OK   bool          `json:"ok"`
	Data cliHealthData `json:"data"`
}

func parseCLIHealth(out []byte) (cliHealth, error) {
	var h cliHealth
	if err := json.Unmarshal(out, &h); err != nil {
		return cliHealth{}, err
	}
	if h.AuthValid || h.Host != "" {
		return h, nil
	}
	var env cliHealthEnvelope
	if err := json.Unmarshal(out, &env); err != nil {
		return cliHealth{}, err
	}
	if len(env.Data.Checks) > 0 {
		authPass := false
		networkPass := false
		for _, check := range env.Data.Checks {
			name := strings.ToLower(check.Check)
			status := strings.ToLower(check.Status)
			switch name {
			case "auth":
				authPass = status == "pass"
			case "network":
				networkPass = status == "pass"
			}
		}
		return cliHealth{AuthValid: authPass && networkPass, Host: env.Data.Host}, nil
	}
	if env.Data.AuthValid || env.Data.Host != "" {
		return cliHealth{AuthValid: env.Data.AuthValid, Host: env.Data.Host}, nil
	}
	return h, nil
}

// Run returns the preflight checks for cfg. cfgErr is the error from loading and
// validating config (nil when it loaded and validated cleanly).
func Run(cfg config.Config, cfgErr error, look LookPath, probe Probe, tmpl TemplateProbe, skills SkillProbe) []Check {
	checks := []Check{configCheck(cfgErr)}

	// Config-independent checks always run — useful even when config is broken.
	checks = append(checks,
		lookCheck(look, "git", "git", true),
		capabilityCheck(look, probe, "jira-cli", true),
		capabilityCheck(look, probe, "gitlab-cli", true),
		capabilityCheck(look, probe, "kibana-cli", false),
		capabilityCheck(look, probe, "archery-cli", false),
	)

	// Config-derived checks only make sense once config loaded and validated;
	// running them on a zero/invalid config would emit misleading diagnostics.
	if cfgErr != nil {
		return checks
	}

	// Agent CLI: the binary is argv[0] of agent.command (tool-agnostic — no
	// per-agent table, whatever vibe-coding tool you configured is checked).
	if cfg.Agent.Command == "" {
		checks = append(checks, Check{"agent CLI", Fail, "agent.command is empty; run setup"})
	} else if tokens, err := agent.ParseCommand(cfg.Agent.Command); err != nil {
		checks = append(checks, Check{"agent CLI", Fail, err.Error()})
	} else {
		checks = append(checks, lookCheck(look, "agent CLI ("+tokens[0]+")", tokens[0], true))
	}

	checks = append(checks, agentTemplateCheck(tmpl, cfg.Agent.AgentType))
	checks = append(checks, agentSkillsCheck(skills, cfg.Agent.AgentType))
	if c, ok := commandDriftCheck(cfg.Agent); ok {
		checks = append(checks, c)
	}
	checks = append(checks, filterScopeCheck(cfg.Poll.Filter))

	// Completion-notification preflight: when enabled, the channel's delivery CLI
	// must be present AND usable (authenticated + connected) via its own
	// `doctor --json`. The notification is a required hand-off at this stage, so an
	// unusable channel is a Fail, not a warning.
	if cfg.Notify.Enabled {
		ch, cerr := notify.Get(cfg.Notify.Channel)
		if cerr != nil {
			checks = append(checks, Check{"notify", Fail, cerr.Error()})
		} else if bin := ch.DoctorBin(); bin != "" {
			checks = append(checks, capabilityCheck(look, probe, bin, true))
		}
	}

	// Informational: surface where repos get cloned so the user is aware of the
	// disk location (the default lives under home — C:\ on Windows). Never blocks.
	if cfg.Workspace.Root != "" {
		checks = append(checks, Check{"workspace", Info, "repos are cloned to " + cfg.Workspace.Root})
	}
	return checks
}

// agentTemplateCheck verifies the subagent workflow template is installed for the
// configured agentType. Without it the spawned agent runs but has no workflow.
func agentTemplateCheck(tmpl TemplateProbe, agentType string) Check {
	missing, verifiable := tmpl(agentType)
	if !verifiable {
		return Check{"agent template", Warn, "custom agent.command (agentType unset); cannot verify the subagent template is installed"}
	}
	if len(missing) > 0 {
		return Check{"agent template", Fail, "not installed; run `auto-bug-fix setup --agent " + agentType + "`"}
	}
	return Check{"agent template", OK, "installed for " + agentType}
}

// agentSkillsCheck verifies the sibling-CLI skills (jira-cli/gitlab-cli/kibana-cli,
// all required) are installed in the configured agent's skill directory. Without
// them the spawned agent has no usage reference and guesses CLI flags.
func agentSkillsCheck(skills SkillProbe, agentType string) Check {
	dir, missingReq, missingOpt, verifiable := skills(agentType)
	if !verifiable {
		return Check{"cli skills", Warn, "custom agent (agentType unset); cannot verify jira-cli/gitlab-cli/kibana-cli/archery-cli skills are installed"}
	}
	if len(missingReq) > 0 {
		hint := "install each into the agent's own skill dir"
		if flag := installer.SkillsAgentFlag(agentType); flag != "" {
			hint = "install each: `npx skills add fatecannotbealtered/<skill> -g -a " + flag + " -y`"
		}
		return Check{"cli skills", Fail, "missing required skill(s) " + strings.Join(missingReq, ", ") + " in " + dir + " — " + hint}
	}
	if len(missingOpt) > 0 {
		detail := "optional skill(s) missing from " + dir + ": " + strings.Join(missingOpt, ", ")
		if flag := installer.SkillsAgentFlag(agentType); flag != "" {
			detail += " — install with `npx skills add fatecannotbealtered/<skill> -g -a " + flag + " -y` if the workflow needs log (kibana) or database-state (archery) evidence"
		}
		return Check{"cli skills", Warn, detail}
	}
	return Check{"cli skills", OK, "jira-cli, gitlab-cli, kibana-cli, archery-cli installed in " + dir}
}

func configCheck(cfgErr error) Check {
	if cfgErr != nil {
		return Check{"config", Fail, cfgErr.Error()}
	}
	return Check{"config", OK, "loaded and valid"}
}

func lookCheck(look LookPath, name, bin string, required bool) Check {
	if path, err := look(bin); err == nil {
		return Check{name, OK, path}
	}
	return Check{name, failLevel(required), "not found on PATH"}
}

// capabilityCheck confirms a sibling CLI is usable: present on PATH AND reporting
// authValid via its own `doctor --json`. Auth is the CLI's own concern; we only
// verify it is ready so the spawned agent does not fail mid-fix.
func capabilityCheck(look LookPath, probe Probe, bin string, required bool) Check {
	if _, err := look(bin); err != nil {
		return Check{bin, failLevel(required), "not found on PATH"}
	}
	out, perr := probe(bin, "doctor", "--json", "--quiet")
	h, unmarshalErr := parseCLIHealth(out)
	if h.AuthValid {
		detail := "authenticated"
		if h.Host != "" {
			detail += ": " + h.Host
		}
		return Check{bin, OK, detail}
	}
	// A non-zero exit OR unparseable output both mean "can't confirm usable" —
	// reporting "not authenticated" on garbage output would be a misdiagnosis.
	if perr != nil || unmarshalErr != nil {
		return Check{bin, failLevel(required), "not usable; run `" + bin + " doctor`"}
	}
	return Check{bin, failLevel(required), "not authenticated; run `" + authLoginCommand(bin) + "`"}
}

func authLoginCommand(bin string) string {
	switch bin {
	case "jira-cli":
		return "jira-cli login"
	case "gitlab-cli":
		return "gitlab-cli auth login"
	case "kibana-cli":
		return "kibana-cli auth login"
	case "archery-cli":
		return "archery-cli auth login"
	default:
		return bin + " auth login"
	}
}

// commandDriftCheck warns when a known agentType also has an explicit
// agent.command that differs from the derived one: the explicit command wins but
// won't track template/upgrade changes. Returns ok=false (no check) otherwise.
func commandDriftCheck(a config.AgentConfig) (Check, bool) {
	if !config.KnownAgentType(a.AgentType) {
		return Check{}, false
	}
	if a.Command != "" && a.Command != installer.AgentCommand(a.AgentType, a.Model) {
		return Check{"agent command", Warn, "explicit agent.command overrides the command derived from agentType=" + a.AgentType + "; remove it to always match the installed subagent"}, true
	}
	return Check{}, false
}

func failLevel(required bool) Level {
	if required {
		return Fail
	}
	return Warn
}

// filterScopeCheck reports how wide the poller's matching scope is, so the agent
// can ask the user whether to limit it. No title AND not assignee-limited matches
// every open Bug in the instance and blocks.
func filterScopeCheck(f config.FilterConfig) Check {
	if f.TitleContains == "" && !f.AssignedToMe {
		return Check{"fix scope", Fail, "matches EVERY open Bug in the Jira instance — set poll.filter.titleContains or assignedToMe to limit scope"}
	}
	if f.TitleContains == "" {
		return Check{"fix scope", Warn, "no title filter: every open Bug assigned to you will be auto-fixed — ask the user whether to limit the scope (poll.filter.titleContains)"}
	}
	return Check{"fix scope", OK, "limited by title containing \"" + f.TitleContains + "\""}
}

// HasFailure reports whether any check failed (drives the exit code).
func HasFailure(checks []Check) bool {
	for _, c := range checks {
		if c.Level == Fail {
			return true
		}
	}
	return false
}
