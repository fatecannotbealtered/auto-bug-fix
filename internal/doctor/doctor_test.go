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

// probeFake returns canned `doctor --json` output per binary. A binary mapped to
// "" simulates a non-zero exit with no JSON (unusable / unauthenticated).
func probeFake(out map[string]string) Probe {
	return func(bin string, _ ...string) ([]byte, error) {
		if body, ok := out[bin]; ok && body != "" {
			return []byte(body), nil
		}
		return nil, errors.New("exit 1")
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

const authOK = `{"authValid":true,"host":"https://jira.example.com"}`

func allPresent() map[string]bool {
	return map[string]bool{"kiro-cli": true, "git": true, "jira-cli": true, "gitlab-cli": true, "kibana-cli": true}
}

func TestRun_AllUsable(t *testing.T) {
	probe := probeFake(map[string]string{"jira-cli": authOK, "gitlab-cli": authOK, "kibana-cli": authOK})
	checks := Run(cfgWith("kiro-cli chat \"fix {issueKey}\""), nil, lookFake(allPresent()), probe)
	if HasFailure(checks) {
		t.Fatalf("expected no failure, got %+v", checks)
	}
	if got := levelOf(checks, "jira-cli"); got != OK {
		t.Errorf("jira-cli should be OK, got %v", got)
	}
}

func TestRun_RequiredCliMissingFails(t *testing.T) {
	look := lookFake(map[string]bool{"kiro-cli": true, "git": true, "gitlab-cli": true, "kibana-cli": true}) // jira-cli absent
	probe := probeFake(map[string]string{"gitlab-cli": authOK, "kibana-cli": authOK})
	checks := Run(cfgWith("kiro-cli"), nil, look, probe)
	if !HasFailure(checks) {
		t.Fatal("expected failure when a required CLI is missing")
	}
	if got := levelOf(checks, "jira-cli"); got != Fail {
		t.Errorf("jira-cli should Fail, got %v", got)
	}
}

func TestRun_RequiredCliUnauthenticatedFails(t *testing.T) {
	// present on PATH but doctor reports not usable (no authValid)
	probe := probeFake(map[string]string{"gitlab-cli": authOK, "kibana-cli": authOK}) // jira-cli -> exit 1
	checks := Run(cfgWith("kiro-cli"), nil, lookFake(allPresent()), probe)
	if !HasFailure(checks) {
		t.Fatal("expected failure when a required CLI is not authenticated")
	}
	if got := levelOf(checks, "jira-cli"); got != Fail {
		t.Errorf("unauthenticated jira-cli should Fail, got %v", got)
	}
}

func TestRun_OptionalKibanaUnusableWarnsOnly(t *testing.T) {
	probe := probeFake(map[string]string{"jira-cli": authOK, "gitlab-cli": authOK}) // kibana-cli -> exit 1
	checks := Run(cfgWith("kiro-cli"), nil, lookFake(allPresent()), probe)
	if HasFailure(checks) {
		t.Fatalf("optional kibana-cli unusable must not fail, got %+v", checks)
	}
	if got := levelOf(checks, "kibana-cli"); got != Warn {
		t.Errorf("kibana-cli should Warn, got %v", got)
	}
}

func TestRun_ConfigErrorFails(t *testing.T) {
	probe := probeFake(map[string]string{"jira-cli": authOK, "gitlab-cli": authOK, "kibana-cli": authOK})
	checks := Run(cfgWith("kiro-cli"), errors.New("agent.command is required"), lookFake(allPresent()), probe)
	if !HasFailure(checks) {
		t.Fatal("expected failure on config error")
	}
	if got := levelOf(checks, "config"); got != Fail {
		t.Errorf("config should Fail, got %v", got)
	}
}

func TestRun_EmptyCommandFails(t *testing.T) {
	probe := probeFake(map[string]string{"jira-cli": authOK, "gitlab-cli": authOK, "kibana-cli": authOK})
	checks := Run(cfgWith(""), nil, lookFake(allPresent()), probe)
	if !HasFailure(checks) {
		t.Fatal("expected failure when agent.command is empty")
	}
	if got := levelOf(checks, "agent CLI"); got != Fail {
		t.Errorf("agent CLI should Fail on empty command, got %v", got)
	}
}
