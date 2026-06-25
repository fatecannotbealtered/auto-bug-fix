package cmd

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/fatecannotbealtered/auto-bug-fix/internal/agent"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/config"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/daemon"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/doctor"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/guard"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/installer"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/notify"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/poller"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/state"
)

// version is overridden at release time via -ldflags "-X .../cmd.version=<tag>".
var version = "1.0.12"

const schemaVersion = "1.0"

var (
	commandStartedAt  time.Time
	changelogMarkdown string
)

// SetChangelog injects the root CHANGELOG.md embedded by main.
func SetChangelog(markdown string) {
	changelogMarkdown = markdown
}

type jsonEnvelope struct {
	OK            bool           `json:"ok"`
	SchemaVersion string         `json:"schema_version"`
	Data          any            `json:"data,omitempty"`
	Error         *jsonError     `json:"error,omitempty"`
	Meta          map[string]any `json:"meta"`
}

type jsonError struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Details   map[string]any `json:"details,omitempty"`
	Retryable bool           `json:"retryable"`
}

func defaultConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".auto-bug-fix", "config.json")
}

func defaultStatePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".auto-bug-fix", "state.json")
}

func defaultPIDPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".auto-bug-fix", "poller.pid")
}

func defaultLogPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".auto-bug-fix", "poller.log")
}

// pidString renders a PID as a string for JSON output, keeping IDs as strings
// per the machine contract (CLI-SPEC §12.6). An empty string means "no PID"
// (poller not running), which is unambiguous next to the `running` flag.
func pidString(pid int) string {
	if pid <= 0 {
		return ""
	}
	return strconv.Itoa(pid)
}

func Execute() {
	commandStartedAt = time.Now()
	activeOutput = outputOptions{Format: "json"}
	args := setOutputFromArgs(os.Args[1:])
	if len(args) < 1 {
		fail(exitUsage, "E_USAGE", "usage: auto-bug-fix <command>", map[string]any{"commands": commandList()}, false)
	}

	switch args[0] {
	case "help", "--help", "-h":
		printUsage()
	case "setup":
		runSetup(args[1:])
	case "start":
		runStart(args[1:])
	case "stop":
		runStop(args[1:])
	case "status":
		runStatus(args[1:])
	case "doctor":
		runDoctor(args[1:])
	case "context":
		runContext(args[1:])
	case "reference":
		runReference(args[1:])
	case "changelog":
		runChangelog(args[1:])
	case "update":
		runUpdate(args[1:])
	case "fix":
		if len(args) < 2 {
			fail(exitUsage, "E_USAGE", "usage: auto-bug-fix fix <issueKey>", nil, false)
		}
		runFix(args[1], args[2:])
	case "notify":
		runNotify(args[1:])
	case "version", "--version", "-v":
		if wantsText() {
			fmt.Println(version)
			return
		}
		printJSON(map[string]any{"version": version})
	default:
		fail(exitUsage, "E_USAGE", "unknown command: "+args[0], map[string]any{"commands": commandList()}, false)
	}
}

func commandList() []string {
	return []string{"setup", "start", "stop", "status", "doctor", "context", "reference", "changelog", "update", "fix", "notify", "version"}
}

func printUsage() {
	fmt.Println(`auto-bug-fix - autonomous bug fix tool

Usage:
  auto-bug-fix setup [--agent type] --dry-run|--confirm <token>   Create config template
  auto-bug-fix start [--detach] --dry-run|--confirm <token>       Start polling loop
  auto-bug-fix stop --dry-run|--confirm <token>                   Stop poller and child agents
  auto-bug-fix status                                             Show whether the poller is running
  auto-bug-fix doctor                                             Check config and required CLIs
  auto-bug-fix context                                            Show local paths and runtime context
  auto-bug-fix reference                                          Describe commands and machine contract
  auto-bug-fix changelog [--since vX.Y.Z]                         Show version changes
  auto-bug-fix update [--check|--dry-run] [--target-version vX.Y.Z]  Update package and Skill in one call
  auto-bug-fix fix <issueKey> --dry-run|--confirm <token>          Manually trigger a fix
  auto-bug-fix notify --issue KEY --outcome auto-fix [...]        Send the fixed Lark completion card (agent-invoked)
  auto-bug-fix version                                           Print version

Global flags:
  --format json|text|raw   Output format (default: json)
  --json                   Compatibility alias for --format json
  --compact                Compact JSON output
  --fields a,b             Project selected data fields
  --quiet                  Suppress non-error stderr output`)
}

// ── setup ────────────────────────────────────────────────────────────────────

