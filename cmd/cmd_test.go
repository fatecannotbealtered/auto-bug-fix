package cmd

import (
	"strings"
	"testing"

	"github.com/fatecannotbealtered/auto-bug-fix/internal/config"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/installer"
)

func TestHasFlag(t *testing.T) {
	if !hasFlag([]string{"--detach", "--json"}, "--json") {
		t.Error("expected --json to be found")
	}
	if hasFlag([]string{"--detach"}, "--json") {
		t.Error("did not expect --json")
	}
	if hasFlag(nil, "--json") {
		t.Error("nil args should not match")
	}
}

func TestResolveAgentCommand_DerivesForKnownType(t *testing.T) {
	cfg := config.Config{Agent: config.AgentConfig{AgentType: "kiro"}}
	resolveAgentCommand(&cfg)
	if cfg.Agent.Command != installer.AgentCommand("kiro", "") {
		t.Errorf("expected derived command, got %q", cfg.Agent.Command)
	}
}

func TestResolveAgentCommand_InjectsModelForFlagAgent(t *testing.T) {
	cfg := config.Config{Agent: config.AgentConfig{AgentType: "cursor", Model: "sonnet-4.5"}}
	resolveAgentCommand(&cfg)
	if !strings.Contains(cfg.Agent.Command, `--model "sonnet-4.5"`) {
		t.Errorf("expected model flag in derived command, got %q", cfg.Agent.Command)
	}
}

func TestResolveAgentCommand_KeepsExplicitCommand(t *testing.T) {
	cfg := config.Config{Agent: config.AgentConfig{AgentType: "kiro", Command: "my-agent {issueKey}"}}
	resolveAgentCommand(&cfg)
	if cfg.Agent.Command != "my-agent {issueKey}" {
		t.Errorf("explicit command must be preserved, got %q", cfg.Agent.Command)
	}
}

func TestResolveAgentCommand_EmptyForUnknownType(t *testing.T) {
	cfg := config.Config{Agent: config.AgentConfig{AgentType: ""}}
	resolveAgentCommand(&cfg)
	if cfg.Agent.Command != "" {
		t.Errorf("unknown agentType with no command should stay empty, got %q", cfg.Agent.Command)
	}
}
