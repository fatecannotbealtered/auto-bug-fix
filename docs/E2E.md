# E2E Testing

auto-bug-fix E2E testing requires a disposable Jira Bug, a GitLab project that the spawned agent may branch and push to, and optional Kibana logs. Do not run E2E against production tickets unless the user explicitly accepts the workflow.

## Prerequisites

```bash
jira-cli context --compact
jira-cli doctor --compact
gitlab-cli context --compact
gitlab-cli doctor --compact
kibana-cli context --compact
kibana-cli doctor --compact
auto-bug-fix doctor --compact
```

Required local setup:

- `jira-cli` authenticated to a Jira Data Center / Server instance.
- `gitlab-cli` authenticated to the GitLab instance that hosts the test repository.
- `git` configured with an identity that can push branches.
- A selected agent template installed with `auto-bug-fix setup --agent <type>`.
- Optional `kibana-cli` authentication when the test issue includes a trace/log lookup path.

## Read-Only Smoke

```bash
auto-bug-fix context --compact
auto-bug-fix reference --compact
auto-bug-fix changelog --since 1.0.4 --compact
auto-bug-fix doctor --compact
auto-bug-fix status --compact
```

## Mutating Smoke

Use a disposable test Bug key:

```bash
auto-bug-fix fix TEST-123 --dry-run --compact
auto-bug-fix fix TEST-123 --confirm <confirm_token> --compact
```

Record:

- Jira issue key and initial status.
- GitLab project path and branch created by the spawned agent.
- MR URL or `needs-info` / `auto-diagnose` outcome.
- Commands run and timestamps.
- Whether `jira-cli`, `gitlab-cli`, and optional `kibana-cli` doctor checks passed.

Store evidence outside the repository unless all identifiers are sanitized. A summarized, redacted run can be added to `docs/LIVE-SMOKE-EVIDENCE.md`.
