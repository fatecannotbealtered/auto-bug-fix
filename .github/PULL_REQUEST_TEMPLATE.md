## Summary

<!-- What does this PR change? -->

## Type of Change

- [ ] Bug fix
- [ ] New feature
- [ ] CLI contract / machine output
- [ ] Agent template / Skill
- [ ] Documentation
- [ ] Release / packaging / CI

## Checklist

- [ ] `go test ./...` passes
- [ ] `go vet ./...` passes
- [ ] `node scripts/check-version.js` passes
- [ ] `npm audit --audit-level=high` passes
- [ ] `npm pack --dry-run` passes
- [ ] New or changed public behavior has command-level tests
- [ ] Functional Contract Coverage remains complete for README / Skill / `reference` / `context` / `doctor` / `changelog` / `update`
- [ ] `reference.release_readiness` and the `doctor` `release_readiness` check are still accurate
- [ ] `README.md` and `README_zh.md` are synced when user-facing docs changed
- [ ] `skills/auto-bug-fix/SKILL.md` and `skills/auto-bug-fix/test-prompts.json` are updated when agent behavior changed
- [ ] `.agent` contract impact has been reviewed
- [ ] No secrets, internal hostnames, tokens, or private account data are committed
- [ ] `CHANGELOG.md` is updated under `[Unreleased]`

## Related Issues

<!-- Closes #N -->
