---
name: auto-bug-fix
description: Non-interactive bug-fix execution agent. The external launch flow has already selected a Jira issue key; process the ticket directly and produce auto-fix, auto-diagnose, or needs-info.
---

# auto-bug-fix

Non-interactive bug-fix execution workflow for AI agents.

The external launch flow has already completed required confirmations and passed one Jira issue key in the current task text. Extract that issue key and process the ticket directly. Do not ask the user to confirm the key, confirm whether to start, or provide additional input.

If the current task text contains no Jira issue key, stop immediately and print:

```text
AUTO_BUG_FIX_RESULT outcome=needs-info
```

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

   Setup installs that agent's instructions and creates `~/.auto-bug-fix/config.json` with `agent.command` pre-filled. If no agent type is known yet, run `auto-bug-fix setup` to create a generic config template and fill `agent.command` yourself.

2. **Collect the remaining required values by asking the human** — do not invent them:
   - `jira.host` — their Jira Data Center base URL
   - `gitlab.host` — their GitLab base URL
   - `poll.filter` — ask three plain questions (all optional; skip = use default):
     1. **标题关键词？** (`titleContains`) — only process Bugs whose title contains this string. Leave blank = process all Bugs.
     2. **只处理分配给我的 Bug？** (`assignedToMe`) — default `true`.
     3. **排除哪些状态？** (`excludeStatuses`) — status names to skip, e.g. `["已关闭", "Done"]`. Leave blank = skip nothing extra (already excludes Done category by default).
   - `poll.intervalSeconds` — polling interval in seconds (default 300; ask only if they want a different value).
   - `poll.maxConcurrent` — maximum simultaneous agent fixes (default 3; ask only if they want a different value).
   - `workspace.root` — local clone root (default `~/.auto-bug-fix/workspaces`).
   - `workspace.cleanup` — cleanup policy: `keep` (default), `on-success`, or `always`.
   - `knowledge.dir` — repo-local business knowledge directory (default `.tcl`).
   - `knowledge.read` / `knowledge.update` / `knowledge.handoff` — default `true`.
   - `knowledge.handoffDir` — subdirectory under `knowledge.dir` for confirmation handoff files (default `handoff`).

3. **Write hosts, command, poll config, workspace config, and knowledge config into the file; keep secrets in env vars.** Edit `jira.host` / `gitlab.host` / `agent.command` / `poll.filter` / `workspace.*` / `knowledge.*` directly in `config.json`. For every `$VAR` placeholder (`$JIRA_TOKEN`, `$GITLAB_TOKEN`, Kibana creds), do **not** hard-code the secret — instruct the human to set that environment variable in the shell that runs the poller.

   **Important — env vars are session-scoped by default.** Remind the human to persist them so the poller survives restarts. Recommended: create a startup script (e.g. `start.ps1` on Windows, `start.sh` on Linux/macOS) that sets all env vars and then runs `auto-bug-fix start`. Store this script outside version control.

4. **Kibana is optional.** Only fill the `kibana.*` block if they use it; otherwise leave the placeholders and Step 4 of the workflow is skipped at runtime.

5. **Start the poller in the background.** From the machine that will run it:

   ```bash
   auto-bug-fix start --detach   # background; prints PID, logs to ~/.auto-bug-fix/poller.log
   auto-bug-fix status           # check whether it is running
   auto-bug-fix stop             # stop the poller and any in-flight fix agents
   ```

   Foreground `auto-bug-fix start` is still available for debugging (Ctrl+C to stop).

6. **Verify before finishing:** confirm `jira-cli` and `gitlab-cli` are authenticated (see Prerequisites below). If not, guide the human through their login commands.

## Prerequisites

```bash
jira-cli doctor --json              # authValid must be true
gitlab-cli context --json --compact # exit 3 if not authenticated
git --version                       # must be available for local clone/branch/commit/push
kibana-cli search --index "app-logs-*" --query "test" --last 1h --json   # optional — skip Step 4 if unavailable
```

## Safety

- Never commit directly to the default branch — always use `fix/<TICKET_KEY>-<short-desc>`
- Never merge the MR or close the ticket — those are human tasks
- Never create the MR with failing tests — resolve test failures autonomously before opening it
- Kibana is a fallback only — attempt code analysis first (Step 3) before querying logs (Step 4)
- Keep knowledge repo-local and business-focused. Do not store credentials, one-off bug narratives, or raw logs in the knowledge directory.

