// Package notify renders and sends the auto-bug-fix completion notification.
//
// Notifications are abstracted over a Channel so the tool can support multiple
// messaging back-ends. Currently the only implemented channel is Lark/Feishu
// (see lark.go); adding another (Slack, email, …) means adding one file that
// implements Channel and registers itself — nothing else here changes.
//
// The per-run CONTENT is supplied by the spawned agent as flat Params; each
// Channel owns the fixed presentation/layout so it never drifts across runs,
// agents, or models. Delivery goes through the channel's own external CLI (e.g.
// lark-cli) via an injected Runner; this package holds no credentials.
package notify

import (
	"fmt"
	"sort"
	"strings"
)

// DefaultChannel is the channel used when notify.channel is unset.
const DefaultChannel = "lark"

// Outcome values mirror the agent's AUTO_BUG_FIX_RESULT outcomes. The first three
// are agent-reported. needs-review is an internal fallback auto-bug-fix uses when
// the agent finished without printing a marker: the orchestrator then sends a
// degraded "verify manually" card itself. A channel keys its header/action/buttons
// off these.
const (
	OutcomeAutoFix      = "auto-fix"
	OutcomeAutoDiagnose = "auto-diagnose"
	OutcomeNeedsInfo    = "needs-info"
	OutcomeNeedsReview  = "needs-review"
)

// ValidOutcome reports whether o is a renderable outcome.
func ValidOutcome(o string) bool {
	switch o {
	case OutcomeAutoFix, OutcomeAutoDiagnose, OutcomeNeedsInfo, OutcomeNeedsReview:
		return true
	default:
		return false
	}
}

// Params is the flat, channel-neutral per-run content the agent supplies. Every
// field is plain data (treated as _untrusted text_); the layout around it is
// fixed by the Channel.
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

// Runner executes a channel's delivery CLI (bin + args) and returns its stdout.
// It should return stdout even on a non-zero exit so a JSON envelope can still
// be parsed. Injectable for tests.
type Runner func(bin string, args ...string) ([]byte, error)

// Channel is one messaging back-end. Implementations own the fixed presentation
// and the delivery call; the content fields (Params) are channel-neutral.
type Channel interface {
	// Name is the channel id used in config (notify.channel), e.g. "lark".
	Name() string
	// DoctorBin is the external CLI that doctor health-checks for this channel
	// (e.g. "lark-cli"), or "" when the channel needs no external binary.
	DoctorBin() string
	// Healthy reports whether the channel's delivery CLI is usable, using run to
	// invoke it. Each channel owns its own doctor contract (flags + output shape):
	// delivery CLIs are not all fateforge siblings — e.g. lark-cli rejects --json
	// and emits a flat {ok, checks:[...]} rather than the {data:{authValid}}
	// envelope — so the channel, not doctor, parses its own health. detail is a
	// human-readable status (e.g. "ready", or a fix hint when not ok).
	Healthy(run Runner) (ok bool, detail string)
	// Render turns channel-neutral Params into this channel's send payload.
	Render(p Params) (payload string, err error)
	// Send delivers payload to recipient, using run to invoke the delivery CLI.
	// Returns a channel-specific message reference (e.g. a Lark message_id).
	Send(recipient, payload string, run Runner) (ref string, err error)
}

// Correlation ties an interactive card's callback back to the run that sent it.
// It is embedded in the card so the daemon listener knows which issue an action
// belongs to. It is UNTRUSTED for authorization — only the callback's
// server-delivered operator_id may be used to authorize an action.
type Correlation struct {
	Action string // verb the button performs, e.g. "answer"
	Issue  string // Jira issue key the card is about
}

const actionNamePrefix = "abf"

// EncodeActionName encodes a Correlation into a form submit button's `name`. A
// form submit button carries no `behaviors` (so no action_value), but the callback
// reports the triggered component's name as `action_name` — that is the only field
// that can carry the correlation for a form submit. Issue keys contain no "|".
func EncodeActionName(corr Correlation) string {
	return actionNamePrefix + "|" + corr.Action + "|" + corr.Issue
}

// DecodeActionName parses a submit button `action_name` back into a Correlation.
// ok is false for any name not produced by EncodeActionName.
func DecodeActionName(name string) (corr Correlation, ok bool) {
	parts := strings.SplitN(name, "|", 3)
	if len(parts) != 3 || parts[0] != actionNamePrefix {
		return Correlation{}, false
	}
	return Correlation{Action: parts[1], Issue: parts[2]}, true
}

// InteractiveChannel is an optional capability: a Channel that can also render a
// Card-2.0 interactive clarification card carrying a callback the daemon listener
// consumes. Channels without a callback back-end (e.g. email) simply do not
// implement it, and callers fall back to the one-way Render.
type InteractiveChannel interface {
	Channel
	// RenderClarify renders an interactive needs-info card: the agent's questions
	// plus an input box and a submit button whose callback carries corr.
	RenderClarify(p Params, corr Correlation) (payload string, err error)
	// RenderApproval renders an MR-approval card: approve (callback) / reject (form
	// with optional reason), correlated to corr, with a human-readable diff summary.
	RenderApproval(p Params, diffSummary string, corr Correlation) (payload string, err error)
	// RenderControl renders the poller control card: pause/resume/status buttons and
	// a rerun form. statusSummary is a short poller snapshot.
	RenderControl(statusSummary string) (payload string, err error)
}

var channels = map[string]Channel{}

// register adds a channel implementation; called from each channel's init.
func register(c Channel) { channels[c.Name()] = c }

// Get returns the Channel named name (DefaultChannel when empty), or an error
// listing the supported channels when name is unknown.
func Get(name string) (Channel, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = DefaultChannel
	}
	c, ok := channels[name]
	if !ok {
		return nil, fmt.Errorf("unknown notify channel %q (supported: %s)", name, strings.Join(Names(), ", "))
	}
	return c, nil
}

// Names lists the registered channel ids, sorted.
func Names() []string {
	out := make([]string, 0, len(channels))
	for n := range channels {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
