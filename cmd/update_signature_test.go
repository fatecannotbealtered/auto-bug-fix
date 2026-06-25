package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestVerifyUpdateChecksumSignature_FailClosed is the in-process fail-closed
// contract for the signature gate: a missing bundle is refused (no skip), a
// failing verification aborts, and only a successful verification yields
// "verified".
func TestVerifyUpdateChecksumSignature_FailClosed(t *testing.T) {
	tmp := t.TempDir()

	if _, err := verifyUpdateChecksumSignature(context.Background(), tmp+"/checksums.txt", "", tmp); err == nil {
		t.Fatal("missing signature bundle must be refused")
	} else if !strings.Contains(err.Error(), "unsigned release") {
		t.Fatalf("unexpected error for missing bundle: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"bundle":"stub"}`))
	}))
	defer srv.Close()
	origClient := updateHTTPClient
	origVerify := updateVerifySignature
	defer func() { updateHTTPClient = origClient; updateVerifySignature = origVerify }()
	updateHTTPClient = srv.Client()

	updateVerifySignature = func(_, _, _ string) error { return nil }
	status, err := verifyUpdateChecksumSignature(context.Background(), tmp+"/c.txt", srv.URL+"/b.json", tmp)
	if err != nil || status != "verified" {
		t.Fatalf("expected verified, got status=%q err=%v", status, err)
	}

	updateVerifySignature = func(_, _, _ string) error { return errors.New("certificate identity mismatch") }
	if _, err := verifyUpdateChecksumSignature(context.Background(), tmp+"/c.txt", srv.URL+"/b.json", tmp); err == nil {
		t.Fatal("signature verification failure must abort")
	}
}

// signedReleaseServer stands in for the GitHub releases API + asset CDN. It
// serves a release JSON listing the platform archive, checksums.txt, and the
// sigstore bundle, plus the asset bytes themselves.
func signedReleaseServer(t *testing.T, assetName string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, _ *http.Request) {
		rel := map[string]any{
			"tag_name": "v9.9.9",
			"html_url": "https://example.test/releases/v9.9.9",
			"assets": []map[string]any{
				{"name": assetName, "browser_download_url": base + "/dl/" + assetName},
				{"name": "checksums.txt", "browser_download_url": base + "/dl/checksums.txt"},
				{"name": "checksums.txt.sigstore.json", "browser_download_url": base + "/dl/checksums.txt.sigstore.json"},
			},
		}
		_ = json.NewEncoder(w).Encode(rel)
	})
	mux.HandleFunc("/dl/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("payload for " + r.URL.Path))
	})
	srv := httptest.NewServer(mux)
	base = srv.URL
	return srv
}

// TestRunUpdateBinarySignedPath drives the full raw-binary path in-process with
// every external seam stubbed: discover -> download -> verify_signature ->
// verify_checksum -> replace -> skill_sync. It asserts signature is verified
// before checksum and that the success envelope carries signature_verified.
func TestRunUpdateBinarySignedPath(t *testing.T) {
	t.Setenv("AUTO_BUG_FIX_INSTALL_METHOD", "binary")
	srv := signedReleaseServer(t, "auto-bug-fix-9.9.9-linux-amd64.tar.gz")
	defer srv.Close()

	var order []string
	restore := withBinaryUpdateSeams(t, srv.URL, &order)
	defer restore()

	env := captureBinaryUpdate(t, func() {
		runUpdate([]string{"--target-version", "9.9.9"})
	})
	if !env.OK {
		t.Fatalf("signed binary update should succeed: %+v", env.Error)
	}
	data, _ := env.Data.(map[string]any)
	if replaced, _ := data["binary_replaced"].(bool); !replaced {
		t.Fatalf("binary_replaced should be true on success: %#v", data)
	}
	if sv, _ := data["signature_verified"].(bool); !sv {
		t.Fatalf("signature_verified must be true after in-process verification: %#v", data)
	}
	if ss, _ := data["signature_status"].(string); ss != "verified" {
		t.Fatalf("signature_status should be verified: %#v", data)
	}
	if cv, _ := data["checksum_verified"].(bool); !cv {
		t.Fatalf("checksum_verified should be true: %#v", data)
	}
	// Signature must be verified BEFORE the checksum.
	sigIdx, sumIdx := indexOf(order, "verify_signature"), indexOf(order, "verify_checksum")
	if sigIdx < 0 || sumIdx < 0 || sigIdx > sumIdx {
		t.Fatalf("signature must be verified before checksum, order=%v", order)
	}
}

