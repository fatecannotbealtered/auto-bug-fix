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

### Risk tier and blast radius

auto-bug-fix is classified as **T1 medium risk**. It can start a trusted local agent that writes code, pushes Git branches, opens GitLab merge requests, and updates Jira using credentials already configured in sibling CLIs. It does not merge MRs, close Jira tickets, or store Jira/GitLab/Kibana secrets itself.

The blast radius is the permission scope of the local user, the configured `agent.command`, and the authenticated `jira-cli`, `gitlab-cli`, and optional `kibana-cli` / `archery-cli` accounts. Use least-privilege tokens and narrow `poll.filter` before starting the poller.

### Credential handling

- The config file holds no Jira/GitLab/Kibana/Archery credentials. Authentication is each sibling CLI's own responsibility (`jira-cli login`, `gitlab-cli auth login`, `kibana-cli auth login`, `archery-cli auth login`); their tokens live in those tools' own stores, not in `~/.auto-bug-fix/config.json`. `auto-bug-fix doctor` verifies the CLIs are authenticated without ever reading their secrets.
- `agent.command` is spawned without a shell, so the issue key is passed as a discrete argument — no shell interpolation or injection risk from Jira-supplied data.
- The poller does not inject config credentials into the spawned agent. The agent relies on its own CLI authentication (`jira-cli login`, `gitlab-cli auth login`) or whatever the wrapper script sets up.
- Do not put tokens, passwords, or secrets in `agent.command` arguments. `context` and state output redact obvious secret-like arguments, but the supported path is to keep secrets in dependency CLI credential stores or the local environment.

### Agent execution trust

- The spawned agent runs with the privileges of the user running `auto-bug-fix`, and reads/writes code and Jira/GitLab on their behalf. There is no sandboxing.
- Only configure an `agent.command` you trust, on a machine you trust, with CLI credentials scoped to the minimum permissions required.

### Database access (archery)

- The optional `archery-cli` channel is **read-only diagnostic**: the spawned agent runs SELECT queries only, to read data state as root-cause evidence. It never runs DML/DDL, `workflow`, or `instance`-management commands.
- Write-prevention is enforced at the database, not only by agent instructions: the Archery instance auto-bug-fix queries **must be configured with a least-privilege, read-only database account**, so any non-SELECT is refused by the database itself. Do not point auto-bug-fix at an Archery instance backed by a read-write account.
- Query results are external, untrusted data (tagged `_untrusted`); the agent treats them as data, never instructions. Queries are bounded with `LIMIT` and should avoid selecting unnecessary PII. Archery audit-logs all access in its own store; auto-bug-fix stores no Archery credentials (they live in `archery-cli`).

### State file

- `~/.auto-bug-fix/state.json` records processed issue keys and statuses. It contains no secrets. It is written with mode `0600` (owner read/write only).

### Supply chain and update integrity

