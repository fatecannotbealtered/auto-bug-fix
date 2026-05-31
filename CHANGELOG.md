# Changelog

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).
This project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [Unreleased]

### Added

- **The agent reads ticket comments and won't duplicate an existing fix.** Step 1 now also reads the Jira comments (`jira-cli issue comment list <KEY> --json`) for human clarifications, answers to earlier questions, and prior auto-bug-fix history. Before doing any work, the agent checks for an already-open MR for the ticket (`gitlab-cli mr list --search "<KEY>" --state opened`) — if one exists (or its own prior result comment is present) it stops and reports that MR instead of opening a duplicate. Setup guidance also suggests adding the in-flight status (e.g. `In Progress` / `In Review`) to `poll.filter.excludeStatuses` so the poller doesn't re-pick a ticket already being worked on. Synced across all four agent templates.

### Changed

- **`setup --agent kiro` now generates a standard kiro subagent.** It writes a small JSON config plus an **editable Markdown prompt** (`~/.kiro/agents/auto-bug-fix.md`) that the config references with a portable relative `file://./auto-bug-fix.md` URI — instead of borrowing the workflow from a skill via `resources: skill://…`. Setup no longer writes anything under `~/.kiro/skills/`. This cleanly separates the two audiences: the **operator skill** (`skills/auto-bug-fix/SKILL.md`, installed via `npx skills add`) is what the main agent discovers, while the **executor workflow** lives in the spawned subagent's own prompt file under `~/.kiro/agents/` (easy for a user to tweak). Previously both shared `~/.kiro/skills/auto-bug-fix/SKILL.md`, so a new main-agent session saw the execution steps instead of the operator skill. (Repo source renamed `agents/kiro/SKILL.md` → `agents/kiro/auto-bug-fix.md`.)

## [1.0.1] - 2026-06-01

### Added

