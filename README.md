[English](README.md) | [中文](README.zh-CN.md)

---

<div align="center">

# auto-bug-fix

**Agent-agnostic autonomous bug-fix skill for Jira DC + GitLab**

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-%3E%3D1.21-blue)](https://golang.org)
[![CI](https://github.com/fatecannotbealtered/auto-bug-fix/actions/workflows/ci.yml/badge.svg)](https://github.com/fatecannotbealtered/auto-bug-fix/actions)

</div>

A Jira Bug ticket arrives → an AI agent reads the ticket, finds the GitLab repo, analyses the code, optionally queries Kibana logs, writes a targeted fix, verifies unit tests, creates a MR, and updates the ticket to "In Progress" — all without human intervention.

Works with **any AI agent**: Codex, Kiro, Claude Code, Cursor, or any CLI you configure via `agent.command`. Not tied to a specific LLM or agent platform.

---

## How it works

```
Jira polling (every 5 min by default)
      ↓
New Bug found matching poll.filter
      ↓
Spawn your configured agent.command
      ↓
Agent: read ticket → find repo → analyse code
      ↓
      ├─ root cause clear? → write fix
      └─ unclear?          → query Kibana → write fix
      ↓
Write / run unit tests (loop until passing)
      ↓
Create GitLab MR  ←── human reviews & merges
      ↓
Jira → "In Progress" + MR link comment
```

**What stays human:** MR review, merge, and ticket close.

---

## Quick Start

```bash
# 1. Install the CLI (a Go binary shipped via npm; requires curl on PATH)
npm install -g @fatecannotbealtered-/auto-bug-fix
# Alternatives: `go install github.com/fatecannotbealtered/auto-bug-fix@latest`, or download from GitHub Releases.

# Install AI agent skill
npx skills add fatecannotbealtered/auto-bug-fix -y -g

# 2. Authenticate prerequisite CLIs
jira-cli login --host https://jira.company.com --token <PAT>
gitlab-cli auth login --host https://gitlab.company.com --token <PAT>

# 3. Configure
auto-bug-fix setup --agent codex   # or: kiro, cursor, claude-code
# edit the config: set agent.model (required) and poll.filter

# 4. Verify prerequisites
auto-bug-fix doctor   # checks config + required CLIs (agent, jira-cli, gitlab-cli, git) on PATH

# 5. Start polling (background)
auto-bug-fix start --detach   # logs to ~/.auto-bug-fix/poller.log; `auto-bug-fix stop` to stop

# Manual trigger
auto-bug-fix fix PROJ-123
```

---

## Prerequisites

| Tool | Required | Purpose |
|------|----------|---------|
| [jira-cli](https://github.com/fatecannotbealtered/jira-cli) | Yes | Read tickets, transition status, add comments |
| [gitlab-cli](https://github.com/fatecannotbealtered/gitlab-cli) | Yes | Read code, create branches, commit, open MRs |
| `git` | Yes | Clone repositories, create branches, commit, push |
| [kibana-cli](https://github.com/fatecannotbealtered/kibana-cli) | Optional | Query production logs for root cause analysis |
| An AI agent CLI | Yes | Whatever you put in `agent.command` (e.g. `claude`, `cursor`) — must have the `auto-bug-fix` skill installed |
| Go ≥ 1.21 | Build only | Not needed if using a pre-built binary |

---

## Agent command

auto-bug-fix doesn't embed an agent — when a matching issue is found, it spawns one command and lets that agent do the work, using the workflow from the installed `auto-bug-fix` skill.

For a supported `agent.agentType`, that command is **derived automatically** (so it always matches the installed subagent template — no drift on upgrade); you only choose the type via `setup --agent` and pin the model via `agent.model` (**required** for a known type):

| `agentType` | Derived command |
|-------|-----------------|
| `kiro` | `kiro-cli chat --no-interactive --trust-all-tools --agent auto-bug-fix "Fix bug {issueKey}"` |
| `codex` | `codex exec --model "<model>" --dangerously-bypass-approvals-and-sandbox --skip-git-repo-check "Fix bug {issueKey} using the auto-bug-fix skill"` |
| `claude-code` | `claude --model "<model>" --agent auto-bug-fix -p "Fix bug {issueKey}" --permission-mode acceptEdits` |
| `cursor` | `cursor-agent --model "<model>" --print --force "Fix bug {issueKey} using the auto-bug-fix workflow"` |

**Model (`agent.model`, required for a known type):** the spawned agent should never silently fall back to a CLI default model on an unattended fix. `cursor` / `claude-code` / `codex` accept a `--model` flag, so the model is injected into the derived command and always reflects config. **kiro-cli `chat` has no `--model` flag** — its model lives in the agent JSON (`~/.kiro/agents/auto-bug-fix.json`), so `setup --agent kiro` writes `agent.model` there. After changing `agent.model` for kiro, re-run `auto-bug-fix setup --agent kiro` to apply it.

For any other agent, leave `agentType` empty and set a **custom `agent.command`** yourself (e.g. `/path/to/fix.sh`, key appended as `$1`). If you set both, the explicit command wins and `doctor` warns about the override.

**Substitution:** if the command contains `{issueKey}`, every occurrence is replaced with the Jira key (including inside a quoted prompt). If it does not, the key is appended as the last argument.

**No shell:** the command is tokenized and spawned without a shell, so pipes, redirects, and environment-variable expansion are not interpreted — wrap those in a script if you need them.

**Credentials:** the agent calls `jira-cli` / `gitlab-cli` (and optionally `kibana-cli`); those must already be authenticated in the environment that runs the poller. The poller does **not** inject tokens into the spawned process. Use a wrapper script when you need to set up credentials or do shell-level work:

```bash
#!/usr/bin/env bash
# fix.sh — receives the issue key as $1
export JIRA_TOKEN=...  GITLAB_TOKEN=...
your-agent exec "Fix bug $1 using the auto-bug-fix skill"
```

---

## Configuration

Config lives at `~/.auto-bug-fix/config.json` (created by `auto-bug-fix setup`). All `$ENV_VAR` values are substituted at load time from environment variables; unresolved placeholders are logged at load time instead of silently becoming empty.

```json
{
  "agent":  {
    "agentType": "codex",
    "model": "gpt-5.1-codex"
  },
  "poll": {
    "intervalSeconds": 300,
    "maxConcurrent": 3,
    "stateExpiryDays": 0,
    "filter": {
      "titleContains": "",
      "assignedToMe": true,
      "excludeStatuses": []
    }
  },
  "workspace": {
    "root": "$HOME/.auto-bug-fix/workspaces",
    "cleanup": "keep"
  },
  "knowledge": {
    "dir": ".tcl",
    "read": true,
    "update": true,
    "handoff": true,
    "handoffDir": "handoff"
  }
}
```

<details>
<summary><strong>Full field reference</strong></summary>

| Field | Default | Description |
|-------|---------|-------------|
| `agent.command` | derived / custom | Command spawned (no shell) per matching issue. For a known `agentType` it is **derived automatically** — leave it unset; **required only for a custom agent** (empty `agentType`). `{issueKey}` is substituted, or appended if absent. |
| `agent.agentType` | — | Template selected by setup: `kiro` / `cursor` / `claude-code` / `codex`, or empty for custom. |
| `agent.model` | — | **Required for a known `agentType`.** The model the spawned agent must use. Injected as `--model` for `cursor`/`claude-code`/`codex`; written into the kiro agent JSON for `kiro` (re-run `setup --agent kiro` to apply). Not used for a custom agent (put the model in your `agent.command`). |
| `poll.intervalSeconds` | `300` | Polling interval in seconds (`0` → default). |
| `poll.maxConcurrent` | `3` | Max issues running agent fixes at once (`0` → default). |
| `poll.stateExpiryDays` | `0` | Re-trigger `done` / `failed` / `waiting` issues after this many days. `0` = never. |
| `poll.filter.titleContains` | — | Only process Bugs whose title contains this string. |
| `poll.filter.assignedToMe` | `true` | Only process Bugs assigned to the current user. |
| `poll.filter.excludeStatuses` | `[]` | Extra status names to skip. Bugs in the Done status category are always excluded. |
| `workspace.root` | `~/.auto-bug-fix/workspaces` | Where repos are cloned/reused. Passed as `AUTO_BUG_FIX_WORKSPACE_ROOT`. |
| `workspace.cleanup` | `keep` | `keep`, `on-success`, or `always`. Passed as `AUTO_BUG_FIX_WORKSPACE_CLEANUP`. |
| `knowledge.dir` | `.tcl` | Repo-local business-knowledge directory (repo-relative). |
| `knowledge.read` / `update` / `handoff` | `true` | Whether the agent reads / updates knowledge and writes handoff files. |
| `knowledge.handoffDir` | `handoff` | Handoff subdirectory under `knowledge.dir`. |

Jira/GitLab/Kibana hosts and tokens are **not** stored here. Authentication is each sibling CLI's own job — run `jira-cli login`, `gitlab-cli auth login` (and optionally `kibana-cli auth login`). `auto-bug-fix doctor` verifies they are authenticated and reachable.

</details>

---

## Running the poller

```bash
auto-bug-fix doctor           # preflight: config valid + required CLIs on PATH (exit 1 if any required check fails)
auto-bug-fix start --detach   # background; PID at ~/.auto-bug-fix/poller.pid, logs at ~/.auto-bug-fix/poller.log
auto-bug-fix status           # running PID + log path, or "not running"
auto-bug-fix stop             # terminate the poller AND its in-flight fix agents, then remove the PID file

auto-bug-fix start            # foreground (debugging); Ctrl+C to stop
auto-bug-fix fix PROJ-123     # one-shot fix, bypasses polling and state
```

`stop` kills the whole process tree, so no fix agent is left orphaned. A fix interrupted mid-run is reset to retriable and picked up again on the next start.

Run the poller where `jira-cli` / `gitlab-cli` (and optionally `kibana-cli`) are authenticated and the `$ENV_VAR`s referenced by config are set. If you launch it from a slash/custom command, confirm that command inherits the same environment as your terminal, or use a wrapper script that sets the env and then calls `auto-bug-fix start`.

---

## State tracking

Processed issues are recorded in `~/.auto-bug-fix/state.json` with status `triggered` / `done` / `failed` / `waiting`. Each entry also records audit fields such as agent command, attempts, timestamps, duration, exit code, last error, outcome, MR URL, and handoff path when the agent prints an `AUTO_BUG_FIX_RESULT` marker. The poller skips any issue already in state, so Jira re-deliveries and repeated polls never spawn duplicate fixes.

Issues the agent could not fix on its own — outcome `needs-info` or `auto-diagnose` — are recorded as `waiting` (a human now owns the ticket), not `done`. The poller does not read ticket comments, so a human reply does not re-trigger it automatically. After answering on the ticket, re-run `auto-bug-fix fix <KEY>`, or set `poll.stateExpiryDays > 0` to have the poller retry `waiting` (and `done` / `failed`) entries after that many days.

---

## Repo knowledge

Agents can read and update a repo-local knowledge directory, defaulting to `.tcl/`. Use it for durable business meaning such as product rules, workflow constraints, domain terms, ownership notes, and integration contracts. When a fix is blocked by unclear business meaning, the agent can write a local handoff file under `.tcl/handoff/` and return that path in the audit marker for a human or outer coding agent to confirm.

---

## Design & non-goals

The Go binary is a **deterministic scheduler**: configuration, Jira polling, idempotent de-duplication, process launch, and audit. It does **not** embed a model or implement repository-specific repair logic — the bug-fix workflow (root-cause analysis, the auto-fix / auto-diagnose / needs-info confidence gate, Jira comment format) lives in the `auto-bug-fix` skill and the per-agent templates under `agents/`.

- **Single `agent.command` contract** — Kiro, Cursor, Claude Code, Codex, and custom agents all enter the same way. Tool-specific behaviour belongs in `agents/`, not in Go adapter code.
- **Credentials stay outside the poller** — the agent relies on its own authenticated CLIs; the poller never injects tokens.
- **Conservative automation** — the agent only writes code and opens an MR when the root cause is clear and locally testable; otherwise it diagnoses or asks on Jira.

**Non-goals (1.0.3):** no GitLab editing or Kibana calls inside the Go binary, no MR-merge or ticket-close automation, no per-agent adapter framework.

---

## Documentation

| Document | Description |
|----------|-------------|
| [SKILL.md](skills/auto-bug-fix/SKILL.md) | Workflow instructions for AI agents |
| [CONTRIBUTING.md](CONTRIBUTING.md) | How to contribute |
| [CHANGELOG.md](CHANGELOG.md) | Version history |
| [SECURITY.md](SECURITY.md) | Security policy and vulnerability reporting |

---

## Contributing

Contributions are welcome. Please read [CONTRIBUTING.md](CONTRIBUTING.md) before submitting a PR.

---

## License

[MIT](LICENSE) © fatecannotbealtered
