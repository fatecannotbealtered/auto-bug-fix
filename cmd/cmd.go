package cmd

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/fatecannotbealtered/auto-bug-fix/internal/agent"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/config"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/daemon"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/installer"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/poller"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/state"
)

// version is overridden at release time via -ldflags "-X .../cmd.version=<tag>".
var version = "1.0.0"

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

func Execute() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "setup":
		runSetup(os.Args[2:])
	case "start":
		runStart(os.Args[2:])
	case "stop":
		runStop(os.Args[2:])
	case "status":
		runStatus(os.Args[2:])
	case "fix":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: auto-bug-fix fix <issueKey>")
			os.Exit(1)
		}
		runFix(os.Args[2], os.Args[3:])
	case "version", "--version", "-v":
		fmt.Println(version)
	default:
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`auto-bug-fix - autonomous bug fix tool

Usage:
  auto-bug-fix setup [--agent type]   Create config template
  auto-bug-fix start [--detach]       Start polling loop (--detach runs in background)
  auto-bug-fix stop                   Stop the background poller and its child agents
  auto-bug-fix status                 Show whether the background poller is running
  auto-bug-fix fix <issueKey>         Manually trigger a fix
  auto-bug-fix version                Print version`)
}

// ── setup ────────────────────────────────────────────────────────────────────

func runSetup(args []string) {
	fs := flag.NewFlagSet("setup", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath(), "config file path")
	agentType := fs.String("agent", "", "agent type: kiro, cursor, claude-code, codex, or empty")
	if err := fs.Parse(args); err != nil {
		log.Fatalf("setup: %v", err)
	}
	if !validSetupAgentType(*agentType) {
		log.Fatalf("setup: --agent must be kiro, cursor, claude-code, codex, or empty")
	}

	dir := filepath.Dir(*cfgPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Fatalf("setup: %v", err)
	}
	if _, err := os.Stat(*cfgPath); err == nil {
		fmt.Printf("Config already exists at %s\n", *cfgPath)
		if *agentType != "" {
			installAgentTemplate(*agentType)
		}
		fmt.Println("Edit it directly or delete it and run setup again.")
		return
	}

	if *agentType != "" {
		installAgentTemplate(*agentType)
	}

	// Write config with agentType and auto-filled command
	cfg := defaultConfig(*agentType)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		log.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(*cfgPath, data, 0o600); err != nil {
		log.Fatalf("setup: %v", err)
	}

	fmt.Printf("Config created at %s\n", *cfgPath)
	if *agentType == "" {
		fmt.Println("Next: fill in agent.command, jira.host, gitlab.host, and set environment variables for tokens.")
	} else {
		fmt.Println("Next: fill in jira.host, gitlab.host, and set environment variables for tokens.")
		fmt.Printf("Agent command pre-filled: %s\n", installer.AgentCommand(*agentType))
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

func installAgentTemplate(agentType string) {
	home, _ := os.UserHomeDir()

	switch agentType {
	case "kiro":
		if err := installer.InstallKiro(home); err != nil {
			log.Fatalf("setup: install kiro agent: %v", err)
		}
		fmt.Println("Kiro agent configured at ~/.kiro/agents/auto-bug-fix.json")
		fmt.Println("Skill installed at ~/.kiro/skills/auto-bug-fix/SKILL.md")
	case "cursor":
		if err := installer.InstallCursor(home); err != nil {
			log.Fatalf("setup: install cursor rule: %v", err)
		}
		fmt.Println("Cursor rule installed at ~/.cursor/rules/auto-bug-fix.mdc")
	case "claude-code":
		if err := installer.InstallClaudeCode(home); err != nil {
			log.Fatalf("setup: install claude-code agent: %v", err)
		}
		fmt.Println("Claude Code agent installed at ~/.claude/agents/auto-bug-fix.md")
	case "codex":
		if err := installer.InstallCodex(home); err != nil {
			log.Fatalf("setup: install codex AGENTS.md: %v", err)
		}
		fmt.Println("Codex instructions appended to ~/.codex/AGENTS.md")
	}
}

func defaultConfig(agentType string) map[string]any {
	return map[string]any{
		"agent": map[string]any{
			"agentType": agentType,
			"command":   installer.AgentCommand(agentType),
		},
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
		"jira":   map[string]any{"host": "https://jira.example.com", "token": "$JIRA_TOKEN"},
		"gitlab": map[string]any{"host": "https://gitlab.example.com", "token": "$GITLAB_TOKEN"},
		"kibana": map[string]any{"host": "$KIBANA_HOST", "user": "$KIBANA_USER", "password": "$KIBANA_PASSWORD"},
	}
}

// ── start ────────────────────────────────────────────────────────────────────

func runStart(args []string) {
	cfgPath := defaultConfigPath()
	cfgSet := false
	detach := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--config":
			if i+1 >= len(args) {
				log.Fatalf("start: --config requires a path argument")
			}
			cfgPath = args[i+1]
			cfgSet = true
			i++
		case "--detach", "-d":
			detach = true
		}
	}

	// A detached poller must not depend on the caller's working directory, so
	// resolve an explicitly given config path to absolute before using/forwarding it.
	if cfgSet {
		if abs, err := filepath.Abs(cfgPath); err == nil {
			cfgPath = abs
		}
	}

	// Detached start: re-spawn ourselves in the background and return immediately.
	if detach {
		bin, err := os.Executable()
		if err != nil {
			log.Fatalf("start: %v", err)
		}
		childArgs := []string{"start"}
		if cfgSet {
			childArgs = append(childArgs, "--config", cfgPath)
		}
		pid, already, err := daemon.StartDetached(bin, defaultPIDPath(), defaultLogPath(), childArgs)
		if err != nil {
			log.Fatalf("start: %v", err)
		}
		if already {
			fmt.Printf("auto-bug-fix poller already running (PID %d)\n", pid)
			return
		}
		fmt.Printf("auto-bug-fix poller started (PID %d), logs at %s\n", pid, defaultLogPath())
		return
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("start: %v", err)
	}
	if err := config.Validate(cfg); err != nil {
		log.Fatalf("start: invalid config: %v", err)
	}

	interval := time.Duration(cfg.Poll.IntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 300 * time.Second
	}

	statePath := defaultStatePath()
	st, err := state.Load(statePath)
	if err != nil {
		log.Fatalf("start: %v", err)
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
	trigger := &agentTrigger{command: cfg.Agent.Command, options: agentOptions(cfg)}

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
	poller.Run(ctx, jira, trigger, st, cfg.Poll.Filter, interval, statePath, cfg.Poll.MaxConcurrent, cfg.Poll.StateExpiryDays)
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
	}}
}

