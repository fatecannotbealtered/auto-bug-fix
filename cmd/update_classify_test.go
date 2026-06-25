package cmd

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// TestStatusToCodeTaxonomy pins the single status->code->exit mapping (CLI-SPEC
// §6): 4xx that carry no retry signal are non-retryable and distinct from the
// retryable 429/5xx, and a 404 is E_NOT_FOUND — never collapsed into E_NETWORK.
func TestStatusToCodeTaxonomy(t *testing.T) {
	cases := []struct {
		status    int
		exit      int
		code      string
		retryable bool
	}{
		{401, exitConfig, "E_AUTH", false},
		{403, exitConfig, "E_FORBIDDEN", false},
		{404, exitNotFound, "E_NOT_FOUND", false},
		{408, exitTimeout, "E_TIMEOUT", true},
		{409, exitConflict, "E_CONFLICT", false},
		{429, exitNetwork, "E_RATE_LIMITED", true},
		{500, exitNetwork, "E_SERVER", true},
		{503, exitNetwork, "E_SERVER", true},
		{418, exitUsage, "E_VALIDATION", false},
	}
	for _, c := range cases {
		exit, code, retryable := statusToCode(c.status)
		if exit != c.exit || code != c.code || retryable != c.retryable {
			t.Errorf("status %d: got (%d,%q,%v) want (%d,%q,%v)",
				c.status, exit, code, retryable, c.exit, c.code, c.retryable)
		}
	}
}

// TestExitCodeForUpdateHTTPError: a typed *updateHTTPError is classified by its
// status through statusToCode, so the discover/download HTTP layer never has to
// be sniffed by message text.
func TestExitCodeForUpdateHTTPError(t *testing.T) {
	exit, code, retryable := exitCodeForError(&updateHTTPError{StatusCode: 404, URL: "https://x/repos/y/releases/latest"})
	if code != "E_NOT_FOUND" || exit != exitNotFound || retryable {
		t.Fatalf("404 should be non-retryable E_NOT_FOUND, got (%d,%q,%v)", exit, code, retryable)
	}
	exit, code, retryable = exitCodeForError(&updateHTTPError{StatusCode: 503})
	if code != "E_SERVER" || exit != exitNetwork || !retryable {
		t.Fatalf("503 should be retryable E_SERVER, got (%d,%q,%v)", exit, code, retryable)
	}
}

// TestUpdateHTTPGetReturnsTypedStatus: a non-2xx discover response surfaces a
// typed *updateHTTPError carrying the status, not a bare fmt.Errorf string, so
// the failure is classified by status rather than collapsed into E_NETWORK.
func TestUpdateHTTPGetReturnsTypedStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("rate something"))
	}))
	defer srv.Close()
	origClient := updateHTTPClient
	updateHTTPClient = srv.Client()
	defer func() { updateHTTPClient = origClient }()

	_, err := updateHTTPGet(context.Background(), srv.URL)
	var httpErr *updateHTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("non-2xx discover must return *updateHTTPError, got %T: %v", err, err)
	}
	if httpErr.StatusCode != http.StatusForbidden {
		t.Fatalf("status should be preserved, got %d", httpErr.StatusCode)
	}
	exit, code, retryable := exitCodeForError(err)
	if code != "E_FORBIDDEN" || exit != exitConfig || retryable {
		t.Fatalf("403 discover should be non-retryable E_FORBIDDEN, got (%d,%q,%v)", exit, code, retryable)
	}
}

// TestVerifySignatureBundleDownloadIsRetryableNetwork: a failed download of the
// signature bundle is a NETWORK failure (retryable), NOT an integrity verdict.
// The earlier behavior mislabeled it; this guards the split.
func TestVerifySignatureBundleDownloadIsRetryableNetwork(t *testing.T) {
	tmp := t.TempDir()
	origDownload := updateDownloadHook
	defer func() { updateDownloadHook = origDownload }()
	updateDownloadHook = func(context.Context, string, string) error {
		return &updateHTTPError{StatusCode: 503, URL: "https://x/checksums.txt.sigstore.json"}
	}

	status, err := verifyUpdateChecksumSignature(context.Background(), tmp+"/c.txt", "https://x/b.json", tmp)
	if err == nil {
		t.Fatal("a bundle download failure must surface an error")
	}
	if status != "download_failed" {
		t.Fatalf("status should be download_failed, got %q", status)
	}
	exit, code, retryable := exitCodeForError(err)
	if code == "E_INTEGRITY" {
		t.Fatalf("bundle download failure must NOT be E_INTEGRITY: %v", err)
	}
	if code != "E_SERVER" || exit != exitNetwork || !retryable {
		t.Fatalf("bundle download 503 should be retryable network, got (%d,%q,%v)", exit, code, retryable)
	}
}

// TestVerifySignatureBundleTransportIsRetryableNetwork: a bare transport error
// (DNS/dial) on the bundle download is reclassified to retryable E_NETWORK, not
// E_INTEGRITY.
func TestVerifySignatureBundleTransportIsRetryableNetwork(t *testing.T) {
	tmp := t.TempDir()
	origDownload := updateDownloadHook
	defer func() { updateDownloadHook = origDownload }()
	updateDownloadHook = func(context.Context, string, string) error {
		return errors.New("dial tcp: lookup example.invalid: no such host")
	}

	_, err := verifyUpdateChecksumSignature(context.Background(), tmp+"/c.txt", "https://x/b.json", tmp)
	exit, code, retryable := exitCodeForError(err)
	if code != "E_NETWORK" || exit != exitNetwork || !retryable {
		t.Fatalf("transport bundle failure should be retryable E_NETWORK, got (%d,%q,%v): %v", exit, code, retryable, err)
	}
}