// TestRunUpdateBinarySignatureFailure: a signature that does not verify is a
// non-retryable E_INTEGRITY at stage verify_signature, exit 1, binary untouched.
func TestRunUpdateBinarySignatureFailure(t *testing.T) {
	env, exit := runBinaryUpdateSubprocess(t, "signature_fail")
	if env.OK {
		t.Fatalf("signature failure must fail: %+v", env)
	}
	if env.Error == nil || env.Error.Code != "E_INTEGRITY" || env.Error.Retryable {
		t.Fatalf("signature failure must be non-retryable E_INTEGRITY: %+v", env.Error)
	}
	if exit != exitGeneric {
		t.Fatalf("E_INTEGRITY exit want 1 got %d", exit)
	}
	if env.Error.Details["stage"] != "verify_signature" {
		t.Fatalf("stage should be verify_signature: %#v", env.Error.Details)
	}
	if br, _ := env.Error.Details["binary_replaced"].(bool); br {
		t.Fatalf("signature failure must leave binary_replaced:false: %#v", env.Error.Details)
	}
}

// TestRunUpdateBinaryChecksumMismatch: signature verifies but the checksum does
// not match -> non-retryable E_INTEGRITY at stage verify_checksum.
func TestRunUpdateBinaryChecksumMismatch(t *testing.T) {
	env, exit := runBinaryUpdateSubprocess(t, "checksum_fail")
	if env.OK {
		t.Fatalf("checksum mismatch must fail: %+v", env)
	}
	if env.Error == nil || env.Error.Code != "E_INTEGRITY" || env.Error.Retryable {
		t.Fatalf("checksum mismatch must be non-retryable E_INTEGRITY: %+v", env.Error)
	}
	if exit != exitGeneric {
		t.Fatalf("E_INTEGRITY exit want 1 got %d", exit)
	}
	if env.Error.Details["stage"] != "verify_checksum" {
		t.Fatalf("stage should be verify_checksum: %#v", env.Error.Details)
	}
}

// TestRunUpdateBinaryMissingBundle: a release without the sigstore bundle is
// refused (fail-closed) with E_INTEGRITY at verify_signature.
func TestRunUpdateBinaryMissingBundle(t *testing.T) {
	env, exit := runBinaryUpdateSubprocess(t, "missing_bundle")
	if env.OK {
		t.Fatalf("missing bundle must be refused: %+v", env)
	}
	if env.Error == nil || env.Error.Code != "E_INTEGRITY" || env.Error.Retryable {
		t.Fatalf("missing bundle must be non-retryable E_INTEGRITY: %+v", env.Error)
	}
	if exit != exitGeneric {
		t.Fatalf("E_INTEGRITY exit want 1 got %d", exit)
	}
	if env.Error.Details["stage"] != "verify_signature" {
		t.Fatalf("stage should be verify_signature: %#v", env.Error.Details)
	}
	if !strings.Contains(env.Error.Message, "unsigned release") {
		t.Fatalf("missing bundle message should mention unsigned release: %q", env.Error.Message)
	}
}

// TestRunUpdateBinarySignatureBundleNetwork: a failed download of the signature
// bundle inside verify_signature is a RETRYABLE network failure, NOT a forged
// release. It must surface as retryable (E_SERVER for 503), exit 7, with the
// binary untouched — never the non-retryable E_INTEGRITY an agent must not loop
// on. This is the core of fix #1.
func TestRunUpdateBinarySignatureBundleNetwork(t *testing.T) {
	env, exit := runBinaryUpdateSubprocess(t, "sig_bundle_network")
	if env.OK {
		t.Fatalf("bundle network failure must fail: %+v", env)
	}
	if env.Error == nil {
		t.Fatal("missing error envelope")
	}
	if env.Error.Code == "E_INTEGRITY" || !env.Error.Retryable {
		t.Fatalf("bundle download failure must be retryable network, not E_INTEGRITY: %+v", env.Error)
	}
	if exit != exitNetwork {
		t.Fatalf("retryable network exit want %d got %d", exitNetwork, exit)
	}
	if env.Error.Details["stage"] != "verify_signature" {
		t.Fatalf("stage should be verify_signature: %#v", env.Error.Details)
	}
	if br, _ := env.Error.Details["binary_replaced"].(bool); br {
		t.Fatalf("verify-stage failure must leave binary_replaced:false: %#v", env.Error.Details)
	}
}

