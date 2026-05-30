package config_test

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fatecannotbealtered/auto-bug-fix/internal/config"
)

func writeConfig(t *testing.T, v any) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "config*.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(f).Encode(v); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return f.Name()
}

func TestLoad_SubstitutesEnvVars(t *testing.T) {
	t.Setenv("TEST_TOKEN", "secret123")
	path := writeConfig(t, map[string]any{"poll": map[string]any{"filter": map[string]any{"titleContains": "$TEST_TOKEN"}}})
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Poll.Filter.TitleContains != "secret123" {
		t.Fatalf("got %q, want %q", cfg.Poll.Filter.TitleContains, "secret123")
	}
}

func TestLoad_WarnsOnUnresolvedEnvPlaceholder(t *testing.T) {
	// A placeholder whose variable is unset must surface a warning so the
	// operator is not misled by a downstream "field is required" error.
	const unsetVar = "AUTO_BUG_FIX_DEFINITELY_UNSET_TOKEN"
	os.Unsetenv(unsetVar)

	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	path := writeConfig(t, map[string]any{"poll": map[string]any{"filter": map[string]any{"titleContains": "$" + unsetVar}}})
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Poll.Filter.TitleContains != "" {
		t.Fatalf("unset placeholder should resolve to empty, got %q", cfg.Poll.Filter.TitleContains)
	}
	if out := buf.String(); !strings.Contains(out, unsetVar) {
		t.Fatalf("expected warning naming %q, got %q", unsetVar, out)
	}
}

func TestLoad_NoWarnWhenEnvResolved(t *testing.T) {
	t.Setenv("TEST_TOKEN_RESOLVED", "secret")

	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	path := writeConfig(t, map[string]any{"poll": map[string]any{"filter": map[string]any{"titleContains": "$TEST_TOKEN_RESOLVED"}}})
	if _, err := config.Load(path); err != nil {
		t.Fatal(err)
	}
	if out := buf.String(); strings.Contains(out, "unresolved") {
		t.Fatalf("expected no unresolved-placeholder warning, got %q", out)
	}
}

func TestLoad_AppliesDefaults(t *testing.T) {
	path := writeConfig(t, map[string]any{})
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Poll.IntervalSeconds != config.DefaultPollIntervalSeconds {
		t.Fatalf("interval default: got %d, want %d", cfg.Poll.IntervalSeconds, config.DefaultPollIntervalSeconds)
	}
	if cfg.Poll.MaxConcurrent != config.DefaultPollMaxConcurrent {
		t.Fatalf("maxConcurrent default: got %d, want %d", cfg.Poll.MaxConcurrent, config.DefaultPollMaxConcurrent)
	}
	if !cfg.Poll.Filter.AssignedToMe {
		t.Fatal("assignedToMe should default to true")
	}
	if cfg.Poll.Filter.ExcludeStatuses == nil {
		t.Fatal("excludeStatuses should default to an empty slice")
	}
	if cfg.Workspace.Root == "" {
		t.Fatal("workspace.root should default")
	}
	if cfg.Workspace.Cleanup != config.DefaultWorkspaceCleanup {
		t.Fatalf("workspace cleanup: got %q, want %q", cfg.Workspace.Cleanup, config.DefaultWorkspaceCleanup)
	}
	if cfg.Knowledge.Dir != config.DefaultKnowledgeDir {
		t.Fatalf("knowledge dir: got %q, want %q", cfg.Knowledge.Dir, config.DefaultKnowledgeDir)
	}
	if !cfg.Knowledge.Read || !cfg.Knowledge.Update || !cfg.Knowledge.Handoff {
		t.Fatalf("knowledge booleans should default to true: %+v", cfg.Knowledge)
	}
	if cfg.Knowledge.HandoffDir != config.DefaultKnowledgeHandoffDir {
		t.Fatalf("knowledge handoff dir: got %q, want %q", cfg.Knowledge.HandoffDir, config.DefaultKnowledgeHandoffDir)
	}
}

func TestLoad_PreservesExplicitAssignedToMeFalse(t *testing.T) {
	path := writeConfig(t, map[string]any{
		"poll": map[string]any{
			"filter": map[string]any{"assignedToMe": false},
		},
	})
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Poll.Filter.AssignedToMe {
		t.Fatal("explicit assignedToMe=false should be preserved")
	}
}

