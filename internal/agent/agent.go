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

// Marker prefixes the harness recognizes on the agent's stdout. The single-phase
// RESULT marker is the terminal outcome; PROPOSAL is Phase A's "investigation done,
// here is what I would do" report (no writes yet); VERIFY is the read-only
// verifier's verdict. They are distinct prefixes on purpose: the live scanner kills
// the child on a marker, so a mid-run line sharing the RESULT prefix would kill the
// process prematurely.
const (
	ResultMarkerPrefix   = "AUTO_BUG_FIX_RESULT"
	ProposalMarkerPrefix = "AUTO_BUG_FIX_PROPOSAL"
	VerifyMarkerPrefix   = "AUTO_BUG_FIX_VERIFY"
)

// MarkerKind records which marker line ParseMarker matched, so callers can branch
// without re-parsing the prefix.
type MarkerKind string

const (
	MarkerNone     MarkerKind = ""
	MarkerResult   MarkerKind = "result"
	MarkerProposal MarkerKind = "proposal"
	MarkerVerify   MarkerKind = "verify"
)

// Known outcome values an agent reports in its AUTO_BUG_FIX_RESULT/PROPOSAL marker.
// Only auto-fix means the bug was fixed; the other two mean a human now owns it.
const (
	OutcomeAutoFix      = "auto-fix"
	OutcomeAutoDiagnose = "auto-diagnose"
	OutcomeNeedsInfo    = "needs-info"
)

// Verifier verdicts in the AUTO_BUG_FIX_VERIFY marker.
const (
	VerdictUphold = "uphold"
	VerdictRefute = "refute"
)

type Result struct {
	Outcome        string
	MRURL          string
	HandoffPath    string
	ExitCode       int
	StartedAt      time.Time
	CompletedAt    time.Time
	DurationMillis int64

	// MarkerKind is which marker line was parsed (result/proposal/verify/none).
	MarkerKind MarkerKind
	// NoMarker is set when the process finished cleanly (exit 0, possibly via a
	// post-exit WaitDelay on a leaked pipe) but never printed a completion marker.
	// It distinguishes "completed, outcome unknown" from a genuine failure so a
	// caller can send a fallback notification instead of reporting the fix failed.
	NoMarker bool
	// Phase A PROPOSAL fields — what the agent investigated and would do, before
	// any write side-effect. The harness uses these to locate and re-derive the
	// real diff for verification. They are agent-self-reported (see SECURITY notes).
	Workspace string
	Branch    string
	Base      string
	Head      string
	Evidence  string
	// Verifier VERIFY fields.
	Verdict      string
	VerifyReason string
}

type Options struct {
	Env map[string]string
	// WaitDelay overrides the post-exit pipe-close delay (default 10s). Mainly for tests.
	WaitDelay time.Duration
	// MarkerPrefixes are the stdout marker prefixes the live scanner kills on.
	// Empty means the default [RESULT] — preserving single-phase behavior. A Phase A
	// spawn passes [PROPOSAL, RESULT]; the verifier passes [VERIFY].
	MarkerPrefixes [][]byte
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

// ParseResultMarker parses only the terminal RESULT marker (single-phase / Phase B).
// Kept as a thin, behavior-stable wrapper so existing callers and tests are unaffected.
func ParseResultMarker(output string) Result {
	r := parseMarkerLine(output, []string{ResultMarkerPrefix})
	if r.MarkerKind == MarkerResult {
		return r
	}
	return Result{}
}

// ParseMarker scans output bottom-up for the last line matching any known marker
// prefix (PROPOSAL / VERIFY / RESULT) and fills the matching fields. The marker
// kind is recorded so callers can branch on it. Last-line-wins mirrors the live
// scanner's "kill on first marker" semantics.
func ParseMarker(output string) Result {
	return parseMarkerLine(output, []string{ProposalMarkerPrefix, VerifyMarkerPrefix, ResultMarkerPrefix})
}

// parsePrefixesFor maps the scanner's watched byte prefixes (nil => default
// [RESULT]) to the string prefixes the parser should accept, so a spawn only ever
// parses the markers it actually watched for — a legacy single-shot run stays
// RESULT-only and cannot be diverted by a stray PROPOSAL/VERIFY-prefixed line.
func parsePrefixesFor(prefixes [][]byte) []string {
	if len(prefixes) == 0 {
		return []string{ResultMarkerPrefix}
	}
	out := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		out = append(out, string(p))
	}
	return out
}

