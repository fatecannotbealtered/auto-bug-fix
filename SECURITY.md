[English](#security-policy) | [中文](#安全政策)

---

# Security Policy

## Supported Versions

| Version | Supported |
|---------|-----------|
| 1.x     | ✅ Yes    |

## Reporting a Vulnerability

**Do not open a public GitHub issue for security vulnerabilities.**

Please report security issues by emailing: **guosong6886@gmail.com**

Include in your report:
- Description of the vulnerability
- Steps to reproduce
- Potential impact
- Suggested fix (if any)

You will receive an acknowledgement within **48 hours** and a resolution timeline within **7 days**.

## Security Considerations

### Credential handling

- Config credentials (`jira.token`, `gitlab.token`, etc.) are read from `~/.auto-bug-fix/config.json` at runtime and never logged or written elsewhere. Prefer `$ENV_VAR` references so secrets stay in the environment, not the file.
- `agent.command` is spawned without a shell, so the issue key is passed as a discrete argument — no shell interpolation or injection risk from Jira-supplied data.
- The poller does not inject config credentials into the spawned agent. The agent relies on its own CLI authentication (`jira-cli login`, `gitlab-cli auth login`) or whatever the wrapper script sets up.

### Agent execution trust

- The spawned agent runs with the privileges of the user running `auto-bug-fix`, and reads/writes code and Jira/GitLab on their behalf. There is no sandboxing.
- Only configure an `agent.command` you trust, on a machine you trust, with CLI credentials scoped to the minimum permissions required.

### State file

- `~/.auto-bug-fix/state.json` records processed issue keys and statuses. It contains no secrets. It is written with mode `0600` (owner read/write only).

---
---

# 安全政策

## 支持的版本

| 版本 | 是否支持 |
|------|----------|
| 1.x  | ✅ 支持  |

## 上报漏洞

**请勿在 GitHub 公开 Issue 中报告安全漏洞。**

请发送邮件至：**guosong6886@gmail.com**

邮件内容应包含：
- 漏洞描述
- 复现步骤
- 潜在影响
- 建议修复方案（如有）

我们将在 **48 小时内**确认收到，并在 **7 天内**给出处理时间表。

## 安全注意事项

### 凭证处理

- 配置凭证（`jira.token`、`gitlab.token` 等）仅在运行时从 `~/.auto-bug-fix/config.json` 读取，不会写入其他文件或打印到日志。推荐用 `$ENV_VAR` 引用，让密钥留在环境变量而非文件中。
- `agent.command` 以不经过 shell 的方式启动，issue key 作为独立参数传入——不会发生 shell 插值，也不会因 Jira 提供的 key 产生注入风险。
- poller 不会向被 spawn 的 agent 注入配置凭证。agent 依赖自身的 CLI 认证（`jira-cli login`、`gitlab-cli auth login`），或 wrapper 脚本自行准备的凭证。

### Agent 执行信任

- 被 spawn 的 agent 以运行 `auto-bug-fix` 的用户权限执行，代表该用户读写代码与 Jira/GitLab，无沙盒隔离。
- 只配置你信任的 `agent.command`，在你信任的机器上运行，并将 CLI 凭证权限收敛到最小范围。

### 状态文件

- `~/.auto-bug-fix/state.json` 记录已处理的 issue key 和状态，不含任何密钥。文件权限为 `0600`（仅所有者可读写）。
