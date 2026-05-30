package agents

import (
	"embed"
	"fmt"
	"path"
)

// FS contains the agent templates that must be available after go install.
//
//go:embed kiro/* cursor/* claude-code/* codex/*
var FS embed.FS

// ReadFile returns an embedded template for the given agent type.
func ReadFile(agentType, filename string) (string, error) {
	data, err := FS.ReadFile(path.Join(agentType, filename))
	if err != nil {
		return "", fmt.Errorf("read embedded agent template %s/%s: %w", agentType, filename, err)
	}
	return string(data), nil
}
