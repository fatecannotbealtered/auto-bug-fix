package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// setTempHome points the user home dir (and thus updateCachePath) at a fresh
// temp dir so a test controls the local update cache without touching the real
// one. It sets both HOME (Unix) and USERPROFILE (Windows) so os.UserHomeDir
// resolves to the temp dir on every platform.
func setTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", dir)
	}
	t.Setenv("HOME", dir)
	return dir
}

// writeTestCache seeds the local update cache with one available-update notice.
func writeTestCache(t *testing.T, updateAvailable bool, notices []map[string]any) {
	t.Helper()
	cache := updateCache{
		CurrentVersion:  "1.0.0",
		LatestVersion:   "999.0.0",
		UpdateAvailable: updateAvailable,
		CheckedAt:       "2026-06-21T00:00:00Z",
		Notices:         notices,
	}
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		t.Fatalf("marshal cache: %v", err)
	}
	path := updateCachePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write cache: %v", err)
	}
}

// captureEnvelope renders one JSON envelope from fn (which must call a print*
// path) and parses it.
func captureEnvelope(t *testing.T, fn func()) jsonEnvelope {
	t.Helper()
	origOut := activeOutput
	activeOutput = outputOptions{Format: "json", Compact: true}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = origStdout
	activeOutput = origOut

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	var env jsonEnvelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("output is not one JSON envelope: %v\n%s", err, buf.String())
	}
	return env
}

// failRoundTripper fails the test if any HTTP request is attempted — it guards
// the meta.notices path against accidental network I/O.
type failRoundTripper struct{ t *testing.T }

func (f failRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	f.t.Fatalf("meta.notices path made a network call to %s", req.URL)
	return nil, nil
}

// TestMetaNoticesPresentWhenCacheHasUpdate: an arbitrary (non-update) command's
// meta carries the cached notice when the local cache holds an available update,
// and the path makes no network call.
func TestMetaNoticesPresentWhenCacheHasUpdate(t *testing.T) {
	setTempHome(t)
	notice := map[string]any{"type": "update_available", "severity": "info", "latest_version": "999.0.0"}
	writeTestCache(t, true, []map[string]any{notice})

	origClient := updateHTTPClient
	updateHTTPClient = &http.Client{Transport: failRoundTripper{t}}
	t.Cleanup(func() { updateHTTPClient = origClient })

	env := captureEnvelope(t, func() { printJSON(map[string]any{"running": true}) })

	meta, ok := env.Meta["notices"].([]any)
	if !ok || len(meta) != 1 {
		t.Fatalf("meta.notices should be present with one notice: %#v", env.Meta)
	}
	first, _ := meta[0].(map[string]any)
	if first["type"] != "update_available" {
		t.Fatalf("meta.notices[0] should be the cached update notice: %#v", first)
	}
}

// TestMetaNoticesAbsentWhenCacheEmpty: meta.notices is omitted when the cache is
// missing or holds no available update.
func TestMetaNoticesAbsentWhenCacheEmpty(t *testing.T) {
	setTempHome(t)

	// No cache file at all.
	env := captureEnvelope(t, func() { printJSON(map[string]any{"running": true}) })
	if _, ok := env.Meta["notices"]; ok {
		t.Fatalf("meta.notices must be absent when no cache exists: %#v", env.Meta)
	}

	// Cache present but no available update.
	writeTestCache(t, false, nil)
	env = captureEnvelope(t, func() { printJSON(map[string]any{"running": true}) })
	if _, ok := env.Meta["notices"]; ok {
		t.Fatalf("meta.notices must be absent when cache has no available update: %#v", env.Meta)
	}
}

// TestUpdateNoticeSeverityWarningOnSecurityEntry: a changelog delta containing a
// security entry grades the notice "warning".
func TestUpdateNoticeSeverityWarningOnSecurityEntry(t *testing.T) {
	orig := changelogMarkdown
	t.Cleanup(func() { changelogMarkdown = orig })
	changelogMarkdown = "## [1.0.2] - 2026-06-21\n\n### Security\n\n- patched an auth bypass\n"
	if got := updateNoticeSeverity("1.0.1", "1.0.2"); got != "warning" {
		t.Fatalf("security entry in delta should be warning, got %q", got)
	}
}

// TestUpdateNoticeSeverityWarningOnMajorBump: a major version bump grades the
// notice "warning" even without a security entry.
func TestUpdateNoticeSeverityWarningOnMajorBump(t *testing.T) {
	orig := changelogMarkdown
	t.Cleanup(func() { changelogMarkdown = orig })
	changelogMarkdown = "## [2.0.0] - 2026-06-21\n\n### Changed\n\n- rewrote the config format\n"
	if got := updateNoticeSeverity("1.5.0", "2.0.0"); got != "warning" {
		t.Fatalf("major bump should be warning, got %q", got)
	}
}

// TestUpdateNoticeSeverityInfoOnPlainPatch: a routine patch with no security
// entry grades the notice "info".
func TestUpdateNoticeSeverityInfoOnPlainPatch(t *testing.T) {
	orig := changelogMarkdown
	t.Cleanup(func() { changelogMarkdown = orig })
	changelogMarkdown = "## [1.0.2] - 2026-06-21\n\n### Fixed\n\n- fixed a typo in the log line\n"
	if got := updateNoticeSeverity("1.0.1", "1.0.2"); got != "info" {
		t.Fatalf("plain patch should be info, got %q", got)
	}
}
