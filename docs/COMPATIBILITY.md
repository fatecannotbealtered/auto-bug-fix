# Compatibility

auto-bug-fix targets Jira Data Center / Server plus GitLab, optional Kibana (logs), and optional Archery (read-only database queries) through sibling CLI tools. The Go scheduler does not call Jira, GitLab, Kibana, or Archery HTTP APIs directly; it verifies and spawns tools that do.

## CLI Protocol Baseline

| Dependency | Required | Minimum aligned version | Current local reference | Notes |
|------------|----------|-------------------------|-------------------------|-------|
| `jira-cli` | Yes | `>= 1.1.6` | `1.1.6` | JSON default, `.data` payloads, `doctor.data.checks[]`, write `--dry-run -> --confirm`. |
| `gitlab-cli` | Yes | `>= 1.2.8` | `1.2.8` | JSON default, list `.data.items[]`, camelCase fields such as `pathWithNamespace`, `webUrl`, `defaultBranch`, MR idempotency key. |
| `kibana-cli` | Optional | `>= 1.1.7` | `1.1.7` | JSON default, `search --from <window>`, `.data.hits[]`, `.data.count`, `.data.total`. |
| `archery-cli` | Optional | `>= 1.0.9` | `1.0.9` | JSON default, read-only diagnostic SELECT via `query run` (`--dangerous --dry-run -> --confirm`), `.data.columns` / `.data.rows[]` / `.data.row_count`. Requires a least-privilege **read-only DB account** on the queried instance. |
| `git` | Yes | system git | not pinned | Used by the spawned agent to clone, branch, commit, and push. |

## Backend Matrix

| Backend | Support target | Live smoke evidence |
|---------|----------------|---------------------|
| Jira | Jira Data Center / Server style APIs through `jira-cli` | Not yet recorded in this repository. |
| GitLab | GitLab projects and merge requests through `gitlab-cli` | Not yet recorded in this repository. |
| Kibana / Elasticsearch | Optional log search through `kibana-cli` | Not yet recorded in this repository. |
| Archery (database) | Optional read-only SQL queries through `archery-cli` | Not yet recorded in this repository. |

Until live smoke evidence is recorded, `auto-bug-fix reference` declares `release_readiness.level = "beta"`.

## Non-Goals

- Jira Cloud-specific accountId workflows.
- GitLab repository editing inside the scheduler binary.
- Direct Kibana/Elasticsearch HTTP calls inside the scheduler binary.
- Autonomous database writes — Archery use is read-only SELECT diagnostic only, gated by a read-only DB account.
- MR merge, production rollout, or final Jira ticket close automation.
