package agent

import (
	"bytes"
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
	// WaitDelay overrides the post-exit pipe-close delay (default 10s). Mainly for tests.
	WaitDelay time.Duration
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

var markerPrefixBytes = []byte(ResultMarkerPrefix)

// markerScanner watches the live output stream for the agent's completion marker
// and invokes onMarker once when a line beginning with it is seen. Detection is
// incremental (newline-delimited, prefix compare), so it adds negligible cost
// over the byte copy os/exec already performs. It is written concurrently from
// the stdout and stderr copy goroutines, so all state is mutex-guarded.
type markerScanner struct {
	mu       sync.Mutex
	pending  []byte
	seen     bool
	onMarker func()
}

func (s *markerScanner) Write(p []byte) (int, error) {
	s.mu.Lock()
	trigger := false
	if !s.seen {
		s.pending = append(s.pending, p...)
		for {
			i := bytes.IndexByte(s.pending, '\n')
			if i < 0 {
				break
			}
			line := bytes.TrimSpace(s.pending[:i])
			s.pending = s.pending[i+1:]
			// Match a real terminal marker only. The templates document the marker
			// with `<...>` placeholders (e.g. mr=<MR_URL>); a verbose agent that
			// echoes that example must not trigger a premature kill. Real markers
			// carry concrete URLs/paths and never contain angle-bracket placeholders.
			if bytes.HasPrefix(line, markerPrefixBytes) && !bytes.Contains(line, []byte("<")) {
				s.seen, trigger = true, true
				break
			}
		}
		// Bound the partial-line buffer so a long line with no newline can't grow it unbounded.
		if !s.seen && len(s.pending) > 64*1024 {
			s.pending = s.pending[len(s.pending)-64*1024:]
		}
	}
	cb := s.onMarker
	s.mu.Unlock()
	if trigger && cb != nil {
		cb()
	}
	return len(p), nil
}

func (s *markerScanner) found() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen
}

// killAgentProcess terminates the spawned agent. Killing the direct child makes
// cmd.Wait's process-exit condition true; WaitDelay then force-closes the pipe
// if a detached build daemon still holds it, so Trigger returns promptly.
func killAgentProcess(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
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
	// Watch the live stream for the agent's completion marker. The agent prints it
	// as its final line, after which the MR/comment are already persisted remotely.
	// Some agents then leave the direct child alive — e.g. blocked on a detached
	// build daemon that inherited our stdout pipe — which would hang cmd.Wait
	// indefinitely. On the marker we kill the child so Wait returns instead of
	// waiting for a graceful exit that never comes.
	var killOnce sync.Once
	watcher := &markerScanner{onMarker: func() { killOnce.Do(func() { killAgentProcess(cmd) }) }}
	cmd.Stdout = io.MultiWriter(os.Stdout, output, watcher)
	cmd.Stderr = io.MultiWriter(os.Stderr, output, watcher)
	// A detached grandchild may still hold the pipe after the child exits; once the
	// child has exited, WaitDelay force-closes the leftover pipe so Wait returns
	// instead of blocking forever on the daemon's open write end.
	cmd.WaitDelay = 10 * time.Second
	if len(options) > 0 && options[0].WaitDelay > 0 {
		cmd.WaitDelay = options[0].WaitDelay
	}
	err = cmd.Run()
	completedAt := time.Now()

	result := ParseResultMarker(output.String())
	result.ExitCode = exitCode(err)
	// If we saw the completion marker, the agent finished successfully; a non-zero
	// exit caused by our own marker-triggered kill must not be reported as failure.
	if watcher.found() {
		err = nil
		result.ExitCode = 0
	}
	result.StartedAt = startedAt
	result.CompletedAt = completedAt
	result.DurationMillis = completedAt.Sub(startedAt).Milliseconds()
	return result, err
}
