package cmd

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/fatecannotbealtered/auto-bug-fix/internal/config"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/daemon"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/state"
)

func flagValue(args []string, name string) string {
	prefix := name + "="
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, prefix) {
			return strings.TrimPrefix(a, prefix)
		}
	}
	return ""
}

// cachedNotices returns the update notices from the local cache, always as a
// non-nil slice so the context_result `notices` field is present in the output
// it declares (empty when no update is cached).
func cachedNotices() []map[string]any {
	notices := readUpdateNotices()
	if notices == nil {
		return []map[string]any{}
	}
	return notices
}

func runContext(args []string) {
	cfgPath := defaultConfigPath()
	if v := flagValue(args, "--config"); v != "" {
		cfgPath = v
	}
	cfg, loadErr := config.Load(cfgPath)
	valid := false
	resolvedCommand := ""
	if loadErr == nil {
		valid = config.Validate(cfg) == nil
		if valid {
			resolveAgentCommand(&cfg)
			resolvedCommand = cfg.Agent.Command
		}
	}
	running, pid, _ := daemon.Status(defaultPIDPath())
	data := map[string]any{
		"version": version,
		"paths": map[string]string{
			"config": cfgPath,
			"state":  defaultStatePath(),
			"pid":    defaultPIDPath(),
			"log":    defaultLogPath(),
		},
		"config": map[string]any{
			"exists":    loadErr == nil,
			"valid":     valid,
			"agentType": cfg.Agent.AgentType,
		},
		"poller": map[string]any{
			"running": running,
			"pid":     pidString(pid),
		},
		"credentials": map[string]any{
			"stored": false,
			"source": "sibling CLIs (jira-cli/gitlab-cli/kibana-cli)",
		},
		// notices is read from the local update cache only (written by
		// `update --check`); context never refreshes over the network.
		"notices":    cachedNotices(),
		"_untrusted": []string{},
	}
	if resolvedCommand != "" {
		data["agent"] = map[string]any{"command": state.RedactCommand(resolvedCommand)}
	}
	if loadErr != nil {
		data["config"].(map[string]any)["error"] = loadErr.Error()
	}
	if !wantsText() {
		printJSON(data)
		return
	}
	fmt.Printf("auto-bug-fix context (version %s)\n", version)
	fmt.Printf("config: %s (valid=%t)\n", cfgPath, valid)
	if running {
		fmt.Printf("poller: running (PID %d)\n", pid)
	} else {
		fmt.Println("poller: not running")
	}
}

func runReference(args []string) {
	data := map[string]any{
		"tool":        "auto-bug-fix",
		"version":     version,
		"risk_tier":   "T1",
		"description": "Deterministic scheduler that polls Jira Bugs and dispatches each issue to a configured AI agent.",
		"machine_contract": map[string]any{
			"schema_version": schemaVersion,
			"default_format": "json",
			"json_envelope":  true,
			"stdout":         "json mode emits one JSON envelope on stdout; logs and warnings go to stderr",
			"global_flags":   []string{"--format json|text|raw", "--json", "--compact", "--fields <a,b>", "--dry-run", "--confirm <token>", "--quiet"},
		},
		"release_readiness": releaseReadiness(),
		"commands":          referenceCommands(),
		"schemas":           referenceSchemas(),
		"exit_codes": map[string]string{
			"0": "success",
			"1": "generic runtime error or integrity failure",
			"2": "usage or validation error",
			"3": "resource not found",
			"4": "configuration, auth, or permission failure",
			"5": "confirmation required",
			"6": "confirmation token expired, mismatched, or already consumed",
			"7": "retryable network/server failure",
			"8": "timeout",
		},
		"error_codes": []map[string]any{
			{"code": "E_USAGE", "exit_code": 2, "retryable": false},
			{"code": "E_VALIDATION", "exit_code": 2, "retryable": false},
			{"code": "E_NOT_FOUND", "exit_code": 3, "retryable": false},
			{"code": "E_CONFIG", "exit_code": 4, "retryable": false},
			{"code": "E_CONFIRMATION_REQUIRED", "exit_code": 5, "retryable": false},
			{"code": "E_CONFLICT", "exit_code": 6, "retryable": false},
			{"code": "E_NETWORK", "exit_code": 7, "retryable": true},
			{"code": "E_TIMEOUT", "exit_code": 8, "retryable": true},
			{"code": "E_RUNTIME", "exit_code": 1, "retryable": false},
		},
	}
	if !wantsText() {
		printJSON(data)
		return
	}
	fmt.Println("auto-bug-fix reference")
	fmt.Println("commands: setup, doctor, context, reference, changelog, start, stop, status, fix, version")
}