func runSetup(args []string) {
	write, args := parseWriteArgs(args)
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	cfgPath := fs.String("config", defaultConfigPath(), "config file path")
	agentType := fs.String("agent", "", "agent type: kiro, cursor, claude-code, codex, or empty")
	if err := fs.Parse(args); err != nil {
		fail(exitUsage, "E_USAGE", "setup: "+err.Error(), nil, false)
	}
	if !validSetupAgentType(*agentType) {
		fail(exitUsage, "E_VALIDATION", "setup: --agent must be kiro, cursor, claude-code, codex, or empty", nil, false)
	}
	detail := map[string]any{"configPath": *cfgPath, "agentType": *agentType}
	if handleWriteGate(write, "setup auto-bug-fix", detail) {
		return
	}

	dir := filepath.Dir(*cfgPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		failErr("setup", err)
	}
	if _, err := os.Stat(*cfgPath); err == nil {
		installed := false
		if *agentType != "" {
			// Re-running setup propagates the current config's model into the
			// kiro agent JSON (kiro pins its model there, not via a CLI flag).
			model := ""
			if existing, lerr := config.Load(*cfgPath); lerr == nil {
				model = existing.Agent.Model
			}
			installAgentTemplate(*agentType, model)
			installed = true
		}
		printSetupResult(false, true, *cfgPath, *agentType, installed)
		return
	}

	if *agentType != "" {
		installAgentTemplate(*agentType, "")
	}

	// Write config with agentType and auto-filled command
	cfg := defaultConfig(*agentType)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		failErr("setup", err)
	}
	if err := os.WriteFile(*cfgPath, data, 0o600); err != nil {
		failErr("setup", err)
	}

	printSetupResult(true, false, *cfgPath, *agentType, *agentType != "")
}

func printSetupResult(created, alreadyExists bool, cfgPath, agentType string, installedAgentTemplate bool) {
	next := "fill in agent.command (custom agent) and poll.filter, then authenticate jira-cli and gitlab-cli"
	derivedCommand := ""
	if agentType != "" {
		next = "set agent.model (required) and poll.filter, then authenticate jira-cli and gitlab-cli"
		derivedCommand = installer.AgentCommand(agentType, "")
	}
	data := map[string]any{
		"created":                created,
		"alreadyExists":          alreadyExists,
		"configPath":             cfgPath,
		"agentType":              agentType,
		"installedAgentTemplate": installedAgentTemplate,
		"next_steps": []string{
			next,
			"run jira-cli login and gitlab-cli auth login on the poller machine",
			"run auto-bug-fix doctor --compact",
		},
	}
	if derivedCommand != "" {
		data["derivedCommand"] = derivedCommand
	}
	if !wantsText() {
		printJSON(data)
		return
	}
	if alreadyExists {
		fmt.Printf("Config already exists at %s\n", cfgPath)
		fmt.Println("Edit it directly or delete it and run setup again.")
		return
	}
	fmt.Printf("Config created at %s\n", cfgPath)
	fmt.Println("Next: " + next + " (`jira-cli login`, `gitlab-cli auth login`) and run `auto-bug-fix doctor`.")
	if derivedCommand != "" {
		fmt.Printf("Launch command is derived from agentType=%s at runtime: %s\n", agentType, derivedCommand)
	}
	if agentType == "kiro" {
		fmt.Println("Note: kiro pins its model in the agent JSON — re-run `auto-bug-fix setup --agent kiro` after setting agent.model to apply it.")
	}
}

func validSetupAgentType(agentType string) bool {
	switch agentType {
	case "", "kiro", "cursor", "claude-code", "codex":
		return true
	default:
		return false
	}
}

func installAgentTemplate(agentType, model string) {
	home, _ := os.UserHomeDir()

	switch agentType {
	case "kiro":
		if err := installer.InstallKiro(home, model); err != nil {
			failErr("setup: install kiro agent", err)
		}
		if wantsText() {
			fmt.Println("Kiro agent configured at ~/.kiro/agents/auto-bug-fix.json (prompt: auto-bug-fix.md)")
		}
	case "cursor":
		if err := installer.InstallCursor(home); err != nil {
			failErr("setup: install cursor skill", err)
		}
		if wantsText() {
			fmt.Println("Cursor skill installed at ~/.cursor/skills/auto-bug-fix/SKILL.md")
		}
	case "claude-code":
		if err := installer.InstallClaudeCode(home); err != nil {
			failErr("setup: install claude-code agent", err)
		}
		if wantsText() {
			fmt.Println("Claude Code agent installed at ~/.claude/agents/auto-bug-fix.md")
		}
	case "codex":
		if err := installer.InstallCodex(home); err != nil {
			failErr("setup: install codex AGENTS.md", err)
		}
		if wantsText() {
			fmt.Println("Codex instructions appended to ~/.codex/AGENTS.md")
		}
	}
}