type agentTrigger struct {
	command string
	options agent.Options
}

func (a *agentTrigger) Command() string {
	return a.command
}

func (a *agentTrigger) Trigger(issueKey string) (agent.Result, error) {
	log.Printf("[auto-bug-fix] Triggering fix for %s", issueKey)
	return agent.Trigger(issueKey, a.command, a.options)
}

// ── fix ──────────────────────────────────────────────────────────────────────

func runFix(issueKey string, args []string) {
	cfgPath := defaultConfigPath()
	for i, a := range args {
		if a == "--config" && i+1 < len(args) {
			cfgPath = args[i+1]
		}
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("fix: %v", err)
	}
	if err := config.Validate(cfg); err != nil {
		log.Fatalf("fix: invalid config: %v", err)
	}

	log.Printf("[auto-bug-fix] Triggering fix for %s...", issueKey)
	result, err := agent.Trigger(issueKey, cfg.Agent.Command, agentOptions(cfg))
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
		log.Fatalf("fix: %v", err)
	}
	log.Printf("[auto-bug-fix] Done: %s", issueKey)
}

// ── stop / status ─────────────────────────────────────────────────────────────

func runStop(_ []string) {
	if err := daemon.Stop(defaultPIDPath()); err != nil {
		log.Fatalf("stop: %v", err)
	}
	fmt.Println("auto-bug-fix poller stopped")
}

func runStatus(_ []string) {
	running, pid, err := daemon.Status(defaultPIDPath())
	if err != nil {
		log.Fatalf("status: %v", err)
	}
	if running {
		fmt.Printf("auto-bug-fix poller running (PID %d), logs at %s\n", pid, defaultLogPath())
	} else {
		fmt.Println("auto-bug-fix poller not running")
	}
}
