package installer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fatecannotbealtered/auto-bug-fix/agents"
)

func readAgentFile(agentType, filename string) (string, error) {
	return agents.ReadFile(agentType, filename)
}

// ArtifactPaths returns the files that `setup --agent <agentType>` installs into
// the user's home. It is the single source of truth for both installation and
// doctor's "is the subagent template installed" verification. Returns nil for an
// empty or unknown agentType (a custom agent.command we cannot verify).
func ArtifactPaths(agentType, home string) []string {
	switch agentType {
	case "kiro":
		return []string{
			filepath.Join(home, ".kiro", "skills", "auto-bug-fix", "SKILL.md"),
			filepath.Join(home, ".kiro", "agents", "auto-bug-fix.json"),
		}
	case "cursor":
		return []string{filepath.Join(home, ".cursor", "rules", "auto-bug-fix.mdc")}
	case "claude-code":
		return []string{filepath.Join(home, ".claude", "agents", "auto-bug-fix.md")}
	case "codex":
		return []string{filepath.Join(home, ".codex", "AGENTS.md")}
	default:
		return nil
	}
}

// InstallKiro writes the kiro agent config and execution SKILL.md to ~/.kiro/.
func InstallKiro(home string) error {
	agentJSON, err := readAgentFile("kiro", "auto-bug-fix.json")
	if err != nil {
		return err
	}
	skillMD, err := readAgentFile("kiro", "SKILL.md")
	if err != nil {
		return err
	}

	skillDir := filepath.Join(home, ".kiro", "skills", "auto-bug-fix")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return fmt.Errorf("create kiro skills dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillMD), 0o644); err != nil {
		return fmt.Errorf("write kiro SKILL.md: %w", err)
	}

	agentDir := filepath.Join(home, ".kiro", "agents")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		return fmt.Errorf("create kiro agents dir: %w", err)
	}
	return os.WriteFile(filepath.Join(agentDir, "auto-bug-fix.json"), []byte(agentJSON), 0o644)
}

// InstallCursor writes the cursor rule to ~/.cursor/rules/auto-bug-fix.mdc.
func InstallCursor(home string) error {
	mdc, err := readAgentFile("cursor", "auto-bug-fix.mdc")
	if err != nil {
		return err
	}
	rulesDir := filepath.Join(home, ".cursor", "rules")
	if err := os.MkdirAll(rulesDir, 0o755); err != nil {
		return fmt.Errorf("create cursor rules dir: %w", err)
	}
	return os.WriteFile(filepath.Join(rulesDir, "auto-bug-fix.mdc"), []byte(mdc), 0o644)
}

// InstallClaudeCode writes the claude-code agent to ~/.claude/agents/auto-bug-fix.md.
func InstallClaudeCode(home string) error {
	md, err := readAgentFile("claude-code", "auto-bug-fix.md")
	if err != nil {
		return err
	}
	agentDir := filepath.Join(home, ".claude", "agents")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		return fmt.Errorf("create claude agents dir: %w", err)
	}
	return os.WriteFile(filepath.Join(agentDir, "auto-bug-fix.md"), []byte(md), 0o644)
}

// InstallCodex appends/replaces the auto-bug-fix section in ~/.codex/AGENTS.md.
func InstallCodex(home string) error {
	content, err := readAgentFile("codex", "AGENTS.md")
	if err != nil {
		return err
	}
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		return fmt.Errorf("create codex dir: %w", err)
	}
	agentsPath := filepath.Join(codexDir, "AGENTS.md")

	marker := "<!-- auto-bug-fix start -->"
	endMarker := "<!-- auto-bug-fix end -->"
	section := marker + "\n\n" + content + "\n\n" + endMarker + "\n"

	existing, _ := os.ReadFile(agentsPath)
	var final string
	if len(existing) > 0 {
		s := string(existing)
		if start := strings.Index(s, marker); start >= 0 {
			end := strings.Index(s, endMarker)
			if end >= 0 {
				final = s[:start] + section + s[end+len(endMarker)+1:]
			} else {
				final = s[:start] + section
			}
		} else {
			final = s + "\n" + section
		}
	} else {
		final = section
	}
	if err := os.WriteFile(agentsPath, []byte(final), 0o644); err != nil {
		return err
	}
	return nil
}

// AgentCommand returns the correct non-interactive command for the given agentType.
func AgentCommand(agentType string) string {
	switch agentType {
	case "kiro":
		return `kiro-cli chat --no-interactive --trust-all-tools --agent auto-bug-fix "Fix bug {issueKey}"`
	case "cursor":
		return `cursor-agent --print --force "Fix bug {issueKey} using the auto-bug-fix workflow"`
	case "claude-code":
		return `claude --agent auto-bug-fix -p "Fix bug {issueKey}" --permission-mode acceptEdits`
	case "codex":
		return `codex exec --dangerously-bypass-approvals-and-sandbox --skip-git-repo-check "Fix bug {issueKey} using the auto-bug-fix skill"`
	default:
		return ""
	}
}
