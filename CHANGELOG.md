# Changelog

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).
This project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [Unreleased]

## [1.0.13] - 2026-06-25

### Added

- **Contract single-source (`internal/contract`): `schema_version`, exit codes, and retryability are now generated from `contract/contract.json` (ai-native-cli-spec@v1.4) and cannot drift from the fleet contract.** `schemaVersion` in `cmd.go` is aliased to `contract.SchemaVersion`; `statusToCode` and `exitCodeForError` delegate to `contract.ExitFor`/`contract.Retryable`; `referenceErrorCodes` (used by `reference`) derives its table from `contract.Codes` so `reference` self-description can never drift. `E_RUNTIME` is registered as an ext code in `contract/contract-ext.json`.
- **Conformance test (`cmd/contract_conformance_test.go`) asserts every emitted E_* code is in the canonical contract with exact exit + retryable, schema_version matches, and envelope keys are within the canonical sets.** CI-red guard against future drift.
- **`check-spec.js` added to CI** (Verify spec/contract sync step, Linux only) to guard against the generated `internal/contract/contract_gen.go` going stale.

### Changed

- **`.agent` spec docs synced from ai-native-cli-spec@v1.4** (AGENT.md, CLI-SPEC.md, SEC-SPEC.md, SKILL-SPEC.md and their `_zh` variants).
- **`update` npm path now uses `updateRunPackageManager` seam** (points to `updateRunCommand` by default) so tests can stub the npm install step without shelling out to real npm.

## [1.0.12] - 2026-06-25

### Changed

- **Windows binary self-update now replaces the running executable in place atomically, the same way Unix does — no more deferred `.cmd` restart script.** `update` on a raw-binary Windows install used to copy the new binary to `<exe>.new`, spawn a detached `.cmd` that polled `move`-on-restart, and return `status:"scheduled"` (`pending_path` set) so the swap only completed after the process exited. It now uses the cross-platform rename trick (write `.<base>.new` → rename the in-use binary aside to `.<base>.old` → rename `.new` into place → restore from `.old` on failure → remove `.old`, ignoring a still-locked leftover on Windows), completing the swap immediately and returning `status:"installed"` with `binary_replaced:true`. The `scheduled` status and `pending_path` field are gone.

### Fixed

- **`update` no longer misclassifies transient network failures during signature/checksum verification as non-retryable integrity failures, and a SIGINT in those stages now exits cleanly.** A failed download of the Sigstore signature bundle, and a transient failure fetching the embedded TUF trust root inside in-process verification, were both collapsed into non-retryable `E_INTEGRITY` (exit 1) — telling the agent to stop and report a forged release when the real cause was a network blip it should retry. Those network steps are now classified by the taxonomy as retryable (`E_NETWORK`/`E_SERVER`/`E_TIMEOUT` → 7/8); only a true signature/identity verdict or checksum mismatch stays non-retryable `E_INTEGRITY`. The `verify_signature`/`verify_checksum` stages also now check the update context, so a Ctrl-C/SIGTERM there emits a terminal `E_INTERRUPTED` envelope (exit 130, `binary_replaced:false`, "no change") instead of being swallowed.
- **`update` discover/download HTTP failures are now classified by status code instead of being flattened into `E_NETWORK`.** A non-2xx response from the GitHub releases API (or an asset CDN) was surfaced as an untyped error that the discover/download fallback turned into retryable `E_NETWORK` — so a `404` (release/asset not found) or `403` looked like a transient blip to loop on. Those responses now carry their HTTP status through the single `statusToCode` mapping (CLI-SPEC §6): `404 → E_NOT_FOUND` (3), `401 → E_AUTH` / `403 → E_FORBIDDEN` (4), `409 → E_CONFLICT` (6), `429 → E_RATE_LIMITED` / `5xx → E_SERVER` (7), `408 → E_TIMEOUT` (8); only genuine transport errors (DNS/dial) remain `E_NETWORK`.

## [1.0.11] - 2026-06-24

### Fixed

