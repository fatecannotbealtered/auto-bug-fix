package installer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fatecannotbealtered/auto-bug-fix/agents"
)

func readAgentFile(agentType, filename string) (string, error) {
	return agents.ReadFile(agentType, filename)
}

// CLISkill is a sibling-CLI skill the spawned agent must be able to load before
// driving that CLI. Required mirrors doctor's capability requiredness: the
// jira-cli/gitlab-cli skills are mandatory; the kibana-cli (log evidence) and
// archery-cli (read-only database-state evidence) skills are optional — the
// workflow only consults them when code-level analysis is inconclusive.
type CLISkill struct {
	Name     string
	Required bool
}

// CLISkills is the single source of truth for which CLI skills setup injects and
// doctor verifies, so injection and the preflight check cannot drift apart.
var CLISkills = []CLISkill{
	{"jira-cli", true},
	{"gitlab-cli", true},
	{"kibana-cli", false},
	{"archery-cli", false},
}

// SkillsAgentFlag maps our agentType to the `--agent` identifier used by the
// `skills` CLI (vercel-labs/skills): note kiro's is `kiro-cli`, not `kiro`.
// Returns "" for an empty/unknown (custom) agentType.
func SkillsAgentFlag(agentType string) string {
	switch agentType {
	case "kiro":
		return "kiro-cli"
	case "claude-code":
		return "claude-code"
	case "codex":
		return "codex"
	case "cursor":
		return "cursor"
	default:
		return ""
	}
}

// SkillsDir returns the agent's OWN skill directory for agentType — the stable,
// per-tool location each vibe-coding agent natively loads from (~/.kiro/skills,
// ~/.claude/skills, ~/.codex/skills, ~/.cursor/skills). The shared ~/.agents
// store is only a cross-agent compatibility shim (junctions); requiring the skill
// in the agent's own directory is what makes loading reliable. Returns "" for an
// empty/unknown (custom) agentType we cannot reason about.
func SkillsDir(agentType, home string) string {
	switch agentType {
	case "kiro":
		return filepath.Join(home, ".kiro", "skills")
	case "claude-code":
		return filepath.Join(home, ".claude", "skills")
	case "codex":
		return filepath.Join(home, ".codex", "skills")
	case "cursor":
		return filepath.Join(home, ".cursor", "skills")
	default:
		return ""
	}
}

// kiroSkillResources renders the `skill://` resource URIs for the kiro agent
// JSON, pointing at each CLI skill's absolute SKILL.md in kiro's own skill dir
// (~/.kiro/skills). The spawned `kiro-cli chat` runs in the cloned repo's cwd,
// not home, so a relative path would not resolve — the resource must be absolute.
func kiroSkillResources(home string) []string {
	dir := SkillsDir("kiro", home)
	res := make([]string, 0, len(CLISkills))
	for _, s := range CLISkills {
		res = append(res, "skill://"+filepath.ToSlash(filepath.Join(dir, s.Name, "SKILL.md")))
	}
	return res
}

// ArtifactPaths returns the files that `setup --agent <agentType>` installs into
// the user's home. It is the single source of truth for both installation and
// doctor's "is the subagent template installed" verification. Returns nil for an
// empty or unknown agentType (a custom agent.command we cannot verify).
func ArtifactPaths(agentType, home string) []string {
	switch agentType {
	case "kiro":
		return []string{
			filepath.Join(home, ".kiro", "agents", "auto-bug-fix.json"),
			filepath.Join(home, ".kiro", "agents", "auto-bug-fix.md"),
		}
	case "cursor":
		return []string{filepath.Join(home, ".cursor", "skills", "auto-bug-fix", "SKILL.md")}
	case "claude-code":
		return []string{filepath.Join(home, ".claude", "agents", "auto-bug-fix.md")}
	case "codex":
		return []string{filepath.Join(home, ".codex", "AGENTS.md")}
	default:
		return nil
	}
}

