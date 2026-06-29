package cmd

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const (
	updatePackageName = "@fateforge/auto-bug-fix"
	updateRepo        = "fatecannotbealtered/auto-bug-fix"
	updateSkillRepo   = updateRepo
	updateBinaryName  = "auto-bug-fix"
	updateAPIBaseURL  = "https://api.github.com"
	updateRegistryURL = "https://registry.npmjs.org/%40fateforge%2Fauto-bug-fix/latest"
)

var (
	updateHTTPClient = &http.Client{Timeout: 2 * time.Minute}
	updateRunCommand = runCommand
	// updateRunPackageManager is a testable seam for the npm install step in
	// runUpdateNPM. Tests stub it to avoid shelling out to real npm.
	updateRunPackageManager = updateRunCommand
	updateGitHubAPI         = updateAPIBaseURL
	updatePlatform          = func() (string, string) { return runtime.GOOS, runtime.GOARCH }
	updateExecutable        = os.Executable
	updateApply             = applyUpdateBinary
	// Stage seams, overridable in tests so the staged signed-update failure
	// contract can be exercised without building real signed archives.
	updateDownloadHook = downloadUpdateFile
	updateChecksumHook = verifyUpdateChecksum
	updateExtractHook  = extractUpdateArchive
	// updateSignalContext builds the SIGINT/SIGTERM-cancelled context for the
	// update run. It is a seam so the interrupt contract (terminal envelope + exit
	// 130) can be exercised deterministically, since self-delivered SIGINT is not
	// portable (notably unsupported on Windows).
	updateSignalContext = func() (context.Context, context.CancelFunc) {
		return signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	}
)

type updateResult struct {
	Status            string           `json:"status"`
	CurrentVersion    string           `json:"current_version"`
	LatestVersion     string           `json:"latest_version"`
	TargetVersion     string           `json:"target_version"`
	UpdateAvailable   bool             `json:"update_available"`
	InstallMethod     string           `json:"install_method"`
	Command           string           `json:"command"`
	SkillSyncCommand  string           `json:"skill_sync_command"`
	SkillSyncStatus   string           `json:"skill_sync_status,omitempty"`
	Message           string           `json:"message,omitempty"`
	SignatureStatus   string           `json:"signature_status"`
	SignatureVerified bool             `json:"signature_verified"`
	ChecksumVerified  bool             `json:"checksum_verified"`
	PreviousVersion   string           `json:"previous_version,omitempty"`
	NextSteps         []string         `json:"next_steps,omitempty"`
	Notices           []map[string]any `json:"notices,omitempty"`
	Preview           map[string]any   `json:"preview,omitempty"`
	Stage             string           `json:"stage,omitempty"`
	BinaryReplaced    bool             `json:"binary_replaced"`
	ReleaseURL        string           `json:"release_url,omitempty"`
	Path              string           `json:"path,omitempty"`
}

// updateStage names a single step of the staged self-update so every failure
// and the success payload can report exactly how far the update got. The
// npm-managed path runs discover -> replace (npm install) -> skill_sync; the
// raw-binary path runs discover -> download -> verify_signature ->
// verify_checksum -> replace (atomic swap) -> skill_sync.
const (
	stageDiscover        = "discover"
	stageDownload        = "download"
	stageVerifySignature = "verify_signature"
	stageVerifyChecksum  = "verify_checksum"
	stageReplace         = "replace"
	stageSkillSync       = "skill_sync"
)

func runUpdate(args []string) {
	checkOnly := false
	dryRun := false
	targetVersion := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--check":
			checkOnly = true
		case "--dry-run":
			dryRun = true
		case "--target-version", "--version":
			if i+1 >= len(args) {
				fail(exitUsage, "E_USAGE", "update: "+args[i]+" requires a version", nil, false)
			}
			targetVersion = normalizeVersion(args[i+1])
			i++
		default:
			fail(exitUsage, "E_USAGE", "update: unknown argument "+args[i], nil, false)
		}
	}

	// Trap SIGINT/SIGTERM up front so even the discover-stage network probe (the
	// npm-registry / GitHub release lookup) is cancellable and still emits a
	// terminal envelope on interrupt, rather than a bare killed process. The
	// signal context now covers discovery, not only download/verify/replace.
	ctx, stop := updateSignalContext()
	defer stop()

	// discover stage: resolve the latest (or requested) version.
	result, err := buildUpdateResult(ctx, targetVersion)
	if err != nil {
		if ctx.Err() != nil {
			interruptUpdate(stageDiscover, normalizeVersion(version), false, "not_run")
		}
		failUpdate(stageDiscover, normalizeVersion(version), false, "not_applicable", err)
	}

	if checkOnly {
		result.Status = updateStatus(result)
		result.Notices = updateNotices(result)
		writeUpdateCache(result)
		printUpdate(result)
		return
	}

	// --dry-run is an OPTIONAL read-only preview: no confirm token, no expiry,
	// and never a required step before a bare `update`.
	if dryRun {
		result.Status = "dry_run"
		result.Preview = updateDryRunPreview(result)
		printUpdate(result)
		return
	}

	// Idempotent: already on the latest (or requested) version -> no-op success.
	if !result.UpdateAvailable {
		result.Status = "up_to_date"
		result.SkillSyncStatus = "skipped"
		result.PreviousVersion = result.CurrentVersion
		printUpdate(result)
		return
	}

	// Route by install method: an npm-managed install (exe under node_modules)
	// updates via the package manager; a raw binary self-updates from the signed
	// GitHub release. Raw-binary installs are no longer refused.
	if result.InstallMethod == "npm" {
		runUpdateNPM(ctx, result)
		return
	}
	runUpdateBinary(ctx, result)
}

