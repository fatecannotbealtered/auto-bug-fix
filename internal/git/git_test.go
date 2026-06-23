package git_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/fatecannotbealtered/auto-bug-fix/internal/git"
)

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// setupRepo builds a repo with a base commit on the default branch and a fix
// branch adding one line to a.txt and a new b.txt. Returns the checkout dir, the
// base commit SHA, and the fix branch name.
func setupRepo(t *testing.T) (dir, baseSHA, fixBranch string) {
	t.Helper()
	dir = t.TempDir()
	gitRun(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "base")

	baseSHA, err := git.HeadSHA(dir)
	if err != nil {
		t.Fatalf("HeadSHA after base: %v", err)
	}

	fixBranch = "fix/PROJ-1-thing"
	gitRun(t, dir, "checkout", "-q", "-b", fixBranch)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "fix")
	return dir, baseSHA, fixBranch
}

func TestHeadSHA(t *testing.T) {
	dir, _, _ := setupRepo(t)
	sha, err := git.HeadSHA(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(sha) < 7 {
		t.Fatalf("expected a commit SHA, got %q", sha)
	}
}

func TestNumstat(t *testing.T) {
	dir, base, _ := setupRepo(t)
	stat, err := git.Numstat(dir, base, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if stat.Files != 2 {
		t.Fatalf("files: got %d, want 2", stat.Files)
	}
	if stat.Added != 2 {
		t.Fatalf("added: got %d, want 2", stat.Added)
	}
	if stat.Deleted != 0 {
		t.Fatalf("deleted: got %d, want 0", stat.Deleted)
	}
}

func TestNumstat_EmptyDiffIsZero(t *testing.T) {
	dir, _, _ := setupRepo(t)
	head, _ := git.HeadSHA(dir)
	stat, err := git.Numstat(dir, head, "HEAD") // same ref both sides
	if err != nil {
		t.Fatal(err)
	}
	if stat.Files != 0 {
		t.Fatalf("empty diff must yield 0 files, got %d", stat.Files)
	}
}

func TestDiffText(t *testing.T) {
	dir, base, _ := setupRepo(t)
	diff, err := git.DiffText(dir, base, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if !contains(diff, "b.txt") || !contains(diff, "world") {
		t.Fatalf("diff missing expected content:\n%s", diff)
	}
}

func TestBranchExists(t *testing.T) {
	dir, _, fixBranch := setupRepo(t)
	if !git.BranchExists(dir, fixBranch) {
		t.Fatalf("expected branch %q to exist", fixBranch)
	}
	if git.BranchExists(dir, "fix/does-not-exist") {
		t.Fatal("expected missing branch to report false")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
