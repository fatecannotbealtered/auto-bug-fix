// Package doctor runs read-only preflight checks: config validity and the
// presence of required external CLIs on PATH. It makes no network calls — each
// tool's own `doctor`/`login` owns authentication and connectivity.
package doctor

import (
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

// Run returns the preflight checks for cfg. cfgErr is the error from loading
// and validating config (nil when it loaded and validated cleanly).
func Run(cfg config.Config, cfgErr error, look LookPath) []Check {
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

	checks = append(checks,
		lookCheck(look, "git", "git", true),
		lookCheck(look, "jira-cli", "jira-cli", true),
		lookCheck(look, "gitlab-cli", "gitlab-cli", true),
		lookCheck(look, "kibana-cli", "kibana-cli", false),
	)
	return checks
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
	if required {
		return Check{name, Fail, "not found on PATH"}
	}
	return Check{name, Warn, "not found on PATH (optional)"}
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