// TestVerifySignatureMissingBundleIsIntegrity: a genuinely unsigned release
// (no bundle URL) still fails closed as non-retryable E_INTEGRITY.
func TestVerifySignatureMissingBundleIsIntegrity(t *testing.T) {
	tmp := t.TempDir()
	status, err := verifyUpdateChecksumSignature(context.Background(), tmp+"/c.txt", "", tmp)
	if status != "missing" {
		t.Fatalf("status should be missing, got %q", status)
	}
	exit, code, retryable := exitCodeForError(err)
	if code != "E_INTEGRITY" || exit != exitGeneric || retryable {
		t.Fatalf("missing bundle must be non-retryable E_INTEGRITY, got (%d,%q,%v)", exit, code, retryable)
	}
}

// TestVerifySignatureVerdictIsIntegrity: a real verification verdict failure
// (bad identity/signature) is non-retryable E_INTEGRITY — an agent must never
// retry it.
func TestVerifySignatureVerdictIsIntegrity(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(tmp+"/c.txt", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"bundle":"stub"}`))
	}))
	defer srv.Close()
	origClient, origVerify, origDownload := updateHTTPClient, updateVerifySignature, updateDownloadHook
	defer func() {
		updateHTTPClient, updateVerifySignature, updateDownloadHook = origClient, origVerify, origDownload
	}()
	updateHTTPClient = srv.Client()
	updateDownloadHook = func(_ context.Context, _, dest string) error {
		return os.WriteFile(dest, []byte("stub"), 0o600)
	}
	updateVerifySignature = func(_, _, _ string) error { return errors.New("certificate identity mismatch") }

	_, err := verifyUpdateChecksumSignature(context.Background(), tmp+"/c.txt", srv.URL+"/b.json", tmp)
	exit, code, retryable := exitCodeForError(err)
	if code != "E_INTEGRITY" || exit != exitGeneric || retryable {
		t.Fatalf("signature verdict failure must be non-retryable E_INTEGRITY, got (%d,%q,%v)", exit, code, retryable)
	}
}

// TestVerifySignatureTUFFetchIsRetryableNetwork: a network failure fetching the
// TUF trust root inside verification is retryable network, NOT an integrity
// verdict — the verifier types it and verifyUpdateChecksumSignature passes the
// typed code through.
func TestVerifySignatureTUFFetchIsRetryableNetwork(t *testing.T) {
	tmp := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"bundle":"stub"}`))
	}))
	defer srv.Close()
	origClient, origVerify, origDownload := updateHTTPClient, updateVerifySignature, updateDownloadHook
	defer func() {
		updateHTTPClient, updateVerifySignature, updateDownloadHook = origClient, origVerify, origDownload
	}()
	updateHTTPClient = srv.Client()
	updateDownloadHook = func(_ context.Context, _, dest string) error {
		return os.WriteFile(dest, []byte("stub"), 0o600)
	}
	updateVerifySignature = func(_, _, _ string) error {
		return newCodedError("E_NETWORK", exitNetwork, true, errors.New("loading sigstore trust root: timeout"))
	}

	_, err := verifyUpdateChecksumSignature(context.Background(), tmp+"/c.txt", srv.URL+"/b.json", tmp)
	exit, code, retryable := exitCodeForError(err)
	if code != "E_NETWORK" || exit != exitNetwork || !retryable {
		t.Fatalf("TUF trust-root fetch failure should be retryable E_NETWORK, got (%d,%q,%v): %v", exit, code, retryable, err)
	}
}

// TestFailVerifyClassification documents how failVerify maps a verify-stage
// error to the envelope: a typed network failure stays retryable network; any
// untyped/integrity failure becomes non-retryable E_INTEGRITY.
func TestFailVerifyClassification(t *testing.T) {
	// Network-typed -> retryable network, NOT integrity.
	exit, code, retryable := classifyVerifyError(newCodedError("E_NETWORK", exitNetwork, true, errors.New("net")))
	if code != "E_NETWORK" || exit != exitNetwork || !retryable {
		t.Fatalf("network verify failure should stay retryable network, got (%d,%q,%v)", exit, code, retryable)
	}
	// 503 bundle download -> retryable server, NOT integrity.
	exit, code, retryable = classifyVerifyError(&updateHTTPError{StatusCode: 503})
	if code != "E_SERVER" || exit != exitNetwork || !retryable {
		t.Fatalf("503 verify failure should stay retryable, got (%d,%q,%v)", exit, code, retryable)
	}
	// Bare verdict -> non-retryable integrity.
	exit, code, retryable = classifyVerifyError(errors.New("checksum mismatch for archive"))
	if code != "E_INTEGRITY" || exit != exitGeneric || retryable {
		t.Fatalf("bare verify failure should be non-retryable E_INTEGRITY, got (%d,%q,%v)", exit, code, retryable)
	}
}
