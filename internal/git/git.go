// Package git wraps the few read-only git commands the harness needs to
// independently inspect an agent's local commit before authorizing a write.
// The harness deliberately holds no Jira/GitLab credentials and does not drive
// the repo workflow (clone/branch/push stay in the spawned agent); it only reads
// the diff of a checkout the agent reported, to feed the verifier and to fail a
// proposal whose claimed changes do not exist. All calls are shell:false.
package git

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// run executes a git subcommand in dir and returns trimmed stdout. A non-zero
// exit becomes an error (stderr is not captured to keep secrets out of logs).
func run(dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir}, args...)
	out, err := exec.Command("git", full...).Output() //nolint:gosec
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// HeadSHA returns the full commit SHA of HEAD in dir.
func HeadSHA(dir string) (string, error) {
	return run(dir, "rev-parse", "HEAD")
}

// BranchExists reports whether a local branch exists in dir.
func BranchExists(dir, branch string) bool {
	_, err := run(dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// DiffText returns the unified diff of base...head — the changes introduced on
// head since it diverged from base (merge-base..head), so changes made on base
// after the fork are excluded. This is the real diff the agent produced.
func DiffText(dir, base, head string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "diff", base+"..."+head).Output() //nolint:gosec
	if err != nil {
		return "", fmt.Errorf("git diff %s...%s: %w", base, head, err)
	}
	return string(out), nil
}

// DiffStat summarizes base...head. Files is the number of changed paths; Added and
// Deleted are total line counts (binary files count as a changed file but
// contribute zero line counts).
type DiffStat struct {
	Files   int
	Added   int
	Deleted int
}

// Numstat parses `git diff --numstat base...head` into a DiffStat. An empty diff
// yields the zero value (Files==0), which the guard uses to reject a proposal
// whose claimed changes are not actually present in the checkout.
func Numstat(dir, base, head string) (DiffStat, error) {
	out, err := run(dir, "diff", "--numstat", base+"..."+head)
	if err != nil {
		return DiffStat{}, err
	}
	var stat DiffStat
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		stat.Files++
		if n, err := strconv.Atoi(fields[0]); err == nil { // "-" for binary
			stat.Added += n
		}
		if n, err := strconv.Atoi(fields[1]); err == nil {
			stat.Deleted += n
		}
	}
	return stat, nil
}