func defaultConfig(agentType string) map[string]any {
	// A known agentType derives its command at runtime, so we store only the
	// type. A bare setup (no agentType) writes an empty command for the user to
	// fill — the custom escape hatch.
	agent := map[string]any{"agentType": agentType}
	if !config.KnownAgentType(agentType) {
		agent["command"] = ""
	} else {
		// Required for a known agentType — surfaced empty for the user to fill.
		agent["model"] = ""
	}
	return map[string]any{
		"agent": agent,
		"poll": map[string]any{
			"intervalSeconds": 300,
			"maxConcurrent":   config.DefaultPollMaxConcurrent,
			"stateExpiryDays": 0,
			"filter": map[string]any{
				"titleContains":   "",
				"assignedToMe":    true,
				"excludeStatuses": []string{},
			},
		},
		"workspace": map[string]any{
			"root":    config.DefaultWorkspaceRoot(),
			"cleanup": config.DefaultWorkspaceCleanup,
		},
		"knowledge": map[string]any{
			"dir":        config.DefaultKnowledgeDir,
			"read":       true,
			"update":     true,
			"handoff":    true,
			"handoffDir": config.DefaultKnowledgeHandoffDir,
		},
		"notify": map[string]any{
			"enabled": true,
			"channel": "lark",
			"target":  "",
		},
	}
}

// ── start ────────────────────────────────────────────────────────────────────

func runStart(args []string) {
	write, args := parseWriteArgs(args)
	cfgPath := defaultConfigPath()
	cfgSet := false
	detach := false
	confirmedDetachChild := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--config":
			if i+1 >= len(args) {
				fail(exitUsage, "E_USAGE", "start: --config requires a path argument", nil, false)
			}
			cfgPath = args[i+1]
			cfgSet = true
			i++
		case "--detach", "-d":
			detach = true
		case "--confirmed-detach-child":
			confirmedDetachChild = true
		}
	}

	// A detached poller must not depend on the caller's working directory, so
	// resolve an explicitly given config path to absolute before using/forwarding it.
	if cfgSet {
		if abs, err := filepath.Abs(cfgPath); err == nil {
			cfgPath = abs
		}
	}

	if !confirmedDetachChild {
		if handleWriteGate(write, "start auto-bug-fix", map[string]any{"configPath": cfgPath, "detach": detach}) {
			return
		}
	}

	// Detached start: re-spawn ourselves in the background and return immediately.
	if detach {
		preflight(cfgPath) // fail fast in the foreground; don't background a doomed poller
		bin, err := os.Executable()
		if err != nil {
			failErr("start", err)
		}
		childArgs := []string{"start"}
		if cfgSet {
			childArgs = append(childArgs, "--config", cfgPath)
		}
		childArgs = append(childArgs, "--confirmed-detach-child", "--format", "text")
		pid, already, err := daemon.StartDetached(bin, defaultPIDPath(), defaultLogPath(), childArgs)
		if err != nil {
			failErr("start", err)
		}
		if already {
			if !wantsText() {
				printJSON(map[string]any{"started": false, "alreadyRunning": true, "pid": pidString(pid), "logPath": defaultLogPath()})
				return
			}
			fmt.Printf("auto-bug-fix poller already running (PID %d)\n", pid)
			return
		}
		if !wantsText() {
			printJSON(map[string]any{"started": true, "alreadyRunning": false, "pid": pidString(pid), "logPath": defaultLogPath()})
			return
		}
		fmt.Printf("auto-bug-fix poller started (PID %d), logs at %s\n", pid, defaultLogPath())
		return
	}

	cfg := preflight(cfgPath)

	interval := time.Duration(cfg.Poll.IntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 300 * time.Second
	}

	statePath := defaultStatePath()
	st, err := state.Load(statePath)
	if err != nil {
		failErr("start", err)
	}
	// Any issue still "triggered" means a previous run was killed mid-fix; make it
	// retriable so the bug is not silently lost. Safe here: no other poller runs yet.
	if n := st.ReclaimStaleTriggered(); n > 0 {
		log.Printf("[auto-bug-fix] Reclaimed %d interrupted issue(s) from a previous run", n)
		if err := st.Save(statePath); err != nil {
			log.Printf("[auto-bug-fix] state save error after reclaim: %v", err)
		}
	}

	jira := &poller.CLIJira{}
	trigger := newAgentTrigger(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Printf("[auto-bug-fix] Starting poller (interval=%s)", interval)
	if cfg.Poll.Filter.TitleContains != "" {
		log.Printf("[auto-bug-fix] Filter: title contains %q", cfg.Poll.Filter.TitleContains)
	}
	if cfg.Poll.Filter.AssignedToMe {
		log.Printf("[auto-bug-fix] Filter: assigned to me only")
	}
	if len(cfg.Poll.Filter.ExcludeStatuses) > 0 {
		log.Printf("[auto-bug-fix] Filter: exclude statuses %v", cfg.Poll.Filter.ExcludeStatuses)
	}
	log.Printf("[auto-bug-fix] Agent command: %s", cfg.Agent.Command)
	log.Printf("[auto-bug-fix] Max concurrent fixes: %d", cfg.Poll.MaxConcurrent)
	log.Printf("[auto-bug-fix] Workspace root: %s (cleanup=%s)", cfg.Workspace.Root, cfg.Workspace.Cleanup)
	log.Printf("[auto-bug-fix] Knowledge dir: %s (read=%t update=%t handoff=%t handoffDir=%s)", cfg.Knowledge.Dir, cfg.Knowledge.Read, cfg.Knowledge.Update, cfg.Knowledge.Handoff, cfg.Knowledge.HandoffDir)
	log.Printf("[auto-bug-fix] Verify gate: enabled=%t", cfg.Verify.Enabled)
	var notifier poller.Notifier
	if cfg.Notify.Enabled {
		notifier = fallbackNotifier{cfg: cfg}
	}
	poller.Run(ctx, jira, trigger, st, cfg.Poll.Filter, interval, statePath, cfg.Poll.MaxConcurrent, cfg.Poll.StateExpiryDays, notifier)
	log.Println("[auto-bug-fix] Stopped.")
}

