// Lark/Feishu channel: a fixed-layout interactive card delivered via lark-cli.
//
// This is the only Channel implemented today. The card STYLE (header colour,
// layout, button set, footer) is fixed here so it never drifts across runs,
// agents, or models — the agent supplies only the per-run Params. Sending goes
// through the external lark-cli (Lark/Feishu auth lives there; no credentials
// here), exactly like the poller drives jira-cli/gitlab-cli.
package notify

import (
	"encoding/json"
	"fmt"
	"strings"
)

func init() { register(larkChannel{}) }

type larkChannel struct{}

func (larkChannel) Name() string      { return "lark" }
func (larkChannel) DoctorBin() string { return "lark-cli" }

// Healthy runs `lark-cli doctor` (lark-cli is not a fateforge sibling: it rejects
// --json and emits a flat {ok, checks:[{name,status}], _notice}). It trusts the
// authoritative top-level `ok` that lark-cli computes for itself; the _notice
// update hint is non-fatal and ignored. The runner returns stdout even on a
// non-zero exit, so an unhealthy CLI still yields parseable {ok:false}.
func (larkChannel) Healthy(run Runner) (bool, string) {
	out, _ := run("lark-cli", "doctor")
	var resp struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return false, "not usable; run `lark-cli doctor`"
	}
	if !resp.OK {
		return false, "not authenticated; run `lark-cli auth login`"
	}
	return true, "ready"
}

type headerStyle struct{ color, title string }

var headerByOutcome = map[string]headerStyle{
	OutcomeAutoFix:      {"green", "✅ 已修复，待你评审"},
	OutcomeAutoDiagnose: {"orange", "🔍 已定位，需人工处理"},
	OutcomeNeedsInfo:    {"blue", "❓ 需要你补充信息"},
	OutcomeNeedsReview:  {"grey", "⚠️ 机器人已运行，请人工核对"},
}

var actionLineByOutcome = map[string]string{
	OutcomeAutoFix:      "👉 **下一步：评审并合并下方 MR**",
	OutcomeAutoDiagnose: "👉 **下一步：按下方诊断人工处理**（未自动改代码，未开 MR）",
	OutcomeNeedsInfo:    "👉 **下一步：在 Jira 回答下列问题**（机器人不会自行猜测，未开 MR）",
	OutcomeNeedsReview:  "👉 **下一步：到 Jira / GitLab 核对本次运行结果**（机器人未回传结构化结果）",
}

var solutionLabelByOutcome = map[string]string{
	OutcomeAutoFix:      "解决方案",
	OutcomeAutoDiagnose: "诊断与建议",
	OutcomeNeedsInfo:    "待确认",
	OutcomeNeedsReview:  "说明",
}

func larkMD(content string) map[string]any {
	return map[string]any{"tag": "lark_md", "content": content}
}

func div(content string) map[string]any {
	return map[string]any{"tag": "div", "text": larkMD(content)}
}

func note(content string) map[string]any {
	return map[string]any{"tag": "note", "elements": []any{larkMD(content)}}
}

func button(text, style, url string) map[string]any {
	return map[string]any{"tag": "button", "type": style, "url": url, "text": map[string]any{"tag": "plain_text", "content": text}}
}

func field(short bool, content string) map[string]any {
	return map[string]any{"is_short": short, "text": larkMD(content)}
}

