package agent

import "testing"

func TestMarkerScanner_IgnoresPlaceholderAndFiresOnce(t *testing.T) {
	fired := 0
	s := &markerScanner{onMarker: func() { fired++ }}

	// The documented example uses a `<...>` placeholder and must NOT trigger a
	// kill — a verbose agent echoing the template would otherwise be killed mid-run.
	s.Write([]byte("AUTO_BUG_FIX_RESULT outcome=auto-fix mr=<MR_URL>\n"))
	if fired != 0 || s.found() {
		t.Fatal("placeholder example line must not trigger")
	}

	// A real marker (concrete URL) triggers exactly once.
	s.Write([]byte("AUTO_BUG_FIX_RESULT outcome=auto-fix mr=https://gitlab.example/mr/1\n"))
	if fired != 1 || !s.found() {
		t.Fatalf("real marker must trigger once: fired=%d found=%v", fired, s.found())
	}

	// Once seen, further markers do not re-trigger.
	s.Write([]byte("AUTO_BUG_FIX_RESULT outcome=auto-fix mr=https://gitlab.example/mr/2\n"))
	if fired != 1 {
		t.Fatalf("must fire only once: fired=%d", fired)
	}
}

func TestMarkerScanner_DetectsAcrossChunks(t *testing.T) {
	fired := 0
	s := &markerScanner{onMarker: func() { fired++ }}

	s.Write([]byte("some log line\nAUTO_BUG_FIX_"))
	s.Write([]byte("RESULT outcome=needs-info handoff=.repo-knowledge/handoff/x.md"))
	if fired != 0 {
		t.Fatal("no newline yet — must not trigger on a partial line")
	}
	s.Write([]byte("\n"))
	if fired != 1 || !s.found() {
		t.Fatalf("must trigger once the marker line completes: fired=%d", fired)
	}
}