- **`doctor` command** — preflight that checks config validity, that the agent CLI (argv[0] of `agent.command`) and `git` are on PATH, that the **subagent template is installed** for the configured `agentType` (FAIL if `setup --agent` was skipped; WARN for a custom command that can't be verified), and that the capability CLIs are actually **usable**: it delegates to each sibling CLI's own `doctor --json` to confirm `jira-cli`/`gitlab-cli` (required) and `kibana-cli` (optional) are authenticated and reachable. Supports `--json` for agent consumption and exits non-zero on any required failure.
- **`--json` on lifecycle commands** — `doctor`, `status`, `stop`, and `start --detach` emit structured JSON (human-readable stays the default, matching `jira-cli`/`gitlab-cli`).
- **Preflight gate** — `start` and `fix` run the doctor checks before spawning an agent and abort on any required failure, so an agent is never launched into a broken environment.
- **Effective-location awareness** — `doctor` emits an `INFO` line echoing `workspace.root` (where repos are cloned, default under home / C:\ on Windows), so users and agents notice the disk location instead of silently accepting the default.
- **Fix-scope check** — `doctor` reports the `poll.filter` blast radius so the agent can ask the user whether to limit it: no title filter **and** not assignee-limited matches every open Bug in the instance (`FAIL`, blocks); no title filter but assignee-limited is a `WARN`; a title filter is `OK`.

### Changed

- **`agent.command` is derived from `agentType` at runtime.** `setup --agent <type>` now records only `agent.agentType`; the launch command is derived when the poller runs (an explicit `agent.command` is written only for a *custom* agent with no known type). `doctor` warns if an explicit command drifts from the type. Previously setup pre-filled `agent.command`.
- **Credentials removed from the config schema.** The `jira` / `gitlab` / `kibana` host and token fields are gone from `config.json`; authentication is each capability CLI's own concern (`jira-cli login`, `gitlab-cli auth login`, optional `kibana-cli auth login`), verified by `doctor`. Existing configs that still carry those keys load fine — the extra keys are ignored.
- **Published `skills/auto-bug-fix/SKILL.md` is now operator-facing only.** The globally-discoverable skill (what the installing/main agent sees) described the full per-ticket execution workflow (Confidence Gate, Steps 1–8, result marker) — which that agent never performs and could be misled into running itself. It now covers only operating the tool: what it is, deployment model, `setup --agent`, config, and the `start`/`stop`/`status`/`fix`/`doctor` lifecycle. The per-ticket bug-fix workflow stays where it belongs — in the spawned agent's instructions under `agents/*` (installed via `setup --agent`), which are unchanged.

- **Stronger "surgical fix" guidance.** Step 5 now explicitly forbids adding redundant or speculative fallback paths (e.g. routing the same case through a second mechanism), not just unrelated refactors; if the same fix is needed on another code path, the agent must apply the *same* change rather than introduce a different mechanism. Synced across the published SKILL and all four agent templates.

- **Workflow reads the full ticket and fails fast instead of guessing the repo or branch.** The agent reads the Jira `description` (not just the summary) and determines the **entry service** and **base branch** from it; if either is unclear it stops with `needs-info` rather than searching for a repo or assuming a default branch. The base branch must exist on the remote (else `needs-info`), and both the work branch and the MR target are based on it. A *named* service is still resolved to its full GitLab path via `search projects` when `project get` 404s on a bare name (accepted only on an unambiguous match, else `needs-info`) — distinct from guessing an unnamed service. If root-cause analysis points to a **downstream service**, that repo is resolved from the evidence via `gitlab-cli` (resolve-fail → `needs-info`; cross-service changes prefer `auto-diagnose`).
- **Per-repo checkout is reused instead of re-cloned per ticket.** The working copy now lives at `$AUTO_BUG_FIX_WORKSPACE_ROOT/<repo>` (one per repo, no per-ticket subdirectory), preserving warm build caches/dependencies. Reuse only when the tree is clean; a dirty working copy is never stashed or discarded (the run stops as `needs-info`/`auto-diagnose` or uses a throwaway clone), and the original branch is restored when done. Same-repo fixes run one at a time.
- **`agent.agentType` is now primary; `agent.command` is derived at runtime.** For a known `agentType` (kiro/cursor/claude-code/codex) the launch command is computed from it on every run, so it always matches the installed subagent template and cannot drift across upgrades. An explicit `agent.command` is only needed for a custom/unknown agent (escape hatch) and, when set alongside a known `agentType`, `doctor` warns that it overrides the derived command. `setup --agent` no longer writes a `command` into config.
- **Config no longer stores or requires Jira/GitLab/Kibana hosts or tokens.** Those fields are removed from the config schema entirely. Authentication is each sibling CLI's own responsibility (`jira-cli login`, `gitlab-cli auth login`, `kibana-cli auth login`); `auto-bug-fix doctor` verifies usability without reading secrets. `setup` no longer writes credential blocks, and `Validate` no longer requires them.

### Fixed

- **No zombie agent processes on Unix.** `killTree` now reaps terminated children (`Wait4`), so stopping the poller no longer leaves defunct processes behind.
- **Concurrent-safe output capture.** The agent's tail buffer is guarded by a mutex, removing a data race when stdout and stderr are copied at the same time.
- **`fix` no longer hangs after the agent finishes.** The foreground `fix` path streamed the agent's output through an `io.MultiWriter` pipe, so `cmd.Wait` blocked until every write end closed — and a Gradle daemon (or any long-lived grandchild) the agent spawned inherited that pipe and kept it open, leaving `fix` stuck even though the agent had already exited and returned its `AUTO_BUG_FIX_RESULT`. A `cmd.WaitDelay` (default 10s, overridable via `Options.WaitDelay`) now force-closes the leftover pipe shortly after the agent exits; with no `Context`/`Cancel` set it never kills the agent or the daemon. (`start --detach` was unaffected — it writes to a real file, not a pipe.)

---

## [1.0.0] - 2026-05-29

Initial release — a deterministic scheduler that polls Jira for Bugs and hands each one to a configured AI agent. The Go binary owns config, polling, idempotency, process launch, and audit; the bug-fix workflow lives in the agent templates.

### Added

- **Go CLI** — a single self-contained binary for Windows, macOS, and Linux. Commands: `setup`, `start [--detach]`, `stop`, `status`, `fix <issueKey>`, and `version`.
- **Agent-guided setup** — `auto-bug-fix setup --agent <kiro|cursor|claude-code|codex>` installs that tool's instructions and creates `~/.auto-bug-fix/config.json` with `agent.command` pre-filled; bare `setup` writes a generic, agent-neutral template.
- **Jira polling & filtering** — `start` polls every `poll.intervalSeconds` (default 300) and applies `poll.filter` (title keyword, assignee, excluded statuses) before triggering. `poll.maxConcurrent` (default 3) caps how many fixes run at once.
- **State & idempotency** — processed issues are persisted to `~/.auto-bug-fix/state.json` as `triggered` / `done` / `failed` / `waiting` / `interrupted`, suppressing duplicate triggers across restarts. Runs that end in `needs-info` or `auto-diagnose` are recorded as `waiting` (a human now owns the ticket), not `done`; issues left mid-fix when the poller stops are reclaimed as `interrupted` and retried on the next start.
- **Retry window** — `poll.stateExpiryDays` (default `0` = never) makes aged `done` / `failed` / `waiting` issues eligible again. Otherwise `waiting` re-activation is explicit via `auto-bug-fix fix <key>`; the poller does not read ticket comments, so a human reply alone does not re-trigger.
- **Agent command** — a single `agent.command` adapts to any agent CLI: `{issueKey}` is substituted in place, or appended as the last argument. Commands are spawned without a shell (no pipe/redirect/variable interpretation).
- **Audit marker** — the agent's final `AUTO_BUG_FIX_RESULT` line (outcome, MR URL, handoff path) is parsed into state alongside exit code, duration, attempts, and errors.
- **Workspace & knowledge** — workspace root, cleanup policy, and repo-local knowledge/handoff settings are passed to agents as environment variables; secrets are not injected.
- **Background poller lifecycle** — `start --detach` runs detached (PID/log at `~/.auto-bug-fix/poller.{pid,log}`); `stop` terminates the poller and its in-flight fix agents; `status` reports whether it is running.
- **Config loading** — JSON config with `$ENV_VAR` substitution; unresolved placeholders are logged at load time instead of silently becoming empty strings.
- **Agent instructions** — intent-first execution templates for Kiro, Cursor, Claude Code, and Codex under `agents/`, plus a framework-neutral published skill at `skills/auto-bug-fix/SKILL.md`. They drive `jira-cli` / `gitlab-cli` with the repo's default branch detected at runtime rather than assumed.
- **Distribution** — published to npm as `@fatecannotbealtered-/auto-bug-fix`: a `postinstall` script downloads the matching prebuilt binary (darwin/linux/windows × amd64/arm64, with Windows arm64 falling back to amd64) from GitHub Releases and verifies its checksum. Releases are produced by GoReleaser on tag push; `go install` and direct Release downloads remain supported. CI builds and tests on Linux, macOS, and Windows.
- **Docs** — a single bilingual README (English / 中文) covering install, agent command, configuration, running the poller, state, repo knowledge, and design/non-goals; plus a contribution guide, changelog, and security policy.