// runUpdateNPM updates an npm-managed install via `npm install -g` then syncs
// the Skill. There is no in-process signature/checksum stage here: the package
// manager owns artifact integrity, so signature fields stay package-managed.
func runUpdateNPM(ctx context.Context, result updateResult) {
	// replace stage: npm install. Everything here leaves the installed package
	// untouched until npm commits, so binary_replaced stays false on failure.
	if err := updateRunPackageManager(ctx, "npm", "install", "-g", updatePackageName+"@"+result.TargetVersion); err != nil {
		if ctx.Err() != nil {
			interruptUpdate(stageReplace, result.CurrentVersion, false, "not_started")
		}
		failReplace(result, err)
	}

	// Past the atomic commit point: the package is now the new version.
	result.PreviousVersion = result.CurrentVersion
	result.CurrentVersion = result.TargetVersion
	result.BinaryReplaced = true

	// skill_sync stage: runs AFTER the replace and is independently replayable.
	// A failure here is partial success, not a hard error — the package already
	// updated; the agent just needs to run skill_sync_command.
	if err := updateRunCommand(ctx, "npx", "skills", "add", updateSkillRepo, "-y", "-g"); err != nil {
		if ctx.Err() != nil {
			interruptUpdate(stageSkillSync, result.CurrentVersion, true, "failed")
		}
		failSkillSync(result, err)
	}

	result.Status = "updated"
	result.SkillSyncStatus = "synced"
	result.Stage = stageSkillSync
	result.NextSteps = []string{
		"run auto-bug-fix changelog --since " + result.PreviousVersion + " --compact",
		"run auto-bug-fix reference --compact",
	}
	printUpdate(result)
}