func releaseReadiness() map[string]any {
	return map[string]any{
		"level":                          "beta",
		"fcc_required":                   true,
		"fcc_status":                     "verified",
		"mock_upstream_required":         true,
		"mock_upstream_status":           "verified",
		"live_smoke_required_for_stable": true,
		"live_smoke_status":              "missing",
		"reason":                         "Stable requires recorded live smoke/E2E evidence for Jira/GitLab/Kibana integration.",
		"required_evidence": []string{
			"functional_contract_coverage_100",
			"mock_upstream_contract_tests",
			"recorded_live_smoke_for_stable",
		},
	}
}

func referenceCommands() []map[string]any {
	return []map[string]any{
		refCmd("setup", "write", "write-local", "Create config and install an agent template", "setup_result",
			[]map[string]any{
				param("agent", "string", false, "kiro|cursor|claude-code|codex"),
				param("config", "path", false, "config file path"),
			},
			[]string{"auto-bug-fix setup --agent codex --dry-run --compact", "auto-bug-fix setup --agent codex --confirm <confirm_token> --compact"}),
		refCmd("start", "write", "write-local/process", "Start the foreground or detached poller", "start_result",
			[]map[string]any{
				param("detach", "boolean", false, "start in background"),
				param("config", "path", false, "config file path"),
			},
			[]string{"auto-bug-fix start --detach --dry-run --compact", "auto-bug-fix start --detach --confirm <confirm_token> --compact"}),
		refCmd("stop", "write", "write-local/process", "Stop the background poller and child agents", "stop_result",
			nil,
			[]string{"auto-bug-fix stop --dry-run --compact", "auto-bug-fix stop --confirm <confirm_token> --compact"}),
		refCmd("status", "read", "read", "Show detached poller status", "status_result", nil,
			[]string{"auto-bug-fix status --compact"}),
		refCmd("fix <issueKey>", "write", "external-write-via-agent", "Run one configured agent for a Jira issue", "fix_result",
			[]map[string]any{
				param("issueKey", "string", true, "Jira issue key"),
				param("config", "path", false, "config file path"),
			},
			[]string{"auto-bug-fix fix PROJ-123 --dry-run --compact", "auto-bug-fix fix PROJ-123 --confirm <confirm_token> --compact"}),
		refCmd("doctor", "read", "read", "Check config, sibling CLIs, agent template, skills, scope, and release readiness", "doctor_result", nil,
			[]string{"auto-bug-fix doctor --compact"}),
		refCmd("context", "read", "read", "Show local paths, config validity, version, credentials source, and poller status", "context_result", nil,
			[]string{"auto-bug-fix context --compact"}),
		refCmd("reference", "read", "read", "Describe commands, params, schemas, error codes, and release readiness", "reference_result", nil,
			[]string{"auto-bug-fix reference --compact"}),
		refCmd("changelog", "read", "read", "Show changes embedded from CHANGELOG.md", "changelog_result",
			[]map[string]any{param("since", "semver", false, "only entries newer than this version")},
			[]string{"auto-bug-fix changelog --since 1.0.4 --compact"}),
		refCmd("update", "write", "write-local/update", "Check, preview, or execute binary/package update and Skill sync", "update_result",
			[]map[string]any{
				param("check", "boolean", false, "read-only update check"),
				param("target-version", "semver", false, "specific version"),
			},
			[]string{"auto-bug-fix update --check --compact", "auto-bug-fix update --dry-run --compact", "auto-bug-fix update --confirm <confirm_token> --compact"}),
		refCmd("version", "read", "read", "Print the current tool version", "version_result", nil,
			[]string{"auto-bug-fix --version --compact"}),
	}
}

func refCmd(path, typ, permission, summary, schema string, params []map[string]any, examples []string) map[string]any {
	if params == nil {
		params = []map[string]any{}
	}
	return map[string]any{
		"path":            path,
		"type":            typ,
		"permission":      permission,
		"permission_tier": permission,
		"description":     summary,
		"summary":         summary,
		"params":          params,
		"output_schema":   schema,
		"examples":        examples,
	}
}

func param(name, typ string, required bool, description string) map[string]any {
	return map[string]any{"name": name, "type": typ, "required": required, "multiple": false, "description": description}
}

