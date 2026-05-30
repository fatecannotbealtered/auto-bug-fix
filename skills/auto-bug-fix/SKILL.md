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

4. **Authenticate the capability CLIs (their own concern, not this config).** Make sure `jira-cli login` and `gitlab-cli auth login` are done on the machine that runs the poller; `kibana-cli auth login` is optional (only needed for the log-lookup step). You do not put their tokens in `config.json`.

5. **Start the poller in the background.** From the machine that will run it:

   ```bash
   auto-bug-fix start --detach   # background; prints PID, logs to ~/.auto-bug-fix/poller.log
   auto-bug-fix status           # check whether it is running
   auto-bug-fix stop             # stop the poller and any in-flight fix agents
   ```

   Foreground `auto-bug-fix start` is still available for debugging (Ctrl+C to stop).

6. **Verify before finishing:** run `auto-bug-fix doctor --json` and parse it. It returns `{"ok": bool, "checks": [{"level": "OK|WARN|FAIL", "name", "detail"}]}` on stdout. The checks cover config validity, the agent CLI on PATH, the **subagent template installed for your `agentType`** (a `FAIL` here means Step 1's `setup --agent` was skipped — re-run it), each capability CLI being **authenticated and reachable** (it calls `jira-cli`/`gitlab-cli`/`kibana-cli`'s own `doctor`), and the **fix scope** (`poll.filter`). On a `fix scope` `WARN` or `FAIL`, **ask the human: do you want to limit which Bugs get auto-fixed?** — a `FAIL` (no title filter and not assignee-limited = every open Bug in the instance) blocks until they narrow it. Act programmatically: any `FAIL` blocks the first fix; a `WARN` (e.g. optional `kibana-cli` not configured, a custom `agent.command` whose template can't be verified, or a broad-but-assignee-limited scope) is safe to proceed past once acknowledged.

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

- Always request machine output: `jira-cli ... --json`; `gitlab-cli ... --json --compact`. The same applies to `auto-bug-fix` itself — pass `--json` to `doctor`, `status`, `stop`, and `start --detach` so you parse a result instead of prose. (Foreground `start` streams operational logs and `fix` proxies the child agent's output, so those stay log/marker based.)
- **The commands below are illustrative shapes, not fixed scripts.** Confirm exact subcommands and flags for the installed version with `jira-cli reference` and `gitlab-cli reference --json --compact` (or `--help`) instead of guessing.
- The shell snippets are POSIX — **translate them to the OS you run on** (PowerShell on Windows, etc.).
- `gitlab-cli` write commands may require `--confirm <token>` after a `--dry-run --json` preview.
- Credentials are already configured in the environment; never print or inject tokens.

## Workflow

### Step 1 — Read the ticket

```bash
jira-cli issue get <TICKET_KEY> --json
```

Read the **whole ticket, including `description`** — not just the summary. The description carries the load-bearing detail: reproduction steps, environment/locale, the affected **service/repo**, and often an explicit **development branch** to base the fix on. (If your jira-cli build omits `description` from flat output, fall back to `jira-cli issue get <TICKET_KEY> --raw --json` and read `.fields.description`.)

Extract:
- `serviceName` — the GitLab repo (frequently named in the description, e.g. a `服务/开发分支` block).
- `baseBranch` — the **development branch named in the ticket** (e.g. `开发分支：feat-tcl-home`). This is the branch to base the fix on and to target the MR at; only fall back to the repo `default_branch` if the ticket names none.
- `errorKeywords`, `environment`/locale, and the reproduction steps.

If `serviceName` is missing, search GitLab and pick the closest match:
```bash
gitlab-cli search projects --query "<keyword-from-description>" --json
```

### Step 2 — Prepare the working copy and branch

1. Resolve the repo: `gitlab-cli project get <serviceName> --json`. Read the clone URL (`ssh_url_to_repo` / `http_url_to_repo`) and `default_branch` — **do not assume the default branch is `main`.**
2. **Choose the base branch:** the development branch from the ticket (Step 1 `baseBranch`) when present, otherwise `default_branch`.
3. **Reuse an existing checkout — never clone redundantly.** The working copy lives at `$AUTO_BUG_FIX_WORKSPACE_ROOT/<serviceName>` — **one per repo, reused across tickets, with no per-ticket subdirectory** — so warm build caches and dependencies are not thrown away on every run.
   - **Exists and clean** (`git status --porcelain` is empty): record the current branch (to restore later), `git fetch`, then check out `<baseBranch>` and fast-forward it.
   - **Exists but dirty** (uncommitted changes): **never stash or discard them.** Stop and return `needs-info`/`auto-diagnose` stating the working copy has uncommitted local changes you will not touch (or, if you must proceed, clone a throwaway copy elsewhere) — do not modify the user's work.
   - **Absent:** clone it there, then check out `<baseBranch>`.
   - **One fix per repo at a time:** a second concurrent fix targeting the same repo must wait (the shared checkout is not safe for parallel use).
4. Create the work branch off the base branch: `git checkout -b fix/<TICKET_KEY>-<short-desc>`.
5. Read the class/method in the stack trace, the surrounding logic, and the existing test files.
6. If `AUTO_BUG_FIX_KNOWLEDGE_READ=true`, read durable knowledge under `$AUTO_BUG_FIX_KNOWLEDGE_DIR` (default `.tcl`), excluding the handoff subdirectory. Ignore the directory if it is absent.

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
  --target-branch <baseBranch> \
  --title "fix: <short description> (<TICKET_KEY>)" \
  --json --compact
```

Target the MR at the **base branch from Step 2** (the ticket's development branch, or `default_branch` when the ticket names none) — never a hard-coded `main`. Include root cause, what changed, and the verifying test in the MR description (confirm the description flag via `gitlab-cli reference`). Record the MR `webUrl`.

### Step 8 — Update Jira

```bash
jira-cli issue transition <TICKET_KEY> "In Progress"

jira-cli issue comment add <TICKET_KEY> \
  --body "**Root cause:** <analysis>\n\n**Fix:** <MR_URL>\n\nUnit tests pass. Awaiting review."
```

Done. MR is ready for human review and merge.

**Leave the working copy as you found it:** check out the original branch you recorded in Step 2 (`git checkout <original-branch>`); the fix branch is already pushed, so nothing is lost. Since the checkout is reused (and may be the user's own), `AUTO_BUG_FIX_WORKSPACE_CLEANUP` applies to throwaway clones only: `keep` leaves everything; `on-success`/`always` may remove a **throwaway** clone created for a dirty/absent repo, but must **never delete a reused per-repo checkout** — restoring its branch is the cleanup.

## Guardrails

- Always use `--json` when parsing CLI output
- If `issue transition` fails, check available transitions first: `jira-cli issue transitions <TICKET_KEY> --json`
- If Kibana is unavailable, document the uncertainty in the MR description and proceed with best-effort code analysis
- The poller marks each issue as `triggered` in `~/.auto-bug-fix/state.json` before spawning the agent; it will not re-trigger the same issue unless the state is cleared
- The poller injects the issue key into `agent.command` (replacing `{issueKey}`, or appending it as the last argument when the placeholder is absent)
