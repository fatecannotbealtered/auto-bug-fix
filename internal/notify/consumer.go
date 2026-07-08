// Inbound side of the Feishu interaction: a long-lived consumer of card callback
// events (card.action.trigger) delivered as NDJSON by `lark-cli event consume`.
// This is deliberately NOT part of the Channel interface (which is synchronous and
// per-send); a streaming consumer is a distinct shape. Credentials stay in lark-cli
// (same principle as Send) — this package only parses and dispatches.
package notify

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log"
	"os/exec"
	"strings"
	"time"
)

// EventKeyCardAction is the lark event key for interactive card callbacks.
const EventKeyCardAction = "card.action.trigger"

// CallbackEvent is one decoded card.action.trigger event. Only OperatorID is
// trustworthy for authorization: it is delivered by the server over the bot's
// WebSocket, not carried in the card. Action/Issue/Answer are derived from
// card-carried values and must be treated as untrusted data.
type CallbackEvent struct {
	Type       string
	EventID    string // safe for dedup
	OperatorID string // open_id of the clicker — the ONLY field to authorize on
	MessageID  string // om_… — the card message, for delayed update
	ChatID     string
	Token      string // delayed-update token (30 min, max 2 uses)
	ActionTag  string
	ActionName string

	// Derived correlation / payload:
	Action string            // e.g. "answer"/"approve"/"reject"/"pause" (submit-button name or callback value)
	Issue  string            // Jira issue key the card is about ("" for issue-less control actions)
	Answer string            // clarify form convenience: Fields["answer"]
	Fields map[string]string // all form_value string fields (answer, reason, issueKey, …)
}

// Consumer streams inbound card callbacks and invokes handle for each decodable,
// correlatable event until ctx is cancelled. handle errors are logged, not fatal.
type Consumer interface {
	Consume(ctx context.Context, handle func(CallbackEvent) error) error
}

// LarkConsumer consumes card callbacks via `lark-cli event consume`.
type LarkConsumer struct{}

// rawCallback mirrors the flat NDJSON shape of a card.action.trigger event.
type rawCallback struct {
	Type        string `json:"type"`
	EventID     string `json:"event_id"`
	OperatorID  string `json:"operator_id"`
	MessageID   string `json:"message_id"`
	ChatID      string `json:"chat_id"`
	Token       string `json:"token"`
	ActionTag   string `json:"action_tag"`
	ActionValue string `json:"action_value"`
	ActionName  string `json:"action_name"`
	FormValue   string `json:"form_value"`
}

// decodeCallback parses one NDJSON line into a CallbackEvent. ok is false when the
// line is not JSON, or is not an actionable+correlatable event (no operator, or no
// action/issue we can route). A form submit correlates via action_name (the submit
// button carries no behaviors); a standalone button correlates via action_value.
func decodeCallback(line []byte) (CallbackEvent, bool) {
	var r rawCallback
	if err := json.Unmarshal(line, &r); err != nil {
		return CallbackEvent{}, false
	}
	ev := CallbackEvent{
		Type: r.Type, EventID: r.EventID, OperatorID: r.OperatorID,
		MessageID: r.MessageID, ChatID: r.ChatID, Token: r.Token,
		ActionTag: r.ActionTag, ActionName: r.ActionName,
	}
	if strings.TrimSpace(r.FormValue) != "" {
		// Form submit: correlation is the submit button name; values are in form_value.
		if corr, ok := DecodeActionName(r.ActionName); ok {
			ev.Action, ev.Issue = corr.Action, corr.Issue
		}
		var fv map[string]any
		if json.Unmarshal([]byte(r.FormValue), &fv) == nil {
			ev.Fields = make(map[string]string, len(fv))
			for k, val := range fv {
				if s, ok := val.(string); ok {
					ev.Fields[k] = s
				}
			}
			ev.Answer = ev.Fields["answer"]
		}
	} else if strings.TrimSpace(r.ActionValue) != "" {
		// Standalone callback button: correlation is the developer-defined value.
		var v struct {
			Action string `json:"action"`
			Issue  string `json:"issue"`
		}
		if json.Unmarshal([]byte(r.ActionValue), &v) == nil {
			ev.Action, ev.Issue = v.Action, v.Issue
		}
	}
	// Issue may legitimately be empty for issue-less control actions (pause/resume/
	// status); those carry their target, if any, in Fields. Require only an operator
	// and a decodable action.
	if ev.OperatorID == "" || ev.Action == "" {
		return CallbackEvent{}, false
	}
	return ev, true
}

// scanCallbacks reads NDJSON lines from r and dispatches each decodable event to
// handle. It is the pure, testable core of Consume (feed it any io.Reader).
func scanCallbacks(r io.Reader, handle func(CallbackEvent) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		ev, ok := decodeCallback(line)
		if !ok {
			continue
		}
		if err := handle(ev); err != nil {
			log.Printf("[auto-bug-fix] interact: handling %s for %s failed: %v", ev.Action, ev.Issue, err)
		}
	}
	return sc.Err()
}

// Consume runs `lark-cli event consume card.action.trigger --as bot` and streams
// its NDJSON stdout through scanCallbacks until ctx is cancelled. On cancel it
// closes the child's stdin (the documented graceful-shutdown signal — never
// kill -9, which leaks the server-side subscription); WaitDelay force-terminates
// only if the child ignores EOF.
func (LarkConsumer) Consume(ctx context.Context, handle func(CallbackEvent) error) error {
	cmd := exec.CommandContext(ctx, "lark-cli", "event", "consume", EventKeyCardAction, "--as", "bot") //nolint:gosec
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	// Graceful shutdown: close stdin on cancel so lark-cli exits and unsubscribes.
	cmd.Cancel = func() error { return stdin.Close() }
	cmd.WaitDelay = 10 * time.Second

	if err := cmd.Start(); err != nil {
		return err
	}
	// Drain stderr in a detached goroutine; surface the ready marker so startup is
	// visible. It is NOT joined before cmd.Wait(): joining could deadlock shutdown if
	// a grandchild inherits and holds stderr's write end (the scanner would never
	// reach EOF). cmd.Wait() + WaitDelay force-close the parent pipe ends, which EOFs
	// this goroutine so it self-terminates. stderr is an *os.File (fd-locked), so the
	// concurrent read is memory-safe.
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			if strings.Contains(sc.Text(), "[event] ready") {
				log.Printf("[auto-bug-fix] interact: card callback listener ready")
			}
		}
	}()

	scanErr := scanCallbacks(stdout, handle)
	waitErr := cmd.Wait()
	// A cancel is a clean shutdown, not a failure.
	if ctx.Err() != nil {
		return nil
	}
	if scanErr != nil {
		return scanErr
	}
	return waitErr
}