// runUpdateBinary self-updates a raw binary install from the signed GitHub
// release: resolve the release, download the platform archive + checksums.txt +
// checksums.txt.sigstore.json, verify the Sigstore signature on checksums.txt
// in-process (FIRST), verify the archive SHA256 (SECOND), atomically replace the
// running binary, then sync the Skill. Signature verification is mandatory and
// fail-closed: an unsigned, unverifiable, or mismatched release is refused with
// E_INTEGRITY — there is no skip path.
func runUpdateBinary(ctx context.Context, result updateResult) {
	exePath, _ := updateExecutable()
	if exePath == "" {
		fail(exitGeneric, "E_IO", "update replace failed: could not determine current executable path",
			updateFailureDetails(stageReplace, result.CurrentVersion, false, "not_run"), false)
	}

	rel, err := fetchUpdateRelease(ctx, result.TargetVersion)
	if err != nil {
		if ctx.Err() != nil {
			interruptUpdate(stageDiscover, result.CurrentVersion, false, "not_run")
		}
		failUpdate(stageDiscover, result.CurrentVersion, false, "not_run", err)
	}
	plan, err := buildUpdatePlan(rel, result.TargetVersion)
	if err != nil {
		fail(exitNotFound, "E_NOT_FOUND", "update discover failed: "+err.Error(),
			updateFailureDetails(stageDiscover, result.CurrentVersion, false, "not_run"), false)
	}
	result.ReleaseURL = plan.ReleaseURL

	tmpDir, err := os.MkdirTemp("", "auto-bug-fix-update-*")
	if err != nil {
		fail(exitGeneric, "E_IO", "update replace failed (temp dir): "+err.Error(),
			updateFailureDetails(stageReplace, result.CurrentVersion, false, "not_run"), false)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// download: touches only the temp dir; failure leaves the binary intact.
	archivePath := filepath.Join(tmpDir, plan.AssetName)
	if err := updateDownloadHook(ctx, plan.AssetURL, archivePath); err != nil {
		if ctx.Err() != nil {
			interruptUpdate(stageDownload, result.CurrentVersion, false, "not_run")
		}
		failUpdate(stageDownload, result.CurrentVersion, false, "not_run", err)
	}
	checksumPath := filepath.Join(tmpDir, "checksums.txt")
	if err := updateDownloadHook(ctx, plan.ChecksumURL, checksumPath); err != nil {
		if ctx.Err() != nil {
			interruptUpdate(stageDownload, result.CurrentVersion, false, "not_run")
		}
		failUpdate(stageDownload, result.CurrentVersion, false, "not_run", err)
	}

	// verify_signature -> verify_checksum: the Sigstore signature on checksums.txt
	// is verified FIRST, then the archive checksum. A true signature/identity or
	// checksum mismatch fails closed and non-retryable (E_INTEGRITY) — a forged or
	// corrupt release is not a blip to loop on. But the network steps inside
	// verification (signature-bundle download, TUF trust-root fetch) are retryable
	// network/timeout failures, NOT integrity verdicts, and a SIGINT here must exit
	// 130 with a terminal envelope — so we check ctx first and classify by code.
	if ctx.Err() != nil {
		interruptUpdate(stageVerifySignature, result.CurrentVersion, false, "not_run")
	}
	signatureStatus, err := verifyUpdateChecksumSignature(ctx, checksumPath, plan.SignatureBundleURL, tmpDir)
	if err != nil {
		if ctx.Err() != nil {
			interruptUpdate(stageVerifySignature, result.CurrentVersion, false, "not_run")
		}
		failVerify(stageVerifySignature, result.CurrentVersion, err)
	}
	if ctx.Err() != nil {
		interruptUpdate(stageVerifyChecksum, result.CurrentVersion, false, "not_run")
	}
	if err := updateChecksumHook(archivePath, checksumPath, plan.AssetName); err != nil {
		failVerify(stageVerifyChecksum, result.CurrentVersion, err)
	}

	// replace: local extract + atomic swap. Failures here are filesystem/permission
	// problems, not transient — E_IO (exit 1) or E_FORBIDDEN (exit 4).
	binPath, err := updateExtractHook(archivePath, plan.AssetName, tmpDir)
	if err != nil {
		fail(exitGeneric, "E_IO", "update replace failed (extract): "+err.Error(),
			updateFailureDetails(stageReplace, result.CurrentVersion, false, "not_run"), false)
	}
	applied, err := updateApply(binPath, exePath)
	if err != nil {
		failBinaryReplace(result, err)
	}

	// Past the atomic swap point: the binary is now the new version.
	result.PreviousVersion = result.CurrentVersion
	result.CurrentVersion = result.TargetVersion
	result.BinaryReplaced = true
	result.SignatureStatus = signatureStatus
	result.SignatureVerified = signatureStatus == "verified"
	result.ChecksumVerified = true
	result.Path = applied.Path

	// skill_sync runs AFTER the swap. A failure here is PARTIAL SUCCESS: the
	// binary is on the new version, only the Skill is stale.
	if err := updateRunCommand(ctx, "npx", "skills", "add", updateSkillRepo, "-y", "-g"); err != nil {
		if ctx.Err() != nil {
			interruptUpdate(stageSkillSync, result.CurrentVersion, true, "failed")
		}
		failSkillSync(result, err)
	}

	result.Status = applied.Status
	result.SkillSyncStatus = "synced"
	result.Stage = stageSkillSync
	result.NextSteps = []string{
		"run auto-bug-fix changelog --since " + result.PreviousVersion + " --compact",
		"run auto-bug-fix reference --compact",
	}
	printUpdate(result)
}

func updateDryRunPreview(result updateResult) map[string]any {
	if result.InstallMethod == "npm" {
		return map[string]any{
			"changes": []map[string]any{
				{"action": "package manager update", "command": result.Command},
				{"action": "skill sync", "command": result.SkillSyncCommand},
			},
		}
	}
	return map[string]any{
		"changes": []map[string]any{
			{"action": "replace binary", "currentVersion": result.CurrentVersion, "targetVersion": result.TargetVersion},
			{"action": "skill sync", "command": result.SkillSyncCommand},
		},
		"verification": []string{"verify_signature", "verify_checksum"},
	}
}

// failUpdate emits a staged failure envelope, mapping the discover/download
// failure onto the taxonomy by its typed error so the agent classifies by
// error.code, never by the message string. The details always carry the stage
// invariant fields (stage, current_version, binary_replaced, skill_sync_status).
func failUpdate(stage, current string, binaryReplaced bool, skillSyncStatus string, err error) {
	exitCode, code, retryable := exitCodeForError(err)
	if code == "E_RUNTIME" {
		// An untyped discover/download failure is a network probe failure: retryable.
		code, exitCode, retryable = "E_NETWORK", exitNetwork, true
	}
	fail(exitCode, code, "update "+stage+" failed: "+err.Error(), updateFailureDetails(stage, current, binaryReplaced, skillSyncStatus), retryable)
}

// failVerify classifies a verify_signature/verify_checksum failure. A network
// step inside verification (signature-bundle download, TUF trust-root fetch)
// carries a typed retryable code and is surfaced as such — re-running `update` is
// the right move. Any other failure is a true integrity verdict (bad signature,
// identity mismatch, checksum mismatch): non-retryable E_INTEGRITY, do NOT retry.
func failVerify(stage, current string, err error) {
	exitCode, code, retryable := classifyVerifyError(err)
	fail(exitCode, code, "update "+stage+" failed: "+err.Error(),
		updateFailureDetails(stage, current, false, "not_run"), retryable)
}

// classifyVerifyError maps a verify-stage error onto the taxonomy: a typed
// retryable network/timeout failure (signature-bundle download, TUF trust-root
// fetch) keeps its classification; anything untyped or already E_INTEGRITY is a
// true integrity verdict — non-retryable E_INTEGRITY (exit 1).
func classifyVerifyError(err error) (int, string, bool) {
	exitCode, code, retryable := exitCodeForError(err)
	if code == "E_RUNTIME" || code == "E_INTEGRITY" {
		return exitGeneric, "E_INTEGRITY", false
	}
	return exitCode, code, retryable
}

// failReplace classifies a local npm-install failure. A permission failure is
// E_FORBIDDEN (exit 4); any other local io/disk failure is E_IO (exit 1). These
// are NON-retryable: they need an environment fix, not a blind retry. The
// install is atomic, so binary_replaced is false.
func failReplace(result updateResult, err error) {
	code := "E_IO"
	exitCode := exitGeneric
	msg := "update replace failed (npm install): " + err.Error()
	if isPermissionError(err) {
		code = "E_FORBIDDEN"
		exitCode = exitConfig
	}
	fail(exitCode, code, msg, updateFailureDetails(stageReplace, result.CurrentVersion, false, "not_started"), false)
}

// failBinaryReplace classifies a raw-binary atomic-swap failure: a permission
// error needs an environment fix (E_FORBIDDEN, exit 4), any other filesystem
// failure is E_IO (exit 1). Neither is retryable; binary_replaced is false.
func failBinaryReplace(result updateResult, err error) {
	code := "E_IO"
	exitCode := exitGeneric
	if isPermissionError(err) {
		code = "E_FORBIDDEN"
		exitCode = exitConfig
	}
	fail(exitCode, code, "update replace failed (install binary): "+err.Error(),
		updateFailureDetails(stageReplace, result.CurrentVersion, false, "not_run"), false)
}

// failSkillSync reports PARTIAL SUCCESS: the binary/package already updated
// (binary_replaced:true) but the Skill is stale. ok:false with skill_sync_command
// so the agent knows it is on the new binary and only needs to re-run the sync.
func failSkillSync(result updateResult, err error) {
	details := updateFailureDetails(stageSkillSync, result.CurrentVersion, true, "failed")
	details["previous_version"] = result.PreviousVersion
	details["skill_sync_command"] = result.SkillSyncCommand
	fail(exitNetwork, "E_NETWORK", "updated to "+result.CurrentVersion+" but Skill sync failed; run the skill_sync_command, then changelog --since "+result.PreviousVersion+": "+err.Error(), details, true)
}

// interruptUpdate emits the terminal envelope for a SIGINT/SIGTERM, stating the
// real post-state per the interrupted stage, then exits 130. Before the swap:
// "no change, still on <current>". After the swap during skill_sync: partial
// success (binary already updated, run the skill_sync_command).
func interruptUpdate(stage, current string, binaryReplaced bool, skillSyncStatus string) {
	details := updateFailureDetails(stage, current, binaryReplaced, skillSyncStatus)
	var msg string
	if binaryReplaced {
		details["skill_sync_command"] = updateSkillSyncCommand()
		msg = "update interrupted during skill_sync; already updated to " + current + " — run " + updateSkillSyncCommand() + " to finish"
	} else {
		msg = "update interrupted before any change; no change, still on " + current
	}
	fail(exitInterrupted, "E_INTERRUPTED", msg, details, true)
}

func updateFailureDetails(stage, current string, binaryReplaced bool, skillSyncStatus string) map[string]any {
	return map[string]any{
		"stage":             stage,
		"current_version":   current,
		"binary_replaced":   binaryReplaced,
		"skill_sync_status": skillSyncStatus,
	}
}

// isPermissionError reports whether a command failure was a filesystem
// permission denial, classified by the typed os.ErrPermission / EACCES / EPERM
// errno rather than by sniffing the message string.
func isPermissionError(err error) bool {
	if errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
		return true
	}
	return false
}

