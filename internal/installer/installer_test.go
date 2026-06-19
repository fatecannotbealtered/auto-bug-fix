package installer_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fatecannotbealtered/auto-bug-fix/internal/config"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/installer"
)

func useTempWorkingDir(t *testing.T) {
	t.Helper()
	wd := t.TempDir()
	orig, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(orig) })
	os.Chdir(wd)
}

func TestInstallKiro_CreatesFiles(t *testing.T) {
	useTempWorkingDir(t)
	home := t.TempDir()

	if err := installer.InstallKiro(home, "claude-sonnet-4"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(home, ".kiro", "agents", "auto-bug-fix.json"))
	if err != nil {
		t.Fatal("agent JSON not created")
	}
	var agent map[string]any
	if err := json.Unmarshal(b, &agent); err != nil {
		t.Fatalf("agent JSON invalid: %v", err)
	}
	if m, _ := agent["model"].(string); m != "claude-sonnet-4" {
		t.Errorf("kiro agent should pin the model in JSON, got %q", m)
	}
	if p, _ := agent["prompt"].(string); p != "file://./auto-bug-fix.md" {
		t.Errorf("agent prompt should reference the prompt file, got %q", p)
	}
	if _, ok := agent["resources"]; !ok {
		t.Fatal("kiro agent must inject the CLI skills as resources")
	}
	resJSON, _ := json.Marshal(agent["resources"])
	for _, s := range []string{"jira-cli", "gitlab-cli", "kibana-cli"} {
		if !strings.Contains(string(resJSON), "/.kiro/skills/"+s+"/SKILL.md") {
			t.Errorf("kiro resources should reference the %s skill, got %s", s, resJSON)
		}
	}
	if !strings.Contains(string(resJSON), "skill://") {
		t.Errorf("kiro CLI skills must use the skill:// scheme, got %s", resJSON)
	}
	md, err := os.ReadFile(filepath.Join(home, ".kiro", "agents", "auto-bug-fix.md"))
	if err != nil {
		t.Fatal("prompt markdown not created")
	}
	if !strings.Contains(string(md), "AUTO_BUG_FIX_RESULT") {
		t.Error("prompt markdown should contain the execution workflow")
	}
	if _, err := os.Stat(filepath.Join(home, ".kiro", "skills", "auto-bug-fix", "SKILL.md")); !os.IsNotExist(err) {
		t.Error("setup --agent kiro must not write skills/ (the operator skill is installed via npx)")
	}
}

// ArtifactPaths must list exactly the files each installer writes, so doctor's
// "is the subagent template installed" check cannot drift from installation.
func TestArtifactPaths_MatchInstallers(t *testing.T) {
	useTempWorkingDir(t)
	cases := map[string]func(string) error{
		"kiro":        func(h string) error { return installer.InstallKiro(h, "") },
		"cursor":      installer.InstallCursor,
		"claude-code": installer.InstallClaudeCode,
		"codex":       installer.InstallCodex,
	}
	for agentType, install := range cases {
		t.Run(agentType, func(t *testing.T) {
			home := t.TempDir()
			if err := install(home); err != nil {
				t.Fatal(err)
			}
			paths := installer.ArtifactPaths(agentType, home)
			if len(paths) == 0 {
				t.Fatalf("ArtifactPaths(%q) returned none", agentType)
			}
			if !config.KnownAgentType(agentType) {
				t.Errorf("config.KnownAgentType(%q) is false but installer supports it", agentType)
			}
			if installer.AgentCommand(agentType, "") == "" {
				t.Errorf("AgentCommand(%q) is empty but installer supports it", agentType)
			}
			for _, p := range paths {
				if _, err := os.Stat(p); err != nil {
					t.Errorf("ArtifactPaths lists %s but installer did not create it", p)
				}
			}
		})
	}
	if got := installer.ArtifactPaths("", "/home"); got != nil {
		t.Errorf("empty agentType should yield nil paths, got %v", got)
	}
}

