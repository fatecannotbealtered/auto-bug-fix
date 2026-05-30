---
name: Bug report / Bug 报告
about: Report a bug to help us improve / 反馈问题帮助我们改进
labels: bug
---

## Environment / 环境

- `auto-bug-fix version`:
- `go version`:
- OS:
- `agent.command` (redact secrets / 隐去密钥):

## Description / 问题描述

<!-- A clear description of the bug. / 清晰描述问题。 -->

## Steps to reproduce / 复现步骤

1.
2.
3.

## Expected behaviour / 预期行为

<!-- What you expected to happen. / 预期发生什么。 -->

## Actual behaviour / 实际行为

<!-- What actually happened. Include logs or error output. / 实际发生了什么，包含日志或错误输出。 -->

```
paste logs here
```

## Config (redact all tokens and passwords) / 配置（隐去所有 token 和密码）

```json
{
  "agent":  { "command": "..." },
  "poll": {
    "intervalSeconds": 300,
    "stateExpiryDays": 0,
    "filter": {
      "titleContains": "",
      "assignedToMe": true,
      "excludeStatuses": []
    }
  },
  "workspace": {
    "root": "$HOME/.auto-bug-fix/workspaces",
    "cleanup": "keep"
  },
  "knowledge": {
    "dir": ".tcl",
    "read": true,
    "update": true,
    "handoff": true,
    "handoffDir": "handoff"
  },
  "jira":   { "host": "https://..." },
  "gitlab": { "host": "https://..." }
}
```