func buildUpdateResult(ctx context.Context, targetVersion string) (updateResult, error) {
	latest := targetVersion
	status := ""
	message := ""
	method := installMethod()
	if latest == "" {
		v, err := fetchLatestVersion(ctx, method)
		if err != nil {
			var httpErr *updateHTTPError
			if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
				latest = normalizeVersion(version)
				status = "not_published"
				message = "no published release is available; no update is available"
			} else {
				return updateResult{}, err
			}
		} else {
			latest = v
		}
	}
	current := normalizeVersion(version)
	signatureStatus := "not_applicable_package_manager"
	if method != "npm" {
		// A raw binary verifies the signature in-process during the update; until
		// then it is simply not yet checked (not "not applicable").
		signatureStatus = "not_checked"
	}
	result := updateResult{
		Status:            status,
		Message:           message,
		CurrentVersion:    current,
		LatestVersion:     latest,
		TargetVersion:     latest,
		UpdateAvailable:   compareVersions(latest, current) > 0 || (targetVersion != "" && compareVersions(latest, current) != 0),
		InstallMethod:     method,
		Command:           "npm install -g " + updatePackageName + "@" + latest,
		SkillSyncCommand:  updateSkillSyncCommand(),
		SignatureStatus:   signatureStatus,
		SignatureVerified: false,
		ChecksumVerified:  false,
	}
	return result, nil
}