func agentOptions(cfg config.Config) agent.Options {
	return agent.Options{Env: map[string]string{
		"AUTO_BUG_FIX_WORKSPACE_ROOT":        cfg.Workspace.Root,
		"AUTO_BUG_FIX_WORKSPACE_CLEANUP":     cfg.Workspace.Cleanup,
		"AUTO_BUG_FIX_KNOWLEDGE_DIR":         cfg.Knowledge.Dir,
		"AUTO_BUG_FIX_KNOWLEDGE_READ":        strconv.FormatBool(cfg.Knowledge.Read),
		"AUTO_BUG_FIX_KNOWLEDGE_UPDATE":      strconv.FormatBool(cfg.Knowledge.Update),
		"AUTO_BUG_FIX_KNOWLEDGE_HANDOFF":     strconv.FormatBool(cfg.Knowledge.Handoff),
		"AUTO_BUG_FIX_KNOWLEDGE_HANDOFF_DIR": cfg.Knowledge.HandoffDir,
		"AUTO_BUG_FIX_NOTIFY_ENABLED":        strconv.FormatBool(cfg.Notify.Enabled),
		"AUTO_BUG_FIX_NOTIFY_TARGET":         cfg.Notify.Target,
	}}
}

// envPhase is the env var the agent templates read to pick which slice of the
// per-ticket workflow to run (investigate / verify / execute). Absent = legacy
// single-shot flow.
const envPhase = "AUTO_BUG_FIX_PHASE"

// agentTrigger hosts the two-phase evidence gate. Its Trigger drives
// guard.RunGuarded, and its spawn (a guard.SpawnFunc) wires per-phase command,
// marker prefixes, and env. When the gate is disabled, RunGuarded runs a single
// PhaseFull spawn — behavior identical to the pre-guard single agent.Trigger call.
type agentTrigger struct {
	command         string
	verifierCommand string
	baseEnv         map[string]string
	guardCfg        guard.Config
}

func newAgentTrigger(cfg config.Config) *agentTrigger {
	vcmd := cfg.Verify.Command
	if cfg.Verify.Enabled && strings.TrimSpace(vcmd) == "" {
		vcmd = installer.VerifierCommand(cfg.Agent.AgentType, cfg.Agent.Model)
	}
	return &agentTrigger{
		command:         cfg.Agent.Command,
		verifierCommand: vcmd,
		baseEnv:         agentOptions(cfg).Env,
		guardCfg: guard.Config{
			Enabled:       cfg.Verify.Enabled,
			WorkspaceRoot: cfg.Workspace.Root,
		},
	}
}

func (a *agentTrigger) Command() string {
	return a.command
}

func (a *agentTrigger) Trigger(issueKey string) (agent.Result, error) {
	return guard.RunGuarded(issueKey, a.guardCfg, a.spawn)
}

// spawn implements guard.SpawnFunc: it selects the command, the marker prefixes the
// live scanner kills on, and the AUTO_BUG_FIX_PHASE env for the given phase, then
// merges guard's phase-specific env (expected head, diff path, etc.).
func (a *agentTrigger) spawn(issueKey string, phase guard.Phase, extraEnv map[string]string) (agent.Result, error) {
	command := a.command
	prefixes := [][]byte{[]byte(agent.ResultMarkerPrefix)}
	env := cloneEnv(a.baseEnv)

	switch phase {
	case guard.PhaseFull:
		// legacy single-shot: no phase env, terminal RESULT marker only.
	case guard.PhaseInvestigate:
		env[envPhase] = string(guard.PhaseInvestigate)
		prefixes = [][]byte{[]byte(agent.ProposalMarkerPrefix), []byte(agent.ResultMarkerPrefix)}
	case guard.PhaseVerify:
		command = a.verifierCommand
		env[envPhase] = string(guard.PhaseVerify)
		prefixes = [][]byte{[]byte(agent.VerifyMarkerPrefix)}
	case guard.PhaseExecute:
		env[envPhase] = string(guard.PhaseExecute)
	}
	for k, v := range extraEnv {
		env[k] = v
	}
	log.Printf("[auto-bug-fix] Triggering %s phase for %s", phase, issueKey)
	return agent.Trigger(issueKey, command, agent.Options{Env: env, MarkerPrefixes: prefixes})
}