## Confidence gate — judge before writing the fix

After root-cause analysis (Steps 3–4), choose exactly one result type before writing code:

- **auto-fix** — clear or reproducible root cause, localized single-service change, tests exist or are easy to add → proceed with code, tests, MR, and Jira update.
- **auto-diagnose** — evidence points to a cause or responsible service, but the fix is risky, cross-service, architectural, or unsafe for autonomous editing → do not create an MR. Post diagnosis, evidence, and recommended next step in Jira.
- **needs-info** — reproduction, expected behavior, ownership, or product rule is unclear → do not create an MR. Ask specific, answerable questions in Jira and transition to a needs-info state when available.

Only **auto-fix** may modify code or create an MR.

## Repo knowledge and handoff

The poller injects repo-local knowledge settings:

- `AUTO_BUG_FIX_KNOWLEDGE_DIR` — directory to read/update, default `.tcl`.
- `AUTO_BUG_FIX_KNOWLEDGE_READ` — read existing knowledge before analysis, default `true`.
- `AUTO_BUG_FIX_KNOWLEDGE_UPDATE` — update durable knowledge after a confirmed fix, default `true`.
- `AUTO_BUG_FIX_KNOWLEDGE_HANDOFF` — write a local handoff file when business meaning is unclear, default `true`.
- `AUTO_BUG_FIX_KNOWLEDGE_HANDOFF_DIR` — handoff subdirectory under the knowledge dir, default `handoff`.

Use this directory for reusable business meaning only: domain terms, product rules, workflow constraints, ownership notes, invariants, and integration contracts. Do not record "this bug happened" notes; the Jira ticket already does that.

If a fix reveals durable business knowledge, update or add a concise Markdown file under the knowledge directory and include it in the MR when `AUTO_BUG_FIX_KNOWLEDGE_UPDATE=true`.

If business meaning is unclear and blocks an autonomous fix, create a local handoff file when `AUTO_BUG_FIX_KNOWLEDGE_HANDOFF=true`: `<knowledge_dir>/<handoff_dir>/<TICKET_KEY>.needs-confirmation.md`. Include ticket context, evidence, exact questions, and the suggested target knowledge file. Do not commit this handoff file unless a human explicitly asks. Print its repo-relative path in the audit marker with `handoff=<path>`.

## When information is missing — ask, don't guess

If the ticket is too vague or the root cause stays unclear after code + logs, do not invent a fix. The Jira ticket is your only channel back to the human — use it instead of guessing:

```bash
jira-cli issue comment add <TICKET_KEY> --body "Blocked — need more info: <specific, answerable questions>"
jira-cli issue transitions <TICKET_KEY> --json   # find the appropriate needs-info state
jira-cli issue transition <TICKET_KEY> "<needs-info state>"
```

Then stop. Re-activation is not automatic: the poller dedupes by issue key and does not read the comment a human leaves. After the human replies, the ticket is retried only when someone re-runs `auto-bug-fix fix <TICKET_KEY>`, or when `poll.stateExpiryDays > 0` and the waiting entry has aged past it.

## Audit marker

At the end of every run, print exactly one marker line so `auto-bug-fix` can record the outcome in `state.json`:

```text
AUTO_BUG_FIX_RESULT outcome=auto-fix mr=<MR_URL>
AUTO_BUG_FIX_RESULT outcome=auto-diagnose handoff=<repo-relative-handoff-path>
AUTO_BUG_FIX_RESULT outcome=needs-info handoff=<repo-relative-handoff-path>
```

## Tooling

You drive `jira-cli`, `gitlab-cli`, and optionally `kibana-cli`. They are agent-oriented:

- Always request machine output: `jira-cli ... --json`; `gitlab-cli ... --json --compact`.
- **The commands below are illustrative shapes, not fixed scripts.** Confirm exact subcommands and flags for the installed version with `jira-cli reference` and `gitlab-cli reference --json --compact` (or `--help`) instead of guessing.
- The shell snippets are POSIX — **translate them to the OS you run on** (PowerShell on Windows, etc.).
- `gitlab-cli` write commands may require `--confirm <token>` after a `--dry-run --json` preview.
- Credentials are already configured in the environment; never print or inject tokens.

## Workflow

### Step 1 — Read the ticket

```bash
jira-cli issue get <TICKET_KEY> --json
```

Extract: `serviceName` (GitLab repo name), `errorKeywords` (exception/log fragments), `environment`.