- **An unusable/misconfigured notification CLI no longer blocks `fix`/`start`, and a healthy `lark-cli` is no longer misreported as unusable.** `doctor` health-checked the notify channel through the fateforge sibling contract (`<cli> doctor --json --quiet` + a `{data:{authValid}}` envelope), but `lark-cli` is not a sibling: it rejects `--json` and emits a flat `{ok, checks:[{name,status}], _notice}`. So a healthy, authenticated Lark CLI was judged "not usable", and because the notify check was a hard `Fail` it tripped the preflight gate — `fix`/`start`/`start --detach` exited `E_CONFIG` without ever spawning the agent. Channels now self-report health via a new `Channel.Healthy(run)` (Lark runs `lark-cli doctor` with no `--json` and trusts lark-cli's own top-level `ok`, ignoring the update `_notice`), and the notify check is **advisory**: an unusable channel shows as a `Warn` in `doctor` but never blocks a running fix (notifications are best-effort). The fateforge siblings (jira/gitlab/kibana/archery) keep the `doctor --json` capability check unchanged. Tests now feed lark-cli's real flat doctor shape and assert the check never passes `--json`.

## [1.0.10] - 2026-06-24

### Added

- **Two-phase pre-write evidence gate (`verify.enabled`, default off) so an auto-fix is independently reviewed BEFORE any MR is opened.** When enabled, an `auto-fix` is split into three harness-orchestrated spawns: an **investigate** phase reads/triages/analyzes, writes the minimal fix, runs tests, and commits **locally** on the work branch — no push, no MR, no Jira write — then emits an `AUTO_BUG_FIX_PROPOSAL` marker carrying the workspace, branch, base, committed `head` SHA, and an `evidence.json` path; the harness integrity-checks the proposal against the **real git checkout it owns** (workspace under the configured root, branch `fix/*`, `HEAD` matches the reported commit, diff vs base non-empty); an independent **read-only verifier** spawn reviews the real diff plus the evidence record and emits `AUTO_BUG_FIX_VERIFY verdict=uphold|refute`; only an upheld proposal proceeds to an **execute** phase that pushes and opens the MR. A refuted or integrity-failed proposal is **downgraded to `auto-diagnose` with no MR**, with the verdict + reason persisted to state (`verdict` / `verifyReason`). The verifier holds no write credentials, so its worst case is a false downgrade, never a bad MR. Honest boundary: the investigate/verify read-only posture is template + prompt convention, not an OS sandbox. Costs 2-3 agent spawns per auto-fix; `auto-diagnose`/`needs-info` stay a single spawn. With `verify.enabled=false` the flow is the unchanged single-shot run.
- **New marker prefixes `AUTO_BUG_FIX_PROPOSAL` and `AUTO_BUG_FIX_VERIFY`** (distinct from terminal `AUTO_BUG_FIX_RESULT`), parsed by a generalized `ParseMarker`; the live scanner now watches per-phase prefixes. `ParseResultMarker` and the single-shot path stay backward compatible.
- **New `internal/git` and `internal/guard` packages** (read-only diff/HEAD/branch inspection; the `RunGuarded` two-phase orchestration); `internal/state` records `verdict`/`verifyReason`; all four agent templates gain an Execution Phases section, guarded by the template invariants test.
- **Completion notification is now a multi-channel abstraction, on by default, with a mandatory delivery-CLI preflight.** Notifications go through a `Channel` abstraction selected by a new `notify.channel` config field (default `lark`; only the Lark/Feishu interactive card via `lark-cli` is implemented today). `notify.enabled` now defaults to **`true`** — at this stage the completion card is the human-in-the-loop hand-off, not a nicety — and when enabled `notify.target` (the fallback `chat_id`/`open_id`) is **required**, with config validation failing otherwise. `doctor` now does a **mandatory present-and-authenticated check** of the channel's delivery CLI (`lark-cli`), reusing the sibling `lark-cli doctor --json` contract: a missing or unusable CLI is a **FAIL** (previously a PATH-presence WARN); the FAIL affects only `doctor`'s own exit code and never blocks a running fix. The poller injects a new `AUTO_BUG_FIX_NOTIFY_CHANNEL` env var (beside `AUTO_BUG_FIX_NOTIFY_ENABLED` / `AUTO_BUG_FIX_NOTIFY_TARGET`), and `auto-bug-fix notify` gains a `--channel` flag (default lark, falling back to `$AUTO_BUG_FIX_NOTIFY_CHANNEL`). A send now **retries once and logs loudly on failure**, but still never fails, blocks, or changes the fix outcome — the "don't fail the run" guarantee is unchanged; what changed is that failures are no longer silent.
- **Optional one-way Lark (Feishu) completion notification with a fixed, Go-rendered card.** After a fix run, the agent sends a single interactive Lark card to the Jira follow-up owner (assignee) summarising the outcome (`auto-fix`/`auto-diagnose`/`needs-info`), root cause (【问题原因】), and change (【解决方案】), with jump-out buttons to the Jira issue and the GitLab MR. The card **layout is fixed in `internal/notify` and rendered by a new `auto-bug-fix notify` command** (header colour, the "next step" line, and the MR button are derived from `--outcome`), so the style never drifts across runs/agents/models — the agent supplies only flat fields and builds no card JSON. `notify` sends via `lark-cli` (Lark auth lives there — no secrets in config) and is exempt from the confirm-token gate like `update`; `--dry-run` renders a preview without sending. Opt-in via a new `notify` config block (`notify.enabled`, default `false`; `notify.target` a non-secret fallback Lark `chat_id`/`open_id`), injected to the agent as `AUTO_BUG_FIX_NOTIFY_ENABLED` / `AUTO_BUG_FIX_NOTIFY_TARGET`; the agent resolves the assignee → open_id via the lark-contact skill. It is **best-effort** — a failed send never fails, retries, or changes the fix outcome. `doctor` adds a notify-gated `lark-cli` PATH check (WARN-only) when notifications are enabled. Added Step 8.5 to all four agent templates, guarded by a template invariant. Kibana/Archery evidence is shown as text for now — a one-click deep link is a seam pending kibana-cli (kibana-cli#11).
- **Orchestrator-side fallback completion notification — the card no longer depends on the agent self-invoking Step 8.5.** When a spawned agent finishes WITHOUT printing an `AUTO_BUG_FIX_RESULT` marker (so it almost certainly skipped its Step 8.5 `notify` call), `auto-bug-fix` now sends a degraded **needs-review** card itself — on both the manual `fix` path and the unattended poller — telling the assignee the bot ran but returned no structured result, please verify the MR / Jira. A new internal `needs-review` notify outcome (grey header, "⚠️ 机器人已运行，请人工核对") backs the card; it is internal-only and the `notify` CLI rejects `--outcome needs-review`. A marker-bearing run is trusted to have already sent its own card and is not double-notified. Still best-effort: a fallback send never fails or changes the fix outcome.

### Changed

- **All four agent templates now make the completion marker mandatory** — "you must print exactly one as your final line, even when the fix fully succeeded" — and explain that omitting it gets the run misreported as a failure and the Step 8.5 notification treated as not sent. The cursor template additionally stresses Step 8.5 is a required closing step and documents the orchestrator's degraded-card safety net.

### Fixed

- **`doctor` no longer misreports an authenticated `kibana-cli` as "not authenticated".** The capability check derived auth from a sibling CLI's `doctor` checks array as `auth && network`, but `kibana-cli` names its connectivity check `search`, not `network` (jira-cli/gitlab-cli use `network`), so a healthy kibana-cli's `networkPass` was always false and it was reported unauthenticated. `parseCLIHealth` now trusts the CLI's own authoritative `data.authValid` verdict first, falling back to the checks-array heuristic only when that field is absent.
- **A clean agent exit with no marker is no longer misreported as a fix failure.** When the spawned agent exited 0 but a detached grandchild kept the stdout pipe open, `cmd.Run` returned `exec.ErrWaitDelay` ("WaitDelay expired before I/O complete"), which `runFix` and the poller surfaced as an `E_RUNTIME` failure even though the fix had already succeeded — and in the poller it recorded a permanent `StatusFailed` that was never retried. `Trigger` now treats `ErrWaitDelay` as the clean exit it is (the process already exited 0), flagging the run `NoMarker` instead of failing it; genuine non-zero exits still fail.

## [1.0.9] - 2026-06-22

### Added

- **Update-available notice now appears in every command's `meta.notices` (read-only from cache) and is severity-graded.** The cached update notice is attached to the envelope `meta.notices` (omitempty) on **any** command, read **only from the local update cache** (`~/.auto-bug-fix/update-cache.json`) with **zero network I/O** — business commands surface the cached notice, they never phone home. It is omitted when the cache holds no available update. The fresh/active view stays in `data.notices` on `context`/`doctor`/`update --check`. The notice now carries a `severity` graded at check time from the embedded CHANGELOG delta between the running and latest versions: `warning` when the delta contains a `security` entry OR the latest crosses a major version, otherwise `info` (`critical` is reserved and not derived from the changelog).

## [1.0.8] - 2026-06-21

### Added

- **In-process Sigstore signature + checksum verification on the binary self-update path.** A raw-binary install (not managed by npm) now self-updates directly from the signed GitHub release: it resolves the release, downloads the platform archive, `checksums.txt`, and `checksums.txt.sigstore.json`, then verifies the cosign Sigstore signature on `checksums.txt` **in-process** against this repo's tagged release-workflow identity (`…/auto-bug-fix/.github/workflows/release.yml@refs/tags/v*`, OIDC issuer `https://token.actions.githubusercontent.com`) before verifying the archive SHA256, and only then atomically replaces the running binary and syncs the Skill. The trust anchor is sigstore-go's embedded TUF root — there is no external `cosign`, no user-environment dependency, and no skip path. Verification is **fail-closed**: a missing signature bundle, a signature that does not verify, an identity/issuer mismatch, or a checksum mismatch all return `E_INTEGRITY` (exit 1, non-retryable). The signature is always verified **before** the checksum. The success envelope carries `signature_status: "verified"` and `signature_verified: true`; the staged failure/interrupt envelope now includes the `verify_signature` and `verify_checksum` stages.
- **Raw-binary installs are no longer refused.** `update` previously rejected non-npm installs with `E_CONFIG`; it now routes them to the signed binary path above. npm-managed installs keep the existing `npm install -g` + Skill-sync path unchanged (signature fields stay package-manager managed).

## [1.0.7] - 2026-06-21

### Changed

- **`update` is now a single command with no confirm token.** A bare `auto-bug-fix update` performs the whole self-update in one call — resolve latest (or `--target-version`), update the npm package, then sync the Skill — and is exempt from the `--dry-run → --confirm <token>` write gate (self-update is single-target, non-destructive, and self-verifying; the other data-write commands keep their confirm gate unchanged). `update --check` and `update --dry-run` stay as optional read-only flags; `--dry-run` is now a tokenless preview that issues no `confirm_token`/`expires_at`. `update` is idempotent: already-latest returns `ok` with a no-op result.
- **Staged update failure/interruption envelope.** Update runs as staged work (`discover → replace → skill_sync`); every failure and the interrupt path now carry `stage`, `current_version`, `binary_replaced`, and `skill_sync_status`. Local `replace` failures are now classified `E_IO` (exit 1) or `E_FORBIDDEN` (exit 4) instead of being misreported as `E_NETWORK`. A Skill-sync failure after a successful package update is now **partial success** (`ok:false`, `binary_replaced:true`, retryable, with `skill_sync_command`) so the agent knows it is on the new binary and only needs to re-run the sync.

### Added

- **SIGINT/SIGTERM trap on `update`.** An interrupted update unwinds to a clean state, cleans up, and still emits a terminal JSON envelope (`E_INTERRUPTED`, exit 130) stating the true post-state (before the swap: "no change, still on <current>"; during skill sync: partial success with `skill_sync_command`).
- **New error codes `E_IO` (exit 1) and `E_INTERRUPTED` (exit 130)** added to the error/exit mapping and to `reference`.

## [1.0.6] - 2026-06-19

### Added

- **Early triage gate (Step 1.5) in all four agent templates — route spec-gap/UX tickets to `needs-info` before cloning.** Between reading the ticket and preparing the workspace, the agent now classifies it as a *deterministic defect* vs. a *spec-gap / enhancement / UX request*. Suggestion-shaped wording, an Expected Result that asks for **new behavior** (not a deviation from existing behavior), an undefined or multi-readable expected term, a competitor comparison, or a `component` in a **different layer than the named service** route to a **bounded check** — inspect only the ticket's named entry point (no clone-wide spelunking, no Kibana) — and otherwise return `needs-info` immediately with specific spec questions, instead of grinding the wrong layer to manufacture a fix for an unspecified ask. A trace/message id now triggers the Step 4 runtime-evidence requirement **only on the deterministic-defect path**. Motivated by a real run where the strongest model spent ~18 minutes investigating a backend protocol detail for a ticket whose real ask was an undefined, frontend-rendering UX question — the outcome (`needs-info`) was correct but the path was not. It adds no new outcome type and does not loosen the Confidence Gate; it only makes `needs-info` arrive earlier and cheaper for that class. Guarded by a new invariant so it stays present across all four templates.
- **AI-native self-description commands.** Added `context`, `reference`, and `changelog` JSON-envelope commands. `reference` exposes `release_readiness`, and `doctor --json` now includes a matching `release_readiness` check.
- **Template governance files.** Added `NOTICE.md`, `docs/COMPATIBILITY.md`, `docs/E2E.md`, `docs/LIVE-SMOKE-EVIDENCE.md`, the canonical `README_zh.md`, and `skills/auto-bug-fix/test-prompts.json`.
- **Self-update contract.** Added `update --check`, `update --dry-run`, and `update --confirm <confirm_token>` for npm package updates plus bundled Skill sync.
- **FCC release gate enforced in tests.** Added `cmd/fcc_guard_test.go`, which enumerates every leaf command from `reference` and fails if any lacks a command-level test, plus command-level tests for all leaf commands — so `release_readiness.fcc_status: "verified"` is backed by real coverage rather than asserted.
- **CI lint gate and dependency monitoring.** Added a `golangci-lint` job (golangci-lint v2) so the committed `.golangci.yml` is actually enforced, and `.github/dependabot.yml` for `github-actions`/`npm`/`gomod` update monitoring.
- **Local update-notice cache.** `update --check` now writes a short user-scoped cache; `context` surfaces `data.notices[]` from it with no network call, so an available update reaches read-only commands.
- **Repo-knowledge layout and `caveats.md` convention.** Documented the `.repo-knowledge/` structure (`routing` / `glossary` / `domain-rules` / `integrations` / `caveats` + `handoff/`) in the operator reference, and wired it into all four subagent templates: the execution agent now reads `caveats.md` and `routing.md` first and, on a match, obeys the caveat's boundary instead of "fixing" load-bearing code that only looks wrong (kept for history, external-system accommodation, or an industry rule). A new template invariant keeps the policy equivalent across all four agents.
- **Optional Archery database-state evidence channel.** Wired `archery-cli` as an optional sibling CLI (doctor WARN when absent), giving the spawned agent a third runtime-evidence source beside code and Kibana logs: read-only SELECT queries via `archery-cli query run` to inspect data state during root-cause analysis (Step 4, chosen by the lead — logs→kibana, data→archery). Writes are out of scope and prevented at the database via a required least-privilege read-only DB account; query rows are tagged `_untrusted`. Wired into all four templates (with an invariant), `internal/installer` `CLISkills`, and `internal/doctor`; documented in COMPATIBILITY/SECURITY/NOTICE. No new config field — credentials live in `archery-cli`.

### Changed

- **CLI output now follows the AI-native contract by default.** Commands default to JSON envelopes, `--json` is a compatibility alias, `--format text` opts into prose, and failures use `ok:false` with stable `E_*` codes.
- **Mutating scheduler commands are dry-run guarded.** `setup`, `start`, `stop`, `fix`, and `update` now require `--dry-run` before `--confirm <confirm_token>`.
- **Aligned the spawned-agent templates with the current sibling CLI contracts.** The workflow now reads Jira and GitLab JSON from `.data`, uses Jira/GitLab dry-run confirmation tokens for writes, reads GitLab list results from `.data.items[]`, uses `defaultBranch`/`pathWithNamespace`/`webUrl`, and calls `kibana-cli search --from now-24h` instead of the removed `--last` flag.
- **Npm distribution now uses `@fateforge` optional platform packages.** The root package is `@fateforge/auto-bug-fix`; platform binaries are published as optional OS/CPU packages prepared from GoReleaser artifacts. The release workflow publishes platform packages before the root package and uses npm provenance.
- **Release hardening.** CI and release source the Go version from `go.mod`; GoReleaser signs `checksums.txt` with keyless cosign.
- **Errors are classified by type, not message text.** `exitCodeForError` now maps an explicit coded error or a typed HTTP status onto the `E_*`/exit-code taxonomy in one place (CLI-SPEC §6); the npm registry check maps its status code instead of collapsing every failure into `E_NETWORK`.
- **PIDs serialized as strings** in `start`, `status`, and `context` JSON output, per the all-IDs-are-strings machine-contract convention.
- **Version single-sourced into Go.** The `CHANGELOG.md` embed variable is exported (`ChangelogMarkdown`), and the `cmd/cmd.go` version literal is registered in the version sync/check registry so it can never drift from `package.json`.
- **Default repo-knowledge directory renamed `.tcl` → `.repo-knowledge`.** The old name was opaque and collided with the Tcl language; the new name states what the directory holds (reusable repo/domain knowledge). The default (`internal/config/config.go` `DefaultKnowledgeDir`) and all four agent templates, docs, and examples use the new name; existing configs that set `knowledge.dir` explicitly are unaffected.
- **Subagent templates defer exact CLI syntax to each CLI's skill.** The four execution templates now hard-code only auto-bug-fix's own procedure and safety — workflow sequence, the dry-run→confirm write flow, resolve-to-full-namespaced-path / idempotency / never-assume-`main`, the three outcomes, the Confidence Gate, the caveats / SELECT-only / never-merge boundaries, the audit marker, and the business-language Jira template — and **pointerize** exact sibling-CLI subcommands, flags, and JSON field paths to each CLI's own skill/reference, removing duplicated, drift-prone contract. A prominent "before driving any sibling CLI, load its skill" mandate is added (guarded by an invariant). Template invariants are refocused: CLI field-path assertions dropped; the stale-pattern guard (`--last`, `default_branch`, …) kept as regression insurance and renamed `TestAgentTemplates_NoStaleCLISyntax`; orchestration-policy invariants (full-path, idempotency, no-assume-`main`, write dry-run→confirm) added.

### Fixed

- **`auto-bug-fix doctor` understands current `jira-cli doctor` output.** The preflight now treats Jira as usable when its enveloped `data.checks[]` reports both `auth` and `network` as `pass`, instead of requiring the legacy `authValid` field.
- **Jira polling no longer silently ignores CLI error envelopes.** `jira-cli search` responses with `ok:false` now surface as poll errors instead of looking like an empty issue list.
- **`reference` schemas now match actual output.** `fix` emits the `_untrusted` list it declares (tagging the agent-derived `outcome`/`mrUrl`/`handoffPath`), and `context` emits the `notices` field its schema declares — so the self-description no longer over-promises fields the command never returns.
- **Self-update integrity documented and scoped.** `SECURITY.md` now states the npm-provenance-anchored integrity model, and the npm shell no longer points users at a non-existent standalone self-update path.
- **Vibe-tool adapter conformance (verified against each tool's official docs/source).** Codex: replaced the non-existent `/use jira-cli` skill command with the official `$jira-cli` name invocation (`agents/codex/AGENTS.md`). Cursor: the workflow now installs as a global Agent Skill at `~/.cursor/skills/auto-bug-fix/SKILL.md` (which Cursor auto-loads) instead of a home `~/.cursor/rules/*.mdc` rule, which Cursor does not auto-load — only a project's `.cursor/rules/` and Settings User Rules are. Kiro was verified fully conformant (`file://` prompt, `skill://` resources, `model` field, `~/.kiro/agents/`); its headless launch now pre-trusts exactly the agent's declared tools via `--trust-tools=fs_read,fs_write,execute_bash,grep,glob` instead of the broad `--trust-all-tools` (least privilege; also sidesteps the scripted-startup prompt reported in Kiro #7398). Known upstream risk: Claude Code's `claude --agent <name> -p` may not load the subagent in headless/print mode (anthropics/claude-code#15815); test against the installed version.

## [1.0.5] - 2026-06-02

### Added

- **CLI skills are injected into the spawned agent and enforced by `doctor`.** `setup` now wires the `jira-cli`/`gitlab-cli`/`kibana-cli` skills into the executor per each tool's native mechanism — kiro via `resources` (`skill://` to its own `~/.kiro/skills`), claude-code via the subagent `skills:` frontmatter, codex/cursor via a Tools-section reference — so the agent uses the correct CLI commands instead of guessing flags. `doctor` verifies the required skills (`jira-cli`/`gitlab-cli`) are present in the agent's **own** skill directory and fails preflight if missing (kibana-cli is optional → WARN), with an actionable `npx skills add fatecannotbealtered/<skill> -g -a <agent> -y` hint.

### Changed

- **General root-cause principles now apply on every path.** The empty-result/observability-gap/external-contract principles were moved out of the Kibana-only Step 4 into the Confidence Gate, so they govern the auto-fix decision even when the cause looks clear from code alone (all four templates).

### Fixed

- **codex Setup Mode** no longer lists `jira.host`/`gitlab.host` config fields that are not part of the schema (authentication lives in the sibling CLIs).

## [1.0.4] - 2026-06-02

### Fixed

- **The poller/`fix` no longer hangs after a fix completes.** A spawned agent could finish its work (printing the `AUTO_BUG_FIX_RESULT` marker, after which the MR/comment are already persisted) yet leave the direct child alive — e.g. blocked on a detached build daemon (Gradle) that inherited the stdout pipe — so `agent.Trigger`'s `cmd.Wait` blocked indefinitely (the existing `WaitDelay` only fires *after* the child exits, which never happened). Trigger now watches the live output stream and, on the completion marker, kills the spawned process so it returns promptly; a marker-triggered kill is reported as success. Detection is incremental (newline-delimited, prefix compare) and ignores the documented `<...>` placeholder example lines so a verbose agent echoing the template can't cause a premature kill. Agent-agnostic — works for kiro/cursor/claude-code/codex via the shared marker contract.

### Changed

- **Hardened the agent-template guardrails with evidence/observability principles** (all four templates, guarded by new invariants):
  - An empty, missing, or silent result is a **symptom, not a root cause** — find the condition that produces it; never paper over it with a generic fallback.
  - When the deciding signal is **not in the code or logs**, that is an observability gap: **make it observable first** (log the full upstream result — status, reason/marker, identifiers) and `auto-diagnose`, instead of shipping handling for a cause never observed.
  - **External contracts are confirmed, not inferred** — carry the upstream's own trace/response id to the owning team or its docs and confirm the contract; otherwise `auto-diagnose`/`needs-info`.
  - **Never funnel an entire class of outcomes through one catch-all branch** — that masks distinct causes; handle the specific confirmed signal and reuse the codebase's existing analogous handling.

## [1.0.3] - 2026-06-02

### Added

- **`agent.model` — required model pinning per agent.** A known `agentType` must now specify `agent.model` (validation fails otherwise), so an unattended fix never silently falls back to a CLI's default model. The model is applied per the agent's own mechanism — no forced uniformity: `cursor` / `claude-code` / `codex` accept a `--model` flag, so it is injected into the derived command and always tracks config; **kiro-cli `chat` has no `--model` flag**, so the model is written into the kiro agent JSON (`~/.kiro/agents/auto-bug-fix.json`) by `setup --agent kiro` (re-run it after changing `agent.model` for kiro). Flag syntax verified against each CLI's official docs. Custom agents (empty `agentType`) are unaffected — put the model in your `agent.command`.

### Docs

- Aligned docs with the code: removed the stale `jira`/`gitlab`/`kibana` token blocks from the README config examples (those keys are not part of the config schema — credentials live in the sibling CLIs); the config examples now store only `agentType` for a known agent (the command is derived at runtime); clarified that `agent.command` is required only for a custom agent; added `doctor` to the CONTRIBUTING command list.

### Changed

- **Hardened the per-ticket workflow guardrails in all four agent templates** (kiro/cursor/claude-code/codex), driven by a real misfire where an agent shipped a clean-looking but inert fix built on a wrong link model:
  - Step 3 — **Behavior variants**: when the ticket implies a variant (region, channel, tenant, rollout flag, platform), locate the code path that *actually serves it* and trace the root-cause signal to its construction site; consult the repo knowledge base for variant→path mappings; treat code comments/names/docs as unverified hints, never evidence.
  - Step 4 — **Runtime evidence gate**: when the ticket gives a runtime clue or only reproduces in a specific environment, runtime evidence is required; if kibana-cli is unavailable or returns nothing, must not `auto-fix` — downgrade to `auto-diagnose`/`needs-info`.
  - Confidence Gate — **`auto-fix` now requires evidence independent of the code/tests the agent writes** (runtime logs, a reproduction, or a ticket fact); a self-authored test does not count as root-cause evidence.

## [1.0.2] - 2026-06-01

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
- **Distribution** — published to npm as `@fateforge/auto-bug-fix`: a `postinstall` script downloads the matching prebuilt binary (darwin/linux/windows × amd64/arm64, with Windows arm64 falling back to amd64) from GitHub Releases and verifies its checksum. Releases are produced by GoReleaser on tag push; `go install` and direct Release downloads remain supported. CI builds and tests on Linux, macOS, and Windows.
- **Docs** — a single bilingual README (English / 中文) covering install, agent command, configuration, running the poller, state, repo knowledge, and design/non-goals; plus a contribution guide, changelog, and security policy.
