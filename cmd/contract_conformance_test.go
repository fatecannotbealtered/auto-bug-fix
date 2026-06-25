package cmd

import (
	"encoding/json"
	"testing"

	"github.com/fatecannotbealtered/auto-bug-fix/internal/contract"
)

// allErrorCodes enumerates every E_* code this tool can emit. Keep in sync with
// the constants and usages in output.go and update.go; the conformance test
// asserts each code is present in the canonical contract (core ∪ ext) with the
// exact exit code and retryability.
var allErrorCodes = []string{
	"E_USAGE",
	"E_VALIDATION",
	"E_NOT_FOUND",
	"E_CONFIG",
	"E_AUTH",
	"E_FORBIDDEN",
	"E_CONFIRMATION_REQUIRED",
	"E_CONFLICT",
	"E_NETWORK",
	"E_RATE_LIMITED",
	"E_SERVER",
	"E_TIMEOUT",
	"E_RUNTIME",
	"E_INTEGRITY",
	"E_IO",
	"E_INTERRUPTED",
}

// TestContractConformance_ErrorCodes asserts every emitted error code is in the
// canonical contract (core ∪ ext) with the exact exit + retryable values.
// This is the CI-red guard against drift (misnamed codes, wrong exit mappings).
func TestContractConformance_ErrorCodes(t *testing.T) {
	for _, c := range allErrorCodes {
		spec, ok := contract.Codes[c]
		if !ok {
			t.Errorf("error code %q is not in the canonical contract (core∪ext)", c)
			continue
		}
		if got := exitCodeForCode(c); got != spec.Exit {
			t.Errorf("exit drift for %q: tool=%d contract=%d", c, got, spec.Exit)
		}
		if got := retryableForCode(c); got != spec.Retryable {
			t.Errorf("retryable drift for %q: tool=%v contract=%v", c, got, spec.Retryable)
		}
	}
}

func TestContractConformance_SchemaVersion(t *testing.T) {
	if schemaVersion != contract.SchemaVersion {
		t.Fatalf("schema_version drift: tool=%q contract=%q", schemaVersion, contract.SchemaVersion)
	}
}

// TestContractConformance_EnvelopeKeys asserts the success and error envelopes
// (and meta) carry only the canonical top-level keys, catching extra/renamed
// fields (e.g. a stray meta.timestamp).
func TestContractConformance_EnvelopeKeys(t *testing.T) {
	// Build a success envelope
	successEnv := jsonEnvelope{
		OK:            true,
		SchemaVersion: schemaVersion,
		Data:          map[string]any{"x": 1},
		Meta:          map[string]any{"duration_ms": int64(0)},
	}
	checkEnvelopeKeys(t, successEnv, contract.SuccessEnvelopeKeys, "success")

	// Build an error envelope
	errorEnv := jsonEnvelope{
		OK:            false,
		SchemaVersion: schemaVersion,
		Error: &jsonError{
			Code:      "E_VALIDATION",
			Message:   "m",
			Retryable: false,
		},
		Meta: map[string]any{"duration_ms": int64(0)},
	}
	checkEnvelopeKeys(t, errorEnv, contract.ErrorEnvelopeKeys, "error")
}

func checkEnvelopeKeys(t *testing.T, env jsonEnvelope, canonical []string, label string) {
	t.Helper()
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal %s envelope: %v", label, err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil {
		t.Fatalf("unmarshal %s envelope: %v", label, err)
	}
	// "data"/"error" are omitempty and may be absent; flag only UNEXPECTED keys.
	for k := range top {
		if !containsStr(canonical, k) && k != "data" && k != "error" {
			t.Errorf("%s envelope has unexpected top-level key %q (canonical: %v)", label, k, canonical)
		}
	}
	for _, req := range []string{"ok", "schema_version", "meta"} {
		if _, ok := top[req]; !ok {
			t.Errorf("%s envelope missing required key %q", label, req)
		}
	}
	// 3b: success envelope must contain "data" (success_keys require it).
	if label == "success" {
		if _, ok := top["data"]; !ok {
			t.Errorf("success envelope missing required key \"data\"")
		}
	}
	var meta map[string]json.RawMessage
	if raw, ok := top["meta"]; ok {
		_ = json.Unmarshal(raw, &meta)
	}
	// 3a: assert each MetaRequiredKey is PRESENT (not just no extras).
	for _, req := range contract.MetaRequiredKeys {
		if _, ok := meta[req]; !ok {
			t.Errorf("meta missing required key %q (contract.MetaRequiredKeys=%v)", req, contract.MetaRequiredKeys)
		}
	}
	allowed := append(append([]string{}, contract.MetaRequiredKeys...), contract.MetaOptionalKeys...)
	for k := range meta {
		if !containsStr(allowed, k) {
			t.Errorf("meta has unexpected key %q (canonical: %v)", k, allowed)
		}
	}
}

// TestContractConformance_HardcodedTable asserts ExitCodeForErrorCode and
// RetryableForErrorCode match an INDEPENDENT hardcoded expected map of the 16
// core codes. This catches a wrong contract.json that the contract-delegating
// assertion above cannot detect (it would just pass the wrong values through).
// Canonical table: E_USAGE/E_VALIDATION=2; E_NOT_FOUND=3;
// E_AUTH/E_FORBIDDEN/E_CONFIG=4; E_CONFIRMATION_REQUIRED=5; E_CONFLICT=6;
// E_NETWORK/E_RATE_LIMITED/E_SERVER=7 (retryable); E_TIMEOUT=8 (retryable);
// E_INTEGRITY/E_IO/E_UNKNOWN=1; E_INTERRUPTED=130 (retryable); non-retryable otherwise.
func TestContractConformance_HardcodedTable(t *testing.T) {
	type want struct {
		exit      int
		retryable bool
	}
	table := map[string]want{
		"E_USAGE":                 {exit: 2, retryable: false},
		"E_VALIDATION":            {exit: 2, retryable: false},
		"E_NOT_FOUND":             {exit: 3, retryable: false},
		"E_AUTH":                  {exit: 4, retryable: false},
		"E_FORBIDDEN":             {exit: 4, retryable: false},
		"E_CONFIG":                {exit: 4, retryable: false},
		"E_CONFIRMATION_REQUIRED": {exit: 5, retryable: false},
		"E_CONFLICT":              {exit: 6, retryable: false},
		"E_NETWORK":               {exit: 7, retryable: true},
		"E_RATE_LIMITED":          {exit: 7, retryable: true},
		"E_SERVER":                {exit: 7, retryable: true},
		"E_TIMEOUT":               {exit: 8, retryable: true},
		"E_INTEGRITY":             {exit: 1, retryable: false},
		"E_IO":                    {exit: 1, retryable: false},
		"E_UNKNOWN":               {exit: 1, retryable: false},
		"E_INTERRUPTED":           {exit: 130, retryable: true},
	}
	for code, exp := range table {
		if got := exitCodeForCode(code); got != exp.exit {
			t.Errorf("exit drift for %q: got %d want %d", code, got, exp.exit)
		}
		if got := retryableForCode(code); got != exp.retryable {
			t.Errorf("retryable drift for %q: got %v want %v", code, got, exp.retryable)
		}
	}
}

func containsStr(s []string, x string) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}