func parseMarkerLine(output string, prefixes []string) Result {
	var result Result
	lines := strings.Split(output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		kind, prefix := matchMarkerPrefix(line, prefixes)
		if kind == MarkerNone {
			continue
		}
		// A line with a `<...>` placeholder is a documented example, not a real
		// marker — skip it, mirroring the live scanner's kill condition.
		if strings.Contains(line, "<") {
			continue
		}
		result.MarkerKind = kind
		body := strings.TrimSpace(strings.TrimPrefix(line, prefix))
		// reason= is free-form (may contain spaces); capture it as the rest of the
		// line and parse the space-delimited key=value pairs before it. Templates put
		// reason last.
		if idx := strings.Index(body, "reason="); idx >= 0 {
			result.VerifyReason = strings.TrimSpace(body[idx+len("reason="):])
			body = strings.TrimSpace(body[:idx])
		}
		for _, field := range strings.Fields(body) {
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
			case "workspace":
				result.Workspace = value
			case "branch":
				result.Branch = value
			case "base":
				result.Base = value
			case "head":
				result.Head = value
			case "evidence":
				result.Evidence = value
			case "verdict":
				result.Verdict = value
			}
		}
		return result
	}
	return result
}

// matchMarkerPrefix returns the kind/prefix for the first allowed prefix that line
// starts with. PROPOSAL/VERIFY are checked before RESULT; none are substrings of
// each other, so order only sets precedence when a line somehow starts with two.
func matchMarkerPrefix(line string, prefixes []string) (MarkerKind, string) {
	for _, prefix := range prefixes {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		switch prefix {
		case ProposalMarkerPrefix:
			return MarkerProposal, prefix
		case VerifyMarkerPrefix:
			return MarkerVerify, prefix
		case ResultMarkerPrefix:
			return MarkerResult, prefix
		}
	}
	return MarkerNone, ""
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

var defaultMarkerPrefixes = [][]byte{[]byte(ResultMarkerPrefix)}

// markerScanner watches the live output stream for the agent's completion marker
// and invokes onMarker once when a line beginning with any watched prefix is seen.
// Detection is incremental (newline-delimited, prefix compare), so it adds
// negligible cost over the byte copy os/exec already performs. It is written
// concurrently from the stdout and stderr copy goroutines, so all state is
// mutex-guarded. prefixes==nil means the default [RESULT] (single-phase).
type markerScanner struct {
	mu       sync.Mutex
	pending  []byte
	seen     bool
	prefixes [][]byte
	onMarker func()
}

func (s *markerScanner) watchedPrefixes() [][]byte {
	if len(s.prefixes) == 0 {
		return defaultMarkerPrefixes
	}
	return s.prefixes
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
			if bytes.Contains(line, []byte("<")) {
				continue
			}
			for _, pfx := range s.watchedPrefixes() {
				if bytes.HasPrefix(line, pfx) {
					s.seen, trigger = true, true
					break
				}
			}
			if trigger {
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
	var markerPrefixes [][]byte
	if len(options) > 0 {
		markerPrefixes = options[0].MarkerPrefixes
	}
	var killOnce sync.Once
	watcher := &markerScanner{prefixes: markerPrefixes, onMarker: func() { killOnce.Do(func() { killAgentProcess(cmd) }) }}
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

	result := parseMarkerLine(output.String(), parsePrefixesFor(markerPrefixes))
	result.ExitCode = exitCode(err)
	// Classify the run into three outcomes:
	//   (a) marker seen      — the agent finished its workflow; a non-zero exit
	//       from our own marker-triggered kill must not be reported as failure.
	//   (b) no marker, clean — the process exited cleanly (exit 0, or only a
	//       leaked pipe tripped WaitDelay after a 0 exit) but never printed a
	//       marker: completed with an unknown outcome, NOT a failure. Callers use
	//       NoMarker to send a fallback notification / avoid a permanent fail.
	//   (c) no marker, error — a genuine non-zero exit or spawn error: a failure.
	// ErrWaitDelay is only returned when the process already exited with code 0 but
	// its I/O didn't finish in time, so treating it as a clean exit is safe.
	cleanExit := err == nil || errors.Is(err, exec.ErrWaitDelay)
	switch {
	case watcher.found():
		err = nil
		result.ExitCode = 0
	case cleanExit:
		err = nil
		result.ExitCode = 0
		result.NoMarker = true
	}
	result.StartedAt = startedAt
	result.CompletedAt = completedAt
	result.DurationMillis = completedAt.Sub(startedAt).Milliseconds()
	return result, err
}
