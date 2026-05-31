---
name: auto-bug-fix
description: How to install, configure, and run the auto-bug-fix poller. The per-ticket bug-fix workflow runs inside the spawned agent (installed via `setup --agent`), not here.
---

# auto-bug-fix

`auto-bug-fix` is a deterministic scheduler: it polls Jira for matching Bugs and hands each one to a configured AI agent that performs the actual fix — reads the ticket, finds the GitLab repo, writes a targeted fix, opens an MR, and comments the ticket.

**This skill is for the agent that _operates_ auto-bug-fix** — installing it, configuring it, and running the poller. The per-ticket bug-fix workflow itself runs inside the **spawned agent**, whose instructions `auto-bug-fix setup --agent <type>` installs separately. From here you do not read Jira, edit repos, or open MRs — you only set the tool up and run it.

> Install CLI: download the binary for your platform from GitHub Releases, or `go install github.com/fatecannotbealtered/auto-bug-fix@latest`
>
> Install Skill: `npx skills add fatecannotbealtered/auto-bug-fix -y -g` (installs `skills/auto-bug-fix/SKILL.md`)

## Deployment model

`auto-bug-fix` is a **single-instance tool** — one running poller handles all Jira issues.

- **Personal use:** run `auto-bug-fix start` on your own machine. It polls Jira on a configurable interval and triggers your agent for each new matching issue.
- **Team use (recommended):** deploy one shared instance on a fixed internal server. All team members' bugs are handled by that single poller. Each developer still authenticates their own CLI tools (`jira-cli login`, `gitlab-cli auth login`) on that server.

## First-time setup (agent-guided)

`auto-bug-fix setup` is non-interactive. You, the installed agent, guide the human through the choices and then run setup with the matching `--agent` value. Run it before the first fix if config is missing or incomplete.

1. **Ask which AI agent should run future fixes**, then run one command:
   - Kiro: `auto-bug-fix setup --agent kiro`
   - Cursor: `auto-bug-fix setup --agent cursor`
   - Claude Code: `auto-bug-fix setup --agent claude-code`
   - Codex: `auto-bug-fix setup --agent codex`

   Setup installs that agent's instructions and creates `~/.auto-bug-fix/config.json` with `agent.agentType` set; the launch command is **derived from `agentType` at runtime** (no `agent.command` is written). If no agent type fits, run `auto-bug-fix setup` to create a generic template and set a custom `agent.command` yourself.

2. **Collect the remaining required values by asking the human** — do not invent them:
   - `poll.filter` — ask three plain questions (all optional; skip = use default):
     1. **标题关键词？** (`titleContains`) — only process Bugs whose title contains this string. Leave blank = process all Bugs.
     2. **只处理分配给我的 Bug？** (`assignedToMe`) — default `true`.
     3. **排除哪些状态？** (`excludeStatuses`) — status names to skip, e.g. `["已关闭", "Done"]`. Leave blank = skip nothing extra (already excludes Done category by default).
   - `poll.intervalSeconds` — polling interval in seconds (default 300; ask only if they want a different value).
   - `poll.maxConcurrent` — maximum simultaneous agent fixes (default 3; ask only if they want a different value).
   - `workspace.root` — local clone root (default `~/.auto-bug-fix/workspaces`). Worth surfacing to the human: repos are cloned here and can grow large; the default sits under home (C:\ on Windows), so offer to relocate it (e.g. another drive) if they prefer. `auto-bug-fix doctor` echoes the effective location.
   - `workspace.cleanup` — cleanup policy: `keep` (default), `on-success`, or `always`.
   - `knowledge.dir` — repo-local business knowledge directory (default `.tcl`).
   - `knowledge.read` / `knowledge.update` / `knowledge.handoff` — default `true`.
   - `knowledge.handoffDir` — subdirectory under `knowledge.dir` for confirmation handoff files (default `handoff`).

3. **Write poll config, workspace config, and knowledge config into the file.** Edit `poll.filter` / `workspace.*` / `knowledge.*` directly in `config.json`. For a known `agentType` you do **not** set `agent.command` — it is derived; only set `agent.command` for a custom agent (`agentType` empty). The config holds **no** Jira/GitLab/Kibana hosts or tokens — those are not stored here.

4. **Authenticate the capability CLIs (their own concern, not this config).** Make sure `jira-cli login` and `gitlab-cli auth login` are done on the machine that runs the poller; `kibana-cli auth login` is optional (only needed for the spawned agent's log-lookup step). You do not put their tokens in `config.json`.

5. **Start the poller in the background.** From the machine that will run it:

   ```bash
   auto-bug-fix start --detach   # background; prints PID, logs to ~/.auto-bug-fix/poller.log
   auto-bug-fix status           # check whether it is running
   auto-bug-fix stop             # stop the poller and any in-flight fix agents
   ```

   Foreground `auto-bug-fix start` is still available for debugging (Ctrl+C to stop).

6. **Verify before finishing:** run `auto-bug-fix doctor --json` and parse it. It returns `{"ok": bool, "checks": [{"level": "OK|WARN|FAIL", "name", "detail"}]}` on stdout. The checks cover config validity, the agent CLI on PATH, the **subagent template installed for your `agentType`** (a `FAIL` here means `setup --agent` was skipped — re-run it), each capability CLI being **authenticated and reachable** (it calls `jira-cli`/`gitlab-cli`/`kibana-cli`'s own `doctor`), and the **fix scope** (`poll.filter`). On a `fix scope` `WARN` or `FAIL`, **ask the human: do you want to limit which Bugs get auto-fixed?** — a `FAIL` (no title filter and not assignee-limited = every open Bug in the instance) blocks until they narrow it. Act programmatically: any `FAIL` blocks the first fix; a `WARN` (e.g. optional `kibana-cli` not configured, a custom `agent.command` whose template can't be verified, or a broad-but-assignee-limited scope) is safe to proceed past once acknowledged.

## Prerequisites for the poller machine

The poller and the spawned agent rely on these being present and authenticated on the machine that runs the poller:

```bash
jira-cli doctor --json              # authValid must be true
gitlab-cli context --json --compact # exit 3 if not authenticated
git --version                       # required for the spawned agent's clone/branch/commit/push
kibana-cli search --index "app-logs-*" --query "test" --last 1h --json   # optional — only used by the agent's log-lookup step
```

## Day-to-day

- The poller triggers a fix automatically for each new matching Bug; you do not invoke the workflow by hand.
- Re-run a single ticket on demand: `auto-bug-fix fix <issueKey>`.
- Inspect or control the poller: `auto-bug-fix status`, `auto-bug-fix stop`, `auto-bug-fix start --detach`.
- Re-check the environment at any time: `auto-bug-fix doctor --json`.

Always pass `--json` to `doctor`, `status`, `stop`, and `start --detach` so you parse a result instead of prose (matching the `jira-cli` / `gitlab-cli` convention).

The bug-fix steps themselves — reading the ticket, resolving the repo, writing the fix, tests, MR, and the Jira update — live in the **spawned agent's** instructions, not in this skill.
