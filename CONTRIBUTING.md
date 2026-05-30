[English](#contributing) | [中文](#贡献指南)

---

# Contributing

Thank you for your interest in contributing to auto-bug-fix.

## Development Setup

```bash
git clone https://github.com/fatecannotbealtered/auto-bug-fix.git
cd auto-bug-fix
go test ./...      # all tests must pass before submitting a PR
go build ./...     # verify it compiles
```

## Project Structure

```
main.go                     ← entry point → cmd.Execute()
go.mod
cmd/
  cmd.go                    ← CLI: setup / start / stop / status / fix / version
internal/
  config/                   ← config loading + $ENV substitution + validation
  state/                    ← state.json read/write, issue status tracking
  agent/                    ← spawns the configured agent.command (with {issueKey})
  poller/                   ← polling loop: jira-cli search → filter → trigger
  daemon/                   ← background poller start/stop/status + PID file (per-OS)
  installer/                ← installs the per-agent templates during `setup`
agents/                     ← per-agent templates (kiro / cursor / claude-code / codex), embedded in the binary
skills/auto-bug-fix/
  SKILL.md                  ← generic published agent workflow (installed via npx skills add)
scripts/                    ← npm wrapper: install.js (download binary) + run.js (exec it)
.github/workflows/          ← ci.yml (3-OS matrix) + release.yml (GoReleaser → GitHub Releases → npm publish)
.goreleaser.yml             ← cross-platform release build
package.json                ← npm distribution manifest
Makefile                    ← build / test / lint / snapshot
```

## Contribution Guidelines

### Adding support for a new agent

Agents are driven through `agent.command`. To support a new agent, document a working `agent.command` recipe in the README "Agent command" section. Only touch `internal/agent/` if you need to change how the command is spawned or how `{issueKey}` is substituted.

### Writing tests

- Every new behaviour must have a test. Follow TDD: write the failing test first.
- Mock external processes — tests must run offline with no real Jira/GitLab credentials.
- Tests live alongside source files (`*_test.go`).

### Pull request checklist

- [ ] `go test ./...` passes with no failures
- [ ] `go build ./...` succeeds
- [ ] New code has corresponding tests
- [ ] `README` updated if behaviour or config fields changed
- [ ] `CHANGELOG.md` updated under the next release section
- [ ] Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/): `feat:`, `fix:`, `docs:`, `chore:`

### Commit message format

```
feat: add poll.maxConcurrent config field
fix: handle unterminated quote in agent.command
docs: update configuration.md for poll fields
chore: bump Go minimum version to 1.22
```

## Reporting Issues

Use [GitHub Issues](https://github.com/fatecannotbealtered/auto-bug-fix/issues). Bug reports should include:

- `auto-bug-fix version` output
- `go version` output
- Your `agent.command` (redact secrets)
- Steps to reproduce
- Expected vs actual behaviour

For security vulnerabilities, see [SECURITY.md](SECURITY.md).

---
---

# 贡献指南

感谢你对 auto-bug-fix 的贡献意向。

## 开发环境搭建

```bash
git clone https://github.com/fatecannotbealtered/auto-bug-fix.git
cd auto-bug-fix
go test ./...      # 提交 PR 前所有测试必须通过
go build ./...     # 验证编译通过
```

## 贡献规范

### 支持新的 agent

所有 agent 都通过 `agent.command` 驱动。要支持新 agent，在 README 的「Agent 命令」一节中补充一条可用的 `agent.command` 配方即可。仅当需要改变命令的启动方式或 `{issueKey}` 替换逻辑时，才需要改动 `internal/agent/`。

### 编写测试

- 新行为必须有对应测试。遵循 TDD：先写失败测试再实现。
- Mock 所有外部进程，测试必须在离线状态下运行，不依赖真实凭证。
- 测试文件与源码同目录（`*_test.go`）。

### PR 提交检查清单

- [ ] `go test ./...` 无失败
- [ ] `go build ./...` 编译通过
- [ ] 新代码有对应测试
- [ ] 行为或配置字段变更时同步更新 `README`
- [ ] `CHANGELOG.md` 在下一个版本小节下添加条目
- [ ] 提交信息遵循 [Conventional Commits](https://www.conventionalcommits.org/)

## 问题反馈

使用 [GitHub Issues](https://github.com/fatecannotbealtered/auto-bug-fix/issues)。Bug 报告请包含：

- `auto-bug-fix version` 输出
- `go version` 输出
- 你的 `agent.command`（隐去密钥）
- 复现步骤
- 预期行为与实际行为

安全漏洞请参见 [SECURITY.md](SECURITY.md)。
