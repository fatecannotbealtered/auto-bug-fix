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

func (larkChannel) Name() string         { return "lark" }
func (larkChannel) DoctorBin() string    { return "lark-cli" }
func (larkChannel) DoctorArgs() []string { return []string{"doctor"} }

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