// Render returns the fixed-layout Feishu interactive card JSON for these params.
// The structure is identical across runs; only the slotted content and the
// outcome-driven header/action/buttons differ. Errors on an unknown outcome.
func (larkChannel) Render(p Params) (string, error) {
	if !ValidOutcome(p.Outcome) {
		return "", fmt.Errorf("unknown outcome %q (want auto-fix, auto-diagnose, or needs-info)", p.Outcome)
	}
	h := headerByOutcome[p.Outcome]

	elements := []any{}

	// 1. What — issue + summary, bold first line.
	title := "**" + p.Issue
	if strings.TrimSpace(p.Summary) != "" {
		title += " · " + p.Summary
	}
	title += "**"
	elements = append(elements, div(title))

	// 2. The single most important line: what the recipient must do next.
	elements = append(elements, div(actionLineByOutcome[p.Outcome]))

	// 3. Trust signal (auto-fix only).
	if p.Outcome == OutcomeAutoFix && strings.TrimSpace(p.TestStatus) != "" {
		elements = append(elements, div("🧪 **测试** · "+p.TestStatus))
	}

	// 4. Jump-out buttons (link only — v1 is one-way, no callbacks). MR is the
	// primary CTA on auto-fix; otherwise the Jira issue is the primary CTA.
	var actions []any
	if p.Outcome == OutcomeAutoFix && strings.TrimSpace(p.MRURL) != "" {
		actions = append(actions, button("✅ 评审 MR", "primary", p.MRURL))
	}
	if strings.TrimSpace(p.JiraURL) != "" {
		style, label := "default", "Jira 工单"
		switch p.Outcome {
		case OutcomeNeedsInfo:
			style, label = "primary", "去 Jira 回复"
		case OutcomeAutoDiagnose:
			style, label = "primary", "查看 Jira"
		}
		actions = append(actions, button(label, style, p.JiraURL))
	}
	if len(actions) > 0 {
		elements = append(elements, map[string]any{"tag": "action", "actions": actions})
	}

	// 5. Details (secondary): root cause, solution/diagnosis/questions, evidence.
	if strings.TrimSpace(p.RootCause) != "" || strings.TrimSpace(p.Solution) != "" || strings.TrimSpace(p.Evidence) != "" {
		elements = append(elements, map[string]any{"tag": "hr"})
		if strings.TrimSpace(p.RootCause) != "" {
			elements = append(elements, div("**问题原因**　"+p.RootCause))
		}
		if strings.TrimSpace(p.Solution) != "" {
			elements = append(elements, div("**"+solutionLabelByOutcome[p.Outcome]+"**　"+p.Solution))
		}
		if strings.TrimSpace(p.Evidence) != "" {
			elements = append(elements, note("🔎 证据："+p.Evidence))
		}
	}

	// 6. Footer meta — spaced into a field grid, not crammed into one line.
	var fields []any
	if strings.TrimSpace(p.Service) != "" {
		fields = append(fields, field(true, "**服务**\n"+p.Service))
	}
	if strings.TrimSpace(p.Duration) != "" {
		fields = append(fields, field(true, "**耗时**\n"+p.Duration))
	}
	if strings.TrimSpace(p.Branch) != "" {
		fields = append(fields, field(false, "**分支**\n"+p.Branch))
	}
	if len(fields) > 0 {
		elements = append(elements, map[string]any{"tag": "hr"})
		elements = append(elements, map[string]any{"tag": "div", "fields": fields})
	}
	elements = append(elements, note("ℹ️ review、merge、关单仍由人工完成"))
	elements = append(elements, note("🤖 由 auto-bug-fix 自动发送"))

	card := map[string]any{
		"config": map[string]any{"wide_screen_mode": true},
		"header": map[string]any{
			"template": h.color,
			"title":    map[string]any{"tag": "plain_text", "content": h.title},
		},
		"elements": elements,
	}
	b, err := json.Marshal(card)
	if err != nil {
		return "", fmt.Errorf("marshal card: %w", err)
	}
	return string(b), nil
}

// ── Card 2.0 interactive clarification card ──────────────────────────────────
//
// The completion card above is Card 1.0 (jump-out URL buttons only). The needs-info
// clarification card must be Card 2.0 to carry a callback: a `form` with an input
// box + a submit button whose `name` encodes the correlation (a form submit button
// carries no behaviors, so the callback reports it via `action_name`). The daemon
// listener consumes the resulting card.action.trigger and re-triggers the fix.

func plainText(content string) map[string]any {
	return map[string]any{"tag": "plain_text", "content": content}
}

// note2 is a Card 2.0-safe secondary/footnote line. The Card 1.0 `note` element is
// NOT a Card 2.0 component (the 2.0 schema has no `note` tag), so a 2.0 card that
// used note() could fail to render; a `div` with lark_md renders in 2.0.
func note2(content string) map[string]any { return div(content) }

// linkButton is a standalone Card 2.0 button that opens a URL (no callback).
func linkButton(text, style, url string) map[string]any {
	return map[string]any{
		"tag":       "button",
		"text":      plainText(text),
		"type":      style,
		"behaviors": []any{map[string]any{"type": "open_url", "default_url": url}},
	}
}

// callbackButton is a standalone Card 2.0 button whose click delivers value back
// as action_value on the card.action.trigger event.
func callbackButton(text, style string, value map[string]any) map[string]any {
	return map[string]any{
		"tag":       "button",
		"text":      plainText(text),
		"type":      style,
		"width":     "fill",
		"behaviors": []any{map[string]any{"type": "callback", "value": value}},
	}
}