func referenceSchemas() map[string]any {
	return map[string]any{
		"setup_result":     schema("object", []string{"created", "alreadyExists", "configPath", "agentType", "installedAgentTemplate", "derivedCommand", "next_steps"}, nil),
		"start_result":     schema("object", []string{"started", "alreadyRunning", "pid", "logPath"}, nil),
		"stop_result":      schema("object", []string{"stopped", "wasRunning"}, nil),
		"status_result":    schema("object", []string{"running", "pid", "logPath"}, nil),
		"fix_result":       schema("object", []string{"issueKey", "outcome", "mrUrl", "handoffPath", "_untrusted"}, []string{"outcome", "mrUrl", "handoffPath"}),
		"doctor_result":    schema("object", []string{"ok", "checks"}, nil),
		"context_result":   schema("object", []string{"version", "paths", "config", "poller", "credentials", "agent", "notices", "_untrusted"}, nil),
		"reference_result": schema("object", []string{"tool", "version", "risk_tier", "machine_contract", "release_readiness", "commands", "schemas", "exit_codes", "error_codes"}, nil),
		"changelog_result": schema("object", []string{"current_version", "since", "entries"}, nil),
		"update_result":    schema("object", []string{"status", "message", "current_version", "latest_version", "target_version", "update_available", "install_method", "recommended_command", "skill_sync_command", "skill_sync_status", "signature_status", "signature_verified", "checksum_verified", "preview", "confirm_token", "expires_at", "previous_version", "next_steps", "notices"}, nil),
		"version_result":   schema("object", []string{"version"}, nil),
	}
}

func schema(shape string, fields, untrusted []string) map[string]any {
	if untrusted == nil {
		untrusted = []string{}
	}
	return map[string]any{"shape": shape, "fields": fields, "untrusted_fields": untrusted}
}

type changelogEntry struct {
	Version string              `json:"version"`
	Date    string              `json:"date,omitempty"`
	Changes map[string][]string `json:"changes"`
}

var changelogHeadingRe = regexp.MustCompile(`^## \[([^\]]+)\](?: - ([0-9]{4}-[0-9]{2}-[0-9]{2}))?`)

func runChangelog(args []string) {
	since := strings.TrimPrefix(flagValue(args, "--since"), "v")
	entries := parseChangelog(changelogMarkdown)
	if since != "" {
		filtered := entries[:0]
		for _, e := range entries {
			if strings.EqualFold(e.Version, "Unreleased") {
				continue
			}
			if compareVersions(e.Version, since) > 0 {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}
	data := map[string]any{
		"current_version": strings.TrimPrefix(version, "v"),
		"since":           since,
		"entries":         entries,
	}
	if !wantsText() {
		printJSON(data)
		return
	}
	fmt.Printf("auto-bug-fix changelog (current %s)\n\n", version)
	for _, e := range entries {
		if e.Date != "" {
			fmt.Printf("## %s - %s\n", e.Version, e.Date)
		} else {
			fmt.Printf("## %s\n", e.Version)
		}
		for _, cat := range []string{"added", "changed", "fixed", "deprecated", "removed", "security"} {
			for _, item := range e.Changes[cat] {
				fmt.Printf("- %s: %s\n", cat, item)
			}
		}
		fmt.Println()
	}
}

func parseChangelog(markdown string) []changelogEntry {
	var entries []changelogEntry
	var current *changelogEntry
	category := ""
	for _, raw := range strings.Split(markdown, "\n") {
		line := strings.TrimSpace(raw)
		if m := changelogHeadingRe.FindStringSubmatch(line); m != nil {
			if current != nil {
				entries = append(entries, *current)
			}
			current = &changelogEntry{
				Version: strings.TrimPrefix(m[1], "v"),
				Changes: map[string][]string{
					"added":      {},
					"changed":    {},
					"fixed":      {},
					"deprecated": {},
					"removed":    {},
					"security":   {},
				},
			}
			if len(m) > 2 {
				current.Date = m[2]
			}
			category = ""
			continue
		}
		if current == nil {
			continue
		}
		if strings.HasPrefix(line, "### ") {
			category = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, "### ")))
			if _, ok := current.Changes[category]; !ok {
				current.Changes[category] = []string{}
			}
			continue
		}
		if category == "" || !strings.HasPrefix(line, "- ") {
			continue
		}
		current.Changes[category] = append(current.Changes[category], strings.TrimSpace(strings.TrimPrefix(line, "- ")))
	}
	if current != nil {
		entries = append(entries, *current)
	}
	return entries
}

func compareVersions(a, b string) int {
	ap := versionParts(a)
	bp := versionParts(b)
	for i := 0; i < 3; i++ {
		if ap[i] > bp[i] {
			return 1
		}
		if ap[i] < bp[i] {
			return -1
		}
	}
	return 0
}

func versionParts(v string) [3]int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	parts := strings.Split(v, ".")
	var out [3]int
	for i := 0; i < len(parts) && i < 3; i++ {
		n, _ := strconv.Atoi(parts[i])
		out[i] = n
	}
	return out
}