// fetchLatestVersion resolves the newest published version for the install
// method: the npm registry for an npm-managed install, the GitHub latest-release
// tag for a raw binary (whose updates come from signed GitHub artifacts).
func fetchLatestVersion(ctx context.Context, method string) (string, error) {
	if method == "npm" {
		return fetchLatestNPMVersion(ctx)
	}
	rel, err := fetchUpdateRelease(ctx, "")
	if err != nil {
		return "", err
	}
	tag := normalizeVersion(rel.TagName)
	if tag == "" {
		return "", fmt.Errorf("latest release is missing tag_name")
	}
	return tag, nil
}

func fetchLatestNPMVersion(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, updateRegistryURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := updateHTTPClient.Do(req)
	if err != nil {
		// Transport-level failure (DNS, dial, TLS): a retryable network error,
		// typed here so exitCodeForError never has to read the message string.
		return "", newCodedError("E_NETWORK", exitNetwork, true, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", &updateHTTPError{StatusCode: resp.StatusCode}
	}
	var payload struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	if payload.Version == "" {
		return "", fmt.Errorf("npm registry response missing version")
	}
	return normalizeVersion(payload.Version), nil
}

type updateHTTPError struct {
	StatusCode int
	Body       string
	URL        string
}

func (e *updateHTTPError) Error() string {
	if e.URL != "" {
		msg := fmt.Sprintf("GET %s returned HTTP %d", e.URL, e.StatusCode)
		if e.Body != "" {
			msg += ": " + e.Body
		}
		return msg
	}
	return fmt.Sprintf("registry returned HTTP %d", e.StatusCode)
}

func updateStatus(result updateResult) string {
	if result.Status != "" {
		return result.Status
	}
	if result.UpdateAvailable {
		return "update_available"
	}
	return "up_to_date"
}

// installMethod detects how this binary was installed so update can route to the
// package-manager path (npm) or the signed raw-binary path. The
// AUTO_BUG_FIX_INSTALL_METHOD env var forces a method (used by tests).
func installMethod() string {
	if v := strings.TrimSpace(os.Getenv("AUTO_BUG_FIX_INSTALL_METHOD")); v != "" {
		return v
	}
	exe, _ := updateExecutable()
	return detectInstallMethod(exe)
}

// detectInstallMethod returns "npm" when the executable lives under a
// node_modules tree whose package.json matches this package, otherwise "binary".
func detectInstallMethod(exe string) string {
	exe = filepath.Clean(exe)
	if exe != "" && pathHasSegment(exe, "node_modules") && npmPackageRoot(exe) != "" {
		return "npm"
	}
	return "binary"
}

func pathHasSegment(path, segment string) bool {
	for _, part := range strings.FieldsFunc(filepath.Clean(path), func(r rune) bool {
		return r == os.PathSeparator || r == '/' || r == '\\'
	}) {
		if part == segment {
			return true
		}
	}
	return false
}

func npmPackageRoot(exe string) string {
	for dir := filepath.Dir(exe); dir != "." && dir != string(filepath.Separator); dir = filepath.Dir(dir) {
		data, err := os.ReadFile(filepath.Join(dir, "package.json"))
		if err == nil {
			var pkg struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(data, &pkg) == nil && pkg.Name == updatePackageName {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return ""
}

func updateSkillSyncCommand() string {
	return "npx skills add " + updateSkillRepo + " -y -g"
}

func updateNotices(result updateResult) []map[string]any {
	if !result.UpdateAvailable {
		return nil
	}
	return []map[string]any{{
		"type":                "update_available",
		"severity":            updateNoticeSeverity(result.CurrentVersion, result.TargetVersion),
		"current_version":     result.CurrentVersion,
		"latest_version":      result.TargetVersion,
		"install_method":      result.InstallMethod,
		"recommended_command": "auto-bug-fix update --compact",
		"release_url":         "https://github.com/" + updateSkillRepo + "/releases/tag/v" + result.TargetVersion,
		"checked_at":          time.Now().UTC().Format(time.RFC3339),
		"next_steps": []string{
			"run auto-bug-fix update --compact",
			"after update, run auto-bug-fix changelog --since " + result.CurrentVersion + " --compact",
			"refresh auto-bug-fix reference --compact",
		},
	}}
}

// updateNoticeSeverity grades the update notice from the embedded CHANGELOG
// delta between the running version and the latest, computed at check time and
// stored in the cache (CLI-SPEC §14). It returns "warning" when the delta
// contains a security entry OR the latest crosses a major version (likely
// security-relevant or breaking), and "info" otherwise.
func updateNoticeSeverity(current, latest string) string {
	if versionParts(latest)[0] > versionParts(current)[0] {
		return "warning"
	}
	for _, e := range parseChangelog(changelogMarkdown) {
		if strings.EqualFold(e.Version, "Unreleased") {
			continue
		}
		if compareVersions(e.Version, current) <= 0 {
			continue
		}
		if len(e.Changes["security"]) > 0 {
			return "warning"
		}
	}
	return "info"
}

// updateCachePath is the local, user-scoped cache that `update --check` writes
// and read-only commands (context, --help) read so they can surface an
// available update without ever touching the network.
func updateCachePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".auto-bug-fix", "update-cache.json")
}

type updateCache struct {
	CurrentVersion  string           `json:"current_version"`
	LatestVersion   string           `json:"latest_version"`
	UpdateAvailable bool             `json:"update_available"`
	CheckedAt       string           `json:"checked_at"`
	Notices         []map[string]any `json:"notices,omitempty"`
}

// writeUpdateCache persists the latest check result. Best-effort: a cache write
// failure must never fail the user's `update --check`.
func writeUpdateCache(result updateResult) {
	cache := updateCache{
		CurrentVersion:  result.CurrentVersion,
		LatestVersion:   result.LatestVersion,
		UpdateAvailable: result.UpdateAvailable,
		CheckedAt:       time.Now().UTC().Format(time.RFC3339),
		Notices:         result.Notices,
	}
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return
	}
	path := updateCachePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o600)
}