// card2 assembles a Card 2.0 root with a coloured header and body elements.
func card2(template, title string, elements []any) map[string]any {
	return map[string]any{
		"schema": "2.0",
		"config": map[string]any{"update_multi": true, "width_mode": "default"},
		"header": map[string]any{"template": template, "title": plainText(title)},
		"body":   map[string]any{"direction": "vertical", "elements": elements},
	}
}

// RenderClarify builds the Card 2.0 needs-info clarification card. The questions
// come from the same fields the agent already passes to `notify` (Solution, with
// RootCause as context); the input box collects the human answer and the submit
// button's name carries corr so the listener can correlate the callback.
func (larkChannel) RenderClarify(p Params, corr Correlation) (string, error) {
	if p.Outcome != OutcomeNeedsInfo {
		return "", fmt.Errorf("clarify card is only for outcome needs-info, got %q", p.Outcome)
	}
	if strings.TrimSpace(corr.Issue) == "" {
		return "", fmt.Errorf("clarify card needs a correlation issue key")
	}

	elements := []any{}

	title := "**" + p.Issue
	if strings.TrimSpace(p.Summary) != "" {
		title += " · " + p.Summary
	}
	title += "**"
	elements = append(elements, div(title))
	elements = append(elements, div("🤖 **机器人需要你补充信息才能继续修复**，请在下方作答后提交，机器人会自动带着你的回答重跑。"))

	if q := strings.TrimSpace(p.Solution); q != "" {
		elements = append(elements, div("**待确认**　"+q))
	}
	if rc := strings.TrimSpace(p.RootCause); rc != "" {
		elements = append(elements, note2("背景：不清楚之处 — "+rc))
	}
	if strings.TrimSpace(p.JiraURL) != "" {
		elements = append(elements, linkButton("查看 Jira 工单", "default", p.JiraURL))
	}

	// The form: a multiline input for the answer + a submit button whose name
	// encodes the correlation (read back as action_name on the callback).
	form := map[string]any{
		"tag":  "form",
		"name": "abf_clarify_form",
		"elements": []any{
			map[string]any{
				"tag":         "input",
				"name":        "answer",
				"required":    true,
				"input_type":  "multiline_text",
				"label":       plainText("你的补充说明"),
				"placeholder": plainText("在此填写答案，例如：环境为预发、只在 iOS 复现、期望行为是……"),
			},
			map[string]any{
				"tag":              "button",
				"text":             plainText("✅ 提交并重跑"),
				"type":             "primary_filled",
				"width":            "fill",
				"form_action_type": "submit",
				"name":             EncodeActionName(corr),
			},
		},
	}
	elements = append(elements, form)
	elements = append(elements, note2("ℹ️ 只有被授权的成员提交才会生效；提交后机器人自动重跑本工单"))
	elements = append(elements, note2("🤖 由 auto-bug-fix 自动发送"))

	b, err := json.Marshal(card2("blue", "❓ 需要你补充信息", elements))
	if err != nil {
		return "", fmt.Errorf("marshal clarify card: %w", err)
	}
	return string(b), nil
}

// RenderApproval builds the Card 2.0 MR-approval card: the fix passed the AI
// verifier and is held pending a human decision. Approve is a callback button; the
// reject form collects an optional reason. Correlation goes in the approve button's
// value (action_value) and in the reject submit button's name (action_name).
func (larkChannel) RenderApproval(p Params, diffSummary string, corr Correlation) (string, error) {
	if strings.TrimSpace(corr.Issue) == "" {
		return "", fmt.Errorf("approval card needs a correlation issue key")
	}
	elements := []any{}

	title := "**" + p.Issue
	if strings.TrimSpace(p.Summary) != "" {
		title += " · " + p.Summary
	}
	title += "**"
	elements = append(elements, div(title))
	elements = append(elements, div("✅ **已通过 AI 独立复核**，待你确认后开 MR。**当前尚未开 MR**，拒绝不会产生任何写操作。"))
	if rc := strings.TrimSpace(p.RootCause); rc != "" {
		elements = append(elements, div("**问题原因**　"+rc))
	}
	if strings.TrimSpace(p.Branch) != "" {
		elements = append(elements, note2("分支 "+p.Branch+" → "+p.Service))
	}
	if ds := strings.TrimSpace(diffSummary); ds != "" {
		elements = append(elements, div("**改动**\n```\n"+ds+"\n```"))
	}
	if strings.TrimSpace(p.JiraURL) != "" {
		elements = append(elements, linkButton("查看 Jira 工单", "default", p.JiraURL))
	}

	// Approve: one-click callback carrying the correlation as action_value.
	elements = append(elements, callbackButton("✅ 批准合并（开 MR）", "primary_filled",
		map[string]any{"action": "approve", "issue": corr.Issue}))

	// Reject: a form with an optional reason; correlation in the submit button name.
	rejectForm := map[string]any{
		"tag":  "form",
		"name": "abf_reject_form",
		"elements": []any{
			map[string]any{
				"tag": "input", "name": "reason", "input_type": "multiline_text",
				"label":       plainText("拒绝原因（可选）"),
				"placeholder": plainText("为什么不合并，例如：方案不对、需要人工处理……"),
			},
			map[string]any{
				"tag": "button", "text": plainText("❌ 拒绝"), "type": "danger",
				"width": "fill", "form_action_type": "submit",
				"name": EncodeActionName(Correlation{Action: "reject", Issue: corr.Issue}),
			},
		},
	}
	elements = append(elements, rejectForm)
	elements = append(elements, note2("ℹ️ 只有被授权的成员操作才会生效"))
	elements = append(elements, note2("🤖 由 auto-bug-fix 自动发送"))

	b, err := json.Marshal(card2("orange", "🔒 待你批准合并 MR", elements))
	if err != nil {
		return "", fmt.Errorf("marshal approval card: %w", err)
	}
	return string(b), nil
}

