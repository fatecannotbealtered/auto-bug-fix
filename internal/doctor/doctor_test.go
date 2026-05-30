package doctor

import (
	"errors"
	"testing"

	"github.com/fatecannotbealtered/auto-bug-fix/internal/config"
)

func lookFake(present map[string]bool) LookPath {
	return func(bin string) (string, error) {
		if present[bin] {
			return "/usr/bin/" + bin, nil
		}
		return "", errors.New("not found")
	}
}

func levelOf(checks []Check, name string) Level {
	for _, c := range checks {
		if c.Name == name {
			return c.Level
		}
	}
	return -1
}

func cfgWith(command string) config.Config {
	return config.Config{Agent: config.AgentConfig{Command: command}}
}

func TestRun_AllPresent(t *testing.T) {
	look := lookFake(map[string]bool{"kiro-cli": true, "git": true, "jira-cli": true, "gitlab-cli": true, "kibana-cli": true})
	checks := Run(cfgWith("kiro-cli chat \"fix {issueKey}\""), nil, look)
	if HasFailure(checks) {
		t.Fatalf("expected no failure, got %+v", checks)
	}
	if got := levelOf(checks, "agent CLI (kiro-cli)"); got != OK {
		t.Errorf("agent CLI should be OK, got %v", got)
	}
}

func TestRun_RequiredMissingFails(t *testing.T) {
	look := lookFake(map[string]bool{"kiro-cli": true, "git": true, "kibana-cli": true}) // jira-cli & gitlab-cli missing
	checks := Run(cfgWith("kiro-cli"), nil, look)
	if !HasFailure(checks) {
		t.Fatal("expected failure when required CLIs are missing")
	}
	if got := levelOf(checks, "jira-cli"); got != Fail {
		t.Errorf("jira-cli should Fail, got %v", got)
	}
}

func TestRun_OptionalMissingWarnsOnly(t *testing.T) {
	look := lookFake(map[string]bool{"kiro-cli": true, "git": true, "jira-cli": true, "gitlab-cli": true}) // kibana-cli missing
	checks := Run(cfgWith("kiro-cli"), nil, look)
	if HasFailure(checks) {
		t.Fatalf("optional kibana-cli missing must not fail, got %+v", checks)
	}
	if got := levelOf(checks, "kibana-cli"); got != Warn {
		t.Errorf("kibana-cli should Warn, got %v", got)
	}
}

func TestRun_ConfigErrorFails(t *testing.T) {
	look := lookFake(map[string]bool{"kiro-cli": true, "git": true, "jira-cli": true, "gitlab-cli": true, "kibana-cli": true})
	checks := Run(cfgWith("kiro-cli"), errors.New("missing jira.host"), look)
	if !HasFailure(checks) {
		t.Fatal("expected failure on config error")
	}
	if got := levelOf(checks, "config"); got != Fail {
		t.Errorf("config should Fail, got %v", got)
	}
}

func TestRun_EmptyCommandFails(t *testing.T) {
	look := lookFake(map[string]bool{"git": true, "jira-cli": true, "gitlab-cli": true, "kibana-cli": true})
	checks := Run(cfgWith(""), nil, look)
	if !HasFailure(checks) {
		t.Fatal("expected failure when agent.command is empty")
	}
	if got := levelOf(checks, "agent CLI"); got != Fail {
		t.Errorf("agent CLI should Fail on empty command, got %v", got)
	}
}
