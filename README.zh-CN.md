[English](README.md) | [中文](README.zh-CN.md)

---

<div align="center">

# auto-bug-fix

**平台无关的 Jira DC + GitLab 自治 Bug 修复技能**

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-%3E%3D1.21-blue)](https://golang.org)
[![CI](https://github.com/fatecannotbealtered/auto-bug-fix/actions/workflows/ci.yml/badge.svg)](https://github.com/fatecannotbealtered/auto-bug-fix/actions)

</div>

Jira 新建 Bug ticket → AI agent 读取 ticket，找到 GitLab 仓库，分析代码，按需查询 Kibana 日志，写出定点修复，跑通单测，创建 MR，并将 ticket 更新为 "In Progress"。全程无需人工介入。

兼容**任意 AI agent**：Codex、Kiro、Claude Code、Cursor 或任何你自己配置的工具，不绑定特定 LLM 或 agent 平台。

---

## 工作流

```
定时轮询 Jira（默认每 5 分钟）
      ↓
发现符合 poll.filter 的新 Bug
      ↓
spawn 你配置的 agent.command
      ↓
Agent：读 ticket → 找仓库 → 分析代码
      ↓
      ├─ 根因明确？ → 写修复
      └─ 不明确？   → 查 Kibana → 写修复
      ↓
写/运行单测（循环直到全部通过）
      ↓
创建 GitLab MR  ←── 人工 review & 合并
      ↓
Jira → "In Progress" + MR 链接评论
```

**保留人工**：MR review、合并、关闭 ticket。

---

## 快速开始

```bash
# 1. 安装 CLI（Go 二进制，通过 npm 分发；需要 PATH 中有 curl）
npm install -g @fatecannotbealtered-/auto-bug-fix
# 备选：`go install github.com/fatecannotbealtered/auto-bug-fix@latest`，或从 GitHub Releases 下载。

# 安装 AI agent skill
npx skills add fatecannotbealtered/auto-bug-fix -y -g

# 2. 认证依赖 CLI
jira-cli login --host https://jira.company.com --token <PAT>
gitlab-cli auth login --host https://gitlab.company.com --token <PAT>

# 3. 配置
auto-bug-fix setup --agent codex   # 或：kiro、cursor、claude-code
# 编辑配置：填写 host、token 和 poll.filter

# 4. 校验前置依赖
auto-bug-fix doctor   # 检查 config + 必需 CLI（agent、jira-cli、gitlab-cli、git）是否在 PATH

# 5. 启动轮询（后台运行）
auto-bug-fix start --detach   # 日志在 ~/.auto-bug-fix/poller.log；`auto-bug-fix stop` 停止

# 手动触发
auto-bug-fix fix PROJ-123
```

---

## 前置依赖

| 工具 | 是否必须 | 用途 |
|------|----------|------|
| [jira-cli](https://github.com/fatecannotbealtered/jira-cli) | 必须 | 读取 ticket、流转状态、添加评论 |
| [gitlab-cli](https://github.com/fatecannotbealtered/gitlab-cli) | 必须 | 读代码、建分支、提交、开 MR |
| `git` | 必须 | clone 仓库、建分支、提交、push |
| [kibana-cli](https://github.com/fatecannotbealtered/kibana-cli) | 可选 | 查生产日志辅助定位根因 |
| 某个 AI agent CLI | 必须 | 你填进 `agent.command` 的那个（如 `claude`、`cursor`）——需已安装 `auto-bug-fix` skill |
| Go ≥ 1.21 | 仅构建时 | 使用预编译二进制则不需要 |

---

## Agent 命令

auto-bug-fix 不内置 agent——发现匹配 issue 时，它只 spawn 你在 `agent.command` 中配置的那条命令，由该 agent 按已安装的 `auto-bug-fix` skill 工作流完成修复。

| Agent | `agent.command` |
|-------|-----------------|
| Codex | `codex exec --dangerously-bypass-approvals-and-sandbox --skip-git-repo-check "Fix bug {issueKey} using the auto-bug-fix skill"` |
| Claude Code | `claude --agent auto-bug-fix -p "Fix bug {issueKey}" --permission-mode acceptEdits` |
| Cursor | `cursor-agent --print --force "Fix bug {issueKey} using the auto-bug-fix workflow"` |
| 自定义脚本 | `/path/to/fix.sh`（key 作为 `$1` 追加） |

**占位符替换**：命令含 `{issueKey}` 时，所有出现处都被替换为 Jira key（包括嵌在带引号的 prompt 中）；没有占位符则把 key 作为最后一个参数追加。

**不经过 shell**：命令被分词后以非 shell 方式启动，管道、重定向、环境变量展开都不会被解释——需要这些请封装为脚本。

**凭证**：agent 会调用 `jira-cli` / `gitlab-cli`（及可选 `kibana-cli`），它们必须在运行 poller 的环境中已认证。poller **不会**向被 spawn 的进程注入 token。需要注入凭证或做 shell 层工作时，用 wrapper 脚本：

```bash
#!/usr/bin/env bash
# fix.sh — issue key 作为 $1 传入
export JIRA_TOKEN=...  GITLAB_TOKEN=...
your-agent exec "Fix bug $1 using the auto-bug-fix skill"
```

---

## 配置

配置文件位于 `~/.auto-bug-fix/config.json`（由 `auto-bug-fix setup` 创建）。所有 `$ENV_VAR` 值在加载时从环境变量替换；未解析的占位符会在加载时记录告警，而非静默变为空字符串。

```json
{
  "agent":  {
    "agentType": "codex",
    "command": "codex exec --dangerously-bypass-approvals-and-sandbox --skip-git-repo-check \"Fix bug {issueKey} using the auto-bug-fix skill\""
  },
  "poll": {
    "intervalSeconds": 300,
    "maxConcurrent": 3,
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
  "jira":   { "host": "https://jira.example.com", "token": "$JIRA_TOKEN" },
  "gitlab": { "host": "https://gitlab.example.com", "token": "$GITLAB_TOKEN" },
  "kibana": { "host": "$KIBANA_HOST", "user": "$KIBANA_USER", "password": "$KIBANA_PASSWORD" }
}
```

<details>
<summary><strong>完整字段参考</strong></summary>

| 字段 | 默认值 | 说明 |
|------|--------|------|
| `agent.command` | **必填** | 每个匹配 issue 启动的命令（不经过 shell）。`{issueKey}` 会被替换，无占位符则追加。 |
| `agent.agentType` | — | setup 选择的模板：`kiro` / `cursor` / `claude-code` / `codex`，自定义留空。 |
| `poll.intervalSeconds` | `300` | 轮询间隔（秒），`0` 用默认。 |
| `poll.maxConcurrent` | `3` | 同时运行的 agent 修复数上限，`0` 用默认。 |
| `poll.stateExpiryDays` | `0` | 超过此天数后重新触发 `done` / `failed` / `waiting` 的 issue，`0` = 永不。 |
| `poll.filter.titleContains` | — | 只处理标题包含此字符串的 Bug。 |
| `poll.filter.assignedToMe` | `true` | 只处理分配给当前用户的 Bug。 |
| `poll.filter.excludeStatuses` | `[]` | 额外排除的状态名；Done 状态大类始终被排除。 |
| `workspace.root` | `~/.auto-bug-fix/workspaces` | clone/复用仓库的根目录，通过 `AUTO_BUG_FIX_WORKSPACE_ROOT` 传给 agent。 |
| `workspace.cleanup` | `keep` | `keep`、`on-success`、`always`，通过 `AUTO_BUG_FIX_WORKSPACE_CLEANUP` 传给 agent。 |
| `knowledge.dir` | `.tcl` | 仓库内业务知识目录（仓库相对路径）。 |
| `knowledge.read` / `update` / `handoff` | `true` | agent 是否读取/更新知识、写 handoff 文件。 |
| `knowledge.handoffDir` | `handoff` | `knowledge.dir` 下的 handoff 子目录。 |
| `jira.host` / `jira.token` | **必填** | Jira DC 基础 URL 与个人访问令牌（PAT）。 |
| `gitlab.host` / `gitlab.token` | **必填** | GitLab 基础 URL 与带 `api` 权限的 PAT。 |
| `kibana.host` / `user` / `password` | 可选 | 任一存在则三者必填；否则跳过日志查询（Step 4）。 |

密钥放环境变量、以 `$VAR` 引用（如 `"token": "$JIRA_TOKEN"`）。环境变量是会话级的——用一个先 set 环境变量再 `auto-bug-fix start` 的启动脚本来持久化。

</details>

---

## 运行 poller

```bash
auto-bug-fix doctor           # 预检：config 有效 + 必需 CLI 在 PATH（任一必需项失败则退出码 1）
auto-bug-fix start --detach   # 后台；PID 在 ~/.auto-bug-fix/poller.pid，日志在 ~/.auto-bug-fix/poller.log
auto-bug-fix status           # 运行中的 PID + 日志路径，或 "not running"
auto-bug-fix stop             # 终止 poller 及其在跑的子 agent，并删除 PID 文件

auto-bug-fix start            # 前台（调试用）；Ctrl+C 停止
auto-bug-fix fix PROJ-123     # 一次性修复，绕过轮询与 state
```

`stop` 会终止整棵进程树，不留孤儿子 agent。中途被打断的修复会被重置为可重试，下次 start 时重新拾起。

在 `jira-cli` / `gitlab-cli`（及可选 `kibana-cli`）已认证、且 config 引用的 `$ENV_VAR` 已设置的环境里运行 poller。如果你从 slash/custom command 启动，确认该命令继承了与终端相同的环境，或用先设置环境再调 `auto-bug-fix start` 的 wrapper 脚本。

---

## 状态追踪

已处理的 issue 记录在 `~/.auto-bug-fix/state.json` 中，状态为 `triggered` / `done` / `failed` / `waiting`。每条记录还会保存 agent 命令、尝试次数、时间戳、耗时、退出码、失败原因，以及 agent 打印 `AUTO_BUG_FIX_RESULT` 标记时的 outcome、MR URL 和 handoff 路径。poller 会跳过已在状态文件中的 issue，避免重复触发。

agent 无法自行修复的 issue——outcome 为 `needs-info` 或 `auto-diagnose`——会被记为 `waiting`（球已交到人这边），而非 `done`。poller 不读取 ticket 评论，因此人的回复不会自动重触发它。在 ticket 上答复后，重新运行 `auto-bug-fix fix <KEY>`，或设置 `poll.stateExpiryDays > 0` 让 poller 在指定天数后重试 `waiting`（以及 `done` / `failed`）记录。

---

## 仓库知识库

agent 可以读取和更新仓库内知识库目录，默认是 `.tcl/`。这里用于沉淀长期有效的业务含义，例如产品规则、流程约束、领域术语、责任边界和接口契约。若修复被不清楚的业务含义阻塞，agent 可以在 `.tcl/handoff/` 下写本地 handoff 文件，并在审计标记中返回路径，供人工或外层 coding agent 确认。

---

## 设计与非目标

Go 程序是一个**确定性调度器**：配置、Jira 轮询、幂等去重、进程启动、审计记录。它**不**内置模型、也不实现仓库特定的修复逻辑——修 bug 的流程（根因分析、auto-fix / auto-diagnose / needs-info 置信度门、Jira 评论格式）都在 `auto-bug-fix` skill 和 `agents/` 下的各 agent 模板里。

- **单一 `agent.command` 契约**——Kiro、Cursor、Claude Code、Codex 和自定义 agent 都从同一入口进入。工具特定行为放在 `agents/`，不写进 Go 适配代码。
- **凭证留在 poller 之外**——agent 依赖自身已认证的 CLI，poller 从不注入 token。
- **保守自动化**——只有根因明确、可本地验证时，agent 才写代码、开 MR；否则只诊断或在 Jira 提问。

**非目标（1.0.0）**：Go 程序内不做 GitLab 编辑或 Kibana 调用，不自动 merge MR 或关闭 ticket，不引入 per-agent 适配器框架。

---

## 文档

| 文档 | 说明 |
|------|------|
| [SKILL.md](skills/auto-bug-fix/SKILL.md) | AI agent 工作流指令 |
| [CONTRIBUTING.md](CONTRIBUTING.md) | 贡献指南 |
| [CHANGELOG.md](CHANGELOG.md) | 版本记录 |
| [SECURITY.md](SECURITY.md) | 安全漏洞上报 |

---

## 贡献

欢迎贡献。提交 PR 前请阅读 [CONTRIBUTING.md](CONTRIBUTING.md)。

---

## 许可证

[MIT](LICENSE) © fatecannotbealtered
