package notify

import (
	"strings"
	"testing"
)

func TestDecodeCallback_FormSubmit(t *testing.T) {
	name := EncodeActionName(Correlation{Action: "answer", Issue: "PROJ-1234"})
	line := `{"type":"card.action.trigger","event_id":"e1","operator_id":"ou_alice","message_id":"om_1","token":"tk","action_tag":"button","action_name":"` + name + `","form_value":"{\"answer\":\"环境是预发\"}"}`
	ev, ok := decodeCallback([]byte(line))
	if !ok {
		t.Fatal("a form submit with correlation + answer should decode")
	}
	if ev.OperatorID != "ou_alice" {
		t.Fatalf("operator: got %q", ev.OperatorID)
	}
	if ev.Action != "answer" || ev.Issue != "PROJ-1234" {
		t.Fatalf("correlation: got action=%q issue=%q", ev.Action, ev.Issue)
	}
	if ev.Answer != "环境是预发" {
		t.Fatalf("answer: got %q", ev.Answer)
	}
}

func TestDecodeCallback_StandaloneButtonValue(t *testing.T) {
	line := `{"operator_id":"ou_bob","action_tag":"button","action_value":"{\"action\":\"approve\",\"issue\":\"PROJ-9\"}"}`
	ev, ok := decodeCallback([]byte(line))
	if !ok {
		t.Fatal("a standalone callback button should decode from action_value")
	}
	if ev.Action != "approve" || ev.Issue != "PROJ-9" {
		t.Fatalf("correlation: got action=%q issue=%q", ev.Action, ev.Issue)
	}
}

func TestDecodeCallback_IssuelessControlButton(t *testing.T) {
	line := `{"operator_id":"ou_a","action_tag":"button","action_value":"{\"action\":\"pause\"}"}`
	ev, ok := decodeCallback([]byte(line))
	if !ok {
		t.Fatal("an issue-less control action (pause) must still decode")
	}
	if ev.Action != "pause" || ev.Issue != "" {
		t.Fatalf("got action=%q issue=%q", ev.Action, ev.Issue)
	}
}

func TestDecodeCallback_RejectFormCarriesReason(t *testing.T) {
	name := EncodeActionName(Correlation{Action: "reject", Issue: "PROJ-3"})
	line := `{"operator_id":"ou_a","action_tag":"button","action_name":"` + name + `","form_value":"{\"reason\":\"方案不对\"}"}`
	ev, ok := decodeCallback([]byte(line))
	if !ok {
		t.Fatal("reject form should decode")
	}
	if ev.Action != "reject" || ev.Issue != "PROJ-3" || ev.Fields["reason"] != "方案不对" {
		t.Fatalf("got %+v", ev)
	}
}

func TestDecodeCallback_RerunFormCarriesIssueKey(t *testing.T) {
	name := EncodeActionName(Correlation{Action: "rerun"})
	line := `{"operator_id":"ou_a","action_tag":"button","action_name":"` + name + `","form_value":"{\"issueKey\":\"PROJ-9\"}"}`
	ev, ok := decodeCallback([]byte(line))
	if !ok {
		t.Fatal("rerun form (issue-less action, target in fields) should decode")
	}
	if ev.Action != "rerun" || ev.Fields["issueKey"] != "PROJ-9" {
		t.Fatalf("got %+v", ev)
	}
}

func TestDecodeCallback_RejectsUncorrelatable(t *testing.T) {
	cases := map[string]string{
		"no operator":    `{"action_tag":"button","action_value":"{\"action\":\"x\",\"issue\":\"P-1\"}"}`,
		"no correlation": `{"operator_id":"ou_a","action_tag":"button"}`,
		"not json":       `not json at all`,
		"foreign name":   `{"operator_id":"ou_a","action_tag":"button","action_name":"someone_elses_button","form_value":"{\"answer\":\"x\"}"}`,
	}
	for name, line := range cases {
		if _, ok := decodeCallback([]byte(line)); ok {
			t.Errorf("%s: should not decode into an actionable event", name)
		}
	}
}

func TestScanCallbacks_DispatchesDecodableLinesOnly(t *testing.T) {
	name := EncodeActionName(Correlation{Action: "answer", Issue: "PROJ-1"})
	line := func(ans string) string {
		return `{"operator_id":"ou_a","action_tag":"button","action_name":"` + name + `","form_value":"{\"answer\":\"` + ans + `\"}"}`
	}
	stream := strings.Join([]string{line("a1"), "", "garbage-not-json", line("a2")}, "\n")
	var got []string
	err := scanCallbacks(strings.NewReader(stream), func(ev CallbackEvent) error {
		got = append(got, ev.Answer)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "a1" || got[1] != "a2" {
		t.Fatalf("expected the 2 valid lines dispatched, got %v", got)
	}
}