func cloneEnv(src map[string]string) map[string]string {
	dst := make(map[string]string, len(src)+4)
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// ── fix ──────────────────────────────────────────────────────────────────────

func runFix(issueKey string, args []string) {
	write, args := parseWriteArgs(args)
	cfgPath := defaultConfigPath()
	for i, a := range args {
		if a == "--config" && i+1 < len(args) {
			cfgPath = args[i+1]
		}
	}
	if handleWriteGate(write, "fix jira issue", map[string]any{"issueKey": issueKey, "configPath": cfgPath}) {
		return
	}

	cfg := preflight(cfgPath)

	log.Printf("[auto-bug-fix] Triggering fix for %s...", issueKey)
	result, err := newAgentTrigger(cfg).Trigger(issueKey)
	if result.Outcome != "" {
		log.Printf("[auto-bug-fix] Outcome: %s", result.Outcome)
	}
	if result.MRURL != "" {
		log.Printf("[auto-bug-fix] MR: %s", result.MRURL)
	}
	if result.HandoffPath != "" {
		log.Printf("[auto-bug-fix] Handoff: %s", result.HandoffPath)
	}
	if err != nil {
		failErr("fix", err)
	}
	// No marker means the agent finished but didn't run its own Step 8.5 notify,
	// so send a degraded completion card ourselves — the notification must not
	// silently depend on agent compliance. Best-effort; never fail the fix on it.
	if result.NoMarker && cfg.Notify.Enabled {
		if nerr := sendFallbackCard(cfg, issueKey, cliRunner); nerr != nil {
			log.Printf("[auto-bug-fix] WARNING fallback notification not delivered for %s: %v", issueKey, nerr)
		} else {
			log.Printf("[auto-bug-fix] fallback notification sent for %s (agent printed no marker)", issueKey)
		}
	}
	log.Printf("[auto-bug-fix] Done: %s", issueKey)
	if !wantsText() {
		printJSON(fixResultData(issueKey, result))
	}
}

// fixResultData builds the fix_result payload. outcome/mrUrl/handoffPath are
// parsed from the spawned agent's stdout marker (external/agent-controlled), so
// they are tagged via _untrusted exactly as the reference fix_result schema
// declares — keeping the self-description honest.
func fixResultData(issueKey string, result agent.Result) map[string]any {
	untrusted := []string{"outcome", "mrUrl", "handoffPath"}
	data := map[string]any{
		"issueKey":    issueKey,
		"outcome":     result.Outcome,
		"mrUrl":       result.MRURL,
		"handoffPath": result.HandoffPath,
	}
	// When the gate acted, surface its verdict + reason. verifyReason comes from
	// the verifier agent's stdout, so it is tagged _untrusted like the other
	// agent-controlled fields.
	if result.Verdict != "" {
		data["verdict"] = result.Verdict
		data["verifyReason"] = result.VerifyReason
		untrusted = append(untrusted, "verdict", "verifyReason")
	}
	data["_untrusted"] = untrusted
	return data
}

// ── notify ─────────────────────────────────────────────────────────────────────

// cliRunner runs a channel's delivery CLI (bin) and returns stdout, keeping
// stdout even on a non-zero exit so a JSON envelope (which the CLI prints on
// failure too) stays parseable.
func cliRunner(bin string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, args...).Output() //nolint:gosec
	if len(out) > 0 {
		return out, nil
	}
	return out, err
}

// sendFallbackCard sends a degraded "needs-review" completion card itself, for
// when the agent finished without printing a marker (so it almost certainly did
// not run its own Step 8.5 notify). Best-effort: the caller logs and continues —
// a notify failure must never fail a fix that already succeeded remotely.
func sendFallbackCard(cfg config.Config, issueKey string, run notify.Runner) error {
	ch, err := notify.Get(cfg.Notify.Channel)
	if err != nil {
		return err
	}
	card, err := ch.Render(notify.Params{Issue: issueKey, Outcome: notify.OutcomeNeedsReview})
	if err != nil {
		return err
	}
	recipient := strings.TrimSpace(cfg.Notify.Target)
	if recipient == "" {
		recipient = strings.TrimSpace(os.Getenv("AUTO_BUG_FIX_NOTIFY_TARGET"))
	}
	if recipient == "" {
		return fmt.Errorf("no recipient (set notify.target or $AUTO_BUG_FIX_NOTIFY_TARGET)")
	}
	var serr error
	for attempt := 1; attempt <= 2; attempt++ {
		if _, serr = ch.Send(recipient, card, run); serr == nil {
			return nil
		}
	}
	return serr
}