// TestRunUpdateBinaryVerifyInterrupt: a SIGINT during the verify stage emits a
// terminal E_INTERRUPTED envelope (exit 130) reporting no change, not a bare
// killed process and not E_INTEGRITY.
func TestRunUpdateBinaryVerifyInterrupt(t *testing.T) {
	env, exit := runBinaryUpdateSubprocess(t, "verify_interrupt")
	if env.OK {
		t.Fatalf("interrupt must fail: %+v", env)
	}
	if env.Error == nil || env.Error.Code != "E_INTERRUPTED" {
		t.Fatalf("verify-stage interrupt must be E_INTERRUPTED: %+v", env.Error)
	}
	if exit != exitInterrupted {
		t.Fatalf("E_INTERRUPTED exit want %d got %d", exitInterrupted, exit)
	}
	stage, _ := env.Error.Details["stage"].(string)
	if stage != "verify_signature" && stage != "verify_checksum" {
		t.Fatalf("interrupt stage should be a verify stage: %#v", env.Error.Details)
	}
	if br, _ := env.Error.Details["binary_replaced"].(bool); br {
		t.Fatalf("pre-swap interrupt must report binary_replaced:false: %#v", env.Error.Details)
	}
}

// runBinaryUpdateSubprocess re-executes this test binary in the signed-binary
// hook entrypoint so paths that exit via fail() can be observed.
func runBinaryUpdateSubprocess(t *testing.T, mode string) (jsonEnvelope, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run", "TestBinaryUpdateSubprocessHook") //nolint:gosec
	cmd.Env = append(os.Environ(),
		"AUTO_BUG_FIX_TEST_BINUPDATE_HOOK="+mode,
		"AUTO_BUG_FIX_INSTALL_METHOD=binary",
	)
	out, err := cmd.Output()
	exit := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		exit = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("subprocess run: %v", err)
	}
	var env jsonEnvelope
	if jerr := json.Unmarshal(out, &env); jerr != nil {
		t.Fatalf("subprocess stdout is not one JSON envelope: %v\n%s", jerr, string(out))
	}
	return env, exit
}

// TestBinaryUpdateSubprocessHook is the child entrypoint for signed-binary
// failure modes. It wires the seams to a local server and a scripted failure,
// then runs runUpdate (which exits via fail()).
func TestBinaryUpdateSubprocessHook(t *testing.T) {
	mode := os.Getenv("AUTO_BUG_FIX_TEST_BINUPDATE_HOOK")
	if mode == "" {
		t.Skip("not a subprocess invocation")
	}
	assetName := "auto-bug-fix-9.9.9-linux-amd64.tar.gz"
	srv := signedReleaseServer(t, assetName)
	if mode == "missing_bundle" {
		srv.Close()
		// Re-serve a release WITHOUT the sigstore bundle asset.
		mux := http.NewServeMux()
		var base string
		mux.HandleFunc("/repos/", func(w http.ResponseWriter, _ *http.Request) {
			rel := map[string]any{
				"tag_name": "v9.9.9",
				"assets": []map[string]any{
					{"name": assetName, "browser_download_url": base + "/dl/" + assetName},
					{"name": "checksums.txt", "browser_download_url": base + "/dl/checksums.txt"},
				},
			}
			_ = json.NewEncoder(w).Encode(rel)
		})
		mux.HandleFunc("/dl/", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("x")) })
		srv = httptest.NewServer(mux)
		base = srv.URL
	}
	defer srv.Close()

	var order []string
	withBinaryUpdateSeams(t, srv.URL, &order)
	activeOutput = outputOptions{Format: "json", Compact: true}

	switch mode {
	case "signature_fail":
		updateVerifySignature = func(_, _, _ string) error { return errors.New("certificate identity mismatch") }
	case "checksum_fail":
		updateVerifySignature = func(_, _, _ string) error { return nil }
		updateChecksumHook = func(_, _, _ string) error { return errors.New("checksum mismatch for archive") }
	case "sig_bundle_network":
		// The signature-bundle download fails with a 503: a retryable NETWORK
		// failure inside the verify stage, NOT an integrity verdict. The archive +
		// checksums downloads (called first) must still succeed, so only fail the
		// bundle URL.
		updateDownloadHook = func(_ context.Context, url, dest string) error {
			if strings.HasSuffix(url, "checksums.txt.sigstore.json") {
				return &updateHTTPError{StatusCode: 503, URL: url}
			}
			return os.WriteFile(dest, []byte("stub"), 0o600)
		}
	case "verify_interrupt":
		// Simulate a SIGINT landing during the verify stage. The signal context is
		// stubbed so that when the bundle download (the first network step of
		// verify_signature) starts, it cancels the update context — exactly as a
		// real SIGINT would. The verify stage must then observe ctx.Err() and emit a
		// terminal E_INTERRUPTED envelope (exit 130), never E_INTEGRITY. (A
		// self-delivered SIGINT is not portable — unsupported on Windows — so the
		// signal seam is what makes this deterministic.)
		ctx, cancel := context.WithCancel(context.Background())
		updateSignalContext = func() (context.Context, context.CancelFunc) { return ctx, cancel }
		updateDownloadHook = func(_ context.Context, url, dest string) error {
			if err := os.WriteFile(dest, []byte("stub"), 0o600); err != nil {
				return err
			}
			if strings.HasSuffix(url, "checksums.txt.sigstore.json") {
				cancel()
			}
			return nil
		}
	}
	runUpdate([]string{"--target-version", "9.9.9"})
}

