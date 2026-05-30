package installer_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	if _, err := os.Stat(filepath.Join(home, ".kiro", "agents", "auto-bug-fix.json")); err != nil {
		t.Fatal("agent JSON not created")
	}
	if _, err := os.Stat(filepath.Join(home, ".kiro", "skills", "auto-bug-fix", "SKILL.md")); err != nil {
		t.Fatal("SKILL.md not created")
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