func TestInstallCursor_CreatesGlobalSkill(t *testing.T) {
	useTempWorkingDir(t)
	home := t.TempDir()

	if err := installer.InstallCursor(home); err != nil {
		t.Fatal(err)
	}
	// Cursor auto-loads skills from ~/.cursor/skills/; a home ~/.cursor/rules/*.mdc
	// is not an auto-loaded rules location, so the workflow ships as a global skill.
	data, err := os.ReadFile(filepath.Join(home, ".cursor", "skills", "auto-bug-fix", "SKILL.md"))
	if err != nil {
		t.Fatal("cursor SKILL.md not created")
	}
	if !strings.Contains(string(data), "name: auto-bug-fix") {
		t.Error("SKILL.md should carry SKILL frontmatter (name)")
	}
	if !strings.Contains(string(data), "AUTO_BUG_FIX_RESULT") {
		t.Error("SKILL.md should contain the execution workflow body")
	}
	if strings.Contains(string(data), "alwaysApply") {
		t.Error("rule frontmatter (alwaysApply) should be stripped from the skill")
	}
}

func TestInstallClaudeCode_CreatesAgentMD(t *testing.T) {
	useTempWorkingDir(t)
	home := t.TempDir()

	if err := installer.InstallClaudeCode(home); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "agents", "auto-bug-fix.md")); err != nil {
		t.Fatal("agent md not created")
	}
}

func TestInstallCodex_AppendsToAgentsMD(t *testing.T) {
	useTempWorkingDir(t)
	home := t.TempDir()

	if err := installer.InstallCodex(home); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".codex", "AGENTS.md"))
	if err != nil {
		t.Fatal("AGENTS.md not created")
	}
	if !strings.Contains(string(data), "auto-bug-fix") {
		t.Error("should contain codex instructions")
	}
}

func TestInstallCodex_IdempotentReplace(t *testing.T) {
	useTempWorkingDir(t)
	home := t.TempDir()

	if err := installer.InstallCodex(home); err != nil {
		t.Fatal(err)
	}
	if err := installer.InstallCodex(home); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(filepath.Join(home, ".codex", "AGENTS.md"))
	if count := strings.Count(string(data), "<!-- auto-bug-fix start -->"); count != 1 {
		t.Fatalf("expected one managed section, got %d", count)
	}
}

func TestAgentCommand(t *testing.T) {
	cases := map[string]string{
		"kiro":        "kiro-cli",
		"cursor":      "cursor-agent",
		"claude-code": "claude",
		"codex":       "codex exec",
	}
	for agentType, want := range cases {
		cmd := installer.AgentCommand(agentType, "")
		if !strings.Contains(cmd, want) {
			t.Errorf("agentType %q: command should contain %q, got %q", agentType, want, cmd)
		}
	}

	// Flag-capable agents inject --model; kiro pins its model in JSON instead.
	for _, agentType := range []string{"cursor", "claude-code", "codex"} {
		if cmd := installer.AgentCommand(agentType, "my-model"); !strings.Contains(cmd, `--model "my-model"`) {
			t.Errorf("agentType %q should inject the model flag, got %q", agentType, cmd)
		}
	}
	if cmd := installer.AgentCommand("kiro", "my-model"); strings.Contains(cmd, "--model") {
		t.Errorf("kiro has no --model flag; model belongs in the agent JSON, got %q", cmd)
	}

	// kiro grants headless tool permission with least privilege: pre-trust exactly
	// the agent's declared tools via --trust-tools, never the broad --trust-all-tools.
	if cmd := installer.AgentCommand("kiro", ""); strings.Contains(cmd, "--trust-all-tools") || !strings.Contains(cmd, "--trust-tools=fs_read,fs_write,execute_bash,grep,glob") {
		t.Errorf("kiro should use least-privilege --trust-tools, got %q", cmd)
	}
}
