package doctor

import (
	"errors"
	"strings"
	"testing"

	"github.com/fatecannotbealtered/auto-bug-fix/internal/config"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/installer"
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

func TestInteractCheck_WarnsWhenLarkCLIMissing(t *testing.T) {
	cfg := config.Config{Interact: config.InteractConfig{Enabled: true}}
	c := interactCheck(lookFake(map[string]bool{}), cfg)
	if c.Level != Warn || !strings.Contains(c.Detail, "lark-cli") {
		t.Fatalf("missing lark-cli must Warn, got %+v", c)
	}
}

func TestInteractCheck_InfoReminderWhenLarkCLIPresent(t *testing.T) {
	cfg := config.Config{Interact: config.InteractConfig{Enabled: true}}
	c := interactCheck(lookFake(map[string]bool{"lark-cli": true}), cfg)
	if c.Level != Info || !strings.Contains(c.Detail, "Callback Configuration") {
		t.Fatalf("with lark-cli present, expect an Info console-config reminder, got %+v", c)
	}
}

// tmplInstalled treats a known agentType as fully installed.
func tmplInstalled(agentType string) ([]string, bool) {
	if agentType == "" {
		return nil, false
	}
	return nil, true
}

// tmplMissing treats a known agentType as present-but-template-missing.
func tmplMissing(agentType string) ([]string, bool) {
	if agentType == "" {
		return nil, false
	}
	return []string{"/home/.kiro/agents/auto-bug-fix.json"}, true
}

// skillsInstalled treats a known agentType as having all CLI skills installed.
func skillsInstalled(agentType string) (string, []string, []string, bool) {
	if agentType == "" {
		return "", nil, nil, false
	}
	return "/home/.kiro/skills", nil, nil, true
}

// skillsMissingRequired simulates a missing required CLI skill (jira-cli).
func skillsMissingRequired(agentType string) (string, []string, []string, bool) {
	return "/home/.kiro/skills", []string{"jira-cli"}, nil, true
}

// skillsMissingOptional simulates only the optional kibana-cli skill missing.
func skillsMissingOptional(agentType string) (string, []string, []string, bool) {
	return "/home/.kiro/skills", nil, []string{"kibana-cli"}, true
}

func levelOf(checks []Check, name string) Level {
	for _, c := range checks {
		if c.Name == name {
			return c.Level
		}
	}
	return -1
}

func detailOf(checks []Check, name string) string {
	for _, c := range checks {
		if c.Name == name {
			return c.Detail
		}
	}
	return ""
}

func cfgWith(command, agentType string) config.Config {
	return config.Config{
		Agent: config.AgentConfig{Command: command, AgentType: agentType},
		Poll:  config.PollConfig{Filter: config.FilterConfig{TitleContains: "bug"}},
	}
}

const authOK = `{"authValid":true,"host":"https://jira.example.com"}`

// lark-cli is NOT a fateforge sibling: its `doctor` ignores --json and emits a
// flat {ok, checks, _notice} (no data/authValid envelope). These fixtures use that
// REAL shape — the v1.0.10 bug was that tests fed the fateforge authOK shape.
const larkOK = `{"ok":true,"checks":[{"name":"identity_ready","status":"pass"}],"_notice":{"update":{"message":"1.0.57 available"}}}`
const larkUnauthed = `{"ok":false,"checks":[{"name":"identity_ready","status":"fail"}]}`

func allPresent() map[string]bool {
	return map[string]bool{"kiro-cli": true, "git": true, "jira-cli": true, "gitlab-cli": true, "kibana-cli": true}
}

func allAuthed() map[string]string {
	return map[string]string{"jira-cli": authOK, "gitlab-cli": authOK, "kibana-cli": authOK}
}

func TestRun_CapabilityDoctorEnvelope(t *testing.T) {
	enveloped := `{"ok":true,"schema_version":"1.0","data":{"authValid":true,"host":"https://jira.example.com"},"meta":{"duration_ms":1}}`
	checks := Run(cfgWith("kiro-cli chat \"fix {issueKey}\"", "kiro"), nil, lookFake(allPresent()), probeFake(map[string]string{
		"jira-cli":   enveloped,
		"gitlab-cli": enveloped,
		"kibana-cli": enveloped,
	}), tmplInstalled, skillsInstalled)
	if HasFailure(checks) {
		t.Fatalf("expected enveloped sibling CLI doctor output to pass, got %+v", checks)
	}
	if got := levelOf(checks, "gitlab-cli"); got != OK {
		t.Errorf("gitlab-cli should be OK from enveloped doctor output, got %v", got)
	}
}

func TestRun_JiraDoctorChecksEnvelope(t *testing.T) {
	jiraDoctor := `{"ok":true,"schema_version":"1.0","data":{"checks":[{"check":"auth","status":"pass"},{"check":"network","status":"pass"}],"host":"https://jira.example.com"},"meta":{"duration_ms":1}}`
	checks := Run(cfgWith("kiro-cli chat \"fix {issueKey}\"", "kiro"), nil, lookFake(allPresent()), probeFake(map[string]string{
		"jira-cli":   jiraDoctor,
		"gitlab-cli": authOK,
		"kibana-cli": authOK,
	}), tmplInstalled, skillsInstalled)
	if HasFailure(checks) {
		t.Fatalf("expected Jira checks envelope to pass, got %+v", checks)
	}
	if got := levelOf(checks, "jira-cli"); got != OK {
		t.Errorf("jira-cli should be OK from auth/network checks, got %v", got)
	}
}

func TestRun_JiraDoctorChecksAuthFail(t *testing.T) {
	jiraDoctor := `{"ok":true,"schema_version":"1.0","data":{"checks":[{"check":"auth","status":"fail"},{"check":"network","status":"pass"}],"host":"https://jira.example.com"},"meta":{"duration_ms":1}}`
	checks := Run(cfgWith("kiro-cli chat \"fix {issueKey}\"", "kiro"), nil, lookFake(allPresent()), probeFake(map[string]string{
		"jira-cli":   jiraDoctor,
		"gitlab-cli": authOK,
		"kibana-cli": authOK,
	}), tmplInstalled, skillsInstalled)
	if !HasFailure(checks) {
		t.Fatalf("expected Jira auth failure to fail, got %+v", checks)
	}
	if got := levelOf(checks, "jira-cli"); got != Fail {
		t.Errorf("jira-cli should Fail from auth check, got %v", got)
	}
}

func TestRun_KibanaDoctorChecksAuthValidWithoutNetworkCheck(t *testing.T) {
	// kibana-cli names its connectivity check "search", not "network", and reports
	// an authoritative data.authValid. The auth/network heuristic must not override
	// that verdict, or kibana is misreported as unauthenticated.
	kibanaDoctor := `{"ok":true,"schema_version":"1.0","data":{"checks":[{"check":"auth","status":"pass"},{"check":"search","status":"pass"}],"authValid":true,"host":"https://kibana.example.com"},"meta":{"duration_ms":1}}`
	checks := Run(cfgWith("kiro-cli chat \"fix {issueKey}\"", "kiro"), nil, lookFake(allPresent()), probeFake(map[string]string{
		"jira-cli":   authOK,
		"gitlab-cli": authOK,
		"kibana-cli": kibanaDoctor,
	}), tmplInstalled, skillsInstalled)
	if got := levelOf(checks, "kibana-cli"); got != OK {
		t.Errorf("kibana-cli should be OK from data.authValid despite no 'network' check, got %v (detail %q)", got, detailOf(checks, "kibana-cli"))
	}
}

func TestRun_AllUsable(t *testing.T) {
	checks := Run(cfgWith("kiro-cli chat \"fix {issueKey}\"", "kiro"), nil, lookFake(allPresent()), probeFake(allAuthed()), tmplInstalled, skillsInstalled)
	if HasFailure(checks) {
		t.Fatalf("expected no failure, got %+v", checks)
	}
	if got := levelOf(checks, "jira-cli"); got != OK {
		t.Errorf("jira-cli should be OK, got %v", got)
	}
	if got := levelOf(checks, "agent template"); got != OK {
		t.Errorf("agent template should be OK, got %v", got)
	}
}

func TestRun_RequiredCliMissingFails(t *testing.T) {
	look := lookFake(map[string]bool{"kiro-cli": true, "git": true, "gitlab-cli": true, "kibana-cli": true}) // jira-cli absent
	checks := Run(cfgWith("kiro-cli", "kiro"), nil, look, probeFake(allAuthed()), tmplInstalled, skillsInstalled)
	if !HasFailure(checks) {
		t.Fatal("expected failure when a required CLI is missing")
	}
	if got := levelOf(checks, "jira-cli"); got != Fail {
		t.Errorf("jira-cli should Fail, got %v", got)
	}
}

func TestRun_RequiredCliUnauthenticatedFails(t *testing.T) {
	probe := probeFake(map[string]string{"gitlab-cli": authOK, "kibana-cli": authOK}) // jira-cli -> exit 1
	checks := Run(cfgWith("kiro-cli", "kiro"), nil, lookFake(allPresent()), probe, tmplInstalled, skillsInstalled)
	if !HasFailure(checks) {
		t.Fatal("expected failure when a required CLI is not authenticated")
	}
	if got := levelOf(checks, "jira-cli"); got != Fail {
		t.Errorf("unauthenticated jira-cli should Fail, got %v", got)
	}
}

func TestRun_JiraUnauthenticatedHint(t *testing.T) {
	checks := Run(cfgWith("kiro-cli", "kiro"), nil, lookFake(allPresent()), probeFake(map[string]string{
		"jira-cli":   `{"ok":true,"schema_version":"1.0","data":{"checks":[{"check":"auth","status":"fail"},{"check":"network","status":"pass"}],"host":"https://jira.example.com"}}`,
		"gitlab-cli": authOK,
		"kibana-cli": authOK,
	}), tmplInstalled, skillsInstalled)
	if !HasFailure(checks) {
		t.Fatal("expected failure when jira-cli reports auth failure")
	}
	if got := detailOf(checks, "jira-cli"); !strings.Contains(got, "jira-cli login") {
		t.Errorf("jira-cli auth hint should use jira-cli login, got %q", got)
	}
}

func TestRun_OptionalKibanaUnusableWarnsOnly(t *testing.T) {
	probe := probeFake(map[string]string{"jira-cli": authOK, "gitlab-cli": authOK}) // kibana-cli -> exit 1
	checks := Run(cfgWith("kiro-cli", "kiro"), nil, lookFake(allPresent()), probe, tmplInstalled, skillsInstalled)
	if HasFailure(checks) {
		t.Fatalf("optional kibana-cli unusable must not fail, got %+v", checks)
	}
	if got := levelOf(checks, "kibana-cli"); got != Warn {
		t.Errorf("kibana-cli should Warn, got %v", got)
	}
}

func TestRun_TemplateMissingFails(t *testing.T) {
	checks := Run(cfgWith("kiro-cli", "kiro"), nil, lookFake(allPresent()), probeFake(allAuthed()), tmplMissing, skillsInstalled)
	if !HasFailure(checks) {
		t.Fatal("expected failure when the subagent template is not installed")
	}
	if got := levelOf(checks, "agent template"); got != Fail {
		t.Errorf("missing template should Fail, got %v", got)
	}
}

func TestRun_TemplateUnverifiableWarnsOnly(t *testing.T) {
	// empty agentType (custom command) — cannot verify, must not fail
	checks := Run(cfgWith("kiro-cli run {issueKey}", ""), nil, lookFake(allPresent()), probeFake(allAuthed()), tmplInstalled, skillsInstalled)
	if HasFailure(checks) {
		t.Fatalf("unverifiable template must not fail, got %+v", checks)
	}
	if got := levelOf(checks, "agent template"); got != Warn {
		t.Errorf("unverifiable template should Warn, got %v", got)
	}
}

func TestRun_MissingRequiredSkillFails(t *testing.T) {
	checks := Run(cfgWith("kiro-cli", "kiro"), nil, lookFake(allPresent()), probeFake(allAuthed()), tmplInstalled, skillsMissingRequired)
	if !HasFailure(checks) {
		t.Fatal("expected failure when a required CLI skill is not installed")
	}
	if got := levelOf(checks, "cli skills"); got != Fail {
		t.Errorf("missing required skill should Fail, got %v", got)
	}
}

func TestRun_MissingOptionalSkillWarnsOnly(t *testing.T) {
	checks := Run(cfgWith("kiro-cli", "kiro"), nil, lookFake(allPresent()), probeFake(allAuthed()), tmplInstalled, skillsMissingOptional)
	if HasFailure(checks) {
		t.Fatalf("missing optional (kibana-cli) skill must not fail, got %+v", checks)
	}
	if got := levelOf(checks, "cli skills"); got != Warn {
		t.Errorf("missing optional skill should Warn, got %v", got)
	}
}

func TestRun_SkillsUnverifiableWarnsOnly(t *testing.T) {
	checks := Run(cfgWith("kiro-cli run {issueKey}", ""), nil, lookFake(allPresent()), probeFake(allAuthed()), tmplInstalled, skillsInstalled)
	if HasFailure(checks) {
		t.Fatalf("unverifiable skills must not fail, got %+v", checks)
	}
	if got := levelOf(checks, "cli skills"); got != Warn {
		t.Errorf("unverifiable skills should Warn, got %v", got)
	}
}
func TestRun_FilterUnscopedFails(t *testing.T) {
	cfg := config.Config{
		Agent: config.AgentConfig{Command: "kiro-cli", AgentType: "kiro"},
		Poll:  config.PollConfig{Filter: config.FilterConfig{TitleContains: "", AssignedToMe: false}},
	}
	checks := Run(cfg, nil, lookFake(allPresent()), probeFake(allAuthed()), tmplInstalled, skillsInstalled)
	if !HasFailure(checks) {
		t.Fatal("unscoped filter (no title, not assignee-limited) must fail")
	}
	if got := levelOf(checks, "fix scope"); got != Fail {
		t.Errorf("fix scope should Fail, got %v", got)
	}
}

func TestRun_FilterBroadWarnsOnly(t *testing.T) {
	cfg := config.Config{
		Agent: config.AgentConfig{Command: "kiro-cli", AgentType: "kiro"},
		Poll:  config.PollConfig{Filter: config.FilterConfig{TitleContains: "", AssignedToMe: true}},
	}
	checks := Run(cfg, nil, lookFake(allPresent()), probeFake(allAuthed()), tmplInstalled, skillsInstalled)
	if HasFailure(checks) {
		t.Fatalf("assignee-limited filter must not fail, got %+v", checks)
	}
	if got := levelOf(checks, "fix scope"); got != Warn {
		t.Errorf("broad filter should Warn, got %v", got)
	}
}

func TestRun_WorkspaceSurfacedAsInfo(t *testing.T) {
	cfg := cfgWith("kiro-cli", "kiro")
	cfg.Workspace.Root = "/data/abf/workspaces"
	checks := Run(cfg, nil, lookFake(allPresent()), probeFake(allAuthed()), tmplInstalled, skillsInstalled)
	if HasFailure(checks) {
		t.Fatalf("workspace info must not fail, got %+v", checks)
	}
	if got := levelOf(checks, "workspace"); got != Info {
		t.Errorf("workspace should be INFO, got %v", got)
	}
}

func TestRun_CommandDriftWarns(t *testing.T) {
	// known agentType but an explicit command that differs from the derived one
	cfg := cfgWith("kiro-cli --custom \"{issueKey}\"", "kiro")
	checks := Run(cfg, nil, lookFake(allPresent()), probeFake(allAuthed()), tmplInstalled, skillsInstalled)
	if HasFailure(checks) {
		t.Fatalf("command drift is a warning, not a failure: %+v", checks)
	}
	if got := levelOf(checks, "agent command"); got != Warn {
		t.Errorf("explicit override should Warn, got %v", got)
	}
}

func TestRun_DerivedCommandNoDrift(t *testing.T) {
	cfg := cfgWith(installer.AgentCommand("kiro", ""), "kiro")
	checks := Run(cfg, nil, lookFake(allPresent()), probeFake(allAuthed()), tmplInstalled, skillsInstalled)
	if got := levelOf(checks, "agent command"); got != -1 {
		t.Errorf("derived command should add no drift check, got level %v", got)
	}
}

func TestRun_ConfigErrorFails(t *testing.T) {
	checks := Run(cfgWith("kiro-cli", "kiro"), errors.New("agent.command is required"), lookFake(allPresent()), probeFake(allAuthed()), tmplInstalled, skillsInstalled)
	if !HasFailure(checks) {
		t.Fatal("expected failure on config error")
	}
	if got := levelOf(checks, "config"); got != Fail {
		t.Errorf("config should Fail, got %v", got)
	}
}

func TestRun_EmptyCommandFails(t *testing.T) {
	checks := Run(cfgWith("", "kiro"), nil, lookFake(allPresent()), probeFake(allAuthed()), tmplInstalled, skillsInstalled)
	if !HasFailure(checks) {
		t.Fatal("expected failure when agent.command is empty")
	}
	if got := levelOf(checks, "agent CLI"); got != Fail {
		t.Errorf("agent CLI should Fail on empty command, got %v", got)
	}
}

func TestRun_NotifyEnabledLarkUsableOK(t *testing.T) {
	cfg := cfgWith("kiro-cli", "kiro")
	cfg.Notify.Enabled = true
	present := allPresent()
	present["lark-cli"] = true
	probe := allAuthed()
	probe["lark-cli"] = larkOK
	checks := Run(cfg, nil, lookFake(present), probeFake(probe), tmplInstalled, skillsInstalled)
	if HasFailure(checks) {
		t.Fatalf("notify must pass when lark-cli is present and authed, got %+v", checks)
	}
	if got := levelOf(checks, "lark-cli"); got != OK {
		t.Errorf("lark-cli should be OK when present and authed, got %v", got)
	}
}

func TestRun_NotifyEnabledLarkMissingIsAdvisory(t *testing.T) {
	cfg := cfgWith("kiro-cli", "kiro")
	cfg.Notify.Enabled = true
	// allPresent() has no lark-cli, so the binary is absent. Notifications are
	// best-effort, so a missing channel CLI is advisory (Warn) and must NOT block.
	checks := Run(cfg, nil, lookFake(allPresent()), probeFake(allAuthed()), tmplInstalled, skillsInstalled)
	if HasFailure(checks) {
		t.Fatalf("a missing notify CLI must not block fix (advisory), got %+v", checks)
	}
	if got := levelOf(checks, "lark-cli"); got != Warn {
		t.Errorf("missing lark-cli should Warn when notify enabled, got %v", got)
	}
}

func TestRun_NotifyLarkUnauthenticatedDoesNotBlock(t *testing.T) {
	cfg := cfgWith("kiro-cli", "kiro")
	cfg.Notify.Enabled = true
	present := allPresent()
	present["lark-cli"] = true
	probe := allAuthed()
	probe["lark-cli"] = larkUnauthed
	checks := Run(cfg, nil, lookFake(present), probeFake(probe), tmplInstalled, skillsInstalled)
	if HasFailure(checks) {
		t.Fatalf("an unauthenticated lark-cli must not block fix (advisory), got %+v", checks)
	}
	if got := levelOf(checks, "lark-cli"); got != Warn {
		t.Errorf("unauthenticated lark-cli should Warn, got %v", got)
	}
}

func TestRun_NotifyLarkDoctorGetsNoJSONFlag(t *testing.T) {
	// Regression guard: lark-cli rejects --json; the channel health check must run
	// `lark-cli doctor` with NO --json (the v1.0.10 bug passed --json and broke it).
	cfg := cfgWith("kiro-cli", "kiro")
	cfg.Notify.Enabled = true
	present := allPresent()
	present["lark-cli"] = true
	probe := func(bin string, args ...string) ([]byte, error) {
		if bin == "lark-cli" {
			for _, a := range args {
				if a == "--json" {
					return nil, errors.New("unknown flag --json")
				}
			}
			return []byte(larkOK), nil
		}
		return []byte(authOK), nil
	}
	checks := Run(cfg, nil, lookFake(present), probe, tmplInstalled, skillsInstalled)
	if got := levelOf(checks, "lark-cli"); got != OK {
		t.Errorf("lark-cli health check must not pass --json; got level %v", got)
	}
}

func TestRun_NotifyUnknownChannelIsAdvisory(t *testing.T) {
	// A misconfigured notify.channel (notify.Get fails) must also be advisory —
	// surfaced as a Warn but never blocking the fix.
	cfg := cfgWith("kiro-cli", "kiro")
	cfg.Notify.Enabled = true
	cfg.Notify.Channel = "nonexistent"
	checks := Run(cfg, nil, lookFake(allPresent()), probeFake(allAuthed()), tmplInstalled, skillsInstalled)
	if HasFailure(checks) {
		t.Fatalf("unknown notify channel must not block fix (advisory), got %+v", checks)
	}
	if got := levelOf(checks, "notify"); got != Warn {
		t.Errorf("unknown notify channel should Warn, got %v", got)
	}
}

func TestRun_NotifyDisabledNoCheck(t *testing.T) {
	cfg := cfgWith("kiro-cli", "kiro") // Notify.Enabled defaults false
	checks := Run(cfg, nil, lookFake(allPresent()), probeFake(allAuthed()), tmplInstalled, skillsInstalled)
	if got := levelOf(checks, "lark-cli"); got != -1 {
		t.Errorf("lark-cli check should be absent when notify is disabled, got level %v", got)
	}
}