// fallbackNotifier adapts sendFallbackCard to poller.Notifier, so the unattended
// poll loop also sends a degraded card when an agent finishes without a marker.
type fallbackNotifier struct{ cfg config.Config }

func (f fallbackNotifier) Notify(issueKey string, _ agent.Result) {
	if err := sendFallbackCard(f.cfg, issueKey, cliRunner); err != nil {
		log.Printf("[auto-bug-fix] WARNING fallback notification not delivered for %s: %v", issueKey, err)
	} else {
		log.Printf("[auto-bug-fix] fallback notification sent for %s (agent printed no marker)", issueKey)
	}
}

// runNotify renders the fixed completion card from flat fields and sends it via
// lark-cli. The card layout is owned by internal/notify (not the agent), so the
// style is identical across runs. It is agent-invoked at Step 8.5 and is exempt
// from the confirm-token write gate (like `update`): a single best-effort send
// that the agent never lets fail the fix. `--dry-run` renders a preview without
// sending and needs no recipient.
func runNotify(args []string) {
	fs := flag.NewFlagSet("notify", flag.ContinueOnError)
	issue := fs.String("issue", "", "Jira issue key")
	outcome := fs.String("outcome", "", "auto-fix | auto-diagnose | needs-info")
	summary := fs.String("summary", "", "ticket title/summary")
	rootCause := fs.String("root-cause", "", "问题原因 (business language)")
	solution := fs.String("solution", "", "解决方案 / 诊断与建议 / 待确认问题")
	mrURL := fs.String("mr-url", "", "GitLab MR web URL (auto-fix only)")
	jiraURL := fs.String("jira-url", "", "Jira issue URL")
	to := fs.String("to", "", "recipient open_id (ou_…) or chat_id (oc_…); falls back to $AUTO_BUG_FIX_NOTIFY_TARGET")
	service := fs.String("service", "", "service/repo name (footer)")
	branch := fs.String("branch", "", "work branch (footer)")
	duration := fs.String("duration", "", "run duration (footer)")
	evidence := fs.String("evidence", "", "evidence note, e.g. Kibana logs / Archery data")
	testStatus := fs.String("test-status", "", "test status line (auto-fix)")
	channel := fs.String("channel", "", "notification channel (default: lark); falls back to $AUTO_BUG_FIX_NOTIFY_CHANNEL")
	dryRun := fs.Bool("dry-run", false, "render and preview the card without sending")
	if err := fs.Parse(args); err != nil {
		fail(exitUsage, "E_USAGE", "notify: "+err.Error(), nil, false)
	}
	if strings.TrimSpace(*issue) == "" || strings.TrimSpace(*outcome) == "" {
		fail(exitUsage, "E_VALIDATION", "notify: --issue and --outcome are required", nil, false)
	}
	if *outcome == notify.OutcomeNeedsReview {
		fail(exitUsage, "E_VALIDATION", "notify: --outcome needs-review is internal-only (the fix fallback uses it)", nil, false)
	}
	if !notify.ValidOutcome(*outcome) {
		fail(exitUsage, "E_VALIDATION", "notify: --outcome must be auto-fix, auto-diagnose, or needs-info", nil, false)
	}
	chName := strings.TrimSpace(*channel)
	if chName == "" {
		chName = strings.TrimSpace(os.Getenv("AUTO_BUG_FIX_NOTIFY_CHANNEL"))
	}
	ch, err := notify.Get(chName)
	if err != nil {
		fail(exitUsage, "E_VALIDATION", "notify: "+err.Error(), nil, false)
	}
	card, err := ch.Render(notify.Params{
		Issue: *issue, Outcome: *outcome, Summary: *summary,
		RootCause: *rootCause, Solution: *solution, MRURL: *mrURL, JiraURL: *jiraURL,
		Service: *service, Branch: *branch, Duration: *duration,
		Evidence: *evidence, TestStatus: *testStatus,
	})
	if err != nil {
		fail(exitUsage, "E_VALIDATION", "notify: "+err.Error(), nil, false)
	}

	recipient := strings.TrimSpace(*to)
	if recipient == "" {
		recipient = strings.TrimSpace(os.Getenv("AUTO_BUG_FIX_NOTIFY_TARGET"))
	}

	if *dryRun {
		var preview any
		_ = json.Unmarshal([]byte(card), &preview)
		printJSON(map[string]any{
			"wouldSend": true,
			"recipient": recipient,
			"outcome":   *outcome,
			"preview":   preview,
		})
		return
	}

	if recipient == "" {
		fail(exitUsage, "E_VALIDATION", "notify: no recipient — pass --to <open_id/chat_id> or set notify.target", nil, false)
	}
	var messageID string
	var serr error
	for attempt := 1; attempt <= 2; attempt++ {
		messageID, serr = ch.Send(recipient, card, cliRunner)
		if serr == nil {
			break
		}
		if attempt < 2 {
			log.Printf("[auto-bug-fix] notify: %s send attempt %d failed: %v — retrying once", ch.Name(), attempt, serr)
		}
	}
	if serr != nil {
		log.Printf("[auto-bug-fix] notify: WARNING %s notification NOT delivered to %s after 2 attempts: %v", ch.Name(), recipient, serr)
		fail(exitGeneric, "E_RUNTIME", "notify: send failed after 2 attempts via "+ch.Name()+": "+serr.Error(), nil, false)
	}
	printJSON(map[string]any{
		"sent":      true,
		"channel":   ch.Name(),
		"messageId": messageID,
		"recipient": recipient,
		"outcome":   *outcome,
	})
}