// RenderControl builds the Card 2.0 poller control card: pause/resume/refresh are
// issue-less callback buttons; the rerun form takes a typed issue key. statusSummary
// is a short human-readable snapshot of the poller.
func (larkChannel) RenderControl(statusSummary string) (string, error) {
	elements := []any{}
	if s := strings.TrimSpace(statusSummary); s != "" {
		elements = append(elements, div(s))
	}
	elements = append(elements,
		callbackButton("⏸️ 暂停轮询", "default", map[string]any{"action": "pause"}),
		callbackButton("▶️ 恢复轮询", "default", map[string]any{"action": "resume"}),
		callbackButton("🔄 刷新状态", "primary", map[string]any{"action": "status"}),
	)
	rerunForm := map[string]any{
		"tag":  "form",
		"name": "abf_rerun_form",
		"elements": []any{
			map[string]any{
				"tag": "input", "name": "issueKey", "required": true,
				"label":       plainText("重跑工单"),
				"placeholder": plainText("输入 Jira 工单号，例如 PROJ-1234"),
			},
			map[string]any{
				"tag": "button", "text": plainText("↻ 立即重跑"), "type": "primary",
				"width": "fill", "form_action_type": "submit",
				"name": EncodeActionName(Correlation{Action: "rerun"}),
			},
		},
	}
	elements = append(elements, rerunForm)
	elements = append(elements, note2("ℹ️ 只有被授权的成员操作才会生效"))
	elements = append(elements, note2("🤖 auto-bug-fix 控制台"))

	b, err := json.Marshal(card2("wathet", "🕹️ auto-bug-fix 控制台", elements))
	if err != nil {
		return "", fmt.Errorf("marshal control card: %w", err)
	}
	return string(b), nil
}

type larkEnvelope struct {
	OK   bool `json:"ok"`
	Data struct {
		MessageID string `json:"message_id"`
		ChatID    string `json:"chat_id"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Send delivers cardJSON to recipient via lark-cli as an interactive card.
// recipient is an open_id (ou_…, sent as a DM) or a chat_id (oc_…, sent to a
// group). Returns the created message id.
func (larkChannel) Send(recipient, cardJSON string, run Runner) (string, error) {
	recipient = strings.TrimSpace(recipient)
	if recipient == "" {
		return "", fmt.Errorf("no recipient")
	}
	idFlag := "--user-id"
	if strings.HasPrefix(recipient, "oc_") {
		idFlag = "--chat-id"
	}
	out, err := run("lark-cli", "im", "+messages-send", idFlag, recipient, "--msg-type", "interactive", "--content", cardJSON, "--as", "bot")
	if err != nil && len(out) == 0 {
		return "", err
	}
	var env larkEnvelope
	if jerr := json.Unmarshal(out, &env); jerr != nil {
		return "", fmt.Errorf("parse lark-cli output: %w", jerr)
	}
	if !env.OK {
		msg := "lark-cli returned ok:false"
		if env.Error != nil && env.Error.Message != "" {
			msg = env.Error.Message
		}
		return "", fmt.Errorf("%s", msg)
	}
	return env.Data.MessageID, nil
}