func TestLoad_PreservesExplicitKnowledgeFalse(t *testing.T) {
	path := writeConfig(t, map[string]any{
		"knowledge": map[string]any{
			"read":    false,
			"update":  false,
			"handoff": false,
		},
	})
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Knowledge.Read || cfg.Knowledge.Update || cfg.Knowledge.Handoff {
		t.Fatalf("explicit knowledge booleans should be preserved: %+v", cfg.Knowledge)
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := config.Load(filepath.Join(t.TempDir(), "missing.json"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLoad_InvalidJSON(t *testing.T) {
	f, _ := os.CreateTemp(t.TempDir(), "*.json")
	f.WriteString("{bad}")
	f.Close()
	_, err := config.Load(f.Name())
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestValidate_HappyPath(t *testing.T) {
	if err := config.Validate(validConfig()); err != nil {
		t.Fatal(err)
	}
}

func TestValidate_HappyPath_EmptyFilter(t *testing.T) {
	// filter is fully optional — empty filter is valid
	cfg := validConfig()
	cfg.Poll.Filter = config.FilterConfig{}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestValidate_MissingRequiredFields(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*config.Config)
	}{
		{"no agent.command", func(c *config.Config) { c.Agent.Command = "" }},
		{"blank agent.command", func(c *config.Config) { c.Agent.Command = "   " }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			tc.mutate(&cfg)
			if err := config.Validate(cfg); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
func TestValidate_KnownAgentTypeAllowsEmptyCommand(t *testing.T) {
	cfg := validConfig()
	cfg.Agent.Command = ""
	cfg.Agent.AgentType = "kiro"
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("known agentType with empty command must be valid: %v", err)
	}
}

func TestValidate_NoCommandAndNoAgentTypeFails(t *testing.T) {
	cfg := validConfig()
	cfg.Agent.Command = ""
	cfg.Agent.AgentType = ""
	if err := config.Validate(cfg); err == nil {
		t.Fatal("empty command with unknown agentType must fail")
	}
}

func TestValidate_PollInvalidInterval(t *testing.T) {
	cfg := validConfig()
	cfg.Poll.IntervalSeconds = -1
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected error for negative interval")
	}
}

func TestValidate_PollInvalidMaxConcurrent(t *testing.T) {
	cfg := validConfig()
	cfg.Poll.MaxConcurrent = -1
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected error for negative maxConcurrent")
	}
}

func TestValidate_WorkspaceCleanup(t *testing.T) {
	cfg := validConfig()
	cfg.Workspace.Cleanup = "sometimes"
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected error for invalid cleanup")
	}
}

func TestValidate_KnowledgePaths(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*config.Config)
	}{
		{"absolute dir", func(c *config.Config) {
			c.Knowledge.Dir = filepath.Join(string(filepath.Separator), "tmp", "knowledge")
		}},
		{"parent dir", func(c *config.Config) { c.Knowledge.Dir = "../knowledge" }},
		{"absolute handoff", func(c *config.Config) {
			c.Knowledge.HandoffDir = filepath.Join(string(filepath.Separator), "tmp", "handoff")
		}},
		{"parent handoff", func(c *config.Config) { c.Knowledge.HandoffDir = "../handoff" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			tc.mutate(&cfg)
			if err := config.Validate(cfg); err == nil {
				t.Fatal("expected error for invalid knowledge path")
			}
		})
	}
}

func TestValidate_FilterFields(t *testing.T) {
	cfg := validConfig()
	cfg.Poll.Filter = config.FilterConfig{
		TitleContains:   "payment",
		AssignedToMe:    true,
		ExcludeStatuses: []string{"Done", "Closed"},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("valid filter should pass: %v", err)
	}
}

func TestValidate_AgentType(t *testing.T) {
	for _, valid := range []string{"kiro", "cursor", "claude-code", "codex", ""} {
		cfg := validConfig()
		cfg.Agent.AgentType = valid
		if err := config.Validate(cfg); err != nil {
			t.Fatalf("agentType %q should be valid: %v", valid, err)
		}
	}
	cfg := validConfig()
	cfg.Agent.AgentType = "unknown"
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected error for unknown agentType")
	}
}

func validConfig() config.Config {
	return config.Config{
		Agent: config.AgentConfig{Command: `agent -p "Fix bug {issueKey}"`},
		Poll: config.PollConfig{
			IntervalSeconds: 300,
			MaxConcurrent:   config.DefaultPollMaxConcurrent,
		},
		Workspace: config.WorkspaceConfig{
			Root:    config.DefaultWorkspaceRoot(),
			Cleanup: config.DefaultWorkspaceCleanup,
		},
		Knowledge: config.KnowledgeConfig{
			Dir:        config.DefaultKnowledgeDir,
			Read:       true,
			Update:     true,
			Handoff:    true,
			HandoffDir: config.DefaultKnowledgeHandoffDir,
		},
	}
}
