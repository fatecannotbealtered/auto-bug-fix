package agent

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const ResultMarkerPrefix = "AUTO_BUG_FIX_RESULT"

// Known outcome values an agent reports in its AUTO_BUG_FIX_RESULT marker.
// Only auto-fix means the bug was fixed; the other two mean a human now owns it.
const (
	OutcomeAutoFix      = "auto-fix"
	OutcomeAutoDiagnose = "auto-diagnose"
	OutcomeNeedsInfo    = "needs-info"
)

type Result struct {
	Outcome        string
	MRURL          string
	HandoffPath    string
	ExitCode       int
	StartedAt      time.Time
	CompletedAt    time.Time
	DurationMillis int64
}

type Options struct {
	Env map[string]string
}

// tailBuffer keeps only the last max bytes written. It is written concurrently:
// os/exec copies the child's stdout and stderr in separate goroutines, and both
// are wired to the same tailBuffer, so Write must hold the lock.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if b.max > 0 && len(b.buf) > b.max {
		b.buf = b.buf[len(b.buf)-b.max:]
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

// ParseCommand tokenizes a shell-like command string (no shell expansion).
// Supports single and double quotes; backslash escapes inside quotes.
func ParseCommand(command string) ([]string, error) {
	var tokens []string
	var cur strings.Builder
	var quote rune
	input := strings.TrimSpace(command)

	for i := 0; i < len(input); i++ {
		ch := rune(input[i])

		if quote != 0 {
			if ch == '\\' && i+1 < len(input) {
				next := rune(input[i+1])
				if next == quote || next == '\\' {
					cur.WriteRune(next)
					i++
					continue
				}
			}
			if ch == quote {
				quote = 0
				continue
			}
			cur.WriteRune(ch)
			continue
		}

		if ch == '"' || ch == '\'' {
			quote = ch
			continue
		}
		if ch == ' ' || ch == '\t' {
			if cur.Len() > 0 {
				tokens = append(tokens, cur.String())
				cur.Reset()
			}
			continue
		}
		cur.WriteRune(ch)
	}

	if quote != 0 {
		return nil, fmt.Errorf("unterminated quote in agent.command")
	}
	if cur.Len() > 0 {
		tokens = append(tokens, cur.String())
	}
	if len(tokens) == 0 {
		return nil, fmt.Errorf("agent.command is required")
	}
	return tokens, nil
}

// BuildArgs resolves command into a [bin, ...args] slice.
// {issueKey} is substituted in place; without it, issueKey is appended last.
func BuildArgs(command, issueKey string) ([]string, error) {
	if strings.Contains(command, "{issueKey}") {
		return ParseCommand(strings.ReplaceAll(command, "{issueKey}", issueKey))
	}
	tokens, err := ParseCommand(command)
	if err != nil {
		return nil, err
	}
	return append(tokens, issueKey), nil
}

func ParseResultMarker(output string) Result {
	var result Result
	lines := strings.Split(output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, ResultMarkerPrefix) {
			continue
		}
		for _, field := range strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, ResultMarkerPrefix))) {
			key, value, ok := strings.Cut(field, "=")
			if !ok {
				continue
			}
			switch key {
			case "outcome":
				result.Outcome = value
			case "mr":
				result.MRURL = value
			case "handoff":
				result.HandoffPath = value
			}
		}
		return result
	}
	return result
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// Trigger spawns the agent command for the given issueKey (shell:false).
func Trigger(issueKey, command string, options ...Options) (Result, error) {
	args, err := BuildArgs(command, issueKey)
	if err != nil {
		return Result{ExitCode: -1}, err
	}

	startedAt := time.Now()
	output := &tailBuffer{max: 64 * 1024}
	cmd := exec.Command(args[0], args[1:]...) //nolint:gosec
	if len(options) > 0 && len(options[0].Env) > 0 {
		cmd.Env = os.Environ()
		for key, value := range options[0].Env {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	cmd.Stdout = io.MultiWriter(os.Stdout, output)
	cmd.Stderr = io.MultiWriter(os.Stderr, output)
	err = cmd.Run()
	completedAt := time.Now()

	result := ParseResultMarker(output.String())
	result.ExitCode = exitCode(err)
	result.StartedAt = startedAt
	result.CompletedAt = completedAt
	result.DurationMillis = completedAt.Sub(startedAt).Milliseconds()
	return result, err
}
