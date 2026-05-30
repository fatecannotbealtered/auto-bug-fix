// Package doctor runs preflight checks. It verifies config validity, that the
// agent CLI and git are on PATH, and that the capability CLIs (jira-cli,
// gitlab-cli, kibana-cli) are actually usable by delegating to each one's own
// `doctor --json` (authentication/connectivity is each CLI's responsibility, not
// stored in this tool's config).
package doctor

import (
	"encoding/json"

	"github.com/fatecannotbealtered/auto-bug-fix/internal/agent"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/config"
)

type Level int

const (
	OK Level = iota
	Warn
	Fail
)

func (l Level) String() string {
	switch l {
	case OK:
		return "OK"
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

// cliHealth is the JSON shape shared by the sibling CLIs' `doctor --json`.
type cliHealth struct {
	AuthValid bool   `json:"authValid"`
	Host      string `json:"host"`
}

// Run returns the preflight checks for cfg. cfgErr is the error from loading and
// validating config (nil when it loaded and validated cleanly).
func Run(cfg config.Config, cfgErr error, look LookPath, probe Probe, tmpl TemplateProbe) []Check {
	checks := []Check{configCheck(cfgErr)}

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

	checks = append(checks,
		lookCheck(look, "git", "git", true),
		capabilityCheck(look, probe, "jira-cli", true),
		capabilityCheck(look, probe, "gitlab-cli", true),
		capabilityCheck(look, probe, "kibana-cli", false),
	)
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
	var h cliHealth
	_ = json.Unmarshal(out, &h)
	if h.AuthValid {
		detail := "authenticated"
		if h.Host != "" {
			detail += ": " + h.Host
		}
		return Check{bin, OK, detail}
	}
	if perr != nil {
		return Check{bin, failLevel(required), "not usable; run `" + bin + " doctor`"}
	}
	return Check{bin, failLevel(required), "not authenticated; run `" + bin + " auth login`"}
}

func failLevel(required bool) Level {
	if required {
		return Fail
	}
	return Warn
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