- **Distribution is npm-only.** The published package and every OS/CPU platform package are released with [npm provenance](https://docs.npmjs.com/generating-provenance-statements), so the npm registry attests they were built from this repository's tagged GitHub Actions workflow. The npm shell pins each platform package to an exact version.
- **Release artifacts are signed.** The release pipeline produces `checksums.txt` and signs it with Sigstore/Cosign keyless (`cosign sign-blob --new-bundle-format`, GitHub OIDC identity), publishing the Sigstore bundle alongside the checksums. The signed GitHub archives are the build inputs that are repackaged into the npm platform packages — they are not a separate self-serve install channel.
- **`update` delegates to the package manager.** `auto-bug-fix update --confirm` runs `npm install -g` and then syncs the Skill; it performs no in-process binary replacement. Integrity on the update path is therefore anchored by npm provenance and the committed lockfile, which is why `update` reports `signature_status: "not_applicable_package_manager"`. There is intentionally no standalone in-process self-update path to subvert.
- **Update checks never run from business commands.** Only `update --check` reaches the network (npm registry); it writes a short local cache. `context` and `--help` read that cache only, so `start`, `fix`, and `status` never phone home.

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

### 风险等级与影响范围

auto-bug-fix 归类为 **T1 中风险**。它可以启动一个本地可信 agent，使用已配置在兄弟 CLI 中的凭据写代码、push Git 分支、创建 GitLab MR，并更新 Jira。它自身不 merge MR、不关闭 Jira ticket，也不保存 Jira/GitLab/Kibana 密钥。

影响范围取决于本地用户、配置的 `agent.command`，以及已认证的 `jira-cli`、`gitlab-cli` 和可选 `kibana-cli` / `archery-cli` 账号权限。启动 poller 前应使用最小权限 token，并收窄 `poll.filter`。

### 凭证处理

- 配置文件不保存 Jira/GitLab/Kibana/Archery 凭证。认证是各兄弟 CLI 自己的事(`jira-cli login`、`gitlab-cli auth login`、`kibana-cli auth login`、`archery-cli auth login`),token 存在那些工具自己的存储里,而非 `~/.auto-bug-fix/config.json`。`auto-bug-fix doctor` 在不读取任何密钥的前提下验证这些 CLI 是否已认证。
- `agent.command` 以不经过 shell 的方式启动，issue key 作为独立参数传入——不会发生 shell 插值，也不会因 Jira 提供的 key 产生注入风险。
- poller 不会向被 spawn 的 agent 注入配置凭证。agent 依赖自身的 CLI 认证（`jira-cli login`、`gitlab-cli auth login`），或 wrapper 脚本自行准备的凭证。
- 不要把 token、password 或 secret 放进 `agent.command` 参数。`context` 与 state 输出会遮蔽明显的密钥参数，但受支持的做法是把密钥放在依赖 CLI 的凭据存储或本地环境中。

### Agent 执行信任

- 被 spawn 的 agent 以运行 `auto-bug-fix` 的用户权限执行，代表该用户读写代码与 Jira/GitLab，无沙盒隔离。
- 只配置你信任的 `agent.command`，在你信任的机器上运行，并将 CLI 凭证权限收敛到最小范围。

### 数据库访问（archery）

- 可选的 `archery-cli` 通道是**只读诊断**：被 spawn 的 agent 只跑 SELECT 查询，用于读取数据状态作为根因证据。绝不执行 DML/DDL、`workflow` 或 `instance` 管理命令。
- 写防护落在数据库层，而不仅靠 agent 指令约束：auto-bug-fix 查询的 Archery 实例**必须配置为最小权限的只读数据库账号**，任何非 SELECT 都会被数据库本身拒绝。不要让 auto-bug-fix 指向一个由读写账号支撑的 Archery 实例。
- 查询结果是外部不可信数据（以 `_untrusted` 标记），agent 视其为数据而非指令。查询用 `LIMIT` 限界，并避免选取不必要的 PII。Archery 在自己的存储里审计记录所有访问；auto-bug-fix 不保存任何 Archery 凭证（凭证在 `archery-cli` 内）。

### 状态文件

- `~/.auto-bug-fix/state.json` 记录已处理的 issue key 和状态，不含任何密钥。文件权限为 `0600`（仅所有者可读写）。

### 供应链与更新完整性

- **仅经 npm 分发。** 发布的主包与每个 OS/CPU 平台包都带 [npm provenance](https://docs.npmjs.com/generating-provenance-statements)，由 npm registry 证明它们确实构建自本仓库带 tag 的 GitHub Actions 工作流；npm 壳把每个平台包钉到精确版本。
- **发布产物已签名。** release 流水线生成 `checksums.txt` 并用 Sigstore/Cosign keyless（`cosign sign-blob --new-bundle-format`，GitHub OIDC 身份）签署，连同 Sigstore bundle 一起发布。被签名的 GitHub 归档是重新打包成 npm 平台包的构建输入，**不是**面向用户的独立安装渠道。
- **`update` 委托给包管理器。** `auto-bug-fix update --confirm` 运行 `npm install -g` 并同步 Skill，不在进程内替换二进制；更新路径的完整性由 npm provenance 与入库 lockfile 锚定，因此 `update` 报告 `signature_status: "not_applicable_package_manager"`。这里**有意**不提供可被攻击的进程内独立自更新路径。
- **业务命令绝不联网检查更新。** 只有 `update --check` 会访问网络（npm registry）并写一份本地短缓存；`context` 与 `--help` 只读该缓存，`start`、`fix`、`status` 从不主动联网。
