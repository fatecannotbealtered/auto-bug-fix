package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	ConfirmToken       string           `json:"confirm_token,omitempty"`
	ExpiresAt          string           `json:"expires_at,omitempty"`
}

func runUpdate(args []string) {
	write, args := parseWriteArgs(args)
	checkOnly := false
	targetVersion := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--check":
			checkOnly = true
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

	result, err := buildUpdateResult(targetVersion)
	if err != nil {
		failErr("update check failed", err)
	}
	if checkOnly {
		result.Status = updateStatus(result)
		result.Notices = updateNotices(result)
		writeUpdateCache(result)
		printUpdate(result)
		return
	}

	detail := map[string]any{
		"currentVersion":   result.CurrentVersion,
		"targetVersion":    result.TargetVersion,
		"installMethod":    result.InstallMethod,
		"recommended":      result.RecommendedCommand,
		"skillSyncCommand": result.SkillSyncCommand,
	}
	if write.DryRun {
		expires := time.Now().UTC().Add(confirmTTL)
		token, err := generateConfirmToken("update auto-bug-fix", detail, expires)
		if err != nil {
			failErr("update", err)
		}
		result.Status = "dry_run"
		result.Preview = map[string]any{
			"changes": []map[string]any{
				{"action": "package manager update", "command": result.RecommendedCommand},
				{"action": "skill sync", "command": result.SkillSyncCommand},
			},
		}
		result.ConfirmToken = token
		result.ExpiresAt = expires.Format(time.RFC3339)
		printUpdate(result)
		return
	}
	if write.Confirm == "" {
		fail(exitConfirmNeeded, "E_CONFIRMATION_REQUIRED", "update requires --dry-run followed by --confirm <confirm_token>", nil, false)
	}
	if err := validateConfirmToken("update auto-bug-fix", detail, write.Confirm, time.Now().UTC()); err != nil {
		fail(exitConflict, "E_CONFLICT", err.Error(), nil, false)
	}
	if result.InstallMethod != "npm" {
		fail(exitConfig, "E_CONFIG", "automatic update is supported only for npm installations; run the recommended command manually", map[string]any{"recommended_command": result.RecommendedCommand}, false)
	}
	if err := consumeConfirmToken(write.Confirm); err != nil && !activeOutput.Quiet {
		fmt.Fprintf(os.Stderr, "warning: could not record consumed confirm token: %v\n", err)
	}
	if err := updateRunCommand(context.Background(), "npm", "install", "-g", updatePackageName+"@"+result.TargetVersion); err != nil {
		fail(exitNetwork, "E_NETWORK", "npm update failed: "+err.Error(), nil, true)
	}
	skillStatus := "synced"
	if err := updateRunCommand(context.Background(), "npx", "skills", "add", updateSkillRepo, "-y", "-g"); err != nil {
		skillStatus = "failed"
		result.SkillSyncStatus = skillStatus
		fail(exitGeneric, "E_RUNTIME", "updated package but failed to sync Skill: "+err.Error(), map[string]any{"skill_sync_command": result.SkillSyncCommand}, false)
	}
	result.Status = "updated"
	result.PreviousVersion = result.CurrentVersion
	result.CurrentVersion = result.TargetVersion
	result.SkillSyncStatus = skillStatus
	result.NextSteps = []string{
		"run auto-bug-fix changelog --since " + result.PreviousVersion + " --compact",
		"run auto-bug-fix reference --compact",
	}
	printUpdate(result)
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
		"recommended_command": "auto-bug-fix update --dry-run --compact",
		"release_url":         "https://github.com/" + updateSkillRepo + "/releases/tag/v" + result.TargetVersion,
		"checked_at":          time.Now().UTC().Format(time.RFC3339),
		"next_steps": []string{
			"ask the user before confirming local self-update",
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
