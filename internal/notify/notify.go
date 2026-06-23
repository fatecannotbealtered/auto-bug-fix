// Package notify renders and sends the auto-bug-fix completion notification card.
//
// The card STYLE is fixed here in Go (header colour, layout, button set, footer)
// so it never drifts across runs, agents, or models — the spawned agent only
// supplies the per-run CONTENT as flat fields via `auto-bug-fix notify`. Sending
// goes through the external `lark-cli` (Lark/Feishu auth lives there; this package
// holds no credentials), exactly like the poller drives jira-cli/gitlab-cli.
package notify

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Outcome values mirror the agent's AUTO_BUG_FIX_RESULT outcomes. Only these
// three are valid; the renderer keys header colour, action line, and button set
// off them.
const (
	OutcomeAutoFix      = "auto-fix"
	OutcomeAutoDiagnose = "auto-diagnose"
	OutcomeNeedsInfo    = "needs-info"
)

// ValidOutcome reports whether o is a renderable outcome.
func ValidOutcome(o string) bool {
	switch o {
	case OutcomeAutoFix, OutcomeAutoDiagnose, OutcomeNeedsInfo:
		return true
	default:
		return false
	}
}

// Params is the flat per-run content the agent supplies. Every field is plain
// data (treated as _untrusted text); the layout around it is fixed.
type Params struct {
	Issue      string // Jira issue key, e.g. PROJ-1234
	Outcome    string // auto-fix | auto-diagnose | needs-info
	Summary    string // ticket title
	RootCause  string // 问题原因
	Solution   string // 解决方案 / 诊断与建议 / 待确认问题 (labelled by outcome)
	MRURL      string // GitLab MR web URL (auto-fix only)
	JiraURL    string // Jira issue URL
	Service    string // footer: service/repo
	Branch     string // footer: work branch
	Duration   string // footer: run duration
	Evidence   string // evidence note text (e.g. "Kibana 日志…")
	TestStatus string // auto-fix test line, e.g. "复现用例 fail→pass，存量全过"
}

type headerStyle struct{ color, title string }

var headerByOutcome = map[string]headerStyle{
	OutcomeAutoFix:      {"green", "✅ 已修复，待你评审"},
	OutcomeAutoDiagnose: {"orange", "🔍 已定位，需人工处理"},
	OutcomeNeedsInfo:    {"blue", "❓ 需要你补充信息"},
}

var actionLineByOutcome = map[string]string{
	OutcomeAutoFix:      "👉 **下一步：评审并合并下方 MR**",
	OutcomeAutoDiagnose: "👉 **下一步：按下方诊断人工处理**（未自动改代码，未开 MR）",
	OutcomeNeedsInfo:    "👉 **下一步：在 Jira 回答下列问题**（机器人不会自行猜测，未开 MR）",
}

var solutionLabelByOutcome = map[string]string{
	OutcomeAutoFix:      "解决方案",
	OutcomeAutoDiagnose: "诊断与建议",
	OutcomeNeedsInfo:    "待确认",
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

// RenderCard returns the fixed-layout Feishu interactive card JSON for these
// params. The structure is identical across runs; only the slotted content and
// the outcome-driven header/action/buttons differ. Returns an error for an
// unknown outcome.
func RenderCard(p Params) (string, error) {
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

// Runner runs lark-cli with the given args and returns its stdout. It should
// return stdout even on a non-zero exit so the JSON envelope can be parsed.
type Runner func(args ...string) ([]byte, error)

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
// group). Returns the created message and chat IDs.
func Send(recipient, cardJSON string, run Runner) (messageID, chatID string, err error) {
	recipient = strings.TrimSpace(recipient)
	if recipient == "" {
		return "", "", fmt.Errorf("no recipient")
	}
	idFlag := "--user-id"
	if strings.HasPrefix(recipient, "oc_") {
		idFlag = "--chat-id"
	}
	out, err := run("im", "+messages-send", idFlag, recipient, "--msg-type", "interactive", "--content", cardJSON, "--as", "bot")
	if err != nil && len(out) == 0 {
		return "", "", err
	}
	var env larkEnvelope
	if jerr := json.Unmarshal(out, &env); jerr != nil {
		return "", "", fmt.Errorf("parse lark-cli output: %w", jerr)
	}
	if !env.OK {
		msg := "lark-cli returned ok:false"
		if env.Error != nil && env.Error.Message != "" {
			msg = env.Error.Message
		}
		return "", "", fmt.Errorf("%s", msg)
	}
	return env.Data.MessageID, env.Data.ChatID, nil
}
