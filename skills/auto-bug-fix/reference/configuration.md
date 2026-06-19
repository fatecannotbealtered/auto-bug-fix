# Configuration Reference

This file is the Agent-facing reference for `~/.auto-bug-fix/config.json`.
Use it when setting up or reviewing auto-bug-fix configuration. Do not store
Jira, GitLab, Kibana, PATs, passwords, or cookies in this file; those belong to
the sibling CLIs and their own credential stores.

## Contents

- [Location](#location)
- [Example](#example)
- [`agent`](#agent)
- [`poll`](#poll)
- [`poll.filter`](#pollfilter)
- [`workspace`](#workspace)
- [`knowledge`](#knowledge)
- [Environment Passed To Spawned Agents](#environment-passed-to-spawned-agents)
- [Review Checklist](#review-checklist)

## Location

Default path:

```text
~/.auto-bug-fix/config.json
```

Create or refresh the config through the guarded setup flow:

```bash
auto-bug-fix setup --agent codex --dry-run --compact
auto-bug-fix setup --agent codex --confirm <confirm_token> --compact
```

`$ENV_VAR` placeholders in string fields are substituted when the config is
loaded. If an environment variable is missing, auto-bug-fix logs a warning and
the placeholder resolves to an empty string.

## Example

```json
{
  "agent": {
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
    "dir": ".repo-knowledge",
    "read": true,
    "update": true,
    "handoff": true,
    "handoffDir": "handoff"
  }
}
```

## `agent`

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `agent.agentType` | string | Required unless `agent.command` is custom | `""` | Supported values: `kiro`, `cursor`, `claude-code`, `codex`, or empty for a custom command. For a known type, auto-bug-fix derives the launch command at runtime. |
| `agent.model` | string | Required for known `agentType` | `""` | Model the spawned agent must use. For `cursor`, `claude-code`, and `codex`, it is injected into the derived command. For `kiro`, run `auto-bug-fix setup --agent kiro` again after changing this value so the Kiro agent JSON is updated. |
| `agent.command` | string | Required only when `agentType` is empty or custom | derived | Custom command to spawn for each issue. `{issueKey}` is replaced everywhere; if absent, the issue key is appended as the final argument. Do not put secrets in command args. |

Validation:

- `agent.agentType` must be one of `kiro`, `cursor`, `claude-code`, `codex`, or empty.
- Known `agentType` requires `agent.model`.
- Unknown/custom agent requires `agent.command`.
- Explicit `agent.command` overrides the derived command; `doctor` warns because it can drift from installed templates.

## `poll`

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `poll.intervalSeconds` | integer | No | `300` | Polling interval in seconds. `0` uses the default. Negative values are invalid. |
| `poll.maxConcurrent` | integer | No | `3` | Maximum simultaneous spawned agent fixes. `0` uses the default. Negative values are invalid. |
| `poll.stateExpiryDays` | integer | No | `0` | Re-trigger `done`, `failed`, or `waiting` issues after this many days. `0` means never re-trigger by age. |

## `poll.filter`

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `poll.filter.titleContains` | string | No | `""` | Only process Bugs whose title contains this string. Empty means no title filter. |
| `poll.filter.assignedToMe` | boolean | No | `true` | Only process Bugs assigned to the authenticated Jira user. |
| `poll.filter.excludeStatuses` | string array | No | `[]` | Extra Jira status names to skip. Done-category issues are already excluded by the poller query. |

Scope rules:

- `titleContains == ""` and `assignedToMe == false` matches every open Bug in the Jira instance; `doctor` fails this as too broad.
- `titleContains == ""` and `assignedToMe == true` is allowed but warned: every open Bug assigned to the current user can be auto-fixed.
- Add in-flight statuses such as `In Progress` or `In Review` to `excludeStatuses` if those tickets should not be picked up again before review/merge.

## `workspace`

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `workspace.root` | string path | No | `~/.auto-bug-fix/workspaces` | Root directory where repositories are cloned or reused. Passed to the spawned agent as `AUTO_BUG_FIX_WORKSPACE_ROOT`. |
| `workspace.cleanup` | string | No | `keep` | Cleanup policy. Allowed values: `keep`, `on-success`, `always`. Passed as `AUTO_BUG_FIX_WORKSPACE_CLEANUP`. |

Cleanup behavior:

- `keep`: keep workspaces after every run.
- `on-success`: remove workspaces after successful auto-fix runs.
- `always`: remove workspaces after every run, including failed or diagnostic outcomes.

## `knowledge`

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `knowledge.dir` | repo-relative path | No | `.repo-knowledge` | Repo-local business knowledge directory. Must be relative to the cloned repo and must not escape upward. |
| `knowledge.read` | boolean | No | `true` | Whether the spawned agent may read existing knowledge files. |
| `knowledge.update` | boolean | No | `true` | Whether the spawned agent may update knowledge files after learning durable project context. |
| `knowledge.handoff` | boolean | No | `true` | Whether the spawned agent may write handoff files when human confirmation is needed. |
| `knowledge.handoffDir` | repo-relative path | No | `handoff` | Subdirectory under `knowledge.dir` for handoff files. Must be repo-relative and must not escape upward. |

Validation:

- `knowledge.dir` and `knowledge.handoffDir` must be repo-relative paths.
- Empty, absolute, `.` / `..`, or upward-escaping paths are invalid.

For how the knowledge directory is organized (file roles, entry skeleton, and
the `caveats.md` convention that restrains auto-fix on load-bearing code), see
[repo-knowledge.md](repo-knowledge.md).

## Environment Passed To Spawned Agents

auto-bug-fix passes these environment variables to the spawned agent:

| Variable | Source |
|----------|--------|
| `AUTO_BUG_FIX_WORKSPACE_ROOT` | `workspace.root` |
| `AUTO_BUG_FIX_WORKSPACE_CLEANUP` | `workspace.cleanup` |
| `AUTO_BUG_FIX_KNOWLEDGE_DIR` | `knowledge.dir` |
| `AUTO_BUG_FIX_KNOWLEDGE_READ` | `knowledge.read` |
| `AUTO_BUG_FIX_KNOWLEDGE_UPDATE` | `knowledge.update` |
| `AUTO_BUG_FIX_KNOWLEDGE_HANDOFF` | `knowledge.handoff` |
| `AUTO_BUG_FIX_KNOWLEDGE_HANDOFF_DIR` | `knowledge.handoffDir` |

Jira/GitLab/Kibana hosts and tokens are not passed by auto-bug-fix. The spawned
agent relies on `jira-cli`, `gitlab-cli`, and optional `kibana-cli` already being
authenticated in the poller machine environment.

## Database evidence (archery, optional)

`archery-cli` is an optional sibling CLI, not an auto-bug-fix config field — like `kibana-cli`,
its credentials live in the CLI's own store (`archery-cli auth login`), and `auto-bug-fix
doctor` reports it at WARN when absent. The spawned agent uses it for **read-only** data-state
evidence during root-cause analysis (Step 4): SELECT queries via `archery-cli query run`.

Operator requirement: the Archery instance auto-bug-fix queries **must be configured with a
least-privilege, read-only database account**. Write-prevention is enforced at the database,
not just by the agent's SELECT-only instructions — so any DML/DDL is refused even if something
goes wrong upstream. See [SECURITY.md](../../../SECURITY.md) → "Database access (archery)".

## Review Checklist

Before confirming `start --detach`:

- `agent.agentType` is known, or `agent.command` is a trusted custom command.
- `agent.model` is set for a known agent type.
- `poll.filter` is intentionally scoped and not every open Bug in Jira.
- `workspace.root` has enough disk space and is acceptable for cloned repos.
- `knowledge.dir` and `knowledge.handoffDir` are repo-relative.
- If `archery-cli` is used, its queried instance uses a read-only DB account.
- `auto-bug-fix doctor --compact` has no blocking failure.