// readUpdateNotices returns the cached update_available notices, or nil when no
// update is cached. Read-only: it never refreshes from the network.
func readUpdateNotices() []map[string]any {
	data, err := os.ReadFile(updateCachePath())
	if err != nil {
		return nil
	}
	var cache updateCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil
	}
	if !cache.UpdateAvailable {
		return nil
	}
	// Version-aware: suppress a stale "update available" notice once the running
	// binary is already at (or past) the cached latest version — e.g. right after
	// a successful update. This cache carries no TTL, so without this guard the
	// notice would nag until the next active --check rewrites it.
	if cache.LatestVersion != "" && compareVersions(cache.LatestVersion, normalizeVersion(version)) <= 0 {
		return nil
	}
	return cache.Notices
}

func printUpdate(result updateResult) {
	if !wantsText() {
		printJSON(result)
		return
	}
	fmt.Printf("auto-bug-fix update: %s -> %s (%s)\n", result.CurrentVersion, result.TargetVersion, result.Status)
	if result.Command != "" {
		fmt.Println(result.Command)
	}
}

func normalizeVersion(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "refs/tags/")
	v = strings.TrimPrefix(v, "v")
	return v
}

func runCommand(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// ── signed binary-update plan + transport ─────────────────────────────────────

type updateReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type updateRelease struct {
	TagName string               `json:"tag_name"`
	HTMLURL string               `json:"html_url"`
	Assets  []updateReleaseAsset `json:"assets"`
}

type updatePlan struct {
	TargetVersion      string
	ReleaseURL         string
	AssetName          string
	AssetURL           string
	ChecksumURL        string
	SignatureBundleURL string
}

type updateApplyResult struct {
	Status string
	Path   string
}

func fetchUpdateRelease(ctx context.Context, targetVersion string) (*updateRelease, error) {
	url := updateReleaseURL(updateRepo, targetVersion)
	data, err := updateHTTPGet(ctx, url)
	if err != nil {
		return nil, err
	}
	var rel updateRelease
	if err := json.Unmarshal(data, &rel); err != nil {
		return nil, fmt.Errorf("parsing release JSON: %w", err)
	}
	return &rel, nil
}

func updateReleaseURL(repo, targetVersion string) string {
	base := strings.TrimRight(updateGitHubAPI, "/")
	if strings.TrimSpace(targetVersion) != "" {
		return base + "/repos/" + repo + "/releases/tags/" + canonicalVersionTag(targetVersion)
	}
	return base + "/repos/" + repo + "/releases/latest"
}

func canonicalVersionTag(v string) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, "v") {
		return v
	}
	return "v" + v
}

