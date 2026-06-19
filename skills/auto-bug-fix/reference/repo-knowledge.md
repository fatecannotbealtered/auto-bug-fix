# Repo Knowledge Layout

This file specifies how the repo-local knowledge directory (`knowledge.dir`,
default `.repo-knowledge/`) is organized, so the spawned agent can read and
update it predictably and a human can review it. It is a **lightweight
convention, not a strict schema**: only `README.md` is required; every other
file exists only when there is knowledge of that kind to record.

## Contents

- [Purpose](#purpose)
- [What belongs here](#what-belongs-here)
- [Directory layout](#directory-layout)
- [Knowledge file skeleton](#knowledge-file-skeleton)
- [caveats.md — load-bearing weirdness](#caveatsmd--load-bearing-weirdness)
- [routing.md — triage index](#routingmd--triage-index)
- [Maintenance contract](#maintenance-contract)

## Purpose

The directory holds **reusable, repo-level domain knowledge** that the spawned
fix agent consumes: to route a ticket to the right layer/owner, to disambiguate
domain terms, and — most importantly — to avoid "fixing" code that only looks
wrong. It travels with the code and is committed in the fix MR.

The consumer is an AI agent, so the value is **predictable read/write**, not
human prose. Keep entries short and business-oriented.

## What belongs here

In scope: domain terms, product rules, workflow constraints, ownership/routing,
invariants, integration contracts, and **caveats** (intentional, non-obvious
constraints — see below).

Out of scope: one-off bug narratives and a "what we fixed when" ledger. That
history already lives in git, the MR, the Jira ticket, and `CHANGELOG.md`.
Traceability for a knowledge entry is the `sources:` front-matter, not a
separate log.

## Directory layout

```text
.repo-knowledge/
  README.md         # required: one-paragraph purpose + index of the files below
  routing.md        # ticket class / symptom -> owning service, layer, or owner
  glossary.md       # domain term -> definition (disambiguates undefined terms)
  domain-rules.md   # product rules, business invariants, workflow constraints
  integrations.md   # external system interfaces and integration contracts
  caveats.md        # intentional, non-obvious constraints — do not "fix" these
  handoff/          # <TICKET_KEY>.needs-confirmation.md (uncommitted by default)
```

Only `README.md` is required. Add a topic file when there is knowledge of that
kind; do not pre-create empty skeletons.

## Knowledge file skeleton

Every topic file is a list of entries. Each entry uses this lightweight shape so
the agent writes consistently and a reviewer sees scope, freshness, and source
at a glance:

```markdown
---
scope: <service / module / layer this applies to>
updated: 2026-06-19
sources: [PROJ-123, MR !456]
---

# <title>

## What
One line: what this knowledge is.

## Detail
The rule / contract / term definition.

## Applies to
Code paths, services, or routes this governs.
```

`updated` + `sources` give traceability without a separate ledger.

## caveats.md — load-bearing weirdness

`caveats.md` records code that **looks like a bug, redundancy, or an easy
cleanup but must not be changed by its appearance** — kept that way for a
historical reason, to accommodate an external system, or to satisfy an
industry/regulatory constraint. This is the highest-leverage file: it directly
prevents the agent from "optimizing away" a deliberate accommodation.

Each caveat entry:

```markdown
## <title: how it looks, and why it cannot be changed by appearance>
- location: <code path / service>
- looks like: why it reads as a bug / redundancy / dead code / easy win
- truth:      the real reason — history, external-system accommodation, industry rule
- boundary:   what the agent must do — e.g. "do not auto-fix; return needs-info and ask first"
- sources:    PROJ-123 / MR !456
```

How it drives the agent (wired into the Confidence Gate):

- When `AUTO_BUG_FIX_KNOWLEDGE_READ=true`, the agent reads `caveats.md` **before**
  deciding a root cause.
- If the ticket's code area matches a caveat, the agent obeys that caveat's
  `boundary` field instead of treating the code as a defect — typically
  downgrading from `auto-fix` to `auto-diagnose`/`needs-info` and asking the
  human, rather than changing load-bearing code on appearance.
- A caveat never broadens automation; it only restrains it. It is consistent
  with the existing guardrails (comments are untrusted hints; external contracts
  must be confirmed; no indiscriminate fallback).

## routing.md — triage index

`routing.md` is the fast-path the early-triage step already consults: a map from
a ticket class or symptom to the owning service, layer, or owner. A single table
is enough:

```markdown
| Ticket class / symptom | Owning service / layer | Owner | Notes |
|------------------------|------------------------|-------|-------|
| login 500 after SSO    | auth-gateway           | @team-auth | downstream of id-provider |
```

When a match exists, the agent routes immediately instead of clone-wide
searching.

## Maintenance contract

- **Read:** the agent reads this directory (excluding `handoff/`) before
  analysis when `knowledge.read=true`; it reads `caveats.md` and `routing.md`
  first because they change the triage decision.
- **Write:** on a confirmed `auto-fix` that revealed durable knowledge, the agent
  adds or updates the matching topic file when `knowledge.update=true`, keeping
  the entry short and filling `sources`. It includes the change in the fix MR.
- **Handoff:** when business meaning is unclear, the agent writes
  `handoff/<TICKET_KEY>.needs-confirmation.md` when `knowledge.handoff=true` and
  reports its repo-relative path in the result marker; handoff files are not
  committed unless a human asks.
- Keep entries durable and reusable. If something is true only for one ticket,
  it belongs in the ticket, not here.