// ── stop / status ─────────────────────────────────────────────────────────────

func runStop(args []string) {
	write, args := parseWriteArgs(args)
	running, _, _ := daemon.Status(defaultPIDPath())
	if handleWriteGate(write, "stop auto-bug-fix", map[string]any{"pidPath": defaultPIDPath(), "wasRunning": running}) {
		return
	}
	if err := daemon.Stop(defaultPIDPath()); err != nil {
		failErr("stop", err)
	}
	_ = args
	if !wantsText() {
		printJSON(map[string]any{"stopped": true, "wasRunning": running})
		return
	}
	fmt.Println("auto-bug-fix poller stopped")
}

func runStatus(args []string) {
	_ = args
	running, pid, err := daemon.Status(defaultPIDPath())
	if err != nil {
		failErr("status", err)
	}
	if !wantsText() {
		printJSON(map[string]any{"running": running, "pid": pidString(pid), "logPath": defaultLogPath()})
		return
	}
	if running {
		fmt.Printf("auto-bug-fix poller running (PID %d), logs at %s\n", pid, defaultLogPath())
	} else {
		fmt.Println("auto-bug-fix poller not running")
	}
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// ── doctor ────────────────────────────────────────────────────────────────────

// cliProbe runs a sibling CLI and returns stdout even on non-zero exit so its
// JSON can still be parsed (used by doctor to verify sibling CLI health). A timeout keeps
// a hung CLI from blocking preflight (and thus start/fix) indefinitely.
func cliProbe(bin string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, args...).Output() //nolint:gosec
	return out, err
}

// templateProbe reports which subagent template files are missing for agentType.
func templateProbe(agentType string) ([]string, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, false // cannot resolve home — treat as unverifiable
	}
	paths := installer.ArtifactPaths(agentType, home)
	if len(paths) == 0 {
		return nil, false // empty/unknown agentType — custom command, cannot verify
	}
	var missing []string
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			missing = append(missing, p)
		}
	}
	return missing, true
}

// skillProbe reports which required/optional CLI skills are missing from the
// agent's skill directory. Mirrors templateProbe: unverifiable for a custom
// (empty/unknown) agentType.
func skillProbe(agentType string) (dir string, missingRequired, missingOptional []string, verifiable bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", nil, nil, false
	}
	dir = installer.SkillsDir(agentType, home)
	if dir == "" {
		return "", nil, nil, false
	}
	for _, s := range installer.CLISkills {
		if _, err := os.Stat(filepath.Join(dir, s.Name, "SKILL.md")); err != nil {
			if s.Required {
				missingRequired = append(missingRequired, s.Name)
			} else {
				missingOptional = append(missingOptional, s.Name)
			}
		}
	}
	return dir, missingRequired, missingOptional, true
}

// resolveAgentCommand derives agent.command from agentType when no explicit
// command is set, so a known agentType always launches the matching subagent and
// cannot drift from the installed template. An explicit command (custom escape
// hatch) is left untouched.
func resolveAgentCommand(cfg *config.Config) {
	if strings.TrimSpace(cfg.Agent.Command) == "" {
		cfg.Agent.Command = installer.AgentCommand(cfg.Agent.AgentType, cfg.Agent.Model)
	}
}

// loadConfig loads + validates config and, on success, resolves the agent
// command. The load/validate error is returned so callers (doctor) can surface
// CLI/PATH checks alongside a config failure; the command is only resolved when
// config is valid (a zero-value config has nothing meaningful to resolve).
func loadConfig(cfgPath string) (config.Config, error) {
	cfg, err := config.Load(cfgPath)
	if err == nil {
		err = config.Validate(cfg)
	}
	if err == nil {
		resolveAgentCommand(&cfg)
	}
	return cfg, err
}