func buildUpdatePlan(rel *updateRelease, targetVersion string) (updatePlan, error) {
	if rel == nil {
		return updatePlan{}, errors.New("empty release response")
	}
	target := normalizeVersion(rel.TagName)
	if target == "" {
		target = normalizeVersion(targetVersion)
	}
	if target == "" {
		return updatePlan{}, errors.New("release is missing tag_name")
	}
	assetName, err := updateArchiveName(target)
	if err != nil {
		return updatePlan{}, err
	}
	assetURL := findUpdateAssetURL(rel.Assets, assetName)
	if assetURL == "" {
		return updatePlan{}, fmt.Errorf("release %s does not include asset %s", rel.TagName, assetName)
	}
	checksumURL := findUpdateAssetURL(rel.Assets, "checksums.txt")
	if checksumURL == "" {
		return updatePlan{}, fmt.Errorf("release %s does not include checksums.txt", rel.TagName)
	}
	signatureBundleURL := findUpdateAssetURL(rel.Assets, "checksums.txt.sigstore.json")
	return updatePlan{
		TargetVersion:      target,
		ReleaseURL:         rel.HTMLURL,
		AssetName:          assetName,
		AssetURL:           assetURL,
		ChecksumURL:        checksumURL,
		SignatureBundleURL: signatureBundleURL,
	}, nil
}

func updateArchiveName(ver string) (string, error) {
	goos, goarch := updatePlatform()
	platform, ok := map[string]string{
		"darwin":  "darwin",
		"linux":   "linux",
		"windows": "windows",
	}[goos]
	if !ok {
		return "", fmt.Errorf("unsupported update platform: %s-%s", goos, goarch)
	}
	arch, ok := map[string]string{
		"amd64": "amd64",
		"arm64": "arm64",
	}[goarch]
	if !ok {
		return "", fmt.Errorf("unsupported update platform: %s-%s", goos, goarch)
	}
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("%s-%s-%s-%s%s", updateBinaryName, normalizeVersion(ver), platform, arch, ext), nil
}

func findUpdateAssetURL(assets []updateReleaseAsset, name string) string {
	for _, asset := range assets {
		if asset.Name == name {
			return asset.BrowserDownloadURL
		}
	}
	return ""
}

func updateHTTPGet(ctx context.Context, url string) ([]byte, error) {
	req, err := newUpdateRequest(ctx, url, "application/json")
	if err != nil {
		return nil, err
	}
	resp, err := updateHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, fmt.Errorf("reading response: %w", readErr)
	}
	if resp.StatusCode >= 400 {
		// Classify by HTTP status via the taxonomy (CLI-SPEC §6): a 404 is
		// E_NOT_FOUND, 429/5xx are retryable, etc. Carry the body for the message
		// but never let an agent classify by sniffing it.
		return nil, &updateHTTPError{StatusCode: resp.StatusCode, Body: truncateForError(string(data), 200), URL: url}
	}
	return data, nil
}

func downloadUpdateFile(ctx context.Context, url, dest string) error {
	req, err := newUpdateRequest(ctx, url, "application/octet-stream")
	if err != nil {
		return err
	}
	resp, err := updateHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("executing request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(resp.Body)
		return &updateHTTPError{StatusCode: resp.StatusCode, Body: truncateForError(string(data), 200), URL: url}
	}
	tmp := dest + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func newUpdateRequest(ctx context.Context, url, accept string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "auto-bug-fix")
	if tok := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	return req, nil
}

func verifyUpdateChecksum(archivePath, checksumPath, assetName string) error {
	checksumData, err := os.ReadFile(checksumPath)
	if err != nil {
		return fmt.Errorf("reading checksums: %w", err)
	}
	expected := ""
	for _, line := range strings.Split(string(checksumData), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if filepath.Base(fields[len(fields)-1]) == assetName {
			expected = strings.ToLower(fields[0])
			break
		}
	}
	if expected == "" {
		return fmt.Errorf("checksum for %s not found", assetName)
	}
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("reading archive: %w", err)
	}
	defer func() { _ = f.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return fmt.Errorf("hashing archive: %w", err)
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if actual != expected {
		return fmt.Errorf("checksum mismatch for %s", assetName)
	}
	return nil
}

// verifyUpdateChecksumSignature enforces a mandatory, in-process Sigstore
// signature check on checksums.txt before the release is trusted. There is no
// skip path: a release without a signature bundle, or one whose signature does
// not verify against this repo's release-workflow identity, is refused. The
// returned status is always "verified" on the nil-error path.
func verifyUpdateChecksumSignature(ctx context.Context, checksumPath, bundleURL, tmpDir string) (string, error) {
	if strings.TrimSpace(bundleURL) == "" {
		// A genuinely unsigned release fails closed as an integrity failure: there
		// is no skip path. Typed E_INTEGRITY so the caller never retries it.
		return "missing", newCodedError("E_INTEGRITY", exitGeneric, false,
			errors.New("release does not include checksums.txt.sigstore.json; refusing to install an unsigned release"))
	}
	bundlePath := filepath.Join(tmpDir, "checksums.txt.sigstore.json")
	if err := updateDownloadHook(ctx, bundleURL, bundlePath); err != nil {
		// Fetching the signature bundle is a network step, not a signature verdict:
		// a transient download failure is retryable, NOT E_INTEGRITY. Reclassify the
		// raw download error (typed HTTP status or transport) via the taxonomy.
		return "download_failed", classifyNetworkFailure(fmt.Errorf("downloading checksum signature bundle: %w", err))
	}
	if err := updateVerifySignature(ctx, checksumPath, bundlePath, updateSignerIdentityRegexp()); err != nil {
		// The verifier already tags a TUF-trust-root network fetch as retryable
		// network; any other verdict is a true signature/identity failure.
		var ce *codedError
		if errors.As(err, &ce) {
			return "failed", err
		}
		return "failed", newCodedError("E_INTEGRITY", exitGeneric, false, err)
	}
	return "verified", nil
}

