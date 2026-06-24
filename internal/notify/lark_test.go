package notify_test

import (
	"errors"
	"strings"
	"testing"
)

// larkChan is defined in notify_test.go (same package).

func TestLarkHealthy_OKFromTopLevelOk(t *testing.T) {
	// Real lark-cli doctor shape: flat {ok, checks, _notice}. The _notice update
	// hint must be ignored; only the authoritative top-level `ok` decides health.
	run := func(_ string, _ ...string) ([]byte, error) {
		return []byte(`{"ok":true,"checks":[{"name":"identity_ready","status":"pass"}],"_notice":{"update":{"message":"1.0.57 available"}}}`), nil
	}
	if ok, detail := larkChan(t).Healthy(run); !ok {
		t.Fatalf("healthy lark (ok:true) should be ok, got detail %q", detail)
	}
}

func TestLarkHealthy_NotAuthenticatedWhenOkFalse(t *testing.T) {
	run := func(_ string, _ ...string) ([]byte, error) {
		return []byte(`{"ok":false,"checks":[{"name":"identity_ready","status":"fail"}]}`), nil
	}
	ok, detail := larkChan(t).Healthy(run)
	if ok {
		t.Fatal("ok:false must be reported not ok")
	}
	if !strings.Contains(detail, "auth login") {
		t.Errorf("detail should hint auth login, got %q", detail)
	}
}

func TestLarkHealthy_NotUsableOnUnparseable(t *testing.T) {
	run := func(_ string, _ ...string) ([]byte, error) {
		return []byte("not json"), errors.New("exit 1")
	}
	ok, detail := larkChan(t).Healthy(run)
	if ok {
		t.Fatal("unparseable output must be not ok")
	}
	if !strings.Contains(detail, "lark-cli doctor") {
		t.Errorf("detail should hint running doctor, got %q", detail)
	}
}

func TestLarkHealthy_DoesNotPassJSONFlag(t *testing.T) {
	// lark-cli rejects --json; Healthy must invoke `lark-cli doctor` without it.
	var gotArgs []string
	run := func(_ string, args ...string) ([]byte, error) {
		gotArgs = args
		return []byte(`{"ok":true}`), nil
	}
	larkChan(t).Healthy(run)
	for _, a := range gotArgs {
		if a == "--json" {
			t.Fatalf("Healthy must not pass --json to lark-cli, got args %v", gotArgs)
		}
	}
}