// preflight loads + validates config and runs doctor checks. It aborts the
// process when any required check fails, so we never spawn an agent into a
// broken environment (missing/unusable CLI, invalid config). Returns the config.
func preflight(cfgPath string) config.Config {
	cfg, err := loadConfig(cfgPath)
	checks := doctor.Run(cfg, err, exec.LookPath, cliProbe, templateProbe, skillProbe)
	checks = append(checks, versionCheck())
	failed := doctor.HasFailure(checks)
	// Surface every non-OK check: FAIL blocks, WARN is a reminder (e.g. an
	// optional CLI missing) so a passing preflight is never silent about gaps.
	for _, c := range checks {
		if c.Level != doctor.OK {
			fmt.Fprintf(os.Stderr, "[%s] %s: %s\n", c.Level, c.Name, c.Detail)
		}
	}
	if failed {
		fail(exitConfig, "E_CONFIG", "preflight failed; fix the failed doctor checks before continuing", map[string]any{"checks": doctorJSONChecks(checks)}, false)
	}
	return cfg
}

func runDoctor(args []string) {
	cfgPath := defaultConfigPath()
	for i := 0; i < len(args); i++ {
		if args[i] == "--config" && i+1 < len(args) {
			cfgPath = args[i+1]
			i++
		}
	}

	cfg, err := loadConfig(cfgPath)

	checks := doctor.Run(cfg, err, exec.LookPath, cliProbe, templateProbe, skillProbe)
	checks = append(checks, versionCheck())
	checks = append(checks, doctor.Check{
		Name:   "release_readiness",
		Level:  doctor.Warn,
		Detail: "beta: functional and mock contract tests are expected; live Jira/GitLab/Kibana smoke evidence is not recorded",
	})
	ok := !doctor.HasFailure(checks)

	if !wantsText() {
		out := map[string]any{"checks": doctorJSONChecks(checks), "ok": ok}
		if !ok {
			printJSONEnvelope(false, nil, &jsonError{
				Code:      "E_CONFIG",
				Message:   "one or more required doctor checks failed",
				Details:   out,
				Retryable: false,
			})
			os.Exit(exitConfig)
		}
		printJSON(out)
		return
	}

	for _, c := range checks {
		fmt.Printf("[%s] %s: %s\n", c.Level, c.Name, c.Detail)
	}
	if !ok {
		fmt.Println("\nSome required checks failed. Install/authenticate the missing tools, then re-run.")
		os.Exit(exitConfig)
	}
	fmt.Println("\nAll required checks passed.")
}

func versionCheck() doctor.Check {
	return doctor.Check{
		Name:   "version",
		Level:  doctor.OK,
		Detail: "binary version " + version + " satisfies bundled Skill minimum " + version,
	}
}

type doctorJSONCheck struct {
	Check   string `json:"check"`
	Status  string `json:"status"`
	Message string `json:"message"`
	Fix     string `json:"fix,omitempty"`
	Level   string `json:"level"`
	Name    string `json:"name"`
	Detail  string `json:"detail"`
}

func doctorJSONChecks(checks []doctor.Check) []doctorJSONCheck {
	out := make([]doctorJSONCheck, 0, len(checks))
	for _, c := range checks {
		out = append(out, doctorJSONCheck{
			Check:   c.Name,
			Status:  doctorStatus(c.Level),
			Message: c.Detail,
			Fix:     doctorFix(c),
			Level:   c.Level.String(),
			Name:    c.Name,
			Detail:  c.Detail,
		})
	}
	return out
}

func doctorStatus(level doctor.Level) string {
	switch level {
	case doctor.OK:
		return "pass"
	case doctor.Info:
		return "info"
	case doctor.Warn:
		return "warn"
	default:
		return "fail"
	}
}

func doctorFix(c doctor.Check) string {
	if c.Name == "release_readiness" {
		return "Record live smoke evidence and raise functional contract coverage before declaring stable."
	}
	if c.Level != doctor.Fail {
		return ""
	}
	switch c.Name {
	case "config":
		return "Run auto-bug-fix setup or fix the config file."
	case "jira-cli":
		return "Run jira-cli doctor and authenticate with jira-cli login."
	case "gitlab-cli":
		return "Run gitlab-cli doctor and authenticate with gitlab-cli auth login."
	case "agent template":
		return "Run auto-bug-fix setup --agent <type>."
	case "cli skills":
		return "Install the required jira-cli and gitlab-cli skills into the configured agent."
	case "fix scope":
		return "Set poll.filter.titleContains or poll.filter.assignedToMe to limit the poller scope."
	default:
		return c.Detail
	}
}
