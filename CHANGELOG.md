# Changelog

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).
This project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [Unreleased]

### Added

- **`doctor` command** — preflight that checks config validity, that the agent CLI (argv[0] of `agent.command`) and `git` are on PATH, that the **subagent template is installed** for the configured `agentType` (FAIL if `setup --agent` was skipped; WARN for a custom command that can't be verified), and that the capability CLIs are actually **usable**: it delegates to each sibling CLI's own `doctor --json` to confirm `jira-cli`/`gitlab-cli` (required) and `kibana-cli` (optional) are authenticated and reachable. Supports `--json` for agent consumption and exits non-zero on any required failure.
- **`--json` on lifecycle commands** — `doctor`, `status`, `stop`, and `start --detach` emit structured JSON (human-readable stays the default, matching `jira-cli`/`gitlab-cli`).
- **Preflight gate** — `start` and `fix` run the doctor checks before spawning an agent and abort on any required failure, so an agent is never launched into a broken environment.

### Changed

- **Config no longer stores or requires Jira/GitLab/Kibana hosts or tokens.** Those fields are removed from the config schema entirely. Authentication is each sibling CLI's own responsibility (`jira-cli login`, `gitlab-cli auth login`, `kibana-cli auth login`); `auto-bug-fix doctor` verifies usability without reading secrets. `setup` no longer writes credential blocks, and `Validate` no longer requires them.

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