If `serviceName` is missing, search GitLab and pick the closest match:
```bash
gitlab-cli search projects --query "<keyword-from-description>" --json
```

### Step 2 — Prepare local workspace and read the source code

1. Resolve the repo: `gitlab-cli project get <serviceName> --json`. From the result read the clone URL (`ssh_url_to_repo` / `http_url_to_repo`) **and `default_branch` — do not assume the default branch is `main`.**
2. Clone into (or reuse) `$AUTO_BUG_FIX_WORKSPACE_ROOT/<TICKET_KEY>/<serviceName>`, fetch the latest default branch, then create a work branch `fix/<TICKET_KEY>-<short-desc>` off it.
3. Read the class/method in the stack trace, the surrounding logic, and the existing test files.
4. If `AUTO_BUG_FIX_KNOWLEDGE_READ=true`, read durable knowledge under `$AUTO_BUG_FIX_KNOWLEDGE_DIR` (default `.tcl`), excluding the handoff subdirectory. Ignore the directory if it is absent.

### Step 3 — Root cause analysis (code-only)

Determine root cause from code alone. If clear → skip to Step 5. If inconclusive → Step 4.

### Step 4 — Query Kibana (only if Step 3 inconclusive)

```bash
kibana-cli search --index "app-logs-*" --query "<errorKeyword>" --last 24h --json
kibana-cli search --index "app-logs-*" --query "<errorKeyword>" --last 24h --service <serviceName> --json
```

Analyse frequency, stack traces, timestamps. Identify root cause.

### Step 5 — Write the fix

```bash
pwd
git status --short
```

Write the minimal fix in the local repo. Do not refactor unrelated code. Do not commit yet.

### Step 6 — Unit tests

```bash
# Choose the project-native test command from README/build files, then run it locally.
git status --short
git diff --check
```

**Tests exist:** run them locally. Fix any failures before continuing.

**No tests:** write a test reproducing the bug, then run the full suite.

**Loop:** if tests fail, adjust fix or test, re-run. Repeat until all pass.

### Step 6.5 — Update knowledge or create handoff

If the run is `auto-fix` and reusable business knowledge was confirmed, update `$AUTO_BUG_FIX_KNOWLEDGE_DIR` when `AUTO_BUG_FIX_KNOWLEDGE_UPDATE=true`. Keep it short and business-oriented.

If the run is `auto-diagnose` or `needs-info` because product meaning is unclear, write the handoff file described above when `AUTO_BUG_FIX_KNOWLEDGE_HANDOFF=true`, then include `handoff=<path>` in the final marker.

### Step 7 — Commit, push, and create MR

```bash
git add <changed-files>
git commit -m "fix: <short description> (<TICKET_KEY>)"
git push -u origin fix/<TICKET_KEY>-<short-desc>

gitlab-cli mr create --project <serviceName> \
  --source-branch fix/<TICKET_KEY>-<short-desc> \
  --target-branch <default_branch> \
  --title "fix: <short description> (<TICKET_KEY>)" \
  --json --compact
```

Open the MR against the repo's `default_branch` (from Step 2), not a hard-coded `main`. Include root cause, what changed, and the verifying test in the MR description (confirm the description flag via `gitlab-cli reference`). Record the MR `webUrl`.

### Step 8 — Update Jira

```bash
jira-cli issue transition <TICKET_KEY> "In Progress"

jira-cli issue comment add <TICKET_KEY> \
  --body "**Root cause:** <analysis>\n\n**Fix:** <MR_URL>\n\nUnit tests pass. Awaiting review."
```

Done. MR is ready for human review and merge.

Honor `AUTO_BUG_FIX_WORKSPACE_CLEANUP`: `keep` leaves the local workspace for debugging; `on-success` removes `$AUTO_BUG_FIX_WORKSPACE_ROOT/<TICKET_KEY>` after the MR is created; `always` may remove it before exit.

## Guardrails

- Always use `--json` when parsing CLI output
- If `issue transition` fails, check available transitions first: `jira-cli issue transitions <TICKET_KEY> --json`
- If Kibana is unavailable, document the uncertainty in the MR description and proceed with best-effort code analysis
- The poller marks each issue as `triggered` in `~/.auto-bug-fix/state.json` before spawning the agent; it will not re-trigger the same issue unless the state is cleared
- The poller injects the issue key into `agent.command` (replacing `{issueKey}`, or appending it as the last argument when the placeholder is absent)