// classifyNetworkFailure ensures a download error carries a retryable network
// taxonomy code. A typed HTTP status (or an already-coded error) is passed
// through so statusToCode decides; a bare transport error becomes E_NETWORK.
func classifyNetworkFailure(err error) error {
	var ce *codedError
	if errors.As(err, &ce) {
		return err
	}
	var httpErr *updateHTTPError
	if errors.As(err, &httpErr) {
		return err
	}
	return newCodedError("E_NETWORK", exitNetwork, true, err)
}

func extractUpdateArchive(archivePath, assetName, tmpDir string) (string, error) {
	if strings.HasSuffix(assetName, ".zip") {
		return extractUpdateZip(archivePath, tmpDir)
	}
	if strings.HasSuffix(assetName, ".tar.gz") {
		return extractUpdateTarGz(archivePath, tmpDir)
	}
	return "", fmt.Errorf("unsupported archive type: %s", assetName)
}

func extractUpdateZip(archivePath, tmpDir string) (string, error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", err
	}
	defer func() { _ = zr.Close() }()
	want := updateArchiveBinaryName()
	for _, f := range zr.File {
		if filepath.Base(f.Name) != want {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		defer func() { _ = rc.Close() }()
		return writeExtractedUpdateBinary(tmpDir, want, rc)
	}
	return "", fmt.Errorf("%s not found in archive", want)
}

func extractUpdateTarGz(archivePath, tmpDir string) (string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	want := updateArchiveBinaryName()
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != want {
			continue
		}
		return writeExtractedUpdateBinary(tmpDir, want, tr)
	}
	return "", fmt.Errorf("%s not found in archive", want)
}

func updateArchiveBinaryName() string {
	goos, _ := updatePlatform()
	if goos == "windows" {
		return updateBinaryName + ".exe"
	}
	return updateBinaryName
}

func writeExtractedUpdateBinary(tmpDir, name string, r io.Reader) (string, error) {
	outDir := filepath.Join(tmpDir, "extracted")
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return "", err
	}
	outPath := filepath.Join(outDir, name)
	f, err := os.OpenFile(outPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o700)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return outPath, nil
}

// applyUpdateBinary atomically replaces the running executable in place using the
// rename trick — identical on Windows and Unix, no GOOS branch. It writes the new
// binary to .<base>.new, renames the in-use target out of the way to .<base>.old
// (Windows permits renaming a running .exe), moves .new into place, and removes
// .old on success (a left-behind .old on Windows, if still locked, is ignored). On
// any rename failure the original is restored from .old.
func applyUpdateBinary(src, dst string) (updateApplyResult, error) {
	target := dst
	if resolved, err := filepath.EvalSymlinks(dst); err == nil {
		target = resolved
	}
	mode := os.FileMode(0o755)
	if st, err := os.Stat(target); err == nil {
		mode = st.Mode().Perm()
		if mode&0o111 == 0 {
			mode |= 0o755
		}
	}
	dir := filepath.Dir(target)
	base := filepath.Base(target)
	newPath := filepath.Join(dir, "."+base+".new")
	backupPath := filepath.Join(dir, "."+base+".old")

	_ = os.Remove(newPath)
	if err := copyFile(src, newPath, mode); err != nil {
		_ = os.Remove(newPath)
		return updateApplyResult{}, err
	}

	_ = os.Remove(backupPath)
	if err := os.Rename(target, backupPath); err != nil {
		_ = os.Remove(newPath)
		return updateApplyResult{}, fmt.Errorf("preparing to replace %s: %w", target, err)
	}
	if err := os.Rename(newPath, target); err != nil {
		_ = os.Rename(backupPath, target)
		return updateApplyResult{}, fmt.Errorf("replacing %s: %w; original restored", target, err)
	}
	// Best-effort: on Windows the moved-aside old binary may still be locked by the
	// running process and can't be deleted yet — that's harmless, so ignore.
	_ = os.Remove(backupPath)
	return updateApplyResult{Status: "installed", Path: target}, nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
}

func truncateForError(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