// withBinaryUpdateSeams stubs every external dependency of the raw-binary path so
// it runs hermetically: GitHub API host, platform, executable, download,
// signature, checksum, extract, atomic apply, and the npx skill-sync runner. The
// order slice records which verification stages ran, in order.
func withBinaryUpdateSeams(t *testing.T, apiURL string, order *[]string) func() {
	t.Helper()
	orig := struct {
		api      string
		platform func() (string, string)
		exe      func() (string, error)
		download func(context.Context, string, string) error
		verify   func(string, string, string) error
		checksum func(string, string, string) error
		extract  func(string, string, string) (string, error)
		apply    func(string, string) (updateApplyResult, error)
		runner   func(context.Context, string, ...string) error
	}{updateGitHubAPI, updatePlatform, updateExecutable, updateDownloadHook, updateVerifySignature, updateChecksumHook, updateExtractHook, updateApply, updateRunCommand}

	updateGitHubAPI = apiURL
	updatePlatform = func() (string, string) { return "linux", "amd64" }
	updateExecutable = func() (string, error) { return "/usr/local/bin/auto-bug-fix", nil }
	updateDownloadHook = func(ctx context.Context, _, dest string) error {
		return os.WriteFile(dest, []byte("stub"), 0o600)
	}
	updateVerifySignature = func(_, _, _ string) error { *order = append(*order, "verify_signature"); return nil }
	updateChecksumHook = func(_, _, _ string) error { *order = append(*order, "verify_checksum"); return nil }
	updateExtractHook = func(_, _, tmpDir string) (string, error) {
		p := tmpDir + "/auto-bug-fix"
		return p, os.WriteFile(p, []byte("new binary"), 0o755)
	}
	updateApply = func(_, dst string) (updateApplyResult, error) {
		return updateApplyResult{Status: "installed", Path: dst}, nil
	}
	updateRunCommand = func(_ context.Context, _ string, _ ...string) error { return nil }

	restore := func() {
		updateGitHubAPI = orig.api
		updatePlatform = orig.platform
		updateExecutable = orig.exe
		updateDownloadHook = orig.download
		updateVerifySignature = orig.verify
		updateChecksumHook = orig.checksum
		updateExtractHook = orig.extract
		updateApply = orig.apply
		updateRunCommand = orig.runner
	}
	t.Cleanup(restore)
	return restore
}

func captureBinaryUpdate(t *testing.T, fn func()) jsonEnvelope {
	t.Helper()
	origOut := activeOutput
	activeOutput = outputOptions{Format: "json", Compact: true}
	t.Cleanup(func() { activeOutput = origOut })

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = origStdout
	var buf strings.Builder
	b := make([]byte, 4096)
	for {
		n, e := r.Read(b)
		if n > 0 {
			buf.Write(b[:n])
		}
		if e != nil {
			break
		}
	}
	var env jsonEnvelope
	if err := json.Unmarshal([]byte(buf.String()), &env); err != nil {
		t.Fatalf("binary update output is not one JSON envelope: %v\n%s", err, buf.String())
	}
	return env
}

func indexOf(xs []string, want string) int {
	for i, x := range xs {
		if x == want {
			return i
		}
	}
	return -1
}