// InstallKiro writes the standard kiro subagent definition to ~/.kiro/agents/:
// a small JSON config plus an editable Markdown prompt file it references via a
// relative `file://./auto-bug-fix.md` URI (resolved against the config's own
// directory). The execution workflow lives in that prompt file — owned by the
// spawned subagent — leaving the skills/ directory for the operator skill
// (installed separately via `npx skills add`).
func InstallKiro(home, model string) error {
	agentJSON, err := readAgentFile("kiro", "auto-bug-fix.json")
	if err != nil {
		return err
	}
	workflowMD, err := readAgentFile("kiro", "auto-bug-fix.md")
	if err != nil {
		return err
	}

	var agent map[string]any
	if err := json.Unmarshal([]byte(agentJSON), &agent); err != nil {
		return fmt.Errorf("parse kiro agent json: %w", err)
	}
	agent["prompt"] = "file://./auto-bug-fix.md"
	// Inject the CLI skills the executor needs (jira-cli/gitlab-cli/kibana-cli)
	// as on-demand skill:// resources. The workflow prompt stays in the prompt
	// file; these only add the sibling-CLI usage skills.
	agent["resources"] = kiroSkillResources(home)
	// kiro pins the model via the agent JSON (no CLI --model flag).
	if m := strings.TrimSpace(model); m != "" {
		agent["model"] = m
	}

	out, err := json.MarshalIndent(agent, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal kiro agent json: %w", err)
	}

	agentDir := filepath.Join(home, ".kiro", "agents")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		return fmt.Errorf("create kiro agents dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "auto-bug-fix.md"), []byte(stripFrontmatter(workflowMD)), 0o644); err != nil {
		return fmt.Errorf("write kiro prompt file: %w", err)
	}
	return os.WriteFile(filepath.Join(agentDir, "auto-bug-fix.json"), out, 0o644)
}

// stripFrontmatter removes a leading YAML frontmatter block (--- ... ---) so the
// markdown body can be used directly as an agent prompt.
func stripFrontmatter(md string) string {
	s := strings.ReplaceAll(md, "\r\n", "\n")
	if strings.HasPrefix(s, "---\n") {
		if idx := strings.Index(s[4:], "\n---\n"); idx >= 0 {
			return strings.TrimLeft(s[4+idx+len("\n---\n"):], "\n")
		}
	}
	return s
}

// cursorSkillFrontmatter is the SKILL.md header for the Cursor adapter. Cursor
// auto-loads Agent Skills from the user-level ~/.cursor/skills/ directory, so the
// workflow is installed as a global skill (see InstallCursor).
const cursorSkillFrontmatter = `---
name: auto-bug-fix
description: Autonomous Jira + GitLab bug-fix execution workflow. Use when fixing a Jira bug end-to-end — read the ticket, locate the GitLab repo, analyse the code, write a targeted fix, run tests, open a merge request, and update Jira.
---

`

// InstallCursor installs the auto-bug-fix workflow as a global Cursor Agent Skill
// at ~/.cursor/skills/auto-bug-fix/SKILL.md. Cursor auto-discovers skills from
// that user-level directory across all projects; a home ~/.cursor/rules/*.mdc is
// NOT an auto-loaded rules location (only a project's .cursor/rules/ and the
// Settings UI are), so a skill is the correct global mechanism. The embedded
// .mdc body is reused with its rule frontmatter replaced by SKILL.md frontmatter.
func InstallCursor(home string) error {
	mdc, err := readAgentFile("cursor", "auto-bug-fix.mdc")
	if err != nil {
		return err
	}
	skill := cursorSkillFrontmatter + stripFrontmatter(mdc)
	skillDir := filepath.Join(home, ".cursor", "skills", "auto-bug-fix")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return fmt.Errorf("create cursor skill dir: %w", err)
	}
	return os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skill), 0o644)
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

// AgentCommand returns the correct non-interactive command for the given
// agentType, pinning model when set. Flag syntax per each CLI's official docs:
// cursor-agent/claude/codex all accept `--model <name>`; kiro-cli chat has NO
// --model flag (its model lives in the agent JSON — see InstallKiro), so the
// kiro command is identical regardless of model.
func AgentCommand(agentType, model string) string {
	switch agentType {
	case "kiro":
		// Least-privilege headless trust: pre-approve exactly the agent's declared
		// tools via --trust-tools (canonical names matching the agent JSON) rather
		// than --trust-all-tools. Fully covers what the agent can do, and avoids the
		// scripted-startup prompt reported for --trust-all-tools (Kiro #7398).
		return `kiro-cli chat --no-interactive --trust-tools=fs_read,fs_write,execute_bash,grep,glob --agent auto-bug-fix "Fix bug {issueKey}"`
	case "cursor":
		return `cursor-agent` + modelFlag(model) + ` --print --force "Fix bug {issueKey} using the auto-bug-fix workflow"`
	case "claude-code":
		return `claude` + modelFlag(model) + ` --agent auto-bug-fix -p "Fix bug {issueKey}" --permission-mode acceptEdits`
	case "codex":
		return `codex exec` + modelFlag(model) + ` --dangerously-bypass-approvals-and-sandbox --skip-git-repo-check "Fix bug {issueKey} using the auto-bug-fix skill"`
	default:
		return ""
	}
}

// modelFlag renders ` --model "<model>"` (quoted, so a model name with spaces
// survives the no-shell tokenizer) or "" when no model is pinned.
func modelFlag(model string) string {
	m := strings.TrimSpace(model)
	if m == "" {
		return ""
	}
	return ` --model "` + m + `"`
}
