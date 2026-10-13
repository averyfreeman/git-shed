package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fixture struct{ root, local, remote string }

func mustGit(t *testing.T, path string, args ...string) string {
	t.Helper()
	s, e := git(context.Background(), path, args...)
	if e != nil {
		t.Fatal(e)
	}
	return strings.TrimSpace(s)
}
func write(t *testing.T, p, s string) {
	t.Helper()
	if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(p, []byte(s), 0600); e != nil {
		t.Fatal(e)
	}
}
func repoFixture(t *testing.T) fixture {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "Git Shed Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "Git Shed Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.invalid")
	root, err := canonical(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{root, filepath.Join(root, "local"), filepath.Join(root, "remote.git")}
	mustGit(t, root, "init", "--bare", f.remote)
	mustGit(t, root, "init", "-b", "main", f.local)
	write(t, filepath.Join(f.local, "file.txt"), "initial\n")
	write(t, filepath.Join(f.local, ".gitignore"), "cache/\n.env\n")
	mustGit(t, f.local, "add", ".")
	mustGit(t, f.local, "commit", "-m", "initial")
	mustGit(t, f.local, "remote", "add", "origin", f.remote)
	mustGit(t, f.local, "push", "-u", "origin", "main")
	return f
}
func inspectFixture(t *testing.T, f fixture) Repo {
	t.Helper()
	r := Inspect(context.Background(), f.local, true, true)
	if len(r.Errors) > 0 {
		t.Fatalf("inspection errors: %v", r.Errors)
	}
	return r
}
func contains(a []string, s string) bool {
	for _, v := range a {
		if strings.Contains(v, s) {
			return true
		}
	}
	return false
}
func TestVerificationAllLocalBranches(t *testing.T) {
	f := repoFixture(t)
	r := inspectFixture(t, f)
	if r.Status != "synchronized" || r.LocalOnly != 0 || len(r.Blockers) > 0 {
		t.Fatalf("initial: %+v", r)
	}
	mustGit(t, f.local, "switch", "-c", "unpublished")
	write(t, filepath.Join(f.local, "other.txt"), "other\n")
	mustGit(t, f.local, "add", ".")
	mustGit(t, f.local, "commit", "-m", "local branch")
	mustGit(t, f.local, "switch", "main")
	r = inspectFixture(t, f)
	if r.Status != "synchronized" || r.LocalOnly != 1 || !contains(r.Blockers, "absent from origin") {
		t.Fatalf("secondary branch must block cleanup: %+v", r)
	}
	mustGit(t, f.local, "push", "origin", "unpublished")
	r = inspectFixture(t, f)
	if r.LocalOnly != 0 || len(r.Blockers) > 0 {
		t.Fatalf("published secondary branch: %+v", r)
	}
}
func TestBranchStates(t *testing.T) {
	f := repoFixture(t)
	write(t, filepath.Join(f.local, "file.txt"), "ahead\n")
	mustGit(t, f.local, "commit", "-am", "ahead")
	r := inspectFixture(t, f)
	if r.Status != "ahead" || r.LocalOnly != 1 {
		t.Fatalf("ahead: %+v", r)
	}
	mustGit(t, f.local, "push")
	other := filepath.Join(f.root, "other")
	mustGit(t, f.root, "clone", "--branch", "main", f.remote, other)
	write(t, filepath.Join(other, "remote.txt"), "remote\n")
	mustGit(t, other, "add", ".")
	mustGit(t, other, "commit", "-m", "remote")
	mustGit(t, other, "push")
	r = inspectFixture(t, f)
	if r.Status != "behind" || r.LocalOnly != 0 || len(r.Blockers) > 0 {
		t.Fatalf("behind is recoverable: %+v", r)
	}
	write(t, filepath.Join(f.local, "local.txt"), "local\n")
	mustGit(t, f.local, "add", ".")
	mustGit(t, f.local, "commit", "-m", "local")
	r = inspectFixture(t, f)
	if r.Status != "diverged" || r.LocalOnly != 1 {
		t.Fatalf("diverged: %+v", r)
	}
}
func TestDeletedRemoteAndTags(t *testing.T) {
	f := repoFixture(t)
	mustGit(t, f.local, "tag", "-a", "v1", "-m", "tag")
	r := inspectFixture(t, f)
	if len(r.UnpublishedTags) != 1 || !contains(r.Blockers, "tags") {
		t.Fatal(r)
	}
	mustGit(t, f.local, "push", "origin", "v1")
	r = inspectFixture(t, f)
	if len(r.Blockers) > 0 {
		t.Fatal(r.Blockers)
	}
	mustGit(t, f.local, "push", "origin", ":main", ":v1")
	r = inspectFixture(t, f)
	if r.Status != "missing_remote_branch" || r.LocalOnly != 1 || len(r.UnpublishedTags) != 1 {
		t.Fatalf("cached refs must not approve deleted server refs: %+v", r)
	}
}
func TestDirtyStashesDetachedAndNoOrigin(t *testing.T) {
	f := repoFixture(t)
	write(t, filepath.Join(f.local, "file.txt"), "dirty\n")
	write(t, filepath.Join(f.local, "untracked.txt"), "local\n")
	write(t, filepath.Join(f.local, "cache", "output"), "cache\n")
	r := inspectFixture(t, f)
	if len(r.Changes) != 1 || len(r.Untracked) != 1 || !contains(r.Ignored, "cache/") {
		t.Fatalf("file classification: %+v", r)
	}
	mustGit(t, f.local, "stash", "push", "-u")
	r = inspectFixture(t, f)
	if r.Stashes != 1 || !contains(r.Blockers, "stashes") {
		t.Fatal(r)
	}
	mustGit(t, f.local, "stash", "drop")
	mustGit(t, f.local, "switch", "--detach")
	r = inspectFixture(t, f)
	if r.Status != "detached" || r.LocalOnly != 0 {
		t.Fatal(r)
	}
	mustGit(t, f.local, "remote", "remove", "origin")
	r = Inspect(context.Background(), f.local, false, false)
	if r.Status != "no_origin" {
		t.Fatal(r)
	}
}
func TestDiscoveryNestedInvalidSymlinkAndDuplicate(t *testing.T) {
	f := repoFixture(t)
	nested := filepath.Join(f.local, "nested")
	mustGit(t, f.root, "clone", "--branch", "main", f.remote, nested)
	invalid := filepath.Join(f.root, "broken")
	write(t, filepath.Join(invalid, ".git"), "gitdir: nowhere\n")
	if e := os.Symlink(f.local, filepath.Join(f.root, "link")); e != nil {
		t.Fatal(e)
	}
	report, e := Scan(context.Background(), ScanOptions{Root: f.root, Workers: 2, Sizes: true})
	if e != nil {
		t.Fatal(e)
	}
	if len(report.Repositories) != 2 || len(report.Invalid) != 1 || len(report.Duplicates) != 1 {
		t.Fatalf("discovery: %+v", report)
	}
	for _, r := range report.Repositories {
		if r.Path == f.local && !contains(r.Blockers, "nested") {
			t.Fatal("parent must be protected")
		}
	}
	size, alloc, e := treeSize(context.Background(), f.local)
	if e != nil {
		t.Fatal(e)
	}
	if report.Summary["checkout_bytes"] != size || report.Summary["checkout_allocated_bytes"] != alloc {
		t.Fatal("nested sizes counted twice")
	}
}
func TestRemoteFailureAndFingerprint(t *testing.T) {
	f := repoFixture(t)
	verified := inspectFixture(t, f)
	cached := Inspect(context.Background(), f.local, false, false)
	if cached.Fingerprint != verified.Fingerprint {
		t.Fatal("cached and verified metadata differ in local fingerprint")
	}
	mustGit(t, f.local, "remote", "set-url", "origin", filepath.Join(f.root, "missing.git"))
	r := Inspect(context.Background(), f.local, false, true)
	if r.Verification != "failed" || len(r.Blockers) == 0 {
		t.Fatal(r)
	}
}
func TestRecoveryAndWorktreeAndLFS(t *testing.T) {
	f := repoFixture(t)
	write(t, filepath.Join(f.local, "file.txt"), "recovery\n")
	mustGit(t, f.local, "commit", "-am", "recovery")
	mustGit(t, f.local, "reset", "--hard", "HEAD~1")
	r := inspectFixture(t, f)
	if r.RecoveryCommits != 1 {
		t.Fatalf("reflog recovery: %d", r.RecoveryCommits)
	}
	wt := filepath.Join(f.root, "worktree")
	mustGit(t, f.local, "worktree", "add", "--detach", wt)
	r = inspectFixture(t, f)
	if !contains(r.Blockers, "linked worktrees") {
		t.Fatal(r)
	}
	linked := Inspect(context.Background(), wt, false, false)
	if linked.Kind != "linked_or_submodule" {
		t.Fatal(linked)
	}
	mustGit(t, f.local, "worktree", "remove", wt)
	mustGit(t, f.local, "config", "filter.lfs.process", "a-global-lfs-filter")
	r = inspectFixture(t, f)
	if contains(r.Blockers, "LFS") {
		t.Fatal("global filter config without LFS files must not block every repo")
	}
	write(t, filepath.Join(f.local, ".gitattributes"), "*.txt filter=lfs\n")
	r = Inspect(context.Background(), f.local, false, false)
	if !contains(r.Blockers, "LFS") {
		t.Fatal("LFS attributes must block cleanup")
	}
}
func TestReportFormatsRedactCredentials(t *testing.T) {
	r := Report{Repositories: []Repo{{Path: "/tmp/example", Origin: "https://user:secret@github.com/org/repo.git", PushURLs: []string{"https://token@github.com/org/repo.git"}}}, Summary: map[string]int64{"repositories": 1}, Duplicates: map[string][]string{normalizeOrigin("https://user:secret@github.com/org/repo.git"): {"/tmp/example", "/tmp/copy"}}}
	for _, format := range []string{"table", "json", "csv", "markdown"} {
		var b strings.Builder
		if e := writeReport(&b, r, format); e != nil {
			t.Fatal(e)
		}
		if strings.Contains(b.String(), "secret") || strings.Contains(b.String(), "token@") {
			t.Fatal("credentials leaked in " + format)
		}
		if format == "json" {
			var decoded Report
			if e := json.Unmarshal([]byte(b.String()), &decoded); e != nil {
				t.Fatal(e)
			}
		}
	}
	if !strings.Contains(r.Repositories[0].Origin, "secret") {
		t.Fatal("redaction mutated action targets")
	}
}
func TestSkipWorktreeAndWeirdFilenames(t *testing.T) {
	f := repoFixture(t)
	mustGit(t, f.local, "update-index", "--assume-unchanged", "file.txt")
	write(t, filepath.Join(f.local, "file.txt"), "hidden change\n")
	r := Inspect(context.Background(), f.local, false, true)
	if !contains(r.Blockers, "assume-unchanged") {
		t.Fatal("hidden worktree changes must block cleanup")
	}
	mustGit(t, f.local, "update-index", "--no-assume-unchanged", "file.txt")
	mustGit(t, f.local, "restore", "file.txt")
	name := "unusual\ncomma, tab\t.txt"
	write(t, filepath.Join(f.local, name), "weird\n")
	r = Inspect(context.Background(), f.local, false, false)
	if len(r.Untracked) != 1 || r.Untracked[0] != name {
		t.Fatalf("filename split: %q", r.Untracked)
	}
	a := autoActions()
	a.Files = []string{name}
	a.Message = "unusual name"
	if e := CommitPush(context.Background(), r, a); e != nil {
		t.Fatal(e)
	}
}
func TestCaseDistinctRemoteRefsAndShallowProtection(t *testing.T) {
	f := repoFixture(t)
	caseRemote := filepath.Join(f.root, "case.git")
	if _, e := git(context.Background(), f.root, "init", "--bare", "--ref-format=reftable", caseRemote); e == nil {
		mustGit(t, f.local, "push", caseRemote, "main:main", "main:Main")
		mustGit(t, f.local, "remote", "set-url", "origin", caseRemote)
		r := inspectFixture(t, f)
		if r.Verification != "verified" || len(r.Blockers) > 0 {
			t.Fatal(r)
		}
	}
	shallow := filepath.Join(f.root, "shallow")
	mustGit(t, f.root, "clone", "--depth=1", "--branch", "main", "file://"+f.remote, shallow)
	r := Inspect(context.Background(), shallow, false, true)
	if r.Verification != "refs_only" || !contains(r.Blockers, "shallow") || r.LocalOnly != 0 {
		t.Fatal(r)
	}
}
func TestBranchTagNameCollision(t *testing.T) {
	f := repoFixture(t)
	mustGit(t, f.local, "tag", "main")
	r := inspectFixture(t, f)
	if r.Branch != "main" || len(r.Branches) != 1 || r.Branches[0].Name != "main" {
		t.Fatal(r)
	}
	a := autoActions()
	a.Branches = []string{"main"}
	a.Tags = []string{"main"}
	if e := Push(context.Background(), r, a); e != nil {
		t.Fatal(e)
	}
}
