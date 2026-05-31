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

	if err := installer.InstallKiro(home); err != nil {
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
	if p, _ := agent["prompt"].(string); !strings.Contains(p, "AUTO_BUG_FIX_RESULT") {
		t.Error("agent prompt should inline the execution workflow")
	}
	if _, ok := agent["resources"]; ok {
		t.Error("standard kiro agent must own its prompt, not borrow a skill resource")
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
		"kiro":        installer.InstallKiro,
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
			if installer.AgentCommand(agentType) == "" {
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

func TestInstallCursor_CreatesMDC(t *testing.T) {
	useTempWorkingDir(t)
	home := t.TempDir()

	if err := installer.InstallCursor(home); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".cursor", "rules", "auto-bug-fix.mdc"))
	if err != nil {
		t.Fatal("mdc not created")
	}
	if !strings.Contains(string(data), "alwaysApply") {
		t.Error("mdc should contain alwaysApply")
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
		"cursor":      "cursor-agent --print --force",
		"claude-code": "claude",
		"codex":       "codex exec --dangerously-bypass-approvals-and-sandbox --skip-git-repo-check",
	}
	for agentType, want := range cases {
		cmd := installer.AgentCommand(agentType)
		if !strings.Contains(cmd, want) {
			t.Errorf("agentType %q: command should contain %q, got %q", agentType, want, cmd)
		}
	}
}
