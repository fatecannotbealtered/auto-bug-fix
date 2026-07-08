package notify_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fatecannotbealtered/auto-bug-fix/internal/notify"
)

func TestEncodeDecodeActionName_RoundTrip(t *testing.T) {
	corr := notify.Correlation{Action: "answer", Issue: "PROJ-1234"}
	got, ok := notify.DecodeActionName(notify.EncodeActionName(corr))
	if !ok || got != corr {
		t.Fatalf("round-trip failed: got %+v ok=%v", got, ok)
	}
	if _, ok := notify.DecodeActionName("random_button_name"); ok {
		t.Fatal("a name not produced by EncodeActionName must not decode")
	}
}

func TestRenderClarify_IsCard2WithFormAndEncodedCallback(t *testing.T) {
	ic, ok := larkChan(t).(notify.InteractiveChannel)
	if !ok {
		t.Fatal("lark channel must implement InteractiveChannel")
	}
	corr := notify.Correlation{Action: "answer", Issue: "PROJ-1234"}
	card, err := ic.RenderClarify(notify.Params{
		Issue: "PROJ-1234", Outcome: notify.OutcomeNeedsInfo,
		Summary: "登录报错", Solution: "期望的行为是什么？", JiraURL: "https://jira.example/PROJ-1234",
	}, corr)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(card), &root); err != nil {
		t.Fatalf("card is not valid JSON: %v", err)
	}
	if root["schema"] != "2.0" {
		t.Fatalf("clarify card must be Card 2.0, got schema %v", root["schema"])
	}
	if !strings.Contains(card, notify.EncodeActionName(corr)) {
		t.Fatal("submit button name must encode the correlation")
	}
	if !strings.Contains(card, `"tag":"form"`) || !strings.Contains(card, `"input_type":"multiline_text"`) {
		t.Fatal("card must contain a form with a multiline input box")
	}
	if !strings.Contains(card, "https://jira.example/PROJ-1234") {
		t.Fatal("card should include the Jira link button when JiraURL is set")
	}
}

// The Card 1.0 `note` element is not a Card 2.0 component; none of the 2.0
// renderers may emit it (guards against the note()/note2() miss recurring).
func TestCard2Renderers_NeverEmitNoteTag(t *testing.T) {
	ic := larkChan(t).(notify.InteractiveChannel)
	clarify, err := ic.RenderClarify(
		notify.Params{Issue: "P-1", Outcome: notify.OutcomeNeedsInfo, RootCause: "rc", Solution: "q"},
		notify.Correlation{Action: "answer", Issue: "P-1"})
	if err != nil {
		t.Fatal(err)
	}
	approval, err := ic.RenderApproval(
		notify.Params{Issue: "P-1", Outcome: notify.OutcomeAutoFix, Branch: "fix/P-1", Service: "repo"},
		"2 files", notify.Correlation{Action: "approve", Issue: "P-1"})
	if err != nil {
		t.Fatal(err)
	}
	control, err := ic.RenderControl("轮询：运行中")
	if err != nil {
		t.Fatal(err)
	}
	for name, card := range map[string]string{"clarify": clarify, "approval": approval, "control": control} {
		if strings.Contains(card, `"tag":"note"`) {
			t.Errorf("%s Card 2.0 body must not contain the Card 1.0 `note` tag", name)
		}
	}
}

func TestRenderApproval_Card2WithApproveCallbackAndRejectForm(t *testing.T) {
	ic := larkChan(t).(notify.InteractiveChannel)
	corr := notify.Correlation{Action: "approve", Issue: "PROJ-3"}
	card, err := ic.RenderApproval(
		notify.Params{Issue: "PROJ-3", Outcome: notify.OutcomeAutoFix, Branch: "fix/PROJ-3", Service: "repo"},
		"2 文件，+10 −2\n--- a\n+++ b", corr)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(card), &root); err != nil {
		t.Fatalf("approval card invalid JSON: %v", err)
	}
	if root["schema"] != "2.0" {
		t.Fatalf("approval card must be Card 2.0, got %v", root["schema"])
	}
	if !strings.Contains(card, `"action":"approve"`) || !strings.Contains(card, "PROJ-3") {
		t.Fatal("approve button must carry the {action:approve,issue} callback value")
	}
	if !strings.Contains(card, notify.EncodeActionName(notify.Correlation{Action: "reject", Issue: "PROJ-3"})) {
		t.Fatal("reject submit button name must encode the reject correlation")
	}
	if !strings.Contains(card, "2 文件") {
		t.Fatal("approval card should include the diff summary")
	}
}

func TestRenderApproval_RejectsEmptyIssue(t *testing.T) {
	ic := larkChan(t).(notify.InteractiveChannel)
	if _, err := ic.RenderApproval(notify.Params{Outcome: notify.OutcomeAutoFix}, "", notify.Correlation{Action: "approve"}); err == nil {
		t.Fatal("approval card must require a correlation issue key")
	}
}

func TestRenderControl_Card2WithButtonsAndRerunForm(t *testing.T) {
	ic := larkChan(t).(notify.InteractiveChannel)
	card, err := ic.RenderControl("轮询：运行中")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []string{`"action":"pause"`, `"action":"resume"`, `"action":"status"`} {
		if !strings.Contains(card, a) {
			t.Fatalf("control card missing button %s", a)
		}
	}
	if !strings.Contains(card, notify.EncodeActionName(notify.Correlation{Action: "rerun"})) {
		t.Fatal("rerun submit button name must encode the rerun action")
	}
	if !strings.Contains(card, "issueKey") {
		t.Fatal("control card must have the rerun issueKey input")
	}
}

func TestRenderClarify_RejectsNonNeedsInfo(t *testing.T) {
	ic := larkChan(t).(notify.InteractiveChannel)
	if _, err := ic.RenderClarify(
		notify.Params{Issue: "P-1", Outcome: notify.OutcomeAutoFix},
		notify.Correlation{Action: "answer", Issue: "P-1"},
	); err == nil {
		t.Fatal("clarify card must reject a non-needs-info outcome")
	}
}
