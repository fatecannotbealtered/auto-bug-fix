package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	updatePackageName = "@fateforge/auto-bug-fix"
	updateSkillRepo   = "fatecannotbealtered/auto-bug-fix"
	updateRegistryURL = "https://registry.npmjs.org/%40fateforge%2Fauto-bug-fix/latest"
)

var (
	updateHTTPClient = &http.Client{Timeout: 10 * time.Second}
	updateRunCommand = runCommand
)

type updateResult struct {
	Status             string           `json:"status"`
	CurrentVersion     string           `json:"current_version"`
	LatestVersion      string           `json:"latest_version"`
	TargetVersion      string           `json:"target_version"`
	UpdateAvailable    bool             `json:"update_available"`
	InstallMethod      string           `json:"install_method"`
	RecommendedCommand string           `json:"recommended_command"`
	SkillSyncCommand   string           `json:"skill_sync_command"`
	SkillSyncStatus    string           `json:"skill_sync_status,omitempty"`
	Message            string           `json:"message,omitempty"`
	SignatureStatus    string           `json:"signature_status"`
	SignatureVerified  bool             `json:"signature_verified"`
	ChecksumVerified   bool             `json:"checksum_verified"`
	PreviousVersion    string           `json:"previous_version,omitempty"`
	NextSteps          []string         `json:"next_steps,omitempty"`
	Notices            []map[string]any `json:"notices,omitempty"`
	Preview            map[string]any   `json:"preview,omitempty"`
	Stage              string           `json:"stage,omitempty"`
	BinaryReplaced     bool             `json:"binary_replaced"`
}

// updateStage names a single step of the staged self-update so every failure
// and the success payload can report exactly how far the update got. For an
// npm-distributed tool there is no signature/checksum stage: discover -> replace
// (npm install) -> skill_sync.
const (
	stageDiscover  = "discover"
	stageReplace   = "replace"
	stageSkillSync = "skill_sync"
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

	// discover stage: resolve the latest (or requested) version.
	result, err := buildUpdateResult(targetVersion)
	if err != nil {
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
		result.Preview = map[string]any{
			"changes": []map[string]any{
				{"action": "package manager update", "command": result.RecommendedCommand},
				{"action": "skill sync", "command": result.SkillSyncCommand},
			},
		}
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

	if result.InstallMethod != "npm" {
		fail(exitConfig, "E_CONFIG", "automatic update is supported only for npm installations; run the recommended command manually", map[string]any{"recommended_command": result.RecommendedCommand}, false)
	}

	// Trap SIGINT/SIGTERM: on interrupt we still emit a terminal JSON envelope
	// describing the post-state per the stage invariant, then exit 130 — never a
	// bare killed process. The context cancels the in-flight npm/npx command.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// replace stage: npm install. Everything here leaves the installed package
	// untouched until npm commits, so binary_replaced stays false on failure.
	if err := updateRunCommand(ctx, "npm", "install", "-g", updatePackageName+"@"+result.TargetVersion); err != nil {
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

// failUpdate emits a staged failure envelope, mapping the discover/download
// failure onto the taxonomy by its typed error so the agent classifies by
// error.code, never by the message string. The details always carry the stage
// invariant fields (stage, current_version, binary_replaced, skill_sync_status).
func failUpdate(stage, current string, binaryReplaced bool, skillSyncStatus string, err error) {
	exitCode, code, retryable := exitCodeForError(err)
	if code == "E_RUNTIME" {
		// An untyped discover failure is a network probe failure: retryable.
		code, exitCode, retryable = "E_NETWORK", exitNetwork, true
	}
	fail(exitCode, code, "update "+stage+" failed: "+err.Error(), updateFailureDetails(stage, current, binaryReplaced, skillSyncStatus), retryable)
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

// failSkillSync reports PARTIAL SUCCESS: the package already updated
// (binary_replaced:true) but the Skill is stale. ok:false with skill_sync_command
// so the agent knows it is on the new binary and only needs to re-run the sync.
func failSkillSync(result updateResult, err error) {
	details := updateFailureDetails(stageSkillSync, result.CurrentVersion, true, "failed")
	details["previous_version"] = result.PreviousVersion
	details["skill_sync_command"] = result.SkillSyncCommand
	fail(exitNetwork, "E_NETWORK", "updated package to "+result.CurrentVersion+" but Skill sync failed; run the skill_sync_command, then changelog --since "+result.PreviousVersion+": "+err.Error(), details, true)
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
		msg = "update interrupted during skill_sync; package already updated to " + current + " — run " + updateSkillSyncCommand() + " to finish"
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

func buildUpdateResult(targetVersion string) (updateResult, error) {
	latest := targetVersion
	status := ""
	message := ""
	if latest == "" {
		v, err := fetchLatestNPMVersion(context.Background())
		if err != nil {
			var httpErr *updateHTTPError
			if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
				latest = normalizeVersion(version)
				status = "not_published"
				message = "npm package is not published yet; no registry update is available"
			} else {
				return updateResult{}, err
			}
		} else {
			latest = v
		}
	}
	current := normalizeVersion(version)
	result := updateResult{
		Status:             status,
		Message:            message,
		CurrentVersion:     current,
		LatestVersion:      latest,
		TargetVersion:      latest,
		UpdateAvailable:    compareVersions(latest, current) > 0 || (targetVersion != "" && compareVersions(latest, current) != 0),
		InstallMethod:      installMethod(),
		RecommendedCommand: "npm install -g " + updatePackageName + "@" + latest,
		SkillSyncCommand:   updateSkillSyncCommand(),
		SignatureStatus:    "not_applicable_package_manager",
		SignatureVerified:  false,
		ChecksumVerified:   false,
	}
	return result, nil
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
}

func (e *updateHTTPError) Error() string {
	return fmt.Sprintf("npm registry returned HTTP %d", e.StatusCode)
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

func installMethod() string {
	if v := strings.TrimSpace(os.Getenv("AUTO_BUG_FIX_INSTALL_METHOD")); v != "" {
		return v
	}
	exe, _ := os.Executable()
	if strings.Contains(strings.ToLower(exe), "node_modules") {
		return "npm"
	}
	return "npm"
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
	return cache.Notices
}

func printUpdate(result updateResult) {
	if !wantsText() {
		printJSON(result)
		return
	}
	fmt.Printf("auto-bug-fix update: %s -> %s (%s)\n", result.CurrentVersion, result.TargetVersion, result.Status)
	if result.RecommendedCommand != "" {
		fmt.Println(result.RecommendedCommand)
	}
}

func normalizeVersion(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}

func runCommand(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
